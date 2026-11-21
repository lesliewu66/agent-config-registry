package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

//go:embed database.sql
var databaseSchema string

type Store struct {
	db *sql.DB
}

func openStore(ctx context.Context, path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	dsn := url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "_foreign_keys=on&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_txlock=immediate",
	}
	db, err := sql.Open("sqlite3", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	_, err = inTx(ctx, store, func(tx *sql.Tx) (struct{}, error) {
		_, err := tx.ExecContext(ctx, databaseSchema)
		return struct{}{}, err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func inTx[T any](ctx context.Context, s *Store, run func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	result, err := run(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func collect[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func createdAt() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func uniqueError(err error, what, key string) error {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey ||
		sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique) {
		return conflict("%s %q already exists", what, key)
	}
	return err
}

func (s *Store) schemas(ctx context.Context) ([]Schema, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.key, s.name, s.kind, MAX(v.version)
		FROM schemas s JOIN schema_versions v ON v.schema_key = s.key
		GROUP BY s.key ORDER BY s.kind, s.key`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row *sql.Rows) (Schema, error) {
		var item Schema
		err := row.Scan(&item.Key, &item.Name, &item.Kind, &item.LatestVersion)
		return item, err
	})
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createSchema(ctx context.Context, tx *sql.Tx, input NewSchema) (Schema, error) {
	if err := checkKey("key", input.Key); err != nil {
		return Schema{}, err
	}
	if err := checkName(input.Name); err != nil {
		return Schema{}, err
	}
	if err := checkKind(input.Kind); err != nil {
		return Schema{}, err
	}
	if _, err := compileSchema(input.Definition); err != nil {
		return Schema{}, err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO schemas(key, name, kind) VALUES (?, ?, ?)", input.Key, input.Name, input.Kind)
	if err != nil {
		return Schema{}, uniqueError(err, "schema", input.Key)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO schema_versions VALUES (?, 1, ?, ?)", input.Key, string(input.Definition), createdAt())
	return Schema{Key: input.Key, Name: input.Name, Kind: input.Kind, LatestVersion: 1}, err
}

func (s *Store) schemaVersions(ctx context.Context, key string) ([]SchemaVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, definition, created_at FROM schema_versions WHERE schema_key = ? ORDER BY version DESC`, key)
	if err != nil {
		return nil, err
	}
	versions, err := collect(rows, func(row *sql.Rows) (SchemaVersion, error) {
		var item SchemaVersion
		var definition string
		err := row.Scan(&item.Version, &definition, &item.CreatedAt)
		item.Definition = json.RawMessage(definition)
		return item, err
	})
	if err == nil && len(versions) == 0 {
		return nil, missing("schema %q not found", key)
	}
	return versions, err
}

func addSchemaVersion(ctx context.Context, tx *sql.Tx, key string, input SchemaVersionInput) (SchemaVersion, error) {
	var latest sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_versions WHERE schema_key = ?", key).Scan(&latest)
	if err != nil {
		return SchemaVersion{}, err
	}
	if !latest.Valid {
		return SchemaVersion{}, missing("schema %q not found", key)
	}
	if input.BaseVersion != latest.Int64 {
		return SchemaVersion{}, conflict("schema is at version %d; reload before creating another version", latest.Int64)
	}
	if _, err := compileSchema(input.Definition); err != nil {
		return SchemaVersion{}, err
	}
	result := SchemaVersion{Version: latest.Int64 + 1, Definition: input.Definition, CreatedAt: createdAt()}
	_, err = tx.ExecContext(ctx, "INSERT INTO schema_versions VALUES (?, ?, ?, ?)", key, result.Version, string(result.Definition), result.CreatedAt)
	return result, err
}

func schemaDefinition(ctx context.Context, tx *sql.Tx, ref SchemaRef) (json.RawMessage, string, error) {
	var definition, kind string
	err := tx.QueryRowContext(ctx, `
		SELECT v.definition, s.kind FROM schema_versions v JOIN schemas s ON s.key = v.schema_key
		WHERE v.schema_key = ? AND v.version = ?`, ref.Key, ref.Version).Scan(&definition, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", unprocessable("schema version %s@%d does not exist", ref.Key, ref.Version)
	}
	return json.RawMessage(definition), kind, err
}

func (s *Store) assets(ctx context.Context) ([]Asset, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.key, a.name, a.kind, v.schema_key, v.schema_version, v.version
		FROM assets a JOIN asset_versions v ON v.asset_key = a.key
		  AND v.version = (SELECT MAX(version) FROM asset_versions WHERE asset_key = a.key)
		ORDER BY a.kind, a.key`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row *sql.Rows) (Asset, error) {
		var item Asset
		err := row.Scan(&item.Key, &item.Name, &item.Kind, &item.Schema.Key, &item.Schema.Version, &item.LatestVersion)
		return item, err
	})
}

func createAsset(ctx context.Context, tx *sql.Tx, item NewAsset) (Asset, error) {
	if err := checkKey("key", item.Key); err != nil {
		return Asset{}, err
	}
	if err := checkName(item.Name); err != nil {
		return Asset{}, err
	}
	definition, kind, err := schemaDefinition(ctx, tx, item.Schema)
	if err != nil {
		return Asset{}, err
	}
	config, err := validateConfig(item.Config, definition)
	if err != nil {
		return Asset{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO assets(key, name, kind) VALUES (?, ?, ?)", item.Key, item.Name, kind)
	if err != nil {
		return Asset{}, uniqueError(err, "asset", item.Key)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO asset_versions VALUES (?, 1, ?, ?, ?, ?)`, item.Key, item.Schema.Key, item.Schema.Version, config, createdAt())
	return Asset{Key: item.Key, Name: item.Name, Kind: kind, Schema: item.Schema, LatestVersion: 1}, err
}

func (s *Store) versions(ctx context.Context, key string) ([]AssetVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, schema_key, schema_version, config, created_at FROM asset_versions
		WHERE asset_key = ? ORDER BY version DESC`, key)
	if err != nil {
		return nil, err
	}
	versions, err := collect(rows, func(row *sql.Rows) (AssetVersion, error) {
		var item AssetVersion
		var config string
		err := row.Scan(&item.Version, &item.Schema.Key, &item.Schema.Version, &config, &item.CreatedAt)
		item.Config = json.RawMessage(config)
		return item, err
	})
	if err == nil && len(versions) == 0 {
		return nil, missing("asset %q not found", key)
	}
	return versions, err
}

func addVersion(ctx context.Context, tx *sql.Tx, key string, input VersionInput) (AssetVersion, error) {
	var latest int64
	var kind string
	err := tx.QueryRowContext(ctx, `
		SELECT a.kind, (SELECT MAX(version) FROM asset_versions WHERE asset_key = a.key)
		FROM assets a WHERE a.key = ?`, key).Scan(&kind, &latest)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetVersion{}, missing("asset %q not found", key)
	}
	if err != nil {
		return AssetVersion{}, err
	}
	if input.BaseVersion != latest {
		return AssetVersion{}, conflict("asset is at version %d; reload before creating another version", latest)
	}
	definition, schemaKind, err := schemaDefinition(ctx, tx, input.Schema)
	if err != nil {
		return AssetVersion{}, err
	}
	if schemaKind != kind {
		return AssetVersion{}, unprocessable("asset type %q cannot use a %q schema", kind, schemaKind)
	}
	config, err := validateConfig(input.Config, definition)
	if err != nil {
		return AssetVersion{}, err
	}
	result := AssetVersion{Version: latest + 1, Schema: input.Schema, Config: json.RawMessage(config), CreatedAt: createdAt()}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO asset_versions VALUES (?, ?, ?, ?, ?, ?)`, key, result.Version,
		result.Schema.Key, result.Schema.Version, config, result.CreatedAt)
	return result, err
}

func createExperiment(ctx context.Context, db execer, tag, name string) (Experiment, error) {
	if err := checkKey("tag", tag); err != nil {
		return Experiment{}, err
	}
	if err := checkName(name); err != nil {
		return Experiment{}, err
	}
	_, err := db.ExecContext(ctx, "INSERT INTO experiments(tag, name) VALUES (?, ?)", tag, name)
	return Experiment{Tag: tag, Name: name}, uniqueError(err, "experiment", tag)
}

const experimentQuery = `
	SELECT e.tag, e.name, e.revision, COALESCE(r.release, 0),
		(SELECT COUNT(*) FROM draft_agents a WHERE a.tag = e.tag)
	FROM experiments e LEFT JOIN active_releases r ON r.tag = e.tag`

func (s *Store) experiments(ctx context.Context) ([]Experiment, error) {
	rows, err := s.db.QueryContext(ctx, experimentQuery+" ORDER BY e.tag")
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row *sql.Rows) (Experiment, error) {
		return scanExperiment(row)
	})
}

type rowScanner interface {
	Scan(...any) error
}

func scanExperiment(row rowScanner) (Experiment, error) {
	var item Experiment
	var active int64
	err := row.Scan(&item.Tag, &item.Name, &item.Revision, &active, &item.AgentCount)
	if err == nil && active > 0 {
		id := releaseID(item.Tag, active)
		item.ActiveReleaseID = &id
	}
	return item, err
}

func experiment(ctx context.Context, tx *sql.Tx, tag string) (Experiment, error) {
	item, err := scanExperiment(tx.QueryRowContext(ctx, experimentQuery+" WHERE e.tag = ?", tag))
	if errors.Is(err, sql.ErrNoRows) {
		return Experiment{}, missing("experiment %q not found", tag)
	}
	return item, err
}

func draftAgents(ctx context.Context, tx *sql.Tx, tag string) ([]AgentRef, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.slot, a.asset_key, a.version, d.slot, d.asset_key, d.version
		FROM draft_agents a LEFT JOIN draft_dependencies d ON d.tag = a.tag AND d.agent_slot = a.slot
		WHERE a.tag = ? ORDER BY a.position, d.position`, tag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []AgentRef{}
	for rows.Next() {
		var a Ref
		var slot, key sql.NullString
		var version sql.NullInt64
		if err := rows.Scan(&a.Slot, &a.AssetKey, &a.Version, &slot, &key, &version); err != nil {
			return nil, err
		}
		if len(agents) == 0 || agents[len(agents)-1].Slot != a.Slot {
			agents = append(agents, AgentRef{Ref: a, Dependencies: []Ref{}})
		}
		if slot.Valid {
			last := &agents[len(agents)-1]
			last.Dependencies = append(last.Dependencies, Ref{Slot: slot.String, AssetKey: key.String, Version: version.Int64})
		}
	}
	return agents, rows.Err()
}

func releaseID(tag string, version int64) string { return fmt.Sprintf("%s@%d", tag, version) }

func (s *Store) experimentDetail(ctx context.Context, tag string) (ExperimentDetail, error) {
	return inTx(ctx, s, func(tx *sql.Tx) (ExperimentDetail, error) {
		item, err := experiment(ctx, tx, tag)
		if err != nil {
			return ExperimentDetail{}, err
		}
		agents, err := draftAgents(ctx, tx, tag)
		if err != nil {
			return ExperimentDetail{}, err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT release, draft_revision, published_at FROM releases WHERE tag = ? ORDER BY release DESC`, tag)
		if err != nil {
			return ExperimentDetail{}, err
		}
		releases, err := collect(rows, func(row *sql.Rows) (ReleaseMeta, error) {
			var item ReleaseMeta
			err := row.Scan(&item.ReleaseNumber, &item.DraftRevision, &item.PublishedAt)
			item.ReleaseID = releaseID(tag, item.ReleaseNumber)
			return item, err
		})
		return ExperimentDetail{Experiment: item, Agents: agents, Releases: releases}, err
	})
}

func resolveRef(ctx context.Context, tx *sql.Tx, ref Ref) (ResolvedAsset, string, error) {
	item := ResolvedAsset{Ref: ref}
	var config, kind string
	err := tx.QueryRowContext(ctx, `
				SELECT a.name, a.kind, v.schema_key, v.schema_version, v.config
		FROM asset_versions v JOIN assets a ON a.key = v.asset_key
		WHERE a.key = ? AND v.version = ?`, ref.AssetKey, ref.Version).Scan(
		&item.Name, &kind, &item.Schema.Key, &item.Schema.Version, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return ResolvedAsset{}, "", unprocessable("asset version %s@%d does not exist", ref.AssetKey, ref.Version)
	}
	item.Config = json.RawMessage(config)
	return item, kind, err
}

func resolveDraft(ctx context.Context, tx *sql.Tx, item Experiment, agents []AgentRef) (Config, error) {
	result := Config{Tag: item.Tag, Name: item.Name, Agents: []ResolvedAgent{}, ReleaseMeta: ReleaseMeta{DraftRevision: item.Revision}}
	for _, agent := range agents {
		data, kind, err := resolveRef(ctx, tx, agent.Ref)
		if err != nil {
			return Config{}, err
		}
		if kind != "agent" {
			return Config{}, unprocessable("%q must reference an agent asset", agent.Slot)
		}
		row := ResolvedAgent{ResolvedAsset: data, Skills: []ResolvedAsset{}, Prompts: []ResolvedAsset{}}
		for _, dep := range agent.Dependencies {
			data, kind, err := resolveRef(ctx, tx, dep)
			if err != nil {
				return Config{}, err
			}
			switch kind {
			case "skill":
				row.Skills = append(row.Skills, data)
			case "prompt":
				row.Prompts = append(row.Prompts, data)
			default:
				return Config{}, unprocessable("dependency %q must reference a skill or prompt", dep.Slot)
			}
		}
		result.Agents = append(result.Agents, row)
	}
	return result, nil
}

func saveDraft(ctx context.Context, tx *sql.Tx, tag string, draft Draft) (Draft, error) {
	if err := checkDraft(draft); err != nil {
		return Draft{}, err
	}
	item, err := experiment(ctx, tx, tag)
	if err != nil {
		return Draft{}, err
	}
	if item.Revision != draft.Revision {
		return Draft{}, conflict("draft revision is %d; reload before saving", item.Revision)
	}
	if _, err := resolveDraft(ctx, tx, item, draft.Agents); err != nil {
		return Draft{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM draft_agents WHERE tag = ?", tag); err != nil {
		return Draft{}, err
	}
	for i, agent := range draft.Agents {
		if _, err := tx.ExecContext(ctx, "INSERT INTO draft_agents VALUES (?, ?, ?, ?, ?)", tag, agent.Slot, agent.AssetKey, agent.Version, i); err != nil {
			return Draft{}, err
		}
		for j, dep := range agent.Dependencies {
			if _, err := tx.ExecContext(ctx, "INSERT INTO draft_dependencies VALUES (?, ?, ?, ?, ?, ?)", tag, agent.Slot, dep.Slot, dep.AssetKey, dep.Version, j); err != nil {
				return Draft{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE experiments SET revision = revision + 1 WHERE tag = ?", tag); err != nil {
		return Draft{}, err
	}
	draft.Revision++
	return draft, nil
}

func getPreview(ctx context.Context, tx *sql.Tx, tag string) (Config, error) {
	item, err := experiment(ctx, tx, tag)
	if err != nil {
		return Config{}, err
	}
	agents, err := draftAgents(ctx, tx, tag)
	if err != nil {
		return Config{}, err
	}
	return resolveDraft(ctx, tx, item, agents)
}

func publish(ctx context.Context, tx *sql.Tx, tag string, revision int64, expectedActive *string) (Config, error) {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT snapshot FROM releases WHERE tag = ? AND draft_revision = ?", tag, revision).Scan(&stored)
	if err == nil {
		// Retrying an earlier request returns its original release without moving the active pointer.
		var result Config
		err := json.Unmarshal([]byte(stored), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Config{}, err
	}
	item, err := experiment(ctx, tx, tag)
	if err != nil {
		return Config{}, err
	}
	if item.Revision != revision {
		return Config{}, conflict("draft revision is %d; preview it before publishing", item.Revision)
	}
	if err := checkActive(item, expectedActive); err != nil {
		return Config{}, err
	}
	agents, err := draftAgents(ctx, tx, tag)
	if err != nil {
		return Config{}, err
	}
	result, err := resolveDraft(ctx, tx, item, agents)
	if err != nil {
		return Config{}, err
	}
	if len(result.Agents) == 0 {
		return Config{}, unprocessable("add at least one agent before publishing")
	}
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(release), 0) + 1 FROM releases WHERE tag = ?", tag).Scan(&result.ReleaseNumber)
	if err != nil {
		return Config{}, err
	}
	result.ReleaseID = releaseID(tag, result.ReleaseNumber)
	result.PublishedAt = createdAt()
	data, err := json.Marshal(result)
	if err != nil {
		return Config{}, err
	}
	if len(data) > 4<<20 {
		return Config{}, unprocessable("published snapshot exceeds 4 MiB")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO releases VALUES (?, ?, ?, ?, ?)",
		tag, result.ReleaseNumber, revision, result.PublishedAt, string(data))
	if err != nil {
		return Config{}, err
	}
	return result, activate(ctx, tx, tag, result.ReleaseNumber)
}

func activate(ctx context.Context, tx *sql.Tx, tag string, release int64) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM releases WHERE tag = ? AND release = ?", tag, release).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return missing("release %s not found", releaseID(tag, release))
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO active_releases VALUES (?, ?) ON CONFLICT(tag) DO UPDATE SET release = excluded.release`, tag, release)
	return err
}

func checkActive(item Experiment, expected *string) error {
	current := item.ActiveReleaseID
	if (expected == nil && current == nil) || (expected != nil && current != nil && *expected == *current) {
		return nil
	}
	return conflict("the active release changed; reload before retrying")
}

func parseReleaseID(tag, id string) (int64, error) {
	prefix, version, ok := strings.Cut(id, "@")
	number, err := strconv.ParseInt(version, 10, 64)
	if !ok || prefix != tag || err != nil || number < 1 || releaseID(tag, number) != id {
		return 0, missing("release %q for tag %q not found", id, tag)
	}
	return number, nil
}

func (s *Store) config(ctx context.Context, tag string, release int64) (json.RawMessage, error) {
	var snapshot string
	var err error
	if release == 0 {
		err = s.db.QueryRowContext(ctx, `
			SELECT r.snapshot FROM active_releases a JOIN releases r ON r.tag = a.tag AND r.release = a.release
			WHERE a.tag = ?`, tag).Scan(&snapshot)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT snapshot FROM releases WHERE tag = ? AND release = ?", tag, release).Scan(&snapshot)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("published configuration for tag %q not found", tag)
	}
	return json.RawMessage(snapshot), err
}
