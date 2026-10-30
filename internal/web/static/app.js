import { Terminal } from "./vendor/xterm.js";
import { FitAddon } from "./vendor/addon-fit.js";
import { t, applyI18n, setLanguage } from "./i18n.js";

const $ = (id) => document.getElementById(id);
const Theme = window.DockerboxTheme; // theme.js, loaded synchronously in <head>
const encoder = new TextEncoder();

const state = {
  agents: [],
  selected: null,
  info: null,
  consoles: new Map(), // agent name -> Console
  busy: new Map(), // agent name -> action in flight
  loaded: false, // agent list fetched at least once since login
  deleteTarget: null,
  saveSeq: 0,
  popout: null, // agent name when opened as stand-alone console window
  timers: [],
};

// ---------------------------------------------------------------- API

class AuthError extends Error {}

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (method !== "GET") opts.headers["X-Dockerbox-CSRF"] = "1";
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts); // relative paths: works behind a sub-path reverse proxy
  if (res.status === 401 && path !== "api/login") {
    showLogin();
    throw new AuthError(t("sessionExpired"));
  }
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

const agentPath = (name, suffix = "") => `api/agents/${encodeURIComponent(name)}${suffix}`;

// ---------------------------------------------------------------- UI helpers

let toastTimer;
function toast(msg, isError = false) {
  const el = $("toast");
  el.textContent = msg;
  el.classList.toggle("error", isError);
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 4000);
}

function reportError(err) {
  if (!(err instanceof AuthError)) toast(err.message, true);
}

function savePref(key, value) {
  try { localStorage.setItem(`dockerbox.${key}`, value); } catch { /* storage unavailable */ }
}
function loadPref(key) {
  try { return localStorage.getItem(`dockerbox.${key}`); } catch { return null; }
}

function formatMB(bytes) {
  return `${Math.round(bytes / 1048576)} MB`;
}

const current = () => state.agents.find((a) => a.name === state.selected) ?? null;
const isRunning = (a) => a?.state === "running";

// ---------------------------------------------------------------- Console

// copyText writes to the clipboard; falls back to execCommand on plain HTTP,
// where navigator.clipboard is not available.
function copyText(text) {
  if (!text) return;
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).catch(() => {});
    return;
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.append(ta);
  ta.select();
  try { document.execCommand("copy"); } catch { /* not supported */ }
  ta.remove();
}

class Console {
  constructor(name) {
    this.name = name;
    this.closedReason = null;
    this.closedAt = 0;
    this.el = document.createElement("div");
    this.el.className = "term";
    $("terminals").append(this.el);
    this.term = new Terminal({
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily: 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace',
      fontSize: Theme.current.fontSize,
      lineHeight: 1.15,
      scrollback: 5000,
      // Transparent so the console glass and wallpaper shine through.
      allowTransparency: true,
      theme: {
        background: "rgba(0,0,0,0)",
        foreground: "#e8e8ed",
        cursor: "#ffffff",
        selectionBackground: "rgba(10,132,255,0.35)",
        black: "#1c1c1e", red: "#ff453a", green: "#30d158", yellow: "#ffd60a",
        blue: "#0a84ff", magenta: "#bf5af2", cyan: "#64d2ff", white: "#e5e5ea",
        brightBlack: "#8e8e93", brightRed: "#ff6961", brightGreen: "#4ee07a", brightYellow: "#ffe066",
        brightBlue: "#409cff", brightMagenta: "#da8fff", brightCyan: "#8fe3ff", brightWhite: "#ffffff",
      },
    });
    this.fit = new FitAddon();
    this.term.loadAddon(this.fit);
    this.term.open(this.el);
    this.term.onData((d) => this.send(encoder.encode(d)));
    // Clipboard like in a desktop terminal:
    //  - Ctrl+V / Ctrl+Shift+V / Cmd+V: let the browser fire its paste event,
    //    which xterm.js forwards (works over plain HTTP, unlike the async
    //    clipboard API). Without this, xterm sends ^V to the box.
    //  - Ctrl+C with a selection, Ctrl+Shift+C, Cmd+C: copy the selection;
    //    Ctrl+C without a selection still interrupts the running program.
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      const mod = e.ctrlKey || e.metaKey;
      const key = e.key.toLowerCase();
      if (mod && !e.altKey && key === "v") return false;
      if (mod && !e.altKey && key === "c" && (e.shiftKey || e.metaKey || this.term.hasSelection())) {
        copyText(this.term.getSelection());
        e.preventDefault();
        return false;
      }
      return true;
    });
    this.term.onBinary((d) => this.send(Uint8Array.from(d, (c) => c.charCodeAt(0))));
    this.term.onResize(({ rows, cols }) => this.sendControl({ type: "resize", rows, cols }));
    this.resizeObserver = new ResizeObserver(() => this.refit());
    this.resizeObserver.observe(this.el);
    this.connect();
  }

  refit() {
    if (this.el.offsetParent === null) return; // hidden
    try { this.fit.fit(); } catch { /* not yet laid out */ }
  }

  connect() {
    this.closedReason = null;
    this.refit();
    const url = new URL(agentPath(this.name, "/terminal"), location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("rows", this.term.rows);
    url.searchParams.set("cols", this.term.cols);
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
      this.term.reset();
      this.sendControl({ type: "resize", rows: this.term.rows, cols: this.term.cols });
      if (this.name === state.selected) this.term.focus();
      render();
    };
    ws.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) this.term.write(new Uint8Array(e.data));
    };
    ws.onclose = (e) => {
      if (this.ws !== ws || this.disposed) return;
      this.closedReason = e.reason || "disconnected"; // translated when rendered
      this.closedAt = Date.now();
      render();
    };
    this.ws = ws;
  }

  get connected() {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  send(bytes) {
    if (this.connected) this.ws.send(bytes);
  }

  sendControl(msg) {
    if (this.connected) this.ws.send(JSON.stringify(msg));
  }

  // show toggles visibility. Focus moves to the console only when it becomes
  // visible (selection change), never on the periodic refresh.
  show(visible) {
    const wasVisible = !this.el.hidden;
    this.el.hidden = !visible;
    if (visible && !wasVisible) {
      requestAnimationFrame(() => {
        this.refit();
        this.term.focus();
      });
    }
  }

  dispose() {
    this.disposed = true;
    this.resizeObserver.disconnect();
    this.ws?.close();
    this.term.dispose();
    this.el.remove();
  }
}

function syncConsoles() {
  for (const [name, c] of state.consoles) {
    const agent = state.agents.find((a) => a.name === name);
    if (!isRunning(agent)) {
      c.dispose();
      state.consoles.delete(name);
    } else if (c.closedReason && Date.now() - c.closedAt > 3000 && !state.busy.has(name)) {
      c.connect(); // automatic reconnect, e.g. after a restart
    }
  }
  const a = current();
  if (isRunning(a) && !state.busy.has(a.name) && !state.consoles.has(a.name)) {
    state.consoles.set(a.name, new Console(a.name));
  }
  for (const [name, c] of state.consoles) c.show(name === state.selected);
}

// ---------------------------------------------------------------- Rendering

function render() {
  renderList();
  renderMain();
}

function dotClass(agent) {
  return state.busy.has(agent.name) ? "busy" : agent.state;
}

function renderList() {
  const list = $("agent-list");
  list.replaceChildren(
    ...state.agents.map((a) => {
      const li = document.createElement("li");
      const btn = document.createElement("button");
      btn.type = "button";
      btn.setAttribute("aria-current", String(a.name === state.selected));
      const dot = document.createElement("span");
      dot.className = `dot ${dotClass(a)}`;
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = a.name;
      const status = document.createElement("span");
      status.className = "sr-only"; // state is not conveyed by colour alone
      status.textContent = `, ${isRunning(a) ? t("running") : t("stateStopped")}`;
      const meta = document.createElement("span");
      meta.className = "meta";
      meta.textContent = a.memoryMB >= 1024 ? `${+(a.memoryMB / 1024).toFixed(1)} GB` : `${a.memoryMB} MB`;
      btn.append(dot, name, status, meta);
      btn.addEventListener("click", () => select(a.name));
      li.append(btn);
      return li;
    }),
  );
}

// reasonText translates the close reasons the server sends.
function reasonText(reason) {
  const known = { disconnected: "disconnected", "session ended": "sessionEnded", "session expired": "sessionExpired" };
  return known[reason] ? t(known[reason]) : reason;
}

const busyText = { start: "starting", stop: "stopping", restart: "restarting", recreate: "recreating" };

function renderMain() {
  const a = current();
  const busy = a && state.busy.has(a.name);
  $("empty").hidden = !state.loaded || state.agents.length > 0;
  $("current-name").textContent = a?.name ?? "";
  $("current-dot").className = `dot ${a ? dotClass(a) : "none"}`;
  $("settings-btn").hidden = !a;
  if (!isRunning(a) || busy) $("usage").textContent = "";

  const off = !!a && (!isRunning(a) || busy);
  $("powered-off").hidden = !off;
  $("power").disabled = busy || isRunning(a);
  $("power-status").textContent = busy ? t(busyText[state.busy.get(a.name)] ?? "starting") : a ? `${t("stopped")} · ${a.status}` : "";
  // While a box is off or busy the (transparent) console must not show through.
  $("terminals").hidden = off;

  const c = a && state.consoles.get(a.name);
  const overlay = !!c && !busy && isRunning(a) && (c.closedReason || !c.connected);
  $("term-overlay").hidden = !overlay;
  $("term-overlay-text").textContent = c?.closedReason ? reasonText(c.closedReason) : t("connecting");
  $("reconnect").hidden = !c?.closedReason;

  // The toolbar power button toggles between start and shut down.
  const power = $("tb-power");
  power.dataset.action = isRunning(a) ? "stop" : "start";
  power.title = t(power.dataset.action);
  power.setAttribute("aria-label", power.title);
  power.classList.toggle("on", isRunning(a));
  $("tb-restart").disabled = !isRunning(a) || !!busy;
  power.disabled = !a || !!busy;

  for (const btn of $("menu").querySelectorAll("[data-action]")) {
    const act = btn.dataset.action;
    const needsAgent = !["logout", "settings"].includes(act);
    let enabled = !needsAgent || (!!a && !busy);
    if (act === "start") enabled &&= !isRunning(a);
    if (["stop", "restart", "popout"].includes(act)) enabled &&= isRunning(a);
    btn.disabled = !enabled;
  }
}

// ---------------------------------------------------------------- Data

async function refresh() {
  try {
    state.agents = await api("GET", "api/agents");
    state.loaded = true;
  } catch (err) {
    reportError(err);
    return;
  }
  if (!current()) state.selected = state.popout ?? state.agents[0]?.name ?? null;
  syncConsoles();
  render();
}

async function refreshUsage() {
  const a = current();
  if (!isRunning(a) || document.hidden) {
    $("usage").textContent = "";
    return;
  }
  try {
    const u = await api("GET", agentPath(a.name, "/usage"));
    if (!isRunning(current()) || current().name !== a.name || !u.memoryLimit) return;
    $("usage").textContent = `CPU ${u.cpuPercent.toFixed(1)} % · RAM ${formatMB(u.memoryBytes)} / ${formatMB(u.memoryLimit)}`;
  } catch {
    $("usage").textContent = "";
  }
}

function select(name) {
  state.selected = name;
  savePref("selected", name);
  if (matchMedia("(max-width: 720px)").matches) setCollapsed(true);
  syncConsoles();
  render();
  refreshUsage();
}

async function runAction(name, action) {
  state.busy.set(name, action);
  render();
  try {
    await api("POST", agentPath(name, `/${action}`));
  } catch (err) {
    reportError(err);
  } finally {
    state.busy.delete(name);
    const c = state.consoles.get(name);
    if (c && action !== "stop") c.closedAt = 0; // reconnect right away
    await refresh();
  }
}

// ---------------------------------------------------------------- Dialogs & menu

function setCollapsed(collapsed) {
  $("app").classList.toggle("collapsed", collapsed);
  $("expand").hidden = !collapsed;
  savePref("collapsed", collapsed ? "1" : "0");
  for (const c of state.consoles.values()) requestAnimationFrame(() => c.refit());
}

function openCreate() {
  $("create-form").reset();
  $("create-error").textContent = "";
  $("create-memory").value = state.info?.defaultMemoryMB ?? 2048;
  $("create-memory").max = state.info?.maxMemoryMB ?? "";
  $("create-cpus").value = state.info?.defaultCPUs ?? 2;
  $("create-cpus").max = state.info?.maxCPUs ?? "";
  $("create-dialog").showModal();
}

async function submitCreate(e) {
  e.preventDefault();
  const submit = $("create-submit");
  const name = $("create-name").value.trim();
  submit.disabled = true;
  $("create-error").textContent = "";
  const slow = setTimeout(() => ($("create-error").textContent = t("creating")), 1500);
  try {
    await api("POST", "api/agents", {
      name,
      memoryMB: Number($("create-memory").value),
      cpus: Number($("create-cpus").value),
    });
    $("create-dialog").close();
    state.selected = name;
    savePref("selected", name);
    await refresh();
  } catch (err) {
    $("create-error").textContent = err.message;
  } finally {
    clearTimeout(slow);
    submit.disabled = false;
  }
}

function openDelete() {
  const a = current();
  if (!a) return;
  state.deleteTarget = a.name; // the agent shown in the dialog, not whatever is selected later
  $("delete-name").textContent = a.name;
  $("delete-keep").checked = true;
  $("delete-error").textContent = "";
  $("delete-dialog").showModal();
}

async function submitDelete(e) {
  e.preventDefault();
  const name = state.deleteTarget;
  if (!name) return;
  const keep = $("delete-keep").checked;
  try {
    await api("DELETE", agentPath(name, keep ? "" : "?keepData=false"));
    $("delete-dialog").close();
    state.deleteTarget = null;
    state.consoles.get(name)?.dispose();
    state.consoles.delete(name);
    state.selected = null;
    toast(t("deleted"));
    await refresh();
  } catch (err) {
    $("delete-error").textContent = err.message;
  }
}

function openDetails() {
  const a = current();
  if (!a) return;
  const rows = [
    [t("name"), a.name],
    [t("state"), `${a.state} (${a.status})`],
    [t("image"), a.image],
    [t("limits"), `${a.memoryMB} MB · ${a.cpus} CPU`],
    [t("created"), new Date(a.created).toLocaleString()],
    [t("runtime"), state.info?.runtime || "runc"],
    [t("version"), state.info?.version ?? ""],
  ];
  $("details-list").replaceChildren(
    ...rows.flatMap(([k, v]) => {
      const dt = document.createElement("dt");
      dt.textContent = k;
      const dd = document.createElement("dd");
      dd.textContent = v;
      return [dt, dd];
    }),
  );
  $("details-dialog").showModal();
}

function toggleMenu(open = $("menu").hidden) {
  $("menu").hidden = !open;
  $("menu-btn").setAttribute("aria-expanded", String(open));
  if (open) $("menu").querySelector("button:not(:disabled)")?.focus();
  else if ($("menu").contains(document.activeElement)) $("menu-btn").focus();
}

// Arrow keys move between enabled menu items (WAI-ARIA menu pattern).
function menuKeys(e) {
  const items = [...$("menu").querySelectorAll("button:not(:disabled)")];
  const i = items.indexOf(document.activeElement);
  const move = { ArrowDown: 1, ArrowUp: -1 }[e.key];
  if (move) {
    e.preventDefault();
    items[(i + move + items.length) % items.length]?.focus();
  } else if (e.key === "Home" || e.key === "End") {
    e.preventDefault();
    items[e.key === "Home" ? 0 : items.length - 1]?.focus();
  } else if (e.key === "Tab") {
    toggleMenu(false);
  }
}

async function onMenu(action) {
  toggleMenu(false);
  const a = current();
  switch (action) {
    case "stop":
      // Shutting down kills whatever the agent is doing.
      if (confirm(t("stopConfirm", { name: a.name }))) return runAction(a.name, "stop");
      return;
    case "start":
    case "restart":
      return runAction(a.name, action);
    case "recreate":
      if (confirm(t("recreateConfirm", { name: a.name }))) return runAction(a.name, "recreate");
      return;
    case "popout":
      return window.open(`#/console/${encodeURIComponent(a.name)}`, `dockerbox-${a.name}`, "noopener");
    case "delete":
      return openDelete();
    case "settings":
      return openSettings();
    case "logout":
      await api("POST", "api/logout").catch(() => {});
      return showLogin();
  }
}

// ---------------------------------------------------------------- Settings

let saveTimer;

// applyRuntime applies the parts of the settings that are not pure CSS.
function applyRuntime(s) {
  setLanguage(s.language);
  for (const c of state.consoles.values()) {
    if (c.term.options.fontSize !== s.fontSize) {
      c.term.options.fontSize = s.fontSize;
      c.refit();
    }
  }
  render();
}

async function loadSettings() {
  try {
    const s = Theme.apply(await api("GET", "api/settings"));
    Theme.cache(s);
    applyRuntime(s);
  } catch (err) {
    reportError(err);
  }
}

function buildSettingsUI() {
  const radio = (name, value, cls, inner) => {
    const label = document.createElement("label");
    label.className = cls;
    const input = document.createElement("input");
    input.type = "radio";
    input.name = name;
    input.value = value;
    label.append(input, ...inner);
    return label;
  };
  $("set-accents").replaceChildren(
    ...Theme.ACCENTS.map(([id, color]) => {
      const dot = document.createElement("span");
      const el = radio("accent", color, "swatch", [dot]);
      el.style.setProperty("--c", color);
      el.title = t(`color_${id}`);
      el.dataset.i18nTitle = `color_${id}`;
      return el;
    }),
    (() => {
      const dot = document.createElement("span");
      const picker = document.createElement("input");
      picker.type = "color";
      picker.id = "set-accent-custom";
      const el = radio("accent", "custom", "swatch custom", [dot, picker]);
      el.dataset.i18nTitle = "custom";
      return el;
    })(),
  );
  $("set-wallpapers").replaceChildren(
    ...[...Object.keys(Theme.WALLPAPERS), "custom"].map((id) => {
      const preview = document.createElement("span");
      preview.className = "preview";
      const label = document.createElement("span");
      label.dataset.i18n = id;
      label.textContent = t(id);
      if (id === "custom") {
        const ns = "http://www.w3.org/2000/svg";
        const svg = document.createElementNS(ns, "svg");
        svg.setAttribute("viewBox", "0 0 24 24");
        svg.setAttribute("aria-hidden", "true");
        const plus = document.createElementNS(ns, "path");
        plus.setAttribute("d", "M12 5v14M5 12h14");
        svg.append(plus);
        preview.append(svg);
      } else {
        Theme.WALLPAPERS[id].dark.forEach((c, i) => preview.style.setProperty(`--w${i + 1}`, c));
      }
      return radio("wallpaper", id, id === "custom" ? "tile custom" : "tile", [preview, label]);
    }),
  );
}

function fillSettings(s) {
  const form = $("settings-form");
  form.elements.theme.value = s.theme;
  form.elements.language.value = s.language;
  const preset = Theme.ACCENTS.some(([, c]) => c === s.accent);
  form.elements.accent.value = preset ? s.accent : "custom";
  $("set-accent-custom").value = s.accent;
  form.elements.wallpaper.value = s.wallpaper;
  $("set-wallcolors").hidden = s.wallpaper !== "custom";
  for (const input of $("set-wallcolors").querySelectorAll("input")) input.value = s.wallColors[input.dataset.wall];
  $("set-glass").value = s.glass;
  $("set-motion").checked = s.motion;
  $("set-font").value = s.fontSize;
  updateSliderLabels();
}

function updateSliderLabels() {
  for (const [id, suffix] of [["set-glass", " %"], ["set-font", " px"]]) {
    const el = $(id);
    $(`${id}-out`).textContent = el.value + suffix;
    el.style.setProperty("--fill", `${((el.value - el.min) / (el.max - el.min)) * 100}%`);
  }
}

function readSettings() {
  const form = $("settings-form");
  const accent = form.elements.accent.value;
  return {
    theme: form.elements.theme.value,
    accent: accent === "custom" ? $("set-accent-custom").value : accent,
    wallpaper: form.elements.wallpaper.value,
    wallColors: [...$("set-wallcolors").querySelectorAll("input")].map((i) => i.value),
    glass: Number($("set-glass").value),
    motion: $("set-motion").checked,
    fontSize: Number($("set-font").value),
    language: form.elements.language.value,
  };
}

function onSettingsChange(e) {
  if (e.target.id === "set-accent-custom") $("settings-form").elements.accent.value = "custom";
  const s = Theme.apply(readSettings());
  $("set-wallcolors").hidden = s.wallpaper !== "custom";
  updateSliderLabels();
  applyRuntime(s);
  scheduleSave(s);
}

let pendingSave = null;

function scheduleSave(s) {
  pendingSave = s;
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flushSave, 400);
}

// flushSave sends the latest pending settings. Responses of older saves are
// ignored so a slow request cannot overwrite a newer choice in the cache.
async function flushSave({ keepalive = false } = {}) {
  clearTimeout(saveTimer);
  const s = pendingSave;
  if (!s) return;
  pendingSave = null;
  Theme.cache(s); // the browser reflects the choice even if the request is cut off
  const seq = ++state.saveSeq;
  try {
    if (keepalive) {
      // Page is going away: fire and forget, but let the request outlive the page.
      fetch("api/settings", {
        method: "PUT", keepalive: true, credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-Dockerbox-CSRF": "1" },
        body: JSON.stringify(s),
      });
      return;
    }
    const saved = await api("PUT", "api/settings", s);
    if (seq === state.saveSeq) Theme.cache(saved);
    $("settings-error").textContent = "";
  } catch (err) {
    if (!(err instanceof AuthError)) $("settings-error").textContent = t("saveFailed", { msg: err.message });
  }
}

function openSettings() {
  fillSettings(Theme.current);
  $("settings-error").textContent = "";
  $("settings-dialog").showModal();
}

function resetSettings() {
  const s = Theme.apply(Theme.DEFAULTS);
  fillSettings(s);
  applyRuntime(s);
  scheduleSave(s);
}

// ---------------------------------------------------------------- Login & boot

function stopTimers() {
  state.timers.forEach(clearInterval);
  state.timers = [];
}

function showLogin() {
  stopTimers();
  clearTimeout(saveTimer);
  pendingSave = null;
  for (const d of document.querySelectorAll("dialog[open]")) d.close();
  toggleMenu(false);
  for (const c of state.consoles.values()) c.dispose();
  state.consoles.clear();
  state.loaded = false;
  $("app").hidden = true;
  $("login").hidden = false;
  $("login-password").value = "";
  $("login-password").focus();
}

async function submitLogin(e) {
  e.preventDefault();
  $("login-error").textContent = "";
  try {
    await api("POST", "api/login", { password: $("login-password").value });
    await startApp();
  } catch (err) {
    $("login-error").textContent = err.message === "wrong password" ? t("wrongPassword") : err.message;
  }
}

async function startApp() {
  $("login").hidden = true;
  $("app").hidden = false;
  state.info = await api("GET", "api/info").catch(() => null);
  await loadSettings();
  await refresh();
  if (!$("login").hidden) return; // session expired while starting up
  stopTimers();
  state.timers.push(setInterval(refresh, 4000), setInterval(refreshUsage, 3000));
  refreshUsage();
}

function bind() {
  $("login-form").addEventListener("submit", submitLogin);
  $("open-settings").addEventListener("click", openSettings);
  $("settings-form").addEventListener("input", onSettingsChange);
  $("settings-form").addEventListener("change", onSettingsChange);
  $("settings-reset").addEventListener("click", resetSettings);
  $("settings-dialog").addEventListener("close", () => flushSave());
  addEventListener("pagehide", () => flushSave({ keepalive: true }));
  $("menu").addEventListener("keydown", menuKeys);
  $("collapse").addEventListener("click", () => setCollapsed(true));
  $("expand").addEventListener("click", () => setCollapsed(false));
  $("add-agent").addEventListener("click", openCreate);
  $("empty-add").addEventListener("click", openCreate);
  $("create-form").addEventListener("submit", submitCreate);
  $("delete-form").addEventListener("submit", submitDelete);
  $("settings-btn").addEventListener("click", openDetails);
  $("power").addEventListener("click", () => current() && runAction(current().name, "start"));
  $("reconnect").addEventListener("click", () => state.consoles.get(state.selected)?.connect());
  $("menu-btn").addEventListener("click", (e) => {
    e.stopPropagation();
    toggleMenu();
  });
  $("menu").addEventListener("click", (e) => {
    const btn = e.target.closest("[data-action]");
    if (btn && !btn.disabled) onMenu(btn.dataset.action).catch(reportError);
  });
  for (const id of ["tb-power", "tb-restart"]) {
    $(id).addEventListener("click", (e) => {
      const btn = e.currentTarget;
      if (!btn.disabled) onMenu(btn.dataset.action).catch(reportError);
    });
  }
  document.addEventListener("click", (e) => {
    if (!$("menu").hidden && !e.target.closest(".menu-wrap")) toggleMenu(false);
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("menu").hidden) toggleMenu(false);
  });
  for (const btn of document.querySelectorAll("dialog [data-close]")) {
    btn.addEventListener("click", () => btn.closest("dialog").close());
  }
  document.addEventListener("visibilitychange", () => !document.hidden && refreshUsage());
}

async function boot() {
  setLanguage(Theme.current.language);
  buildSettingsUI();
  applyI18n();
  bind();
  const m = location.hash.match(/^#\/console\/([a-z0-9-]+)$/);
  if (m) {
    state.popout = m[1];
    state.selected = m[1];
    document.body.classList.add("popout");
    document.title = `${m[1]} · Dockerbox`;
  } else {
    state.selected = loadPref("selected");
    const narrow = matchMedia("(max-width: 720px)").matches;
    if (loadPref("collapsed") === "1" || (narrow && state.selected)) setCollapsed(true);
  }
  const session = await api("GET", "api/session").catch(() => ({ authenticated: false }));
  if (session.authenticated) await startApp();
  else showLogin();
}

boot();
