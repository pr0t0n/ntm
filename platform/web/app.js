"use strict";
// Atlas CMDB layout, wired to the real NTM platform API. The screen structure,
// inline styles and component classes are the imported design; only the data
// bindings are adapted to the platform's own hosts/agents.

const API = "/api/v1";
const CRIT = "oklch(0.72 0.14 22)";
const WARN = "oklch(0.83 0.12 80)";
const ICON = {
  "Servidor": "ph-hard-drives", "Estação": "ph-desktop", "Switch": "ph-swap",
  "Roteador": "ph-arrows-split", "Firewall": "ph-shield", "Storage": "ph-database",
  "Balanceador": "ph-scales", "AP Wi-Fi": "ph-wifi-high", "Impressora": "ph-printer",
  "Câmera IP": "ph-security-camera", "Controle de acesso": "ph-door",
  "Sensor": "ph-thermometer", "Desconhecido": "ph-question",
};
const EOL = ["Windows Server 2012", "Windows Server 2008", "CentOS 7", "Windows 10",
  "Windows 7", "Windows XP", "Cisco IOS 15.2", "Ubuntu 16.04", "Debian 9"];
const FAM_COLOR = {
  "Windows": "var(--color-accent-400)", "Linux": "var(--color-accent-200)",
  "macOS": "var(--color-accent-600)", "Rede / appliance": "var(--color-neutral-300)",
  "Embarcado / desconhecido": "var(--color-neutral-600)",
};

const state = {
  screen: localStorage.getItem("atlas.screen") || "dash",
  search: "", fType: "Todos", fFam: "Todas", fSubnet: "Todas", fStatus: "Todos",
  fEol: false, stale: false, limit: 40, drawer: null,
  mapSel: null, focus: null, colorBy: "tipo", hl: "none", links: true, mapView: null, installToken: null,
  scanName: "Varredura ad-hoc", agentId: null, range: "",
};

let ASSETS = [];
let SUBNETS = [];
let AGENTS = [];
let byId = {};
let byIP = {};
let CHANGES = [];
const CHANGE_KIND = {
  host_novo: { icon: "ph-plus-circle", label: "Novo host", tone: "var(--color-accent-400)" },
  hostname: { icon: "ph-identification-card", label: "Hostname", tone: "var(--color-accent-300)" },
  so: { icon: "ph-desktop", label: "Sistema operacional", tone: "var(--color-accent-300)" },
  porta_aberta: { icon: "ph-door-open", label: "Porta aberta", tone: "var(--color-accent-400)" },
  porta_fechada: { icon: "ph-door", label: "Porta fechada", tone: "var(--color-neutral-400)" },
};

// ---- data loading: map real hosts to the design's asset shape ----
async function getJSON(u) { const r = await fetch(u); if (!r.ok) throw new Error(r.status + " " + u); return r.json(); }

function typeFromAsset(t) {
  return { server: "Servidor", computer: "Estação", network: "Switch",
    printer: "Impressora", unknown: "Desconhecido" }[t] || "Desconhecido";
}
function famFromOs(os) {
  os = os || "";
  if (/windows/i.test(os)) return "Windows";
  if (/ubuntu|rhel|cent ?os|debian|linux|unix/i.test(os)) return "Linux";
  if (/mac|darwin|apple/i.test(os)) return "macOS";
  if (/network|cisco|juniper|fortinet|aruba|mikrotik/i.test(os)) return "Rede / appliance";
  return "Embarcado / desconhecido";
}
function catFromType(t) {
  if (["Servidor", "Storage", "Balanceador"].includes(t)) return "Servidores";
  if (t === "Estação") return "Endpoints";
  if (["Switch", "Roteador", "Firewall", "AP Wi-Fi"].includes(t)) return "Rede";
  return "IoT / periféricos";
}
function critFromType(t) {
  if (["Firewall", "Storage", "Balanceador", "Roteador"].includes(t)) return "Alta";
  if (t === "Servidor") return "Média";
  return "Baixa";
}
const isEol = os => !!os && EOL.some(e => os.toLowerCase().includes(e.toLowerCase()));
function statusColor(s) { return s === "Gerenciado" ? "var(--color-accent-400)" : s === "Novo" ? WARN : CRIT; }

async function loadAssets() {
  const [hostsRes, agentsRes] = await Promise.all([
    getJSON(`${API}/hosts?field=all&q=`).catch(() => ({ hosts: [] })),
    getJSON(`${API}/agents`).catch(() => ({ agents: [] })),
  ]);
  AGENTS = agentsRes.agents || [];
  const now = Date.now();
  ASSETS = (hostsRes.hosts || []).map(h => {
    const type = typeFromAsset(h.asset_type);
    const os = h.os_guess || "";
    const discDays = h.first_seen ? Math.max(0, Math.floor((now - new Date(h.first_seen)) / 86400000)) : 0;
    const seenMin = h.last_seen ? Math.max(0, Math.floor((now - new Date(h.last_seen)) / 60000)) : 0;
    const managed = !!(h.hostname && os);
    const status = discDays < 7 ? "Novo" : managed ? "Gerenciado" : "Não gerenciado";
    const prefix = (h.ip || "0.0.0.0").split(".").slice(0, 3).join(".");
    const ports = h.ports || [];
    const app = ports.map(p => (p.product || p.service || "") + (p.version ? " " + p.version : "")).filter(Boolean)[0] || "";
    return {
      id: String(h.id), ci: "CI" + h.id, host: h.hostname || h.ip, hostname: h.hostname || "", ip: h.ip, mac: h.mac || "",
      type, icon: ICON[type], os: os || "Desconhecido", fam: famFromOs(os), cat: catFromType(type),
      eol: isEol(os), subnet: prefix, subnetName: prefix + ".0/24", status, discDays, seenMin,
      crit: critFromType(type), owner: "—", user: "—", vendor: h.vendor || app || "—",
      cpu: "—", ports, portCount: ports.length, hostUp: h.status !== "down", app,
    };
  });
  byId = {}; byIP = {}; ASSETS.forEach(a => { byId[a.id] = a; byIP[a.ip] = a; });
  buildSubnets();
  try { CHANGES = (await getJSON(`${API}/changes?limit=200`)).changes || []; } catch (e) { CHANGES = []; }
}

function buildSubnets() {
  const map = {};
  ASSETS.forEach(a => {
    if (!map[a.subnet]) map[a.subnet] = { id: a.subnet, name: a.subnetName, cidr: a.subnetName, base: a.subnet + ".", list: [] };
    map[a.subnet].list.push(a);
  });
  SUBNETS = Object.values(map).sort((a, b) => b.list.length - a.list.length);
  layoutMap();
}

let MAP_H = 600;
// layoutMap arranges subnet hubs in a grid (so many /24s don't overlap) and
// places each subnet's hosts in rings around its hub.
function layoutMap() {
  const n = SUBNETS.length || 1, W = 1240;
  const cols = Math.max(1, Math.min(n, Math.floor(W / 230)));
  const rows = Math.ceil(n / cols);
  const colGap = W / (cols + 1), rowH = 210, topY = 180;
  SUBNETS.forEach((sn, i) => {
    const c = i % cols, r = Math.floor(i / cols);
    sn.x = +(colGap * (c + 1)).toFixed(1);
    sn.y = topY + r * rowH;
    const list = sn.list; let idx = 0, ring = 0;
    while (idx < list.length) {
      const rad = 30 + ring * 15, cap = Math.max(1, Math.floor(2 * Math.PI * rad / 13));
      const cnt = Math.min(cap, list.length - idx);
      for (let k = 0; k < cnt; k++) {
        const ang = -Math.PI / 2 + (k / cnt) * 2 * Math.PI + ring * 0.3;
        list[idx + k].x = +(sn.x + Math.cos(ang) * rad).toFixed(1);
        list[idx + k].y = +(sn.y + Math.sin(ang) * rad).toFixed(1);
      }
      idx += cnt; ring++;
    }
    sn.outer = 30 + Math.max(0, ring - 1) * 15;
  });
  MAP_H = topY + (rows - 1) * rowH + 130;
}

// ---- helpers ----
function esc(s) { return s == null ? "" : String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }
function fmtSeen(m) { return m < 60 ? `há ${m} min` : m < 1440 ? `há ${Math.floor(m / 60)} h` : `há ${Math.floor(m / 1440)} dias`; }
function seg(opts, cur, act) {
  return opts.map(o => {
    const v = o.v != null ? o.v : o, on = cur === v, label = o.l != null ? o.l : o;
    return `<button onclick="App.${act}('${v}')" style="border:0;background:transparent;padding:6px 11px;font:inherit;font-size:12.5px;cursor:pointer;color:${on ? "var(--color-accent)" : "var(--color-neutral-300)"};box-shadow:${on ? "inset 0 0 0 1px var(--color-accent)" : "none"}">${esc(label)}</button>`;
  }).join("");
}
function dayLabel(d) { const dt = new Date(Date.now() - d * 86400000); return dt.toLocaleDateString("pt-BR", { day: "2-digit", month: "short" }).replace(".", ""); }

// ============================ RENDER ============================
function render() {
  renderNav();
  const titles = { dash: "Dashboard", map: "Mapa de rede", cmdb: "CMDB", changes: "Mudanças", scan: "Varredura" };
  document.getElementById("title").textContent = titles[state.screen];
  document.getElementById("crumb").textContent = titles[state.screen];
  const scr = document.getElementById("screen");
  scr.innerHTML = state.screen === "dash" ? viewDash()
    : state.screen === "cmdb" ? viewCmdb()
    : state.screen === "changes" ? viewChanges()
    : state.screen === "scan" ? viewScan()
    : viewMap();
  document.getElementById("drawer").innerHTML = state.drawer ? viewDrawer() : "";
  renderCollector();
  if (state.screen === "map") wireMap();
}

// wireMap adds wheel-zoom (toward the cursor) and drag-to-pan on the map SVG by
// mutating its viewBox live, and keeps the view in state so it survives
// re-renders (e.g. selecting a node).
function wireMap() {
  const svg = document.getElementById("mapSvg");
  if (!svg) return;
  const vb = () => state.mapView || { x: 0, y: 0, w: 1240, h: MAP_H };
  const toSvg = e => { const r = svg.getBoundingClientRect(); const v = vb(); return { x: v.x + (e.clientX - r.left) / r.width * v.w, y: v.y + (e.clientY - r.top) / r.height * v.h }; };
  const apply = v => { state.mapView = v; svg.setAttribute("viewBox", `${v.x} ${v.y} ${v.w} ${v.h}`); };
  svg.onwheel = e => {
    e.preventDefault();
    const v = vb(), p = toSvg(e);
    let f = e.deltaY < 0 ? 0.85 : 1.18;
    let nw = v.w * f, nh = v.h * f;
    const maxW = 1240, minW = 180;
    if (nw > maxW) { nw = maxW; nh = maxW * (MAP_H / 1240); }
    if (nw < minW) { nw = minW; nh = minW * (MAP_H / 1240); }
    apply({ x: p.x - (p.x - v.x) * (nw / v.w), y: p.y - (p.y - v.y) * (nh / v.h), w: nw, h: nh });
  };
  // Pan only when the pointer actually moves past a small threshold, so a plain
  // click still reaches a node/subnet (drill-down keeps working).
  let drag = null, panned = false;
  svg.onpointerdown = e => { drag = { sx: e.clientX, sy: e.clientY, v: vb() }; panned = false; };
  svg.onpointermove = e => {
    if (!drag) return;
    if (!panned && Math.abs(e.clientX - drag.sx) + Math.abs(e.clientY - drag.sy) < 5) return;
    panned = true; svg.style.cursor = "grabbing";
    const r = svg.getBoundingClientRect(), v = drag.v;
    apply({ x: v.x - (e.clientX - drag.sx) / r.width * v.w, y: v.y - (e.clientY - drag.sy) / r.height * v.h, w: v.w, h: v.h });
  };
  const end = () => { drag = null; svg.style.cursor = "grab"; };
  svg.onpointerup = end; svg.onpointerleave = end;
  // Swallow the click that follows a real pan so it does not select a node.
  svg.addEventListener("click", e => { if (panned) { e.stopPropagation(); e.preventDefault(); panned = false; } }, true);
}

function renderNav() {
  const N = ASSETS.length;
  const running = AGENTS.some(a => a.scan_state === "running");
  const NAV = [["dash", "Dashboard", "ph-squares-four", ""], ["map", "Mapa de rede", "ph-graph", ""],
    ["cmdb", "CMDB", "ph-database", N], ["changes", "Mudanças", "ph-clock-counter-clockwise", CHANGES.length || ""],
    ["scan", "Varredura", "ph-broadcast", running ? "●" : ""]];
  document.getElementById("nav").innerHTML = NAV.map(([id, label, icon, count]) => {
    const on = state.screen === id;
    return `<button onclick="App.go('${id}')" style="display:flex;align-items:center;gap:10px;padding:9px 10px;border:0;border-radius:var(--radius-md);background:${on ? "color-mix(in srgb,var(--color-accent) 12%,transparent)" : "transparent"};color:${on ? "var(--color-accent-300)" : "var(--color-neutral-300)"};font:inherit;font-size:14px;cursor:pointer;text-align:left;box-shadow:${on ? "inset 2px 0 0 var(--color-accent)" : "none"};width:100%">
      <i class="ph ${icon}" style="font-size:18px"></i><span style="flex:1">${label}</span><span style="font-size:11px;color:var(--color-neutral-500)">${count}</span>
    </button>`;
  }).join("");
}

function renderCollector() {
  const a = AGENTS[0];
  const online = AGENTS.some(x => x.online);
  document.getElementById("collectorStatus").innerHTML =
    `<span style="width:7px;height:7px;border-radius:50%;background:${online ? "var(--color-accent-400)" : "var(--color-neutral-600)"};box-shadow:${online ? "0 0 8px var(--color-accent)" : "none"}"></span>${a ? esc(a.name) : "Coletor"} ${online ? "online" : "offline"}`;
  const last = a && a.last_scan_at && !a.last_scan_at.startsWith("0001") ? new Date(a.last_scan_at).toLocaleString("pt-BR") : "nunca";
  document.getElementById("collectorSub").textContent = `${AGENTS.length} coletor(es) · última varredura: ${last}`;
}

// ---------------- DASHBOARD ----------------
function dash() {
  const assets = ASSETS, N = assets.length || 1;
  const novos = assets.filter(a => a.discDays < 7).length;
  const totalPorts = assets.reduce((n, a) => n + a.portCount, 0);
  const withPorts = assets.filter(a => a.portCount > 0).length;
  const famCount = {}; assets.forEach(a => famCount[a.fam] = (famCount[a.fam] || 0) + 1);
  const families = Object.entries(famCount).sort((a, b) => b[1] - a[1]).map(([name, count]) => ({ name, count, pct: Math.round(count / N * 100), color: FAM_COLOR[name] || "var(--color-neutral-500)" }));
  const osC = {}; assets.forEach(a => osC[a.os] = (osC[a.os] || 0) + 1);
  const osSorted = Object.entries(osC).sort((a, b) => b[1] - a[1]);
  const maxOs = osSorted.length ? osSorted[0][1] : 1;
  const osList = osSorted.slice(0, 10).map(([name, count]) => { const a = assets.find(x => x.os === name); return { name, count, w: Math.round(count / maxOs * 100), color: FAM_COLOR[a.fam] || "var(--color-neutral-500)" }; });
  const tC = {}; assets.forEach(a => tC[a.type] = (tC[a.type] || 0) + 1);
  const types = Object.entries(tC).sort((a, b) => b[1] - a[1]).map(([name, count]) => ({ name, count, icon: ICON[name] }));
  const svcC = {}; assets.forEach(a => a.ports.forEach(p => { const k = p.service || p.product || ("porta " + p.port); svcC[k] = (svcC[k] || 0) + 1; }));
  const svcSorted = Object.entries(svcC).sort((a, b) => b[1] - a[1]);
  const maxSvc = svcSorted.length ? svcSorted[0][1] : 1;
  const services = svcSorted.slice(0, 10).map(([name, count]) => ({ name, count, w: Math.round(count / maxSvc * 100) }));
  const tot = [], nw = [];
  for (let d = 29; d >= 0; d--) { tot.push(assets.filter(a => a.discDays >= d).length); nw.push(assets.filter(a => a.discDays === d).length); }
  const mn = Math.max(0, Math.min(...tot) - 4), mx = Math.max(...tot) + 2, maxNew = Math.max(...nw, 1);
  const X = i => i * (600 / 29), Y = v => 170 - (v - mn) / Math.max(1, mx - mn) * 150;
  const line = tot.map((v, i) => `${i ? "L" : "M"}${X(i).toFixed(1)},${Y(v).toFixed(1)}`).join(" ");
  const bars = nw.map((v, i) => ({ x: (X(i) - 5).toFixed(1), w: 10, h: (v / maxNew * 46).toFixed(1), y: (180 - v / maxNew * 46).toFixed(1) }));
  const subnets = SUBNETS.map(sn => { const l = sn.list, ports = l.reduce((n, a) => n + a.portCount, 0); const svc = new Set(); l.forEach(a => a.ports.forEach(p => svc.add(p.service || p.product || p.port))); const maxC = Math.max(1, ...SUBNETS.map(x => x.list.length)); return { id: sn.id, name: sn.name, cidr: sn.cidr, count: l.length, w: Math.round(l.length / maxC * 100), ports, services: svc.size }; });
  const recent = [...assets].sort((a, b) => a.seenMin - b.seenMin).slice(0, 6).map(a => ({ id: a.id, icon: a.icon, tone: "var(--color-accent-400)", title: a.host, sub: `${a.type} · ${a.os} · ${a.ip}`, when: fmtSeen(a.seenMin) }));
  return {
    summary: `${assets.length} ativos mapeados em ${SUBNETS.length} sub-rede(s), com ${totalPorts} portas abertas em ${svcSorted.length} serviço(s) distinto(s).`,
    kpis: [
      { icon: "ph-cube", label: "Ativos descobertos", value: assets.length, unit: "itens", sub: `+${novos} nos últimos 7 dias`, tone: "var(--color-accent)", nav: "cmdb" },
      { icon: "ph-plugs-connected", label: "Portas abertas", value: totalPorts, unit: "portas", sub: `${withPorts} ativos com portas`, tone: "var(--color-accent-300)", nav: "cmdb" },
      { icon: "ph-tree-structure", label: "Sub-redes", value: SUBNETS.length, unit: "redes", sub: "descobertas na rede", tone: "var(--color-accent-400)", nav: "map" },
      { icon: "ph-sparkle", label: "Novos (7 dias)", value: novos, unit: "ativos", sub: "descobertos recentemente", tone: WARN, nav: "cmdb" },
    ],
    families, osList, osVersions: osSorted.length, types, typeCount: types.length,
    services, svcCount: svcSorted.length,
    line, area: line + " L600,180 L0,180 Z", bars, trendMax: mx, trendMin: mn, d0: dayLabel(29), d15: dayLabel(15),
    subnets, recent,
  };
}

function viewDash() {
  const d = dash();
  const card = "padding:18px 20px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);display:flex;flex-direction:column;gap:14px";
  return `<div style="display:flex;flex-direction:column;gap:18px">
    <p style="margin:0;font-size:15px;color:var(--color-neutral-400);max-width:760px;text-wrap:pretty">${esc(d.summary)}</p>
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px">
      ${d.kpis.map(k => `<button onclick="App.go('${k.nav}')" style="display:flex;flex-direction:column;gap:6px;text-align:left;padding:16px 18px;border:0;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);color:var(--color-text);font:inherit;cursor:pointer">
        <div style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--color-neutral-400)"><i class="ph ${k.icon}" style="font-size:15px;color:${k.tone}"></i>${k.label}</div>
        <div style="display:flex;align-items:baseline;gap:8px"><span style="font-size:34px;font-weight:500;letter-spacing:-.02em;line-height:1.1">${k.value}</span><span style="font-size:13px;color:var(--color-neutral-500)">${k.unit}</span></div>
        <div style="font-size:12px;color:var(--color-neutral-500)">${esc(k.sub)}</div>
        <div style="width:18px;height:2px;border-radius:2px;background:${k.tone}"></div>
      </button>`).join("")}
    </div>

    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:14px">
      <section style="${card}">
        <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Sistemas operacionais</h4><span style="font-size:12px;color:var(--color-neutral-500)">${d.osVersions} versões em uso</span></div>
        <div style="display:flex;height:10px;border-radius:5px;overflow:hidden;gap:2px">
          ${d.families.map(f => `<div title="${esc(f.name)}" style="width:${f.pct}%;background:${f.color}"></div>`).join("")}
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:6px 18px">
          ${d.families.map(f => `<div style="display:flex;align-items:center;gap:7px;font-size:12px"><span style="width:8px;height:8px;border-radius:2px;background:${f.color}"></span>${esc(f.name)}<span style="color:var(--color-neutral-500)">${f.count} · ${f.pct}%</span></div>`).join("")}
        </div>
        <div style="display:flex;flex-direction:column;gap:7px;margin-top:4px">
          ${d.osList.map(o => `<button onclick='App.filterTo({"search":${JSON.stringify(o.name)}})' style="display:grid;grid-template-columns:minmax(0,190px) minmax(0,1fr) 34px;align-items:center;gap:12px;border:0;background:transparent;color:var(--color-text);font:inherit;padding:2px 0;cursor:pointer;text-align:left">
            <span style="font-size:13px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(o.name)}</span>
            <span style="height:6px;border-radius:3px;background:var(--color-neutral-900)"><span style="display:block;height:100%;border-radius:3px;width:${o.w}%;background:${o.color}"></span></span>
            <span style="font-size:13px;text-align:right;color:var(--color-neutral-300)">${o.count}</span>
          </button>`).join("") || `<span style="font-size:13px;color:var(--color-neutral-500)">Sem dados</span>`}
        </div>
      </section>

      <section style="${card}">
        <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Aplicações e serviços</h4><span style="font-size:12px;color:var(--color-neutral-500)">${d.svcCount} serviços em escuta</span></div>
        <div style="display:flex;flex-direction:column;gap:7px;margin-top:2px">
          ${d.services.map(sv => `<button onclick='App.filterTo({"search":${JSON.stringify(sv.name)}})' style="display:grid;grid-template-columns:minmax(0,190px) minmax(0,1fr) 34px;align-items:center;gap:12px;border:0;background:transparent;color:var(--color-text);font:inherit;padding:2px 0;cursor:pointer;text-align:left">
            <span style="font-size:13px;display:flex;align-items:center;gap:7px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis"><i class="ph ph-plug" style="color:var(--color-accent-400)"></i>${esc(sv.name)}</span>
            <span style="height:6px;border-radius:3px;background:var(--color-neutral-900)"><span style="display:block;height:100%;border-radius:3px;width:${sv.w}%;background:var(--color-accent-500)"></span></span>
            <span style="font-size:13px;text-align:right;color:var(--color-neutral-300)">${sv.count}</span>
          </button>`).join("") || `<span style="font-size:13px;color:var(--color-neutral-500);padding:10px 0">Nenhuma porta aberta detectada.</span>`}
        </div>
      </section>
    </div>

    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:14px">
      <section style="${card.replace("gap:14px", "gap:12px")}">
        <div style="display:flex;align-items:baseline;gap:10px;flex-wrap:wrap"><h4 style="margin:0;font-size:16px">Evolução do inventário</h4><span style="font-size:12px;color:var(--color-neutral-500)">últimos 30 dias</span>
          <div style="margin-left:auto;display:flex;gap:14px;font-size:12px;color:var(--color-neutral-400)"><span style="display:flex;align-items:center;gap:6px"><span style="width:14px;height:2px;background:var(--color-accent)"></span>Total de ativos</span><span style="display:flex;align-items:center;gap:6px"><span style="width:8px;height:8px;border-radius:2px;background:var(--color-accent-800)"></span>Novos no dia</span></div>
        </div>
        <div style="display:flex;gap:10px">
          <div style="display:flex;flex-direction:column;justify-content:space-between;font-size:11px;color:var(--color-neutral-600);padding:2px 0 18px"><span>${d.trendMax}</span><span>${d.trendMin}</span></div>
          <div style="flex:1;min-width:0;display:flex;flex-direction:column;gap:4px">
            <svg viewBox="0 0 600 180" preserveAspectRatio="none" style="width:100%;height:180px;display:block">
              ${d.bars.map(b => `<rect x="${b.x}" y="${b.y}" width="${b.w}" height="${b.h}" rx="2" style="fill:var(--color-accent-800)"></rect>`).join("")}
              <path d="${d.area}" style="fill:color-mix(in srgb,var(--color-accent) 12%,transparent)"></path>
              <path d="${d.line}" style="fill:none;stroke:var(--color-accent);stroke-width:2;vector-effect:non-scaling-stroke"></path>
            </svg>
            <div style="display:flex;justify-content:space-between;font-size:11px;color:var(--color-neutral-600)"><span>${d.d0}</span><span>${d.d15}</span><span>Hoje</span></div>
          </div>
        </div>
      </section>

      <section style="${card.replace("gap:14px", "gap:12px")}">
        <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Ativos por tipo</h4><span style="font-size:12px;color:var(--color-neutral-500)">${d.typeCount} categorias</span></div>
        <div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:8px">
          ${d.types.map(t => `<button onclick='App.filterTo({"fType":${JSON.stringify(t.name)}})' style="display:flex;align-items:center;gap:10px;padding:10px 12px;border:0;border-radius:var(--radius-md);background:var(--color-bg);color:var(--color-text);font:inherit;cursor:pointer;text-align:left">
            <i class="ph ${t.icon}" style="font-size:18px;color:var(--color-accent-400)"></i>
            <span style="flex:1;font-size:12.5px;color:var(--color-neutral-300)">${esc(t.name)}</span>
            <span style="font-size:15px;font-weight:500">${t.count}</span>
          </button>`).join("")}
        </div>
      </section>
    </div>

    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:14px">
      <section style="${card.replace("gap:14px", "gap:8px")}">
        <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Ativos por sub-rede</h4><button class="btn btn-ghost" onclick="App.go('map')" style="margin-left:auto;font-size:12.5px">Abrir mapa<i class="ph ph-arrow-right"></i></button></div>
        <table class="table" style="font-size:13px">
          <thead><tr><th>Sub-rede</th><th>Ativos</th><th style="width:34%">Distribuição</th><th>Portas</th><th>Serviços</th></tr></thead>
          <tbody>
            ${d.subnets.map(sv => `<tr onclick='App.go("map",{"focus":${JSON.stringify(sv.id)}})' style="cursor:pointer">
              <td><div style="display:flex;flex-direction:column"><span>${esc(sv.name)}</span><span style="font-size:11.5px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(sv.cidr)}</span></div></td>
              <td>${sv.count}</td>
              <td><span style="display:block;height:6px;border-radius:3px;background:var(--color-neutral-900)"><span style="display:block;height:100%;border-radius:3px;width:${sv.w}%;background:var(--color-accent-500)"></span></span></td>
              <td style="color:var(--color-neutral-300)">${sv.ports}</td>
              <td style="color:var(--color-neutral-300)">${sv.services}</td>
            </tr>`).join("")}
          </tbody>
        </table>
      </section>
      <section style="${card.replace("gap:14px", "gap:8px")}">
        <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Mudanças recentes</h4><button class="btn btn-ghost" onclick="App.go('changes')" style="margin-left:auto;font-size:12.5px">Ver todas<i class="ph ph-arrow-right"></i></button></div>
        <div style="display:flex;flex-direction:column">
          ${CHANGES.slice(0, 6).map(changeRow).join("") || `<span style="font-size:13px;color:var(--color-neutral-500);padding:10px 0">Nenhuma mudança registrada ainda.</span>`}
        </div>
      </section>
    </div>
  </div>`;
}

// ---------------- CMDB ----------------
function filtered() {
  const s = state, q = s.search.trim().toLowerCase();
  return ASSETS.filter(a =>
    (s.fType === "Todos" || a.type === s.fType) &&
    (s.fFam === "Todas" || a.fam === s.fFam) &&
    (s.fSubnet === "Todas" || a.subnetName === s.fSubnet) &&
    (!q || (a.host + " " + a.ip + " " + a.mac + " " + a.os + " " + a.vendor + " " +
            a.ports.map(p => p.port + " " + (p.service || "") + " " + (p.product || "")).join(" ")).toLowerCase().includes(q)));
}

function viewCmdb() {
  const s = state, l = filtered();
  const rows = l.slice(0, s.limit);
  const withPorts = l.filter(a => a.portCount > 0).length;
  const totalPorts = l.reduce((n, a) => n + a.portCount, 0);
  const hasFilters = s.fType !== "Todos" || s.fFam !== "Todas" || s.fSubnet !== "Todas" || !!s.search;
  const typeOpts = ["Todos", ...Object.keys(ICON)];
  const famOpts = ["Todas", "Windows", "Linux", "macOS", "Rede / appliance", "Embarcado / desconhecido"];
  const subOpts = ["Todas", ...SUBNETS.map(x => x.name)];
  const opt = (arr, cur) => arr.map(o => `<option value="${esc(o)}" ${o === cur ? "selected" : ""}>${esc(o)}</option>`).join("");
  const chip = "display:inline-block;font-size:11px;padding:1px 7px;border-radius:5px;background:var(--color-bg);color:var(--color-neutral-300);font-family:ui-monospace,monospace;margin:1px 2px 1px 0";
  const portCell = r => {
    if (!r.ports.length) return `<span style="color:var(--color-neutral-600)">—</span>`;
    const shown = r.ports.slice(0, 8).map(p => `<span style="${chip}">${p.port}${p.service ? "/" + esc(p.service) : ""}</span>`).join("");
    return shown + (r.ports.length > 8 ? `<span style="font-size:11px;color:var(--color-neutral-500)">+${r.ports.length - 8}</span>` : "");
  };
  return `<div style="display:flex;flex-direction:column;gap:14px">
    <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:flex-end">
      <div class="field" style="width:170px"><label>Tipo</label><select class="input" onchange="App.setFilter('fType',this.value)">${opt(typeOpts, s.fType)}</select></div>
      <div class="field" style="width:200px"><label>Família de SO</label><select class="input" onchange="App.setFilter('fFam',this.value)">${opt(famOpts, s.fFam)}</select></div>
      <div class="field" style="width:200px"><label>Sub-rede</label><select class="input" onchange="App.setFilter('fSubnet',this.value)">${opt(subOpts, s.fSubnet)}</select></div>
      <div style="margin-left:auto;display:flex;gap:8px">
        ${hasFilters ? `<button class="btn btn-ghost" onclick="App.clearFilters()" style="font-size:12.5px;height:36px">Limpar filtros</button>` : ""}
        <button class="btn btn-secondary" onclick="App.exportCsv()" style="height:36px"><i class="ph ph-download-simple"></i>Exportar CSV</button>
      </div>
    </div>
    <div style="display:flex;gap:18px;font-size:12.5px;color:var(--color-neutral-400)">
      <span><strong style="color:var(--color-text);font-weight:500">${l.length}</strong> itens de configuração</span>
      <span>${withPorts} com portas abertas</span><span>${totalPorts} portas abertas no total</span>
    </div>
    <div style="border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);padding:6px 10px;overflow-x:auto">
      <table class="table" style="font-size:13px;min-width:960px">
        <thead><tr><th>Ativo</th><th>Hostname</th><th>Tipo</th><th>Sistema operacional</th><th>Sub-rede</th><th>Portas</th><th>Visto</th></tr></thead>
        <tbody>
          ${rows.map(r => `<tr onclick="App.openDrawer('${r.id}')" style="cursor:pointer">
            <td><span style="font-weight:500;font-family:ui-monospace,monospace">${esc(r.ip)}</span></td>
            <td>${r.hostname ? esc(r.hostname) : `<span style="color:var(--color-neutral-600)">—</span>`}</td>
            <td><span style="display:flex;align-items:center;gap:7px;color:var(--color-neutral-300)"><i class="ph ${r.icon}" style="color:var(--color-accent-400)"></i>${esc(r.type)}</span></td>
            <td>${esc(r.os)}</td>
            <td style="color:var(--color-neutral-300)">${esc(r.subnetName)}</td>
            <td style="max-width:360px">${portCell(r)}</td>
            <td style="color:var(--color-neutral-400);white-space:nowrap">${fmtSeen(r.seenMin)}</td>
          </tr>`).join("")}
        </tbody>
      </table>
      ${!l.length ? `<div style="padding:40px;text-align:center;color:var(--color-neutral-500);font-size:13px">Nenhum ativo corresponde aos filtros.</div>` : ""}
      ${l.length > s.limit ? `<div style="padding:12px 8px;display:flex;align-items:center;gap:12px;font-size:12.5px;color:var(--color-neutral-500)"><span>Exibindo ${rows.length} de ${l.length}</span><button class="btn btn-ghost" onclick="App.showMore()" style="font-size:12.5px">Carregar mais</button></div>` : ""}
    </div>
  </div>`;
}

// ---------------- DRAWER ----------------
function viewDrawer() {
  const a = byId[state.drawer]; if (!a) return "";
  const disc = a.discDays != null ? new Date(Date.now() - a.discDays * 86400000).toLocaleDateString("pt-BR") : "—";
  const portRows = a.ports.length
    ? a.ports.map(p => ({ k: `${p.port}/${p.protocol}`, v: [p.service, p.product, p.version].filter(Boolean).join(" ") || "aberta" }))
    : [{ k: "Portas", v: "Nenhuma porta aberta" }];
  const groups = [
    { title: "Sistema", rows: [{ k: "Sistema operacional", v: a.os }, { k: "Família", v: a.fam }, { k: "Fabricante / vendor", v: a.vendor }] },
    { title: "Rede", rows: [{ k: "IP", v: a.ip }, { k: "MAC", v: a.mac || "—" }, { k: "Sub-rede", v: a.subnetName }, { k: "Gateway", v: a.ip.replace(/\\d+$/, "1") }] },
    { title: `Portas / serviços (${a.ports.length})`, rows: portRows },
    { title: "Descoberta", rows: [{ k: "Descoberto em", v: disc }, { k: "Última vez visto", v: fmtSeen(a.seenMin) }] },
  ];
  const hist = CHANGES.filter(c => c.host_ip === a.ip).slice(0, 12);
  const rowStyle = "display:flex;justify-content:space-between;gap:14px;padding:7px 0;font-size:13px;background:linear-gradient(to right,transparent,color-mix(in srgb,var(--color-text) 8%,transparent) 48px,color-mix(in srgb,var(--color-text) 8%,transparent) calc(100% - 48px),transparent) no-repeat bottom/100% 1px";
  return `<div onclick="App.closeDrawer()" style="position:fixed;inset:0;background:color-mix(in srgb,var(--color-neutral-900) 55%,transparent);z-index:20"></div>
    <aside style="position:fixed;top:0;right:0;bottom:0;width:440px;max-width:100%;z-index:21;background:var(--color-surface);box-shadow:var(--shadow-lg);display:flex;flex-direction:column;overflow-y:auto">
      <div style="display:flex;align-items:flex-start;gap:12px;padding:20px 22px 14px">
        <div style="width:42px;height:42px;border-radius:var(--radius-md);display:grid;place-items:center;background:var(--color-bg);color:var(--color-accent-400)"><i class="ph ${a.icon}" style="font-size:22px"></i></div>
        <div style="flex:1;min-width:0;display:flex;flex-direction:column;gap:2px"><span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">${esc(a.ci)}</span><span style="font-size:17px;font-weight:500">${esc(a.host)}</span><span style="font-size:12px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(a.ip)}</span></div>
        <button class="btn btn-icon btn-ghost" onclick="App.closeDrawer()" style="width:30px;height:30px"><i class="ph ph-x"></i></button>
      </div>
      <div style="display:flex;gap:6px;flex-wrap:wrap;padding:0 22px"><span class="tag tag-neutral">${esc(a.type)}</span><span class="tag tag-neutral">${esc(a.fam)}</span>${a.portCount ? `<span class="tag tag-accent">${a.portCount} porta(s) aberta(s)</span>` : ""}</div>
      <div style="padding:18px 22px;display:flex;flex-direction:column;gap:18px">
        ${groups.map(g => `<div style="display:flex;flex-direction:column">
          <div style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500);margin-bottom:4px">${esc(g.title)}</div>
          ${g.rows.map(r => `<div style="${rowStyle}"><span style="color:var(--color-neutral-500)">${esc(r.k)}</span><span style="text-align:right">${esc(r.v)}</span></div>`).join("")}
        </div>`).join("")}
        ${hist.length ? `<div style="display:flex;flex-direction:column">
          <div style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500);margin-bottom:4px">Histórico</div>
          ${hist.map(c => { const k = CHANGE_KIND[c.kind] || { icon: "ph-dot", tone: "var(--color-neutral-400)" }; return `<div style="display:flex;gap:10px;align-items:flex-start;padding:6px 0;font-size:12.5px"><i class="ph ${k.icon}" style="color:${k.tone};margin-top:2px"></i><div style="flex:1;display:flex;flex-direction:column"><span>${esc(c.detail)}</span><span style="font-size:11px;color:var(--color-neutral-500)">${new Date(c.ts).toLocaleString("pt-BR")}</span></div></div>`; }).join("")}
        </div>` : ""}
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn btn-primary" onclick="App.go('map',{mapSel:'${a.id}',focus:'${esc(a.subnet)}'})"><i class="ph ph-graph"></i>Ver no mapa</button>
          <button class="btn btn-secondary" onclick="App.rescan('${a.id}')"><i class="ph ph-arrows-clockwise"></i>Revarrer host</button>
        </div>
      </div>
    </aside>`;
}

// ---------------- MAP ----------------
function viewMap() {
  const s = state;
  const typeC = { "Servidores": "var(--color-accent-400)", "Endpoints": "var(--color-neutral-300)", "Rede": "var(--color-accent-200)", "IoT / periféricos": "var(--color-neutral-600)" };
  const colorOf = a => s.colorBy === "tipo" ? (typeC[a.cat] || "var(--color-neutral-500)") : (FAM_COLOR[a.fam] || "var(--color-neutral-500)");
  const legend = s.colorBy === "tipo" ? Object.entries(typeC) : Object.entries(FAM_COLOR);
  const q = s.search.trim().toLowerCase();
  const qm = a => !q || (a.host + a.ip + a.mac + a.os).toLowerCase().includes(q);
  const match = a => s.hl === "ports" ? a.portCount > 0 : true;
  const core = { x: 620, y: 70 };
  const nodes = ASSETS.filter(a => a.x != null).map(a => {
    const hit = match(a) && qm(a), sel = s.mapSel === a.id, r = a.cat === "Rede" ? 5 : a.cat === "Servidores" ? 4.5 : 3.8;
    return { id: a.id, x: a.x, y: a.y, r, halo: r + 4, sel, color: colorOf(a), op: hit || sel ? 1 : 0.13 };
  });
  const spokes = s.links ? ASSETS.filter(a => a.x != null).map(a => { const sn = SUBNETS.find(x => x.id === a.subnet); return { x1: sn.x, y1: sn.y, x2: a.x, y2: a.y, op: match(a) && qm(a) ? 0.4 : 0.07 }; }) : [];
  const edges = SUBNETS.map(sn => ({ x1: core.x, y1: core.y, x2: sn.x, y2: sn.y }));
  const selA = s.mapSel && byId[s.mapSel];
  const foc = s.focus ? SUBNETS.find(x => x.id === s.focus) : null;

  let aside;
  if (selA) {
    const rows = [["Sistema operacional", selA.os], ["Fabricante / vendor", selA.vendor], ["Sub-rede", selA.subnetName], ["MAC", selA.mac || "—"], ["Portas abertas", selA.portCount], ["Visto", fmtSeen(selA.seenMin)]];
    aside = `<div style="display:flex;flex-direction:column;gap:12px">
      <div style="display:flex;align-items:center;gap:10px">
        <div style="width:36px;height:36px;border-radius:var(--radius-md);display:grid;place-items:center;background:var(--color-bg);color:var(--color-accent-400)"><i class="ph ${selA.icon}" style="font-size:19px"></i></div>
        <div style="flex:1;min-width:0;display:flex;flex-direction:column"><span style="font-size:15px;font-weight:500">${esc(selA.host)}</span><span style="font-size:12px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(selA.ip)}</span></div>
        <button class="btn btn-icon btn-ghost" onclick="App.clearSel()" style="width:30px;height:30px"><i class="ph ph-x"></i></button>
      </div>
      <div style="display:flex;gap:6px;flex-wrap:wrap"><span class="tag tag-neutral">${esc(selA.type)}</span><span class="tag tag-neutral">${esc(selA.fam)}</span></div>
      <div style="display:flex;flex-direction:column">
        ${rows.map(([k, v]) => `<div style="display:flex;justify-content:space-between;gap:12px;padding:7px 0;font-size:12.5px;background:linear-gradient(to right,transparent,color-mix(in srgb,var(--color-text) 8%,transparent) 48px,color-mix(in srgb,var(--color-text) 8%,transparent) calc(100% - 48px),transparent) no-repeat bottom/100% 1px"><span style="color:var(--color-neutral-500)">${esc(k)}</span><span style="text-align:right">${esc(v)}</span></div>`).join("")}
      </div>
      <button class="btn btn-primary" onclick="App.openDrawer('${selA.id}')"><i class="ph ph-database"></i>Abrir item no CMDB</button>
    </div>`;
  } else if (foc) {
    const l = foc.list, tC = {}; l.forEach(a => tC[a.type] = (tC[a.type] || 0) + 1);
    const ports = l.reduce((n, a) => n + a.portCount, 0); const svc = new Set(); l.forEach(a => a.ports.forEach(p => svc.add(p.service || p.product || p.port)));
    const stats = [["Ativos", l.length], ["Portas", ports], ["Serviços", svc.size]];
    aside = `<div style="display:flex;flex-direction:column;gap:12px">
      <div style="display:flex;flex-direction:column;gap:2px"><span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-accent)">Sub-rede</span><span style="font-size:17px;font-weight:500">${esc(foc.name)}</span><span style="font-size:12px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(foc.cidr)}</span></div>
      <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:8px">
        ${stats.map(([k, v]) => `<div style="padding:10px;border-radius:var(--radius-md);background:var(--color-bg);display:flex;flex-direction:column;gap:2px"><span style="font-size:19px;font-weight:500">${v}</span><span style="font-size:11px;color:var(--color-neutral-500)">${esc(k)}</span></div>`).join("")}
      </div>
      <div style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">Composição</div>
      <div style="display:flex;flex-direction:column;gap:6px">
        ${Object.entries(tC).sort((a, b) => b[1] - a[1]).map(([name, count]) => `<div style="display:flex;align-items:center;gap:8px;font-size:12.5px"><i class="ph ${ICON[name]}" style="color:var(--color-accent-400)"></i><span style="flex:1">${esc(name)}</span><span>${count}</span></div>`).join("")}
      </div>
      <button class="btn btn-secondary" onclick='App.filterTo({"fSubnet":${JSON.stringify(foc.name)}})'><i class="ph ph-list-bullets"></i>Listar ativos da sub-rede</button>
    </div>`;
  } else {
    aside = `<div style="display:flex;flex-direction:column;gap:12px">
      <div style="display:flex;flex-direction:column;gap:2px"><span style="font-size:15px;font-weight:500">Topologia descoberta</span><span style="font-size:12.5px;color:var(--color-neutral-500);text-wrap:pretty">${ASSETS.length} ativos em ${SUBNETS.length} sub-rede(s). Clique numa sub-rede para ver os detalhes.</span></div>
      <div style="display:flex;flex-direction:column;max-height:60vh;overflow-y:auto">
        ${SUBNETS.map(sn => { const ports = sn.list.reduce((n, a) => n + a.portCount, 0); return `<button onclick='App.focusSubnet(${JSON.stringify(sn.id)})' style="display:flex;align-items:center;gap:10px;padding:10px 4px;border:0;background:linear-gradient(to right,transparent,color-mix(in srgb,var(--color-text) 8%,transparent) 48px,color-mix(in srgb,var(--color-text) 8%,transparent) calc(100% - 48px),transparent) no-repeat bottom/100% 1px;color:var(--color-text);font:inherit;cursor:pointer;text-align:left">
          <div style="flex:1;display:flex;flex-direction:column"><span style="font-size:13px;font-family:ui-monospace,monospace">${esc(sn.cidr)}</span><span style="font-size:11.5px;color:var(--color-neutral-500)">${sn.list.length} ativo(s) · ${ports} porta(s)</span></div>
          <i class="ph ph-caret-right" style="color:var(--color-neutral-500)"></i>
        </button>`; }).join("")}
      </div>
    </div>`;
  }

  return `<div style="display:flex;flex-direction:column;gap:14px">
    <div style="display:flex;flex-wrap:wrap;align-items:center;gap:12px">
      <div style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--color-neutral-500)">Colorir por
        <div style="display:inline-flex;border:1px solid var(--color-divider);border-radius:var(--radius-md);overflow:hidden">${seg([{ l: "Tipo", v: "tipo" }, { l: "SO", v: "so" }], s.colorBy, "setColorBy")}</div>
      </div>
      <div style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--color-neutral-500)">Destacar
        <div style="display:inline-flex;border:1px solid var(--color-divider);border-radius:var(--radius-md);overflow:hidden">${seg([{ l: "Nada", v: "none" }, { l: "Com portas abertas", v: "ports" }], s.hl, "setHl")}</div>
      </div>
      <button class="btn btn-secondary" onclick="App.toggleLinks()" style="font-size:12.5px"><i class="ph ${s.links ? "ph-eye" : "ph-eye-slash"}"></i>Ligações</button>
      ${foc || selA ? `<button class="btn btn-secondary" onclick="App.clearFocus()" style="font-size:12.5px"><i class="ph ph-arrows-out"></i>Visão geral</button>` : ""}
      <div style="margin-left:auto;display:flex;align-items:center;gap:6px">
        <button class="btn btn-secondary btn-icon" title="Diminuir zoom" onclick="App.zoomBy(1.25)"><i class="ph ph-minus"></i></button>
        <button class="btn btn-secondary btn-icon" title="Aumentar zoom" onclick="App.zoomBy(0.8)"><i class="ph ph-plus"></i></button>
        <button class="btn btn-secondary" style="font-size:12.5px" title="Ajustar à tela" onclick="App.zoomFit()"><i class="ph ph-corners-out"></i>Ajustar</button>
      </div>
    </div>
    <div style="display:flex;flex-wrap:wrap;gap:14px;align-items:flex-start">
      <div style="flex:999 1 620px;min-width:min(100%,560px);border-radius:var(--radius-md);background:radial-gradient(ellipse at 50% 20%,color-mix(in srgb,var(--color-accent) 7%,var(--color-surface)),var(--color-bg) 75%);box-shadow:var(--shadow-sm);overflow:hidden">
        <svg id="mapSvg" viewBox="${s.mapView ? `${s.mapView.x} ${s.mapView.y} ${s.mapView.w} ${s.mapView.h}` : `0 0 1240 ${MAP_H}`}" preserveAspectRatio="xMidYMid meet" style="width:100%;height:auto;max-height:74vh;display:block;cursor:grab;touch-action:none">
          <rect x="0" y="0" width="1240" height="${MAP_H}" onclick="App.clearSel()" style="fill:transparent"></rect>
          ${spokes.map(e => `<line x1="${e.x1}" y1="${e.y1}" x2="${e.x2}" y2="${e.y2}" style="stroke:var(--color-neutral-700);stroke-width:.7;opacity:${e.op}"></line>`).join("")}
          ${edges.map(e => `<line x1="${e.x1}" y1="${e.y1}" x2="${e.x2}" y2="${e.y2}" style="stroke:var(--color-accent-500);stroke-width:1.4;opacity:.6"></line>`).join("")}
          ${nodes.map(n => `<g onclick="App.selNode('${n.id}')" style="cursor:pointer;opacity:${n.op}">
            ${n.sel ? `<circle cx="${n.x}" cy="${n.y}" r="${n.halo}" style="fill:none;stroke:var(--color-accent-200);stroke-width:1.5"></circle>` : ""}
            <circle cx="${n.x}" cy="${n.y}" r="${n.r}" style="fill:${n.color};stroke:var(--color-bg);stroke-width:1"></circle>
          </g>`).join("")}
          <g onclick="App.clearFocus()" style="cursor:pointer">
            <circle cx="${core.x}" cy="${core.y}" r="30" style="fill:color-mix(in srgb,var(--color-accent) 14%,transparent)"></circle>
            <circle cx="${core.x}" cy="${core.y}" r="19" style="fill:var(--color-surface);stroke:var(--color-accent);stroke-width:1.5"></circle>
            <text x="${core.x}" y="${core.y + 4}" text-anchor="middle" style="fill:var(--color-accent-200);font-size:11px;font-weight:600;font-family:var(--font-body)">CORE</text>
          </g>
          ${SUBNETS.map(sn => `<g onclick='App.focusSubnet(${JSON.stringify(sn.id)})' style="cursor:pointer">
            <circle cx="${sn.x}" cy="${sn.y}" r="22" style="fill:color-mix(in srgb,var(--color-accent) 14%,transparent)"></circle>
            <circle cx="${sn.x}" cy="${sn.y}" r="14" style="fill:var(--color-surface);stroke:${s.focus === sn.id ? "var(--color-accent-200)" : "var(--color-accent-500)"};stroke-width:1.5"></circle>
            <text x="${sn.x}" y="${sn.y + sn.outer + 20}" text-anchor="middle" style="fill:var(--color-neutral-300);font-size:13px;font-family:ui-monospace,monospace">${esc(sn.cidr)}</text>
            <text x="${sn.x}" y="${sn.y + sn.outer + 36}" text-anchor="middle" style="fill:var(--color-neutral-600);font-size:11px;font-family:var(--font-body)">${sn.list.length} ativos</text>
          </g>`).join("")}
        </svg>
        <div style="display:flex;flex-wrap:wrap;align-items:center;gap:4px 14px;padding:10px 14px;background:linear-gradient(to right,transparent,var(--color-divider) 48px,var(--color-divider) calc(100% - 48px),transparent) no-repeat top/100% 1px">
          ${legend.map(([label, color]) => `<span style="display:flex;align-items:center;gap:6px;font-size:11.5px;color:var(--color-neutral-300)"><span style="width:8px;height:8px;border-radius:50%;background:${color}"></span>${esc(label)}</span>`).join("")}
          <span style="margin-left:auto;font-size:11.5px;color:var(--color-neutral-500)">Clique numa sub-rede ou num ativo</span>
        </div>
      </div>
      <aside style="flex:1 1 300px;min-width:280px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);padding:16px 18px;display:flex;flex-direction:column;gap:12px">${aside}</aside>
    </div>
  </div>`;
}

// ---------------- MUDANÇAS ----------------
function changeRow(c) {
  const k = CHANGE_KIND[c.kind] || { icon: "ph-dot", label: c.kind, tone: "var(--color-neutral-400)" };
  const a = byIP[c.host_ip];
  const when = new Date(c.ts).toLocaleString("pt-BR");
  return `<div ${a ? `onclick="App.openDrawer('${a.id}')" style="cursor:pointer"` : ""} style="display:flex;gap:12px;align-items:center;padding:10px 0;background:linear-gradient(to right,transparent,color-mix(in srgb,var(--color-text) 8%,transparent) 48px,color-mix(in srgb,var(--color-text) 8%,transparent) calc(100% - 48px),transparent) no-repeat bottom/100% 1px">
    <div style="width:30px;height:30px;flex:none;border-radius:var(--radius-md);display:grid;place-items:center;background:var(--color-bg);color:${k.tone}"><i class="ph ${k.icon}"></i></div>
    <div style="flex:1;min-width:0;display:flex;flex-direction:column"><span style="font-size:13px">${esc(c.detail)}</span><span style="font-size:12px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(c.host_ip)}${a && a.hostname ? " · " + esc(a.hostname) : ""}</span></div>
    <span style="font-size:11px;padding:2px 8px;border-radius:5px;background:var(--color-bg);color:var(--color-neutral-400)">${esc(k.label)}</span>
    <span style="font-size:11.5px;color:var(--color-neutral-500);white-space:nowrap">${when}</span>
  </div>`;
}

function viewChanges() {
  const kinds = {}; CHANGES.forEach(c => kinds[c.kind] = (kinds[c.kind] || 0) + 1);
  const chips = Object.entries(CHANGE_KIND).map(([k, v]) => `<div style="display:flex;align-items:center;gap:8px;padding:10px 14px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm)"><i class="ph ${v.icon}" style="color:${v.tone};font-size:16px"></i><span style="flex:1;font-size:12.5px;color:var(--color-neutral-300)">${v.label}</span><span style="font-size:15px;font-weight:500">${kinds[k] || 0}</span></div>`).join("");
  return `<div style="display:flex;flex-direction:column;gap:16px">
    <p style="margin:0;font-size:14px;color:var(--color-neutral-400);max-width:760px">Tudo que as varreduras detectaram mudar no inventário — novos hosts, nomes resolvidos, sistemas operacionais e portas que abriram ou fecharam.</p>
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px">${chips}</div>
    <section style="padding:14px 20px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);display:flex;flex-direction:column">
      <div style="display:flex;align-items:baseline;gap:10px;padding-bottom:6px"><h4 style="margin:0;font-size:16px">Linha do tempo</h4><span style="font-size:12px;color:var(--color-neutral-500)">${CHANGES.length} evento(s)</span></div>
      ${CHANGES.map(changeRow).join("") || `<span style="font-size:13px;color:var(--color-neutral-500);padding:16px 0">Nenhuma mudança registrada ainda. Rode uma varredura para começar a acompanhar.</span>`}
    </section>
  </div>`;
}

// ---------------- VARREDURA ----------------
function viewScan() {
  const s = state;
  const agent = AGENTS.find(a => String(a.id) === String(s.agentId)) || AGENTS[0];
  const running = agent && agent.scan_state === "running";
  const card = "padding:18px 20px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);display:flex;flex-direction:column;gap:16px";
  const suggested = agent ? agent.suggested_range || "" : "";
  const rangeVal = s.range || "";
  const targets = SUBNETS.map(sn => sn.cidr);
  // run progress
  let runHtml = "";
  if (running || (agent && agent.scan_phase)) {
    const phases = ["ping", "hostname", "ports", "detect"];
    const phaseLabels = { ping: "Ping (ICMP)", hostname: "Hostname (DNS)", ports: "Portas (TCP)", detect: "SO e plataformas" };
    const cur = phases.indexOf(agent.scan_phase);
    const pct = running ? Math.round((cur + 1) / 4 * 100) : 100;
    const started = agent.scan_started_at && !agent.scan_started_at.startsWith("0001") ? new Date(agent.scan_started_at) : null;
    const elapsed = started ? fmtElapsed(Date.now() - started.getTime()) : "—";
    runHtml = `<div style="display:flex;flex-direction:column;gap:14px">
      <div style="display:flex;align-items:center;gap:10px">
        ${running ? `<span style="width:8px;height:8px;border-radius:50%;background:var(--color-accent);box-shadow:0 0 10px var(--color-accent);animation:pulse 1.2s infinite"></span>` : ""}
        <h4 style="margin:0;font-size:16px;flex:1">${esc(agent.name)}</h4>
        ${running ? `<button class="btn btn-secondary" onclick="App.stopScan()" style="font-size:12.5px">Cancelar</button>` : `<button class="btn btn-ghost" onclick="App.go('cmdb')" style="font-size:12.5px">Ver no CMDB<i class="ph ph-arrow-right"></i></button>`}
      </div>
      <div style="display:flex;flex-direction:column;gap:6px">
        <div style="display:flex;justify-content:space-between;font-size:12px;color:var(--color-neutral-400)"><span>${running ? esc(phaseLabels[agent.scan_phase] || "Varrendo…") : "Concluída"} · ${esc(agent.scan_range || (agent.ranges || []).join(", "))}</span><span>${pct}% · ${elapsed}</span></div>
        <div style="height:6px;border-radius:3px;background:var(--color-neutral-900);overflow:hidden"><div style="height:100%;width:${pct}%;background:var(--color-accent);box-shadow:0 0 12px var(--color-accent);transition:width .2s"></div></div>
      </div>
      <div style="display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px">
        ${[["Hosts encontrados", agent.scan_hosts || 0, "var(--color-text)"], ["Fase", (cur + 1) + "/4", "var(--color-accent-300)"], ["Range", (agent.scan_range || "").split(",").length, "var(--color-text)"], ["Estado", running ? "ativo" : "idle", running ? WARN : "var(--color-neutral-400)"]].map(([k, v, c]) => `<div style="padding:10px 12px;border-radius:var(--radius-md);background:var(--color-bg);display:flex;flex-direction:column;gap:2px"><span style="font-size:20px;font-weight:500;color:${c}">${v}</span><span style="font-size:11px;color:var(--color-neutral-500)">${esc(k)}</span></div>`).join("")}
      </div>
    </div>`;
  } else {
    runHtml = `<div style="display:flex;flex-direction:column;gap:12px">
      <h4 style="margin:0;font-size:16px">Coletores</h4>
      ${AGENTS.map(a => `<div style="display:flex;align-items:center;gap:12px;padding:10px 0;background:linear-gradient(to right,transparent,color-mix(in srgb,var(--color-text) 8%,transparent) 48px,color-mix(in srgb,var(--color-text) 8%,transparent) calc(100% - 48px),transparent) no-repeat bottom/100% 1px">
        <div style="width:32px;height:32px;border-radius:var(--radius-md);display:grid;place-items:center;background:var(--color-bg);color:var(--color-accent-400)"><i class="ph ph-cpu"></i></div>
        <div style="flex:1;display:flex;flex-direction:column"><span style="font-size:13px">${esc(a.name)}</span><span style="font-size:12px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(a.ip || "—")}</span></div>
        <div style="display:flex;flex-direction:column;align-items:flex-end"><span style="font-size:12.5px;color:${a.online ? "var(--color-accent-400)" : "var(--color-neutral-500)"}">${a.online ? "online" : "offline"}</span><span style="font-size:11.5px;color:var(--color-neutral-500)">${a.last_scan_at && !a.last_scan_at.startsWith("0001") ? new Date(a.last_scan_at).toLocaleTimeString("pt-BR") : "nunca"}</span></div>
      </div>`).join("") || `<span style="font-size:12px;color:var(--color-neutral-500)">Nenhum coletor registrado.</span>`}
      <div style="font-size:12px;color:var(--color-neutral-500)">Nenhuma varredura em execução. Configure o escopo ao lado e inicie.</div>
    </div>`;
  }
  return `<div style="display:flex;flex-direction:column;gap:14px">
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(380px,1fr));gap:14px;align-items:start">
      <section style="${card}">
        <h4 style="margin:0;font-size:16px">Nova varredura</h4>
        <div class="field"><label>Coletor</label><select class="input" onchange="App.setAgent(this.value)">${AGENTS.map(a => `<option value="${a.id}" ${agent && String(a.id) === String(agent.id) ? "selected" : ""}>${esc(a.name)} — ${esc(a.ip || "")}</option>`).join("") || `<option>Nenhum coletor</option>`}</select></div>
        <div class="field"><label>IP do coletor</label><div style="font-size:14px;font-weight:500">${agent ? esc(agent.ip || "—") : "—"}</div></div>
        <div class="field"><label>Escopo · range a varrer (CIDR, separado por vírgula)</label><input class="input" id="rangeField" value="${esc(rangeVal)}" placeholder="${esc(suggested || "ex: 192.168.0.0/24")}" oninput="App.onRange(this.value)"></div>
        ${suggested ? `<div class="field"><label>Sugestão (sub-rede do coletor)</label><div style="display:flex;flex-wrap:wrap;gap:6px"><button onclick="App.useRange('${esc(suggested)}')" style="display:flex;flex-direction:column;align-items:flex-start;padding:7px 11px;border-radius:var(--radius-md);border:1px solid var(--color-divider);background:transparent;color:var(--color-text);font:inherit;cursor:pointer"><span style="font-size:12.5px">Coletor</span><span style="font-size:11px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(suggested)}</span></button></div></div>` : ""}
        ${targets.length ? `<div class="field"><label>Sub-redes descobertas</label><div style="display:flex;flex-wrap:wrap;gap:6px">${targets.map(t => `<button onclick="App.useRange('${esc(t)}')" style="display:flex;flex-direction:column;align-items:flex-start;padding:7px 11px;border-radius:var(--radius-md);border:1px solid var(--color-divider);background:transparent;color:var(--color-text);font:inherit;cursor:pointer"><span style="font-size:11px;color:var(--color-neutral-500);font-family:ui-monospace,monospace">${esc(t)}</span></button>`).join("")}</div></div>` : ""}
        <div style="display:flex;align-items:center;gap:12px;flex-wrap:wrap">
          <button class="btn btn-primary" onclick="App.startScan()" ${!agent || running ? "disabled" : ""}><i class="ph ph-play"></i>Iniciar varredura</button>
          <span style="font-size:12px;color:var(--color-neutral-500)">${running ? "Varredura em execução…" : agent ? "Varre o range informado (vazio = escopo do coletor)" : "Selecione um coletor"}</span>
        </div>
      </section>
      <section style="${card.replace("gap:16px", "gap:14px")}">${runHtml}</section>
    </div>
    <section style="padding:18px 20px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);display:flex;flex-direction:column;gap:14px">
      <div style="display:flex;align-items:baseline;gap:10px"><h4 style="margin:0;font-size:16px">Instalação do coletor</h4><span style="font-size:12px;color:var(--color-neutral-500)">um token por instalação</span></div>
      <div style="display:flex;flex-wrap:wrap;gap:12px 28px;align-items:center">
        <div style="display:flex;flex-direction:column;gap:5px">
          <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">Sistemas aceitos</span>
          <div style="display:flex;gap:6px;flex-wrap:wrap"><span class="tag tag-accent"><i class="ph ph-linux-logo"></i>&nbsp;Linux (x86-64 / ARM64)</span></div>
        </div>
        <div style="display:flex;flex-direction:column;gap:5px">
          <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">Requisitos</span>
          <span style="font-size:12.5px;color:var(--color-neutral-300)">Python 3 · root / <code style="font-family:ui-monospace,monospace">CAP_NET_RAW</code></span>
        </div>
        <button class="btn btn-primary" style="margin-left:auto" onclick="App.newInstall()"><i class="ph ph-plus-circle"></i>Instalar coletor</button>
      </div>
      ${state.installToken ? `
      <div style="border-top:1px solid var(--color-divider);padding-top:14px;display:flex;flex-wrap:wrap;gap:28px;align-items:flex-start">
        <div style="display:flex;flex-direction:column;gap:12px;min-width:260px">
          <div style="display:flex;flex-direction:column;gap:5px">
            <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">1 · Baixar o agente</span>
            <a class="btn btn-secondary" href="/ntm_agent.py" download="ntm_agent.py" style="align-self:flex-start"><i class="ph ph-download-simple"></i>Baixar agente (ntm_agent.py)</a>
          </div>
          <div style="display:flex;flex-direction:column;gap:5px">
            <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">2 · Conexão</span>
            <div style="display:flex;justify-content:space-between;gap:12px;font-size:12.5px"><span style="color:var(--color-neutral-500)">Plataforma</span><span style="font-family:ui-monospace,monospace">${esc(location.origin)}</span></div>
            <div style="display:flex;justify-content:space-between;gap:12px;font-size:12.5px"><span style="color:var(--color-neutral-500)">TLS</span><span>self-signed (aceite o aviso)</span></div>
          </div>
          <div style="display:flex;flex-direction:column;gap:5px">
            <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">Token desta instalação</span>
            <div style="display:flex;align-items:center;gap:8px">
              <code style="font-family:ui-monospace,monospace;font-size:12.5px;background:var(--color-bg);padding:7px 10px;border-radius:var(--radius-md);color:var(--color-accent-200);user-select:all;word-break:break-all">${esc(state.installToken)}</code>
              <button class="btn btn-secondary btn-icon" title="Copiar token" onclick="App.copy(${JSON.stringify(state.installToken)})"><i class="ph ph-copy"></i></button>
            </div>
            <span style="font-size:11px;color:var(--color-neutral-600)">Único para este coletor. Gere um novo para cada host.</span>
          </div>
        </div>
        <div style="flex:1;min-width:320px;display:flex;flex-direction:column;gap:6px">
          <span style="font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--color-neutral-500)">3 · Instalar (host Linux)</span>
          <pre style="margin:0;background:var(--color-bg);border-radius:var(--radius-md);padding:12px 14px;font-family:ui-monospace,monospace;font-size:12px;color:var(--color-neutral-300);overflow-x:auto;white-space:pre;line-height:1.6">sudo apt-get install -y python3
export NTM_PLATFORM_URL=${esc(location.origin)}
export NTM_INSTALL_TOKEN=${esc(state.installToken)}
sudo -E python3 ntm_agent.py</pre>
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
            <button class="btn btn-secondary" onclick="App.copy(${JSON.stringify("sudo apt-get install -y python3\nexport NTM_PLATFORM_URL=")} + location.origin + ${JSON.stringify("\nexport NTM_INSTALL_TOKEN=" + "")} + ${JSON.stringify(state.installToken)} + ${JSON.stringify("\nsudo -E python3 ntm_agent.py")})"><i class="ph ph-copy"></i>Copiar comandos</button>
            <span style="font-size:11.5px;color:var(--color-neutral-500)">O range a varrer é definido aqui na plataforma (campo Escopo). Em produção, instale como serviço (systemd).</span>
          </div>
        </div>
      </div>` : `<span style="font-size:12px;color:var(--color-neutral-500)">Clique em <strong style="color:var(--color-neutral-300);font-weight:500">Instalar coletor</strong> para gerar um token único e ver os passos de download, conexão e instalação.</span>`}
    </section>
    <section style="padding:14px 18px;border-radius:var(--radius-md);background:var(--color-surface);box-shadow:var(--shadow-sm);display:flex;flex-direction:column;gap:6px;overflow-x:auto">
      <h4 style="margin:0;font-size:16px">Coletores registrados</h4>
      <table class="table" style="font-size:13px;min-width:600px">
        <thead><tr><th>Coletor</th><th>IP</th><th>Escopo</th><th>Último scan</th><th>Hosts</th><th>Estado</th><th></th></tr></thead>
        <tbody>
          ${AGENTS.map(a => `<tr>
            <td style="font-weight:500">${esc(a.name)}</td><td style="color:var(--color-neutral-300);font-family:ui-monospace,monospace">${esc(a.ip || "—")}</td>
            <td style="color:var(--color-neutral-300)">${esc((a.ranges || []).join(", "))}</td>
            <td style="color:var(--color-neutral-300)">${a.last_scan_at && !a.last_scan_at.startsWith("0001") ? new Date(a.last_scan_at).toLocaleString("pt-BR") : "nunca"}</td>
            <td>${a.scan_hosts || 0}</td>
            <td><span style="display:flex;align-items:center;gap:7px"><span style="width:7px;height:7px;border-radius:50%;background:${a.scan_state === "running" ? WARN : a.online ? "var(--color-accent-400)" : "var(--color-neutral-600)"}"></span>${a.scan_state === "running" ? "varrendo" : a.online ? "online" : "offline"}</span></td>
            <td><button class="btn btn-ghost" onclick="App.removeAgent('${a.id}')" style="font-size:12px;color:${CRIT}">Remover</button></td>
          </tr>`).join("")}
        </tbody>
      </table>
    </section>
  </div>`;
}

function fmtElapsed(ms) {
  const s = Math.max(0, Math.floor(ms / 1000)), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  const mmss = `${String(m).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
  return h > 0 ? `${h}:${mmss}` : mmss;
}

// ============================ ACTIONS ============================
const App = {
  go(screen, extra) { localStorage.setItem("atlas.screen", screen); Object.assign(state, { screen, drawer: null }, extra || {}); render(); },
  filterTo(f) { Object.assign(state, { screen: "cmdb", search: "", fType: "Todos", fFam: "Todas", fSubnet: "Todas", fStatus: "Todos", fEol: false, stale: false, limit: 40, drawer: null }, f || {}); localStorage.setItem("atlas.screen", "cmdb"); render(); },
  setFilter(k, v) { state[k] = v; state.limit = 40; render(); },
  setStatus(v) { state.fStatus = v; state.limit = 40; render(); },
  setColorBy(v) { state.colorBy = v; render(); },
  setHl(v) { state.hl = v; render(); },
  toggleEol() { state.fEol = !state.fEol; state.limit = 40; render(); },
  toggleLinks() { state.links = !state.links; render(); },
  clearFilters() { Object.assign(state, { search: "", fType: "Todos", fFam: "Todas", fSubnet: "Todas", fStatus: "Todos", fEol: false, stale: false }); render(); },
  clearFocus() { state.focus = null; state.mapSel = null; state.mapView = null; render(); },
  clearSel() { state.mapSel = null; render(); },
  selNode(id) { state.mapSel = id; render(); },
  focusSubnet(id) { state.focus = id; state.mapSel = null; const sn = SUBNETS.find(x => x.id === id); if (sn) { const w = 520, h = w * (MAP_H / 1240); state.mapView = { x: sn.x - w / 2, y: sn.y - h / 2, w, h }; } render(); },
  zoomBy(f) { const v = state.mapView || { x: 0, y: 0, w: 1240, h: MAP_H }; const cx = v.x + v.w / 2, cy = v.y + v.h / 2; let nw = Math.min(1240, Math.max(180, v.w * f)), nh = nw * (MAP_H / 1240); state.mapView = { x: cx - nw / 2, y: cy - nh / 2, w: nw, h: nh }; render(); },
  zoomFit() { state.mapView = null; render(); },
  showMore() { state.limit += 40; render(); },
  openDrawer(id) { state.drawer = id; render(); },
  closeDrawer() { state.drawer = null; render(); },
  async newInstall() {
    try { const r = await fetch(`${API}/install-tokens`, { method: "POST" }); const d = await r.json(); state.installToken = d.install_token || null; } catch (e) { console.error(e); alert("Falha ao gerar instalação."); return; }
    state.screen = "scan"; render();
  },
  copy(txt) { if (!txt) return; (navigator.clipboard ? navigator.clipboard.writeText(txt) : Promise.reject()).catch(() => { const t = document.createElement("textarea"); t.value = txt; document.body.appendChild(t); t.select(); try { document.execCommand("copy"); } catch (e) {} t.remove(); }); },
  setAgent(id) { state.agentId = id; render(); },
  onRange(v) { state.range = v; },
  useRange(v) { state.range = v; const f = document.getElementById("rangeField"); if (f) f.value = v; },
  rescan(id) { const a = byId[id]; state.drawer = null; state.screen = "scan"; state.range = a ? a.subnetName : ""; render(); },
  exportCsv() {
    const rows = filtered();
    const head = ["CI", "IP", "Hostname", "MAC", "Tipo", "SO", "Sub-rede", "Portas abertas"];
    const csv = [head, ...rows.map(a => [a.ci, a.ip, a.hostname, a.mac, a.type, a.os, a.subnetName, a.ports.map(p => p.port + (p.service ? "/" + p.service : "")).join(" ")])].map(r => r.map(x => `"${x}"`).join(";")).join("\n");
    const el = document.createElement("a");
    el.href = URL.createObjectURL(new Blob(["﻿" + csv], { type: "text/csv" }));
    el.download = "cmdb-ativos.csv"; el.click();
  },
  async startScan() {
    const agent = AGENTS.find(a => String(a.id) === String(state.agentId)) || AGENTS[0];
    if (!agent) return;
    try {
      await fetch(`${API}/agents/${agent.id}/run`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ range: (state.range || "").trim() }) });
      setTimeout(refresh, 800);
    } catch (e) { console.error(e); }
  },
  async stopScan() {
    const agent = AGENTS.find(a => String(a.id) === String(state.agentId)) || AGENTS[0];
    if (!agent) return;
    try { await fetch(`${API}/agents/${agent.id}/stop`, { method: "POST" }); setTimeout(refresh, 800); } catch (e) { console.error(e); }
  },
  async removeAgent(id) {
    const a = AGENTS.find(x => String(x.id) === String(id));
    if (!a || !confirm(`Remover o coletor ${a.name}?\n\nOs ativos descobertos são mantidos.`)) return;
    try { await fetch(`${API}/agents/${id}`, { method: "DELETE" }); await refresh(); } catch (e) { console.error(e); }
  },
};
window.App = App;

// global search in header
function wireHeader() {
  document.getElementById("globalSearch").addEventListener("input", e => {
    state.search = e.target.value;
    if (state.screen !== "map") { state.screen = "cmdb"; state.limit = 40; localStorage.setItem("atlas.screen", "cmdb"); }
    render();
    const f = document.getElementById("globalSearch"); if (f) { f.focus(); const v = f.value; f.value = ""; f.value = v; }
  });
  document.getElementById("goScan").addEventListener("click", () => App.go("scan"));
}

async function refresh() {
  try { await loadAssets(); } catch (e) { console.error(e); }
  render();
}

// live refresh (agents/scan progress) without disturbing typing
setInterval(() => {
  if (state.screen === "scan" || AGENTS.some(a => a.scan_state === "running")) refresh();
}, 4000);

async function boot() {
  wireHeader();
  await refresh();
}
document.addEventListener("DOMContentLoaded", boot);
if (document.readyState !== "loading") boot();
