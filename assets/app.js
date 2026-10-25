/* LLM Proxy console. No build step, no external dependencies. */
(function () {
  "use strict";

  const API = "/api/ui";
  const REQUEST_WINDOW = 500;

  const ICONS = {
    overview:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7.5" height="7.5" rx="1.6"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.6"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.6"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.6"/></svg>',
    models:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 3 7.5l9 4.5 9-4.5L12 3Z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/></svg>',
    providers:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/></svg>',
    requests:
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h4l2.4-7 4.2 14L16 12h5"/></svg>',
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
  };

  const VIEW_META = {
    overview: {
      title: "Overview",
      subtitle: "Usage and activity at a glance",
    },
    models: {
      title: "Models",
      subtitle: "Models available to your account",
    },
    providers: {
      title: "Providers",
      subtitle: "Upstream services behind your models",
    },
    requests: {
      title: "Requests",
      subtitle: "Request history for your account",
    },
    account: {
      title: "Account",
      subtitle: "Profile and access details",
    },
  };

  const VIEWS = Object.keys(VIEW_META);

  const VIEW_LOADERS = {
    overview: () => api("/overview").then(overviewView),
    models: () => api("/models").then(modelsView),
    providers: () => api("/providers").then(providersView),
    requests: () => api("/requests?limit=" + REQUEST_WINDOW).then(requestsView),
    account: () => Promise.resolve(accountView()),
  };

  const state = {
    session: null,
    view: "overview",
    refs: null,
    modelFilter: "",
    providerFilter: "",
    requestFilter: "",
    requestStatus: "all",
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

  function currentView() {
    const hash = window.location.hash.replace(/^#\/?/, "").split("?")[0];
    return VIEWS.indexOf(hash) >= 0 ? hash : "overview";
  }

  function initials(id) {
    const text = String(id || "?").trim();
    if (!text) return "?";
    const parts = text.split(/[\s._/-]+/).filter(Boolean);
    if (parts.length > 1) return (parts[0][0] + parts[1][0]).toUpperCase();
    return text.slice(0, 2).toUpperCase();
  }

  function plural(count, singular, many) {
    return Number(count) === 1 ? singular : many || singular + "s";
  }

  function countLabel(visible, total, noun) {
    const word = plural(total, noun);
    if (visible === total) return formatNumber(total) + " " + word;
    return formatNumber(visible) + " of " + formatNumber(total) + " " + word;
  }

  function matchesQuery(values, term) {
    if (!term) return true;
    return values
      .filter((value) => value !== undefined && value !== null)
      .join(" ")
      .toLowerCase()
      .indexOf(term) >= 0;
  }

  function titleCase(value) {
    return String(value || "")
      .split(/[\s_-]+/)
      .filter(Boolean)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
      .join(" ");
  }

  function protocolLabel(value) {
    switch (value) {
      case "openai":
        return "OpenAI";
      case "responses":
        return "Responses";
      case "anthropic":
        return "Anthropic";
      case "":
      case undefined:
      case null:
        return "—";
      default:
        return titleCase(value);
    }
  }

  function entityLabel(entity) {
    return (entity && entity.name) || (entity && entity.id) || "unknown";
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

  function formatNumber(value) {
    return Number(value || 0).toLocaleString();
  }

  // scaled renders a number in the first matching unit and trims trailing
  // zeros, so callers only have to describe their unit table and precision.
  function scaled(value, units, digitsFor) {
    const number = Number(value || 0);
    if (!Number.isFinite(number) || number <= 0) return "0";
    for (let index = 0; index < units.length; index += 1) {
      const size = units[index][0];
      if (number >= size) {
        const scaledValue = number / size;
        const digits = digitsFor(scaledValue);
        const text = digits > 0 ? scaledValue.toFixed(digits).replace(/\.?0+$/, "") : scaledValue.toFixed(0);
        return text + units[index][1];
      }
    }
    return String(Math.round(number));
  }

  function formatSize(value) {
    return scaled(
      value,
      [
        [1024 * 1024 * 1024, "G"],
        [1024 * 1024, "M"],
        [1024, "K"],
      ],
      (amount) => (amount >= 100 ? 0 : amount >= 10 ? 1 : 2)
    );
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

  async function copyModelID(value, button) {
    const copied = await copyText(value);
    if (!copied) return;
    button.classList.add("copied");
    button.title = "Copied";
    button.setAttribute("aria-label", "Copied");
    button.replaceChildren(icon("check"));
    setTimeout(() => {
      button.classList.remove("copied");
      button.title = "Copy model ID";
      button.setAttribute("aria-label", "Copy model ID");
      button.replaceChildren(icon("copy"));
    }, 1400);
  }

  function compact(value) {
    return scaled(
      value,
      [
        [1e9, "B"],
        [1e6, "M"],
        [1e3, "K"],
      ],
      (amount) => (amount >= 100 ? 0 : 1)
    );
  }

  function formatDuration(ms) {
    const value = Number(ms || 0);
    if (value < 1000) return Math.max(0, Math.round(value)) + " ms";
    if (value < 60000) return (value / 1000).toFixed(2) + " s";
    return (value / 60000).toFixed(1) + " min";
  }

  

  function formatDate(iso) {
    if (!iso) return "—";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "—";
    return date.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  function formatClock(iso) {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "";
    return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }

  function timeAgo(iso) {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "—";
    const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
    if (seconds < 10) return "just now";
    if (seconds < 60) return seconds + "s ago";
    if (seconds < 3600) return Math.floor(seconds / 60) + "m ago";
    if (seconds < 86400) return Math.floor(seconds / 3600) + "h ago";
    return Math.floor(seconds / 86400) + "d ago";
  }

  function formatUptime(seconds) {
    const total = Math.max(0, Number(seconds || 0));
    if (total < 60) return Math.round(total) + "s";
    if (total < 3600) return Math.floor(total / 60) + "m " + Math.round(total % 60) + "s";
    if (total < 86400) return Math.floor(total / 3600) + "h " + Math.floor((total % 3600) / 60) + "m";
    return Math.floor(total / 86400) + "d " + Math.floor((total % 86400) / 3600) + "h";
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
      el("strong", { text: "Couldn't load this page" }),
      el("p", { text: message }),
      el("button", { class: "btn ghost", type: "button", text: "Try again", on: { click: retry } }),
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

  // ------------------------------------------------------------------ boot

  function boot() {
    api("/session")
      .then((session) => {
        state.session = session;
        startApp();
      })
      .catch((error) => {
        if (error.status === 403) {
          showNotice("Console unavailable", error.message, false);
        } else if (error.status === 401) {
          showLogin("");
        } else {
          showNotice(
            "Unable to connect",
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
    const input = el("input", {
      class: "input login-key",
      type: "password",
      placeholder: "Enter your API key",
      autocomplete: "current-password",
      spellcheck: "false",
      required: "required",
      "aria-label": "API key",
    });
    const submit = el("button", { class: "btn primary wide", type: "submit", text: "Sign in" });
    const error = el("p", { class: "form-error", role: "alert", text: message || "" });
    const form = el("form", { class: "login-form", on: { submit: onSubmit } }, [
      el("label", { class: "field-label", for: "api-key", text: "API key" }),
      input,
      submit,
      error,
    ]);
    input.id = "api-key";

    showAuthCard([
      form,
      el("div", { class: "login-foot" }, [
        icon("lock"),
        el("span", {
          text: "Your API key is exchanged for a secure session and is never stored in this browser.",
        }),
      ]),
    ]);
    input.focus();

    async function onSubmit(event) {
      event.preventDefault();
      const key = input.value.trim();
      error.textContent = "";
      if (!key) {
        error.textContent = "Enter your API key to continue.";
        input.focus();
        return;
      }
      submit.disabled = true;
      submit.textContent = "Signing in…";
      try {
        const session = await api("/login", { method: "POST", body: { api_key: key } });
        state.session = session;
        startApp();
      } catch (failure) {
        error.textContent = failure.message;
        input.select();
      } finally {
        submit.disabled = false;
        submit.textContent = "Sign in";
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
            text: "Try again",
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

    const user = state.session.user || {};
    const userChip = el("a", { class: "user-chip", href: "#/account" }, [
      el("span", { class: "avatar", text: initials(entityLabel(user)) }),
      el("span", { class: "user-meta" }, [
        el("span", { class: "user-name", text: entityLabel(user) }),
        el("span", { class: "user-role", text: user.unrestricted ? "Full access" : "Limited access" }),
      ]),
    ]);

    const main = el("main", { class: "main" }, [
      el("header", { class: "topbar" }, [
        el("div", { class: "page-head" }, [pageTitle, pageSub]),
        el("div", { class: "topbar-actions" }, [refreshButton, userChip]),
      ]),
      content,
    ]);

    app.append(el("div", { class: "layout" }, [buildSidebar(), main]));
    state.refs = { content, pageTitle, pageSub, refreshButton };
    loadCurrent(false);
  }

  function buildSidebar() {
    const user = state.session.user || {};
    const nav = el(
      "nav",
      { class: "nav", "aria-label": "Console sections" },
      VIEWS.map((view) =>
        el("a", { class: "nav-link", href: "#/" + view, dataset: { view } }, [
          icon(view),
          el("span", { text: VIEW_META[view].title }),
        ])
      )
    );

    return el("aside", { class: "sidebar" }, [
      el("a", { class: "brand", href: "#/overview" }, [
        el("span", { class: "brand-mark", html: brandMark() }),
        el("span", { class: "brand-copy" }, [
          el("strong", { text: "LLM Proxy" }),
          el("small", { text: "console" }),
        ]),
      ]),
      nav,
      el("div", { class: "sidebar-spacer" }),
      el("div", { class: "sidebar-footer" }, [
        el("div", { class: "account-mini" }, [
          el("span", { class: "avatar", text: initials(entityLabel(user)) }),
          el("span", { class: "user-meta" }, [
            el("span", { class: "user-name", text: entityLabel(user) }),
            el("span", { class: "user-role", text: "API key" }),
          ]),
        ]),
        el(
          "button",
          { class: "btn ghost small", type: "button", on: { click: logout } },
          [icon("logout"), el("span", { text: "Sign out" })]
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

  async function loadCurrent(manual) {
    if (!state.refs) return;
    const view = currentView();
    state.view = view;
    const meta = VIEW_META[view];
    state.refs.pageTitle.textContent = meta.title;
    state.refs.pageSub.textContent = meta.subtitle;
    updateActiveNav();

    if (manual) state.refs.refreshButton.classList.add("spinning");
    state.refs.content.replaceChildren(loadingState());

    try {
      const node = await VIEW_LOADERS[view]();
      if (currentView() !== view) return;
      state.refs.content.replaceChildren(node);
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        state.session = null;
        showLogin(error.message);
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
      ? "Within the last " + formatNumber(history.capacity) + " gateway requests"
      : "Since gateway start";

    const hero = el("section", { class: "card hero" }, [
      el("div", { class: "hero-main" }, [
        el("div", { class: "hero-status" }, [el("span", { class: "status-dot" }), el("span", { text: "Online" })]),
        el("h2", { text: "LLM Proxy" }),
        el("p", { class: "hero-version", text: gateway.version ? "v" + gateway.version : "" }),
      ]),
      el("div", { class: "hero-meta" }, [
        metaCell("Uptime", formatUptime(gateway.uptime_seconds)),
        metaCell("Models", formatNumber(counts.models)),
        metaCell("Providers", formatNumber(counts.providers)),
        metaCell("Started", formatDate(gateway.started_at)),
      ]),
    ]);

    const metrics = el("section", { class: "metrics" }, [
      metricCard("Requests", formatNumber(requests), requestsDetail),
      metricCard(
        "Success rate",
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
      metricCard("Avg latency", requests ? formatDuration(summary.avg_duration_ms) : "—", "Per request"),
    ]);

    const activity = el("section", { class: "card" }, [
      cardHeader("Request volume", "Requests per hour over the last 24 hours"),
      activityChart(data.series || []),
    ]);

    const modelTraffic = el("section", { class: "grid two" }, [
      breakdownCard("Top models", data.by_model || [], "Model activity will appear here.", "Model"),
      breakdownCard("Top providers", data.by_provider || [], "Provider activity will appear here.", "Provider"),
    ]);

    const recent = el("section", { class: "card" }, [
      el("div", { class: "card-head" }, [
        el("div", {}, [
          el("h2", { text: "Recent requests" }),
          el("p", { class: "card-sub", text: "Latest requests for your account" }),
        ]),
        el("a", { class: "link", href: "#/requests", text: "View all" }),
      ]),
      requestsTable(data.recent || [], { compact: true }),
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
        "No requests in the last 24 hours",
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
                el("th", { text: "Avg latency" }),
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
      : emptyState("No activity yet", emptyText);

    return el("section", { class: "card" }, [cardHeader(title, ""), body]);
  }

  // ---------------------------------------------------------------- tables

  function requestsTable(records, options) {
    const compactMode = options && options.compact;
    if (!records.length) {
      return emptyState(
        (options && options.emptyTitle) || "No requests yet",
        (options && options.emptyText) || "Requests made with your API key will appear here."
      );
    }
    const columns = compactMode
      ? ["Time", "Model", "Provider", "Status", "Duration", "Tokens"]
      : ["Time", "Request ID", "Model", "Provider", "Protocol", "Status", "Duration", "Tokens"];

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
      cells.push(
        el("td", {}, [
          el("span", {
            class: "mono ellipsis",
            title: "Provider: " + (record.provider || "—") + (record.upstream_model ? " · Upstream: " + record.upstream_model : ""),
            text: record.provider_name || record.provider || "—",
          }),
        ])
      );
      if (!compactMode) {
        cells.push(
          el("td", {}, [
            el("span", { class: "row-chips" }, [
              chip(protocolLabel(record.inbound)),
              record.stream
                ? el("span", { class: "stream-chip", title: "Streaming response", html: ICONS.stream })
                : null,
              record.upstream && record.upstream !== record.inbound
                ? chip("→ " + protocolLabel(record.upstream), "subtle")
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
            : emptyState("No " + options.noun + "s match your search", "Try a different search term.")
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
      render();
    });
    render();

    return el("div", { class: "stack" }, [
      el("div", { class: "toolbar" }, [
        el("div", { class: "search-wrap" }, [icon("search"), search]),
        note,
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
      columns: ["Model", "Modalities", "Context", "Max output", "Providers"],
      emptyTitle: "No models available",
      emptyText: "Your account doesn't have access to any models.",
    });
  }

  function matchesModel(model, term) {
    const parts = [model.id, model.name, ...(model.modalities || []), ...(model.output_modalities || [])];
    (model.providers || []).forEach((provider) => {
      parts.push(provider.id, provider.name, provider.upstream_model, ...(provider.protocols || []));
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
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Context" }),
        el("span", { class: "list-value", text: model.context_length ? formatSize(model.context_length) : "—" }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Max output" }),
        el("span", { class: "list-value", text: model.max_output_length ? formatSize(model.max_output_length) : "—" }),
      ]),
      el("div", { class: "list-field list-providers" }, [
        el("span", { class: "list-label", text: "Providers" }),
        el("span", {
          class: "list-value list-ellipsis",
          title: providerNames || "—",
          text: providerNames || "—",
        }),
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
      columns: ["Provider", "Base URL", "Models", "Retries"],
      emptyTitle: "No providers available",
      emptyText: "No upstream services are connected to your models.",
    });
  }

  function matchesProvider(provider, term) {
    const baseURLs = Object.values(provider.base_urls || {});
    const endpoints = Object.values(provider.endpoints || {}).flatMap((byOperation) => Object.values(byOperation || {}));
    return matchesQuery(
      [
        provider.id,
        provider.name,
        ...(provider.protocols || []),
        ...baseURLs,
        ...endpoints,
        ...(provider.models || []),
        ...Object.values(provider.model_names || {}),
      ],
      term
    );
  }

  // baseURLField lists each protocol's base URL on its own line, in full, with
  // the protocol label kept in front so endpoints remain identifiable.
  function baseURLField(provider) {
    const baseURLs = Object.entries(provider.base_urls || {}).filter(([, value]) => String(value || "").trim());
    if (!baseURLs.length) return el("span", { class: "list-value", text: "—" });
    return el(
      "span",
      { class: "list-urls" },
      baseURLs.map(([protocol, baseURL]) =>
        el("span", { class: "url-endpoint" }, [
          el("span", { class: "url-protocol", text: protocolLabel(protocol) }),
          el("code", { class: "mono url-full", title: baseURL, text: baseURL }),
        ])
      )
    );
  }

  function providerRow(provider) {
    const modelNames = (provider.models || []).map((model) => (provider.model_names || {})[model] || model);
    return el("article", { class: "list-row provider-row" }, [
      el("div", { class: "list-identity" }, [
        el("div", { class: "list-title" }, [
          el("h3", { class: "provider-name", title: provider.id, text: entityLabel(provider) }),
        ]),
        el("div", { class: "list-sub-row" }, [
          el("span", { class: "list-sub mono", title: provider.id, text: provider.id }),
          ...(provider.protocols || []).map((protocol) => chip(protocolLabel(protocol))),
        ]),
      ]),
      el("div", { class: "list-field list-base-url" }, [
        el("span", { class: "list-label", text: "Base URL" }),
        baseURLField(provider),
      ]),
      el("div", { class: "list-field list-models" }, [
        el("span", { class: "list-label", text: "Models" }),
        el("span", {
          class: "list-value list-ellipsis",
          title: modelNames.join(", ") || "—",
          text: modelNames.join(", ") || "—",
        }),
      ]),
      el("div", { class: "list-field" }, [
        el("span", { class: "list-label", text: "Retries" }),
        el("span", { class: "list-value", text: String(Number(provider.max_retries) || 0) }),
      ]),
    ]);
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
        el("option", { value: "all", text: "All statuses", selected: state.requestStatus === "all" }),
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
          emptyTitle: records.length === 0 ? "No requests yet" : "No requests match your filters",
          emptyText:
            records.length === 0
              ? "Requests made with your API key will appear here."
              : "Try a different search term or status filter.",
        })
      );
    }

    let pending = 0;
    search.addEventListener("input", () => {
      state.requestFilter = search.value;
      window.clearTimeout(pending);
      pending = window.setTimeout(render, 120);
    });
    render();

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
          " requests are shown. History resets when the gateway restarts.",
      }),
    ]);
  }

  // --------------------------------------------------------------- account

  function accountView() {
    const user = state.session.user || {};
    const gateway = state.session.gateway || {};
    const allowed = user.allowed_models || [];
    const modelNames = user.allowed_model_names || {};

    const accessChips = user.unrestricted
      ? [chip("All models", "good")]
      : allowed.length
      ? allowed.map((model) => chip(modelNames[model] || model, "mono"))
      : [chip("No models assigned")];

    return el("div", { class: "stack" }, [
      el("section", { class: "card account-card" }, [
        el("div", { class: "account-head" }, [
          el("span", { class: "avatar xlarge", text: initials(entityLabel(user)) }),
          el("div", {}, [
            el("h2", { text: entityLabel(user) }),
            el("p", { class: "muted", text: "Signed in with an API key" }),
          ]),
          el("button", { class: "btn ghost", type: "button", on: { click: logout } }, [
            icon("logout"),
            el("span", { text: "Sign out" }),
          ]),
        ]),
        el("div", { class: "meta-grid" }, [
          metaCell(
            "Model access",
            user.unrestricted
              ? "All models"
              : allowed.length
              ? allowed.length + " " + plural(allowed.length, "model")
              : "None"
          ),
          metaCell("Session expires", formatDate(state.session.expires_at)),
          metaCell("Gateway version", gateway.version ? "v" + gateway.version : "—"),
        ]),
        el("div", { class: "section-block" }, [
          el("h3", { text: "Allowed models" }),
          el("div", { class: "row-chips" }, accessChips),
        ]),
      ]),
    ]);
  }

  document.addEventListener("DOMContentLoaded", () => {
    window.addEventListener("hashchange", onRouteChange);
    boot();
  });
})();