package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func attachSchemaDraft(ctx context.Context, tx *sql.Tx, tag string, schema SchemaRef) error {
	_, kind, err := schemaDefinition(ctx, tx, schema)
	if err != nil {
		return err
	}
	if kind != "experiment" {
		return unprocessable("experiment groups require a schema of kind experiment")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO schema_drafts(tag, schema_key, schema_version) VALUES (?, ?, ?)",
		tag, schema.Key, schema.Version)
	return err
}

func readSchemaDraft(ctx context.Context, tx *sql.Tx, tag string) (*SchemaRef, json.RawMessage, error) {
	var schema SchemaRef
	var config sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT schema_key, schema_version, config FROM schema_drafts WHERE tag = ?", tag).
		Scan(&schema.Key, &schema.Version, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	return &schema, json.RawMessage(config.String), err
}

func saveSchemaDraft(ctx context.Context, tx *sql.Tx, tag string, expected int64, schema SchemaRef, input json.RawMessage) (Draft, error) {
	item, err := experiment(ctx, tx, tag)
	if err != nil {
		return Draft{}, err
	}
	if item.Revision != expected {
		return Draft{}, conflict("draft revision is %d; reload before saving", item.Revision)
	}
	current, _, err := readSchemaDraft(ctx, tx, tag)
	if err != nil {
		return Draft{}, err
	}
	if current == nil {
		return Draft{}, unprocessable("this experiment uses agent bindings")
	}
	if current.Key != schema.Key {
		return Draft{}, unprocessable("an experiment cannot change its schema key")
	}
	definition, _, err := schemaDefinition(ctx, tx, schema)
	if err != nil {
		return Draft{}, err
	}
	config, err := validateConfig(input, definition)
	if err != nil {
		return Draft{}, err
	}
	if _, err := newResolver(ctx, tx).transform(json.RawMessage(config), definition, false); err != nil {
		return Draft{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_drafts SET schema_version = ?, config = ? WHERE tag = ?",
		schema.Version, config, tag); err != nil {
		return Draft{}, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE experiments SET revision = revision + 1 WHERE tag = ?", tag)
	return Draft{Revision: expected + 1, Agents: []AgentRef{}, Schema: &schema, Config: json.RawMessage(config)}, err
}

func acceptedKinds(annotation any) ([]string, bool) {
	var values []any
	switch value := annotation.(type) {
	case string:
		values = []any{value}
	case []any:
		values = value
	}
	kinds := make([]string, 0, len(values))
	for _, value := range values {
		kind, ok := value.(string)
		if !ok || (kind != "agent" && kind != "skill" && kind != "prompt") || slices.Contains(kinds, kind) {
			return nil, false
		}
		kinds = append(kinds, kind)
	}
	return kinds, len(kinds) > 0
}

func checkAssetAnnotations(schema map[string]any, path string) error {
	return inspectAnnotations(schema, path, true)
}

func inspectAnnotations(schema map[string]any, path string, allowed bool) error {
	if value, exists := schema["x-asset"]; exists {
		if !allowed {
			return unprocessable("x-asset at %s must be in properties, items or a local definition", path)
		}
		if _, ok := acceptedKinds(value); !ok {
			return unprocessable("x-asset at %s must name agent, skill or prompt", path)
		}
	}
	for _, keyword := range []string{"properties", "$defs", "definitions"} {
		if fields, ok := schema[keyword].(map[string]any); ok {
			for name, value := range fields {
				if child, ok := value.(map[string]any); ok {
					if err := inspectAnnotations(child, path+"/"+keyword+"/"+pointerPart(name), allowed); err != nil {
						return err
					}
				}
			}
		}
	}
	if child, ok := schema["items"].(map[string]any); ok {
		if err := inspectAnnotations(child, path+"/items", allowed); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems", "not", "if", "then", "else", "contains", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "patternProperties", "dependentSchemas"} {
		var err error
		switch v := schema[keyword].(type) {
		case map[string]any:
			if keyword == "patternProperties" || keyword == "dependentSchemas" {
				for key, value := range v {
					if child, ok := value.(map[string]any); ok {
						if err := inspectAnnotations(child, path+"/"+keyword+"/"+pointerPart(key), false); err != nil {
							return err
						}
					}
				}
			} else {
				err = inspectAnnotations(v, path+"/"+keyword, false)
			}
		case []any:
			for index, item := range v {
				if child, ok := item.(map[string]any); ok {
					if err := inspectAnnotations(child, fmt.Sprintf("%s/%s/%d", path, keyword, index), false); err != nil {
						return err
					}
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var escapePointer = strings.NewReplacer("~", "~0", "/", "~1")
var decodePointer = strings.NewReplacer("~1", "/", "~0", "~")

func pointerPart(s string) string { return escapePointer.Replace(s) }

func attachedShape(root, schema map[string]any) map[string]any {
	for range 16 {
		ref, ok := schema["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#") || schema["x-asset"] != nil || schema["properties"] != nil || schema["items"] != nil {
			break
		}
		pointer, err := url.PathUnescape(ref[1:])
		if err != nil {
			break
		}
		var value any = root
		if pointer != "" {
			if !strings.HasPrefix(pointer, "/") {
				break
			}
			for _, part := range strings.Split(pointer[1:], "/") {
				fields, _ := value.(map[string]any)
				value = fields[decodePointer.Replace(part)]
			}
		}
		target, ok := value.(map[string]any)
		if !ok {
			break
		}
		schema = target
	}
	return schema
}

type assetResolver struct {
	ctx context.Context
	tx  *sql.Tx

	references int
	bytes      int
	stack      map[Ref]bool
	schemas    map[SchemaRef]json.RawMessage
}

func newResolver(ctx context.Context, tx *sql.Tx) *assetResolver {
	return &assetResolver{ctx: ctx, tx: tx, stack: make(map[Ref]bool), schemas: make(map[SchemaRef]json.RawMessage)}
}

func (r *assetResolver) transform(raw, schema json.RawMessage, expand bool) (json.RawMessage, error) {
	definition, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	root, ok := definition.(map[string]any)
	if !ok {
		return nil, errors.New("stored schema is not an object")
	}
	payload, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	resolved, err := r.walk(root, root, payload, "", expand)
	if err != nil || !expand {
		return raw, err
	}
	return json.Marshal(resolved)
}

func (r *assetResolver) asset(reference Ref) (ResolvedAsset, string, error) {
	asset, kind, err := resolveRef(r.ctx, r.tx, reference)
	if err == nil {
		asset.Config, err = r.expand(asset)
	}
	return asset, kind, err
}

func (r *assetResolver) expand(asset ResolvedAsset) (json.RawMessage, error) {
	identity := Ref{AssetKey: asset.AssetKey, Version: asset.Version}
	r.references++
	r.bytes += len(asset.Config)
	if r.references > 120 || r.bytes > 4<<20 {
		return nil, unprocessable("expanded release exceeds the reference or size limit")
	}
	if r.stack[identity] || len(r.stack) >= 16 {
		return nil, unprocessable("asset references form a cycle or exceed 16 levels")
	}
	r.stack[identity] = true
	defer delete(r.stack, identity)
	definition, exists := r.schemas[asset.Schema]
	if !exists {
		var err error
		definition, _, err = schemaDefinition(r.ctx, r.tx, asset.Schema)
		if err != nil {
			return nil, err
		}
		r.schemas[asset.Schema] = definition
	}
	return r.transform(asset.Config, definition, true)
}

func (r *assetResolver) walk(root, schema map[string]any, value any, path string, expand bool) (any, error) {
	schema = attachedShape(root, schema)
	if annotation, exists := schema["x-asset"]; exists {
		kinds, _ := acceptedKinds(annotation)
		reference, err := readAssetValue(value, path)
		if err != nil {
			return nil, err
		}
		item, kind, err := resolveRef(r.ctx, r.tx, reference)
		if err != nil {
			var validation *requestError
			if errors.As(err, &validation) {
				return nil, unprocessable("%s: %s", path, validation.Message)
			}
			return nil, err
		}
		if !slices.Contains(kinds, kind) {
			return nil, unprocessable("%s expects %s, received %s", path, strings.Join(kinds, " or "), kind)
		}
		if !expand {
			return value, nil
		}
		item.Config, err = r.expand(item)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"assetKey": item.AssetKey, "version": item.Version, "kind": kind,
			"name": item.Name, "schema": item.Schema, "config": item.Config,
		}, nil
	}
	switch data := value.(type) {
	case map[string]any:
		fields, _ := schema["properties"].(map[string]any)
		for name, field := range fields {
			childSchema, ok := field.(map[string]any)
			child, present := data[name]
			if !ok || !present {
				continue
			}
			resolved, err := r.walk(root, childSchema, child, path+"/"+pointerPart(name), expand)
			if err != nil {
				return nil, err
			}
			data[name] = resolved
		}
	case []any:
		if childSchema, ok := schema["items"].(map[string]any); ok {
			for index, child := range data {
				resolved, err := r.walk(root, childSchema, child, fmt.Sprintf("%s/%d", path, index), expand)
				if err != nil {
					return nil, err
				}
				data[index] = resolved
			}
		}
	}
	return value, nil
}

func readAssetValue(value any, path string) (Ref, error) {
	object, _ := value.(map[string]any)
	key, _ := object["assetKey"].(string)
	version, _ := object["version"].(json.Number)
	number, err := version.Int64()
	if len(object) != 2 || !keyPattern.MatchString(key) || err != nil || number < 1 {
		return Ref{}, unprocessable("%s requires an object with assetKey and a positive integer version", path)
	}
	return Ref{AssetKey: key, Version: number}, nil
}
