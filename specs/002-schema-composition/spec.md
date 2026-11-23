# Schema 驱动的实验配置

**版本**　v0.2

## 配置方式

在「配置结构 → 组合 Schema」定义配置组字段。Agent、Skill 和 Prompt 的 Schema 在「配置结构 → 资产 Schema」单独管理。两类 Schema 都支持资产选择字段。在「组合配置」页新建配置组时选一个组合结构版本。编辑配置组时，侧栏显示当前选定的 Schema、版本、字段类型和必填项，可切换到原始 JSON。切换同一 Schema 的版本后，表单与侧栏同步更新。发布前先看展开后的结构，也可以通过 JSON 核对完整响应。

```json
{
  "type": "object",
  "properties": {
    "primary": { "title": "主 Agent", "x-asset": "agent" },
    "alternatives": { "type": "array", "items": { "x-asset": ["agent", "skill"] } },
    "requestTimeoutMs": { "type": "integer", "minimum": 1 }
  },
  "required": ["primary"]
}
```

`x-asset` 可填写 `agent`、`skill`、`prompt`，也可以填写这些类型组成的列表。将它放在字段或数组元素的 Schema 上。由 `properties`、`items` 和本地 JSON Pointer `$ref` 形成的层级支持视觉填表。更复杂的 JSON Schema 可通过原始 JSON 编辑。需要切换语言的字段可用 `title` 指定默认名称，再用 `"x-title": {"zh-CN": "中文名"}` 指定中文名称。

表单将引用保存为 `{"assetKey":"support-agent","version":2}`。切换资产时，表单默认显示当时的最新版本，实际保存的是上述指定版本。服务端验证引用版本存在、类型与字段匹配，以及配置符合对应的 JSON Schema。

## 发布与实验标签

实验组只需要一个 tag。tag 指向组的当前发布。发布在同一事务写入完全展开的 JSON 快照并更新指针。Schema 驱动的配置保留字段名并放在响应的 `config` 下。引用字段展开为含 `assetKey`、`version`、`kind`、`schema` 和 `config` 的对象，资产内部的引用也会继续展开。

```text
GET /api/v1/config?tag=schema-routing
GET /api/v1/config?tag=schema-routing&releaseId=schema-routing%401
```

若有对照组和实验组，可分别发布到例如 `routing-control` 和 `routing-treatment`，由调用方分流后选择相应的 tag。资产版本可以被多个实验复用。旧版 Agent 固定绑定的实验仍按原格式返回 `agents` 列表。

## 业务字段和分支

组合 Schema 同时描述业务值和资产引用。[有图／无图样例](../../examples/image-routing.json) 包含业务标识 `businessKey`、`branches.withImage` 和 `branches.withoutImage`。每个分支都有业务自定义的 `when.hasImage`、Agent 引用和独立的超时参数。

`when` 和 `branches` 都是普通业务字段，名称、类型和含义由调用方的 Schema 决定。平台按 Schema 校验这些字段，并把值保存到发布快照。资产展开由 `x-asset` 声明触发。业务如何解释字段、是否匹配条件、走哪个分支，都由调用方决定。组合配置也可以只包含普通业务字段。

## 验收

1. 创建自定义实验组 Schema，可以填写资产、数组和普通参数。填写项和输出字段名由 Schema 决定。
2. 创建包含资产引用的 Agent 版本，可以为其中的 Skill、Prompt 选版本。无效版本或不匹配类型返回错误和字段路径。
3. 组内两个 Agent 各自引用 Skill `v3` 和 `v4`。新增 Skill `v5` 后，原发布仍可读取原版本。
4. 验证已有数据库升级后仍能读取原发布，且新旧两种配置形式可同时使用。
5. 中文为默认语言，切到英文后刷新能记住选择。切换语言期间保留未保存的草稿。

首版的发布限制沿用 120 项资产引用和 4 MiB 快照上限，资产引用展开深度上限为 16 层。
