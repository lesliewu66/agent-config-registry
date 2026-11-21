"use strict";

const workspace = document.querySelector("#workspace");
const dialog = document.querySelector("#dialog");
const dialogContent = document.querySelector("#dialog-content");
const notice = document.querySelector("#notice");
const state = {
  view: "experiments",
  experiments: [],
  schemas: [],
  assets: [],
  selected: null,
  agents: [],
  dirty: false,
  saving: false,
  filters: { assets: "all", schemas: "all" },
};

const labels = { agent: "Agent", skill: "Skill", prompt: "Prompt" };
const keyPattern = "[a-z0-9](?:[a-z0-9_]|-){0,63}";
const encode = encodeURIComponent;
const route = (tag) => "/experiments/" + encode(tag);
const versionRoute = (collection, key) => "/" + collection + "/" + encode(key) + "/versions";
const configPath = (tag, releaseId) => {
  const params = new URLSearchParams({ tag });
  if (releaseId) params.set("releaseId", releaseId);
  return "/config?" + params;
};

function parseJSON(text) {
  return JSON.parse(text, (_, value, context) => {
    if (typeof value !== "number" || !context?.source || !JSON.rawJSON) return value;
    return String(value) === context.source ? value : JSON.rawJSON(context.source);
  });
}

async function api(path, { method = "GET", payload, raw } = {}) {
  const options = { method };
  if (raw !== undefined || payload !== undefined) {
    options.headers = { "Content-Type": "application/json" };
    options.body = raw ?? JSON.stringify(payload);
  }
  const response = await fetch("/api/v1" + path, options);
  const data = parseJSON(await response.text());
  if (!response.ok) {
    const error = new Error(data.message ?? "Request failed");
    error.status = response.status;
    throw error;
  }
  return data;
}

function node(tag, attributes = {}, ...children) {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "on") {
      for (const [event, handler] of Object.entries(value)) element.addEventListener(event, handler);
    } else {
      element.setAttribute(key, value === true ? "" : String(value));
    }
  }
  element.append(...children.flat(Infinity).filter((child) => child != null && child !== false));
  return element;
}

function button(text, action, className = "", attributes = {}) {
  return node("button", {
    type: "button", class: ("button " + className).trim(), ...attributes,
    on: { click: () => Promise.resolve().then(action).catch(reportError) },
  }, text);
}

let noticeTimer;
function announce(text, isError = false) {
  clearTimeout(noticeTimer);
  notice.textContent = text;
  notice.className = "notice" + (isError ? " error" : "");
  notice.hidden = false;
  noticeTimer = setTimeout(() => { notice.hidden = true; }, isError ? 10000 : 4000);
}

function reportError(error) {
  const message = error instanceof TypeError ? "Could not reach the local registry." : error.message;
  if (dialog.open) {
    const inline = dialogContent.querySelector("[data-error]");
    if (inline) {
      inline.textContent = message;
      inline.hidden = false;
      return;
    }
  }
  announce(message, true);
}

let fieldCount = 0;
function field(label, input, hint) {
  const id = "field-" + (++fieldCount);
  input.id = id;
  if (hint) input.setAttribute("aria-describedby", id + "-hint");
  return node("div", { class: "field" }, node("label", { for: id }, label), input,
    hint ? node("small", { id: id + "-hint" }, hint) : null);
}

function input(attrs) {
  return node("input", attrs);
}

function select(options, value, placeholder) {
  const element = node("select");
  setOptions(element, options, value, placeholder);
  return element;
}

function setOptions(element, options, value, placeholder) {
  const items = options.map(([optionValue, label]) => node("option", { value: optionValue }, label));
  if (placeholder) items.unshift(node("option", { value: "" }, placeholder));
  element.replaceChildren(...items);
  if (value !== undefined) element.value = String(value);
}

function keyInput(value = "") {
  return input({ value, pattern: keyPattern, maxlength: 64, required: true, spellcheck: "false",
    title: "Lowercase letters, digits, hyphens and underscores. Start with a letter or digit." });
}

function modal(title, content) {
  document.querySelector("#dialog-title").textContent = title;
  dialogContent.replaceChildren(content);
  if (!dialog.open) dialog.showModal();
}

function formDialog(title, fields, submitLabel, onSubmit) {
  const error = node("p", { hidden: true, class: "error", role: "alert", "data-error": true });
  const submit = node("button", { type: "submit", class: "button primary" }, submitLabel);
  const form = node("form", { class: "form" }, fields, error,
    node("div", { class: "form-actions" }, button("Cancel", () => dialog.close()), submit));
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.hidden = true;
    submit.disabled = true;
    try {
      await onSubmit();
    } catch (failure) {
      reportError(failure);
    } finally {
      submit.disabled = false;
    }
  });
  modal(title, form);
}

document.querySelector("#close-dialog").addEventListener("click", () => dialog.close());
document.querySelectorAll("[data-view]").forEach((link) => link.addEventListener("click", () => {
  state.view = link.dataset.view;
  render();
}));
window.addEventListener("beforeunload", (event) => {
  if (state.dirty) event.preventDefault();
});

async function refreshLists() {
  const [experiments, schemas, assets] = await Promise.all([api("/experiments"), api("/schemas"), api("/assets")]);
  Object.assign(state, { experiments, schemas, assets });
}

async function loadExperiment(tag) {
  const result = await api(route(tag));
  state.selected = result;
  state.agents = structuredClone(result.agents);
  state.dirty = false;
}

async function switchExperiment(tag) {
  if (tag === state.selected?.tag) return;
  if (state.dirty && !window.confirm("Discard the unsaved draft edits?")) return;
  await loadExperiment(tag);
  render();
}

function render() {
  document.querySelectorAll("[data-view]").forEach((link) => {
    if (link.dataset.view === state.view) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  workspace.setAttribute("aria-busy", String(state.saving));
  workspace.inert = state.saving;
  workspace.replaceChildren(state.view === "experiments" ? experimentView() : catalogView(state.view));
}

function markDraft() {
  state.dirty = true;
  render();
}

function empty(title, detail, action) {
  return node("div", { class: "empty" }, node("h3", {}, title), node("p", {}, detail), action);
}

function tagLabel(text, kind = "") {
  return node("span", { class: "tag " + kind }, text);
}

function experimentView() {
  const sidebar = node("aside", { class: "sidebar" },
    node("div", { class: "sidebar-heading" }, node("h2", {}, "Experiments"),
      button("New", newExperimentDialog, "small", { "aria-label": "New experiment" })),
    node("div", { class: "experiment-list" }, state.experiments.map((item) =>
      node("button", {
        type: "button", class: "experiment-link",
        "aria-current": state.selected?.tag === item.tag ? "true" : null,
        on: { click: () => switchExperiment(item.tag).catch(reportError) },
      }, node("strong", {}, item.name), node("code", {}, item.tag)))));
  const experiment = state.selected;
  if (!experiment) {
    return node("div", { class: "shell" }, sidebar, empty("Create your first experiment",
      "Give a configuration group a tag, then add the Agent versions it should serve.",
      button("New experiment", newExperimentDialog, "primary")));
  }

  const title = node("div", { class: "section-title" },
    node("div", {}, node("h1", {}, experiment.name),
      node("p", { class: "subtitle" }, node("code", {}, "tag / " + experiment.tag),
        tagLabel("Draft r" + experiment.revision, state.dirty ? "draft" : ""),
        state.dirty ? tagLabel("Unsaved changes", "draft") : null)),
    node("div", { class: "page-actions" },
      button("Reload", async () => {
        if (state.dirty && !window.confirm("Discard the unsaved draft edits?")) return;
        await refreshLists();
        await loadExperiment(experiment.tag);
        render();
      }, "quiet"),
      button("Save draft", saveDraft, state.dirty ? "primary" : "", { disabled: !state.dirty }),
      button("Preview & publish", previewDraft, state.dirty ? "" : "primary", { disabled: state.dirty })));

  const tree = node("section", { class: "panel", "aria-labelledby": "tree-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "tree-title" }, "Configuration tree"),
      button("Add agent", () => bindingDialog(), "", { disabled: !state.assets.some((a) => a.kind === "agent") })),
    node("div", { class: "panel-body tree" },
      state.agents.length
        ? state.agents.map(agentCard)
        : empty("No agent bindings", "Add an Agent version to compose this experiment.",
          state.assets.some((a) => a.kind === "agent") ? null : button("Go to assets", () => {
            state.view = "assets";
            render();
          }, "quiet"))));
  return node("div", { class: "shell" }, sidebar,
    node("div", {}, title, node("div", { class: "detail-grid" }, tree, releasePanel(experiment))));
}

function agentCard(agent, index) {
  const asset = state.assets.find((item) => item.key === agent.assetKey);
  const editableDependencies = state.assets.some((item) => item.kind !== "agent");
  const dependencies = agent.dependencies.map((binding, childIndex) => {
    const item = state.assets.find((candidate) => candidate.key === binding.assetKey);
    return node("li", { class: "dependency" },
      node("div", {},
        node("div", { class: "asset-title" }, tagLabel(labels[item?.kind] || "Asset", item?.kind),
          node("strong", {}, item?.name ?? binding.assetKey), tagLabel("v" + binding.version)),
        node("div", { class: "ref-line mono" }, binding.slot + " / " + binding.assetKey)),
      node("div", { class: "row-actions" },
        button("Edit", () => bindingDialog(index, childIndex), "quiet small",
          { "aria-label": "Edit " + agent.slot + " / " + binding.slot }),
        button("×", () => { agent.dependencies.splice(childIndex, 1); markDraft(); }, "quiet small danger",
          { "aria-label": "Remove " + agent.slot + " / " + binding.slot })));
  });
  return node("article", { class: "agent-card", "aria-label": "Agent " + agent.slot },
    node("div", { class: "agent-head" },
      node("div", {},
        node("div", { class: "asset-title" }, tagLabel("Agent", "agent"),
          node("h3", {}, asset?.name ?? agent.assetKey), tagLabel("v" + agent.version)),
        node("div", { class: "ref-line mono" }, agent.slot + " / " + agent.assetKey)),
      node("div", { class: "row-actions" },
        button("Edit", () => bindingDialog(index), "quiet small", { "aria-label": "Edit agent " + agent.slot }),
        button("×", () => { state.agents.splice(index, 1); markDraft(); }, "quiet small danger",
          { "aria-label": "Remove agent " + agent.slot }))),
    node("ul", { class: "dependencies", "aria-label": "Dependencies of " + agent.slot },
      dependencies,
      node("li", { class: "add-dep" },
        button("Add dependency", () => bindingDialog(index, -1), "small", { disabled: !editableDependencies }))));
}

function releasePanel(experiment) {
  const current = experiment.releases.find((r) => r.releaseId === experiment.activeReleaseId);
  const body = current
    ? [node("code", { class: "release-id" }, current.releaseId),
      node("p", { class: "release-meta" }, tagLabel("Active", "live"), node("span", { class: "subtle" }, dateLabel(current.publishedAt))),
      button("View active JSON", () => viewRelease(experiment.tag, current.releaseId), "small")]
    : [node("p", { class: "panel-note" }, "This tag is served after the first release.")];
  return node("aside", { class: "panel", "aria-labelledby": "release-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "release-title" }, "Current release")),
    node("div", { class: "panel-body" }, body,
      experiment.releases.length ? node("div", { class: "release-history" },
        node("h3", {}, "History"),
        experiment.releases.map((release) => node("div", { class: "release" },
          node("code", {}, release.releaseId),
          node("div", { class: "row-actions" },
            button("View", () => viewRelease(experiment.tag, release.releaseId), "quiet small",
              { "aria-label": "View " + release.releaseId }),
            current?.releaseId !== release.releaseId
              ? button("Activate", () => activateRelease(experiment, release.releaseId), "small",
                { "aria-label": "Activate " + release.releaseId }) : tagLabel("Active", "live"))))) : null));
}

function dateLabel(timestamp) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(timestamp));
}

function newExperimentDialog() {
  const name = input({ required: true, maxlength: 80, placeholder: "Support routing" });
  const tag = keyInput();
  formDialog("New experiment", [field("Name", name), field("Experiment tag", tag, "Used by clients to retrieve the active release.")],
    "Create experiment", async () => {
      if (state.dirty && !window.confirm("Discard the unsaved draft edits?")) return;
      const created = await api("/experiments", { method: "POST", payload: { name: name.value.trim(), tag: tag.value } });
      await refreshLists();
      await loadExperiment(created.tag);
      dialog.close();
      render();
      announce("Experiment created.");
    });
}

async function bindingDialog(agentIndex = -1, depIndex) {
  const isDependency = depIndex !== undefined;
  const agent = state.agents[agentIndex];
  const existing = isDependency ? agent.dependencies[depIndex] : agent;
  const candidates = state.assets.filter((a) => isDependency ? a.kind !== "agent" : a.kind === "agent");
  if (!candidates.length) throw new Error("Create an eligible asset first.");
  const slot = keyInput(existing?.slot ?? "");
  const assetSelect = select(candidates.map((a) => [a.key, labels[a.kind] + " · " + a.name]), existing?.assetKey);
  const versionSelect = select([]);
  const usedSlots = (isDependency ? agent.dependencies : state.agents).filter((binding) => binding !== existing);
  let sequence = 0;
  let versionError;
  async function loadVersions(preselected) {
    const id = ++sequence;
    const key = assetSelect.value;
    setOptions(versionSelect, [], undefined, "Loading versions…");
    versionSelect.disabled = true;
    versionError = undefined;
    try {
      const versions = await api(versionRoute("assets", key));
      if (id !== sequence) return;
      setOptions(versionSelect, versions.map((v) => [v.version, "v" + v.version]), preselected);
      versionSelect.disabled = false;
    } catch (error) {
      if (id === sequence) {
        versionError = error;
        setOptions(versionSelect, [], undefined, "Versions unavailable");
        reportError(error);
      }
    }
  }
  function suggestSlot() {
    const key = assetSelect.value;
    let suggestion = key;
    for (let suffix = 2; usedSlots.some((ref) => ref.slot === suggestion); suffix++) {
      const marker = "-" + suffix;
      suggestion = key.slice(0, 64 - marker.length) + marker;
    }
    slot.value = suggestion;
  }
  if (!existing) suggestSlot();
  assetSelect.addEventListener("change", () => {
    if (!existing) suggestSlot();
    void loadVersions();
  });
  formDialog(existing ? "Edit binding" : (isDependency ? "Add dependency" : "Add agent"),
    [field("Binding name", slot), node("div", { class: "form-columns" }, field("Asset", assetSelect), field("Version", versionSelect))],
    existing ? "Apply binding" : "Add binding", async () => {
      if (versionError) throw versionError;
      if (versionSelect.disabled || !versionSelect.value) throw new Error("Select an available asset version.");
      if (usedSlots.some((ref) => ref.slot === slot.value)) throw new Error("This binding name is already in use.");
      const ref = { slot: slot.value, assetKey: assetSelect.value, version: Number(versionSelect.value) };
      if (isDependency) {
        if (existing) agent.dependencies[depIndex] = ref;
        else agent.dependencies.push(ref);
      } else if (existing) {
        state.agents[agentIndex] = { ...ref, dependencies: agent.dependencies };
      } else {
        state.agents.push({ ...ref, dependencies: [] });
      }
      dialog.close();
      markDraft();
    });
  void loadVersions(existing?.version);
}

async function saveDraft() {
  if (state.saving) return;
  const experiment = state.selected;
  const tag = experiment.tag;
  const payload = { expectedRevision: experiment.revision, agents: state.agents };
  state.saving = true;
  render();
  try {
    await api(route(tag) + "/draft", { method: "PUT", payload });
    await refreshLists();
    await loadExperiment(tag);
    announce("Draft saved.");
  } finally {
    state.saving = false;
    render();
  }
}

function viewJSON(title, data, controls = []) {
  const rendered = JSON.stringify(data, null, 2);
  modal(title, node("section", {},
    node("p", { hidden: true, class: "error", role: "alert", "data-error": true }),
    node("pre", { class: "json-output", tabindex: "0" }, rendered),
    node("div", { class: "form-actions" },
      button("Copy JSON", async () => { await navigator.clipboard.writeText(rendered); announce("JSON copied."); }), controls)));
}

async function previewDraft() {
  const experiment = state.selected;
  const data = await api(route(experiment.tag) + "/preview");
  if (data.draftRevision !== experiment.revision) {
    throw new Error("The draft changed. Reload it before publishing.");
  }
  const alreadyReleased = experiment.releases.some((r) => r.draftRevision === data.draftRevision);
  let publishButton;
  publishButton = button("Publish release", async () => {
    publishButton.disabled = true;
    try {
      const snapshot = await api(route(experiment.tag) + "/releases", { method: "POST", payload: {
        expectedRevision: data.draftRevision,
        expectedActiveReleaseId: experiment.activeReleaseId,
      } });
      await refreshLists();
      await loadExperiment(experiment.tag);
      dialog.close();
      render();
      announce("Published " + snapshot.releaseId + ".");
    } finally {
      publishButton.disabled = false;
    }
  }, "primary", { disabled: !data.agents.length || alreadyReleased });
  const hint = alreadyReleased
    ? node("p", { class: "subtle" }, "This revision is already in release history.")
    : (!data.agents.length ? node("p", { class: "subtle" }, "Add at least one Agent before publishing.") : null);
  viewJSON("Preview · draft r" + data.draftRevision, data, [hint, publishButton]);
}

async function viewRelease(tag, releaseId) {
  const path = configPath(tag, releaseId);
  const data = await api(path);
  viewJSON(data.releaseId, data, [node("a", { class: "button", href: "/api/v1" + path,
    target: "_blank", rel: "noopener" }, "Open API response")]);
}

function activateRelease(experiment, releaseId) {
  formDialog("Activate " + releaseId,
    [node("p", {}, "Requests for ", node("code", {}, experiment.tag), " will serve this snapshot.")],
    "Activate release", async () => {
      await api(route(experiment.tag) + "/active-release", { method: "PUT", payload: {
        releaseId,
        expectedActiveReleaseId: experiment.activeReleaseId,
      } });
      await refreshLists();
      if (state.dirty) state.selected = { ...state.selected, activeReleaseId: releaseId };
      else await loadExperiment(experiment.tag);
      dialog.close();
      render();
      announce("Active release is now " + releaseId + ".");
    });
}

function catalogView(collection) {
  const isSchema = collection === "schemas";
  const title = isSchema ? "Schemas" : "Assets";
  const filter = state.filters[collection];
  const items = state[collection].filter((item) => filter === "all" || item.kind === filter);
  const create = () => isSchema ? schemaDialog() : assetDialog();
  const filterButtons = ["all", "agent", "skill", "prompt"].map((kind) => button(
    kind === "all" ? "All" : labels[kind], () => { state.filters[collection] = kind; render(); },
    "small " + (filter === kind ? "selected" : ""), { "aria-pressed": String(filter === kind) }));
  return node("section", { class: "catalog" },
    node("div", { class: "section-title" }, node("h1", {}, title),
      button(isSchema ? "New schema" : "Import asset", create, "primary")),
    node("section", { class: "panel" },
      node("div", { class: "panel-header filters", "aria-label": "Filter by asset type" }, filterButtons),
      items.length ? node("div", { class: "catalog-list" }, items.map((item) =>
        node("div", { class: "catalog-row" },
          node("div", {}, node("h3", {}, item.name),
            node("p", { class: "ref-line mono" }, item.key + (isSchema ? "" : " / " + item.schema.key + "@" + item.schema.version))),
          tagLabel(labels[item.kind], item.kind),
          button("Versions · v" + item.latestVersion, () => versionsDialog(collection, item), "small",
            { "aria-label": "Versions for " + item.name }))))
        : node("div", { class: "panel-body" }, empty("No " + title.toLowerCase() + " in this view",
          isSchema ? "Register a JSON Schema to validate asset versions." : "Import JSON using a registered Schema version.",
          null))));
}

function jsonEditor(label, initial, rows = 10) {
  const area = node("textarea", { rows, spellcheck: "false", required: true, wrap: "off" });
  area.value = typeof initial === "string" ? initial : JSON.stringify(initial, null, 2);
  const file = input({ type: "file", accept: ".json,application/json" });
  const container = node("div", { class: "fields" },
    field(label, area), field("Load JSON file", file));
  file.addEventListener("change", async () => {
    const chosen = file.files[0];
    if (!chosen) return;
    try {
      if (chosen.size > 1 << 20) throw new Error("Choose a JSON file smaller than 1 MiB.");
      area.value = await chosen.text();
      area.dispatchEvent(new Event("change"));
    } catch (error) {
      reportError(error);
    } finally {
      file.value = "";
    }
  });
  return { area, container };
}

function objectFromJSON(source) {
  const value = parseJSON(source);
  if (value === null || Array.isArray(value) || typeof value !== "object") {
    throw new Error("The JSON must contain an object.");
  }
  return value;
}

function jsonPayload(fields, property, source) {
  objectFromJSON(source);
  return JSON.stringify(fields).slice(0, -1) + "," + JSON.stringify(property) + ":" + source.trim() + "}";
}

function schemaDialog(item, source) {
  const key = keyInput(item?.key ?? "");
  const name = input({ required: true, maxlength: 80 });
  const kind = select(Object.entries(labels));
  const root = { "$schema": "https://json-schema.org/draft/2020-12/schema", type: "object",
    properties: { message: { type: "string" } }, required: ["message"] };
  const editor = jsonEditor("JSON Schema", source?.definition ?? root);
  const fields = item ? [editor.container] : [field("Name", name),
    node("div", { class: "form-columns" }, field("Schema key", key), field("Asset type", kind)),
    editor.container];
  formDialog(item ? "New schema version" : "New schema", fields, item ? "Create version" : "Create schema", async () => {
    const path = item ? versionRoute("schemas", item.key) : "/schemas";
    const meta = item ? { baseVersion: item.latestVersion } : { key: key.value, name: name.value.trim(), kind: kind.value };
    await api(path, { method: "POST", raw: jsonPayload(meta, "definition", editor.area.value) });
    await refreshLists();
    dialog.close();
    render();
    announce(item ? "Schema version created." : "Schema created.");
  });
}

function usableProperties(schema) {
  return Object.entries(schema?.properties ?? {}).filter(([, definition]) => {
    if (!definition || typeof definition !== "object") return false;
    if (Array.isArray(definition.enum) && definition.enum.length && definition.enum.every((value) =>
      value === null || ["string", "number", "boolean"].includes(typeof value))) return true;
    return ["string", "integer", "number", "boolean"].includes(definition.type);
  });
}

function generatedFields(properties, required, editor) {
  const values = Object.assign(Object.create(null), objectFromJSON(editor.area.value));
  return properties.map(([key, definition]) => {
    const current = values[key];
    let control;
    let read;
    const named = definition.title || key;
    const hasEnum = Array.isArray(definition.enum) && definition.enum.length && definition.enum.every((value) =>
      value === null || ["string", "number", "boolean"].includes(typeof value));
    if (hasEnum) {
      const index = definition.enum.findIndex((value) => Object.is(value, current));
      control = select(definition.enum.map((value, i) => [i, typeof value === "string" ? value : JSON.stringify(value)]),
        index === -1 ? "" : index, "Select a value");
      read = () => control.value === "" ? undefined : definition.enum[Number(control.value)];
    } else if (definition.type === "boolean") {
      control = select([["true", "True"], ["false", "False"]], current === undefined ? "" : current, "Select a value");
      read = () => control.value === "" ? undefined : control.value === "true";
    } else if (definition.type === "string") {
      control = input({ value: typeof current === "string" ? current : "" });
      read = () => control.value === "" && !required.includes(key) && current === undefined ? undefined : control.value;
    } else {
      control = input({ type: "number", step: definition.type === "integer" ? "1" : "any",
        value: current === undefined ? "" : JSON.stringify(current) });
      read = () => control.value === "" ? undefined : parseJSON(control.value);
    }
    control.required = required.includes(key);
    control.addEventListener("input", () => {
      if (control.validity.badInput) return;
      const value = read();
      if (value === undefined) delete values[key];
      else values[key] = value;
      editor.area.value = JSON.stringify(values, null, 2);
    });
    return field(named, control, typeof definition.description === "string" ? definition.description : undefined);
  });
}

async function assetDialog(item, source) {
  const eligible = state.schemas.filter((entry) => !item || item.kind === entry.kind);
  if (!eligible.length) {
    modal("Register a schema first", node("div", { class: "form" },
      node("p", {}, "A Schema version validates the JSON you import."),
      node("div", { class: "form-actions" }, button("New schema", () => {
        state.view = "schemas";
        render();
        schemaDialog();
      }, "primary"))));
    return;
  }

  const key = keyInput(item?.key ?? "");
  const name = input({ required: true, maxlength: 80 });
  const schemaSelect = select(eligible.map((entry) => [entry.key, labels[entry.kind] + " · " + entry.name]), source?.schema.key);
  const schemaVersion = select([]);
  const editor = jsonEditor("Config JSON", source?.config ?? {});
  const fieldsArea = node("div", { class: "fields", hidden: true });
  let schemaVersions = [];
  let sequence = 0;
  let formMode = false;
  let showForm;
  const definition = () => schemaVersions.find((entry) => entry.version === Number(schemaVersion.value))?.definition;

  function renderEditor() {
    const schema = definition();
    const properties = usableProperties(schema);
    showForm.disabled = properties.length === 0;
    if (!properties.length) formMode = false;
    if (formMode) {
      try {
        fieldsArea.replaceChildren(...generatedFields(properties, Array.isArray(schema.required) ? schema.required : [], editor));
      } catch (error) {
        formMode = false;
        reportError(error);
      }
    }
    showForm.textContent = formMode ? "Edit raw JSON" : "Use schema form";
    fieldsArea.hidden = !formMode;
    editor.container.hidden = formMode;
    editor.area.required = !formMode;
    if (!formMode) fieldsArea.replaceChildren();
  }

  showForm = button("Use schema form", () => { formMode = !formMode; renderEditor(); }, "small", { disabled: true });

  async function loadSchemas(selectedVersion) {
    const id = ++sequence;
    schemaVersions = [];
    schemaVersion.disabled = true;
    setOptions(schemaVersion, [], undefined, "Loading versions…");
    renderEditor();
    try {
      const result = await api(versionRoute("schemas", schemaSelect.value));
      if (id !== sequence) return;
      schemaVersions = result;
      setOptions(schemaVersion, result.map((entry) => [entry.version, "v" + entry.version]), selectedVersion);
      schemaVersion.disabled = false;
      renderEditor();
    } catch (error) {
      if (id === sequence) {
        setOptions(schemaVersion, [], undefined, "Versions unavailable");
        reportError(error);
      }
    }
  }

  schemaSelect.addEventListener("change", () => { void loadSchemas(); });
  schemaVersion.addEventListener("change", renderEditor);
  const fields = [
    item ? null : node("div", { class: "form-columns" }, field("Name", name), field("Asset key", key)),
    node("div", { class: "form-columns" }, field("Schema", schemaSelect), field("Schema version", schemaVersion)),
    node("div", { class: "editor-tools" }, showForm), editor.container, fieldsArea,
  ];
  formDialog(item ? "New asset version" : "Import asset", fields, item ? "Create version" : "Create asset", async () => {
    if (!definition()) throw new Error("Select an available Schema version.");
    const schema = { key: schemaSelect.value, version: Number(schemaVersion.value) };
    const meta = item ? { schema, baseVersion: item.latestVersion } : { schema, key: key.value, name: name.value.trim() };
    const path = item ? versionRoute("assets", item.key) : "/assets";
    await api(path, { method: "POST", raw: jsonPayload(meta, "config", editor.area.value) });
    await refreshLists();
    dialog.close();
    render();
    announce(item ? "Asset version created." : "Asset imported.");
  });
  await loadSchemas(source?.schema.version);
}

async function versionsDialog(collection, item) {
  const entries = await api(versionRoute(collection, item.key));
  const isSchema = collection === "schemas";
  const versionSelect = select(entries.map((entry) => [entry.version, "v" + entry.version + " · " + dateLabel(entry.createdAt)]));
  const data = node("pre", { class: "json-output", tabindex: "0" });
  const selected = () => entries.find((entry) => entry.version === Number(versionSelect.value));
  const show = () => { data.textContent = JSON.stringify(isSchema ? selected().definition : selected().config, null, 2); };
  versionSelect.addEventListener("change", show);
  show();
  const latest = { ...item, latestVersion: entries[0].version };
  modal(item.name, node("div", { class: "form" }, field("Version", versionSelect), data,
    node("div", { class: "form-actions" }, button("Create next version",
      () => isSchema ? schemaDialog(latest, selected()) : assetDialog(latest, selected()), "primary"))));
}

async function start() {
  await refreshLists();
  if (state.experiments.length) await loadExperiment(state.experiments[0].tag);
  render();
}

start().catch((error) => {
  workspace.removeAttribute("aria-busy");
  workspace.replaceChildren(empty("Could not load the registry", error.message,
    button("Retry", start, "primary")));
});
