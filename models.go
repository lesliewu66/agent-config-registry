package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Schema struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	LatestVersion int64  `json:"latestVersion"`
}

type NewSchema struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Definition json.RawMessage `json:"definition"`
}

type SchemaVersion struct {
	Version    int64           `json:"version"`
	Definition json.RawMessage `json:"definition"`
	CreatedAt  string          `json:"createdAt"`
}

type SchemaVersionInput struct {
	BaseVersion int64           `json:"baseVersion"`
	Definition  json.RawMessage `json:"definition"`
}

type SchemaRef struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
}

type Asset struct {
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Schema        SchemaRef `json:"schema"`
	Kind          string    `json:"kind"`
	LatestVersion int64     `json:"latestVersion"`
}

type NewAsset struct {
	Key    string          `json:"key"`
	Name   string          `json:"name"`
	Schema SchemaRef       `json:"schema"`
	Config json.RawMessage `json:"config"`
}

type AssetVersion struct {
	Schema    SchemaRef       `json:"schema"`
	Version   int64           `json:"version"`
	Config    json.RawMessage `json:"config"`
	CreatedAt string          `json:"createdAt"`
}

type VersionInput struct {
	Schema      SchemaRef       `json:"schema"`
	BaseVersion int64           `json:"baseVersion"`
	Config      json.RawMessage `json:"config"`
}

type Ref struct {
	Slot     string `json:"slot"`
	AssetKey string `json:"assetKey"`
	Version  int64  `json:"version"`
}

type AgentRef struct {
	Ref
	Dependencies []Ref `json:"dependencies"`
}

type Draft struct {
	Revision int64      `json:"revision"`
	Agents   []AgentRef `json:"agents"`
}

type DraftInput struct {
	ExpectedRevision *int64     `json:"expectedRevision"`
	Agents           []AgentRef `json:"agents"`
}

type PublishInput struct {
	ExpectedRevision        *int64          `json:"expectedRevision"`
	ExpectedActiveReleaseID json.RawMessage `json:"expectedActiveReleaseId"`
}

type ActivateInput struct {
	ReleaseID               string          `json:"releaseId"`
	ExpectedActiveReleaseID json.RawMessage `json:"expectedActiveReleaseId"`
}

type Experiment struct {
	Tag             string  `json:"tag"`
	Name            string  `json:"name"`
	Revision        int64   `json:"revision"`
	ActiveReleaseID *string `json:"activeReleaseId"`
	AgentCount      int     `json:"agentCount"`
}

type ReleaseMeta struct {
	ReleaseNumber int64  `json:"releaseNumber,omitempty"`
	ReleaseID     string `json:"releaseId,omitempty"`
	DraftRevision int64  `json:"draftRevision"`
	PublishedAt   string `json:"publishedAt,omitempty"`
}

type ExperimentDetail struct {
	Experiment
	Agents   []AgentRef    `json:"agents"`
	Releases []ReleaseMeta `json:"releases"`
}

type ResolvedAsset struct {
	Ref
	Name   string          `json:"name"`
	Schema SchemaRef       `json:"schema"`
	Config json.RawMessage `json:"config"`
}

type ResolvedAgent struct {
	ResolvedAsset
	Skills  []ResolvedAsset `json:"skills"`
	Prompts []ResolvedAsset `json:"prompts"`
}

type Config struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
	ReleaseMeta
	Agents []ResolvedAgent `json:"agents"`
}

type requestError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *requestError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &requestError{400, "invalid_request", fmt.Sprintf(format, args...)}
}

func missing(format string, args ...any) error {
	return &requestError{404, "not_found", fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &requestError{409, "version_conflict", fmt.Sprintf(format, args...)}
}

func unprocessable(format string, args ...any) error {
	return &requestError{422, "validation_failed", fmt.Sprintf(format, args...)}
}

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func checkKey(label, key string) error {
	if !keyPattern.MatchString(key) {
		return invalid("%s must contain 1–64 lowercase letters, digits, underscores or hyphens, starting with a letter or digit", label)
	}
	return nil
}

func checkName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return invalid("name must contain 1–200 UTF-8 bytes")
	}
	return nil
}

func checkKind(kind string) error {
	switch kind {
	case "agent", "skill", "prompt":
		return nil
	default:
		return invalid("kind must be agent, skill or prompt")
	}
}

const schemaURL = "https://schemas.agent-config.invalid/schema.json"

type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are disabled: %s", url)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, unprocessable("invalid JSON Schema: %v", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, unprocessable("schema definition must be a JSON object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(offlineLoader{})
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, unprocessable("invalid JSON Schema: %v", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, unprocessable("invalid JSON Schema: %v", err)
	}
	return schema, nil
}

func validateConfig(raw json.RawMessage, definition json.RawMessage) (string, error) {
	if len(raw) > 128<<10 {
		return "", unprocessable("asset config exceeds 128 KiB")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return "", unprocessable("invalid config JSON: %v", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return "", unprocessable("config must be a JSON object")
	}
	schema, err := compileSchema(definition)
	if err != nil {
		return "", fmt.Errorf("stored schema failed to compile: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		var validation *jsonschema.ValidationError
		if errors.As(err, &validation) {
			return "", unprocessable("config does not match its schema: %v", validation)
		}
		return "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func checkDraft(draft Draft) error {
	if draft.Revision < 0 || draft.Agents == nil {
		return unprocessable("draft requires a nonnegative revision and an agents array")
	}
	if len(draft.Agents) > 24 {
		return unprocessable("an experiment can have at most 24 agents")
	}
	agentSlots := map[string]bool{}
	totalRefs := len(draft.Agents)
	for _, agent := range draft.Agents {
		if err := checkRef(agent.Ref); err != nil {
			return err
		}
		if agentSlots[agent.Slot] {
			return unprocessable("duplicate agent slot %q", agent.Slot)
		}
		agentSlots[agent.Slot] = true
		totalRefs += len(agent.Dependencies)
		if totalRefs > 120 {
			return unprocessable("an experiment can have at most 120 asset references")
		}
		slots := map[string]bool{}
		for _, dependency := range agent.Dependencies {
			if err := checkRef(dependency); err != nil {
				return err
			}
			if slots[dependency.Slot] {
				return unprocessable("duplicate dependency slot %q on agent %q", dependency.Slot, agent.Slot)
			}
			slots[dependency.Slot] = true
		}
	}
	return nil
}

func checkRef(ref Ref) error {
	if err := checkKey("slot", ref.Slot); err != nil {
		return err
	}
	if err := checkKey("assetKey", ref.AssetKey); err != nil {
		return err
	}
	if ref.Version < 1 {
		return unprocessable("reference %q requires a positive version", ref.Slot)
	}
	return nil
}
