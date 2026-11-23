"use strict";

const workspace = document.querySelector("#workspace");
const dialog = document.querySelector("#dialog");
const dialogContent = document.querySelector("#dialog-content");
const notice = document.querySelector("#notice");
const languageButton = document.querySelector("#switch-language");
const viewLabels = { experiments: "Compositions", assets: "Assets", schemas: "Schemas" };
const state = {
  view: "experiments",
  experiments: [],
  schemas: [],
  assets: [],
  selected: null,
  agents: [],
  dirty: false,
  saving: false,
  loading: true,
  loadError: null,
  schemaDraft: null,
  schemaScope: "asset",
  filters: { assets: "all", schemas: "all" },
};

const labels = { agent: "Agent", skill: "Skill", prompt: "Prompt", experiment: "Experiment" };
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
    const error = new Error(data.message ?? t("Request failed"));
    error.status = response.status;
    error.code = data.code;
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
function announce(text, isError = false, detail) {
  clearTimeout(noticeTimer);
  notice.replaceChildren(text, detail ?? "");
  notice.className = "notice" + (isError ? " error" : "");
  notice.hidden = false;
  noticeTimer = setTimeout(() => { notice.hidden = true; }, isError ? 10000 : 4000);
}

const apiErrorMessages = {
  invalid_request: "The request is invalid.",
  not_found: "The requested item was not found.",
  version_conflict: "The data changed. Reload and try again.",
  validation_failed: "Validation failed. Check the configuration and references.",
  request_too_large: "The request is too large.",
  internal_error: "The registry could not complete the operation.",
  unsupported_media_type: "The request must contain JSON.",
  invalid_host: "Use the local address to access the registry.",
};

function errorText(error) {
  if (error instanceof TypeError) return t("Could not reach the local registry.");
  if (error instanceof SyntaxError) return t("JSON syntax is invalid.");
  const apiMessage = apiErrorMessages[error.code];
  return locale === "zh-CN" && apiMessage ? t(apiMessage) : error.message;
}

function reportError(error) {
  const message = errorText(error);
  const detail = message !== error.message && (error.code || error instanceof SyntaxError)
    ? node("details", { lang: "en" }, node("summary", { lang: locale }, t("Technical details")), error.message)
    : undefined;
  if (dialog.open) {
    const inline = dialogContent.querySelector("[data-error]");
    if (inline) {
      inline.replaceChildren(message);
      if (message !== error.message && (error.code || error instanceof SyntaxError)) {
        inline.append(detail);
      }
      inline.hidden = false;
      return;
    }
  }
  announce(message, true, detail);
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
    title: t("Lowercase letters, digits, hyphens and underscores. Start with a letter or digit.") });
}

function modal(title, content) {
  document.querySelector("#dialog-title").textContent = title;
  dialogContent.replaceChildren(content);
  if (!dialog.open) dialog.showModal();
}

function formDialog(title, fields, submitLabel, onSubmit) {
  const error = node("div", { hidden: true, class: "error", role: "alert", "data-error": true });
  const submit = node("button", { type: "submit", class: "button primary" }, submitLabel);
  const form = node("form", { class: "form" }, fields, error,
    node("div", { class: "form-actions" }, button(t("Cancel"), () => dialog.close()), submit));
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
languageButton.addEventListener("click", () => {
  setLocale(locale === "zh-CN" ? "en" : "zh-CN");
  notice.hidden = true;
  render();
});
window.addEventListener("beforeunload", (event) => {
  if (state.dirty) event.preventDefault();
});

async function refreshLists() {
  const [experiments, schemas, assets] = await Promise.all([api("/experiments"), api("/schemas"), api("/assets")]);
  Object.assign(state, { experiments, schemas, assets });
}

async function loadExperiment(tag) {
  const result = await api(route(tag));
  const versions = result.schema ? await api(versionRoute("schemas", result.schema.key)) : [];
  state.schemaDraft = result.schema
    ? { schema: { ...result.schema }, versions, source: JSON.stringify(result.config ?? {}, null, 2), rawMode: false, inspectorMode: "fields" }
    : null;
  state.selected = result;
  state.agents = structuredClone(result.agents);
  state.dirty = false;
}

async function switchExperiment(tag) {
  if (tag === state.selected?.tag) return;
  if (state.dirty && !window.confirm(t("Discard the unsaved draft edits?"))) return;
  await loadExperiment(tag);
  render();
}

function render() {
  document.documentElement.lang = locale;
  document.querySelector(".nav").setAttribute("aria-label", t("Workspace"));
  document.querySelector("#close-dialog").setAttribute("aria-label", t("Close dialog"));
  languageButton.textContent = locale === "zh-CN" ? "English" : "中文";
  languageButton.setAttribute("aria-label", locale === "zh-CN" ? "Switch to English" : "切换到中文");
  languageButton.setAttribute("lang", locale === "zh-CN" ? "en" : "zh-CN");
  document.querySelectorAll("[data-view]").forEach((link) => {
    link.textContent = t(viewLabels[link.dataset.view]);
    if (link.dataset.view === state.view) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  workspace.setAttribute("aria-busy", String(state.saving || state.loading));
  if (state.loading) {
    workspace.replaceChildren(node("p", { class: "muted" }, t("Loading workspace…")));
    return;
  }
  if (state.loadError) {
    workspace.replaceChildren(empty(t("Could not load the registry"), errorText(state.loadError),
      button(t("Retry"), start, "primary")));
    return;
  }
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
    node("div", { class: "sidebar-heading" }, node("h2", {}, t("Configuration groups")),
      state.experiments.length ? button(t("New"), newExperimentDialog, "small", { "aria-label": t("New composition") }) : null),
    node("div", { class: "experiment-list" }, state.experiments.map((item) =>
      node("button", {
        type: "button", class: "experiment-link",
        "aria-current": state.selected?.tag === item.tag ? "true" : null,
        on: { click: () => switchExperiment(item.tag).catch(reportError) },
      }, node("strong", {}, displayName(item)), node("code", {}, item.tag)))));
  const experiment = state.selected;
  if (!experiment) {
    return node("div", { class: "shell" }, sidebar, empty(t("Compose your first configuration"),
      t("Choose a schema, fill its fields with asset versions and publish the group."),
      button(t("New composition"), newExperimentDialog, "primary")));
  }

  const unsaved = tagLabel(t("Unsaved changes"), "draft");
  unsaved.hidden = !state.dirty;
  const saveButton = button(t("Save draft"), saveDraft, state.dirty ? "primary" : "", { disabled: !state.dirty });
  const previewButton = button(t("Preview & publish"), previewDraft, state.dirty ? "" : "primary", {
    disabled: state.dirty || (Boolean(experiment.schema) && !experiment.config),
  });
  const editSchemaDraft = () => {
    state.dirty = true;
    unsaved.hidden = false;
    saveButton.disabled = false;
    saveButton.classList.add("primary");
    previewButton.disabled = true;
    previewButton.classList.remove("primary");
  };
  const title = node("div", { class: "section-title" },
    node("div", {}, node("h1", {}, displayName(experiment)),
      node("p", { class: "subtitle" }, node("code", {}, t("tag") + " / " + experiment.tag),
        tagLabel(t("Draft r{revision}", { revision: experiment.revision }), state.dirty ? "draft" : ""),
        unsaved)),
    node("div", { class: "page-actions" },
      button(t("Reload"), async () => {
        if (state.dirty && !window.confirm(t("Discard the unsaved draft edits?"))) return;
        await refreshLists();
        await loadExperiment(experiment.tag);
        render();
      }, "quiet"),
      saveButton, previewButton));

  if (state.schemaDraft) {
    const { editor, inspector } = schemaDraftPanel(editSchemaDraft);
    return node("div", { class: "shell" }, sidebar, node("div", {}, title,
      node("div", { class: "detail-grid with-schema" }, editor,
        node("div", { class: "composition-sidebar" }, inspector, releasePanel(experiment)))));
  }

  const tree = node("section", { class: "panel", "aria-labelledby": "tree-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "tree-title" }, t("Legacy Agent bindings")),
      button(t("Add agent"), () => bindingDialog(), "", { disabled: !state.assets.some((a) => a.kind === "agent") })),
    node("div", { class: "panel-body tree" },
      state.agents.length
        ? state.agents.map(agentCard)
        : empty(t("No agent bindings"), t("Add an Agent version to compose this experiment."),
          state.assets.some((a) => a.kind === "agent") ? null : button(t("Go to assets"), () => {
            state.view = "assets";
            render();
          }, "quiet"))));
  return node("div", { class: "shell" }, sidebar,
    node("div", {}, title, node("div", { class: "detail-grid" }, tree, releasePanel(experiment))));
}

function schemaDraftPanel(edited) {
  const draft = state.schemaDraft;
  const definition = () => draft.versions.find((item) => item.version === draft.schema.version)?.definition;
  const panel = node("div", { class: "fields" });
  const editButton = button("", () => {
    draft.rawMode = !draft.rawMode;
    draw();
  }, "small");

  function rawEditor() {
    const editor = jsonEditor(t("Config JSON"), draft.source);
    editor.area.addEventListener("input", () => { draft.source = editor.area.value; edited(); });
    editor.area.addEventListener("change", () => { draft.source = editor.area.value; edited(); });
    return editor.container;
  }

  function draw() {
    const schema = definition();
    const supported = schema && schemaShape(schema, schema).properties;
    if (!supported) draft.rawMode = true;
    editButton.textContent = draft.rawMode ? t("Use schema form") : t("Edit raw JSON");
    editButton.disabled = !supported;
    if (!draft.rawMode) {
      try {
        const config = objectFromJSON(draft.source);
        panel.replaceChildren(...renderStructuredFields(schema, config, (value) => {
          draft.source = JSON.stringify(value, null, 2);
          edited();
        }));
        return;
      } catch (error) {
        draft.rawMode = true;
        editButton.textContent = t("Use schema form");
        reportError(error);
      }
    }
    panel.replaceChildren(rawEditor());
  }

  const inspector = compositionSchemaInspector(draft, definition, (version) => {
    draft.schema.version = version;
    edited();
    draw();
  });
  draw();
  const editor = node("section", { class: "panel", "aria-labelledby": "schema-config-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "schema-config-title" }, t("Composition fields")),
      node("div", { class: "row-actions" }, node("a", {
        href: "#composition-schema", class: "button small schema-shortcut",
        on: { click: (event) => {
          event.preventDefault();
          inspector.scrollIntoView({ block: "start" });
          inspector.focus({ preventScroll: true });
        } },
      }, t("View schema")), editButton)),
    node("div", { class: "panel-body fields" }, panel));
  return { editor, inspector };
}


function agentCard(agent, index) {
  const asset = state.assets.find((item) => item.key === agent.assetKey);
  const editableDependencies = state.assets.some((item) => item.kind !== "agent");
  const dependencies = agent.dependencies.map((binding, childIndex) => {
    const item = state.assets.find((candidate) => candidate.key === binding.assetKey);
    return node("li", { class: "dependency" },
      node("div", {},
        node("div", { class: "asset-title" }, tagLabel(labels[item?.kind] || t("Asset"), item?.kind),
          node("strong", {}, item ? displayName(item) : binding.assetKey), tagLabel("v" + binding.version)),
        node("div", { class: "ref-line mono" }, binding.slot + " / " + binding.assetKey)),
      node("div", { class: "row-actions" },
        button(t("Edit"), () => bindingDialog(index, childIndex), "quiet small",
          { "aria-label": t("Edit {binding}", { binding: agent.slot + " / " + binding.slot }) }),
        button("×", () => { agent.dependencies.splice(childIndex, 1); markDraft(); }, "quiet small danger",
          { "aria-label": t("Remove {binding}", { binding: agent.slot + " / " + binding.slot }) })));
  });
  return node("article", { class: "agent-card", "aria-label": "Agent " + agent.slot },
    node("div", { class: "agent-head" },
      node("div", {},
        node("div", { class: "asset-title" }, tagLabel("Agent", "agent"),
          node("h3", {}, asset ? displayName(asset) : agent.assetKey), tagLabel("v" + agent.version)),
        node("div", { class: "ref-line mono" }, agent.slot + " / " + agent.assetKey)),
      node("div", { class: "row-actions" },
        button(t("Edit"), () => bindingDialog(index), "quiet small", { "aria-label": t("Edit agent {slot}", { slot: agent.slot }) }),
        button("×", () => { state.agents.splice(index, 1); markDraft(); }, "quiet small danger",
          { "aria-label": t("Remove agent {slot}", { slot: agent.slot }) }))),
    node("ul", { class: "dependencies", "aria-label": t("Dependencies of {slot}", { slot: agent.slot }) },
      dependencies,
      node("li", { class: "add-dep" },
        button(t("Add dependency"), () => bindingDialog(index, -1), "small", { disabled: !editableDependencies }))));
}

function releasePanel(experiment) {
  const current = experiment.releases.find((r) => r.releaseId === experiment.activeReleaseId);
  const body = current
    ? [node("code", { class: "release-id" }, current.releaseId),
      node("p", { class: "release-meta" }, tagLabel(t("Active"), "live"), node("span", { class: "subtle" }, dateLabel(current.publishedAt))),
      button(t("View active release"), () => viewRelease(experiment.tag, current.releaseId), "small"),
      node("div", { class: "release-history" }, node("h3", {}, t("Lookup by tag")),
        node("a", { class: "api-path mono", href: "/api/v1" + configPath(experiment.tag), target: "_blank", rel: "noopener",
          "aria-label": t("Open active API response") }, "GET " + "/api/v1" + configPath(experiment.tag)))]
    : [node("p", { class: "panel-note" }, t("This tag is served after the first release."))];
  return node("aside", { class: "panel", "aria-labelledby": "release-title" },
    node("div", { class: "panel-header" }, node("h2", { id: "release-title" }, t("Current release"))),
    node("div", { class: "panel-body" }, body,
      experiment.releases.length ? node("details", { class: "release-history release-details" },
        node("summary", {}, t("Publication history ({count})", { count: experiment.releases.length })),
        experiment.releases.map((release) => node("div", { class: "release" },
          node("code", {}, release.releaseId),
          node("div", { class: "row-actions" },
            button(t("View"), () => viewRelease(experiment.tag, release.releaseId), "quiet small",
              { "aria-label": t("View {releaseId}", { releaseId: release.releaseId }) }),
            current?.releaseId !== release.releaseId
              ? button(t("Activate"), () => activateRelease(experiment, release.releaseId), "small",
                { "aria-label": t("Activate {releaseId}", { releaseId: release.releaseId }) }) : tagLabel(t("Active"), "live"))))) : null));
}

function dateLabel(timestamp) {
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(timestamp));
}

function newExperimentDialog(initial = {}) {
  const name = input({ required: true, maxlength: 80, value: initial.name ?? "", placeholder: locale === "zh-CN" ? "客服路由配置" : "Support routing" });
  const tag = keyInput(initial.tag ?? "");
  const schemas = state.schemas.filter((item) => item.kind === "experiment");
  const schema = select(schemas.map((item) => [item.key, displayName(item)]));
  const version = select([]);
  const legacy = input({ type: "checkbox" });
  const schemaFields = node("div", { class: "form-columns" },
    field(t("Composition schema"), schema), field(t("Schema version"), version));
  const noSchema = node("div", { class: "fields" },
    node("p", { class: "subtle" }, t("Create a composition schema before creating this group.")),
    node("div", {}, button(t("Create composition schema"), () => schemaDialog(undefined, undefined, {
      kind: "experiment",
      onSaved: () => newExperimentDialog({ name: name.value, tag: tag.value }),
    }), "small")));
  const schemaHint = node("p", { class: "subtle", hidden: !schemas.length },
    t("Use a composition schema to define which assets and parameters this group contains."));
  const legacyHint = node("p", { class: "subtle", hidden: true }, t("Fixed Agent bindings use the original editor."));

  function showOptions() {
    schemaFields.hidden = legacy.checked || !schemas.length;
    noSchema.hidden = legacy.checked || Boolean(schemas.length);
    schemaHint.hidden = legacy.checked || !schemas.length;
    legacyHint.hidden = !legacy.checked;
    const entry = schemas.find((item) => item.key === schema.value);
    const versions = entry ? Array.from({ length: entry.latestVersion }, (_, i) => entry.latestVersion - i) : [];
    setOptions(version, versions.map((n) => [n, "v" + n]));
  }
  legacy.addEventListener("change", showOptions);
  schema.addEventListener("change", showOptions);
  showOptions();
  formDialog(t("New composition"), [field(t("Name"), name), field(t("Experiment tag"), tag, t("Used by clients to retrieve the active release.")),
    schemaFields, schemaHint, noSchema, node("label", { class: "legacy-option" }, legacy, t("Use existing Agent bindings")), legacyHint],
    t("Create composition"), async () => {
      if (state.dirty && !window.confirm(t("Discard the unsaved draft edits?"))) return;
      const payload = { name: name.value.trim(), tag: tag.value };
      if (!legacy.checked) {
        if (!schema.value || !version.value) throw new Error(t("Choose an available composition schema."));
        payload.schema = { key: schema.value, version: Number(version.value) };
      }
      const created = await api("/experiments", { method: "POST", payload });
      await refreshLists();
      await loadExperiment(created.tag);
      dialog.close();
      render();
      announce(t("Composition created."));
    });
}

async function bindingDialog(agentIndex = -1, depIndex) {
  const isDependency = depIndex !== undefined;
  const agent = state.agents[agentIndex];
  const existing = isDependency ? agent.dependencies[depIndex] : agent;
  const candidates = state.assets.filter((a) => isDependency ? a.kind !== "agent" : a.kind === "agent");
  if (!candidates.length) throw new Error(t("Create an eligible asset first."));
  const slot = keyInput(existing?.slot ?? "");
  const assetSelect = select(candidates.map((a) => [a.key, labels[a.kind] + " · " + displayName(a)]), existing?.assetKey);
  const versionSelect = select([]);
  const usedSlots = (isDependency ? agent.dependencies : state.agents).filter((binding) => binding !== existing);
  let sequence = 0;
  let versionError;
  async function loadVersions(preselected) {
    const id = ++sequence;
    const key = assetSelect.value;
    setOptions(versionSelect, [], undefined, t("Loading versions…"));
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
        setOptions(versionSelect, [], undefined, t("Versions unavailable"));
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
  formDialog(existing ? t("Edit binding") : (isDependency ? t("Add dependency") : t("Add agent")),
    [field(t("Binding name"), slot), node("div", { class: "form-columns" }, field(t("Asset"), assetSelect), field(t("Version"), versionSelect))],
    existing ? t("Apply binding") : t("Add binding"), async () => {
      if (versionError) throw versionError;
      if (versionSelect.disabled || !versionSelect.value) throw new Error(t("Select an available asset version."));
      if (usedSlots.some((ref) => ref.slot === slot.value)) throw new Error(t("This binding name is already in use."));
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
    const draft = state.schemaDraft;
    const request = draft
      ? { raw: jsonPayload({ expectedRevision: experiment.revision, schema: draft.schema }, "config", draft.source) }
      : { payload };
    await api(route(tag) + "/draft", { method: "PUT", ...request });
    await refreshLists();
    await loadExperiment(tag);
    announce(t("Draft saved."));
  } finally {
    state.saving = false;
    render();
  }
}

function viewComposition(title, data, controls = []) {
  const rendered = JSON.stringify(data, null, 2);
  const outline = compositionOutline(data);
  const code = node("pre", { class: "json-output", tabindex: "0", hidden: true }, rendered);
  const reveal = (showJSON) => {
    outline.hidden = showJSON;
    code.hidden = !showJSON;
    structure.setAttribute("aria-pressed", String(!showJSON));
    json.setAttribute("aria-pressed", String(showJSON));
    copy.hidden = !showJSON;
  };
  const structure = button(t("Structure"), () => reveal(false), "small", { "aria-pressed": "true" });
  const json = button("JSON", () => reveal(true), "small", { "aria-pressed": "false" });
  const copy = button(t("Copy JSON"), async () => {
    await navigator.clipboard.writeText(rendered);
    announce(t("JSON copied."));
  });
  copy.hidden = true;
  modal(title, node("section", { class: "fields" },
    node("div", { hidden: true, class: "error", role: "alert", "data-error": true }),
    node("div", { class: "filters preview-modes" }, structure, json),
    outline, code, node("div", { class: "form-actions" }, copy, controls)));
}

async function previewDraft() {
  const experiment = state.selected;
  const data = await api(route(experiment.tag) + "/preview");
  if (data.draftRevision !== experiment.revision) {
    throw new Error(t("The draft changed. Reload it before publishing."));
  }
  const alreadyReleased = experiment.releases.some((r) => r.draftRevision === data.draftRevision);
  const empty = !data.schema && !data.agents.length;
  let publishButton;
  publishButton = button(t("Publish release"), async () => {
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
      announce(t("Published {releaseId}.", { releaseId: snapshot.releaseId }));
    } finally {
      publishButton.disabled = false;
    }
  }, "primary", { disabled: empty || alreadyReleased });
  const hint = alreadyReleased
    ? node("p", { class: "subtle" }, t("This revision is already in release history."))
    : (empty ? node("p", { class: "subtle" }, t("Add at least one Agent before publishing.")) : null);
  viewComposition(t("Preview composition · draft r{revision}", { revision: data.draftRevision }), data, [hint, publishButton]);
}

async function viewRelease(tag, releaseId) {
  const path = configPath(tag, releaseId);
  const data = await api(path);
  viewComposition(data.releaseId, data, [node("a", { class: "button", href: "/api/v1" + path,
    target: "_blank", rel: "noopener" }, t("Open API response"))]);
}

function activateRelease(experiment, releaseId) {
  formDialog(t("Activate {releaseId}", { releaseId }),
    [node("p", {}, t("Requests for {tag} will serve this snapshot.", { tag: experiment.tag }))],
    t("Activate release"), async () => {
      await api(route(experiment.tag) + "/active-release", { method: "PUT", payload: {
        releaseId,
        expectedActiveReleaseId: experiment.activeReleaseId,
      } });
      await refreshLists();
      if (state.dirty) state.selected = { ...state.selected, activeReleaseId: releaseId };
      else await loadExperiment(experiment.tag);
      dialog.close();
      render();
      announce(t("Active release is now {releaseId}.", { releaseId }));
    });
}

function catalogView(collection) {
  const isSchema = collection === "schemas";
  const isComposition = isSchema && state.schemaScope === "experiment";
  const title = isSchema ? t("Schemas") : t("Assets");
  const filter = state.filters[collection];
  const items = state[collection].filter((item) => {
    if (isComposition) return item.kind === "experiment";
    return item.kind !== "experiment" && (filter === "all" || item.kind === filter);
  });
  const create = () => {
    if (!isSchema) return assetDialog();
    return schemaDialog(undefined, undefined, { kind: isComposition ? "experiment" : undefined });
  };
  const createLabel = !isSchema ? t("Import asset") : t(isComposition ? "New composition schema" : "New asset schema");
  const filterButtons = ["all", "agent", "skill", "prompt"].map((kind) => button(
    kind === "all" ? t("All") : labels[kind], () => { state.filters[collection] = kind; render(); },
    "small " + (filter === kind ? "selected" : ""), { "aria-pressed": String(filter === kind) }));
  const categories = isSchema ? node("nav", { class: "filters schema-categories", "aria-label": t("Schema categories") },
    [["asset", "Asset schemas"], ["experiment", "Composition schemas"]].map(([scope, label]) => {
      const selected = state.schemaScope === scope;
      return button(t(label), () => { state.schemaScope = scope; render(); }, "small " + (selected ? "selected" : ""),
        { "aria-pressed": String(selected) });
    })) : null;

  return node("section", { class: "catalog" },
    node("div", { class: "section-title" }, node("h1", {}, title), button(createLabel, create, "primary")),
    categories,
    node("section", { class: "panel" },
      isComposition ? null : node("div", { class: "panel-header filters", "aria-label": t("Filter by asset type") }, filterButtons),
      items.length ? node("div", { class: "catalog-list" }, items.map((item) =>
        node("div", { class: "catalog-row" },
          node("div", {}, node("h3", {}, displayName(item)),
            node("p", { class: "ref-line mono" }, item.key + (isSchema ? "" : " / " + item.schema.key + "@" + item.schema.version))),
          tagLabel(t(labels[item.kind]), item.kind),
          button(t("Versions · v{version}", { version: item.latestVersion }), () => versionsDialog(collection, item), "small",
            { "aria-label": t("Versions for {name}", { name: displayName(item) }) }))))
        : node("div", { class: "panel-body" }, empty(
          t(isComposition ? "No composition schemas yet" : (isSchema ? "No schemas in this view" : "No assets in this view")),
          t(isComposition ? "Register a schema to define the fields in a configuration group."
            : (isSchema ? "Register a JSON Schema to validate asset versions." : "Import JSON using a registered Schema version.")),
          null))));
}

function jsonEditor(label, initial, rows = 10) {
  const area = node("textarea", { rows, spellcheck: "false", required: true, wrap: "off" });
  area.value = typeof initial === "string" ? initial : JSON.stringify(initial, null, 2);
  const file = input({ type: "file", accept: ".json,application/json", hidden: true, "aria-label": t("Load JSON file") });
  const container = node("div", { class: "fields" },
    field(label, area), node("div", { class: "editor-tools" },
      node("button", { type: "button", class: "button small", on: { click: () => file.click() } }, t("Load JSON file")), file));
  file.addEventListener("change", async () => {
    const chosen = file.files[0];
    if (!chosen) return;
    try {
      if (chosen.size > 1 << 20) throw new Error(t("Choose a JSON file smaller than 1 MiB."));
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
    throw new Error(t("The JSON must contain an object."));
  }
  return value;
}

function jsonPayload(fields, property, source) {
  objectFromJSON(source);
  return JSON.stringify(fields).slice(0, -1) + "," + JSON.stringify(property) + ":" + source.trim() + "}";
}

function schemaDialog(item, source, options = {}) {
  const key = keyInput(item?.key ?? "");
  const name = input({ required: true, maxlength: 80 });
  const isComposition = item?.kind === "experiment" || options.kind === "experiment";
  const kind = select(["agent", "skill", "prompt"].map((id) => [id, labels[id]]));
  const root = { "$schema": "https://json-schema.org/draft/2020-12/schema", type: "object",
    ...(isComposition
      ? { properties: { primary: { "x-asset": "agent" } }, required: ["primary"] }
      : { properties: { message: { type: "string" } }, required: ["message"] }) };
  const editor = jsonEditor("JSON Schema", source?.definition ?? root);
  const fields = item ? [editor.container] : [field(t("Name"), name),
    node("div", { class: "form-columns" }, field(t("Schema key"), key),
      isComposition ? null : field(t("Asset type"), kind)),
    editor.container];
  fields.push(node("p", { class: "subtle" }, t("Use x-asset in a field or array item to choose an existing asset version.")));
  formDialog(item ? t("New schema version") : t(isComposition ? "New composition schema" : "New asset schema"), fields, item ? t("Create version") : t("Create schema"), async () => {
    const path = item ? versionRoute("schemas", item.key) : "/schemas";
    const meta = item ? { baseVersion: item.latestVersion } : { key: key.value, name: name.value.trim(), kind: isComposition ? "experiment" : kind.value };
    await api(path, { method: "POST", raw: jsonPayload(meta, "definition", editor.area.value) });
    await refreshLists();
    dialog.close();
    render();
    announce(item ? t("Schema version created.") : t("Schema created."));
    options.onSaved?.();
  });
}

async function assetDialog(item, source) {
  const eligible = state.schemas.filter((entry) => entry.kind !== "experiment" && (!item || item.kind === entry.kind));
  if (!eligible.length) {
    modal(t("Register a schema first"), node("div", { class: "form" },
      node("p", {}, t("A Schema version validates the JSON you import.")),
      node("div", { class: "form-actions" }, button(t("New asset schema"), () => {
        state.view = "schemas";
        state.schemaScope = "asset";
        render();
        schemaDialog();
      }, "primary"))));
    return;
  }

  const key = keyInput(item?.key ?? "");
  const name = input({ required: true, maxlength: 80 });
  const schemaSelect = select(eligible.map((entry) => [entry.key, labels[entry.kind] + " · " + displayName(entry)]), source?.schema.key);
  const schemaVersion = select([]);
  const editor = jsonEditor(t("Config JSON"), source?.config ?? {});
  const fieldsArea = node("div", { class: "fields", hidden: true });
  let schemaVersions = [];
  let sequence = 0;
  let formMode = false;
  let showForm;
  const definition = () => schemaVersions.find((entry) => entry.version === Number(schemaVersion.value))?.definition;

  function renderEditor() {
    const schema = definition();
    const supported = schema && Object.entries(schemaShape(schema, schema).properties ?? {}).some(([, child]) => formSupported(schema, child));
    showForm.disabled = !supported;
    if (!supported) formMode = false;
    if (formMode) {
      try {
        fieldsArea.replaceChildren(...renderStructuredFields(schema, objectFromJSON(editor.area.value), (values) => {
          editor.area.value = JSON.stringify(values, null, 2);
        }));
      } catch (error) {
        formMode = false;
        reportError(error);
      }
    }
    showForm.textContent = formMode ? t("Edit raw JSON") : t("Use schema form");
    fieldsArea.hidden = !formMode;
    editor.container.hidden = formMode;
    editor.area.required = !formMode;
    if (!formMode) fieldsArea.replaceChildren();
  }

  showForm = button(t("Use schema form"), () => { formMode = !formMode; renderEditor(); }, "small", { disabled: true });

  async function loadSchemas(selectedVersion) {
    const id = ++sequence;
    schemaVersions = [];
    schemaVersion.disabled = true;
    setOptions(schemaVersion, [], undefined, t("Loading versions…"));
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
        setOptions(schemaVersion, [], undefined, t("Versions unavailable"));
        reportError(error);
      }
    }
  }

  schemaSelect.addEventListener("change", () => { void loadSchemas(); });
  schemaVersion.addEventListener("change", renderEditor);
  const fields = [
    item ? null : node("div", { class: "form-columns" }, field(t("Name"), name), field(t("Asset key"), key)),
    node("div", { class: "form-columns" }, field(t("Schema"), schemaSelect), field(t("Schema version"), schemaVersion)),
    node("div", { class: "editor-tools" }, showForm), editor.container, fieldsArea,
  ];
  formDialog(item ? t("New asset version") : t("Import asset"), fields, item ? t("Create version") : t("Create asset"), async () => {
    if (!definition()) throw new Error(t("Select an available Schema version."));
    const schema = { key: schemaSelect.value, version: Number(schemaVersion.value) };
    const meta = item ? { schema, baseVersion: item.latestVersion } : { schema, key: key.value, name: name.value.trim() };
    const path = item ? versionRoute("assets", item.key) : "/assets";
    await api(path, { method: "POST", raw: jsonPayload(meta, "config", editor.area.value) });
    await refreshLists();
    dialog.close();
    render();
    announce(item ? t("Asset version created.") : t("Asset imported."));
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
  modal(displayName(item), node("div", { class: "form" }, field(t("Version"), versionSelect), data,
    node("div", { class: "form-actions" }, button(t("Create next version"),
      () => isSchema ? schemaDialog(latest, selected()) : assetDialog(latest, selected()), "primary"))));
}

async function start() {
  state.loading = true;
  state.loadError = null;
  render();
  try {
    await refreshLists();
    if (state.experiments.length) await loadExperiment(state.experiments[0].tag);
  } catch (error) {
    state.loadError = error;
  } finally {
    state.loading = false;
    render();
  }
}

void start();
