/* LLM Proxy console. No build step, no external dependencies. */
(function () {
  "use strict";

  // Stateless formatting helpers live in format.js, which index.html loads
  // first, so this file only holds view state and rendering.
  const {
    plural,
    formatNumber,
    countLabel,
    matchesQuery,
    titleCase,
    formatSize,
    compact,
    formatDuration,
    formatDate,
    formatClock,
    timeAgo,
    formatUptime,
  } = globalThis.LPFormat;

  const API = "/api/ui";
  const REQUEST_WINDOW = 500;
  const ENDPOINT_FORMATS = ["chat_completions", "responses", "messages"];
  const FALLBACK_CONVERTERS = [
    { id: "openai_chat_to_claude_messages", from: "chat_completions", to: "messages", label: "OpenAI Chat → Claude Messages" },
    { id: "claude_messages_to_openai_chat", from: "messages", to: "chat_completions", label: "Claude Messages → OpenAI Chat" },
    { id: "openai_chat_to_openai_responses", from: "chat_completions", to: "responses", label: "OpenAI Chat → OpenAI Responses" },
    { id: "openai_responses_to_openai_chat", from: "responses", to: "chat_completions", label: "OpenAI Responses → OpenAI Chat" },
    { id: "claude_messages_to_openai_responses", from: "messages", to: "responses", label: "Claude Messages → OpenAI Responses" },
    { id: "openai_responses_to_claude_messages", from: "responses", to: "messages", label: "OpenAI Responses → Claude Messages" },
  ];
  let CONVERTERS = FALLBACK_CONVERTERS.slice();

  const ICONS = {
    overview:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7.5" height="7.5" rx="1.6"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.6"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.6"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.6"/></svg>',
    catalog:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 8h16l-1 12H5L4 8Z"/><path d="M8.5 8V6.5a3.5 3.5 0 0 1 7 0V8"/><path d="M9.5 13h5"/></svg>',
    models:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 3 7.5l9 4.5 9-4.5L12 3Z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/></svg>',
    providers:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/></svg>',
    requests:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h4l2.4-7 4.2 14L16 12h5"/></svg>',
    users:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="8" r="3"/><path d="M3 20c.6-3 2.8-4.8 6-4.8S14.4 17 15 20"/><circle cx="17" cy="9" r="2.3"/><path d="M16.3 15.4c2.4.2 4.1 1.7 4.7 4.6"/></svg>',
    keys:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="15" r="4"/><path d="m11 12 8-8"/><path d="m16 5 3 3"/><path d="m13 8 3 3"/></svg>',
    account:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="3.4"/><path d="M5 20c.8-3.4 3.5-5.1 7-5.1s6.2 1.7 7 5.1"/></svg>',
    logout:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M9 4H5a1 1 0 0 0-1 1v14a1 1 0 0 0 1 1h4"/><path d="m16 8 4 4-4 4"/><path d="M20 12H9"/></svg>',
    refresh:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12a8 8 0 1 1-2.4-5.7"/><path d="M20 4v5h-5"/></svg>',
    search:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="6"/><path d="m20 20-4.3-4.3"/></svg>',
    stream:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h2.2l2-6 3.2 12L14 9l2 3h4"/></svg>',
    lock:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M8.5 10V7.5a3.5 3.5 0 0 1 7 0V10"/><path d="M12 14v2"/></svg>',
    copy:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3"/></svg>',
    check:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 4 4L19 6"/></svg>',
    menu:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/></svg>',
    settings:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.56V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1.11-1.56 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.56-1H3a2 2 0 1 1 0-4h.09A1.7 1.7 0 0 0 4.6 8.9a1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 8.96 4.6H9a1.7 1.7 0 0 0 1-1.56V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.56 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87V9a1.7 1.7 0 0 0 1.56 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.56 1Z"/></svg>',
  };

  const VIEW_META = {
    overview: {
      title: "Overview",
      subtitle: "Usage and activity at a glance",
      path: "overview",
    },
    catalog: {
      title: "Models",
      subtitle: "Models your account can call through the gateway",
      path: "models",
    },
    requests: {
      title: "Requests",
      subtitle: "Request history across the gateway",
      path: "requests",
    },
    keys: {
      title: "API Keys",
      subtitle: "Credentials for your own gateway access",
      path: "keys",
    },
    account: {
      title: "Account",
      subtitle: "Security and session details",
      path: "account",
    },
    "admin-models": {
      title: "Administration",
      subtitle: "Manage models, providers, console accounts, and registration",
      tab: "Models",
      path: "admin/models",
    },
    "admin-providers": {
      title: "Administration",
      subtitle: "Manage models, providers, console accounts, and registration",
      tab: "Providers",
      path: "admin/providers",
    },
    "admin-users": {
      title: "Administration",
      subtitle: "Manage models, providers, console accounts, and registration",
      tab: "Users",
      path: "admin/users",
    },
    "admin-settings": {
      title: "Administration",
      subtitle: "Manage models, providers, console accounts, and registration",
      tab: "Settings",
      path: "admin/settings",
    },
  };

  const VIEWS = Object.keys(VIEW_META);
  const VIEW_PATHS = {};
  VIEWS.forEach((view) => {
    VIEW_PATHS[VIEW_META[view].path] = view;
  });
  // Keep the old member catalog link working after the page moved to /models.
  VIEW_PATHS.catalog = "catalog";

  // Administrator-only sections. Everyone can open Overview, Models, Requests,
  // API keys and Account; administration pages come from the header.
  const ADMIN_VIEWS = ["admin-models", "admin-providers", "admin-users", "admin-settings"];

  // A single navigation group keeps the sidebar compact.
  const VIEW_GROUPS = [
    { label: "", views: ["overview", "catalog", "requests", "keys", "account"] },
  ];

  const VIEW_LOADERS = {
    overview: () => api("/overview").then(overviewView),
    catalog: () => api("/catalog").then(catalogView),
    "admin-models": () => api("/models").then(modelsView),
    "admin-providers": () => api("/providers").then(providersView),
    "admin-users": () => api("/users").then(usersView),
    "admin-settings": () =>
      Promise.all([api("/settings"), api("/models")]).then(([settings, models]) => settingsView(settings, models)),
    requests: () => api("/requests?limit=" + REQUEST_WINDOW).then(requestsView),
    keys: () => api("/keys").then(keysView),
    account: () => Promise.resolve(accountView()),
  };

  const state = {
    session: null,
    view: "overview",
    refs: null,
    catalogFilter: "",
    modelFilter: "",
    providerFilter: "",
    requestFilter: "",
    requestStatus: "all",
    userFilter: "",
    keyFilter: "",
  };

  // ---------------------------------------------------------------- helpers

  function el(tag, props, children) {
    const node = document.createElement(tag);
    if (props) {
      Object.entries(props).forEach(([key, value]) => {
        if (value === undefined || value === null) return;
        if (key === "class") node.className = value;
        else if (key === "text") node.textContent = String(value);
        else if (key === "html") node.innerHTML = value;
        else if (key === "on") {
          Object.entries(value).forEach(([event, handler]) => node.addEventListener(event, handler));
        } else if (key === "dataset") {
          Object.assign(node.dataset, value);
        } else if (key === "value" || key === "checked" || key === "disabled" || key === "selected") {
          node[key] = value;
        } else {
          node.setAttribute(key, String(value));
        }
      });
    }
    append(node, children);
    return node;
  }

  function append(node, children) {
    if (children === undefined || children === null) return;
    const list = Array.isArray(children) ? children : [children];
    list.forEach((child) => {
      if (child === undefined || child === null || child === false) return;
      node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    });
  }

  function icon(name, extraClass) {
    return el("span", {
      class: "icon" + (extraClass ? " " + extraClass : ""),
      html: ICONS[name] || "",
      "aria-hidden": "true",
    });
  }

  function brandMark() {
    return (
      '<svg viewBox="0 0 32 32" aria-hidden="true">' +
      '<rect x="1" y="1" width="30" height="30" rx="9" fill="url(#g)"/>' +
      '<path d="M9 21V11h4v7h6v3H9Z" fill="#fff"/>' +
      '<circle cx="23" cy="12" r="2.2" fill="#d9d2ff"/>' +
      '<defs><linearGradient id="g" x1="1" y1="1" x2="31" y2="31" gradientUnits="userSpaceOnUse">' +
      '<stop stop-color="#7c5cff"/><stop offset="1" stop-color="#22d3ee"/></linearGradient></defs>' +
      "</svg>"
    );
  }

  async function api(path, options) {
    const settings = options || {};
    const init = {
      method: settings.method || "GET",
      credentials: "same-origin",
      headers: {},
    };
    if (settings.body !== undefined) {
      init.body = typeof settings.body === "string" ? settings.body : JSON.stringify(settings.body);
      init.headers["content-type"] = "application/json";
    }

    const response = await fetch(API + path, init);
    const text = await response.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (_) {
        data = null;
      }
    }
    if (!response.ok) {
      const message =
        data && data.error && data.error.message
          ? data.error.message
          : "The server returned an error (HTTP " + response.status + ").";
      const error = new Error(message);
      error.status = response.status;
      error.code = data && data.error ? data.error.code : "";
      throw error;
    }
    return data || {};
  }

  function isAdmin() {
    return Boolean(state.session && state.session.user && state.session.user.is_admin);
  }

  function roleLabel(user) {
    return user && user.is_admin ? "Administrator" : "Member";
  }

  function defaultView() {
    return isAdmin() ? "overview" : "catalog";
  }

  function viewPath(view) {
    return (VIEW_META[view] && VIEW_META[view].path) || view;
  }

  function currentView() {
    const hash = window.location.hash.replace(/^#\/?/, "").split("?")[0].replace(/\/+$/, "");
    const view = VIEW_PATHS[hash] || defaultView();
    if (!isAdmin() && ADMIN_VIEWS.indexOf(view) >= 0) return defaultView();
    return view;
  }

  function searchPlaceholder(view) {
    switch (view) {
      case "catalog":
      case "admin-models":
        return "Search models";
      case "admin-providers":
        return "Search providers";
      case "requests":
        return "Search requests";
      case "admin-users":
        return "Search users";
      case "keys":
        return "Search API keys";
      default:
        return "Search models, providers, requests";
    }
  }

  function visibleGroups() {
    return VIEW_GROUPS.map((group) => ({
      label: group.label,
      views: group.views.filter((view) => isAdmin() || ADMIN_VIEWS.indexOf(view) < 0),
    })).filter((group) => group.views.length);
  }

  function initials(id) {
    const text = String(id || "?").trim();
    if (!text) return "?";
    const parts = text.split(/[\s._/-]+/).filter(Boolean);
    if (parts.length > 1) return (parts[0][0] + parts[1][0]).toUpperCase();
    return text.slice(0, 2).toUpperCase();
  }

  function endpointLabel(value) {
    switch (value) {
      case "chat_completions":
        return "OpenAI Chat";
      case "responses":
        return "OpenAI Responses";
      case "messages":
        return "Claude Messages";
      case "none":
        return "None";
      case "":
      case undefined:
      case null:
        return "—";
      default:
        return titleCase(value);
    }
  }

  function converterLabel(value) {
    if (!value || value === "none") return "None (Passthrough)";
    const found = CONVERTERS.find((item) => item.id === value);
    return found ? found.label : titleCase(value);
  }

  function converterOptions(format) {
    const options = [{ value: "none", label: "None (Passthrough)" }];
    CONVERTERS.filter((item) => item.from === format).forEach((item) => {
      options.push({ value: item.id, label: item.label || converterLabel(item.id) });
    });
    return options;
  }

  function defaultEndpointPath(value) {
    switch (value) {
      case "chat_completions":
        return "/chat/completions";
      case "responses":
        return "/responses";
      case "messages":
        return "/v1/messages";
      default:
        return "";
    }
  }

  function entityLabel(entity) {
    return (entity && entity.name) || (entity && entity.id) || "unknown";
  }

  function routingLabel(value) {
    switch (value) {
      case "round_robin":
        return "Round Robin";
      case "random":
        return "Random";
      default:
        return "Priority";
    }
  }

  

  function modalityLabel(value) {
    switch (value) {
      case "text":
        return "Text";
      case "image":
        return "Image";
      case "video":
        return "Video";
      case "audio":
        return "Audio";
      default:
        return titleCase(value);
    }
  }

  // modalityOptions keeps the editor's token list in sync with the model's
  // current values, including any custom modality an upstream may report.
  function modalityOptions(value) {
    const base = ["text", "image", "audio", "video"];
    const extra = splitList(String(value || ""))
      .map((item) => item.toLowerCase())
      .filter((item) => item && base.indexOf(item) < 0);
    return base.concat(extra).map((item) => ({ value: item, label: modalityLabel(item) }));
  }

  // modalityFlow renders the "input → output" modality pair used by model rows
  // and cards, mirroring the OpenRouter listing style.
  function modalityFlow(modalities, outputModalities) {
    const inputs = modalities && modalities.length ? modalities : ["text"];
    const outputs = outputModalities && outputModalities.length ? outputModalities : ["text"];
    return el("span", { class: "modality-flow" }, [
      el(
        "span",
        { class: "row-chips" },
        inputs.map((item) => chip(modalityLabel(item)))
      ),
      el("span", { class: "modality-arrow", "aria-hidden": "true", text: "→" }),
      el(
        "span",
        { class: "row-chips" },
        outputs.map((item) => chip(modalityLabel(item), "subtle"))
      ),
    ]);
  }

  async function copyText(value) {
    if (navigator.clipboard && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(value);
        return true;
      } catch (_) {
        // Fall through to the selection-based fallback.
      }
    }
    const area = document.createElement("textarea");
    area.value = value;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.top = "-1000px";
    area.style.opacity = "0";
    document.body.append(area);
    area.select();
    let copied = false;
    try {
      copied = document.execCommand("copy");
    } catch (_) {
      copied = false;
    }
    area.remove();
    return copied;
  }

  async function copyValue(value, button, label) {
    const copied = await copyText(value);
    if (!copied) {
      showToast("Copy failed. Select the value and copy it manually.");
      return;
    }
    button.classList.add("copied");
    button.title = "Copied";
    button.setAttribute("aria-label", "Copied");
    button.replaceChildren(icon("check"));
    setTimeout(() => {
      button.classList.remove("copied");
      button.title = label;
      button.setAttribute("aria-label", label);
      button.replaceChildren(icon("copy"));
    }, 1400);
  }

  function copyModelID(value, button) {
    return copyValue(value, button, "Copy model ID");
  }

  function cardHeader(title, subtitle) {
    return el("div", { class: "card-head" }, [
      el("div", {}, [
        el("h2", { text: title }),
        subtitle ? el("p", { class: "card-sub", text: subtitle }) : null,
      ]),
    ]);
  }

  function emptyState(title, detail) {
    return el("div", { class: "empty" }, [
      el("strong", { text: title }),
      detail ? el("p", { text: detail }) : null,
    ]);
  }

  function loadingState() {
    return el("div", { class: "loading", role: "status" }, [
      el("span", { class: "spinner" }),
      el("span", { text: "Loading…" }),
    ]);
  }

  function errorState(message, retry) {
    return el("div", { class: "empty error" }, [
      el("strong", { text: "Couldn't Load This Page" }),
      el("p", { text: message }),
      el("button", { class: "btn ghost", type: "button", text: "Try Again", on: { click: retry } }),
    ]);
  }

  // ------------------------------------------------------------ admin forms

  function openModal(title, subtitle, fields, onSubmit, submitLabel, options) {
    const settings = options || {};
    const error = el("p", { class: "form-error modal-error", role: "alert", text: "" });
    const form = el("form", { class: "modal-form" });
    const body = el("div", { class: "modal-body" });
    const controls = {};

    let sectionKey = null;
    let sectionGrid = null;

    fields.forEach((field) => {
      const startsSection = field.section !== undefined;
      if (!sectionGrid || (startsSection && field.section !== sectionKey)) {
        const section = el("section", { class: "form-section" });
        if (field.section) {
          section.append(
            el("div", { class: "form-section-head" }, [
              el("h3", { text: field.section }),
              field.sectionHint ? el("p", { text: field.sectionHint }) : null,
            ])
          );
        }
        sectionGrid = el("div", { class: "form-grid" + (field.grid ? " " + field.grid : "") });
        section.append(sectionGrid);
        body.append(section);
        sectionKey = field.section || "";
      }

      const control = fieldControl(field);
      controls[field.name] = control;
      const focusTarget = control.__input || control;
      focusTarget.id = "f-" + field.name;

      if (field.type === "checkbox") {
        sectionGrid.append(
          el("label", { class: "form-field toggle-field" + (field.wide ? " wide" : ""), for: focusTarget.id }, [
            control,
            el("span", { class: "toggle-copy" }, [
              el("span", { class: "toggle-title", text: field.label }),
              field.help ? el("span", { class: "field-help", text: field.help }) : null,
            ]),
          ])
        );
        return;
      }

      sectionGrid.append(
        el("div", { class: "form-field" + (field.wide ? " wide" : "") }, [
          el("label", {
            class: "field-label" + (field.hideLabel ? " sr-only" : ""),
            text: field.label,
            for: focusTarget.id,
          }),
          control,
          field.help ? el("span", { class: "field-help", text: field.help }) : null,
        ])
      );
    });

    const submit = el("button", {
      class: "btn primary" + (settings.danger ? " danger" : ""),
      type: "submit",
      text: submitLabel || "Save",
    });
    const cancel = el("button", { class: "btn ghost", type: "button", text: "Cancel", on: { click: close } });
    form.append(
      body,
      el("div", { class: "modal-foot" }, [
        error,
        el("div", { class: "modal-actions" }, [cancel, submit]),
      ])
    );

    const overlay = el("div", { class: "modal-backdrop", role: "presentation" }, [
      el(
        "section",
        {
          class: "modal" + (settings.wide ? " wide" : ""),
          role: "dialog",
          "aria-modal": "true",
          "aria-label": title,
        },
        [
          el("div", { class: "modal-head" }, [
            el("div", { class: "modal-title" }, [
              el("h2", { text: title }),
              subtitle ? el("p", { class: "modal-sub", text: subtitle }) : null,
            ]),
            el("button", {
              class: "icon-btn modal-close",
              type: "button",
              "aria-label": "Close",
              text: "×",
              on: { click: close },
            }),
          ]),
          form,
        ]
      ),
    ]);

    function close() {
      overlay.remove();
      document.removeEventListener("keydown", onKey);
    }
    function onKey(event) {
      if (event.key === "Escape") close();
    }

    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      error.textContent = "";
      submit.disabled = true;
      submit.textContent = "Saving…";
      try {
        await onSubmit(collect(fields, controls));
        close();
      } catch (failure) {
        error.textContent = failure.message;
      } finally {
        submit.disabled = false;
        submit.textContent = submitLabel || "Save";
      }
    });

    document.body.append(overlay);
    document.addEventListener("keydown", onKey);
    const first = form.querySelector("input:not([disabled]), textarea, select");
    if (first) first.focus();
  }

  // tagInput turns free-form text into removable labels. Enter or a comma
  // commits the current value; Backspace on an empty field removes the last
  // tag. It reads back as an array so provider catalogs stay structured.
  function tagInput(field) {
    const values = Array.isArray(field.value)
      ? field.value.map((item) => String(item).trim()).filter(Boolean)
      : splitList(String(field.value || ""));
    const node = el("div", { class: "tag-input", role: "group" });
    const chips = el("div", { class: "tag-chips" });
    const input = el("input", {
      class: "tag-field",
      type: "text",
      placeholder: field.placeholder || "Type a value and press Enter",
      autocomplete: "off",
      spellcheck: "false",
    });
    node.append(chips, input);
    node.__input = input;

    function render() {
      chips.replaceChildren(
        ...values.map((value) => {
          const remove = el("button", {
            class: "tag-remove",
            type: "button",
            "aria-label": "Remove " + value,
            text: "×",
          });
          remove.addEventListener("click", (event) => {
            event.stopPropagation();
            removeValue(value);
          });
          return el("span", { class: "tag" }, [el("span", { text: value }), remove]);
        })
      );
    }

    function add(value) {
      const trimmed = value.trim();
      if (!trimmed || values.includes(trimmed)) return;
      values.push(trimmed);
      render();
    }

    function removeValue(value) {
      const index = values.indexOf(value);
      if (index >= 0) {
        values.splice(index, 1);
        render();
      }
    }

    function commit() {
      if (!input.value.trim()) return;
      input.value.split(",").forEach(add);
      input.value = "";
    }

    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === ",") {
        event.preventDefault();
        commit();
      } else if (event.key === "Backspace" && input.value === "" && values.length) {
        values.pop();
        render();
      } else if (event.key === "Escape") {
        input.value = "";
      }
    });
    input.addEventListener("blur", commit);
    node.addEventListener("click", (event) => {
      if (event.target === node || event.target === chips) input.focus();
    });
    node.__read = () => {
      commit();
      return values.slice();
    };
    render();
    return node;
  }

  // modelAccessEditor turns the user allow-list into checkboxes. Checking
  // "Select all" persists the "*" wildcard, which also allows models added later.
  // Without it, only the individually checked models are allowed; saving no
  // checks stores an empty list and grants no model access.
  function modelAccessEditor(field) {
    const raw = Array.isArray(field.value) ? field.value.slice() : [];
    const wildcard = raw.indexOf("*") >= 0;
    const choices = (field.models || []).map((model) => ({
      id: String(model.id),
      name: String(model.name || ""),
    }));
    raw.forEach((id) => {
      if (id === "*" || choices.some((choice) => choice.id === id)) return;
      choices.push({ id, name: "" });
    });

    const grid = el("div", { class: "model-access-grid" });
    const note = el("span", { class: "model-access-note", "aria-live": "polite" });
    const selectAll = el("input", { type: "checkbox", class: "checkbox" });
    selectAll.checked = wildcard;

    if (!choices.length) {
      grid.append(
        el("span", {
          class: "model-access-empty",
          text: "No models are configured yet. Select all to allow every model, including future additions.",
        })
      );
    }

    const boxes = choices.map((choice) => {
      const box = el("input", { type: "checkbox", class: "checkbox" });
      box.checked = wildcard || raw.indexOf(choice.id) >= 0;
      const name = choice.name || choice.id;
      const option = el("label", { class: "model-option" }, [
        box,
        el("span", { class: "model-option-copy" }, [
          el("span", { class: "model-option-name", text: name }),
          choice.name && choice.name !== choice.id
            ? el("span", { class: "model-option-id mono", text: choice.id })
            : null,
        ]),
      ]);
      box.addEventListener("change", sync);
      grid.append(option);
      return box;
    });

    function sync() {
      const checked = boxes.filter((box) => box.checked).length;
      boxes.forEach((box) => {
        box.disabled = selectAll.checked;
      });
      if (selectAll.checked) {
        selectAll.indeterminate = false;
        note.textContent = "All Models, Including Future Additions";
      } else {
        selectAll.indeterminate = checked > 0 && checked < boxes.length;
        note.textContent =
          formatNumber(checked) +
          " of " +
          formatNumber(boxes.length) +
          " selected · future models excluded";
      }
    }

    selectAll.addEventListener("change", () => {
      if (selectAll.checked) {
        boxes.forEach((box) => {
          box.checked = true;
        });
      }
      sync();
    });
    sync();

    const node = el("div", { class: "model-access" }, [
      el("div", { class: "model-access-bar" }, [
        el("label", { class: "inline-check" }, [
          selectAll,
          el("span", { text: "Select All, Including Future Models" }),
        ]),
        note,
      ]),
      grid,
    ]);
    node.__read = () => {
      if (selectAll.checked) return ["*"];
      return choices.filter((_, index) => boxes[index].checked).map((choice) => choice.id);
    };
    return node;
  }

  function fieldControl(field) {
    if (field.type === "models") {
      return modelAccessEditor(field);
    }
    if (field.type === "tags") {
      return tagInput(field);
    }
    if (field.type === "endpoints") {
      const editor = endpointEditor(field.value || {});
      editor.node.__read = editor.read;
      return editor.node;
    }
    if (field.type === "bindings") {
      const editor = bindingEditor(field.value || [], field.providers || []);
      editor.node.__read = editor.read;
      return editor.node;
    }
    if (field.type === "tokens") {
      const values = field.options || [];
      const chosen = new Set(
        splitList(String(field.value || ""))
          .map((item) => item.toLowerCase())
          .filter(Boolean)
      );
      const node = el("div", { class: "token-select", role: "group", "aria-label": field.label });
      values.forEach((option) => {
        const value = String(option.value).toLowerCase();
        const button = el("button", {
          class: "token" + (chosen.has(value) ? " active" : ""),
          type: "button",
          "aria-pressed": chosen.has(value) ? "true" : "false",
          text: option.label,
          on: {
            click: () => {
              if (chosen.has(value)) chosen.delete(value);
              else chosen.add(value);
              button.classList.toggle("active", chosen.has(value));
              button.setAttribute("aria-pressed", chosen.has(value) ? "true" : "false");
            },
          },
        });
        node.append(button);
      });
      node.__read = () => Array.from(chosen).join(", ");
      return node;
    }
    if (field.type === "textarea") {
      return el("textarea", {
        class: "input textarea",
        rows: field.rows || 4,
        placeholder: field.placeholder || "",
        spellcheck: "false",
      }, field.value || "");
    }
    if (field.type === "select") {
      return el("select", { class: "input" }, (field.options || []).map((option) =>
        el("option", { value: option.value, text: option.label, selected: option.value === field.value })
      ));
    }
    if (field.type === "checkbox") {
      const input = el("input", { type: "checkbox", class: "checkbox" });
      input.checked = Boolean(field.value);
      return input;
    }
    const input = el("input", {
      class: "input",
      type: field.type || "text",
      placeholder: field.placeholder || "",
      step: field.step,
      min: field.min,
      autocomplete: field.autocomplete || "off",
      spellcheck: "false",
      disabled: Boolean(field.disabled),
    });
    input.value = field.value === undefined || field.value === null ? "" : String(field.value);
    return input;
  }

  function collect(fields, controls) {
    const body = {};
    fields.forEach((field) => {
      const control = controls[field.name];
      if (typeof control.__read === "function") {
        body[field.name] = control.__read();
      } else if (field.type === "checkbox") {
        body[field.name] = control.checked;
      } else if (field.type === "number") {
        body[field.name] = control.value === "" ? null : Number(control.value);
      } else {
        body[field.name] = control.value;
      }
    });
    return body;
  }

  function confirmDelete(kind, name, onConfirm) {
    const kindLabel = kind === "API key" ? "API Key" : titleCase(kind);
    openModal(
      "Delete " + kindLabel,
      "This cannot be undone.",
      [{ name: "confirm", label: "Type the " + kindLabel + " ID to confirm", placeholder: name }],
      async (values) => {
        if (values.confirm.trim() !== name) {
          throw new Error("The ID you typed doesn't match.");
        }
        await onConfirm();
      },
      "Delete",
      { danger: true }
    );
  }

  function splitList(value) {
    return String(value || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function addButton(label, onClick) {
    return el("button", { class: "btn primary small", type: "button", on: { click: onClick } }, [
      el("span", { text: "+ " + label }),
    ]);
  }

  function metaCell(label, value) {
    return el("div", { class: "meta" }, [
      el("span", { class: "meta-label", text: label }),
      el("strong", { class: "meta-value", text: value }),
    ]);
  }

  function chip(label, tone) {
    return el("span", { class: "chip" + (tone ? " " + tone : ""), text: label });
  }

  function applySession(session) {
    state.session = session;
    const gateway = (session && session.gateway) || {};
    if (Array.isArray(gateway.converters) && gateway.converters.length) {
      CONVERTERS = gateway.converters;
    }
  }

  // ------------------------------------------------------------------ boot

  function boot() {
    api("/session")
      .then((session) => {
        applySession(session);
        startApp();
      })
      .catch((error) => {
        if (error.status === 403) {
          showNotice("Console Unavailable", error.message, false);
        } else if (error.status === 401) {
          if (error.code === "registration_required") {
            showRegister("", true);
          } else {
            showLogin(error.code === "session_expired" ? error.message : "");
          }
        } else {
          showNotice(
            "Unable to Connect",
            "The console couldn't reach the gateway. Check your connection and try again.",
            true
          );
        }
      });
  }

  function authBrand() {
    return el("div", { class: "login-brand" }, [
      el("span", { class: "brand-mark large", html: brandMark() }),
      el("div", {}, [
        el("h1", { text: "LLM Proxy" }),
        el("p", { class: "muted", text: "Gateway console" }),
      ]),
    ]);
  }

  function showAuthCard(children) {
    state.refs = null;
    const app = document.getElementById("app");
    app.className = "auth-screen";
    app.replaceChildren(
      el("div", { class: "login-wrap" }, [el("div", { class: "login-card" }, [authBrand(), ...children])])
    );
  }

  function showLogin(message) {
    const username = el("input", {
      class: "input",
      type: "text",
      placeholder: "username",
      autocomplete: "username",
      spellcheck: "false",
      required: "required",
      "aria-label": "Username",
    });
    const password = el("input", {
      class: "input login-key",
      type: "password",
      placeholder: "Enter your password",
      autocomplete: "current-password",
      spellcheck: "false",
      required: "required",
      "aria-label": "Password",
    });
    const submit = el("button", { class: "btn primary wide login-submit", type: "submit", text: "Sign In" });
    const createAccount = el("button", {
      class: "btn ghost wide login-secondary",
      type: "button",
      text: "Create an Account",
      on: { click: () => showRegister("", false) },
    });
    createAccount.hidden = true;
    const error = el("p", { class: "form-error", role: "alert", text: message || "" });

    const form = el("form", { class: "login-form", on: { submit: onSubmit } }, [
      el("label", { class: "field-label", for: "username", text: "Username" }),
      username,
      el("label", { class: "field-label", for: "password", text: "Password" }),
      password,
      error,
      submit,
      createAccount,
    ]);
    username.id = "username";
    password.id = "password";

    showAuthCard([
      form,
      el("div", { class: "login-foot" }, [
        icon("lock"),
        el("span", {
          text: "Use your console username and password. Ask an administrator if you need an account.",
        }),
      ]),
    ]);
    username.focus();

    // Self-registration is an administrator setting, so the link only appears
    // when the current policy allows new accounts.
    api("/registration")
      .then((info) => {
        if (info.first_run) {
          showRegister("", true);
          return;
        }
        createAccount.hidden = !info.enabled;
      })
      .catch(() => {});

    async function onSubmit(event) {
      event.preventDefault();
      error.textContent = "";
      if (!username.value.trim() || !password.value) {
        error.textContent = "Enter the username and password to continue.";
        if (!username.value.trim()) username.focus();
        else password.focus();
        return;
      }
      submit.disabled = true;
      submit.textContent = "Signing in…";
      try {
        const session = await api("/login", {
          method: "POST",
          body: { username: username.value.trim(), password: password.value },
        });
        applySession(session);
        startApp();
      } catch (failure) {
        if (failure.code === "registration_required") {
          showRegister(failure.message, true);
          return;
        }
        error.textContent = failure.message;
        password.select();
      } finally {
        submit.disabled = false;
        submit.textContent = "Sign In";
      }
    }
  }

  // showRegister covers both first-run administrator creation and optional
  // member self-registration. The server decides which account type is created.
  function showRegister(message, firstRun) {
    const isFirstRun = Boolean(firstRun);
    const username = el("input", {
      class: "input",
      type: "text",
      placeholder: "Choose a username",
      autocomplete: "username",
      spellcheck: "false",
      required: "required",
      "aria-label": "Username",
    });
    const displayName = el("input", {
      class: "input",
      type: "text",
      placeholder: "Display name (optional)",
      autocomplete: "name",
      spellcheck: "false",
      "aria-label": "Display name",
    });
    const password = el("input", {
      class: "input login-key",
      type: "password",
      placeholder: "At least 8 characters",
      autocomplete: "new-password",
      spellcheck: "false",
      required: "required",
      "aria-label": "Password",
    });
    const confirmation = el("input", {
      class: "input login-key",
      type: "password",
      placeholder: "Repeat your password",
      autocomplete: "new-password",
      spellcheck: "false",
      required: "required",
      "aria-label": "Confirm password",
    });
    const submitText = isFirstRun ? "Create Administrator" : "Create Account";
    const submit = el("button", { class: "btn primary wide login-submit", type: "submit", text: submitText });
    const back = isFirstRun
      ? null
      : el("button", {
          class: "btn ghost wide login-secondary",
          type: "button",
          text: "Back to Sign In",
          on: { click: () => showLogin("") },
        });
    const error = el("p", { class: "form-error", role: "alert", text: message || "" });

    const form = el("form", { class: "login-form", on: { submit: onSubmit } }, [
      el("h2", { text: isFirstRun ? "First Run" : "Create Account" }),
      el("p", {
        class: "muted",
        text: isFirstRun
          ? "No administrator exists yet. The account you create now owns this gateway."
          : "Register a console account. An administrator can change your access later.",
      }),
      el("label", { class: "field-label", for: "username", text: "Username" }),
      username,
      el("label", { class: "field-label", for: "name", text: "Display Name" }),
      displayName,
      el("label", { class: "field-label", for: "password", text: "Password" }),
      password,
      el("label", { class: "field-label", for: "confirm-password", text: "Confirm Password" }),
      confirmation,
      error,
      submit,
      back,
    ]);
    username.id = "username";
    displayName.id = "name";
    password.id = "password";
    confirmation.id = "confirm-password";

    showAuthCard([
      form,
      el("div", { class: "login-foot" }, [
        icon("lock"),
        el("span", {
          text: isFirstRun
            ? "Use a username and password you control. Registration closes once the administrator exists."
            : "New accounts receive the default model access configured by an administrator.",
        }),
      ]),
    ]);
    username.focus();

    async function onSubmit(event) {
      event.preventDefault();
      error.textContent = "";
      if (!username.value.trim()) {
        error.textContent = "Choose a username to continue.";
        username.focus();
        return;
      }
      if (password.value.length < 8) {
        error.textContent = "The password must be at least 8 characters.";
        password.focus();
        return;
      }
      if (password.value !== confirmation.value) {
        error.textContent = "The passwords don't match.";
        confirmation.focus();
        return;
      }
      submit.disabled = true;
      submit.textContent = "Creating…";
      try {
        const session = await api("/register", {
          method: "POST",
          body: {
            username: username.value.trim(),
            name: displayName.value.trim(),
            password: password.value,
          },
        });
        applySession(session);
        startApp();
      } catch (failure) {
        if (failure.code === "registration_closed") {
          showLogin(failure.message);
          return;
        }
        error.textContent = failure.message;
      } finally {
        submit.disabled = false;
        submit.textContent = submitText;
      }
    }
  }

  function showNotice(title, message, retryable) {
    showAuthCard([
      el("h2", { text: title }),
      el("p", { class: "muted", text: message }),
      retryable
        ? el("button", {
            class: "btn ghost wide",
            type: "button",
            text: "Try Again",
            on: { click: () => boot() },
          })
        : null,
    ]);
  }

  // ----------------------------------------------------------------- shell

  function startApp() {
    const app = document.getElementById("app");
    app.className = "app";
    app.replaceChildren();

    const content = el("div", { class: "content" });
    const pageTitle = el("h1", { class: "page-title" });
    const pageSub = el("p", { class: "page-sub" });
    const refreshButton = el(
      "button",
      {
        class: "icon-btn",
        type: "button",
        title: "Refresh",
        "aria-label": "Refresh",
        on: { click: () => loadCurrent(true) },
      },
      [icon("refresh")]
    );

    const header = buildHeader(app);
    const main = el("main", { class: "main" }, [
      el("header", { class: "topbar" }, [
        el("div", { class: "page-head" }, [pageTitle, pageSub]),
        el("div", { class: "topbar-actions" }, [refreshButton]),
      ]),
      content,
    ]);

    app.append(
      el("div", { class: "layout" }, [
        header.node,
        el("div", { class: "layout-body" }, [buildSidebar(), main]),
      ])
    );
    state.refs = {
      content,
      pageTitle,
      pageSub,
      refreshButton,
      globalSearch: header.search,
      setGlobalSearch: header.setGlobalSearch,
      applyFilter: null,
    };
    loadCurrent(false);
  }

  function buildHeader(app) {
    const gateway = (state.session && state.session.gateway) || {};
    const user = (state.session && state.session.user) || {};
    const version = gateway.version ? "v" + gateway.version : "";

    const toggle = el(
      "button",
      {
        class: "icon-btn header-toggle",
        type: "button",
        title: "Toggle sidebar",
        "aria-label": "Toggle sidebar",
        "aria-expanded": "true",
        on: {
          click: () => {
            const collapsed = app.classList.toggle("sidebar-collapsed");
            toggle.setAttribute("aria-expanded", String(!collapsed));
          },
        },
      },
      [icon("menu")]
    );

    const search = el("input", {
      class: "input header-search",
      type: "search",
      placeholder: "Search models, providers, requests",
      "aria-label": "Search",
      autocomplete: "off",
      spellcheck: "false",
    });

    let pending = 0;
    search.addEventListener("input", () => {
      const term = search.value;
      window.clearTimeout(pending);
      pending = window.setTimeout(() => {
        if (state.refs && state.refs.applyFilter) state.refs.applyFilter(term);
      }, 90);
    });
    search.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        search.value = "";
        if (state.refs && state.refs.applyFilter) state.refs.applyFilter("");
        return;
      }
      if (event.key !== "Enter") return;
      event.preventDefault();
      if (state.refs && state.refs.applyFilter) {
        state.refs.applyFilter(search.value);
        return;
      }
      const term = search.value.trim();
      if (!term) return;
      state.requestFilter = term;
      const path = viewPath("requests");
      if (window.location.hash === "#/" + path) loadCurrent(false);
      else window.location.hash = "#/" + path;
    });

    function setGlobalSearch(value, placeholder) {
      search.value = value || "";
      if (placeholder) search.placeholder = placeholder;
    }

    const node = el("header", { class: "app-header" }, [
      el("div", { class: "header-left" }, [
        toggle,
        el("a", { class: "brand", href: "#/" + viewPath(defaultView()) }, [
          el("span", { class: "brand-mark", html: brandMark() }),
          el("span", { class: "brand-copy" }, [el("strong", { text: "LLM Proxy" })]),
          version ? el("span", { class: "version-pill", text: version }) : null,
        ]),
      ]),
      el("div", { class: "header-search-wrap" }, [icon("search"), search]),
      el("div", { class: "header-actions" }, [
        isAdmin()
          ? el("a", { class: "btn ghost small header-manage", href: "#/" + viewPath("admin-models") }, [
              icon("settings"),
              el("span", { text: "Manage" }),
            ])
          : null,
        el("span", { class: "header-status" }, [
          el("span", { class: "status-dot" }),
          el("span", { text: "Online" }),
        ]),
        el("a", { class: "user-chip", href: "#/" + viewPath("account") }, [
          el("span", { class: "avatar", text: initials(entityLabel(user)) }),
          el("span", { class: "user-meta" }, [
            el("span", { class: "user-name", text: entityLabel(user) }),
            el("span", { class: "user-role", text: roleLabel(user) }),
          ]),
        ]),
      ]),
    ]);

    return { node, search, setGlobalSearch };
  }

  function buildSidebar() {
    const user = state.session.user || {};
    const nav = el(
      "nav",
      { class: "nav", "aria-label": "Console sections" },
      visibleGroups().map((group) =>
        el("div", { class: "nav-group" }, [
          group.label ? el("span", { class: "nav-group-label", text: group.label }) : null,
          ...group.views.map((view) =>
            el("a", { class: "nav-link", href: "#/" + viewPath(view), dataset: { view }, title: VIEW_META[view].title }, [
              icon(view),
              el("span", { class: "nav-text", text: VIEW_META[view].title }),
            ])
          ),
        ])
      )
    );

    return el("aside", { class: "sidebar" }, [
      nav,
      el("div", { class: "sidebar-spacer" }),
      el("div", { class: "sidebar-footer" }, [
        el("a", { class: "account-mini", href: "#/" + viewPath("account") }, [
          el("span", { class: "avatar", text: initials(entityLabel(user)) }),
          el("span", { class: "user-meta" }, [
            el("span", { class: "user-name", text: entityLabel(user) }),
            el("span", { class: "user-role", text: roleLabel(user) }),
          ]),
        ]),
        el(
          "button",
          { class: "btn ghost small", type: "button", on: { click: logout } },
          [icon("logout"), el("span", { class: "nav-text", text: "Sign Out" })]
        ),
      ]),
    ]);
  }

  function onRouteChange() {
    if (!state.refs) return;
    window.scrollTo(0, 0);
    loadCurrent(false);
  }

  async function logout() {
    try {
      await api("/logout", { method: "POST" });
    } catch (_) {
      // The cookie is cleared locally even if the request failed.
    }
    state.session = null;
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    showLogin("");
  }

  const ADMIN_TAB_ICONS = {
    "admin-models": "models",
    "admin-providers": "providers",
    "admin-users": "users",
    "admin-settings": "settings",
  };

  function adminTabs(active) {
    return el("nav", { class: "admin-tabs", "aria-label": "Administration" }, [
      ...ADMIN_VIEWS.map((view) =>
        el(
          "a",
          {
            class: "admin-tab" + (view === active ? " active" : ""),
            href: "#/" + viewPath(view),
            dataset: { view },
          },
          [icon(ADMIN_TAB_ICONS[view]), el("span", { text: VIEW_META[view].tab || VIEW_META[view].title })]
        )
      ),
    ]);
  }

  async function loadCurrent(manual) {
    if (!state.refs) return;
    const view = currentView();
    const path = viewPath(view);
    const rawHash = window.location.hash.replace(/^#\/?/, "").split("?")[0];
    if (rawHash && rawHash !== path) {
      window.history.replaceState(null, "", window.location.pathname + window.location.search + "#/" + path);
    }
    state.view = view;
    const meta = VIEW_META[view];
    state.refs.pageTitle.textContent = meta.title;
    state.refs.pageSub.textContent = meta.subtitle;
    state.refs.applyFilter = null;
    if (state.refs.setGlobalSearch) {
      state.refs.setGlobalSearch("", searchPlaceholder(view));
    }
    updateActiveNav();

    if (manual) state.refs.refreshButton.classList.add("spinning");
    state.refs.content.replaceChildren(loadingState());

    try {
      const node = await VIEW_LOADERS[view]();
      if (currentView() !== view) return;
      state.refs.content.replaceChildren(
        ADMIN_VIEWS.indexOf(view) >= 0 ? el("div", { class: "stack" }, [adminTabs(view), node]) : node
      );
    } catch (error) {
      // Only a missing or expired session sends the user back to the login
      // screen. A 403 is an in-page permission error (for example, an API-key
      // session opening an administrator endpoint) and must not clear it.
      if (error.status === 401) {
        state.session = null;
        if (error.code === "registration_required") showRegister(error.message, true);
        else showLogin(error.message);
        return;
      }
      if (currentView() !== view) return;
      state.refs.content.replaceChildren(errorState(error.message, () => loadCurrent(true)));
    } finally {
      if (state.refs) state.refs.refreshButton.classList.remove("spinning");
    }
  }

  function updateActiveNav() {
    document.querySelectorAll(".nav-link").forEach((link) => {
      const active = link.dataset.view === state.view;
      link.classList.toggle("active", active);
      if (active) link.setAttribute("aria-current", "page");
      else link.removeAttribute("aria-current");
    });
  }

  // -------------------------------------------------------------- overview

  function overviewView(data) {
    const gateway = data.gateway || {};
    const summary = data.summary || {};
    const counts = data.counts || {};
    const history = data.history || {};
    const requests = Number(summary.requests || 0);
    const requestsDetail = history.full
      ? "Within the last " + formatNumber(history.capacity) + (isAdmin() ? " gateway requests" : " requests")
      : isAdmin()
        ? "Since gateway start"
        : "Your requests since gateway start";
    const heroMeta = [
      metaCell("Uptime", formatUptime(gateway.uptime_seconds)),
      metaCell("Models", formatNumber(counts.models)),
    ];
    if (isAdmin()) heroMeta.push(metaCell("Providers", formatNumber(counts.providers)));
    heroMeta.push(metaCell("Started", formatDate(gateway.started_at)));

    const hero = el("section", { class: "card hero" }, [
      el("div", { class: "hero-main" }, [
        el("div", { class: "hero-status" }, [el("span", { class: "status-dot" }), el("span", { text: "Online" })]),
        el("h2", { text: "Gateway Is Running" }),
        el("p", {
          class: "hero-version",
          text: gateway.version
            ? "LLM Proxy v" + gateway.version + " is accepting requests across every configured protocol."
            : "LLM Proxy is accepting requests across every configured protocol.",
        }),
        el(
          "div",
          { class: "hero-endpoints" },
          ENDPOINT_FORMATS.map((format) => chip(endpointLabel(format), "endpoint"))
        ),
      ]),
      el("div", { class: "hero-meta" }, heroMeta),
    ]);

    const metrics = el("section", { class: "metrics" }, [
      metricCard("Requests", formatNumber(requests), requestsDetail),
      metricCard(
        "Success Rate",
        requests ? Number(summary.success_rate || 0).toFixed(1) + "%" : "—",
        requests
          ? formatNumber(summary.errors) + " " + plural(summary.errors, "failure", "failures")
          : "No requests yet",
        requests ? (Number(summary.errors) > 0 ? "bad" : "good") : ""
      ),
      metricCard(
        "Tokens",
        compact(summary.total_tokens),
        compact(summary.input_tokens) + " input · " + compact(summary.output_tokens) + " output"
      ),
      metricCard("Avg Latency", requests ? formatDuration(summary.avg_duration_ms) : "—", "Per request"),
    ]);

    const activity = el("section", { class: "card" }, [
      cardHeader("Request Volume", "Requests per hour over the last 24 hours"),
      activityChart(data.series || []),
    ]);

    const modelTraffic = isAdmin()
      ? el("section", { class: "grid two" }, [
          breakdownCard("Top Models", data.by_model || [], "Model activity will appear here.", "Model"),
          breakdownCard("Top Providers", data.by_provider || [], "Provider activity will appear here.", "Provider"),
        ])
      : breakdownCard("Top Models", data.by_model || [], "Model activity will appear here.", "Model");

    const recent = el("section", { class: "card" }, [
      el("div", { class: "card-head" }, [
        el("div", {}, [
          el("h2", { text: "Recent Requests" }),
          el("p", { class: "card-sub", text: "Latest requests for your account" }),
        ]),
        el("a", { class: "link", href: "#/" + viewPath("requests"), text: "View All" }),
      ]),
      requestsTable(data.recent || [], { compact: true, showProvider: isAdmin() }),
    ]);

    return el("div", { class: "stack" }, [hero, metrics, activity, modelTraffic, recent]);
  }

  function metricCard(label, value, detail, tone) {
    return el("div", { class: "card metric" + (tone ? " " + tone : "") }, [
      el("span", { class: "metric-label", text: label }),
      el("strong", { class: "metric-value", text: value }),
      el("span", { class: "metric-detail", text: detail || "" }),
    ]);
  }

  function activityChart(series) {
    const total = series.reduce((sum, bucket) => sum + Number(bucket.requests || 0), 0);
    if (!series.length || total === 0) {
      return emptyState(
        "No Requests in the Last 24 Hours",
        "Volume will appear here as soon as your account starts making requests."
      );
    }
    const peak = Math.max(...series.map((bucket) => Number(bucket.requests || 0)));
    const max = Math.max(1, peak);
    const bars = el("div", { class: "chart-bars" });
    series.forEach((bucket) => {
      const requests = Number(bucket.requests || 0);
      const height = requests ? Math.max(6, Math.round((requests / max) * 100)) : 2;
      const bar = el("div", {
        class: "chart-bar" + (requests ? "" : " idle"),
        title:
          formatDate(bucket.start) +
          " · " +
          formatNumber(requests) +
          " requests" +
          (Number(bucket.errors || 0) ? " · " + formatNumber(bucket.errors) + " errors" : "") +
          " · " +
          compact(bucket.total_tokens) +
          " tokens",
      });
      bar.style.height = height + "%";
      bars.append(el("div", { class: "chart-col" }, [bar]));
    });

    const labels = el(
      "div",
      { class: "chart-labels" },
      series.map((bucket, index) =>
        el("span", { text: index % 6 === 0 || index === series.length - 1 ? formatClock(bucket.start) : "" })
      )
    );

    return el("div", { class: "chart" }, [
      el("div", { class: "chart-plot" }, [bars]),
      labels,
      el("div", { class: "chart-foot" }, [
        el("span", { text: "Peak: " + formatNumber(peak) + " requests/hour" }),
        el("span", { text: "Local time" }),
      ]),
    ]);
  }

  function breakdownCard(title, rows, emptyText, nameColumn) {
    const body = rows.length
      ? el(
          "div",
          { class: "table-wrap" },
          el("table", { class: "table compact" }, [
            el(
              "thead",
              {},
              el("tr", {}, [
                el("th", { text: nameColumn || "Name" }),
                el("th", { text: "Requests" }),
                el("th", { text: "Tokens" }),
                el("th", { text: "Avg Latency" }),
              ])
            ),
            el(
              "tbody",
              {},
              rows.slice(0, 8).map((row) =>
                el("tr", {}, [
                  el("td", {}, el("span", { class: "mono ellipsis", title: row.name, text: row.name })),
                  el("td", { text: formatNumber(row.requests) }),
                  el("td", { text: compact(row.total_tokens) }),
                  el("td", { text: formatDuration(row.avg_duration_ms) }),
                ])
              )
            ),
          ])
        )
      : emptyState("No Activity Yet", emptyText);

    return el("section", { class: "card" }, [cardHeader(title, ""), body]);
  }

  // ---------------------------------------------------------------- tables

  function requestsTable(records, options) {
    const settings = options || {};
    const compactMode = settings.compact;
    const showProvider = settings.showProvider !== false;
    if (!records.length) {
      return emptyState(
        settings.emptyTitle || "No Requests Yet",
        settings.emptyText || "Requests made through the gateway will appear here."
      );
    }
    const columns = compactMode
      ? ["Time", "Model", ...(showProvider ? ["Provider"] : []), "Status", "Duration", "Tokens"]
      : [
          "Time",
          "Request ID",
          "Model",
          ...(showProvider ? ["Provider"] : []),
          "Endpoint",
          "Status",
          "Duration",
          "Tokens",
        ];

    const rows = records.map((record) => {
      const cells = [];
      cells.push(
        el("td", { class: "nowrap" }, [
          el("span", { title: formatDate(record.time), text: timeAgo(record.time) }),
        ])
      );
      if (!compactMode) {
        cells.push(
          el("td", {}, [
            el("code", {
              class: "mono request-id",
              title: record.request_id,
              text: record.request_id || "—",
            }),
          ])
        );
      }
      cells.push(
        el("td", {}, el("span", { class: "mono ellipsis", title: record.model, text: record.model_name || record.model || "—" }))
      );
      if (showProvider) {
        cells.push(
          el("td", {}, [
            el("span", {
              class: "mono ellipsis",
              title: "Provider: " + (record.provider || "—") + (record.upstream_model ? " · Upstream: " + record.upstream_model : ""),
              text: record.provider_name || record.provider || "—",
            }),
          ])
        );
      }
      if (!compactMode) {
        cells.push(
          el("td", {}, [
            el("span", { class: "row-chips" }, [
              chip(endpointLabel(record.inbound), "endpoint"),
              record.stream
                ? el("span", { class: "stream-chip", title: "Streaming response", html: ICONS.stream })
                : null,
              record.upstream && record.upstream !== record.inbound
                ? chip("→ " + endpointLabel(record.upstream), "subtle")
                : null,
            ]),
          ])
        );
      }
      cells.push(el("td", {}, statusBadge(record.status)));
      cells.push(el("td", { class: "nowrap", text: formatDuration(record.duration_ms) }));
      cells.push(
        el("td", {
          class: "nowrap",
          title: formatNumber(record.input_tokens) + " input · " + formatNumber(record.output_tokens) + " output",
          text: compact(record.total_tokens),
        })
      );
      return el("tr", {}, cells);
    });

    return el(
      "div",
      { class: "table-wrap" },
      el("table", { class: "table" }, [
        el("thead", {}, el("tr", {}, columns.map((column) => el("th", { text: column })))),
        el("tbody", {}, rows),
      ])
    );
  }

  function statusBadge(status) {
    const code = Number(status || 0);
    let tone = "warn";
    if (code < 300) tone = "good";
    else if (code >= 500) tone = "bad";
    return el("span", { class: "status status-" + tone, text: String(code || "—") });
  }

  // ---------------------------------------------------------------- models

  function searchableList(options) {
    const search = el("input", {
      class: "input search",
      type: "search",
      placeholder: options.placeholder,
      value: state[options.filterKey],
      "aria-label": options.placeholder,
    });
    const note = el("span", { class: "toolbar-note", "aria-live": "polite" });
    const host = el("div");

    function render() {
      const term = state[options.filterKey].trim().toLowerCase();
      const filtered = options.items.filter((item) => options.match(item, term));
      note.textContent = countLabel(filtered.length, options.items.length, options.noun);
      if (!filtered.length) {
        host.replaceChildren(
          options.items.length === 0
            ? emptyState(options.emptyTitle, options.emptyText)
            : emptyState(options.searchEmptyTitle, "Try a different search term.")
        );
        return;
      }
      host.replaceChildren(
        el("div", { class: "list" }, [
          el(
            "div",
            { class: "list-head " + options.rowClass },
            options.columns.map((column) => el("span", { text: column }))
          ),
          ...filtered.map(options.renderRow),
        ])
      );
    }

    search.addEventListener("input", () => {
      state[options.filterKey] = search.value;
      if (state.refs && state.refs.setGlobalSearch) state.refs.setGlobalSearch(search.value, options.placeholder);
      render();
    });
    render();

    if (state.refs) {
      state.refs.setGlobalSearch(state[options.filterKey], options.placeholder);
      state.refs.applyFilter = (term) => {
        state[options.filterKey] = term;
        search.value = term;
        render();
      };
    }

    return el("div", { class: "stack" }, [
      el("div", { class: "toolbar" }, [
        el("div", { class: "search-wrap" }, [icon("search"), search]),
        note,
        options.action ? el("div", { class: "toolbar-actions" }, [options.action]) : null,
      ]),
      host,
    ]);
  }

  function modelsView(data) {
    return searchableList({
      items: data.models || [],
      filterKey: "modelFilter",
      placeholder: "Search models",
      noun: "model",
      match: matchesModel,
      renderRow: modelRow,
      rowClass: "model-row",
      columns: ["Model", "Modalities", "Capabilities", "Context", "Max Output", "Providers", "Actions"],
      emptyTitle: "No Models Available",
      emptyText: "Create a model alias and bind it to an upstream provider model.",
      searchEmptyTitle: "No Models Match Your Search",
      action: addButton("New Model", () => editModel(null)),
    });
  }

  // catalogView is the read-only Models page every account can open. It shows
  // the models the gateway will accept for this account as cards and hides
  // upstream routing details.
  function catalogView(data) {
    const items = data.models || [];
    const search = el("input", {
      class: "input search",
      type: "search",
      placeholder: "Search models",
      value: state.catalogFilter,
      "aria-label": "Search models",
    });
    const note = el("span", { class: "toolbar-note", "aria-live": "polite" });
    const host = el("div");

    function render() {
      const term = state.catalogFilter.trim().toLowerCase();
      const filtered = items.filter((model) => matchesModel(model, term));
      note.textContent = countLabel(filtered.length, items.length, "model");
      if (!filtered.length) {
        host.replaceChildren(
          items.length
            ? emptyState("No Models Match Your Search", "Try a different search term.")
            : emptyState(
                "No Models Available",
                isAdmin()
                  ? "Create a model alias and bind it to an upstream provider model."
                  : "Your account doesn't have access to any models yet. Ask an administrator to grant access."
              )
        );
        return;
      }
      host.replaceChildren(el("div", { class: "model-grid" }, filtered.map(catalogCard)));
    }

    search.addEventListener("input", () => {
      state.catalogFilter = search.value;
      if (state.refs && state.refs.setGlobalSearch) state.refs.setGlobalSearch(search.value, "Search models");
      render();
    });
    render();

    if (state.refs) {
      state.refs.setGlobalSearch(state.catalogFilter, "Search models");
      state.refs.applyFilter = (term) => {
        state.catalogFilter = term;
        search.value = term;
        render();
      };
    }

    return el("div", { class: "stack" }, [
      el("div", { class: "toolbar" }, [
        el("div", { class: "search-wrap" }, [icon("search"), search]),
        note,
      ]),
      host,
    ]);
  }

  function catalogCard(model) {
    const capabilities = [];
    if (model.reasoning) capabilities.push(chip("Reasoning"));
    if (model.tools) capabilities.push(chip("Tools"));
    const routes = Number(model.providers || 0);
    const name = entityLabel(model);
    return el("article", { class: "card model-card" }, [
      el("div", { class: "model-card-head" }, [
        el("div", { class: "model-card-title" }, [
          el("h3", { class: "model-name", title: name, text: name }),
          modelCopyButton(model),
        ]),
        chip("Available", "good"),
      ]),
      el("span", { class: "list-sub mono", title: model.id, text: model.id }),
      el("div", { class: "model-card-block" }, [
        el("span", { class: "model-card-label", text: "Modalities" }),
        modalityFlow(model.modalities, model.output_modalities),
      ]),
      el("div", { class: "model-card-block" }, [
        el("span", { class: "model-card-label", text: "Capabilities" }),
        capabilities.length
          ? el("span", { class: "row-chips" }, capabilities)
          : el("span", { class: "muted", text: "Standard Text" }),
      ]),
      el("dl", { class: "model-card-stats" }, [
        el("div", { class: "model-card-stat" }, [
          el("dt", { text: "Context" }),
          el("dd", { text: model.context_length ? formatSize(model.context_length) : "—" }),
        ]),
        el("div", { class: "model-card-stat" }, [
          el("dt", { text: "Max Output" }),
          el("dd", { text: model.max_output_length ? formatSize(model.max_output_length) : "—" }),
        ]),
        el("div", { class: "model-card-stat" }, [
          el("dt", { text: "Routes" }),
          el("dd", { text: routes ? formatNumber(routes) : "—" }),
        ]),
      ]),
    ]);
  }

  // bindingEditor maps a public model alias onto upstream models. The upstream
  // model is chosen from the selected provider's catalog. Providers without a
  // catalog fall back to a free-text field so they can be bound before their
  // catalog is filled in.
  function bindingEditor(bindings, providers) {
    const rowsNode = el("div", { class: "binding-rows" });
    const rows = [];
    const add = el("button", {
      class: "btn ghost small editor-add binding-add",
      type: "button",
      text: "+ Add Binding",
    });

    function upstreamModels(providerID) {
      const provider = providers.find((item) => item.id === providerID);
      return (provider && provider.upstream_models) || [];
    }

    function syncOrder() {
      rows.forEach((row, index) => {
        row.up.disabled = index === 0;
        row.down.disabled = index === rows.length - 1;
      });
    }

    function move(entry, delta) {
      const index = rows.indexOf(entry);
      const target = index + delta;
      if (index < 0 || target < 0 || target >= rows.length) return;
      rows.splice(index, 1);
      rows.splice(target, 0, entry);
      rowsNode.replaceChildren(...rows.map((row) => row.row));
      syncOrder();
    }

    function addRow(binding) {
      const current = binding || {};
      const options = providers.map((provider) =>
        el("option", {
          value: provider.id,
          text: entityLabel(provider) + (provider.name ? " (" + provider.id + ")" : ""),
          selected: provider.id === current.provider_id,
        })
      );
      if (!providers.length) {
        options.push(el("option", { value: "", text: "No Providers Configured" }));
      }
      if (current.provider_id && !providers.some((provider) => provider.id === current.provider_id)) {
        options.push(el("option", { value: current.provider_id, text: current.provider_id, selected: true }));
      }
      const select = el("select", { class: "input" }, options);
      if (current.provider_id) select.value = current.provider_id;

      const modelCell = el("div", { class: "binding-model-cell" });

      function renderModel(value, keepMissing) {
        const models = upstreamModels(select.value);
        let control;
        if (models.length) {
          const choices = models.slice();
          if (keepMissing && value && !choices.includes(value)) choices.push(value);
          control = el(
            "select",
            { class: "input mono" },
            choices.map((model) =>
              el("option", {
                value: model,
                text: model === value && !models.includes(model) ? model + " (not in catalog)" : model,
              })
            )
          );
          control.value = value && choices.includes(value) ? value : models[0];
        } else {
          control = el("input", {
            class: "input mono",
            type: "text",
            placeholder: "No catalog — type a model ID",
            autocomplete: "off",
            spellcheck: "false",
          });
          control.value = value || "";
        }
        modelCell.replaceChildren(control);
      }

      renderModel(current.upstream_model || "", true);

      const retry = el("input", {
        class: "input",
        type: "number",
        step: "0.1",
        min: "0",
        placeholder: "default",
        "aria-label": "Max retry after seconds",
      });
      retry.value =
        current.max_retry_after === undefined || current.max_retry_after === null
          ? ""
          : String(current.max_retry_after);

      const up = el("button", {
        class: "icon-btn binding-move",
        type: "button",
        "aria-label": "Move binding up",
        title: "Move up",
        text: "↑",
      });
      const down = el("button", {
        class: "icon-btn binding-move",
        type: "button",
        "aria-label": "Move binding down",
        title: "Move down",
        text: "↓",
      });
      const order = el("div", { class: "binding-order" }, [up, down]);
      const remove = el("button", {
        class: "icon-btn editor-remove binding-remove",
        type: "button",
        "aria-label": "Remove binding",
        text: "×",
      });

      const row = el("div", { class: "editor-row binding-row" }, [
        order,
        el("label", { class: "editor-cell binding-cell" }, [
          el("span", { class: "field-label editor-label", text: "Provider" }),
          select,
        ]),
        el("label", { class: "editor-cell binding-cell binding-model" }, [
          el("span", { class: "field-label editor-label", text: "Upstream Model" }),
          modelCell,
        ]),
        el("label", { class: "editor-cell binding-cell binding-retry" }, [
          el("span", { class: "field-label editor-label", text: "Retry After (s)" }),
          retry,
        ]),
        remove,
      ]);

      select.addEventListener("change", () => {
        const previous = modelCell.querySelector("input, select");
        const value = previous ? previous.value : "";
        const models = upstreamModels(select.value);
        renderModel(models.includes(value) ? value : "", false);
      });
      up.addEventListener("click", () => move(entry, -1));
      down.addEventListener("click", () => move(entry, 1));
      remove.addEventListener("click", () => {
        const index = rows.indexOf(entry);
        if (index >= 0) rows.splice(index, 1);
        row.remove();
        syncOrder();
      });

      const entry = { select, modelCell, retry, row, up, down };
      rows.push(entry);
      rowsNode.append(row);
      syncOrder();
    }

    if (bindings.length) {
      bindings.forEach((binding) => addRow(binding));
    } else {
      addRow();
    }
    add.addEventListener("click", () => addRow());

    return {
      node: el("div", { class: "editor binding-editor" }, [
        el("div", { class: "editor-head binding-head" }, [
          el("span", { class: "binding-head-order", text: "↕", title: "Order" }),
          el("span", { text: "Provider" }),
          el("span", { text: "Upstream Model" }),
          el("span", { text: "Retry After (s)" }),
          el("span", { class: "editor-head-end" }),
        ]),
        rowsNode,
        el("div", { class: "editor-foot" }, [add]),
      ]),
      read() {
        if (!rows.length) throw new Error("Add at least one provider binding.");
        const seen = new Set();
        return rows.map((row) => {
          const providerID = row.select.value;
          const modelControl = row.modelCell.querySelector("input, select");
          const upstreamModel = modelControl ? modelControl.value.trim() : "";
          if (!providerID) throw new Error("Choose a provider for every binding.");
          if (!upstreamModel) throw new Error("Choose an upstream model for every binding.");
          const key = providerID + "\u0000" + upstreamModel;
          if (seen.has(key)) {
            throw new Error("Each upstream model can only be bound once per provider.");
          }
          seen.add(key);
          const binding = { provider_id: providerID, upstream_model: upstreamModel };
          const retry = row.retry.value.trim();
          if (retry !== "") binding.max_retry_after = Number(retry);
          return binding;
        });
      },
    };
  }

  function editModel(model) {
    const detailPromise = model
      ? api("/models/" + encodeURIComponent(model.id)).then((payload) => payload.model)
      : Promise.resolve({});
    const providersPromise = api("/providers").then((payload) => payload.providers || []);
    Promise.all([detailPromise, providersPromise])
      .then(([detail, providers]) => {
        openModal(
          model ? "Edit Model" : "New Model",
          "Bind the public model ID to an upstream model and describe its capabilities.",
          [
            {
              name: "id",
              label: "Model ID",
              section: "Identity",
              sectionHint: "Public alias API clients call, plus the name shown across the console.",
              value: detail.id,
              placeholder: "gpt-4o",
              required: true,
              disabled: Boolean(model),
              help: model ? "The model ID cannot be changed; create a new model instead." : undefined,
            },
            {
              name: "name",
              label: "Display Name",
              value: detail.name,
              placeholder: "GPT-4o",
              help: "Console label only. API clients still use the model ID.",
            },
            {
              name: "context_length",
              label: "Context Length",
              section: "Limits",
              sectionHint: "Token budgets advertised to clients and the request timeout.",
              grid: "three",
              type: "number",
              value: detail.context_length,
            },
            { name: "max_output_length", label: "Max Output Tokens", type: "number", value: detail.max_output_length },
            { name: "timeout", label: "Timeout (Seconds)", type: "number", value: detail.timeout, step: "0.1" },
            {
              name: "modalities",
              label: "Input Modalities",
              section: "Capabilities",
              sectionHint: "Declare what this model accepts and which protocol features it supports.",
              type: "tokens",
              options: modalityOptions((detail.modalities || ["text"]).join(", ")),
              value: (detail.modalities || ["text"]).join(", "),
              wide: true,
            },
            {
              name: "output_modalities",
              label: "Output Modalities",
              type: "tokens",
              options: modalityOptions((detail.output_modalities || ["text"]).join(", ")),
              value: (detail.output_modalities || ["text"]).join(", "),
              wide: true,
            },
            {
              name: "reasoning",
              label: "Reasoning",
              type: "checkbox",
              value: Boolean(detail.reasoning),
              help: "Accepts reasoning controls.",
            },
            {
              name: "tools",
              label: "Tools",
              type: "checkbox",
              value: Boolean(detail.tools),
              help: "Accepts tool and function calling.",
            },
            {
              name: "routing",
              label: "Routing Strategy",
              section: "Provider Bindings",
              sectionHint:
                "Choose how a new conversation picks its first provider. Once an upstream serves a request, that conversation stays on it for later turns.",
              type: "select",
              value: detail.routing || "priority",
              options: [
                { value: "priority", label: "Priority — declared order, fail over to the next" },
                { value: "round_robin", label: "Round Robin — rotate new conversations" },
                { value: "random", label: "Random — shuffle new conversations" },
              ],
              wide: true,
              help: "Send x-session-id to guarantee affinity when clients do not resend a stable first message.",
            },
            {
              name: "providers",
              label: "Provider Bindings",
              section: "Provider Bindings",
              type: "bindings",
              wide: true,
              hideLabel: true,
              value: detail.providers || [],
              providers,
            },
          ],
          async (values) => {
            const body = {
              id: values.id.trim(),
              name: values.name.trim(),
              routing: values.routing,
              context_length: values.context_length,
              max_output_length: values.max_output_length,
              timeout: values.timeout,
              modalities: splitList(values.modalities),
              output_modalities: splitList(values.output_modalities),
              reasoning: values.reasoning,
              tools: values.tools,
              providers: values.providers,
            };
            const path = model ? "/models/" + encodeURIComponent(model.id) : "/models";
            await api(path, { method: model ? "PUT" : "POST", body });
            await loadCurrent(true);
          },
          "Save",
          { wide: true }
        );
      })
      .catch((error) => showToast(error.message));
  }

  function deleteModel(model) {
    confirmDelete("model", model.id, async () => {
      await api("/models/" + encodeURIComponent(model.id), { method: "DELETE" });
      await loadCurrent(true);
    });
  }

  function rowActions(edit, remove) {
    return el("div", { class: "row-actions" }, [
      el("button", { class: "btn ghost small", type: "button", text: "Edit", on: { click: edit } }),
      el("button", { class: "btn ghost small danger", type: "button", text: "Delete", on: { click: remove } }),
    ]);
  }

  function showToast(message) {
    const toast = el("div", { class: "toast", role: "status", text: message });
    document.body.append(toast);
    setTimeout(() => toast.remove(), 4200);
  }

  function matchesModel(model, term) {
    const parts = [
      model.id,
      model.name,
      ...(model.modalities || []),
      ...(model.output_modalities || []),
      model.reasoning ? "reasoning" : "",
      model.tools ? "tools" : "",
    ];
    const bindings = Array.isArray(model.providers) ? model.providers : [];
    bindings.forEach((provider) => {
      parts.push(provider.id, provider.name, provider.upstream_model, ...(provider.endpoints || []));
    });
    return matchesQuery(parts, term);
  }

  function modelCopyButton(model) {
    const button = el(
      "button",
      {
        class: "copy-btn",
        type: "button",
        title: "Copy model ID",
        "aria-label": "Copy model ID",
        on: { click: () => copyModelID(model.id, button) },
      },
      [icon("copy")]
    );
    return button;
  }

  function modelRow(model) {
    const providers = model.providers || [];
    const name = entityLabel(model);
    const providerNames = providers.map(entityLabel).join(", ");
    const capabilities = [];
    if (model.reasoning) capabilities.push(chip("Reasoning"));
    if (model.tools) capabilities.push(chip("Tools"));
    return el("article", { class: "list-row model-row" }, [
      el("div", { class: "list-identity" }, [
        el("div", { class: "list-title" }, [
          el("h3", { class: "model-name", title: name, text: name }),
          modelCopyButton(model),
        ]),
        name !== model.id ? el("span", { class: "list-sub mono", title: model.id, text: model.id }) : null,
      ]),
      el("div", { class: "list-field list-modalities" }, [
        el("span", { class: "list-label", text: "Modalities" }),
        modalityFlow(model.modalities, model.output_modalities),
      ]),
      el("div", { class: "list-field list-capabilities" }, [
        el("span", { class: "list-label", text: "Capabilities" }),
        capabilities.length
          ? el("span", { class: "row-chips" }, capabilities)
          : el("span", { class: "list-value", text: "—" }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Context" }),
        el("span", { class: "list-value", text: model.context_length ? formatSize(model.context_length) : "—" }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Max Output" }),
        el("span", { class: "list-value", text: model.max_output_length ? formatSize(model.max_output_length) : "—" }),
      ]),
      el("div", { class: "list-field list-providers" }, [
        el("span", { class: "list-label", text: "Providers" }),
        el("span", {
          class: "list-value list-ellipsis",
          title: providerNames || "—",
          text: providerNames || "—",
        }),
        el("span", {
          class: "list-sub",
          title: "First-provider strategy for a new conversation",
          text: routingLabel(model.routing) + " routing",
        }),
      ]),
      el("div", { class: "list-field list-actions" }, [
        el("span", { class: "list-label", text: "Actions" }),
        rowActions(
          () => editModel(model),
          () => deleteModel(model)
        ),
      ]),
    ]);
  }

  // ------------------------------------------------------------- providers

  function providersView(data) {
    return searchableList({
      items: data.providers || [],
      filterKey: "providerFilter",
      placeholder: "Search providers",
      noun: "provider",
      match: matchesProvider,
      renderRow: providerRow,
      rowClass: "provider-row",
      columns: ["Provider", "Endpoints", "Upstream Models", "Retries", "Actions"],
      emptyTitle: "No Providers Available",
      emptyText: "Add an upstream provider to connect models to an API.",
      searchEmptyTitle: "No Providers Match Your Search",
      action: addButton("New Provider", () => editProvider(null)),
    });
  }

  // endpointEditor renders NewAPI-style channel rows. Every row shares the
  // provider base URL and sets its own upstream path plus converter.
  function endpointEditor(configured) {
    const rowsNode = el("div", { class: "endpoint-rows" });
    const rows = [];
    const add = el("button", {
      class: "btn ghost small editor-add endpoint-add",
      type: "button",
      text: "+ Add Endpoint",
    });

    function usedTypes() {
      return new Set(rows.map((row) => row.select.value));
    }

    function syncOptions() {
      const used = usedTypes();
      rows.forEach((row) => {
        Array.from(row.select.options).forEach((option) => {
          option.disabled = option.value !== row.select.value && used.has(option.value);
        });
      });
      add.disabled = rows.length >= ENDPOINT_FORMATS.length;
    }

    function fillConverters(select, format, selected) {
      select.replaceChildren(
        ...converterOptions(format).map((option) =>
          el("option", { value: option.value, text: option.label })
        )
      );
      const valid = Array.from(select.options).some((option) => option.value === selected);
      select.value = valid ? selected : "none";
    }

    function addRow(type, endpoint) {
      const initial =
        type || ENDPOINT_FORMATS.find((format) => !usedTypes().has(format)) || ENDPOINT_FORMATS[0];
      const select = el(
        "select",
        { class: "input" },
        ENDPOINT_FORMATS.map((format) => el("option", { value: format, text: endpointLabel(format) }))
      );
      select.value = initial;
      const converter = el("select", { class: "input" });
      fillConverters(converter, initial, (endpoint && endpoint.converter) || "none");
      const path = el("input", {
        class: "input mono",
        placeholder: defaultEndpointPath(initial),
        spellcheck: "false",
      });
      path.value = (endpoint && endpoint.path) || "";
      const remove = el("button", {
        class: "icon-btn editor-remove endpoint-remove",
        type: "button",
        "aria-label": "Remove endpoint",
        text: "×",
      });
      const row = el("div", { class: "editor-row endpoint-row" }, [
        el("label", { class: "editor-cell endpoint-cell" }, [
          el("span", { class: "field-label editor-label", text: "Endpoint" }),
          select,
        ]),
        el("label", { class: "editor-cell endpoint-cell" }, [
          el("span", { class: "field-label editor-label", text: "Converter" }),
          converter,
        ]),
        el("label", { class: "editor-cell endpoint-cell" }, [
          el("span", { class: "field-label editor-label", text: "Upstream Path (Optional)" }),
          path,
        ]),
        remove,
      ]);
      const entry = { select, converter, path, row };
      let previousType = initial;
      select.addEventListener("change", () => {
        // The old path may belong to the previous endpoint type; reset a blank
        // or default path so the new type uses its own default instead.
        const current = path.value.trim();
        if (!current || current === defaultEndpointPath(previousType)) path.value = "";
        path.placeholder = defaultEndpointPath(select.value);
        fillConverters(converter, select.value, converter.value);
        syncOptions();
        previousType = select.value;
      });
      remove.addEventListener("click", () => {
        const index = rows.indexOf(entry);
        if (index >= 0) rows.splice(index, 1);
        row.remove();
        syncOptions();
      });
      rows.push(entry);
      rowsNode.append(row);
      syncOptions();
    }

    const configuredTypes = ENDPOINT_FORMATS.filter((format) => configured && configured[format]);
    if (configuredTypes.length) {
      configuredTypes.forEach((format) => addRow(format, configured[format]));
    } else {
      addRow();
    }
    add.addEventListener("click", () => addRow());

    return {
      node: el("div", { class: "editor endpoint-editor" }, [
        el("div", { class: "editor-head endpoint-head" }, [
          el("span", { text: "Endpoint" }),
          el("span", { text: "Converter" }),
          el("span", { text: "Upstream Path (Optional)" }),
          el("span", { class: "editor-head-end" }),
        ]),
        rowsNode,
        el("div", { class: "editor-foot" }, [add]),
      ]),
      read() {
        if (!rows.length) throw new Error("Add at least one endpoint.");
        const result = {};
        rows.forEach((row) => {
          const type = row.select.value;
          if (result[type]) throw new Error("Each endpoint type can be configured only once.");
          const endpoint = { converter: row.converter.value };
          const path = row.path.value.trim();
          if (path) endpoint.path = path;
          result[type] = endpoint;
        });
        return result;
      },
    };
  }

  function editProvider(provider) {
    const load = provider
      ? api("/providers/" + encodeURIComponent(provider.id)).then((payload) => payload.provider)
      : Promise.resolve({});
    load
      .then((detail) => {
        openModal(
          provider ? "Edit Provider" : "New Provider",
          "Connection, catalog, endpoints and routing for one upstream provider.",
          [
            {
              name: "id",
              label: "Provider ID",
              section: "Identity",
              sectionHint: "Stable identifier used by model bindings and the API.",
              value: detail.id,
              placeholder: "openai",
              required: true,
              disabled: Boolean(provider),
              help: provider ? "The provider ID cannot be changed; create a new provider instead." : undefined,
            },
            { name: "name", label: "Display Name", value: detail.name, placeholder: "OpenAI" },
            {
              name: "base_url",
              label: "Base URL",
              section: "Connection",
              sectionHint: "Every endpoint joins its path to this URL; query parameters on the base URL are kept.",
              value: detail.base_url,
              placeholder: "https://api.openai.com/v1",
              required: true,
              wide: true,
            },
            {
              name: "api_key",
              label: "API Key",
              type: "password",
              value: detail.api_key,
              autocomplete: "new-password",
              wide: true,
              help: "Sent as the upstream credential. Leave blank to keep the stored key on edit.",
            },
            {
              name: "upstream_models",
              label: "Upstream Models",
              section: "Upstream Catalog",
              sectionHint:
                "Model IDs this provider serves. Type a name and press Enter to add it; models bind to these IDs.",
              type: "tags",
              value: detail.upstream_models || [],
              placeholder: "gpt-4o",
              wide: true,
              help: "Enter or comma adds a model. Backspace removes the last label; × removes a specific one.",
            },
            {
              name: "endpoints",
              label: "Endpoints",
              section: "Endpoints",
              sectionHint:
                "One row per client protocol. Passthrough forwards the request unchanged; a converter translates requests, responses and streams. Paths default to the canonical upstream route.",
              type: "endpoints",
              wide: true,
              hideLabel: true,
              value: detail.endpoints || {},
            },
            {
              name: "max_retries",
              label: "Max Retries",
              section: "Advanced",
              sectionHint: "Retry policy, transport and extra headers applied to every upstream request.",
              grid: "three",
              type: "number",
              value: detail.max_retries ?? 2,
            },
            { name: "timeout", label: "Timeout (Seconds)", type: "number", value: detail.timeout, step: "0.1" },
            { name: "proxy", label: "Proxy URL", value: detail.proxy, placeholder: "socks5://127.0.0.1:1080" },
            {
              name: "headers",
              label: "Custom Headers (JSON)",
              type: "textarea",
              rows: 4,
              wide: true,
              value: JSON.stringify(detail.headers || {}, null, 2),
              help: "Merged into every upstream request. Configured headers can override defaults.",
            },
          ],
          async (values) => {
            let headers;
            try {
              headers = JSON.parse(values.headers || "{}");
            } catch (error) {
              throw new Error("Headers must be valid JSON.");
            }
            const body = {
              id: values.id.trim(),
              name: values.name.trim(),
              base_url: values.base_url.trim(),
              api_key: values.api_key,
              max_retries: values.max_retries,
              timeout: values.timeout,
              proxy: values.proxy.trim(),
              headers,
              endpoints: values.endpoints,
              upstream_models: values.upstream_models,
            };
            const path = provider ? "/providers/" + encodeURIComponent(provider.id) : "/providers";
            await api(path, { method: provider ? "PUT" : "POST", body });
            await loadCurrent(true);
          },
          "Save",
          { wide: true }
        );
      })
      .catch((error) => showToast(error.message));
  }

  function deleteProvider(provider) {
    confirmDelete("provider", provider.id, async () => {
      await api("/providers/" + encodeURIComponent(provider.id), { method: "DELETE" });
      await loadCurrent(true);
    });
  }

  function matchesProvider(provider, term) {
    const endpoints = provider.endpoints || {};
    const converters = Object.values(endpoints).map((endpoint) => endpoint.converter);
    const paths = Object.values(endpoints).map((endpoint) => endpoint.path);
    return matchesQuery(
      [
        provider.id,
        provider.name,
        provider.base_url,
        ...Object.keys(endpoints),
        ...converters,
        ...paths,
        ...(provider.upstream_models || []),
        ...(provider.models || []),
        ...Object.values(provider.model_names || {}),
      ],
      term
    );
  }

  // endpointField lists the shared base URL plus each endpoint's path and
  // converter, so the upstream target stays identifiable.
  function endpointField(provider) {
    const endpoints = provider.endpoints || {};
    const configured = ENDPOINT_FORMATS.filter((format) => endpoints[format]);
    if (!configured.length) return el("span", { class: "list-value", text: "—" });
    return el(
      "span",
      { class: "list-urls" },
      configured.map((format) => {
        const endpoint = endpoints[format];
        const target = String(provider.base_url || "") + String(endpoint.path || "");
        const converter = endpoint.converter && endpoint.converter !== "none"
          ? " · " + converterLabel(endpoint.converter)
          : "";
        return el("span", { class: "url-endpoint" }, [
          el("span", { class: "url-protocol", text: endpointLabel(format) }),
          el("code", { class: "mono url-full", title: target, text: target }),
          converter ? el("span", { class: "list-sub", text: converter }) : null,
        ]);
      })
    );
  }

  function providerRow(provider) {
    const upstreamModels = provider.upstream_models || [];
    const aliasNames = (provider.models || []).map((model) => (provider.model_names || {})[model] || model);
    return el("article", { class: "list-row provider-row" }, [
      el("div", { class: "list-identity" }, [
        el("div", { class: "list-title" }, [
          el("h3", { class: "provider-name", title: provider.id, text: entityLabel(provider) }),
        ]),
        el("div", { class: "list-sub-row" }, [
          el("span", { class: "list-sub mono", title: provider.id, text: provider.id }),
        ]),
      ]),
      el("div", { class: "list-field list-base-url" }, [
        el("span", { class: "list-label", text: "Endpoints" }),
        endpointField(provider),
      ]),
      el("div", { class: "list-field list-models" }, [
        el("span", { class: "list-label", text: "Upstream Models" }),
        el("span", {
          class: "list-value list-ellipsis",
          title: (upstreamModels.join(", ") || "—") + (aliasNames.length ? " · Aliases: " + aliasNames.join(", ") : ""),
          text: upstreamModels.join(", ") || "—",
        }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Retries" }),
        el("span", { class: "list-value", text: String(Number(provider.max_retries) || 0) }),
      ]),
      el("div", { class: "list-field list-actions" }, [
        el("span", { class: "list-label", text: "Actions" }),
        rowActions(
          () => editProvider(provider),
          () => deleteProvider(provider)
        ),
      ]),
    ]);
  }

  // ----------------------------------------------------------------- users

  function allowedModelsLabel(user) {
    const allowed = user.allowed_models || [];
    if (allowed.indexOf("*") >= 0) return "All Models, Including Future";
    if (!allowed.length) return "No Models";
    return allowed.join(", ");
  }

  function matchesUser(user, term) {
    return matchesQuery(
      [
        user.id,
        user.name,
        user.is_admin ? "administrator admin" : "member",
        user.key_count,
        ...(user.allowed_models || []),
      ],
      term
    );
  }

  function userRow(user) {
    const allowed = allowedModelsLabel(user);
    return el("article", { class: "list-row user-row" }, [
      el("div", { class: "list-identity" }, [
        el("div", { class: "list-title" }, [
          el("h3", { class: "model-name", title: user.name || user.id, text: user.name || user.id }),
          user.is_admin ? chip("Admin", "good") : null,
        ]),
        el("span", { class: "list-sub mono", text: user.id }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Role" }),
        chip(user.is_admin ? "Administrator" : "Member", user.is_admin ? "good" : "subtle"),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "API Keys" }),
        el("span", { class: "list-value", text: String(Number(user.key_count) || 0) }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Allowed Models" }),
        el("span", { class: "list-value list-ellipsis", title: allowed, text: allowed }),
      ]),
      el("div", { class: "list-field list-actions" }, [
        el("span", { class: "list-label", text: "Actions" }),
        el("div", { class: "row-actions" }, [
          user.is_admin
            ? el("span", { class: "muted", text: "Protected" })
            : el("button", {
                class: "btn ghost small",
                type: "button",
                text: "Edit",
                on: { click: () => editUser(user) },
              }),
          user.is_admin
            ? null
            : el("button", {
                class: "btn ghost small danger",
                type: "button",
                text: "Delete",
                on: { click: () => deleteUser(user) },
              }),
        ]),
      ]),
    ]);
  }

  function usersView(data) {
    return searchableList({
      items: data.users || [],
      filterKey: "userFilter",
      placeholder: "Search users",
      noun: "user",
      match: matchesUser,
      renderRow: userRow,
      rowClass: "user-row",
      columns: ["User", "Role", "API Keys", "Allowed Models", "Actions"],
      emptyTitle: "No Users Yet",
      emptyText: "Create an account so a teammate can sign in and manage their own API keys.",
      searchEmptyTitle: "No Users Match Your Search",
      action: addButton("New User", () => editUser(null)),
    });
  }

  function editUser(user) {
    const load = user
      ? api("/users/" + encodeURIComponent(user.id)).then((payload) => payload.user)
      : Promise.resolve({});
    Promise.all([load, api("/models").then((payload) => payload.models || [])])
      .then(([detail, models]) => {
        const choices = models.map((model) => ({ id: model.id, name: model.name || "" }));
        const allowed = detail.allowed_models || [];
        allowed.forEach((id) => {
          if (id === "*" || choices.some((choice) => choice.id === id)) return;
          choices.push({ id, name: "" });
        });
        openModal(
          user ? "Edit User" : "New User",
          "Users sign in with this username and password, then create their own API keys.",
          [
            {
              name: "id",
              label: "User ID",
              value: detail.id,
              placeholder: "team-a",
              required: true,
              disabled: Boolean(user),
              help: user ? "The user ID cannot be changed; create a new account instead." : undefined,
            },
            { name: "name", label: "Display Name", value: detail.name, placeholder: "Team A" },
            {
              name: "password",
              label: user ? "New Password" : "Password",
              type: "password",
              placeholder: user ? "Leave blank to keep the current password" : "At least 8 characters",
              autocomplete: "new-password",
              wide: true,
              help: user
                ? "Leave blank to keep the current password. Changing it signs out that user's other sessions."
                : "At least 8 characters. Share it with the user over a secure channel.",
            },
            {
              name: "allowed_models",
              label: "Allowed Models",
              section: "Model Access",
              sectionHint: "Pick the models this account can call. Members see the resulting list on their Models page.",
              type: "models",
              value: allowed,
              models: choices,
              wide: true,
              hideLabel: true,
              help: "Select all stores the wildcard and also allows models added later. Otherwise only the checked models are allowed; no checks means no model access.",
            },
          ],
          async (values) => {
            const password = values.password;
            if (!user && password.length < 8) {
              throw new Error("The password must be at least 8 characters.");
            }
            if (password && password.length < 8) {
              throw new Error("The new password must be at least 8 characters.");
            }
            const body = {
              id: values.id.trim(),
              name: values.name.trim(),
              allowed_models: values.allowed_models,
            };
            if (password) body.password = password;
            const path = user ? "/users/" + encodeURIComponent(user.id) : "/users";
            await api(path, { method: user ? "PUT" : "POST", body });
            await loadCurrent(true);
          }
        );
      })
      .catch((error) => showToast(error.message));
  }

  function deleteUser(user) {
    confirmDelete("user", user.id, async () => {
      await api("/users/" + encodeURIComponent(user.id), { method: "DELETE" });
      await loadCurrent(true);
    });
  }

  // --------------------------------------------------------------- settings

  function settingsView(data, modelsPayload) {
    const settings = data.settings || {};
    const choices = (modelsPayload.models || []).map((model) => ({
      id: String(model.id),
      name: String(model.name || ""),
    }));
    const access = modelAccessEditor({ value: settings.default_allowed_models || [], models: choices });

    const toggle = el("input", { type: "checkbox", class: "checkbox" });
    toggle.id = "settings-self-registration";
    toggle.checked = Boolean(settings.self_registration);
    const error = el("p", { class: "form-error", role: "alert", text: "" });
    const submit = el("button", { class: "btn primary", type: "submit", text: "Save Settings" });

    const form = el("form", { class: "form-grid" }, [
      el("label", { class: "form-field toggle-field wide", for: toggle.id }, [
        toggle,
        el("span", { class: "toggle-copy" }, [
          el("span", { class: "toggle-title", text: "Allow Self-Registration" }),
          el("span", {
            class: "field-help",
            text: "Visitors can create their own member account from the sign-in screen. The first administrator is always created through registration.",
          }),
        ]),
      ]),
      el("div", { class: "form-field wide" }, [
        el("span", { class: "field-label", text: "Default Model Access" }),
        access,
        el("span", {
          class: "field-help",
          text: "Model permissions assigned to every account created through self-registration. Used when the switch above is enabled.",
        }),
      ]),
      el("div", { class: "form-field wide" }, [submit]),
      el("div", { class: "form-field wide" }, [error]),
    ]);

    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      error.textContent = "";
      submit.disabled = true;
      submit.textContent = "Saving…";
      try {
        await api("/settings", {
          method: "PUT",
          body: {
            self_registration: toggle.checked,
            default_allowed_models: access.__read(),
          },
        });
        showToast("Registration settings saved.");
      } catch (failure) {
        error.textContent = failure.message;
      } finally {
        submit.disabled = false;
        submit.textContent = "Save Settings";
      }
    });

    return el("div", { class: "stack" }, [
      el("section", { class: "card" }, [
        cardHeader("Registration", "Control who can create a console account and what those accounts can use."),
        form,
      ]),
    ]);
  }

  // ------------------------------------------------------------ API keys

  function matchesKey(key, term) {
    return matchesQuery([key.id, key.name, key.key, key.created_at], term);
  }

  function keyRow(key) {
    const copy = el(
      "button",
      {
        class: "copy-btn",
        type: "button",
        title: "Copy API key",
        "aria-label": "Copy API key",
        on: { click: () => copyValue(key.key, copy, "Copy API key") },
      },
      [icon("copy")]
    );
    return el("article", { class: "list-row key-row" }, [
      el("div", { class: "list-identity" }, [
        el("div", { class: "list-title" }, [
          el("h3", { class: "model-name", text: key.name || "API Key" }),
        ]),
        el("span", { class: "list-sub mono", text: key.id }),
      ]),
      el("div", { class: "key-value" }, [
        el("code", { class: "mono key-secret", title: key.key, text: key.key }),
        copy,
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Created" }),
        el("span", { class: "list-value", text: formatDate(key.created_at) }),
      ]),
      el("div", { class: "list-field list-actions" }, [
        el("span", { class: "list-label", text: "Actions" }),
        el("div", { class: "row-actions" }, [
          el("button", {
            class: "btn ghost small danger",
            type: "button",
            text: "Delete",
            on: { click: () => deleteKey(key) },
          }),
        ]),
      ]),
    ]);
  }

  function apiEndpointCard() {
    const root = window.location.origin + "/api";
    const rows = [
      { label: endpointLabel("chat_completions"), value: root + "/v1/chat/completions" },
      { label: endpointLabel("responses"), value: root + "/v1/responses" },
      { label: endpointLabel("messages"), value: root + "/v1/messages" },
    ];
    return el("section", { class: "card" }, [
      cardHeader("API Endpoints", "Authenticate with one of the keys below."),
      el(
        "div",
        { class: "list-urls" },
        rows.map((row) => {
          const copy = el(
            "button",
            {
              class: "copy-btn",
              type: "button",
              title: "Copy " + row.label,
              "aria-label": "Copy " + row.label,
            },
            [icon("copy")]
          );
          copy.addEventListener("click", () => copyValue(row.value, copy, "Copy " + row.label));
          return el("span", { class: "url-endpoint" }, [
            el("span", { class: "url-protocol", text: row.label }),
            el("code", { class: "mono url-full", title: row.value, text: row.value }),
            copy,
          ]);
        })
      ),
    ]);
  }

  function keysView(data) {
    return el("div", { class: "stack" }, [
      apiEndpointCard(),
      searchableList({
        items: data.keys || [],
        filterKey: "keyFilter",
        placeholder: "Search API keys",
        noun: "API Key",
        match: matchesKey,
        renderRow: keyRow,
        rowClass: "key-row",
        columns: ["Name", "Key", "Created", "Actions"],
        emptyTitle: "No API Keys Yet",
        emptyText: "Create a key and send it as Authorization: Bearer <key> or x-api-key.",
        searchEmptyTitle: "No API Keys Match Your Search",
        action: addButton("New Key", () => createKey()),
      }),
    ]);
  }

  function createKey() {
    openModal(
      "New API Key",
      "Name the key so you can recognize which client uses it.",
      [{ name: "name", label: "Key Name", placeholder: "Laptop", required: true }],
      async (values) => {
        await api("/keys", { method: "POST", body: { name: values.name.trim() } });
        await loadCurrent(true);
        showToast("API key created.");
      }
    );
  }

  function deleteKey(key) {
    confirmDelete("API key", key.name || "API key", async () => {
      await api("/keys/" + encodeURIComponent(key.id), { method: "DELETE" });
      await loadCurrent(true);
    });
  }

  // -------------------------------------------------------------- requests

  function requestsView(data) {
    const records = data.records || [];
    const total = Number(data.total || 0);

    const search = el("input", {
      class: "input search",
      type: "search",
      placeholder: "Search requests",
      value: state.requestFilter,
      "aria-label": "Search requests",
    });
    const status = el(
      "select",
      {
        class: "select",
        "aria-label": "Filter by status",
        on: {
          change: () => {
            state.requestStatus = status.value;
            render();
          },
        },
      },
      [
        el("option", { value: "all", text: "All Statuses", selected: state.requestStatus === "all" }),
        el("option", { value: "success", text: "Successful", selected: state.requestStatus === "success" }),
        el("option", { value: "error", text: "Failed", selected: state.requestStatus === "error" }),
      ]
    );
    const note = el("span", { class: "toolbar-note", "aria-live": "polite" });
    const host = el("div");

    function render() {
      const term = state.requestFilter.trim().toLowerCase();
      const filtered = records.filter((record) => {
        if (state.requestStatus === "success" && Number(record.status) >= 400) return false;
        if (state.requestStatus === "error" && Number(record.status) < 400) return false;
        return matchesQuery(
          [
            record.request_id,
            record.model,
            record.model_name,
            record.provider,
            record.provider_name,
            record.upstream_model,
            record.status,
            record.inbound,
          ],
          term
        );
      });
      note.textContent =
        filtered.length === total
          ? formatNumber(filtered.length) + " " + plural(filtered.length, "request")
          : "Showing " + formatNumber(filtered.length) + " of " + formatNumber(total) + " requests";
      host.replaceChildren(
        requestsTable(filtered, {
          compact: false,
          showProvider: isAdmin(),
          emptyTitle: records.length === 0 ? "No Requests Yet" : "No Requests Match Your Filters",
          emptyText:
            records.length === 0
              ? "Requests made through the gateway will appear here."
              : "Try a different search term or status filter.",
        })
      );
    }

    let pending = 0;
    search.addEventListener("input", () => {
      state.requestFilter = search.value;
      if (state.refs && state.refs.setGlobalSearch) state.refs.setGlobalSearch(search.value, "Search requests");
      window.clearTimeout(pending);
      pending = window.setTimeout(render, 120);
    });
    render();

    if (state.refs) {
      state.refs.setGlobalSearch(state.requestFilter, "Search requests");
      state.refs.applyFilter = (term) => {
        state.requestFilter = term;
        search.value = term;
        render();
      };
    }

    return el("div", { class: "stack" }, [
      el("section", { class: "card" }, [
        el("div", { class: "toolbar card-toolbar" }, [
          el("div", { class: "search-wrap" }, [icon("search"), search]),
          status,
          note,
        ]),
        host,
      ]),
      el("p", {
        class: "footnote",
        text:
          "Only the latest " +
          formatNumber(REQUEST_WINDOW) +
          (isAdmin() ? " gateway requests" : " requests for your account") +
          " are shown. History resets when the gateway restarts.",
      }),
    ]);
  }

  // --------------------------------------------------------------- account

  function accountView() {
    const user = state.session.user || {};
    const gateway = state.session.gateway || {};

    return el("div", { class: "stack" }, [
      el("section", { class: "card account-card" }, [
        el("div", { class: "account-head" }, [
          el("span", { class: "avatar xlarge", text: initials(entityLabel(user)) }),
          el("div", {}, [
            el("h2", { text: entityLabel(user) }),
            el("p", { class: "muted", text: roleLabel(user) }),
          ]),
          el("button", { class: "btn ghost", type: "button", on: { click: logout } }, [
            icon("logout"),
            el("span", { text: "Sign Out" }),
          ]),
        ]),
        el("div", { class: "meta-grid" }, [
          metaCell("Session Expires", formatDate(state.session.expires_at)),
          metaCell("Gateway Version", gateway.version ? "v" + gateway.version : "—"),
        ]),
      ]),
      passwordCard(),
    ]);
  }

  function passwordCard() {
    const current = el("input", { class: "input", type: "password", autocomplete: "current-password" });
    const next = el("input", { class: "input", type: "password", autocomplete: "new-password" });
    const confirm = el("input", { class: "input", type: "password", autocomplete: "new-password" });
    const error = el("p", { class: "form-error", role: "alert", text: "" });
    const submit = el("button", { class: "btn primary", type: "submit", text: "Update Password" });
    const form = el("form", { class: "form-grid" }, [
      el("div", { class: "form-field" }, [el("label", { class: "field-label", text: "Current Password" }), current]),
      el("div", { class: "form-field" }, [el("label", { class: "field-label", text: "New Password" }), next]),
      el("div", { class: "form-field" }, [el("label", { class: "field-label", text: "Confirm New Password" }), confirm]),
      el("div", { class: "form-field wide" }, [submit]),
      el("div", { class: "form-field wide" }, [error]),
    ]);
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      error.textContent = "";
      if (next.value.length < 8) {
        error.textContent = "The new password must be at least 8 characters.";
        return;
      }
      if (next.value !== confirm.value) {
        error.textContent = "The new passwords don't match.";
        return;
      }
      submit.disabled = true;
      try {
        await api("/password", {
          method: "POST",
          body: { current_password: current.value, new_password: next.value },
        });
        current.value = "";
        next.value = "";
        confirm.value = "";
        showToast("Password updated.");
      } catch (failure) {
        error.textContent = failure.message;
      } finally {
        submit.disabled = false;
      }
    });
    return el("section", { class: "card" }, [
      cardHeader("Password", "Changing the password signs out every other session for this account."),
      form,
    ]);
  }

  document.addEventListener("DOMContentLoaded", () => {
    window.addEventListener("hashchange", onRouteChange);
    boot();
  });
})();