# HTTP API

The management console and external clients use the same API. The examples assume an empty instance on `127.0.0.1:8080`.

## Create, publish, and read a configuration

Register a schema and import a matching Agent.

```sh
api=http://127.0.0.1:8080/api/v1

curl -fsS "$api/schemas" -H 'Content-Type: application/json' -d '{
  "key":"chat-agent",
  "name":"Chat Agent",
  "kind":"agent",
  "definition":{
    "type":"object",
    "properties":{"model":{"type":"string"}},
    "required":["model"]
  }
}'

curl -fsS "$api/assets" -H 'Content-Type: application/json' -d '{
  "key":"triage-agent",
  "name":"Triage Agent",
  "schema":{"key":"chat-agent","version":1},
  "config":{"model":"example-chat","temperature":0.2}
}'
```

Create the experiment, save its first draft, and publish. An Agent may have zero or more Skill and Prompt dependencies. Each dependency uses the same `slot`, `assetKey`, and `version` fields as an Agent binding.

```sh
curl -fsS "$api/experiments" -H 'Content-Type: application/json' -d '{
  "tag":"routing-trial","name":"Routing trial"
}'

curl -fsS -X PUT "$api/experiments/routing-trial/draft" -H 'Content-Type: application/json' -d '{
  "expectedRevision":0,
  "agents":[{"slot":"triage","assetKey":"triage-agent","version":1,"dependencies":[]}]
}'

curl -fsS "$api/experiments/routing-trial/preview"

curl -fsS "$api/experiments/routing-trial/releases" -H 'Content-Type: application/json' -d '{
  "expectedRevision":1,
  "expectedActiveReleaseId":null
}'

curl -fsS "$api/config?tag=routing-trial"
curl -fsS --get "$api/config" --data-urlencode 'tag=routing-trial' --data-urlencode 'releaseId=routing-trial@1'
```

Each published response contains a `releaseId`, an ordered `agents` array and each Agent’s `skills` and `prompts`. All resolved assets carry their business JSON under `config` and their immutable `schema` and asset versions. Log the `releaseId` if you need to replay an experiment later.

## Schema-driven experiments and asset fields

Register a Schema with `kind: "experiment"` and use `x-asset` in its `properties` or `items`. The same annotation works inside the Schema for an Agent, Skill, or Prompt asset.

```json
{
  "type": "object",
  "properties": {
    "primary": { "x-asset": "agent" },
    "fallbacks": { "type": "array", "items": { "x-asset": ["agent", "skill"] } }
  },
  "required": ["primary"]
}
```

Send `schema: {"key":"routing-profile","version":1}` when creating an experiment. Use `expectedRevision`, `schema`, and `config` when saving its draft. For example, `config.primary` can be `{"assetKey":"schema-support-agent","version":1}`. The server validates the version and kind of that reference and stores it with the draft.

Preview and publish use the same endpoints as the original experiment format. In published responses, `config` contains the named fields and expanded asset objects. Business fields follow only their JSON Schema. Names such as `when` and `branches` are ordinary fields whose meaning belongs to the caller. The underlying asset JSON is nested under each resolved asset's own `config`.

See the [Schema composition spec](../specs/002-schema-composition/spec.md) and [complete sample](../examples/schema-routing.json).

## Versioning and conflicts

Schemas can use local references, such as fragments under `$defs`. Bundle any external schema definitions into the submitted document.

- Use `GET /schemas/{key}/versions` or `GET /assets/{key}/versions` to inspect version history. POST a new version with `baseVersion` set to the current version. Provide `definition` for a Schema or `schema` and `config` for an asset. The server assigns the next version.
- Draft writes use `expectedRevision` from the experiment endpoint. Publish the revision you previewed, using its observed `activeReleaseId` as `expectedActiveReleaseId`.
- Activate a history entry with `PUT /experiments/{tag}/active-release` and `{"releaseId":"routing-trial@1","expectedActiveReleaseId":"routing-trial@2"}`.
- Retrying a completed publish request returns its original snapshot. The current pointer stays at the most recently activated release.
- A `409` calls for a fresh read before retrying a new write. A `422` identifies validation or reference errors. A `404` on the read API means no matching published release exists.
- The API expects JSON request bodies. The default limits are 1 MiB per request, 128 KiB per asset configuration, 4 MiB per snapshot, 24 Agent bindings and 120 total bindings per experiment.

For the complete endpoint map see the [technical design](../specs/001-experiment-config/plan.md#3-http-api-草案).

## Storage and local deployment

The database is `data/config.db` by default. Choose its path with `-db`. SQLite WAL is enabled for consistent reads during publication. Use SQLite’s backup operation for a running instance, or stop the service cleanly before copying the database. Keep the database and its directory readable only by trusted operating system accounts.

```sh
sqlite3 data/config.db '.backup config.backup.db'
```

The listener accepts loopback addresses. The server trusts processes with access to that local interface. A multi-user deployment requires authentication. Keep it local and pass secret references instead of credentials in configuration.
