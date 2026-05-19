package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const authFilesPageHTML = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Auth Files</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#0f0f0f;--surface:#1a1a1a;--surface2:#242424;--surface3:#2e2e2e;--border:#333;--border2:#444;--text:#e0e0e0;--text2:#999;--text3:#666;--primary:#6366f1;--primary-hover:#818cf8;--primary-bg:rgba(99,102,241,.12);--danger:#ef4444;--danger-hover:#f87171;--danger-bg:rgba(239,68,68,.12);--success:#22c55e;--success-hover:#4ade80;--success-bg:rgba(34,197,94,.12);--warning:#f59e0b;--warning-bg:rgba(245,158,11,.12);--info:#3b82f6;--info-bg:rgba(59,130,246,.12)}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.header{background:var(--surface);border-bottom:1px solid var(--border);padding:12px 20px;display:flex;align-items:center;justify-content:space-between;position:sticky;top:0;z-index:10;gap:12px}
.header h1{font-size:18px;font-weight:600;white-space:nowrap}
.header-actions{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.search-box{background:var(--surface2);border:1px solid var(--border);border-radius:6px;padding:6px 12px;color:var(--text);font-size:13px;width:220px;outline:none;transition:border-color .15s}
.search-box:focus{border-color:var(--primary)}
.search-box::placeholder{color:var(--text3)}
.btn{padding:6px 14px;border-radius:6px;border:1px solid var(--border);background:var(--surface2);color:var(--text);font-size:13px;cursor:pointer;transition:all .15s;display:inline-flex;align-items:center;gap:4px;white-space:nowrap;user-select:none}
.btn:hover{background:var(--border)}
.btn:active{transform:scale(.97)}
.btn-primary{background:var(--primary);border-color:var(--primary);color:#fff}
.btn-primary:hover{background:var(--primary-hover)}
.btn-danger{background:transparent;border-color:var(--danger);color:var(--danger)}
.btn-danger:hover{background:var(--danger);color:#fff}
.btn-success{background:transparent;border-color:var(--success);color:var(--success)}
.btn-success:hover{background:var(--success);color:#fff}
.btn-warning{background:transparent;border-color:var(--warning);color:var(--warning)}
.btn-warning:hover{background:var(--warning);color:#fff}
.btn-sm{padding:3px 10px;font-size:12px}
.btn-xs{padding:2px 8px;font-size:11px}
.btn-icon{padding:4px 8px;font-size:16px;line-height:1}
.tabs{display:flex;background:var(--surface);border-bottom:1px solid var(--border);padding:0 20px}
.tab{padding:10px 20px;cursor:pointer;color:var(--text2);font-size:14px;border-bottom:2px solid transparent;transition:all .15s;user-select:none}
.tab.active{color:var(--primary);border-bottom-color:var(--primary)}
.tab:hover{color:var(--text)}
.tab-content{display:none}
.tab-content.active{display:block}
.toolbar{padding:10px 20px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;background:var(--surface);border-bottom:1px solid var(--border)}
.filter-chip{padding:4px 12px;border-radius:20px;border:1px solid var(--border);background:transparent;color:var(--text2);font-size:12px;cursor:pointer;transition:all .15s;display:inline-flex;align-items:center;gap:4px;user-select:none}
.filter-chip.active{background:var(--primary);border-color:var(--primary);color:#fff}
.filter-chip:hover{border-color:var(--primary)}
.filter-chip .chip-count{background:rgba(255,255,255,.2);padding:1px 6px;border-radius:10px;font-size:10px}
.filter-chip.active .chip-count{background:rgba(255,255,255,.25)}
.toggle-wrap{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--text2);cursor:pointer;user-select:none}
.toggle-track{width:36px;height:20px;border-radius:10px;background:var(--border);position:relative;transition:background .2s;cursor:pointer}
.toggle-track.on{background:var(--primary)}
.toggle-thumb{width:16px;height:16px;border-radius:50%;background:#fff;position:absolute;top:2px;left:2px;transition:left .2s}
.toggle-track.on .toggle-thumb{left:18px}
.toggle-count{background:var(--primary-bg);color:var(--primary);padding:1px 6px;border-radius:10px;font-size:10px;font-weight:600}
.sort-select{background:var(--surface2);border:1px solid var(--border);border-radius:6px;padding:4px 8px;color:var(--text);font-size:12px;outline:none;cursor:pointer}
.sort-select:focus{border-color:var(--primary)}
.stats{padding:8px 20px;display:flex;gap:16px;font-size:12px;color:var(--text2);background:var(--surface);border-bottom:1px solid var(--border);flex-wrap:wrap}
.stats span{display:flex;align-items:center;gap:4px}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-ok{background:var(--success)}.dot-err{background:var(--danger)}.dot-dis{background:var(--text2)}.dot-warn{background:var(--warning)}
.table-wrap{overflow-x:auto;padding:0}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;padding:10px 12px;background:var(--surface2);color:var(--text2);font-weight:500;position:sticky;top:0;border-bottom:1px solid var(--border);white-space:nowrap}
td{padding:8px 12px;border-bottom:1px solid var(--border);white-space:nowrap;max-width:200px;overflow:hidden;text-overflow:ellipsis}
tr:hover td{background:var(--surface2)}
tr.selected td{background:var(--primary-bg)}
.badge{padding:2px 8px;border-radius:10px;font-size:11px;font-weight:500;display:inline-block}
.badge-ok{background:var(--success-bg);color:var(--success)}
.badge-err{background:var(--danger-bg);color:var(--danger)}
.badge-dis{background:rgba(153,153,153,.15);color:var(--text2)}
.badge-warn{background:var(--warning-bg);color:var(--warning)}
.badge-info{background:var(--info-bg);color:var(--info)}
.actions{display:flex;gap:4px}
.empty{text-align:center;padding:60px 20px;color:var(--text2)}
.empty p{margin-top:8px;font-size:14px}
.loading{text-align:center;padding:40px;color:var(--text2)}
.pagination{display:flex;align-items:center;justify-content:center;gap:4px;padding:12px 20px;background:var(--surface);border-top:1px solid var(--border)}
.page-btn{padding:4px 10px;border-radius:4px;border:1px solid var(--border);background:var(--surface2);color:var(--text);font-size:12px;cursor:pointer;transition:all .15s;min-width:32px;text-align:center}
.page-btn:hover{background:var(--border)}
.page-btn.active{background:var(--primary);border-color:var(--primary);color:#fff}
.page-btn:disabled{opacity:.4;cursor:not-allowed}
.page-info{font-size:12px;color:var(--text2);margin:0 8px}
.modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:100;display:flex;align-items:center;justify-content:center;animation:fadeIn .15s}
.modal{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:24px;width:90%;max-width:560px;max-height:85vh;overflow-y:auto}
.modal-lg{max-width:720px}
.modal-xl{max-width:860px}
.modal h3{margin-bottom:16px;font-size:16px}
.modal label{display:block;margin-bottom:4px;font-size:13px;color:var(--text2)}
.modal input,.modal select,.modal textarea{width:100%;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;margin-bottom:12px;outline:none;transition:border-color .15s}
.modal input:focus,.modal select:focus,.modal textarea:focus{border-color:var(--primary)}
.modal textarea{min-height:80px;resize:vertical;font-family:monospace}
.modal-actions{display:flex;gap:8px;justify-content:flex-end;margin-top:16px}
.sub-tabs{display:flex;gap:0;border-bottom:1px solid var(--border);margin-bottom:16px}
.sub-tab{padding:8px 16px;cursor:pointer;color:var(--text2);font-size:13px;border-bottom:2px solid transparent;transition:all .15s;user-select:none}
.sub-tab.active{color:var(--primary);border-bottom-color:var(--primary)}
.sub-tab:hover{color:var(--text)}
.sub-content{display:none}
.sub-content.active{display:block}
.field-row{display:flex;align-items:center;margin-bottom:10px;gap:8px}
.field-label{color:var(--text2);font-size:13px;min-width:100px;flex-shrink:0}
.field-value{color:var(--text);font-size:13px;word-break:break-all;flex:1}
.field-input{flex:1;padding:6px 10px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;outline:none;transition:border-color .15s}
.field-input:focus{border-color:var(--primary)}
.model-list{display:flex;flex-wrap:wrap;gap:6px;max-height:300px;overflow-y:auto;padding:4px 0}
.model-item{padding:4px 10px;border-radius:6px;background:var(--surface2);border:1px solid var(--border);font-size:12px;color:var(--text);display:flex;align-items:center;gap:6px}
.model-item input[type=checkbox]{accent-color:var(--primary)}
.chart-container{display:flex;align-items:flex-end;gap:3px;height:140px;padding:8px 0;border-bottom:1px solid var(--border)}
.chart-bar{display:flex;flex-direction:column;align-items:center;gap:2px;flex:1;min-width:16px;max-width:40px}
.chart-bar-inner{width:100%;display:flex;flex-direction:column-reverse;gap:1px}
.chart-seg{width:100%;border-radius:2px;min-height:1px}
.chart-seg-ok{background:var(--success)}
.chart-seg-fail{background:var(--danger)}
.chart-label{font-size:9px;color:var(--text2);white-space:nowrap}
.provider-card{background:var(--surface2);border:1px solid var(--border);border-radius:8px;margin-bottom:8px;overflow:hidden}
.provider-header{padding:12px 16px;cursor:pointer;display:flex;align-items:center;justify-content:space-between;font-size:14px;transition:background .15s}
.provider-header:hover{background:var(--border)}
.provider-body{padding:0 16px 12px;display:none}
.provider-body.open{display:block}
.tag{display:inline-flex;align-items:center;gap:4px;padding:3px 10px;border-radius:14px;background:var(--surface2);border:1px solid var(--border);font-size:12px;color:var(--text);margin:2px}
.tag-remove{cursor:pointer;color:var(--text2);font-size:14px;line-height:1}
.tag-remove:hover{color:var(--danger)}
.add-tag-row{display:flex;gap:6px;margin-top:8px}
.add-tag-row input{flex:1;padding:5px 10px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:12px;outline:none;transition:border-color .15s}
.add-tag-row input:focus{border-color:var(--primary)}
.batch-bar{padding:8px 20px;display:flex;gap:8px;align-items:center;background:var(--surface2);border-bottom:1px solid var(--border);font-size:13px}
.batch-bar .count{color:var(--primary);font-weight:600}
.oauth-section{padding:8px 20px;display:flex;gap:8px;flex-wrap:wrap}
.toast{position:fixed;top:20px;right:20px;padding:10px 20px;border-radius:8px;font-size:13px;z-index:200;animation:fadeIn .2s;max-width:400px;box-shadow:0 4px 12px rgba(0,0,0,.3)}
.toast-ok{background:var(--success);color:#fff}
.toast-err{background:var(--danger);color:#fff}
.stat-card{background:var(--surface2);border:1px solid var(--border);border-radius:8px;padding:12px 16px;text-align:center;flex:1;min-width:100px}
.stat-card .stat-value{font-size:22px;font-weight:700;color:var(--text)}
.stat-card .stat-label{font-size:11px;color:var(--text2);margin-top:4px}
.stat-cards{display:flex;gap:8px;margin-bottom:16px;flex-wrap:wrap}
.time-toggle{display:flex;gap:4px;margin-bottom:12px}
.time-toggle .tt-btn{padding:4px 12px;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--text2);font-size:12px;cursor:pointer;transition:all .15s}
.time-toggle .tt-btn.active{background:var(--primary);border-color:var(--primary);color:#fff}
.tags-section{margin-top:12px;padding-top:12px;border-top:1px solid var(--border)}
.tags-section h4{font-size:13px;color:var(--text2);margin-bottom:8px}
.tags-display{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px}
.tags-display label{display:inline-flex;align-items:center;gap:4px;padding:3px 8px;border-radius:4px;background:var(--surface2);border:1px solid var(--border);font-size:11px;color:var(--text);cursor:pointer;transition:all .15s}
.tags-display label:hover{border-color:var(--primary)}
.tags-display input[type=checkbox]{accent-color:var(--primary)}
.import-search{width:100%;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;margin-bottom:12px;outline:none}
.import-search:focus{border-color:var(--primary)}
.import-list{max-height:300px;overflow-y:auto;border:1px solid var(--border);border-radius:6px}
.import-item{padding:6px 12px;display:flex;align-items:center;gap:8px;border-bottom:1px solid var(--border);font-size:12px;cursor:pointer;transition:background .1s}
.import-item:hover{background:var(--surface2)}
.import-item:last-child{border-bottom:none}
.import-item input[type=checkbox]{accent-color:var(--primary)}
.import-item .ii-name{flex:1;color:var(--text)}
.import-item .ii-owner{color:var(--text2);font-size:11px}
.confirm-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:150;display:flex;align-items:center;justify-content:center;animation:fadeIn .15s}
.confirm-box{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:24px;max-width:400px;width:90%;text-align:center}
.confirm-box p{margin-bottom:16px;font-size:14px}
.confirm-box .confirm-actions{display:flex;gap:8px;justify-content:center}
@keyframes fadeIn{from{opacity:0;transform:translateY(-10px)}to{opacity:1;transform:translateY(0)}}
@media(max-width:768px){.search-box{width:140px}.header h1{font-size:15px}td,th{padding:6px 8px;font-size:12px}.modal{width:95%;padding:16px}.stat-card{min-width:70px}.stat-card .stat-value{font-size:18px}}
.badge-quota{background:rgba(245,158,11,.15);color:#f59e0b;font-size:11px;padding:1px 6px;border-radius:8px;display:inline-block}
.badge-quota-ok{background:rgba(34,197,94,.15);color:#22c55e}
.quota-bar-wrap{display:flex;align-items:center;gap:4px}
.quota-bar{width:60px;height:6px;background:var(--surface3);border-radius:3px;overflow:hidden;position:relative}
.quota-bar-fill{height:100%;border-radius:3px;transition:width .3s}
.quota-bar-fill.high{background:var(--danger)}
.quota-bar-fill.medium{background:var(--warning)}
.quota-bar-fill.low{background:var(--success)}
</style>
</head>
<body>
<div class="header">
  <h1>Auth Files 管理</h1>
  <div class="header-actions">
    <input class="search-box" id="search" placeholder="搜索名称/邮箱/Provider/标签..." oninput="render()">
    <button class="btn btn-primary" onclick="showAddModal()">+ 添加</button>
    <button class="btn" onclick="showUploadModal()">上传</button>
    <button class="btn" onclick="loadData()">刷新</button>
  </div>
</div>
<div class="tabs">
  <div class="tab active" onclick="switchTab(0)">文件管理</div>
  <div class="tab" onclick="switchTab(1)">排除模型</div>
  <div class="tab" onclick="switchTab(2)">模型别名</div>
</div>
<div class="tab-content active" id="tab0">
  <div class="toolbar" id="toolbar"></div>
  <div class="stats" id="stats"></div>
  <div class="batch-bar" id="batchBar" style="display:none">
    <span>已选 <span class="count" id="selCount">0</span> 项:</span>
    <button class="btn btn-sm btn-success" onclick="batchToggle(false)">批量启用</button>
    <button class="btn btn-sm btn-danger" onclick="batchToggle(true)">批量禁用</button>
    <button class="btn btn-sm btn-danger" onclick="batchDelete()">批量删除</button>
    <button class="btn btn-sm" onclick="deleteAllDisabled()">删除所有已禁用</button>
  </div>
  <div class="oauth-section" id="oauthSection">
    <button class="btn btn-sm" onclick="startOAuth('codex')">Codex OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('kimi')">Kimi OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('anthropic')">Anthropic OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('gemini')">Gemini CLI OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('xai')">xAI OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('antigravity')">Vertex 导入</button>
  </div>
  <div class="table-wrap">
    <table>
      <thead><tr>
        <th><input type="checkbox" id="selectAll" onchange="toggleSelectAll(this.checked)"></th>
        <th>#</th><th>名称</th><th>Provider</th><th>状态</th><th>邮箱/账号</th><th>Plan</th><th>额度</th><th>成功</th><th>失败</th><th>最后刷新</th><th>操作</th>
      </tr></thead>
      <tbody id="tbody"></tbody>
    </table>
  </div>
  <div class="pagination" id="pagination"></div>
  <div class="loading" id="loading">加载中...</div>
  <div class="empty" id="empty" style="display:none"><p>未找到 auth 文件</p></div>
</div>
<div class="tab-content" id="tab1">
  <div style="padding:20px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h3 style="font-size:16px">排除模型</h3>
      <div style="display:flex;gap:8px">
        <button class="btn btn-sm btn-primary" onclick="addExcludedProvider()">+ 添加 Provider</button>
        <button class="btn btn-sm" onclick="loadExcludedModels()">刷新</button>
      </div>
    </div>
    <div id="excludedList"></div>
  </div>
</div>
<div class="tab-content" id="tab2">
  <div style="padding:20px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h3 style="font-size:16px">模型别名</h3>
      <div style="display:flex;gap:8px">
        <button class="btn btn-sm btn-primary" onclick="addAliasChannel()">+ 添加 Channel</button>
        <button class="btn btn-sm" onclick="loadModelAliases()">刷新</button>
      </div>
    </div>
    <div id="aliasList"></div>
  </div>
</div>

<script>
var API=window.location.origin+'/v0/management';
var token='';
var files=[];
var filter='all';
var selectedIds={};
var currentTab=0;
var currentPage=1;
var pageSize=20;
var sortBy='default';
var showProblem=false;
var showDisabled=false;
var detailFile=null;
var detailSubTab=0;
var usageTimeWindow='5h';

function getToken(){
  var p=new URLSearchParams(window.location.search);
  token=p.get('token')||'';
  if(!token){document.getElementById('loading').innerHTML='<div class="empty"><p style="color:var(--danger)">错误: 缺少 token 参数</p></div>';return false}
  return true
}

function api(method,path,body){
  var opts={method:method,headers:{'Authorization':'Bearer '+token,'Content-Type':'application/json'}};
  if(body)opts.body=JSON.stringify(body);
  return fetch(API+path,opts).then(function(r){
    if(!r.ok)throw new Error(r.status+' '+r.statusText);
    return r.json()
  })
}

function apiUpload(formData){
  return fetch(API+'/auth-files',{method:'POST',headers:{'Authorization':'Bearer '+token},body:formData}).then(function(r){
    if(!r.ok)throw new Error(r.status+' '+r.statusText);
    return r.json()
  })
}

function toast(msg,ok){
  var d=document.createElement('div');
  d.className='toast toast-'+(ok?'ok':'err');
  d.textContent=msg;
  document.body.appendChild(d);
  setTimeout(function(){d.remove()},3000)
}

function confirmDialog(msg,onConfirm){
  var overlay=document.createElement('div');
  overlay.className='confirm-overlay';
  overlay.innerHTML='<div class="confirm-box"><p>'+escHtml(msg)+'</p><div class="confirm-actions"><button class="btn" id="confirmCancel">取消</button><button class="btn btn-danger" id="confirmOk">确认</button></div></div>';
  document.body.appendChild(overlay);
  overlay.querySelector('#confirmCancel').onclick=function(){overlay.remove()};
  overlay.querySelector('#confirmOk').onclick=function(){overlay.remove();onConfirm()};
  overlay.onclick=function(e){if(e.target===overlay)overlay.remove()}
}

function switchTab(idx){
  currentTab=idx;
  var tabs=document.querySelectorAll('.tab');
  var contents=document.querySelectorAll('.tab-content');
  for(var i=0;i<tabs.length;i++){
    tabs[i].className='tab'+(i===idx?' active':'');
    contents[i].className='tab-content'+(i===idx?' active':'')
  }
  if(idx===1)loadExcludedModels();
  if(idx===2)loadModelAliases()
}

function escHtml(s){
  if(!s)return'';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')
}

function escAttr(s){
  if(!s)return'';
  return String(s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/'/g,'&#39;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
}

function statusBadge(s,disabled,quota){
  if(disabled)return'<span class="badge badge-dis">已禁用</span>';
  if(quota&&quota.exceeded)return'<span class="badge badge-warn">额度超限</span>';
  switch(s){
    case'ok':case'active':return'<span class="badge badge-ok">OK</span>';
    case'error':return'<span class="badge badge-err">错误</span>';
    case'rate_limited':return'<span class="badge badge-warn">限速</span>';
    default:return'<span class="badge badge-warn">'+escHtml(s||'未知')+'</span>'
  }
}

function fmtTime(t){
  if(!t)return'-';
  var d=new Date(t);
  if(isNaN(d))return escHtml(t);
  return d.toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'})
}

function fmtTimeFull(t){
  if(!t)return'-';
  var d=new Date(t);
  if(isNaN(d))return escHtml(t);
  return d.toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit'})
}

function getPlan(f){
  if(!f)return'-';
  var parts=[];
  if(f.id_token&&f.id_token.plan_type){
    parts.push(escHtml(f.id_token.plan_type))
  }
  if(f.unavailable&&!f.disabled)parts.push('<span style="color:var(--warning);font-size:10px">不可用</span>');
  return parts.length?parts.join(' '):'-'
}

function getQuotaHtml(f){
  if(!f)return '-';
  var q=f.quota;
  if(!q)return '-';
  if(f.disabled)return '<span class="badge badge-dis">已禁用</span>';
  if(q.exceeded){
    var recover='';
    if(q.next_recover_at){
      var d=new Date(q.next_recover_at);
      if(!isNaN(d.getTime())){
        var diff=d-new Date();
        if(diff>0){
          var mins=Math.ceil(diff/60000);
          recover=mins>60?Math.ceil(mins/60)+'h':'~'+mins+'m'
        }
      }
    }
    return '<span class="badge-quota">额度超限'+(recover?' <span style="font-size:10px">(' +recover+')</span>':'')+'</span>'
  }
  var r5h=0;
  if(f.recent_requests){
    for(var i=0;i<f.recent_requests.length;i++){
      r5h+=(f.recent_requests[i].success||0)+(f.recent_requests[i].failed||0)
    }
  }
  if(r5h>0)return '<span class="badge-quota badge-quota-ok">'+r5h+' req</span>';
  return '<span class="badge-quota badge-quota-ok">OK</span>'
}

function get5hCount(f){
  if(!f||!f.recent_requests)return 0;
  var total=0;
  for(var i=0;i<f.recent_requests.length;i++){
    total+=(f.recent_requests[i].success||0)+(f.recent_requests[i].failed||0)
  }
  return total
}

function getProviderName(f){
  return f.provider_name||f.provider||f.type||'-'
}

function getProviderKey(f){
  return (f.provider||f.type||'unknown').toLowerCase()
}

function getFilteredFiles(){
  var q=document.getElementById('search').value.toLowerCase();
  return files.filter(function(f){
    if(filter!=='all'){
      var pk=getProviderKey(f);
      if(pk!==filter)return false
    }
    if(showProblem&&f.status!=='error')return false;
    if(showDisabled&&!f.disabled)return false;
    if(q){
      var s=(f.name+f.provider+f.provider_name+f.email+f.type+(f.status||'')+(f.label||'')+(f.note||'')).toLowerCase();
      if(!s.includes(q))return false
    }
    return true
  })
}

function sortFiles(list){
  if(sortBy==='alpha')return list.slice().sort(function(a,b){return(a.name||'').localeCompare(b.name||'')});
  if(sortBy==='priority')return list.slice().sort(function(a,b){return(a.priority||0)-(b.priority||0)});
  return list
}

async function loadData(){
  try{
    var data=await api('GET','/auth-files');
    files=data.files||data||[];
    selectedIds={};
    currentPage=1;
    render()
  }catch(e){toast('加载失败: '+e.message,false);document.getElementById('loading').textContent='加载失败: '+e.message}
}

function render(){
  document.getElementById('loading').style.display='none';
  renderToolbar();
  renderStats();
  var filtered=getFilteredFiles();
  var sorted=sortFiles(filtered);
  var totalPages=Math.ceil(sorted.length/pageSize)||1;
  if(currentPage>totalPages)currentPage=totalPages;
  var start=(currentPage-1)*pageSize;
  var pageItems=sorted.slice(start,start+pageSize);
  renderTable(pageItems,start);
  renderPagination(sorted.length,totalPages);
  updateBatchBar()
}

function renderToolbar(){
  var providerCounts={};
  for(var i=0;i<files.length;i++){
    var pk=getProviderKey(files[i]);
    providerCounts[pk]=(providerCounts[pk]||0)+1
  }
  var problemCount=0,disabledCount=0;
  for(var i=0;i<files.length;i++){
    if(files[i].status==='error')problemCount++;
    if(files[i].disabled)disabledCount++
  }
  var html='';
  html+='<button class="filter-chip'+(filter==='all'?' active':'')+'" onclick="filter=\'all\';currentPage=1;render()">全部 <span class="chip-count">'+files.length+'</span></button>';
  var providerOrder=['codex','anthropic','gemini','openai','xai','kimi','openai-compatibility'];
  var keys=Object.keys(providerCounts).sort(function(a,b){
    var ai=providerOrder.indexOf(a),bi=providerOrder.indexOf(b);
    if(ai===-1)ai=999;if(bi===-1)bi=999;
    if(ai!==bi)return ai-bi;
    return a.localeCompare(b)
  });
  for(var i=0;i<keys.length;i++){
    var k=keys[i];
    var label=k;
    if(k==='openai-compatibility')label='OpenAI 兼容';
    else label=k.charAt(0).toUpperCase()+k.slice(1);
    html+='<button class="filter-chip'+(filter===k?' active':'')+'" onclick="filter=\''+escAttr(k)+'\';currentPage=1;render()">'+escHtml(label)+' <span class="chip-count">'+providerCounts[k]+'</span></button>'
  }
  html+='<span style="width:1px;height:20px;background:var(--border);margin:0 4px"></span>';
  html+='<label class="toggle-wrap" onclick="showProblem=!showProblem;currentPage=1;render()">';
  html+='<div class="toggle-track'+(showProblem?' on':'')+'"><div class="toggle-thumb"></div></div>';
  html+='<span>问题</span>';
  if(problemCount>0)html+='<span class="toggle-count">'+problemCount+'</span>';
  html+='</label>';
  html+='<label class="toggle-wrap" onclick="showDisabled=!showDisabled;currentPage=1;render()">';
  html+='<div class="toggle-track'+(showDisabled?' on':'')+'"><div class="toggle-thumb"></div></div>';
  html+='<span>已禁用</span>';
  if(disabledCount>0)html+='<span class="toggle-count">'+disabledCount+'</span>';
  html+='</label>';
  html+='<span style="width:1px;height:20px;background:var(--border);margin:0 4px"></span>';
  html+='<select class="sort-select" onchange="sortBy=this.value;render()">';
  html+='<option value="default"'+(sortBy==='default'?' selected':'')+'>默认排序</option>';
  html+='<option value="alpha"'+(sortBy==='alpha'?' selected':'')+'>字母排序</option>';
  html+='<option value="priority"'+(sortBy==='priority'?' selected':'')+'>优先级排序</option>';
  html+='</select>';
  document.getElementById('toolbar').innerHTML=html
}

function renderStats(){
  var ok=0,err=0,dis=0,rl=0,qt=0;
  for(var i=0;i<files.length;i++){
    var f=files[i];
    if(f.disabled)dis++;
    else if(f.status==='ok'||f.status==='active')ok++;
    else if(f.status==='error')err++;
    else if(f.status==='rate_limited')rl++;
    if(f.quota&&f.quota.exceeded)qt++
  }
  var statsHtml=
    '<span><span class="dot dot-ok"></span> OK: '+ok+'</span>'+
    '<span><span class="dot dot-err"></span> 错误: '+err+'</span>'+
    '<span><span class="dot dot-warn"></span> 限速: '+rl+'</span>'+
    '<span><span class="dot dot-dis"></span> 已禁用: '+dis+'</span>';
  if(qt>0)statsHtml+='<span style="color:var(--warning)">&#x26A0; 额度超限: '+qt+'</span>';
  statsHtml+='<span>总计: '+files.length+'</span>';
  document.getElementById('stats').innerHTML=statsHtml
}

function renderTable(list,offset){
  var tbody=document.getElementById('tbody');
  if(!list.length){tbody.innerHTML='';document.getElementById('empty').style.display='';return}
  document.getElementById('empty').style.display='none';
  var rows=[];
  for(var i=0;i<list.length;i++){
    var f=list[i];
    var idx=(f.auth_index!=null?f.auth_index:'-');
    var name=f.name||f.id||'-';
    var prov=getProviderName(f);
    var email=f.email||f.account||'-';
    var succ=f.success||0;
    var fail=f.failed||0;
    var lr=f.last_refresh||f.updated_at||'-';
    var fid=escAttr(f.id||'');
    var checked=selectedIds[f.id]?'checked':'';
    var labelHtml=f.label?'<span class="badge badge-info" style="margin-left:4px">'+escHtml(f.label)+'</span>':'';
    rows.push('<tr class="'+(selectedIds[f.id]?'selected':'')+'">'+
      '<td><input type="checkbox" data-id="'+fid+'" class="row-check" '+(checked)+' onchange="toggleSelect(\''+fid+'\',this.checked)"></td>'+
      '<td>'+idx+'</td>'+
      '<td title="'+escAttr(name)+'" style="cursor:pointer;color:var(--primary)" onclick="showDetail(\''+fid+'\')">'+escHtml(name)+labelHtml+'</td>'+
      '<td>'+escHtml(prov)+'</td>'+
      '<td>'+statusBadge(f.status,f.disabled,f.quota)+'</td>'+
      '<td title="'+escAttr(email)+'">'+escHtml(email)+'</td>'+
      '<td>'+getPlan(f)+'</td>'+
      '<td>'+getQuotaHtml(f)+'</td>'+
      '<td>'+succ+'</td>'+
      '<td>'+fail+'</td>'+
      '<td>'+fmtTime(lr)+'</td>'+
      '<td class="actions">'+
        (f.disabled?'<button class="btn btn-xs btn-success" onclick="toggleStatus(\''+fid+'\',false)">启用</button>':'<button class="btn btn-xs" onclick="toggleStatus(\''+fid+'\',true)">禁用</button>')+
        '<button class="btn btn-xs btn-danger" onclick="deleteFile(\''+fid+'\',\''+escAttr(name)+'\')">删除</button>'+
      '</td></tr>')
  }
  tbody.innerHTML=rows.join('')
}

function renderPagination(total,totalPages){
  var c=document.getElementById('pagination');
  if(totalPages<=1){c.innerHTML='';return}
  var html='';
  html+='<button class="page-btn" '+(currentPage<=1?'disabled':'')+' onclick="currentPage--;render()">&laquo;</button>';
  var startPage=Math.max(1,currentPage-3);
  var endPage=Math.min(totalPages,currentPage+3);
  if(startPage>1){html+='<button class="page-btn" onclick="currentPage=1;render()">1</button>';if(startPage>2)html+='<span class="page-info">...</span>'}
  for(var i=startPage;i<=endPage;i++){
    html+='<button class="page-btn'+(i===currentPage?' active':'')+'" onclick="currentPage='+i+';render()">'+i+'</button>'
  }
  if(endPage<totalPages){if(endPage<totalPages-1)html+='<span class="page-info">...</span>';html+='<button class="page-btn" onclick="currentPage='+totalPages+';render()">'+totalPages+'</button>'}
  html+='<button class="page-btn" '+(currentPage>=totalPages?'disabled':'')+' onclick="currentPage++;render()">&raquo;</button>';
  html+='<span class="page-info">共 '+total+' 条</span>';
  c.innerHTML=html
}

function toggleSelect(id,checked){
  if(checked)selectedIds[id]=true;
  else delete selectedIds[id];
  updateBatchBar();
  render()
}

function toggleSelectAll(checked){
  var filtered=getFilteredFiles();
  var sorted=sortFiles(filtered);
  for(var i=0;i<sorted.length;i++){
    var f=sorted[i];
    if(checked)selectedIds[f.id]=true;
    else delete selectedIds[f.id]
  }
  updateBatchBar();
  render()
}

function updateBatchBar(){
  var count=Object.keys(selectedIds).length;
  document.getElementById('selCount').textContent=count;
  document.getElementById('batchBar').style.display=count>0?'flex':'none'
}

async function batchToggle(disable){
  var ids=Object.keys(selectedIds);
  if(!ids.length)return;
  var promises=[];
  for(var i=0;i<ids.length;i++){
    var f=files.find(function(x){return x.id===ids[i]});
    if(!f)continue;
    var name=f.name||f.id;
    promises.push(api('PATCH','/auth-files/status',{name:name,disabled:disable}))
  }
  try{
    await Promise.all(promises);
    toast(disable?'批量禁用成功':'批量启用成功',true);
    selectedIds={};
    loadData()
  }catch(e){toast('操作失败: '+e.message,false)}
}

async function batchDelete(){
  var ids=Object.keys(selectedIds);
  if(!ids.length)return;
  confirmDialog('确认删除 '+ids.length+' 个选中的文件?',function(){
    var promises=[];
    for(var i=0;i<ids.length;i++){
      var f=files.find(function(x){return x.id===ids[i]});
      if(!f)continue;
      var name=f.name||f.id;
      promises.push(api('DELETE','/auth-files?name='+encodeURIComponent(name)))
    }
    Promise.all(promises).then(function(){
      toast('批量删除成功',true);
      selectedIds={};
      loadData()
    }).catch(function(e){toast('删除失败: '+e.message,false)})
  })
}

async function deleteAllDisabled(){
  var disabledCount=files.filter(function(f){return f.disabled}).length;
  if(!disabledCount){toast('没有已禁用的文件',false);return}
  confirmDialog('确认删除所有已禁用的文件 ('+disabledCount+' 个)?',function(){
    api('DELETE','/auth-files?all=true').then(function(){
      toast('已删除所有禁用文件',true);
      selectedIds={};
      loadData()
    }).catch(function(e){toast('删除失败: '+e.message,false)})
  })
}

async function toggleStatus(id,disable){
  var f=files.find(function(x){return x.id===id});
  if(!f)return;
  var name=f.name||f.id;
  try{
    await api('PATCH','/auth-files/status',{name:name,disabled:disable});
    toast(disable?'已禁用':'已启用',true);loadData()
  }catch(e){toast('操作失败: '+e.message,false)}
}

async function deleteFile(id,name){
  confirmDialog('确认删除 "'+name+'"?',function(){
    api('DELETE','/auth-files?name='+encodeURIComponent(name)).then(function(){
      toast('已删除',true);loadData()
    }).catch(function(e){toast('删除失败: '+e.message,false)})
  })
}

function showAddModal(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='addModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>添加 Auth 文件</h3>'+
    '<label>Provider 类型</label><select id="addType" onchange="updateAddFields()">'+
    '<option value="anthropic">Anthropic (Claude)</option>'+
    '<option value="openai">OpenAI</option>'+
    '<option value="gemini">Gemini</option>'+
    '<option value="codex">Codex</option>'+
    '<option value="xai">xAI (Grok)</option>'+
    '<option value="kimi">Kimi</option>'+
    '<option value="openai-compatibility">OpenAI 兼容</option>'+
    '</select>'+
    '<div id="addFields"></div>'+
    '<div id="addCompatFields" style="display:none"></div>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'addModal\')">取消</button>'+
    '<button class="btn btn-primary" onclick="submitAdd()">添加</button>'+
    '</div></div>';
  document.body.appendChild(m);
  updateAddFields()
}

function updateAddFields(){
  var type=document.getElementById('addType').value;
  var f=document.getElementById('addFields');
  var cf=document.getElementById('addCompatFields');
  if(type==='openai-compatibility'){
    f.innerHTML='';
    cf.style.display='';
    cf.innerHTML=
      '<label>兼容名称</label><input id="addCompatName" placeholder="例如 deepseek">'+
      '<label>Base URL</label><input id="addBaseUrl" placeholder="https://api.deepseek.com/v1">'+
      '<label>API Key</label><textarea id="addApiKey" placeholder="sk-..."></textarea>'
  }else{
    cf.style.display='none';cf.innerHTML='';
    if(type==='anthropic')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="sk-ant-..."></textarea>';
    else if(type==='openai')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="sk-..."></textarea>';
    else if(type==='gemini')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="AIza..."></textarea>';
    else if(type==='codex')f.innerHTML='<p style="color:var(--text2);font-size:13px;margin-bottom:12px">请使用 OAuth 登录</p>';
    else if(type==='xai')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="xai-..."></textarea>';
    else if(type==='kimi')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="..."></textarea>';
    else f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="输入 Key..."></textarea>'
  }
}

function closeModal(id){var m=document.getElementById(id);if(m)m.remove()}

async function submitAdd(){
  var type=document.getElementById('addType').value;
  if(type==='openai-compatibility'){
    var name=document.getElementById('addCompatName').value.trim();
    var base=document.getElementById('addBaseUrl').value.trim();
    var key=document.getElementById('addApiKey').value.trim();
    if(!name||!base||!key){toast('所有字段必填',false);return}
    var content=JSON.stringify({provider:"openai-compatibility",compat_name:name,base_url:base,api_key:key});
    var blob=new Blob([content],{type:'application/json'});
    var fd=new FormData();fd.append('file',blob,name+'.json');
    try{await apiUpload(fd);toast('添加成功',true);closeModal('addModal');loadData()}catch(e){toast('添加失败: '+e.message,false)}
  }else{
    var keyEl=document.getElementById('addKey');
    var key=keyEl?keyEl.value.trim():'';
    if(!key&&type!=='codex'){toast('Key 必填',false);return}
    var content=JSON.stringify({provider:type,api_key:key});
    var blob=new Blob([content],{type:'application/json'});
    var fd=new FormData();fd.append('file',blob,type+'-key.json');
    try{await apiUpload(fd);toast('添加成功',true);closeModal('addModal');loadData()}catch(e){toast('添加失败: '+e.message,false)}
  }
}

function showUploadModal(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='uploadModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>上传 Auth 文件</h3>'+
    '<label>选择文件</label><input type="file" id="uploadInput" multiple accept=".json">'+
    '<p style="color:var(--text2);font-size:12px;margin-bottom:12px">支持 .json 文件，可多选</p>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'uploadModal\')">取消</button>'+
    '<button class="btn btn-primary" onclick="submitUpload()">上传</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitUpload(){
  var input=document.getElementById('uploadInput');
  if(!input.files.length){toast('请选择文件',false);return}
  var fd=new FormData();
  for(var i=0;i<input.files.length;i++)fd.append('file',input.files[i]);
  try{await apiUpload(fd);toast('上传成功',true);closeModal('uploadModal');loadData()}catch(e){toast('上传失败: '+e.message,false)}
}

function showDetail(id){
  var f=files.find(function(x){return x.id===id});
  if(!f)return;
  detailFile=f;
  detailSubTab=0;
  var name=escHtml(f.name||f.id||'-');
  var m=document.createElement('div');m.className='modal-overlay';m.id='detailModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal modal-xl"><h3>'+name+'</h3>'+
    '<div class="sub-tabs">'+
    '<div class="sub-tab active" onclick="switchDetailSub(this,0)">使用趋势</div>'+
    '<div class="sub-tab" onclick="switchDetailSub(this,1)">字段编辑</div>'+
    '<div class="sub-tab" onclick="switchDetailSub(this,2)">模型列表</div>'+
    '</div>'+
    '<div class="sub-content active" id="dsub0"></div>'+
    '<div class="sub-content" id="dsub1"></div>'+
    '<div class="sub-content" id="dsub2"></div>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'detailModal\')">关闭</button>'+
    '</div></div>';
  document.body.appendChild(m);
  loadUsageData(f);
  renderFields(f);
  loadModelsForFile(f)
}

function switchDetailSub(el,idx){
  detailSubTab=idx;
  var parent=el.parentElement;
  var tabs=parent.querySelectorAll('.sub-tab');
  var contents=parent.parentElement.querySelectorAll('.sub-content');
  for(var i=0;i<tabs.length;i++){
    tabs[i].className='sub-tab'+(i===idx?' active':'');
    contents[i].className='sub-content'+(i===idx?' active':'')
  }
}

async function loadUsageData(f){
  var c=document.getElementById('dsub0');
  if(!c)return;
  c.innerHTML='<div class="loading">加载使用数据...</div>';
  try{
    var data=await api('GET','/entity-stats?entity_name='+encodeURIComponent(f.name||f.id));
    renderUsageChart(c,data,f)
  }catch(e){
    renderUsageChart(c,null,f)
  }
}

function renderUsageChart(container,data,f){
  var total7d=0,success7d=0,fail7d=0;
  var points=[];
  if(data&&data.stats){
    var stats=data.stats;
    if(stats.recent_requests)points=stats.recent_requests;
    if(stats.total_7d!=null)total7d=stats.total_7d;
    if(stats.success_7d!=null)success7d=stats.success_7d;
    if(stats.failed_7d!=null)fail7d=stats.failed_7d
  }
  if(!total7d&&points.length){
    for(var i=0;i<points.length;i++){
      total7d+=(points[i].success||0)+(points[i].failed||0);
      success7d+=points[i].success||0;
      fail7d+=points[i].failed||0
    }
  }
  if(!points.length&&f){
    if(f.recent_requests)points=f.recent_requests;
    total7d=total7d||(f.success||0)+(f.failed||0);
    success7d=success7d||(f.success||0);
    fail7d=fail7d||(f.failed||0)
  }
  if(f){
    var infoHtml='';
    if(f.quota&&f.quota.exceeded){
      infoHtml+='<div style="padding:8px 12px;background:var(--warning-bg);border:1px solid var(--warning);border-radius:6px;margin-bottom:12px;font-size:13px">';
      infoHtml+='<strong style="color:var(--warning)">额度超限</strong>';
      if(f.quota.reason)infoHtml+=' <span style="color:var(--text2)">('+escHtml(f.quota.reason)+')</span>';
      if(f.quota.next_recover_at){
        var rd=new Date(f.quota.next_recover_at);
        if(!isNaN(rd.getTime())){
          var rdiff=rd-new Date();
          if(rdiff>0)infoHtml+=' <span style="color:var(--text2)">预计恢复: '+fmtTime(f.quota.next_recover_at)+'</span>'
        }
      }
      infoHtml+='</div>'
    }
    if(f.id_token){
      infoHtml+='<div style="padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;margin-bottom:12px;font-size:12px;display:flex;flex-wrap:wrap;gap:12px">';
      if(f.id_token.plan_type)infoHtml+='<span><strong>Plan:</strong> '+escHtml(f.id_token.plan_type)+'</span>';
      if(f.id_token.chatgpt_subscription_active_until){
        var subEnd=f.id_token.chatgpt_subscription_active_until;
        var subDate=new Date(subEnd);
        var subStr=isNaN(subDate.getTime())?escHtml(String(subEnd)):subDate.toLocaleDateString();
        var isExpired=!isNaN(subDate.getTime())&&subDate<new Date();
        infoHtml+='<span><strong>订阅到期:</strong> '+(isExpired?'<span style="color:var(--danger)">'+subStr+' (已过期)</span>':subStr)+'</span>'
      }
      if(f.id_token.chatgpt_account_id)infoHtml+='<span style="color:var(--text2)"><strong>Account:</strong> '+escHtml(f.id_token.chatgpt_account_id)+'</span>';
      infoHtml+='</div>'
    }
    if(f.unavailable&&!f.disabled){
      infoHtml+='<div style="padding:6px 12px;background:var(--danger-bg);border:1px solid var(--danger);border-radius:6px;margin-bottom:12px;font-size:12px;color:var(--danger)">账号标记为不可用</div>'
    }
    if(infoHtml)html+=infoHtml
  }
  var html='<div class="stat-cards">';
  html+='<div class="stat-card"><div class="stat-value">'+total7d+'</div><div class="stat-label">7日总请求</div></div>';
  html+='<div class="stat-card"><div class="stat-value" style="color:var(--success)">'+success7d+'</div><div class="stat-label">成功</div></div>';
  html+='<div class="stat-card"><div class="stat-value" style="color:var(--danger)">'+fail7d+'</div><div class="stat-label">失败</div></div>';
  var r5h=get5hCount(f);
  html+='<div class="stat-card"><div class="stat-value" style="color:var(--info)">'+r5h+'</div><div class="stat-label">近3h请求</div></div>';
  html+='</div>';
  html+='<div class="time-toggle">';
  html+='<button class="tt-btn'+(usageTimeWindow==='5h'?' active':'')+'" onclick="usageTimeWindow=\'5h\';renderUsageChartRefresh()">5小时</button>';
  html+='<button class="tt-btn'+(usageTimeWindow==='week'?' active':'')+'" onclick="usageTimeWindow=\'week\';renderUsageChartRefresh()">一周</button>';
  html+='</div>';
  if(!points.length){
    html+='<div class="empty"><p>暂无使用数据</p></div>'
  }else{
    var maxVal=1;
    for(var i=0;i<points.length;i++){
      var total=(points[i].success||0)+(points[i].failed||0);
      if(total>maxVal)maxVal=total
    }
    html+='<div class="chart-container">';
    for(var i=0;i<points.length;i++){
      var r=points[i];
      var succ=r.success||0;
      var fail=r.failed||0;
      var succH=Math.round((succ/maxVal)*100);
      var failH=Math.round((fail/maxVal)*100);
      var label='-';
      if(r.time){var d=new Date(r.time);if(!isNaN(d))label=d.toLocaleString('zh-CN',{hour:'2-digit',minute:'2-digit'})}
      html+='<div class="chart-bar"><div class="chart-bar-inner">';
      html+='<div class="chart-seg chart-seg-ok" style="height:'+succH+'%" title="成功: '+succ+'"></div>';
      html+='<div class="chart-seg chart-seg-fail" style="height:'+failH+'%" title="失败: '+fail+'"></div>';
      html+='</div><span class="chart-label">'+label+'</span></div>'
    }
    html+='</div>';
    html+='<div style="display:flex;gap:16px;margin-top:8px;font-size:12px;color:var(--text2)">';
    html+='<span><span class="dot dot-ok"></span> 成功</span>';
    html+='<span><span class="dot dot-err"></span> 失败</span>';
    html+='</div>'
  }
  container.innerHTML=html
}

function renderUsageChartRefresh(){
  if(!detailFile)return;
  var c=document.getElementById('dsub0');
  if(!c)return;
  loadUsageData(detailFile)
}

function renderFields(f){
  var c=document.getElementById('dsub1');
  if(!c)return;
  var fname=f.name||f.id||'';
  var rows=[
    {label:'标签',value:f.label||'',editable:true,key:'label'},
    {label:'优先级',value:f.priority!=null?f.priority:'',editable:true,key:'priority'},
    {label:'备注',value:f.note||'',editable:true,key:'note'},
    {label:'前缀 (proxy_url)',value:f.prefix||f.proxy_url||'',editable:true,key:'prefix'},
    {label:'通道名称',value:f.channel_name||'',editable:true,key:'channel_name'},
    {label:'ID',value:f.id||'-',editable:false},
    {label:'路径',value:f.path||'-',editable:false},
    {label:'邮箱',value:f.email||'-',editable:false},
    {label:'Base URL',value:f.base_url||'-',editable:false},
    {label:'账号类型',value:f.account_type||'-',editable:false},
    {label:'项目 ID',value:f.project_id||'-',editable:false},
    {label:'状态消息',value:f.status_message||'-',editable:false},
    {label:'禁用',value:f.disabled?'是':'否',editable:false},
    {label:'不可用',value:f.unavailable?'是':'否',editable:false},
    {label:'额度超限',value:(f.quota&&f.quota.exceeded)?'是 (backoff: '+(f.quota.backoff_level||0)+')':'否',editable:false},
    {label:'预计恢复',value:(f.quota&&f.quota.next_recover_at&&String(f.quota.next_recover_at)!=='0001-01-01T00:00:00Z')?fmtTime(f.quota.next_recover_at):'-',editable:false},
    {label:'创建时间',value:fmtTimeFull(f.created_at),editable:false},
    {label:'更新时间',value:fmtTimeFull(f.updated_at),editable:false}
  ];
  var html='';
  for(var i=0;i<rows.length;i++){
    var r=rows[i];
    if(r.editable){
      html+='<div class="field-row"><span class="field-label">'+r.label+'</span>';
      html+='<input class="field-input" id="field_'+r.key+'" value="'+escAttr(String(r.value))+'">';
      html+='<button class="btn btn-xs btn-primary" onclick="saveField(\''+escAttr(fname)+'\',\''+r.key+'\')">保存</button></div>'
    }else{
      html+='<div class="field-row"><span class="field-label">'+r.label+'</span><span class="field-value">'+escHtml(String(r.value))+'</span></div>'
    }
  }
  html+='<div class="tags-section">';
  html+='<h4>自定义标签</h4>';
  var customTags=f.custom_tags||f.tags||[];
  html+='<div id="customTagsWrap">';
  for(var i=0;i<customTags.length;i++){
    html+='<span class="tag">'+escHtml(customTags[i])+'<span class="tag-remove" onclick="removeCustomTag(\''+escAttr(fname)+'\','+i+')">&times;</span></span>'
  }
  html+='</div>';
  if(customTags.length<3){
    html+='<div class="add-tag-row">';
    html+='<input id="newCustomTag" placeholder="输入标签..." maxlength="20">';
    html+='<button class="btn btn-xs btn-primary" onclick="addCustomTag(\''+escAttr(fname)+'\')">添加</button>';
    html+='</div>'
  }else{
    html+='<p style="color:var(--text3);font-size:11px;margin-top:4px">最多 3 个标签</p>'
  }
  html+='</div>';
  html+='<div class="tags-section">';
  html+='<h4>显示标签</h4>';
  var displayTags=f.display_tags||[];
  var allDisplayOpts=['email','provider','plan','priority','status','label'];
  html+='<div class="tags-display">';
  for(var i=0;i<allDisplayOpts.length;i++){
    var opt=allDisplayOpts[i];
    var checked=displayTags.indexOf(opt)>=0?'checked':'';
    html+='<label><input type="checkbox" value="'+opt+'" '+(checked)+' onchange="toggleDisplayTag(\''+escAttr(fname)+'\',\''+opt+'\',this.checked)"> '+opt+'</label>'
  }
  html+='</div></div>';
  c.innerHTML=html
}

async function saveField(name,key){
  var el=document.getElementById('field_'+key);
  if(!el)return;
  var body={name:name};
  if(key==='label')body.label=el.value;
  else if(key==='priority'){var v=parseInt(el.value,10);if(!isNaN(v))body.priority=v;else{toast('请输入有效数字',false);return}}
  else if(key==='note')body.note=el.value;
  else if(key==='prefix')body.prefix=el.value;
  else if(key==='channel_name')body.channel_name=el.value;
  try{
    await api('PATCH','/auth-files/fields',body);
    toast('已保存',true);
    loadData()
  }catch(e){toast('保存失败: '+e.message,false)}
}

async function addCustomTag(name){
  var input=document.getElementById('newCustomTag');
  if(!input||!input.value.trim()){toast('请输入标签',false);return}
  var f=files.find(function(x){return(x.name||x.id)===name});
  var tags=f?(f.custom_tags||f.tags||[]):[];
  if(tags.length>=3){toast('最多 3 个标签',false);return}
  tags.push(input.value.trim());
  try{
    await api('PATCH','/auth-files/fields',{name:name,custom_tags:tags});
    toast('标签已添加',true);
    loadData();
    if(detailFile&&(detailFile.name||detailFile.id)===name){
      detailFile.custom_tags=tags;
      renderFields(detailFile)
    }
  }catch(e){toast('添加失败: '+e.message,false)}
}

async function removeCustomTag(name,idx){
  var f=files.find(function(x){return(x.name||x.id)===name});
  var tags=f?(f.custom_tags||f.tags||[]):[];
  tags.splice(idx,1);
  try{
    await api('PATCH','/auth-files/fields',{name:name,custom_tags:tags});
    toast('标签已移除',true);
    loadData();
    if(detailFile&&(detailFile.name||detailFile.id)===name){
      detailFile.custom_tags=tags;
      renderFields(detailFile)
    }
  }catch(e){toast('移除失败: '+e.message,false)}
}

async function toggleDisplayTag(name,opt,checked){
  var f=files.find(function(x){return(x.name||x.id)===name});
  var tags=f?(f.display_tags||[]):[];
  if(checked){if(tags.indexOf(opt)<0)tags.push(opt)}
  else{tags=tags.filter(function(t){return t!==opt})}
  try{
    await api('PATCH','/auth-files/fields',{name:name,display_tags:tags});
    toast('已更新',true);
    loadData();
    if(detailFile&&(detailFile.name||detailFile.id)===name){
      detailFile.display_tags=tags;
      renderFields(detailFile)
    }
  }catch(e){toast('更新失败: '+e.message,false)}
}

async function loadModelsForFile(f){
  var c=document.getElementById('dsub2');
  if(!c)return;
  c.innerHTML='<div class="loading">加载模型列表...</div>';
  try{
    var data=await api('GET','/auth-files/models?name='+encodeURIComponent(f.name||f.id));
    var models=data.models||[];
    renderModelsList(c,models,f)
  }catch(e){c.innerHTML='<div class="empty"><p>加载模型失败: '+escHtml(e.message)+'</p></div>'}
}

function renderModelsList(container,models,f){
  if(!models.length){container.innerHTML='<div class="empty"><p>未找到模型</p></div>';return}
  var html='<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">';
  html+='<span style="font-size:13px;color:var(--text2)">共 '+models.length+' 个模型</span>';
  html+='<button class="btn btn-sm btn-primary" onclick="showImportModelsModal(\''+escAttr(f.name||f.id)+'\')">导入模型</button>';
  html+='</div>';
  html+='<div class="model-list">';
  for(var i=0;i<models.length;i++){
    var m=models[i];
    var display=m.display_name||m.id||m.name||'';
    var owned=m.owned_by||'';
    html+='<div class="model-item"><input type="checkbox" checked disabled> <span>'+escHtml(display)+'</span>';
    if(owned)html+='<span style="color:var(--text2);font-size:11px">'+escHtml(owned)+'</span>';
    html+='</div>'
  }
  html+='</div>';
  container.innerHTML=html
}

function showImportModelsModal(targetName){
  var m=document.createElement('div');m.className='modal-overlay';m.id='importModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal modal-lg"><h3>导入模型</h3>'+
    '<p style="color:var(--text2);font-size:12px;margin-bottom:12px">从其他通道选择模型导入到 "'+escHtml(targetName)+'"</p>'+
    '<input class="import-search" id="importSearch" placeholder="搜索模型..." oninput="filterImportModels()">'+
    '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">'+
    '<span style="font-size:12px;color:var(--text2)"><input type="checkbox" id="importSelectAll" onchange="toggleImportSelectAll(this.checked)"> 全选</span>'+
    '<span id="importSelectedCount" style="font-size:12px;color:var(--primary)">已选 0 个</span>'+
    '</div>'+
    '<div class="import-list" id="importList"><div class="loading">加载中...</div></div>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'importModal\')">取消</button>'+
    '<button class="btn btn-primary" onclick="submitImportModels(\''+escAttr(targetName)+'\')">导入</button>'+
    '</div></div>';
  document.body.appendChild(m);
  loadImportModels()
}

var importModelsData=[];

async function loadImportModels(){
  try{
    var allModels=[];
    for(var i=0;i<files.length;i++){
      var f=files[i];
      try{
        var data=await api('GET','/auth-files/models?name='+encodeURIComponent(f.name||f.id));
        var models=data.models||[];
        for(var j=0;j<models.length;j++){
          var mid=models[j].id||models[j].name||'';
          if(mid&&!allModels.find(function(x){return x.id===mid})){
            allModels.push({id:mid,display_name:models[j].display_name||mid,owned_by:models[j].owned_by||'',source:f.name||f.id})
          }
        }
      }catch(e){}
    }
    importModelsData=allModels;
    renderImportModels(allModels)
  }catch(e){document.getElementById('importList').innerHTML='<div class="empty"><p>加载失败</p></div>'}
}

function renderImportModels(models){
  var c=document.getElementById('importList');
  if(!models.length){c.innerHTML='<div class="empty"><p>无可用模型</p></div>';return}
  var html='';
  for(var i=0;i<models.length;i++){
    var m=models[i];
    html+='<div class="import-item" onclick="this.querySelector(\'input\').click()">';
    html+='<input type="checkbox" class="import-check" data-id="'+escAttr(m.id)+'" onclick="event.stopPropagation();updateImportCount()">';
    html+='<span class="ii-name">'+escHtml(m.display_name||m.id)+'</span>';
    html+='<span class="ii-owner">'+escHtml(m.owned_by||'')+'</span>';
    html+='</div>'
  }
  c.innerHTML=html
}

function filterImportModels(){
  var q=document.getElementById('importSearch').value.toLowerCase();
  var filtered=importModelsData.filter(function(m){
    return(m.display_name||m.id||'').toLowerCase().includes(q)||(m.owned_by||'').toLowerCase().includes(q)
  });
  renderImportModels(filtered)
}

function toggleImportSelectAll(checked){
  var checks=document.querySelectorAll('.import-check');
  for(var i=0;i<checks.length;i++)checks[i].checked=checked;
  updateImportCount()
}

function updateImportCount(){
  var checks=document.querySelectorAll('.import-check:checked');
  var el=document.getElementById('importSelectedCount');
  if(el)el.textContent='已选 '+checks.length+' 个'
}

async function submitImportModels(targetName){
  var checks=document.querySelectorAll('.import-check:checked');
  if(!checks.length){toast('请选择模型',false);return}
  var selectedIds=[];
  for(var i=0;i<checks.length;i++)selectedIds.push(checks[i].getAttribute('data-id'));
  try{
    await api('PATCH','/auth-files/fields',{name:targetName,import_models:selectedIds});
    toast('导入成功',true);
    closeModal('importModal');
    var f=files.find(function(x){return(x.name||x.id)===targetName});
    if(f)loadModelsForFile(f)
  }catch(e){toast('导入失败: '+e.message,false)}
}

async function startOAuth(provider){
  var urlMap={
    codex:'/codex-auth-url',
    kimi:'/kimi-auth-url',
    anthropic:'/anthropic-auth-url',
    gemini:'/gemini-cli-auth-url',
    xai:'/xai-auth-url',
    antigravity:'/antigravity-auth-url'
  };
  var path=urlMap[provider];
  if(!path){toast('未知 Provider',false);return}
  try{
    var data=await api('GET',path+'?is_webui=true');
    if(data.url){
      window.open(data.url,'_blank','width=600,height=700');
      toast('OAuth 窗口已打开',true);
      if(data.state)pollOAuthStatus(data.state,provider)
    }else{
      toast('未返回授权 URL',false)
    }
  }catch(e){toast('OAuth 失败: '+e.message,false)}
}

function pollOAuthStatus(state,provider){
  var attempts=0;
  var maxAttempts=120;
  var interval=setInterval(function(){
    attempts++;
    if(attempts>maxAttempts){clearInterval(interval);toast('OAuth 超时',false);return}
    api('GET','/auth-status?state='+encodeURIComponent(state)).then(function(data){
      if(data.status==='ok'){
        clearInterval(interval);
        toast(provider+' OAuth 完成',true);
        loadData()
      }else if(data.status==='error'){
        clearInterval(interval);
        toast('OAuth 错误: '+(data.error||'未知'),false)
      }
    }).catch(function(){})
  },3000)
}

var excludedData={};

async function loadExcludedModels(){
  try{
    var data=await api('GET','/oauth-excluded-models');
    excludedData=data['oauth-excluded-models']||{};
    renderExcludedModels()
  }catch(e){toast('加载失败: '+e.message,false)}
}

function renderExcludedModels(){
  var c=document.getElementById('excludedList');
  var keys=Object.keys(excludedData);
  if(!keys.length){c.innerHTML='<div class="empty"><p>未配置排除模型</p></div>';return}
  var html='';
  for(var i=0;i<keys.length;i++){
    var provider=keys[i];
    var models=excludedData[provider]||[];
    html+='<div class="provider-card">';
    html+='<div class="provider-header" onclick="toggleProvider(this)">';
    html+='<span>'+escHtml(provider)+' ('+models.length+' 个模型)</span>';
    html+='<div style="display:flex;gap:4px;align-items:center">';
    html+='<button class="btn btn-xs btn-danger" onclick="event.stopPropagation();deleteExcludedProvider(\''+escAttr(provider)+'\')">删除</button>';
    html+='<span style="color:var(--text2)">&#9660;</span></div></div>';
    html+='<div class="provider-body" id="exBody_'+i+'">';
    html+='<div style="display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px">';
    for(var j=0;j<models.length;j++){
      html+='<span class="tag">'+escHtml(models[j])+'<span class="tag-remove" onclick="removeExcludedModel(\''+escAttr(provider)+'\',\''+escAttr(models[j])+'\')">&times;</span></span>'
    }
    html+='</div>';
    html+='<div class="add-tag-row">';
    html+='<input id="exAdd_'+i+'" placeholder="模型名称...">';
    html+='<button class="btn btn-xs btn-primary" onclick="addExcludedModel(\''+escAttr(provider)+'\','+i+')">添加</button>';
    html+='</div></div></div>'
  }
  c.innerHTML=html
}

function toggleProvider(el){
  var body=el.nextElementSibling;
  if(body)body.classList.toggle('open')
}

async function addExcludedModel(provider,idx){
  var input=document.getElementById('exAdd_'+idx);
  if(!input||!input.value.trim()){toast('请输入模型名称',false);return}
  var model=input.value.trim();
  var models=excludedData[provider]||[];
  models.push(model);
  try{
    await api('PATCH','/oauth-excluded-models',{provider:provider,models:models});
    toast('已添加',true);
    loadExcludedModels()
  }catch(e){toast('添加失败: '+e.message,false)}
}

async function removeExcludedModel(provider,model){
  var models=excludedData[provider]||[];
  var newModels=models.filter(function(m){return m!==model});
  try{
    if(newModels.length>0){
      await api('PATCH','/oauth-excluded-models',{provider:provider,models:newModels})
    }else{
      await api('DELETE','/oauth-excluded-models?provider='+encodeURIComponent(provider))
    }
    toast('已移除',true);
    loadExcludedModels()
  }catch(e){toast('移除失败: '+e.message,false)}
}

async function deleteExcludedProvider(provider){
  confirmDialog('确认删除 "'+provider+'" 的排除模型配置?',function(){
    api('DELETE','/oauth-excluded-models?provider='+encodeURIComponent(provider)).then(function(){
      toast('已删除',true);
      loadExcludedModels()
    }).catch(function(e){toast('删除失败: '+e.message,false)})
  })
}

function addExcludedProvider(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='exAddModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>添加排除模型 Provider</h3>'+
    '<label>Provider</label><input id="exNewProvider" placeholder="例如 codex">'+
    '<label>模型列表 (逗号分隔)</label><textarea id="exNewModels" placeholder="model-1, model-2"></textarea>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'exAddModal\')">取消</button>'+
    '<button class="btn btn-primary" onclick="submitExcludedProvider()">添加</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitExcludedProvider(){
  var provider=document.getElementById('exNewProvider').value.trim().toLowerCase();
  var modelsStr=document.getElementById('exNewModels').value.trim();
  if(!provider){toast('Provider 必填',false);return}
  var models=modelsStr?modelsStr.split(',').map(function(s){return s.trim()}).filter(function(s){return s}):[];
  try{
    await api('PATCH','/oauth-excluded-models',{provider:provider,models:models});
    toast('已添加',true);
    closeModal('exAddModal');
    loadExcludedModels()
  }catch(e){toast('添加失败: '+e.message,false)}
}

var aliasData={};

async function loadModelAliases(){
  try{
    var data=await api('GET','/oauth-model-alias');
    aliasData=data['oauth-model-alias']||{};
    renderModelAliases()
  }catch(e){toast('加载失败: '+e.message,false)}
}

function renderModelAliases(){
  var c=document.getElementById('aliasList');
  var keys=Object.keys(aliasData);
  if(!keys.length){c.innerHTML='<div class="empty"><p>未配置模型别名</p></div>';return}
  var html='';
  for(var i=0;i<keys.length;i++){
    var channel=keys[i];
    var aliases=aliasData[channel]||[];
    html+='<div class="provider-card">';
    html+='<div class="provider-header" onclick="toggleProvider(this)">';
    html+='<span>'+escHtml(channel)+' ('+aliases.length+' 个别名)</span>';
    html+='<div style="display:flex;gap:4px;align-items:center">';
    html+='<button class="btn btn-xs btn-danger" onclick="event.stopPropagation();deleteAliasChannel(\''+escAttr(channel)+'\')">删除</button>';
    html+='<span style="color:var(--text2)">&#9660;</span></div></div>';
    html+='<div class="provider-body" id="aliasBody_'+i+'">';
    html+='<div style="display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px">';
    for(var j=0;j<aliases.length;j++){
      var a=aliases[j];
      var display='';
      if(typeof a==='object'){display=(a.name||'')+' → '+(a.alias||'');if(a.fork)display+=' (fork)'}
      else display=String(a);
      html+='<span class="tag">'+escHtml(display)+'<span class="tag-remove" onclick="removeAlias(\''+escAttr(channel)+'\','+j+')">&times;</span></span>'
    }
    html+='</div>';
    html+='<div class="add-tag-row">';
    html+='<input id="aliasName_'+i+'" placeholder="模型名称" style="flex:1">';
    html+='<input id="aliasAlias_'+i+'" placeholder="别名" style="flex:1">';
    html+='<button class="btn btn-xs btn-primary" onclick="addAlias(\''+escAttr(channel)+'\','+i+')">添加</button>';
    html+='</div></div></div>'
  }
  c.innerHTML=html
}

async function addAlias(channel,idx){
  var nameEl=document.getElementById('aliasName_'+idx);
  var aliasEl=document.getElementById('aliasAlias_'+idx);
  if(!nameEl||!aliasEl)return;
  var name=nameEl.value.trim();
  var alias=aliasEl.value.trim();
  if(!name||!alias){toast('两个字段都必填',false);return}
  var aliases=aliasData[channel]||[];
  aliases.push({name:name,alias:alias});
  try{
    await api('PATCH','/oauth-model-alias',{channel:channel,aliases:aliases});
    toast('已添加',true);
    loadModelAliases()
  }catch(e){toast('添加失败: '+e.message,false)}
}

async function removeAlias(channel,idx){
  var aliases=aliasData[channel]||[];
  aliases.splice(idx,1);
  try{
    if(aliases.length>0){
      await api('PATCH','/oauth-model-alias',{channel:channel,aliases:aliases})
    }else{
      await api('DELETE','/oauth-model-alias?channel='+encodeURIComponent(channel))
    }
    toast('已移除',true);
    loadModelAliases()
  }catch(e){toast('移除失败: '+e.message,false)}
}

async function deleteAliasChannel(channel){
  confirmDialog('确认删除 "'+channel+'" 的别名配置?',function(){
    api('DELETE','/oauth-model-alias?channel='+encodeURIComponent(channel)).then(function(){
      toast('已删除',true);
      loadModelAliases()
    }).catch(function(e){toast('删除失败: '+e.message,false)})
  })
}

function addAliasChannel(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='aliasAddModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>添加模型别名 Channel</h3>'+
    '<label>Channel</label><input id="aliasNewChannel" placeholder="例如 codex">'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'aliasAddModal\')">取消</button>'+
    '<button class="btn btn-primary" onclick="submitAliasChannel()">添加</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitAliasChannel(){
  var channel=document.getElementById('aliasNewChannel').value.trim().toLowerCase();
  if(!channel){toast('Channel 必填',false);return}
  try{
    await api('PATCH','/oauth-model-alias',{channel:channel,aliases:[]});
    toast('已添加',true);
    closeModal('aliasAddModal');
    loadModelAliases()
  }catch(e){toast('添加失败: '+e.message,false)}
}

if(getToken())loadData();
</script>
</body>
</html>

`

func (h *Handler) ServeAuthFilesPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, authFilesPageHTML)
}
