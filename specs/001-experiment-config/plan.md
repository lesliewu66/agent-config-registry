# v0.1 技术方案

对应 [实验配置组规格](spec.md)。本方案以单进程本地部署为起点，技术选型为 Go、SQLite 和随服务交付的 Web 管理界面。

## 1. 组件与存储

| 记录 | 主键或唯一约束 | 语义 |
|---|---|---|
| Schema 版本 | `(schema_key, version)` | 保存 JSON Schema 和所属资产类型。新增版本保留历史。 |
| 资产 | `asset_key` | 表示一个逻辑 Agent、Skill 或 Prompt。标识和类型稳定。 |
| 资产版本 | `(asset_key, version)` | 保存业务原始 JSON 和验证所用的 Schema 版本。 |
| 实验组 | `tag` | 保存名称和草稿修订号。 |
| 草稿 Agent 绑定 | `(tag, slot)` | 按顺序引用一个明确的 Agent 版本。 |
| 草稿依赖绑定 | `(tag, agent_slot, slot)` | 外键指向所在 Agent 绑定，并引用一个明确的 Skill 或 Prompt 版本。 |
| 发布 | `(tag, release_number)` 和 `(tag, draft_revision)` | 保存解析后的整份 JSON 快照。一个草稿修订只产生一次发布。 |
| 当前发布 | `tag` | 通过复合外键指向同一组的某次发布。 |

SQLite 启用外键和 WAL。外键负责引用存在性，服务层负责类型与约束校验。历史版本通过追加写入维持不可变性。草稿绑定可以规范化存储，发布后以快照读取。使用单个 SQLite 写事务处理每次状态变化。Web UI 与外部客户端共享 HTTP API。

## 2. 发布与读取

1. 保存草稿时传 `expectedRevision`。服务端验证绑定后，在同一事务替换绑定并推进修订号。
2. 发布时传 `expectedRevision` 和 `expectedActiveReleaseId`。服务先检查这个修订是否已有发布，有则返回原结果。否则检查前置条件、按明确的版本解析引用，写入快照并更新当前发布指针，然后提交事务。
3. 运行时读取时，用一次数据库查询取得当前指针对应的快照。指定 `releaseId` 时直接查询对应的历史记录。每次请求基于一个已提交的发布，因此无需在读路径追踪各资产的最新版本。
4. 激活历史发布时传 `releaseId` 和 `expectedActiveReleaseId`。事务内确认它属于该 tag 后切换指针。首次发布时，`expectedActiveReleaseId` 显式传 `null`。
5. 快照会复制每项资产的 `config`，同时记录资产和 Schema 版本。客户端按业务所需的绑定名选择所需数据。v0.1 保留所有发布和资产版本，默认限制单个配置和快照大小。初始上限拟定为单资产 128 KiB、整组 4 MiB，以实际用例校准并写入配置文档。

如果发布提交后客户端超时，重试同一个草稿修订即可找回对应的发布 ID。客户端收到 `409` 时重新读取草稿和当前发布后再提交。默认返回 `Cache-Control: no-store`，让 tag 切换即时反映在后续读请求上。

## 3. HTTP API 草案

| 方法与路径 | 行为 |
|---|---|
| `GET /api/v1/schemas` | 按类型列出 Schema 和最新版本。 |
| `POST /api/v1/schemas` | 注册 Schema 初始版本。 |
| `GET, POST /api/v1/schemas/{key}/versions` | 列出或新增一个 Schema 版本。 |
| `GET, POST /api/v1/assets` | 列出或创建资产及其第一个版本。 |
| `GET, POST /api/v1/assets/{key}/versions` | 列出或新增资产版本，创建时携带期望的当前版本。 |
| `GET, POST /api/v1/experiments` | 列出或创建实验组。 |
| `GET /api/v1/experiments/{tag}` | 返回草稿、草稿修订号、当前发布和发布历史。 |
| `PUT /api/v1/experiments/{tag}/draft` | 按 `expectedRevision` 保存新的绑定集合。 |
| `GET /api/v1/experiments/{tag}/preview` | 展开当前已保存的草稿，响应标明对应修订号。 |
| `POST /api/v1/experiments/{tag}/releases` | 发布指定草稿修订，返回发布 ID。 |
| `PUT /api/v1/experiments/{tag}/active-release` | 将该组已有的发布设为当前版本。 |
| `GET /api/v1/config?tag={tag}&releaseId={id}` | 读取已发布的完整配置，`releaseId` 可省略。 |

HTTP 状态统一使用 `400` 表示请求格式错误，`404` 表示资源或发布不存在，`409` 表示版本冲突，`422` 表示引用或内容验证失败。错误体至少包含稳定的 `code` 和面向人的 `message`。明确指定的未知 `releaseId` 不会回退到当前版本。

### 读取响应示例

下例用两个只绑定了 Skill 的 Agent 演示返回格式。实际可包含独立的 Prompt 列表。

```json
{
  "tag": "support-routing",
  "releaseId": "support-routing@1",
  "draftRevision": 4,
  "publishedAt": "2026-01-01T00:00:00Z",
  "agents": [
    {
      "slot": "intake",
      "assetKey": "support-agent",
      "version": 12,
      "schema": { "key": "chat-agent", "version": 1 },
      "config": { "model": "example-chat", "timeoutMs": 3000 },
      "skills": [
        {
          "slot": "knowledge",
          "assetKey": "knowledge-search",
          "version": 3,
          "schema": { "key": "search-skill", "version": 1 },
          "config": { "topK": 3 }
        }
      ],
      "prompts": []
    },
    {
      "slot": "dispatch",
      "assetKey": "dispatch-agent",
      "version": 8,
      "schema": { "key": "routing-agent", "version": 1 },
      "config": { "routingMode": "rules", "maxCandidates": 2 },
      "skills": [
        {
          "slot": "knowledge",
          "assetKey": "knowledge-search",
          "version": 4,
          "schema": { "key": "search-skill", "version": 1 },
          "config": { "topK": 5 }
        }
      ],
      "prompts": []
    }
  ]
}
```

## 4. 界面

| 页面 | 主要操作 |
|---|---|
| 资产与 Schema | 列表按类型筛选。查看历史版本、创建新版本、导入 JSON。提供表单和原始 JSON 两种入口，校验错误定位到 JSON 路径。 |
| 实验组 | 列表展示 tag、当前发布和草稿状态。详情展开 Agent 与依赖，允许在行内选择资产和版本。 |
| 发布预览 | 显示本次草稿修订号、最终 JSON 和生效版本。发布历史支持查看快照和重新激活。 |

## 5. 实施与 GitHub 验收

| 顺序 | 建议的 PR | 通过条件 |
|---|---|---|
| 1 | Schema 和资产版本 API | AC-01，含真实 SQLite 的验证和持久化测试。 |
| 2 | 实验组草稿、版本引用与发布事务 | AC-03 至 AC-07，在事务测试覆盖并发、失败和重放后再合并。 |
| 3 | Web 管理界面与示例 | AC-02，用浏览器完成导入、编排、预览、发布和回滚。 |
| 4 | 文档、CI 与首个可运行版本 | README 启动命令、示例、API 契约、备份和部署边界可在干净环境验证。 |

规格变更同样走 PR。涉及 API 行为的变更更新契约和对应的验收用例。首个实现 PR 使用合成样例验证完整链路，便于其他贡献者复现。
