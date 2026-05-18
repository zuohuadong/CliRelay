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
  --green:#10b981;--red:#ef4444;--amber:#f59e0b;--purple:#8b5cf6;
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

.card .status-dot{
  width:10px;height:10px;border-radius:50%;justify-self:center;
  flex-shrink:0;
}
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

.empty-state{
  text-align:center;padding:60px 20px;color:var(--text3);
}
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

@media(max-width:640px){
  header{padding:10px 16px}
  .toolbar{padding:10px 16px}
  .stats-bar{padding:8px 16px;flex-wrap:wrap;gap:10px}
  .list{padding:8px}
  .card{grid-template-columns:auto 1fr;gap:8px 12px;padding:12px}
  .card .actions{grid-column:1/-1;justify-content:flex-end}
}

.confirm-overlay{
  position:fixed;inset:0;z-index:200;background:rgba(0,0,0,.6);backdrop-filter:blur(4px);
  display:flex;align-items:center;justify-content:center;
}
.confirm-box{
  background:var(--surface);border:1px solid var(--border);border-radius:12px;
  padding:24px;max-width:360px;width:90%;text-align:center;
}
.confirm-box p{margin-bottom:16px;font-size:.9rem}
.confirm-box .btns{display:flex;gap:10px;justify-content:center}
.confirm-box button{
  padding:8px 20px;border-radius:var(--radius);border:1px solid var(--border);
  cursor:pointer;font-family:inherit;font-size:.8rem;transition:all .15s;
}
.confirm-box .cancel{background:transparent;color:var(--text2)}
.confirm-box .cancel:hover{background:var(--surface2)}
.confirm-box .confirm{background:var(--red);color:#fff;border-color:var(--red)}
.confirm-box .confirm:hover{opacity:.9}
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
</div>

<div class="stats-bar" id="statsBar"></div>
<div class="list" id="list"><div class="loading" id="loadingEl"></div></div>
<div class="toast" id="toast"></div>

<script>
const API_BASE = '/v0/management';
let TOKEN = '';
let allFiles = [];
let currentFilter = 'all';
let searchQuery = '';

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
  if (opts.body && typeof opts.body === 'object') {
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
  const sl = statusLabel(auth);
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

  return '<div class="card' + (disabled ? ' disabled-card' : '') + '" data-status="' + sc + '">' +
    '<div class="status-dot ' + sc + '" title="' + sl + '"></div>' +
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

function escHtml(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function escAttr(s) { return (s || '').replace(/"/g, '&quot;'); }

function renderList() {
  const list = document.getElementById('list');
  let filtered = allFiles;

  if (currentFilter !== 'all') {
    filtered = filtered.filter(a => {
      const sc = statusClass(a);
      return sc === currentFilter;
    });
  }

  if (searchQuery) {
    const q = searchQuery.toLowerCase();
    filtered = filtered.filter(a => {
      return (a.name || '').toLowerCase().includes(q) ||
        (a.email || '').toLowerCase().includes(q) ||
        (a.provider_name || '').toLowerCase().includes(q) ||
        (a.provider || '').toLowerCase().includes(q) ||
        (a.auth_index || '').toLowerCase().includes(q) ||
        (a.base_url || '').toLowerCase().includes(q);
    });
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
      const sa = statusClass(a), sb = statusClass(b);
      const order = { error: 0, active: 1, cooling: 2, disabled: 3 };
      return (order[sa] || 9) - (order[sb] || 9);
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
    await api('/auth-files/status', {
      method: 'PATCH',
      body: { auth_index: authIndex, disabled: !enable }
    });
    showToast(enable ? 'Enabled' : 'Disabled');
    await loadAuthFiles();
  } catch (e) {
    showToast(e.message, 'error');
  }
}

async function deleteAuth(authIndex, name) {
  showConfirm('Delete "' + name + '"?', async () => {
    try {
      await api('/auth-files?auth_index=' + encodeURIComponent(authIndex), { method: 'DELETE' });
      showToast('Deleted');
      await loadAuthFiles();
    } catch (e) {
      showToast(e.message, 'error');
    }
  });
}

function showConfirm(msg, onConfirm) {
  const overlay = document.createElement('div');
  overlay.className = 'confirm-overlay';
  overlay.innerHTML = '<div class="confirm-box"><p>' + escHtml(msg) + '</p><div class="btns"><button class="cancel">Cancel</button><button class="confirm">Delete</button></div></div>';
  document.body.appendChild(overlay);
  overlay.querySelector('.cancel').onclick = () => overlay.remove();
  overlay.querySelector('.confirm').onclick = () => { overlay.remove(); onConfirm(); };
  overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
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
