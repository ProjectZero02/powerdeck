"use strict";

const key = document.querySelector('meta[name="pd-key"]').content;
const $ = (id) => document.getElementById(id);
const $$ = (sel) => [...document.querySelectorAll(sel)];
const GUIDE = "https://github.com/ProjectZero02/powerdeck#ai-agents-mcp";
let state = null;
let qrLoaded = false;
let qrCode = "";
let step = 0; // 0: not in setup; 1–3: the setup step shown
let revealed = false; // the step-3 checks have played their animation
let started = false;

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "X-PowerDeck-Key": key, "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  if (!res.ok) throw new Error(await res.text());
  return res.status === 200 ? res.json() : null;
}

const setting = (body) => api("/api/settings", { method: "POST", body: JSON.stringify(body) });

// Links open in the default browser: through the PowerDeck window when there is one.
function openLink(url) {
  if (window.chrome && window.chrome.webview) window.chrome.webview.postMessage(JSON.stringify({ open: url }));
  else window.open(url, "_blank", "noopener");
}
function link(a, url) {
  a.href = url;
  a.onclick = (e) => { e.preventDefault(); openLink(url); };
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

const scopeText = { status: "status", wake: "turn on", power: "shut down and restart", force: "override", admin: "manage" };
const mark = { ok: "✓", info: "i", warn: "!", error: "✕" };

function checkItem(c, fixable) {
  const li = el("li", c.level ? "lvl-" + c.level : "");
  li.append(el("span", "icon", mark[c.level] || ""));
  const body = el("div", "body");
  body.append(el("div", "", c.title));
  if (c.detail) body.append(el("div", "detail", c.detail));
  li.append(body);
  if (fixable && c.fixable) {
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
  return li;
}

const order = { error: 0, warn: 1, info: 2, ok: 3 };
let showPassed = false;

function renderChecks(checks) {
  const ul = $("checks");
  const sorted = [...checks].sort((a, b) => order[a.level] - order[b.level]);
  const passed = sorted.filter((c) => c.level === "ok");
  const shown = showPassed ? sorted : sorted.filter((c) => c.level !== "ok");
  ul.replaceChildren(...shown.map((c) => checkItem(c, true)));
  if (!shown.length) ul.append(checkItem({ level: "ok", title: "All set" }, false));
  const more = $("checks-more");
  more.hidden = !passed.length || passed.length === sorted.length && !showPassed;
  more.textContent = showPassed ? "Hide passed" : `${passed.length} passed · Show`;
}

// Step 3: the checks arrive one by one, each "checking" for a moment first.
function renderSetupChecks(checks) {
  const ul = $("setup-checks");
  const sorted = [...checks].sort((a, b) => order[a.level] - order[b.level]);
  if (revealed) {
    ul.replaceChildren(...sorted.map((c) => checkItem(c, true)));
    return;
  }
  revealed = true;
  ul.replaceChildren();
  sorted.forEach((c, i) => {
    setTimeout(() => {
      const li = checkItem({ level: "", title: c.title }, false);
      li.classList.add("pending", "reveal");
      ul.append(li);
      setTimeout(() => li.replaceWith(checkItem(c, true)), 450);
    }, i * 260);
  });
}

function renderOwners(owners, legacy) {
  $("owners-legacy").hidden = !legacy;
  const ul = $("owners");
  ul.replaceChildren();
  for (const o of owners) {
    const li = el("li");
    const info = el("div");
    info.append(el("div", "name", o.name + " · owner device"));
    info.append(el("div", "sub", `full access · added ${ago(o.addedAt)}`));
    const btn = el("button", "danger small", "Remove");
    btn.onclick = async () => {
      if (!confirm(`Remove ${o.name}? It can't control this PC until it scans the code again.`)) return;
      await api(`/api/owners/${encodeURIComponent(o.nodeId)}/remove`, { method: "POST" });
      refresh();
    };
    li.append(info, btn);
    ul.append(li);
  }
}

function renderKeys(keys) {
  const ul = $("keys");
  ul.replaceChildren();
  for (const k of keys) {
    const li = el("li");
    const info = el("div");
    info.append(el("div", "name", k.name + ({ automation: " · agent", peer: " · PowerDeck device" }[k.kind] || " · phone")));
    const can = k.kind === "phone" ? "power and wake, no settings" : (k.scopes || []).map((s) => scopeText[s] || s).join(", ");
    const where = k.devices && k.devices.length ? " · " + k.devices.join(", ") : "";
    info.append(el("div", "sub", `${can}${where} · last used ${ago(k.lastUsed)}`));
    const btn = el("button", "danger small", "Remove");
    btn.onclick = async () => {
      if (!confirm(`Remove ${k.name}? It stops working immediately.`)) return;
      await api(`/api/keys/${encodeURIComponent(k.id)}/remove`, { method: "POST" });
      refresh();
    };
    li.append(info, btn);
    ul.append(li);
  }
}

function renderEvents(events) {
  const ul = $("events");
  ul.replaceChildren();
  if (!events.length) ul.append(el("li", "hint", "Nothing yet."));
  for (const e of events.slice(0, 6)) {
    const li = el("li");
    li.append(el("span", "", `${e.who}: ${e.action}`), el("span", "when", ago(e.at)));
    ul.append(li);
  }
}

function renderUpdate(u, settings, at) {
  if (!u) return;
  const paused = settings.updatesPausedUntil && Date.parse(settings.updatesPausedUntil) > Date.now();
  const day = (t) => new Date(t).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
  $("auto-switch").checked = !!settings.autoUpdate;
  let sub = settings.autoUpdate ? "New releases install 3 days after they're published" : "Off: install updates yourself";
  if (paused) sub = `Paused until ${day(settings.updatesPausedUntil)}`;
  else if (at) sub = `PowerDeck ${u.latest} installs on its own ${day(at)}`;
  $("auto-sub").textContent = sub;
  $("pause").textContent = paused ? "Resume" : "Pause for 7 days";
  $("pause").hidden = !settings.autoUpdate;
  let text = `PowerDeck ${u.current}`;
  if (u.installing) text = `Installing PowerDeck ${u.latest}… this window reconnects in a moment.`;
  else if (u.available) text = `PowerDeck ${u.latest} is available.`;
  else if (u.latest) text = `PowerDeck ${u.current} is up to date.`;
  if (u.error) text += ` Last check failed: ${u.error}`;
  $("update-text").textContent = text;
  $("install").hidden = !u.available || u.installing;
}

function showStep(n) {
  step = n;
  $("setup").hidden = n === 0;
  $("home").hidden = n !== 0;
  $$(".step").forEach((s) => s.classList.toggle("now", +s.dataset.step === n));
  $$(".progress li").forEach((li) => {
    li.classList.toggle("on", +li.dataset.step <= n);
    li.classList.toggle("now", +li.dataset.step === n);
  });
  $("next").textContent = n === 3 ? "Done" : "Next";
  if (n === 3 && state) renderSetupChecks(state.checks || []);
  window.scrollTo(0, 0);
}

function finishSetup() {
  setting({ setupDone: true }).catch(() => {});
  if (state) state.settings.setupDone = true;
  showStep(0);
  refresh();
}

async function refresh() {
  let s;
  try {
    s = await api("/api/state");
  } catch {
    $("running").className = "status bad";
    $("running").lastChild.textContent = "Not responding";
    return;
  }
  state = s;
  $("running").className = "status";
  $("running").lastChild.textContent = `Running · ${s.version}`;
  $("pcname").textContent = s.name;
  document.title = `PowerDeck · ${s.name}`;
  const settings = s.settings || {};
  if (!started) {
    started = true;
    link($("app-link"), s.appUrl);
    link($("app-link-home"), s.appUrl);
    showStep(settings.setupDone ? 0 : 1);
  }

  $("link-error").hidden = !s.linkError;
  $("link-error").textContent = s.linkError || "";
  for (const id of ["qr-add", "qr-add-home"]) $(id).hidden = !!s.linkError;
  if (!qrLoaded && !s.linkError) {
    qrLoaded = true;
    $("qr-app").src = `/qr/app.svg?k=${encodeURIComponent(key)}`;
  }
  // The add code carries the pairing code, so it changes when the code is used or expires.
  if (s.pairCode !== qrCode && !s.linkError) {
    qrCode = s.pairCode;
    $("qr-add").src = $("qr-add-home").src = `/qr/add.svg?k=${encodeURIComponent(key)}&c=${encodeURIComponent(s.pairCode)}`;
  }
  const mins = Math.max(0, Math.round((Date.parse(s.pairExpires) - Date.now()) / 60000));
  $$(".pair-code").forEach((e) => { e.textContent = s.pairCode; });
  $$(".pair-expiry").forEach((e) => { e.textContent = `(once, ${mins} min left)`; });

  $("f-ip").textContent = s.tailnetIP && s.tailnetIP !== "invalid IP" ? s.tailnetIP : "Not connected";
  const n = s.network || {};
  $("f-nic").replaceChildren(n.mac ? `${n.interface}${n.wired ? "" : " (Wi-Fi)"}` : "Not found");
  if (n.mac) $("f-nic").append(el("small", "", n.mac));
  const peers = s.peers || [];
  $("f-peers").textContent = peers.length ? peers.join(", ") : "None yet";

  const checks = s.checks || [];
  renderChecks(checks);
  if (step === 3) renderSetupChecks(checks);
  renderOwners(s.owners || [], !!s.ownersLegacy);
  renderKeys(s.keys || []);
  renderEvents(s.events || []);
  renderUpdate(s.update, settings, s.updateAt);

  const agentsOn = !settings.noAgents;
  $("agents-switch").checked = agentsOn;
  const agents = (s.keys || []).filter((k) => k.kind === "automation").length;
  $("agents-sub").textContent = !agentsOn ? "Agents can see whether it's on, but can't turn it on or off"
    : agents ? "Agents can turn it on and off within their limits" : "No agent connected yet";
  $$(".tray-option").forEach((e) => { e.hidden = s.os !== "windows"; });
  $$(".tray-switch").forEach((e) => { if (document.activeElement !== e) e.checked = !!settings.tray; });
}

function openAgent() {
  if (!state) return;
  $("agent-form").hidden = false;
  $("agent-result").hidden = true;
  $("agent-create").hidden = false;
  $("agent-copy").hidden = true;
  $("agent-cancel").textContent = "Cancel";
  const box = $("agent-devices");
  box.querySelectorAll("label").forEach((l) => l.remove());
  for (const name of [state.name, ...(state.peers || [])]) {
    const l = el("label");
    const cb = el("input");
    cb.type = "checkbox";
    cb.value = name;
    cb.className = "device";
    cb.checked = name === state.name;
    l.append(cb, document.createTextNode(" " + name));
    box.append(l);
  }
  $("agent").showModal();
  $("agent-name").focus();
}

async function createAgent() {
  const name = $("agent-name").value.trim();
  if (!name) { $("agent-name").focus(); return; }
  const scopes = ["status", ...$$(".scope:checked").map((c) => c.value)];
  const devices = $$(".device:checked").map((c) => c.value);
  try {
    const r = await api("/api/keys", { method: "POST", body: JSON.stringify({ name, scopes, devices }) });
    const cfg = { mcpServers: { powerdeck: { type: "http", url: state.mcpUrl, headers: { Authorization: `Bearer ${r.key}` } } } };
    $("agent-config").textContent = JSON.stringify(cfg, null, 2);
    $("agent-form").hidden = true;
    $("agent-result").hidden = false;
    $("agent-create").hidden = true;
    $("agent-copy").hidden = false;
    $("agent-cancel").textContent = "Done";
    refresh();
  } catch (e) {
    alert("Couldn't create the key:\n" + e.message);
  }
}

// The top bar gets a hairline once the page scrolls under it.
addEventListener("scroll", () => document.querySelector(".bar").classList.toggle("lifted", scrollY > 4), { passive: true });

$("next").onclick = () => (step === 3 ? finishSetup() : showStep(step + 1));
$("skip").onclick = () => (step === 3 ? finishSetup() : showStep(step + 1));
$("rerun").onclick = () => { revealed = false; showStep(1); };
$("renew").onclick = async () => { await api("/api/pairing/renew", { method: "POST" }); refresh(); };
$("checks-more").onclick = () => { showPassed = !showPassed; if (state) renderChecks(state.checks || []); };
$$(".agent-open").forEach((b) => { b.onclick = openAgent; });
$("agent-cancel").onclick = () => $("agent").close();
$("agent-create").onclick = (e) => { e.target.disabled = true; createAgent().finally(() => { e.target.disabled = false; }); };
$("agent-copy").onclick = () => { navigator.clipboard.writeText($("agent-config").textContent); $("agent-copy").textContent = "Copied"; };
$("agents-switch").onchange = (e) => setting({ agents: e.target.checked }).then(refresh);
$$(".tray-switch").forEach((t) => { t.onchange = (e) => setting({ tray: e.target.checked }).then(refresh); });
$("check").onclick = async (e) => {
  e.target.disabled = true;
  try { await api("/api/update/check", { method: "POST" }); } finally { e.target.disabled = false; refresh(); }
};
$("auto-switch").onchange = (e) => setting({ autoUpdate: e.target.checked }).then(refresh);
$("pause").onclick = () => {
  const paused = state && state.settings && Date.parse(state.settings.updatesPausedUntil || 0) > Date.now();
  setting({ pauseUpdates: !paused }).then(refresh);
};
$("install").onclick = async () => { await api("/api/update/install", { method: "POST" }); refresh(); };
link($("agent-guide"), GUIDE);

refresh();
setInterval(refresh, 5000);
