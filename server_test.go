package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const tag = "support-routing"
const experimentPath = "/api/v1/experiments/" + tag
const configURL = "/api/v1/config?tag=" + tag

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := openStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func testAPI(t *testing.T, demo bool) (*Store, http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	s := openTestStore(t, path)
	if demo {
		if err := seedDemo(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	return s, newHandler(s), path
}

func record(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func invoke(t *testing.T, h http.Handler, want int, method, path, body string) string {
	t.Helper()
	response := record(h, method, path, body)
	if response.Code != want {
		t.Fatalf("%s %s: status %d, want %d\n%s", method, path, response.Code, want, response.Body.String())
	}
	return response.Body.String()
}

func decodeTest[T any](t *testing.T, raw string) T {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("invalid response: %v\n%s", err, raw)
	}
	return value
}

func marshalTest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSchemaAndAssetVersionValidation(t *testing.T) {
	_, h, _ := testAPI(t, false)

	invoke(t, h, 422, "POST", "/api/v1/schemas", `{"key":"remote","name":"Remote","kind":"agent","definition":{"$ref":"file:///tmp/schema.json"}}`)
	invoke(t, h, 201, "POST", "/api/v1/schemas", `{"key":"chat","name":"Chat","kind":"agent","definition":{"type":"object","required":["model"],"properties":{"model":{"$ref":"#/$defs/name"}},"$defs":{"name":{"type":"string"}}}}`)
	invoke(t, h, 201, "POST", "/api/v1/schemas", `{"key":"search","name":"Search","kind":"skill","definition":{"type":"object"}}`)
	invoke(t, h, 201, "POST", "/api/v1/schemas/chat/versions", `{"baseVersion":1,"definition":{"type":"object","required":["model","limit"],"properties":{"limit":{"type":"integer","minimum":1}}}}`)
	invoke(t, h, 409, "POST", "/api/v1/schemas/chat/versions", `{"baseVersion":1,"definition":{"type":"object"}}`)

	rejected := invoke(t, h, 422, "POST", "/api/v1/assets", `{"key":"bot","name":"Bot","schema":{"key":"chat","version":1},"config":{"temperature":0}}`)
	if !strings.Contains(rejected, "validation_failed") || !strings.Contains(rejected, "model") {
		t.Fatalf("missing validation details: %s", rejected)
	}
	if got := decodeTest[[]Asset](t, invoke(t, h, 200, "GET", "/api/v1/assets", "")); len(got) != 0 {
		t.Fatalf("failed import created an asset: %+v", got)
	}
	invoke(t, h, 201, "POST", "/api/v1/assets", `{"key":"bot","name":"Bot","schema":{"key":"chat","version":1},"config":{"model":"example","id":9007199254740993}}`)
	invoke(t, h, 422, "POST", "/api/v1/assets/bot/versions", `{"baseVersion":1,"schema":{"key":"search","version":1},"config":{}}`)
	invoke(t, h, 422, "POST", "/api/v1/assets/bot/versions", `{"baseVersion":1,"schema":{"key":"chat","version":2},"config":{"model":"example","limit":0}}`)
	invoke(t, h, 201, "POST", "/api/v1/assets/bot/versions", `{"baseVersion":1,"schema":{"key":"chat","version":2},"config":{"model":"example","limit":2}}`)
	invoke(t, h, 409, "POST", "/api/v1/assets/bot/versions", `{"baseVersion":1,"schema":{"key":"chat","version":2},"config":{"model":"example","limit":2}}`)
	body := invoke(t, h, 200, "GET", "/api/v1/assets/bot/versions", "")
	versions := decodeTest[[]AssetVersion](t, body)
	if len(versions) != 2 || versions[0].Schema.Version != 2 || versions[1].Schema.Version != 1 ||
		!strings.Contains(string(versions[1].Config), "9007199254740993") {
		t.Fatalf("asset version lost its original JSON or schema reference: %s", body)
	}
}

func TestReleaseReadbackAndRollback(t *testing.T) {
	s, h, dbPath := testAPI(t, true)
	firstRaw := invoke(t, h, 200, "GET", configURL, "")
	first := decodeTest[Config](t, firstRaw)
	assertConfig(t, first, tag+"@1", 3, 4)
	invoke(t, h, 201, "POST", "/api/v1/assets/knowledge-search/versions",
		`{"baseVersion":4,"schema":{"key":"search-skill","version":1},"config":{"topK":6}}`)

	detail := decodeTest[ExperimentDetail](t, invoke(t, h, 200, "GET", experimentPath, ""))
	revision := detail.Revision
	detail.Agents[0].Dependencies[0].Version = 99
	invoke(t, h, 422, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{ExpectedRevision: &revision, Agents: detail.Agents}))
	detail.Agents[0].Dependencies[0] = Ref{Slot: "knowledge", AssetKey: "support-agent", Version: 1}
	invoke(t, h, 422, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{ExpectedRevision: &revision, Agents: detail.Agents}))
	detail.Agents[0].Dependencies[0] = Ref{Slot: "knowledge", AssetKey: "knowledge-search", Version: 5}
	draft := decodeTest[Draft](t, invoke(t, h, 200, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{ExpectedRevision: &revision, Agents: detail.Agents})))
	invoke(t, h, 409, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{ExpectedRevision: &revision, Agents: detail.Agents}))
	preview := decodeTest[Config](t, invoke(t, h, 200, "GET", experimentPath+"/preview", ""))
	assertConfig(t, preview, "", 5, 4)
	if preview.DraftRevision != draft.Revision {
		t.Fatalf("preview revision is %d, want %d", preview.DraftRevision, draft.Revision)
	}
	if got := invoke(t, h, 200, "GET", configURL, ""); got != firstRaw {
		t.Fatal("asset version or draft changes altered the published bytes")
	}
	invoke(t, h, 409, "POST", experimentPath+"/releases", `{"expectedRevision":2,"expectedActiveReleaseId":null}`)

	input := `{"expectedRevision":2,"expectedActiveReleaseId":"support-routing@1"}`
	if _, err := s.db.Exec(`CREATE TRIGGER stop_publish BEFORE UPDATE ON active_releases
		BEGIN SELECT RAISE(ABORT, 'simulated activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	invoke(t, h, 500, "POST", experimentPath+"/releases", input)
	afterFailure := decodeTest[ExperimentDetail](t, invoke(t, h, 200, "GET", experimentPath, ""))
	if len(afterFailure.Releases) != 1 || *afterFailure.ActiveReleaseID != first.ReleaseID {
		t.Fatal("failed publication changed the active pointer or left a partial release")
	}
	if _, err := s.db.Exec("DROP TRIGGER stop_publish"); err != nil {
		t.Fatal(err)
	}

	secondRaw := invoke(t, h, 201, "POST", experimentPath+"/releases", input)
	assertConfig(t, decodeTest[Config](t, secondRaw), tag+"@2", 5, 4)
	if got := invoke(t, h, 200, "GET", configURL+"&releaseId=support-routing%401", ""); got != firstRaw {
		t.Fatal("historical release changed")
	}
	invoke(t, h, 404, "GET", configURL+"&releaseId=other%401", "")
	invoke(t, h, 404, "GET", configURL+"&releaseId=support-routing%4099", "")
	invoke(t, h, 409, "PUT", experimentPath+"/active-release", `{"releaseId":"support-routing@1","expectedActiveReleaseId":"support-routing@1"}`)
	invoke(t, h, 200, "PUT", experimentPath+"/active-release", `{"releaseId":"support-routing@1","expectedActiveReleaseId":"support-routing@2"}`)
	if got := invoke(t, h, 201, "POST", experimentPath+"/releases", input); got != secondRaw {
		t.Fatal("retry did not return the original publication")
	}
	if got := invoke(t, h, 200, "GET", configURL, ""); got != firstRaw {
		t.Fatal("retry reactivated the rolled-back publication")
	}

	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dbPath)
	if err := seedDemo(context.Background(), reopened); err != nil {
		t.Fatalf("repeat demo initialization: %v", err)
	}
	persisted := newHandler(reopened)
	if got := invoke(t, persisted, 200, "GET", configURL, ""); got != firstRaw {
		t.Fatal("application restart or demo initialization changed the active release")
	}
	if got := invoke(t, persisted, 200, "GET", configURL+"&releaseId=support-routing%402", ""); got != secondRaw {
		t.Fatal("second release did not survive restart")
	}
}

func TestConcurrentDraftAndRuntimeReads(t *testing.T) {
	_, writer, path := testAPI(t, true)
	reader := newHandler(openTestStore(t, path))
	first := invoke(t, reader, 200, "GET", configURL, "")
	detail := decodeTest[ExperimentDetail](t, invoke(t, writer, 200, "GET", experimentPath, ""))
	detail.Agents[0].Dependencies[0].Version = 4
	detail.Agents[1].Dependencies[0].Version = 3
	input := marshalTest(t, DraftInput{ExpectedRevision: &detail.Revision, Agents: detail.Agents})
	statuses := make(chan int, 2)
	go func() { statuses <- record(writer, "PUT", experimentPath+"/draft", input).Code }()
	go func() { statuses <- record(reader, "PUT", experimentPath+"/draft", input).Code }()
	a, b := <-statuses, <-statuses
	if !(a == 200 && b == 409 || a == 409 && b == 200) {
		t.Fatalf("expected one successful draft write and one conflict, got %d and %d", a, b)
	}

	var group sync.WaitGroup
	samples := make(chan string, 24)
	start := make(chan struct{})
	for range cap(samples) {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response := record(reader, "GET", configURL, "")
			if response.Code != 200 {
				t.Errorf("runtime read: %s", response.Body.String())
			}
			samples <- response.Body.String()
		}()
	}
	close(start)
	second := invoke(t, writer, 201, "POST", experimentPath+"/releases", `{"expectedRevision":2,"expectedActiveReleaseId":"support-routing@1"}`)
	assertConfig(t, decodeTest[Config](t, second), tag+"@2", 4, 3)
	group.Wait()
	close(samples)
	for sample := range samples {
		if sample != first && sample != second {
			t.Fatalf("reader received a partial release: %s", sample)
		}
	}
}

func assertConfig(t *testing.T, config Config, release string, first, second int64) {
	t.Helper()
	if config.ReleaseID != release || len(config.Agents) != 2 ||
		len(config.Agents[0].Skills) != 1 || len(config.Agents[1].Skills) != 2 ||
		config.Agents[0].Skills[0].Version != first || config.Agents[1].Skills[0].Version != second ||
		len(config.Agents[0].Prompts) != 1 || config.Agents[0].Prompts[0].AssetKey != "intake-prompt" {
		t.Fatalf("unexpected experiment snapshot: %+v", config)
	}
}

func TestUnpublishedAndInvalidRequests(t *testing.T) {
	_, h, _ := testAPI(t, false)
	invoke(t, h, 201, "POST", "/api/v1/experiments", `{"tag":"empty","name":"Empty"}`)
	invoke(t, h, 404, "GET", "/api/v1/config?tag=empty", "")
	invoke(t, h, 400, "GET", "/api/v1/config", "")
	invoke(t, h, 400, "POST", "/api/v1/experiments/empty/releases", `{"expectedRevision":0}`)
	invoke(t, h, 422, "POST", "/api/v1/experiments/empty/releases", `{"expectedRevision":0,"expectedActiveReleaseId":null}`)
	invoke(t, h, 400, "POST", "/api/v1/experiments", `{"tag":"new","name":"New"} {}`)

	request := httptest.NewRequest("POST", "http://localhost/api/v1/experiments", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 415 {
		t.Fatalf("non-JSON request: status %d", response.Code)
	}
	request = httptest.NewRequest("GET", "http://untrusted.example/api/v1/experiments", nil)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("external host header: status %d", response.Code)
	}
}

func TestSchemaCompositionPinsNestedAssetVersions(t *testing.T) {
	_, h, _ := testAPI(t, true)
	const path = "/api/v1/experiments/schema-routing"
	const read = "/api/v1/config?tag=schema-routing"
	firstRaw := invoke(t, h, 200, "GET", read, "")
	assertNestedSkills(t, firstRaw, 3, 4)
	invoke(t, h, 422, "POST", "/api/v1/schemas",
		`{"key":"invalid-pick","name":"Invalid pick","kind":"experiment","definition":{"type":"object","properties":{"bad":{"x-asset":"experiment"}}}}`)
	invoke(t, h, 422, "POST", "/api/v1/assets",
		`{"key":"bad-kind","name":"Invalid asset","schema":{"key":"routing-profile","version":1},"config":{}}`)
	invoke(t, h, 422, "PUT", path+"/draft", `{"expectedRevision":1,"agents":[]}`)
	bad := invoke(t, h, 422, "PUT", path+"/draft",
		`{"expectedRevision":1,"schema":{"key":"routing-profile","version":1},"config":{"primary":{"assetKey":"schema-support-agent","version":1},"candidates":[{"assetKey":"knowledge-search","version":3}],"requestTimeoutMs":100}}`)
	if !strings.Contains(bad, "/candidates/0 expects agent") {
		t.Fatalf("validation did not identify the incorrect asset path: %s", bad)
	}
	invoke(t, h, 201, "POST", "/api/v1/assets/knowledge-search/versions",
		`{"baseVersion":4,"schema":{"key":"search-skill","version":1},"config":{"topK":8}}`)
	invoke(t, h, 422, "POST", "/api/v1/assets/schema-support-agent/versions",
		`{"baseVersion":1,"schema":{"key":"schema-agent","version":1},"config":{"model":"example","tools":{"skills":[{"assetKey":"schema-dispatch-agent","version":1}]}}}`)
	invoke(t, h, 201, "POST", "/api/v1/assets/schema-support-agent/versions",
		`{"baseVersion":1,"schema":{"key":"schema-agent","version":1},"config":{"model":"example","tools":{"skills":[{"assetKey":"knowledge-search","version":5}]}}}`)
	draft := invoke(t, h, 200, "PUT", path+"/draft",
		`{"expectedRevision":1,"schema":{"key":"routing-profile","version":1},"config":{"primary":{"assetKey":"schema-support-agent","version":2},"candidates":[{"assetKey":"schema-dispatch-agent","version":1}],"requestTimeoutMs":3000}}`)
	if !strings.Contains(draft, `"primary":{"assetKey":"schema-support-agent","version":2}`) {
		t.Fatalf("draft should store only the reference: %s", draft)
	}
	assertNestedSkills(t, invoke(t, h, 200, "GET", path+"/preview", ""), 5, 4)
	if got := invoke(t, h, 200, "GET", read, ""); got != firstRaw {
		t.Fatal("editing schema-driven assets and the draft modified the published release")
	}
	second := invoke(t, h, 201, "POST", path+"/releases",
		`{"expectedRevision":2,"expectedActiveReleaseId":"schema-routing@1"}`)
	assertNestedSkills(t, second, 5, 4)
	if got := invoke(t, h, 200, "GET", read+"&releaseId=schema-routing%401", ""); got != firstRaw {
		t.Fatal("old schema-driven release changed")
	}
	invoke(t, h, 200, "PUT", path+"/active-release",
		`{"releaseId":"schema-routing@1","expectedActiveReleaseId":"schema-routing@2"}`)
	if got := invoke(t, h, 200, "GET", read, ""); got != firstRaw {
		t.Fatal("activating the old schema-driven release did not restore it")
	}
}

func assertNestedSkills(t *testing.T, raw string, primary, candidate int64) {
	t.Helper()
	type asset struct {
		Kind   string `json:"kind"`
		Config struct {
			Tools struct {
				Skills []Ref `json:"skills"`
			} `json:"tools"`
		} `json:"config"`
	}
	value := decodeTest[struct {
		Schema SchemaRef `json:"schema"`
		Config struct {
			Primary    asset   `json:"primary"`
			Candidates []asset `json:"candidates"`
			Timeout    int     `json:"requestTimeoutMs"`
		} `json:"config"`
	}](t, raw)
	if value.Schema.Key != "routing-profile" || value.Config.Primary.Kind != "agent" ||
		len(value.Config.Candidates) != 1 || value.Config.Timeout != 3000 {
		t.Fatalf("the response did not keep the schema-defined shape: %s", raw)
	}
	first := value.Config.Primary.Config.Tools.Skills
	second := value.Config.Candidates[0].Config.Tools.Skills
	if len(first) != 1 || len(second) < 1 || first[0].Version != primary || second[0].Version != candidate {
		t.Fatalf("nested references were not expanded at the pinned versions: %s", raw)
	}
}

func TestMigrateLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, _ := strings.Cut(databaseSchema, "CREATE TABLE IF NOT EXISTS schema_drafts")
	legacy = strings.Replace(legacy, "'agent', 'skill', 'prompt', 'experiment'", "'agent', 'skill', 'prompt'", 1)
	if _, err := db.Exec(legacy + `
		INSERT INTO schemas VALUES ('old-schema', 'Old schema', 'agent');
		INSERT INTO schema_versions VALUES ('old-schema', 1, '{"type":"object"}', '2026-01-01T00:00:00Z');
		INSERT INTO assets VALUES ('old-agent', 'Old agent', 'agent');
		INSERT INTO asset_versions VALUES ('old-agent', 1, 'old-schema', 1, '{}', '2026-01-01T00:00:00Z');
		INSERT INTO experiments VALUES ('old-tag', 'Old experiment', 1);
		INSERT INTO draft_agents VALUES ('old-tag', 'primary', 'old-agent', 1, 0);
		INSERT INTO releases VALUES ('old-tag', 1, 1, '2026-01-01T00:00:00Z', '{"tag":"old-tag","releaseId":"old-tag@1","agents":[]}');
		INSERT INTO active_releases VALUES ('old-tag', 1);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, path)
	handler := newHandler(store)
	original := invoke(t, handler, 200, "GET", "/api/v1/config?tag=old-tag", "")
	if original != "{\"tag\":\"old-tag\",\"releaseId\":\"old-tag@1\",\"agents\":[]}\n" {
		t.Fatalf("legacy release was changed: %s", original)
	}
	invoke(t, handler, 201, "POST", "/api/v1/schemas",
		`{"key":"new-shape","name":"New shape","kind":"experiment","definition":{"type":"object","required":["custom"],"properties":{"custom":{"x-asset":"agent"}}}}`)
	invoke(t, handler, 201, "POST", "/api/v1/experiments",
		`{"tag":"new-tag","name":"New experiment","schema":{"key":"new-shape","version":1}}`)
	invoke(t, handler, 200, "PUT", "/api/v1/experiments/new-tag/draft",
		`{"expectedRevision":0,"schema":{"key":"new-shape","version":1},"config":{"custom":{"assetKey":"old-agent","version":1}}}`)
	var enabled int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys disabled after migration, enabled=%d error=%v", enabled, err)
	}
}
