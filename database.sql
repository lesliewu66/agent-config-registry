CREATE TABLE IF NOT EXISTS schemas (
    key TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'skill', 'prompt', 'experiment'))
);

CREATE TABLE IF NOT EXISTS schema_versions (
    schema_key TEXT NOT NULL REFERENCES schemas(key),
    version INTEGER NOT NULL CHECK (version > 0),
    definition TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (schema_key, version)
);

CREATE TABLE IF NOT EXISTS assets (
    key TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'skill', 'prompt'))
);

CREATE TABLE IF NOT EXISTS asset_versions (
    asset_key TEXT NOT NULL REFERENCES assets(key),
    version INTEGER NOT NULL CHECK (version > 0),
    schema_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    config TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (asset_key, version),
    FOREIGN KEY (schema_key, schema_version) REFERENCES schema_versions(schema_key, version)
);

CREATE TABLE IF NOT EXISTS experiments (
    tag TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS draft_agents (
    tag TEXT NOT NULL REFERENCES experiments(tag),
    slot TEXT NOT NULL,
    asset_key TEXT NOT NULL,
    version INTEGER NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (tag, slot),
    FOREIGN KEY (asset_key, version) REFERENCES asset_versions(asset_key, version)
);

CREATE TABLE IF NOT EXISTS draft_dependencies (
    tag TEXT NOT NULL,
    agent_slot TEXT NOT NULL,
    slot TEXT NOT NULL,
    asset_key TEXT NOT NULL,
    version INTEGER NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (tag, agent_slot, slot),
    FOREIGN KEY (tag, agent_slot) REFERENCES draft_agents(tag, slot) ON DELETE CASCADE,
    FOREIGN KEY (asset_key, version) REFERENCES asset_versions(asset_key, version)
);

CREATE TABLE IF NOT EXISTS releases (
    tag TEXT NOT NULL REFERENCES experiments(tag),
    release INTEGER NOT NULL,
    draft_revision INTEGER NOT NULL,
    published_at TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    PRIMARY KEY (tag, release),
    UNIQUE (tag, draft_revision)
);

CREATE TABLE IF NOT EXISTS active_releases (
    tag TEXT PRIMARY KEY,
    release INTEGER NOT NULL,
    FOREIGN KEY (tag, release) REFERENCES releases(tag, release)
);

CREATE TABLE IF NOT EXISTS schema_drafts (
    tag TEXT PRIMARY KEY REFERENCES experiments(tag),
    schema_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    config TEXT,
    FOREIGN KEY (schema_key, schema_version) REFERENCES schema_versions(schema_key, version)
);
