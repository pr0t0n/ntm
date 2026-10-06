"use strict";

const API = "/api/v1";
const PALETTE = {
  computer: "#4f9cf9", server: "#bc8cff", network: "#f0a04b",
  printer: "#f778ba", unknown: "#6e7b8c",
};
const OS_COLORS = ["#4f9cf9", "#bc8cff", "#f0a04b", "#3fb950", "#f778ba", "#56d4dd", "#6e7b8c"];
const charts = {};
let cy = null;

async function getJSON(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`${r.status} ${url}`);
  return r.json();
}

// ---- Navigation ----
function switchView(view) {
  document.querySelectorAll(".nav-item").forEach(b => b.classList.toggle("active", b.dataset.view === view));
  document.querySelectorAll(".view").forEach(v => v.classList.remove("active"));
  document.getElementById("view-" + view).classList.add("active");
  if (view === "dashboard") loadDashboard();
  if (view === "map") loadMap();
  if (view === "cmdb") search();
}

document.querySelectorAll(".nav-item").forEach(btn =>
  btn.addEventListener("click", () => switchView(btn.dataset.view)));

document.getElementById("refreshBtn").addEventListener("click", () => {
  const active = document.querySelector(".nav-item.active").dataset.view;
  switchView(active);
});

// ---- Dashboard ----
async function loadDashboard() {
  let d;
  try { d = await getJSON(`${API}/dashboard`); }
  catch (e) { console.error(e); return; }

  const t = d.by_asset_type || {};
  setText("kpiTotal", d.total_hosts);
  setText("kpiUp", `${d.hosts_up} up`);
  setText("kpiDown", `${d.hosts_down} down`);
  setText("kpiComputers", t.computer || 0);
  setText("kpiServers", t.server || 0);
  setText("kpiNetwork", (t.network || 0) + (t.printer || 0));
  setText("kpiPorts", d.open_ports || 0);
  setText("agentStatus", `${d.agents || 0} agente(s) ativo(s)`);

  // Charts are isolated so a rendering failure never blocks the table below.
  safe(() => drawDoughnut("chartAssets", t, k => PALETTE[k] || PALETTE.unknown, labelAsset));
  safe(() => drawDoughnut("chartOS", d.by_os || {}, (k, i) => OS_COLORS[i % OS_COLORS.length], k => k));
  safe(() => drawApps(d.top_apps || []));
  fillRecent(d.recent || []);
  loadAgents();
}

function safe(fn) {
  try { fn(); } catch (e) { console.error("render error:", e); }
}

// ---- On-demand scan runner ----
let agentsById = {};     // id -> agent snapshot from the last poll
let runnerTimer = null;  // 1s ticker that refreshes the "executing (mm:ss)" label
let agentsPoll = null;   // periodic server refresh while on the dashboard

async function loadAgents() {
  let data;
  try { data = await getJSON(`${API}/agents`); }
  catch (e) { console.error(e); return; }
  const sel = document.getElementById("agentSelect");
  const prev = sel.value;
  const agents = data.agents || [];
  agentsById = {};
  agents.forEach(a => { agentsById[a.id] = a; });

  if (agents.length === 0) {
    sel.innerHTML = `<option value="">Nenhum agente registrado</option>`;
  } else {
    sel.innerHTML = agents.map(a => {
      const dot = a.online ? "🟢" : "⚪";
      const run = a.scan_state === "running" ? " · ⏳ executando" : "";
      const last = a.last_scan_at && !a.last_scan_at.startsWith("0001")
        ? new Date(a.last_scan_at).toLocaleTimeString("pt-BR") : "nunca";
      return `<option value="${a.id}">${dot} #${a.id} ${esc(a.name)} — último scan: ${last}${run}</option>`;
    }).join("");
    sel.value = prev && agentsById[prev] ? prev : agents[0].id;
  }
  updateRunnerUI();
  ensurePolling();
}

// localPending tracks agents the user just asked to run, so the UI shows a
// pending state immediately (and across refresh the server's scan_requested
// flag keeps it) until the agent actually starts.
const localPending = {};

// updateRunnerUI reflects the selected agent's run state on the buttons/labels.
// It survives page refresh because it reads authoritative state from the server
// (scan_state = running, scan_requested = pending).
function updateRunnerUI() {
  const sel = document.getElementById("agentSelect");
  const runBtn = document.getElementById("runBtn");
  const stopBtn = document.getElementById("stopBtn");
  const msg = document.getElementById("runnerMsg");
  const a = agentsById[sel.value];
  if (!a) { runBtn.textContent = "▶ RODAR"; runBtn.disabled = false; stopBtn.classList.add("hidden"); return; }

  const running = a.scan_state === "running";
  const pending = !running && (a.scan_requested || localPending[a.id]);
  if (running) delete localPending[a.id]; // started — drop the optimistic flag

  stopBtn.classList.toggle("hidden", !(running || pending));
  runBtn.disabled = running || pending;

  if (running) {
    const started = a.scan_started_at && !a.scan_started_at.startsWith("0001")
      ? new Date(a.scan_started_at) : null;
    const elapsed = started ? fmtElapsed(Date.now() - started.getTime()) : "";
    const since = started ? started.toLocaleTimeString("pt-BR") : "—";
    runBtn.textContent = `⏳ Executando ${elapsed}`;
    setMsg(msg, `Iniciado às ${since}`, "ok");
  } else if (pending) {
    runBtn.textContent = "⏳ Aguardando início…";
    setMsg(msg, "Scan solicitado — o agente inicia no próximo ciclo.", "ok");
  } else {
    runBtn.textContent = "▶ RODAR";
    if (msg.textContent.startsWith("Iniciado") || msg.textContent.startsWith("Scan solicitado")) setMsg(msg, "", "");
  }
}

function fmtElapsed(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

// ensurePolling keeps the runner live: a 1s ticker updates the elapsed label and
// every 4s we refresh agent state from the server (only while on the dashboard).
function ensurePolling() {
  if (!runnerTimer) runnerTimer = setInterval(updateRunnerUI, 1000);
  if (!agentsPoll) agentsPoll = setInterval(() => {
    if (document.querySelector(".nav-item.active").dataset.view === "dashboard") loadAgents();
  }, 4000);
}

async function runAgent() {
  const sel = document.getElementById("agentSelect");
  const id = sel.value;
  const msg = document.getElementById("runnerMsg");
  if (!id) { setMsg(msg, "Selecione um agente.", "err"); return; }
  localPending[id] = true;         // optimistic: show pending at once
  updateRunnerUI();
  try {
    const r = await fetch(`${API}/agents/${id}/run`, { method: "POST" });
    if (!r.ok) throw new Error(await r.text());
    loadAgents();
    // Refresh inventory once the agent has had time to scan and report.
    setTimeout(() => {
      if (document.querySelector(".nav-item.active").dataset.view === "dashboard") loadDashboard();
    }, 20000);
  } catch (e) {
    console.error(e);
    delete localPending[id];
    setMsg(msg, "Falha ao solicitar análise.", "err");
    updateRunnerUI();
  }
}

async function stopAgent() {
  const id = document.getElementById("agentSelect").value;
  const msg = document.getElementById("runnerMsg");
  if (!id) return;
  document.getElementById("stopBtn").disabled = true;
  delete localPending[id];
  setMsg(msg, "Interrompendo scan…", "err");
  try {
    const r = await fetch(`${API}/agents/${id}/stop`, { method: "POST" });
    if (!r.ok) throw new Error(await r.text());
    setMsg(msg, "⏹ Parada solicitada — encerrando o nmap.", "err");
    setTimeout(loadAgents, 1000);
  } catch (e) {
    console.error(e);
    setMsg(msg, "Falha ao interromper.", "err");
  } finally {
    setTimeout(() => { document.getElementById("stopBtn").disabled = false; }, 1200);
  }
}

function setMsg(el, text, cls) { el.textContent = text; el.className = "muted " + (cls || ""); }

function labelAsset(k) {
  return { computer: "Computadores", server: "Servidores", network: "Equipamentos",
           printer: "Impressoras", unknown: "Desconhecido" }[k] || k;
}

function drawDoughnut(id, obj, colorFn, labelFn) {
  const keys = Object.keys(obj);
  const data = keys.map(k => obj[k]);
  const colors = keys.map((k, i) => colorFn(k, i));
  render(id, {
    type: "doughnut",
    data: { labels: keys.map(labelFn), datasets: [{ data, backgroundColor: colors, borderColor: "#161b22", borderWidth: 2 }] },
    options: { plugins: { legend: { position: "right", labels: { color: "#8b98a9", padding: 12, font: { size: 12 } } } }, cutout: "62%" },
  });
}

function drawApps(apps) {
  render("chartApps", {
    type: "bar",
    data: {
      labels: apps.map(a => a.name),
      datasets: [{ data: apps.map(a => a.count), backgroundColor: "#4f9cf9", borderRadius: 4 }],
    },
    options: {
      indexAxis: "y",
      plugins: { legend: { display: false } },
      scales: {
        x: { ticks: { color: "#8b98a9" }, grid: { color: "#2a3340" } },
        y: { ticks: { color: "#e6edf3" }, grid: { display: false } },
      },
    },
  });
}

function render(id, cfg) {
  if (charts[id]) charts[id].destroy();
  charts[id] = new Chart(document.getElementById(id), cfg);
}

function fillRecent(hosts) {
  const tb = document.querySelector("#recentTable tbody");
  tb.innerHTML = hosts.map(h => `
    <tr><td>${esc(h.ip)}</td><td>${esc(h.hostname) || "—"}</td>
    <td>${badge(h.asset_type)}</td><td>${esc(h.os_guess) || "—"}</td>
    <td>${statusTag(h.status)}</td></tr>`).join("") ||
    `<tr><td colspan="5" class="muted">Nenhum ativo ainda. Instale um agente para iniciar a descoberta.</td></tr>`;
}

// ---- CMDB ----
document.getElementById("searchBtn").addEventListener("click", search);
document.getElementById("searchInput").addEventListener("keydown", e => { if (e.key === "Enter") search(); });

async function search() {
  const field = document.getElementById("searchField").value;
  const q = document.getElementById("searchInput").value.trim();
  let res;
  try { res = await getJSON(`${API}/hosts?field=${encodeURIComponent(field)}&q=${encodeURIComponent(q)}`); }
  catch (e) { console.error(e); return; }
  setText("searchCount", `${res.count} ativo(s)`);
  const body = document.getElementById("cmdbBody");
  body.innerHTML = (res.hosts || []).map(h => {
    const apps = (h.ports || []).slice(0, 8).map(p =>
      `<span class="chip">${p.port}${p.service ? "/" + esc(p.service) : ""}</span>`).join("") || "—";
    const vendor = h.mac ? `${esc(h.mac)}${h.vendor ? " · " + esc(h.vendor) : ""}` : "—";
    return `<tr data-id="${h.id}">
      <td>${esc(h.ip)}</td><td>${esc(h.hostname) || "—"}</td>
      <td>${badge(h.asset_type)}</td><td>${esc(h.os_guess) || "—"}</td>
      <td>${vendor}</td><td>${apps}</td><td>${statusTag(h.status)}</td></tr>`;
  }).join("") || `<tr><td colspan="7" class="muted" style="padding:18px">Nenhum ativo encontrado.</td></tr>`;

  body.querySelectorAll("tr[data-id]").forEach(tr =>
    tr.addEventListener("click", () => showHost(tr.dataset.id)));
}

async function showHost(id) {
  let h;
  try { h = await getJSON(`${API}/hosts/${id}`); }
  catch (e) { console.error(e); return; }
  const ports = (h.ports || []).map(p =>
    `<tr><td>${p.port}/${esc(p.protocol)}</td><td>${esc(p.service) || "—"}</td>
     <td>${esc(p.product)} ${esc(p.version)}</td></tr>`).join("") ||
    `<tr><td colspan="3" class="muted">Nenhuma porta aberta detectada.</td></tr>`;
  document.getElementById("modalContent").innerHTML = `
    <h2>${esc(h.hostname) || esc(h.ip)}</h2>
    <span>${badge(h.asset_type)} ${statusTag(h.status)}</span>
    <dl class="kv">
      <dt>IP</dt><dd>${esc(h.ip)}</dd>
      <dt>Hostname</dt><dd>${esc(h.hostname) || "—"}</dd>
      <dt>Sistema Op.</dt><dd>${esc(h.os_guess) || "—"}</dd>
      <dt>MAC</dt><dd>${esc(h.mac) || "—"}</dd>
      <dt>Vendor</dt><dd>${esc(h.vendor) || "—"}</dd>
      <dt>Visto em</dt><dd>${new Date(h.last_seen).toLocaleString("pt-BR")}</dd>
    </dl>
    <h3>Portas / Aplicações</h3>
    <table class="tbl"><thead><tr><th>Porta</th><th>Serviço</th><th>Produto</th></tr></thead><tbody>${ports}</tbody></table>`;
  document.getElementById("hostModal").classList.remove("hidden");
}
document.getElementById("modalClose").addEventListener("click", () =>
  document.getElementById("hostModal").classList.add("hidden"));
document.getElementById("hostModal").addEventListener("click", e => {
  if (e.target.id === "hostModal") e.currentTarget.classList.add("hidden");
});

// ---- Network Map ----
async function loadMap() {
  let data;
  try { data = await getJSON(`${API}/network-map`); }
  catch (e) { console.error(e); return; }
  const elements = [...(data.nodes || []), ...(data.edges || [])];
  cy = cytoscape({
    container: document.getElementById("cy"),
    elements,
    style: [
      { selector: "node", style: {
        "label": "data(label)", "color": "#e6edf3", "font-size": "10px",
        "text-valign": "bottom", "text-margin-y": 4, "width": 26, "height": 26,
        "background-color": ele => nodeColor(ele), "border-width": 2, "border-color": "#0d1117",
      }},
      { selector: 'node[kind="internet"]', style: { "width": 60, "height": 60, "background-color": "#f0a04b", "font-size": "14px", "font-weight": "bold" } },
      { selector: 'node[kind="subnet"]', style: { "width": 40, "height": 40, "background-color": "#30363d", "border-color": "#8b98a9", "shape": "round-rectangle", "font-size": "11px" } },
      { selector: "edge", style: { "width": 1.5, "line-color": "#2a3340", "curve-style": "bezier", "target-arrow-color": "#2a3340" } },
      { selector: 'node[status="down"]', style: { "opacity": 0.4 } },
    ],
    layout: { name: "cose", animate: false, padding: 30, nodeRepulsion: 6000, idealEdgeLength: 70 },
  });
  cy.on("tap", 'node[kind="host"]', evt => {
    const id = String(evt.target.id()).replace("host-", "");
    switchView("cmdb");
    document.querySelector(".nav-item[data-view='cmdb']").classList.add("active");
    showHost(id);
  });
}

function nodeColor(ele) {
  const k = ele.data("assety");
  if (ele.data("kind") === "internet") return "#f0a04b";
  if (ele.data("kind") === "subnet") return "#30363d";
  return PALETTE[k] || PALETTE.unknown;
}

// ---- helpers ----
function setText(id, v) { const el = document.getElementById(id); if (el) el.textContent = v; }
function badge(t) { t = t || "unknown"; return `<span class="badge ${t}">${labelAsset(t)}</span>`; }
function statusTag(s) { return s === "down" ? `<span class="status-down">● down</span>` : `<span class="status-up">● up</span>`; }
function esc(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Boot — supports deep-linking via #dashboard | #map | #cmdb
function boot() {
  const v = (location.hash || "#dashboard").slice(1);
  switchView(["dashboard", "map", "cmdb"].includes(v) ? v : "dashboard");
}
window.addEventListener("hashchange", boot);
document.getElementById("runBtn").addEventListener("click", runAgent);
document.getElementById("stopBtn").addEventListener("click", stopAgent);
document.getElementById("agentSelect").addEventListener("change", updateRunnerUI);
boot();
