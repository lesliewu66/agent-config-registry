# Agent Config Registry

Import existing Agent, Skill, and Prompt JSON. Bind explicit asset versions in a visual experiment editor. Publish a snapshot that runtimes can retrieve with one experiment tag.

同一实验组可以包含多个 Agent。每个 Agent 可以独立绑定同一个 Skill 的不同版本。发布后，新增资产版本和编辑草稿都不会改变已有快照。

## Quick start

You need Go 1.24 or later and a C compiler for the SQLite driver. On macOS, use the Xcode Command Line Tools. On Linux, use GCC or Clang.

```sh
git clone https://github.com/lesliewu66/agent-config-registry.git
cd agent-config-registry
CGO_ENABLED=1 go run . -demo
```

Open [the local console](http://127.0.0.1:8080). The demo opens **有图／无图路由 / Image routing**, which combines a business key, branch conditions, timeouts, and Agent versions in one schema. It also includes **Schema routing** and **Support routing**. The console defaults to Simplified Chinese. Use **English** in the header to switch, and it remembers your choice. The examples demonstrate two Agents pinning **Knowledge Search v3** and **v4**, respectively.

```sh
curl 'http://127.0.0.1:8080/api/v1/config?tag=image-routing'
curl 'http://127.0.0.1:8080/api/v1/config?tag=support-routing&releaseId=support-routing%401'
```

With the console switched to English, use **Assets → Versions → Create next version** to add Knowledge Search v5. In **Compositions**, open **Support routing** and edit the intake Agent’s knowledge binding, save the draft, and use **Preview & publish**. The tag switches only when the release succeeds. Use **Publication history → Activate** to return the tag to the first snapshot.

For your own data, start with a fresh database and register an asset schema in **Schemas → Asset schemas**. **Assets → Import asset** accepts pasted JSON, a JSON file, or common fields generated from the schema. Create a group schema in **Schemas → Composition schemas** with `x-asset` fields, then select that schema when creating a group. Choose assets and exact versions, inspect the pinned Schema beside the form, and preview the resolved structure or JSON before publishing.

```sh
CGO_ENABLED=1 go run . -addr 127.0.0.1:8090 -db data/my-registry.db
```

The demo flag adds an example only when its tag is absent. Use a separate database for experiments based on real configuration.

## Verify

```sh
CGO_ENABLED=1 go test -race ./...
go vet ./...
go build -o agent-config-registry .
./agent-config-registry -version
```

The tests exercise the HTTP handlers against SQLite. They check schema validation, large JSON integers, two pinned skill versions, failed publication, concurrent draft writes and runtime reads, historical readback, rollback, and persistence across restart.

## Design and API

- [API examples and deployment notes](docs/api.md)
- [Schema-driven composition](specs/002-schema-composition/spec.md)
- [Product specification](specs/001-experiment-config/spec.md)
- [Technical design](specs/001-experiment-config/plan.md)
- [Business fields and branching example](examples/image-routing.json)
- [Schema-driven example](examples/schema-routing.json)
- [Original demo schemas and assets](examples/support-routing.json)

The server binds to localhost and is intended for a trusted workstation. Authentication, team permissions, and operational audit are future production deployment work. Store secret references in asset JSON and resolve them in your runtime.

MIT licensed. See [LICENSE](LICENSE).
