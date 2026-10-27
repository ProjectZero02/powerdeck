"use strict";

const key = document.querySelector('meta[name="pd-key"]').content;
const $ = (id) => document.getElementById(id);
let state = null;
let addLoaded = false;

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "X-PowerDeck-Key": key, "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  if (!res.ok && res.status !== 204 && res.status !== 202) throw new Error(await res.text());
  return res.status === 200 ? res.json() : null;
}

function ago(iso) {
  if (!iso || iso.startsWith("0001")) return "never";
  const s = Math.round((Date.now() - Date.parse(iso)) / 1000);
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const scopeText = { status: "status", wake: "turn on", power: "shut down / restart", force: "override", admin: "manage" };

function renderKeys(keys) {
  const ul = $("keys");
  ul.replaceChildren();
  $("no-keys").hidden = keys.length > 0;
  for (const k of keys) {
    const li = el("li");
    const info = el("div");
    info.append(el("div", "name", k.name + (k.kind === "automation" ? " · agent" : " · phone")));
    const can = k.kind === "automation" ? k.scopes.map((s) => scopeText[s] || s).join(", ") : "full access";
    const where = k.devices && k.devices.length ? " · " + k.devices.join(", ") : "";
    info.append(el("div", "muted", `${can}${where} · created ${ago(k.createdAt)} · last used ${ago(k.lastUsed)}`));
    const btn = el("button", "danger", "Remove");
    btn.onclick = async () => {
      if (!confirm(`Remove ${k.name}? It will stop working immediately.`)) return;
      await api(`/api/keys/${encodeURIComponent(k.id)}/remove`, { method: "POST" });
      refresh();
    };
    li.append(info, btn);
    ul.append(li);
  }
}

function renderChecks(checks) {
  const ul = $("checks");
  ul.replaceChildren();
  for (const c of checks) {
    const li = el("li", c.level);
    li.append(el("span", "icon", { ok: "✓", info: "i", warn: "!", error: "✕" }[c.level] || "•"));
    const body = el("div", "grow");
    body.append(el("div", "", c.title));
    if (c.detail) body.append(el("div", "detail", c.detail));
    li.append(body);
    if (c.fixable) {
      const b = el("button", "secondary small", "Fix");
      b.onclick = async () => {
        b.disabled = true;
        b.textContent = "Fixing…";
        try { await api(`/api/checks/${encodeURIComponent(c.id)}/fix`, { method: "POST" }); }
        catch (e) { alert("Couldn't fix it automatically:\n" + e.message); }
        refresh();
      };
      li.append(b);
    }
    ul.append(li);
  }
}

function renderEvents(events) {
  const ul = $("events");
  ul.replaceChildren();
  if (!events.length) ul.append(el("li", "muted", "Nothing yet."));
  for (const e of events.slice(0, 8)) {
    const li = el("li");
    li.append(el("span", "", `${e.who}: ${e.action}`), el("span", "muted", ` · ${e.result} · ${ago(e.at)}`));
    ul.append(li);
  }
}

function renderUpdate(u, settings) {
  $("autoupdate").checked = settings.autoUpdate;
  let text = `You have PowerDeck ${u.current}.`;
  if (u.installing) text = `Installing PowerDeck ${u.latest}… this window will reconnect in a moment.`;
  else if (u.available) text = `PowerDeck ${u.latest} is available.`;
  else if (u.latest) text += " It's up to date.";
  if (u.error) text += ` (Last check failed: ${u.error})`;
  $("update-text").textContent = text;
  $("install").hidden = !u.available || u.installing;
}

async function refresh() {
  try {
    state = await api("/api/state");
    $("running").className = "pill ok";
    $("running").textContent = `Running · v${state.version}`;
  } catch {
    $("running").className = "pill bad";
    $("running").textContent = "Not responding";
    return;
  }
  const s = state;
  $("pcname").textContent = s.name;
  $("app-link").href = s.appUrl;
  $("qr-app").src = `/qr/app.svg?k=${encodeURIComponent(key)}`;
  $("link-error").hidden = !s.linkError;
  $("link-error").textContent = s.linkError || "";
  $("qr-add").hidden = !!s.linkError;
  if (!s.linkError && !addLoaded) {
    addLoaded = true;
    $("qr-add").src = `/qr/add.svg?k=${encodeURIComponent(key)}`;
  }
  $("pair-code").textContent = s.pairCode;
  const mins = Math.max(0, Math.round((Date.parse(s.pairExpires) - Date.now()) / 60000));
  $("pair-expiry").textContent = `(works once, ${mins} min left)`;
  renderChecks(s.checks);
  renderKeys(s.keys);
  renderEvents(s.events);
  $("f-ip").textContent = s.tailnetIP && s.tailnetIP !== "invalid IP" ? s.tailnetIP : "not connected";
  const n = s.network || {};
  $("f-nic").textContent = n.mac ? `${n.interface} · ${n.mac}${n.wired ? "" : " (Wi-Fi)"}` : "not found";
  $("f-peers").textContent = s.peers.length ? s.peers.join(", ") : "none found yet";
  renderUpdate(s.update, s.settings);
}

function openAgent() {
  $("agent").hidden = false;
  $("agent-result").hidden = true;
  const box = $("agent-devices");
  box.querySelectorAll("label").forEach((l) => l.remove());
  for (const name of [state.name, ...state.peers]) {
    const l = el("label");
    const cb = el("input");
    cb.type = "checkbox";
    cb.value = name;
    cb.className = "device";
    cb.checked = name === state.name;
    l.append(cb, document.createTextNode(" " + name));
    box.append(l);
  }
  $("agent-name").focus();
}

async function createAgent() {
  const name = $("agent-name").value.trim();
  if (!name) { $("agent-name").focus(); return; }
  const scopes = ["status", ...[...document.querySelectorAll(".scope:checked")].map((c) => c.value)];
  const devices = [...document.querySelectorAll(".device:checked")].map((c) => c.value);
  const r = await api("/api/keys", { method: "POST", body: JSON.stringify({ name, scopes, devices }) });
  const cfg = {
    mcpServers: {
      powerdeck: { type: "http", url: state.mcpUrl, headers: { Authorization: `Bearer ${r.key}` } },
    },
  };
  $("agent-config").textContent = JSON.stringify(cfg, null, 2);
  $("agent-result").hidden = false;
  refresh();
}

$("renew").onclick = async () => { await api("/api/pairing/renew", { method: "POST" }); refresh(); };
$("agent-open").onclick = openAgent;
$("agent-cancel").onclick = () => { $("agent").hidden = true; };
$("agent-create").onclick = (e) => { e.target.disabled = true; createAgent().finally(() => { e.target.disabled = false; }); };
$("agent-copy").onclick = () => navigator.clipboard.writeText($("agent-config").textContent);
$("check").onclick = async (e) => {
  e.target.disabled = true;
  try { await api("/api/update/check", { method: "POST" }); } finally { e.target.disabled = false; refresh(); }
};
$("install").onclick = async () => { await api("/api/update/install", { method: "POST" }); refresh(); };
$("autoupdate").onchange = (e) => api("/api/settings", { method: "POST", body: JSON.stringify({ autoUpdate: e.target.checked }) });

refresh();
setInterval(refresh, 5000);
