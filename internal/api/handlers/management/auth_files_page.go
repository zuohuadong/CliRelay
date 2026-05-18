package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const authFilesPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Auth Files Manager</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0a0e17;--surface:#111827;--surface2:#1a2235;--border:#1e293b;
  --text:#e2e8f0;--text2:#94a3b8;--text3:#64748b;
  --accent:#3b82f6;--accent2:#2563eb;
  --green:#10b981;--red:#ef4444;--amber:#f59e0b;--purple:#8b5cf6;--cyan:#06b6d4;
  --radius:8px;
}
html{font-size:14px}
body{font-family:'SF Mono','Fira Code','Cascadia Code',monospace;background:var(--bg);color:var(--text);min-height:100vh;overflow-x:hidden}
a{color:var(--accent);text-decoration:none}

header{
  position:sticky;top:0;z-index:100;
  background:rgba(10,14,23,.85);backdrop-filter:blur(20px);
  border-bottom:1px solid var(--border);
  padding:12px 24px;display:flex;align-items:center;justify-content:space-between;
}
header h1{font-size:1rem;font-weight:600;letter-spacing:-.02em}
header .meta{display:flex;gap:12px;align-items:center;font-size:.75rem;color:var(--text3)}
header .meta .dot{width:6px;height:6px;border-radius:50%;background:var(--green);animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}

.toolbar{
  padding:12px 24px;display:flex;gap:10px;align-items:center;
  border-bottom:1px solid var(--border);background:var(--surface);
  flex-wrap:wrap;
}
.search-box{
  flex:1;min-width:200px;max-width:400px;
  background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);
  padding:8px 12px;color:var(--text);font-family:inherit;font-size:.85rem;
  outline:none;transition:border-color .2s;
}
.search-box:focus{border-color:var(--accent)}
.filter-btn{
  padding:6px 14px;border-radius:var(--radius);border:1px solid var(--border);
  background:transparent;color:var(--text2);cursor:pointer;font-size:.75rem;
  font-family:inherit;transition:all .2s;
}
.filter-btn:hover,.filter-btn.active{background:var(--accent);color:#fff;border-color:var(--accent)}
.refresh-btn{
  padding:6px 14px;border-radius:var(--radius);border:1px solid var(--border);
  background:transparent;color:var(--text2);cursor:pointer;font-size:.75rem;
  font-family:inherit;transition:all .2s;margin-left:auto;
}
.refresh-btn:hover{background:var(--surface2);color:var(--text)}
.add-btn{
  padding:6px 14px;border-radius:var(--radius);border:1px solid var(--accent);
  background:var(--accent);color:#fff;cursor:pointer;font-size:.75rem;
  font-family:inherit;transition:all .2s;margin-left:6px;
}
.add-btn:hover{background:var(--accent2)}

.stats-bar{
  padding:8px 24px;display:flex;gap:20px;font-size:.75rem;color:var(--text3);
  border-bottom:1px solid var(--border);background:rgba(17,24,39,.5);
}
.stats-bar .stat-val{color:var(--text);font-weight:600;margin-left:4px}

.list{padding:12px 16px}
.card{
  background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);
  padding:14px 18px;margin-bottom:8px;display:grid;
  grid-template-columns:auto 1fr auto;
  gap:12px 16px;align-items:center;
  transition:all .2s;cursor:default;
}
.card:hover{border-color:var(--accent);background:var(--surface2)}
.card.disabled-card{opacity:.5}
.card.disabled-card:hover{opacity:.7}

.card .status-dot{width:10px;height:10px;border-radius:50%;justify-self:center;flex-shrink:0}
.card .status-dot.active{background:var(--green);box-shadow:0 0 8px rgba(16,185,129,.4)}
.card .status-dot.error{background:var(--red);box-shadow:0 0 8px rgba(239,68,68,.4)}
.card .status-dot.disabled{background:var(--text3)}
.card .status-dot.cooling{background:var(--amber);box-shadow:0 0 8px rgba(245,158,11,.4)}

.card .info{min-width:0}
.card .info .name{font-weight:600;font-size:.9rem;margin-bottom:3px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .info .name .provider-badge{
  display:inline-block;font-size:.65rem;padding:2px 8px;border-radius:10px;
  background:rgba(139,92,246,.15);color:var(--purple);margin-left:8px;
  font-weight:500;vertical-align:middle;
}
.card .info .email{font-size:.75rem;color:var(--text3);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .info .status-msg{font-size:.7rem;color:var(--amber);margin-top:2px}

.card .actions{display:flex;gap:6px;align-items:center;flex-shrink:0}
.card .metric{text-align:center;min-width:50px}
.card .metric .val{font-size:1rem;font-weight:700;color:var(--text)}
.card .metric .lbl{font-size:.6rem;color:var(--text3);text-transform:uppercase;letter-spacing:.05em}

.action-btn{
  width:28px;height:28px;border-radius:6px;border:1px solid var(--border);
  background:transparent;color:var(--text3);cursor:pointer;
  display:inline-flex;align-items:center;justify-content:center;
  font-size:.75rem;transition:all .15s;
}
.action-btn:hover{background:var(--surface2);color:var(--text)}
.action-btn.enable:hover{color:var(--green);border-color:var(--green)}
.action-btn.disable:hover{color:var(--amber);border-color:var(--amber)}
.action-btn.delete:hover{color:var(--red);border-color:var(--red)}

.empty-state{text-align:center;padding:60px 20px;color:var(--text3)}
.empty-state .icon{font-size:3rem;margin-bottom:12px;opacity:.3}
.empty-state p{font-size:.9rem;margin-bottom:4px}

.toast{
  position:fixed;bottom:24px;right:24px;z-index:999;
  padding:10px 20px;border-radius:var(--radius);
  background:var(--surface2);border:1px solid var(--border);
  color:var(--text);font-size:.8rem;
  transform:translateY(100px);opacity:0;transition:all .3s;
}
.toast.show{transform:translateY(0);opacity:1}
.toast.success{border-color:var(--green)}
.toast.error{border-color:var(--red)}

.loading{display:flex;justify-content:center;padding:40px;color:var(--text3)}
.loading::after{content:'';width:20px;height:20px;border:2px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:spin .8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}

.modal-overlay{
  position:fixed;inset:0;z-index:200;background:rgba(0,0,0,.6);backdrop-filter:blur(4px);
  display:flex;align-items:center;justify-content:center;
}
.modal{
  background:var(--surface);border:1px solid var(--border);border-radius:12px;
  padding:24px;max-width:520px;width:92%;max-height:85vh;overflow-y:auto;
}
.modal h2{font-size:1rem;margin-bottom:16px;font-weight:600}
.modal-section{margin-bottom:16px}
.modal-section h3{font-size:.8rem;color:var(--text2);margin-bottom:8px;text-transform:uppercase;letter-spacing:.05em}
.modal-close{
  float:right;background:none;border:none;color:var(--text3);cursor:pointer;
  font-size:1.2rem;padding:0 4px;
}
.modal-close:hover{color:var(--text)}

.oauth-grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
.oauth-btn{
  padding:10px 12px;border-radius:var(--radius);border:1px solid var(--border);
  background:var(--surface2);color:var(--text);cursor:pointer;font-family:inherit;
  font-size:.8rem;text-align:left;transition:all .15s;
}
.oauth-btn:hover{border-color:var(--accent);background:var(--bg)}
.oauth-btn .oauth-icon{font-size:1.1rem;margin-right:6px}
.oauth-btn .oauth-label{display:block;font-size:.65rem;color:var(--text3);margin-top:2px}

.upload-zone{
  border:2px dashed var(--border);border-radius:var(--radius);
  padding:24px;text-align:center;cursor:pointer;transition:all .2s;
  color:var(--text3);font-size:.85rem;
}
.upload-zone:hover,.upload-zone.dragover{border-color:var(--accent);color:var(--text);background:rgba(59,130,246,.05)}
.upload-zone input{display:none}

.compat-form{display:flex;flex-direction:column;gap:8px}
.compat-form input,.compat-form textarea{
  background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);
  padding:8px 12px;color:var(--text);font-family:inherit;font-size:.8rem;
  outline:none;transition:border-color .2s;
}
.compat-form input:focus,.compat-form textarea:focus{border-color:var(--accent)}
.compat-form textarea{min-height:60px;resize:vertical}
.compat-form label{font-size:.75rem;color:var(--text2)}
.compat-form .form-btn{
  padding:8px 16px;border-radius:var(--radius);border:none;
  background:var(--accent);color:#fff;cursor:pointer;font-family:inherit;
  font-size:.8rem;transition:all .15s;align-self:flex-start;
}
.compat-form .form-btn:hover{background:var(--accent2)}

.oauth-status{
  margin-top:12px;padding:12px;border-radius:var(--radius);
  background:var(--bg);border:1px solid var(--border);font-size:.8rem;
  text-align:center;color:var(--text2);
}
.oauth-status.waiting{border-color:var(--amber);color:var(--amber)}
.oauth-status.done{border-color:var(--green);color:var(--green)}
.oauth-status.fail{border-color:var(--red);color:var(--red)}

@keyframes spin{to{transform:rotate(360deg)}}
@media(max-width:640px){
  header{padding:10px 16px}
  .toolbar{padding:10px 16px}
  .stats-bar{padding:8px 16px;flex-wrap:wrap;gap:10px}
  .list{padding:8px}
  .card{grid-template-columns:auto 1fr;gap:8px 12px;padding:12px}
  .card .actions{grid-column:1/-1;justify-content:flex-end}
  .oauth-grid{grid-template-columns:1fr}
}
</style>
</head>
<body>
<header>
  <h1>Auth Files</h1>
  <div class="meta"><span class="dot"></span><span id="updateTime">--</span></div>
</header>

<div class="toolbar">
  <input class="search-box" id="search" placeholder="Search name / email / provider..." autocomplete="off">
  <button class="filter-btn active" data-filter="all">All</button>
  <button class="filter-btn" data-filter="active">Active</button>
  <button class="filter-btn" data-filter="error">Error</button>
  <button class="filter-btn" data-filter="disabled">Disabled</button>
  <button class="refresh-btn" id="refreshBtn">&#x21BB; Refresh</button>
  <button class="add-btn" id="addBtn">+ Add</button>
</div>

<div class="stats-bar" id="statsBar"></div>
<div class="list" id="list"><div class="loading"></div></div>
<div class="toast" id="toast"></div>

<script>
const API_BASE = '/v0/management';
let TOKEN = '';
let allFiles = [];
let currentFilter = 'all';
let searchQuery = '';
let oauthPollTimer = null;

function getToken() {
  const p = new URLSearchParams(window.location.search);
  return p.get('token') || '';
}

function apiUrl(path) {
  const base = API_BASE + path;
  const sep = base.includes('?') ? '&' : '?';
  return base + sep + 'token=' + encodeURIComponent(TOKEN);
}

async function api(path, opts = {}) {
  const url = apiUrl(path);
  const headers = { 'Authorization': 'Bearer ' + TOKEN };
  if (opts.body && !(opts.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch(url, { ...opts, headers });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(err.error || err.message || res.statusText);
  }
  return res.json();
}

function showToast(msg, type = 'success') {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast show ' + type;
  setTimeout(() => t.className = 'toast', 3000);
}

function escHtml(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function escAttr(s) { return (s || '').replace(/"/g, '&quot;'); }

function statusClass(auth) {
  if (auth.disabled) return 'disabled';
  if (auth.status === 'error' || auth.status === 'unavailable') return 'error';
  if (auth.status === 'cooling' || auth.next_retry_after) return 'cooling';
  return 'active';
}

function statusLabel(auth) {
  if (auth.disabled) return 'Disabled';
  if (auth.status === 'error') return 'Error';
  if (auth.status === 'cooling') return 'Cooling';
  if (auth.status === 'active') return 'Active';
  return auth.status || '--';
}

function renderCard(auth) {
  const sc = statusClass(auth);
  const pn = auth.provider_name || auth.provider || '--';
  const email = auth.email || auth.name || auth.id;
  const sm = auth.status_message ? '<div class="status-msg">' + escHtml(auth.status_message) + '</div>' : '';
  const disabled = auth.disabled;
  let actions = '';
  if (disabled) {
    actions = '<button class="action-btn enable" title="Enable" data-action="enable" data-idx="' + auth.auth_index + '">&#x25B6;</button>';
  } else {
    actions = '<button class="action-btn disable" title="Disable" data-action="disable" data-idx="' + auth.auth_index + '">&#x23F8;</button>';
  }
  actions += '<button class="action-btn delete" title="Delete" data-action="delete" data-idx="' + auth.auth_index + '" data-name="' + escAttr(email) + '">&#x2715;</button>';
  return '<div class="card' + (disabled ? ' disabled-card' : '') + '">' +
    '<div class="status-dot ' + sc + '" title="' + statusLabel(auth) + '"></div>' +
    '<div class="info">' +
      '<div class="name">' + escHtml(email) + '<span class="provider-badge">' + escHtml(pn) + '</span></div>' +
      (auth.base_url ? '<div class="email">' + escHtml(auth.base_url) + '</div>' : '') +
      sm +
    '</div>' +
    '<div class="actions">' +
      '<div class="metric"><div class="val">' + (auth.success || 0) + '</div><div class="lbl">OK</div></div>' +
      '<div class="metric"><div class="val">' + (auth.failed || 0) + '</div><div class="lbl">Fail</div></div>' +
      actions +
    '</div>' +
  '</div>';
}

function renderList() {
  const list = document.getElementById('list');
  let filtered = allFiles;
  if (currentFilter !== 'all') {
    filtered = filtered.filter(a => statusClass(a) === currentFilter);
  }
  if (searchQuery) {
    const q = searchQuery.toLowerCase();
    filtered = filtered.filter(a =>
      [a.name, a.email, a.provider_name, a.provider, a.auth_index, a.base_url]
        .some(v => (v || '').toLowerCase().includes(q))
    );
  }
  if (filtered.length === 0) {
    list.innerHTML = '<div class="empty-state"><div class="icon">&#x1F4C2;</div><p>No auth files found</p></div>';
    return;
  }
  list.innerHTML = filtered.map(renderCard).join('');
}

function renderStats() {
  const total = allFiles.length;
  let active = 0, error = 0, disabled = 0;
  allFiles.forEach(a => {
    const sc = statusClass(a);
    if (sc === 'active') active++;
    else if (sc === 'error') error++;
    else if (sc === 'disabled') disabled++;
  });
  const ok = allFiles.reduce((s, a) => s + (a.success || 0), 0);
  const fail = allFiles.reduce((s, a) => s + (a.failed || 0), 0);
  document.getElementById('statsBar').innerHTML =
    '<span>Total<span class="stat-val">' + total + '</span></span>' +
    '<span>Active<span class="stat-val" style="color:var(--green)">' + active + '</span></span>' +
    '<span>Error<span class="stat-val" style="color:var(--red)">' + error + '</span></span>' +
    '<span>Disabled<span class="stat-val" style="color:var(--text3)">' + disabled + '</span></span>' +
    '<span>Calls OK<span class="stat-val">' + ok + '</span></span>' +
    '<span>Calls Fail<span class="stat-val">' + fail + '</span></span>';
}

async function loadAuthFiles() {
  try {
    const data = await api('/auth-files');
    allFiles = Array.isArray(data) ? data : (data.files || data.data || []);
    allFiles.sort((a, b) => {
      const order = { error: 0, active: 1, cooling: 2, disabled: 3 };
      return (order[statusClass(a)] || 9) - (order[statusClass(b)] || 9);
    });
    renderStats();
    renderList();
    document.getElementById('updateTime').textContent = new Date().toLocaleTimeString();
  } catch (e) {
    document.getElementById('list').innerHTML = '<div class="empty-state"><div class="icon">&#x26A0;</div><p>' + escHtml(e.message) + '</p></div>';
  }
}

async function toggleAuth(authIndex, enable) {
  try {
    await api('/auth-files/status', { method: 'PATCH', body: { auth_index: authIndex, disabled: !enable } });
    showToast(enable ? 'Enabled' : 'Disabled');
    await loadAuthFiles();
  } catch (e) { showToast(e.message, 'error'); }
}

async function deleteAuth(authIndex, name) {
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  overlay.innerHTML = '<div class="modal" style="max-width:360px;text-align:center"><h2>Confirm Delete</h2><p style="font-size:.85rem;color:var(--text2);margin-bottom:16px">Delete "' + escHtml(name) + '"?</p><div style="display:flex;gap:10px;justify-content:center"><button class="modal-cancel-btn" style="padding:8px 20px;border-radius:var(--radius);border:1px solid var(--border);background:transparent;color:var(--text2);cursor:pointer;font-family:inherit">Cancel</button><button class="modal-confirm-btn" style="padding:8px 20px;border-radius:var(--radius);border:none;background:var(--red);color:#fff;cursor:pointer;font-family:inherit">Delete</button></div></div>';
  document.body.appendChild(overlay);
  overlay.querySelector('.modal-cancel-btn').onclick = () => overlay.remove();
  overlay.querySelector('.modal-confirm-btn').onclick = async () => {
    overlay.remove();
    try {
      await api('/auth-files?auth_index=' + encodeURIComponent(authIndex), { method: 'DELETE' });
      showToast('Deleted');
      await loadAuthFiles();
    } catch (e) { showToast(e.message, 'error'); }
  };
  overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
}

function showAddModal() {
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  overlay.id = 'addModal';
  overlay.innerHTML = '<div class="modal">' +
    '<button class="modal-close" id="addModalClose">&times;</button>' +
    '<h2>Add Account</h2>' +
    '<div class="modal-section"><h3>OAuth Login</h3>' +
    '<div class="oauth-grid">' +
      '<button class="oauth-btn" data-oauth="codex"><span class="oauth-icon">&#x1F916;</span> Codex / OpenAI<span class="oauth-label">OAuth PKCE</span></button>' +
      '<button class="oauth-btn" data-oauth="claude"><span class="oauth-icon">&#x1F4AC;</span> Claude / Anthropic<span class="oauth-label">OAuth PKCE</span></button>' +
      '<button class="oauth-btn" data-oauth="gemini"><span class="oauth-icon">&#x2728;</span> Gemini / Google<span class="oauth-label">OAuth PKCE</span></button>' +
      '<button class="oauth-btn" data-oauth="xai"><span class="oauth-icon">&#x26A1;</span> xAI / Grok<span class="oauth-label">OAuth PKCE</span></button>' +
      '<button class="oauth-btn" data-oauth="antigravity"><span class="oauth-icon">&#x1F680;</span> Antigravity<span class="oauth-label">OAuth</span></button>' +
      '<button class="oauth-btn" data-oauth="kimi"><span class="oauth-icon">&#x1F31F;</span> Kimi<span class="oauth-label">Device Code</span></button>' +
    '</div>' +
    '<div id="oauthStatus"></div>' +
    '</div>' +
    '<div class="modal-section"><h3>Upload JSON</h3>' +
    '<div class="upload-zone" id="uploadZone">Drop .json files here or click to browse<input type="file" id="fileInput" multiple accept=".json"></div>' +
    '</div>' +
    '<div class="modal-section"><h3>OpenAI-Compatible API</h3>' +
    '<div class="compat-form">' +
      '<input id="compatName" placeholder="Name (e.g. my-api)">' +
      '<input id="compatUrl" placeholder="Base URL (e.g. https://api.example.com/v1)">' +
      '<input id="compatKey" placeholder="API Key">' +
      '<input id="compatModels" placeholder="Models (comma-separated, optional)">' +
      '<button class="form-btn" id="compatSubmit">Add</button>' +
    '</div>' +
    '</div>' +
  '</div>';
  document.body.appendChild(overlay);
  overlay.querySelector('.modal-close').onclick = () => closeModal();
  overlay.onclick = (e) => { if (e.target === overlay) closeModal(); };

  document.querySelectorAll('[data-oauth]').forEach(btn => {
    btn.onclick = () => startOAuth(btn.dataset.oauth);
  });

  const zone = document.getElementById('uploadZone');
  const fi = document.getElementById('fileInput');
  zone.onclick = () => fi.click();
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('dragover'); };
  zone.ondragleave = () => zone.classList.remove('dragover');
  zone.ondrop = (e) => { e.preventDefault(); zone.classList.remove('dragover'); uploadFiles(e.dataTransfer.files); };
  fi.onchange = () => { if (fi.files.length) uploadFiles(fi.files); };

  document.getElementById('compatSubmit').onclick = addCompatProvider;
}

function closeModal() {
  const m = document.getElementById('addModal');
  if (m) m.remove();
  if (oauthPollTimer) { clearInterval(oauthPollTimer); oauthPollTimer = null; }
}

const OAUTH_ENDPOINTS = {
  codex: '/codex-auth-url',
  claude: '/anthropic-auth-url',
  gemini: '/gemini-cli-auth-url',
  xai: '/xai-auth-url',
  antigravity: '/antigravity-auth-url',
  kimi: '/kimi-auth-url',
};

async function startOAuth(provider) {
  const statusEl = document.getElementById('oauthStatus');
  const endpoint = OAUTH_ENDPOINTS[provider];
  if (!endpoint) { showToast('Unknown provider', 'error'); return; }
  statusEl.className = 'oauth-status waiting';
  statusEl.textContent = 'Requesting authorization URL...';
  try {
    const data = await api(endpoint + '?is_webui=true');
    const url = data.url || data.auth_url || data.authorization_url || data.redirect_url;
    const state = data.state || data.device_code || '';
    if (!url) { statusEl.className = 'oauth-status fail'; statusEl.textContent = 'No auth URL returned'; return; }
    if (data.verification_uri || data.user_code) {
      statusEl.className = 'oauth-status waiting';
      statusEl.innerHTML = 'Kimi Device Code Flow<br>Code: <strong>' + escHtml(data.user_code) + '</strong><br>Visit: <a href="' + escHtml(data.verification_uri) + '" target="_blank">' + escHtml(data.verification_uri) + '</a><br><br><small>Polling for authorization...</small>';
      window.open(data.verification_uri, '_blank');
    } else {
      statusEl.className = 'oauth-status waiting';
      statusEl.innerHTML = 'Opening browser for authorization...<br><small>If not opened, <a href="' + escHtml(url) + '" target="_blank">click here</a></small>';
      window.open(url, '_blank');
    }
    if (state) {
      pollOAuthStatus(state);
    } else {
      statusEl.className = 'oauth-status done';
      statusEl.textContent = 'Authorization URL opened. Check the browser window.';
    }
  } catch (e) {
    statusEl.className = 'oauth-status fail';
    statusEl.textContent = e.message;
  }
}

function pollOAuthStatus(state) {
  if (oauthPollTimer) clearInterval(oauthPollTimer);
  let ticks = 0;
  oauthPollTimer = setInterval(async () => {
    ticks++;
    if (ticks > 120) {
      clearInterval(oauthPollTimer);
      oauthPollTimer = null;
      const s = document.getElementById('oauthStatus');
      if (s) { s.className = 'oauth-status fail'; s.textContent = 'Timed out waiting for authorization'; }
      return;
    }
    try {
      const data = await api('/get-auth-status?state=' + encodeURIComponent(state));
      const statusEl = document.getElementById('oauthStatus');
      if (!statusEl) { clearInterval(oauthPollTimer); oauthPollTimer = null; return; }
      if (data.status === 'ok') {
        statusEl.className = 'oauth-status done';
        statusEl.textContent = 'Authorization successful!';
        clearInterval(oauthPollTimer);
        oauthPollTimer = null;
        await loadAuthFiles();
        setTimeout(() => closeModal(), 1500);
      } else if (data.status === 'error') {
        statusEl.className = 'oauth-status fail';
        statusEl.textContent = 'Authorization failed: ' + escHtml(data.message || data.error || 'unknown error');
        clearInterval(oauthPollTimer);
        oauthPollTimer = null;
      }
    } catch (e) {
      // keep polling
    }
  }, 2000);
}

async function uploadFiles(files) {
  const formData = new FormData();
  for (const f of files) {
    formData.append('files', f);
  }
  try {
    const res = await fetch(apiUrl('/auth-files'), {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + TOKEN },
      body: formData,
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    showToast('Uploaded ' + (files.length) + ' file(s)');
    await loadAuthFiles();
    setTimeout(() => closeModal(), 1000);
  } catch (e) {
    showToast(e.message, 'error');
  }
}

async function addCompatProvider() {
  const name = document.getElementById('compatName').value.trim();
  const url = document.getElementById('compatUrl').value.trim();
  const key = document.getElementById('compatKey').value.trim();
  const models = document.getElementById('compatModels').value.trim();
  if (!name || !url || !key) { showToast('Name, URL and Key are required', 'error'); return; }
  const body = { name: name, base_url: url, api_key: key };
  if (models) {
    body.models = models.split(',').map(m => m.trim()).filter(Boolean);
  }
  try {
    await api('/openai-compatibility', { method: 'PUT', body: body });
    showToast('Added: ' + name);
    await loadAuthFiles();
    setTimeout(() => closeModal(), 1000);
  } catch (e) { showToast(e.message, 'error'); }
}

document.getElementById('list').addEventListener('click', (e) => {
  const btn = e.target.closest('[data-action]');
  if (!btn) return;
  const action = btn.dataset.action;
  const idx = btn.dataset.idx;
  if (action === 'enable') toggleAuth(idx, true);
  else if (action === 'disable') toggleAuth(idx, false);
  else if (action === 'delete') deleteAuth(idx, btn.dataset.name);
});

document.getElementById('search').addEventListener('input', (e) => {
  searchQuery = e.target.value;
  renderList();
});

document.querySelectorAll('.filter-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    currentFilter = btn.dataset.filter;
    renderList();
  });
});

document.getElementById('refreshBtn').addEventListener('click', loadAuthFiles);
document.getElementById('addBtn').addEventListener('click', showAddModal);

TOKEN = getToken();
if (!TOKEN) {
  document.getElementById('list').innerHTML = '<div class="empty-state"><div class="icon">&#x1F512;</div><p>Token required: add ?token=YOUR_KEY to URL</p></div>';
} else {
  loadAuthFiles();
  setInterval(loadAuthFiles, 30000);
}
</script>
</body>
</html>`

func (h *Handler) ServeAuthFilesPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	c.String(http.StatusOK, authFilesPageHTML)
}
