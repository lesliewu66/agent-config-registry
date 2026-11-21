package main

import (
	"context"
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
	invoke(t, h, 422, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{&revision, detail.Agents}))
	detail.Agents[0].Dependencies[0] = Ref{Slot: "knowledge", AssetKey: "support-agent", Version: 1}
	invoke(t, h, 422, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{&revision, detail.Agents}))
	detail.Agents[0].Dependencies[0] = Ref{Slot: "knowledge", AssetKey: "knowledge-search", Version: 5}
	draft := decodeTest[Draft](t, invoke(t, h, 200, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{&revision, detail.Agents})))
	invoke(t, h, 409, "PUT", experimentPath+"/draft", marshalTest(t, DraftInput{&revision, detail.Agents}))
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
	input := marshalTest(t, DraftInput{&detail.Revision, detail.Agents})
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
