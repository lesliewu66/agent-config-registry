"use strict";

function schemaShape(root, definition) {
  let shape = definition;
  for (let depth = 0; depth < 16 && typeof shape?.$ref === "string"; depth++) {
    if (!shape.$ref.startsWith("#") || shape["x-asset"] || shape.properties || shape.items) break;
    let path;
    try {
      path = decodeURIComponent(shape.$ref.slice(1));
    } catch {
      break;
    }
    if (path && !path.startsWith("/")) break;
    const target = path ? path.slice(1).split("/").reduce((value, part) => {
      const key = part.replaceAll("~1", "/").replaceAll("~0", "~");
      return value != null && Object.hasOwn(value, key) ? value[key] : undefined;
    }, root) : root;
    if (!target || typeof target !== "object") break;
    shape = target;
  }
  return shape ?? {};
}

function schemaLabel(definition, fallback) {
  const title = definition?.["x-title"]?.[locale] ?? definition?.title;
  return typeof title === "string" && title ? title : fallback;
}

function formSupported(root, definition = root) {
  const shape = schemaShape(root, definition);
  return Boolean(shape["x-asset"] || shape.properties || shape.items ||
    ["string", "number", "integer", "boolean"].includes(shape.type) || plainEnum(shape));
}

function plainEnum(shape) {
  return Array.isArray(shape.enum) && shape.enum.length && shape.enum.every((value) =>
    value === null || ["string", "number", "boolean"].includes(typeof value));
}

function renderStructuredFields(definition, value, changed) {
  const root = schemaShape(definition, definition);
  return objectFields(definition, root, value, changed, 0);
}

function objectFields(root, shape, value, changed, depth) {
  const fields = Object.assign(Object.create(null), value ?? {});
  return Object.entries(shape.properties ?? {}).flatMap(([key, child]) => {
    if (!formSupported(root, child)) return [];
    const label = schemaLabel(child, schemaLabel(schemaShape(root, child), key));
    const update = (next) => {
      if (next === undefined) delete fields[key];
      else fields[key] = next;
      changed({ ...fields });
    };
    return [shapeField(root, child, label, fields[key], update, depth + 1, shape.required?.includes(key))];
  });
}

function shapeField(root, definition, label, value, changed, depth, required) {
  const shape = schemaShape(root, definition);
  if (depth > 12) return node("p", { class: "subtle" }, t("Use the JSON editor for deeper fields."));
  if (shape["x-asset"]) return assetField(label, shape, value, changed, required);
  if (shape.items && formSupported(root, shape.items)) return arrayField(root, shape, label, value, changed, depth);
  if (shape.properties) {
    return node("fieldset", { class: "schema-fieldset" }, node("legend", {}, label),
      node("div", { class: "fields" }, objectFields(root, shape, value, changed, depth)));
  }
  return simpleField(shape, label, value, changed, required);
}

function assetField(label, shape, value, changed, required) {
  const kinds = Array.isArray(shape["x-asset"]) ? shape["x-asset"] : [shape["x-asset"]];
  const eligible = state.assets.filter((asset) => kinds.includes(asset.kind));
  const picker = select(eligible.map((asset) => [asset.key, (kinds.length > 1 ? labels[asset.kind] + " · " : "") + displayName(asset)]),
    value?.assetKey ?? "", t("Choose an asset"));
  picker.required = Boolean(required);
  const version = select([], "", t("Choose a version"));
  version.required = Boolean(required);
  let sequence = 0;

  async function load(asset, pinned) {
    const id = ++sequence;
    version.disabled = true;
    setOptions(version, [], "", asset ? t("Loading versions…") : t("Choose a version"));
    if (!asset) return;
    try {
      const versions = await api(versionRoute("assets", asset.key));
      if (sequence !== id) return;
      setOptions(version, versions.map((item) => [item.version, "v" + item.version]), pinned, t("Choose a version"));
      version.disabled = false;
    } catch (error) {
      if (sequence === id) {
        setOptions(version, [], "", t("Versions unavailable"));
        reportError(error);
      }
    }
  }

  picker.addEventListener("change", () => {
    const asset = eligible.find((item) => item.key === picker.value);
    changed(asset ? { assetKey: asset.key, version: asset.latestVersion } : undefined);
    void load(asset, asset?.latestVersion);
  });
  version.addEventListener("change", () => {
    changed(version.value ? { assetKey: picker.value, version: Number(version.value) } : undefined);
  });

  const selected = eligible.find((item) => item.key === value?.assetKey);
  void load(selected, value?.version);
  return node("fieldset", { class: "schema-fieldset", "aria-label": label },
    node("legend", {}, label), node("div", { class: "form-columns asset-picker-columns" },
      field(t("Asset"), picker), field(t("Version"), version)));
}

function arrayField(root, shape, label, value, changed, depth) {
  const values = Array.isArray(value) ? [...value] : [];
  const entries = node("div", { class: "fields" });
  function update(index, next) {
    values[index] = next ?? null;
    changed([...values]);
  }
  function draw() {
    entries.replaceChildren(...values.map((item, index) => node("div", { class: "schema-array-row" },
      shapeField(root, shape.items, t("Item {number}", { number: index + 1 }), item, (next) => update(index, next), depth + 1, true),
      button(t("Remove item"), () => { values.splice(index, 1); changed([...values]); draw(); }, "small quiet danger"))));
  }
  draw();
  return node("fieldset", { class: "schema-fieldset", "aria-label": label }, node("legend", {}, label), entries,
    button(t("Add item"), () => { values.push(null); changed([...values]); draw(); }, "small schema-add"));
}

function simpleField(shape, label, current, changed, required) {
  let control;
  let read;
  if (plainEnum(shape)) {
    const index = shape.enum.findIndex((value) => Object.is(value, current));
    control = select(shape.enum.map((value, i) => [i, typeof value === "string" ? value : JSON.stringify(value)]),
      index === -1 ? "" : index, t("Select a value"));
    read = () => control.value === "" ? undefined : shape.enum[Number(control.value)];
  } else if (shape.type === "boolean") {
    control = select([["true", t("True")], ["false", t("False")]], current === undefined ? "" : current, t("Select a value"));
    read = () => control.value === "" ? undefined : control.value === "true";
  } else if (shape.type === "string") {
    control = input({ value: typeof current === "string" ? current : "" });
    read = () => control.value === "" && !required && current === undefined ? undefined : control.value;
  } else {
    control = input({ type: "number", step: shape.type === "integer" ? "1" : "any",
      value: current == null ? "" : JSON.stringify(current) });
    read = () => control.value === "" ? undefined : parseJSON(control.value);
  }
  control.required = Boolean(required);
  control.addEventListener("input", () => {
    if (!control.validity.badInput) changed(read());
  });
  return field(label, control, typeof shape.description === "string" ? shape.description : undefined);
}

function compositionOutline(data) {
  const draft = state.schemaDraft;
  const definition = data.schema && draft && data.schema.key === draft.schema.key
    ? draft.versions.find((item) => item.version === data.schema.version)?.definition
    : undefined;
  const root = definition ?? {};
  let shown = 0;
  let truncated = false;
  function limit(depth) {
    if (++shown <= 200 && depth < 16) return false;
    truncated = true;
    return true;
  }

  function list(children) {
    return children.length ? node("ul", {}, children) : null;
  }

  function assetRow(label, asset, children) {
    const name = displayName({ key: asset.assetKey, name: asset.name ?? asset.assetKey });
    return node("li", { class: "outline-row outline-asset" },
      node("div", { class: "outline-field" }, label),
      node("div", { class: "asset-title" }, tagLabel(labels[asset.kind], asset.kind),
        node("strong", {}, name), tagLabel("v" + asset.version)),
      list(children));
  }

  function rows(value, schema, depth, onlyAssets = false) {
    const shape = schemaShape(root, schema);
    if (!value || typeof value !== "object") return [];
    const items = Array.isArray(value)
      ? value.map((item, i) => [t("Item {number}", { number: i + 1 }), item, shape.items])
      : Object.entries(value).map(([key, item]) => {
        const fieldShape = Object.hasOwn(shape.properties ?? {}, key) ? shape.properties[key] : undefined;
        return [schemaLabel(fieldShape, key), item, fieldShape];
      });
    const result = [];
    for (const [label, item, fieldShape] of items) {
      if (limit(depth)) break;
      if (item && typeof item === "object" && Object.hasOwn(labels, item.kind) && typeof item.assetKey === "string" && Number.isInteger(item.version) && item.schema && item.config) {
        result.push(assetRow(label, item, rows(item.config, undefined, depth + 1, true)));
        continue;
      }
      if (item !== null && typeof item === "object" && !JSON.isRawJSON?.(item)) {
        const children = rows(item, fieldShape, depth + 1, onlyAssets);
        if (children.length) {
          result.push(node("li", { class: "outline-row" }, node("div", { class: "outline-field" }, label), list(children)));
          continue;
        }
        if (Object.keys(item).length) continue;
      }
      if (!onlyAssets) {
        result.push(node("li", { class: "outline-row outline-value" },
          node("span", { class: "outline-field" }, label), node("code", {}, JSON.stringify(item))));
      }
    }
    return result;
  }

  const items = data.schema
    ? rows(data.config, root, 0)
    : data.agents.map((agent) => assetRow(agent.slot, { ...agent, kind: "agent" }, [
      ...agent.skills.map((item) => assetRow(item.slot, { ...item, kind: "skill" }, rows(item.config, undefined, 1, true))),
      ...agent.prompts.map((item) => assetRow(item.slot, { ...item, kind: "prompt" }, rows(item.config, undefined, 1, true))),
      ...rows(agent.config, undefined, 0, true),
    ]));

  return node("div", { class: "preview-outline" },
    list(items) ?? node("p", { class: "subtle" }, t("No fields to preview.")),
    truncated ? node("p", { class: "subtle" }, t("Open JSON to see all fields.")) : null);
}

function compositionSchemaInspector(draft, definition, onVersionChange) {
  const schema = state.schemas.find((item) => item.key === draft.schema.key);
  const body = node("div", { class: "schema-inspector-content" });
  const versions = select(draft.versions.map((item) => [item.version, "v" + item.version]), draft.schema.version);
  const fields = button(t("Schema fields"), () => show("fields"), "small");
  const raw = button("JSON", () => show("json"), "small", { "aria-label": t("Schema JSON") });

  function show(mode) {
    draft.inspectorMode = mode;
    fields.setAttribute("aria-pressed", String(mode === "fields"));
    raw.setAttribute("aria-pressed", String(mode === "json"));
    const schema = definition();
    body.replaceChildren(mode === "fields"
      ? schemaFieldOutline(schema)
      : node("pre", { class: "json-output schema-definition", tabindex: "0", "aria-label": t("Schema JSON") },
        JSON.stringify(schema, null, 2)));
  }
  versions.addEventListener("change", () => {
    onVersionChange(Number(versions.value));
    show(draft.inspectorMode);
  });

  show(draft.inspectorMode);
  return node("section", { class: "panel schema-inspector", id: "composition-schema", tabindex: "-1", "aria-labelledby": "composition-schema-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "composition-schema-title" }, t("Composition schema reference"))),
    node("div", { class: "panel-body fields" },
      node("div", { class: "schema-inspector-meta" },
        node("div", { class: "schema-meta" }, node("strong", {}, schema ? displayName(schema) : draft.schema.key),
          node("div", {}, node("code", {}, draft.schema.key))),
        field(t("Schema version"), versions)),
      node("div", { class: "filters preview-modes" }, fields, raw),
      body));
}

const schemaTypeLabels = {
  string: "String type", boolean: "Boolean type", integer: "Integer type", number: "Number type",
  object: "Object type", array: "Array type", null: "Null type",
};

function schemaFieldOutline(definition) {
  let remaining = 160;
  let truncated = false;
  const root = definition ?? {};

  function fields(shape, depth, visited) {
    if (visited.has(shape) || depth >= 10 || remaining <= 0) {
      truncated = true;
      return [];
    }
    const next = new Set(visited).add(shape);
    const properties = Object.entries(shape.properties ?? {});
    const values = properties.map(([key, child]) => [key, child, shape.required?.includes(key)]);
    if (shape.items && typeof shape.items === "object") {
      values.push(["[]", shape.items, false]);
    }
    const items = [];
    for (const [key, child, required] of values) {
      if (remaining-- <= 0) {
        truncated = true;
        break;
      }
      const resolved = schemaShape(root, child);
      const caption = key === "[]" ? t("Array item") : schemaLabel(child, schemaLabel(resolved, key));
      const types = Array.isArray(resolved.type) ? resolved.type : [resolved.type];
      const declaredType = types.filter((type) => Object.hasOwn(schemaTypeLabels, type)).map((type) => t(schemaTypeLabels[type])).join(" / ");
      const inferredType = resolved.properties ? "Object type" : (resolved.items ? "Array type" : (resolved.enum ? "Enum type" : "Any type"));
      const chosenKinds = Array.isArray(resolved["x-asset"]) ? resolved["x-asset"] : [resolved["x-asset"]];
      const typeLabel = resolved["x-asset"]
        ? t("Asset reference") + " · " + chosenKinds.map((kind) => labels[kind]).join(" / ")
        : declaredType || t(inferredType);
      const children = fields(resolved, depth + 1, next);
      items.push(node("li", { class: "schema-inspector-item" },
        node("div", { class: "schema-inspector-field" },
          node("strong", {}, caption), caption !== key ? node("code", {}, key) : null),
        node("div", { class: "schema-inspector-tags" }, tagLabel(typeLabel), required ? tagLabel(t("Required"), "agent") : null),
        children.length ? node("ul", {}, children) : null));
    }
    return items;
  }

  const items = fields(schemaShape(root, root), 0, new Set());
  return node("div", { class: "schema-inspector-tree", tabindex: "0" },
    items.length ? node("ul", {}, items) : node("p", { class: "subtle" }, t("No named fields declared.")),
    truncated ? node("p", { class: "subtle" }, t("Use Schema JSON to inspect the complete definition.")) : null);
}
