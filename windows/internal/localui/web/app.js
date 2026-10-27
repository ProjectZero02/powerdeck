"use strict";

const key = document.querySelector('meta[name="pd-key"]').content;
const $ = (id) => document.getElementById(id);
let lastPairLink = null;

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

function renderDevices(devices) {
  const ul = $("devices");
  ul.replaceChildren();
  $("no-devices").hidden = devices.length > 0;
  for (const d of devices) {
    const li = el("li");
    const info = el("div");
    info.append(el("div", "name", d.name));
    info.append(el("div", "muted", `${d.model ? d.model + " · " : ""}paired ${ago(d.pairedAt)} · last used ${ago(d.lastSeen)}`));
    const btn = el("button", "danger", "Remove");
    btn.onclick = async () => {
      if (!confirm(`Remove ${d.name}? It will need to pair again.`)) return;
      await api(`/api/devices/${encodeURIComponent(d.id)}/remove`, { method: "POST" });
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
    const li = el("li", c.ok ? "ok" : "bad");
    li.append(el("span", "icon", c.ok ? "✓" : "!"));
    const body = el("div");
    body.append(el("div", "", c.name));
    if (c.detail) body.append(el("div", "detail", c.detail));
    li.append(body);
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

function fact(id, found) {
  $(id).textContent = found ? `${found.name} (${found.address})` : "not found";
}

async function refresh() {
  let s;
  try {
    s = await api("/api/state");
    $("running").className = "pill ok";
    $("running").textContent = `Running · v${s.version}`;
  } catch {
    $("running").className = "pill bad";
    $("running").textContent = "Not responding";
    return;
  }
  $("pcname").textContent = s.name;
  $("app-link").href = s.appUrl;
  $("qr-app").src = `/qr/app.svg?k=${encodeURIComponent(key)}`;
  $("pair-error").hidden = !s.pairError;
  $("pair-error").textContent = s.pairError || "";
  $("qr-pair").hidden = !!s.pairError;
  if (s.pairLink && s.pairLink !== lastPairLink) {
    lastPairLink = s.pairLink;
    $("qr-pair").src = `/qr/pair.svg?k=${encodeURIComponent(key)}&t=${Date.now()}`;
  }
  const mins = Math.max(0, Math.round((Date.parse(s.pairExpires) - Date.now()) / 60000));
  $("pair-expiry").textContent = `This code works once and expires in ${mins} min.`;
  renderDevices(s.devices);
  $("f-ip").textContent = s.tailnetIP && s.tailnetIP !== "invalid IP" ? s.tailnetIP : "not connected";
  $("f-mac").textContent = s.mac || "not found";
  fact("f-kvm", s.discovery.nanokvm);
  fact("f-pve", s.discovery.proxmox);
  renderChecks(s.checks);
  renderUpdate(s.update, s.settings);
}

$("renew").onclick = async () => { await api("/api/pairing/renew", { method: "POST" }); refresh(); };
$("rediscover").onclick = async (e) => {
  e.target.disabled = true;
  await api("/api/rediscover", { method: "POST" });
  setTimeout(() => { e.target.disabled = false; refresh(); }, 4000);
};
$("check").onclick = async (e) => {
  e.target.disabled = true;
  try { await api("/api/update/check", { method: "POST" }); } finally { e.target.disabled = false; refresh(); }
};
$("install").onclick = async () => { await api("/api/update/install", { method: "POST" }); refresh(); };
$("autoupdate").onchange = (e) => api("/api/settings", { method: "POST", body: JSON.stringify({ autoUpdate: e.target.checked }) });

refresh();
setInterval(refresh, 4000);
