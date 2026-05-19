package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const authFilesPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Auth Files</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#0f0f0f;--surface:#1a1a1a;--surface2:#242424;--border:#333;--text:#e0e0e0;--text2:#999;--primary:#6366f1;--primary-hover:#818cf8;--danger:#ef4444;--danger-hover:#f87171;--success:#22c55e;--warning:#f59e0b;--info:#3b82f6}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.header{background:var(--surface);border-bottom:1px solid var(--border);padding:12px 20px;display:flex;align-items:center;justify-content:space-between;position:sticky;top:0;z-index:10}
.header h1{font-size:18px;font-weight:600}
.header-actions{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.search-box{background:var(--surface2);border:1px solid var(--border);border-radius:6px;padding:6px 12px;color:var(--text);font-size:13px;width:220px;outline:none}
.search-box:focus{border-color:var(--primary)}
.btn{padding:6px 14px;border-radius:6px;border:1px solid var(--border);background:var(--surface2);color:var(--text);font-size:13px;cursor:pointer;transition:all .15s;display:inline-flex;align-items:center;gap:4px;white-space:nowrap}
.btn:hover{background:var(--border)}
.btn-primary{background:var(--primary);border-color:var(--primary);color:#fff}
.btn-primary:hover{background:var(--primary-hover)}
.btn-danger{background:transparent;border-color:var(--danger);color:var(--danger)}
.btn-danger:hover{background:var(--danger);color:#fff}
.btn-success{background:transparent;border-color:var(--success);color:var(--success)}
.btn-success:hover{background:var(--success);color:#fff}
.btn-sm{padding:3px 10px;font-size:12px}
.btn-xs{padding:2px 8px;font-size:11px}
.tabs{display:flex;background:var(--surface);border-bottom:1px solid var(--border);padding:0 20px}
.tab{padding:10px 20px;cursor:pointer;color:var(--text2);font-size:14px;border-bottom:2px solid transparent;transition:all .15s}
.tab.active{color:var(--primary);border-bottom-color:var(--primary)}
.tab:hover{color:var(--text)}
.tab-content{display:none}
.tab-content.active{display:block}
.toolbar{padding:10px 20px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;background:var(--surface);border-bottom:1px solid var(--border)}
.filter-chip{padding:4px 12px;border-radius:20px;border:1px solid var(--border);background:transparent;color:var(--text2);font-size:12px;cursor:pointer;transition:all .15s}
.filter-chip.active{background:var(--primary);border-color:var(--primary);color:#fff}
.filter-chip:hover{border-color:var(--primary)}
.stats{padding:8px 20px;display:flex;gap:16px;font-size:12px;color:var(--text2);background:var(--surface);border-bottom:1px solid var(--border);flex-wrap:wrap}
.stats span{display:flex;align-items:center;gap:4px}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-ok{background:var(--success)}.dot-err{background:var(--danger)}.dot-dis{background:var(--text2)}.dot-warn{background:var(--warning)}
.table-wrap{overflow-x:auto;padding:0}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;padding:10px 12px;background:var(--surface2);color:var(--text2);font-weight:500;position:sticky;top:0;border-bottom:1px solid var(--border);white-space:nowrap}
td{padding:8px 12px;border-bottom:1px solid var(--border);white-space:nowrap;max-width:200px;overflow:hidden;text-overflow:ellipsis}
tr:hover td{background:var(--surface2)}
tr.selected td{background:rgba(99,102,241,.1)}
.badge{padding:2px 8px;border-radius:10px;font-size:11px;font-weight:500}
.badge-ok{background:rgba(34,197,94,.15);color:var(--success)}
.badge-err{background:rgba(239,68,68,.15);color:var(--danger)}
.badge-dis{background:rgba(153,153,153,.15);color:var(--text2)}
.badge-warn{background:rgba(245,158,11,.15);color:var(--warning)}
.actions{display:flex;gap:4px}
.empty{text-align:center;padding:60px 20px;color:var(--text2)}
.empty p{margin-top:8px;font-size:14px}
.loading{text-align:center;padding:40px;color:var(--text2)}
.modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:100;display:flex;align-items:center;justify-content:center}
.modal{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:24px;width:90%;max-width:560px;max-height:85vh;overflow-y:auto}
.modal-lg{max-width:720px}
.modal h3{margin-bottom:16px;font-size:16px}
.modal label{display:block;margin-bottom:4px;font-size:13px;color:var(--text2)}
.modal input,.modal select,.modal textarea{width:100%;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;margin-bottom:12px;outline:none}
.modal input:focus,.modal select:focus,.modal textarea:focus{border-color:var(--primary)}
.modal textarea{min-height:80px;resize:vertical;font-family:monospace}
.modal-actions{display:flex;gap:8px;justify-content:flex-end;margin-top:16px}
.sub-tabs{display:flex;gap:0;border-bottom:1px solid var(--border);margin-bottom:16px}
.sub-tab{padding:8px 16px;cursor:pointer;color:var(--text2);font-size:13px;border-bottom:2px solid transparent;transition:all .15s}
.sub-tab.active{color:var(--primary);border-bottom-color:var(--primary)}
.sub-tab:hover{color:var(--text)}
.sub-content{display:none}
.sub-content.active{display:block}
.field-row{display:flex;align-items:center;margin-bottom:10px;gap:8px}
.field-label{color:var(--text2);font-size:13px;min-width:100px;flex-shrink:0}
.field-value{color:var(--text);font-size:13px;word-break:break-all;flex:1}
.field-input{flex:1;padding:6px 10px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;outline:none}
.field-input:focus{border-color:var(--primary)}
.model-list{display:flex;flex-wrap:wrap;gap:6px;max-height:300px;overflow-y:auto;padding:4px 0}
.model-item{padding:4px 10px;border-radius:6px;background:var(--surface2);border:1px solid var(--border);font-size:12px;color:var(--text);display:flex;align-items:center;gap:6px}
.model-item input[type=checkbox]{accent-color:var(--primary)}
.chart-container{display:flex;align-items:flex-end;gap:4px;height:120px;padding:8px 0;border-bottom:1px solid var(--border)}
.chart-bar{display:flex;flex-direction:column;align-items:center;gap:2px;flex:1;min-width:20px}
.chart-bar-inner{width:100%;display:flex;flex-direction:column-reverse;gap:1px}
.chart-seg{width:100%;border-radius:2px;min-height:1px}
.chart-seg-ok{background:var(--success)}
.chart-seg-fail{background:var(--danger)}
.chart-label{font-size:9px;color:var(--text2);white-space:nowrap}
.provider-card{background:var(--surface2);border:1px solid var(--border);border-radius:8px;margin-bottom:8px;overflow:hidden}
.provider-header{padding:12px 16px;cursor:pointer;display:flex;align-items:center;justify-content:space-between;font-size:14px}
.provider-header:hover{background:var(--border)}
.provider-body{padding:0 16px 12px;display:none}
.provider-body.open{display:block}
.tag{display:inline-flex;align-items:center;gap:4px;padding:3px 10px;border-radius:14px;background:var(--surface2);border:1px solid var(--border);font-size:12px;color:var(--text);margin:2px}
.tag-remove{cursor:pointer;color:var(--text2);font-size:14px;line-height:1}
.tag-remove:hover{color:var(--danger)}
.add-tag-row{display:flex;gap:6px;margin-top:8px}
.add-tag-row input{flex:1;padding:5px 10px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:12px;outline:none}
.add-tag-row input:focus{border-color:var(--primary)}
.batch-bar{padding:8px 20px;display:flex;gap:8px;align-items:center;background:var(--surface2);border-bottom:1px solid var(--border);font-size:13px}
.batch-bar .count{color:var(--primary);font-weight:600}
.oauth-section{padding:16px 20px}
.oauth-grid{display:flex;gap:8px;flex-wrap:wrap;margin-top:8px}
.toast{position:fixed;top:20px;right:20px;padding:10px 20px;border-radius:8px;font-size:13px;z-index:200;animation:fadeIn .2s;max-width:400px}
.toast-ok{background:var(--success);color:#fff}
.toast-err{background:var(--danger);color:#fff}
@keyframes fadeIn{from{opacity:0;transform:translateY(-10px)}to{opacity:1;transform:translateY(0)}}
@media(max-width:768px){.search-box{width:140px}.header h1{font-size:15px}td,th{padding:6px 8px;font-size:12px}.modal{width:95%;padding:16px}}
</style>
</head>
<body>
<div class="header">
  <h1>Auth Files</h1>
  <div class="header-actions">
    <input class="search-box" id="search" placeholder="Search..." oninput="render()">
    <button class="btn btn-primary" onclick="showAddModal()">+ Add</button>
    <button class="btn" onclick="showUploadModal()">Upload</button>
    <button class="btn" onclick="loadData()">Refresh</button>
  </div>
</div>
<div class="tabs">
  <div class="tab active" onclick="switchTab(0)">File Management</div>
  <div class="tab" onclick="switchTab(1)">Excluded Models</div>
  <div class="tab" onclick="switchTab(2)">Model Aliases</div>
</div>
<div class="tab-content active" id="tab0">
  <div class="toolbar" id="toolbar"></div>
  <div class="stats" id="stats"></div>
  <div class="batch-bar" id="batchBar" style="display:none">
    <span>Selected <span class="count" id="selCount">0</span> items:</span>
    <button class="btn btn-sm btn-success" onclick="batchToggle(false)">Batch Enable</button>
    <button class="btn btn-sm btn-danger" onclick="batchToggle(true)">Batch Disable</button>
    <button class="btn btn-sm btn-danger" onclick="batchDelete()">Batch Delete</button>
    <button class="btn btn-sm" onclick="deleteAllDisabled()">Delete All Disabled</button>
  </div>
  <div class="oauth-section" style="padding:8px 20px;display:flex;gap:8px;flex-wrap:wrap">
    <button class="btn btn-sm" onclick="startOAuth('codex')">Codex OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('kimi')">Kimi OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('anthropic')">Anthropic OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('gemini')">Gemini CLI OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('xai')">xAI OAuth</button>
    <button class="btn btn-sm" onclick="startOAuth('antigravity')">Vertex Import</button>
  </div>
  <div class="table-wrap">
    <table>
      <thead><tr>
        <th><input type="checkbox" id="selectAll" onchange="toggleSelectAll(this.checked)"></th>
        <th>#</th><th>Name</th><th>Provider</th><th>Status</th><th>Email/Account</th><th>Plan</th><th>Success</th><th>Failed</th><th>Last Refresh</th><th>Actions</th>
      </tr></thead>
      <tbody id="tbody"></tbody>
    </table>
  </div>
  <div class="loading" id="loading">Loading...</div>
  <div class="empty" id="empty" style="display:none"><p>No auth files found</p></div>
</div>
<div class="tab-content" id="tab1">
  <div style="padding:20px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h3 style="font-size:16px">Excluded Models</h3>
      <div style="display:flex;gap:8px">
        <button class="btn btn-sm" onclick="addExcludedProvider()">+ Add Provider</button>
        <button class="btn btn-sm" onclick="loadExcludedModels()">Refresh</button>
      </div>
    </div>
    <div id="excludedList"></div>
  </div>
</div>
<div class="tab-content" id="tab2">
  <div style="padding:20px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h3 style="font-size:16px">Model Aliases</h3>
      <div style="display:flex;gap:8px">
        <button class="btn btn-sm" onclick="addAliasChannel()">+ Add Channel</button>
        <button class="btn btn-sm" onclick="loadModelAliases()">Refresh</button>
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

function getToken(){
  var p=new URLSearchParams(window.location.search);
  token=p.get('token')||'';
  if(!token){document.getElementById('loading').textContent='Error: token parameter is required';return false}
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

function statusBadge(s,disabled){
  if(disabled)return'<span class="badge badge-dis">Disabled</span>';
  switch(s){
    case'ok':case'active':return'<span class="badge badge-ok">OK</span>';
    case'error':return'<span class="badge badge-err">Error</span>';
    case'rate_limited':return'<span class="badge badge-warn">Rate Limited</span>';
    default:return'<span class="badge badge-warn">'+escHtml(s||'Unknown')+'</span>'
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
  if(f.id_token&&f.id_token.plan_type)return escHtml(f.id_token.plan_type);
  return'-'
}

async function loadData(){
  try{
    var data=await api('GET','/auth-files');
    files=data.files||data||[];
    selectedIds={};
    render()
  }catch(e){toast('Failed: '+e.message,false);document.getElementById('loading').textContent='Failed: '+e.message}
}

function render(){
  document.getElementById('loading').style.display='none';
  var q=document.getElementById('search').value.toLowerCase();
  var list=files.filter(function(f){
    if(filter==='ok'&&f.status!=='ok'&&f.status!=='active')return false;
    if(filter==='error'&&f.status!=='error')return false;
    if(filter==='disabled'&&!f.disabled)return false;
    if(filter==='rate_limited'&&f.status!=='rate_limited')return false;
    if(q){var s=(f.name+f.provider+f.provider_name+f.email+f.type+(f.status||'')+(f.label||'')).toLowerCase();if(!s.includes(q))return false}
    return true
  });
  list.sort(function(a,b){return(a.name||'').localeCompare(b.name||'')});

  var ok=0,err=0,dis=0,rl=0;
  for(var i=0;i<files.length;i++){
    var f=files[i];
    if(f.disabled)dis++;
    else if(f.status==='ok'||f.status==='active')ok++;
    else if(f.status==='error')err++;
    else if(f.status==='rate_limited')rl++
  }
  document.getElementById('stats').innerHTML=
    '<span><span class="dot dot-ok"></span> OK: '+ok+'</span>'+
    '<span><span class="dot dot-err"></span> Error: '+err+'</span>'+
    '<span><span class="dot dot-warn"></span> Rate Limited: '+rl+'</span>'+
    '<span><span class="dot dot-dis"></span> Disabled: '+dis+'</span>'+
    '<span>Total: '+files.length+'</span>';

  var filters=[['all','All'],['ok','OK'],['error','Error'],['rate_limited','Rate Limited'],['disabled','Disabled']];
  document.getElementById('toolbar').innerHTML=filters.map(function(item){
    return'<button class="filter-chip'+(filter===item[0]?' active':'')+'" onclick="filter=\''+item[0]+'\';render()">'+item[1]+'</button>'
  }).join('');

  var tbody=document.getElementById('tbody');
  if(!list.length){tbody.innerHTML='';document.getElementById('empty').style.display='';updateBatchBar();return}
  document.getElementById('empty').style.display='none';

  var rows=[];
  for(var i=0;i<list.length;i++){
    var f=list[i];
    var idx=f.auth_index!=null?f.auth_index:'-';
    var name=f.name||f.id||'-';
    var prov=f.provider_name||f.provider||f.type||'-';
    var email=f.email||f.account||'-';
    var succ=f.success||0;
    var fail=f.failed||0;
    var lr=f.last_refresh||f.updated_at||'-';
    var fid=escHtml(f.id||'');
    var checked=selectedIds[f.id]?'checked':'';
    rows.push('<tr class="'+(selectedIds[f.id]?'selected':'')+'">'+
      '<td><input type="checkbox" data-id="'+fid+'" class="row-check" '+(checked)+' onchange="toggleSelect(\''+fid+'\',this.checked)"></td>'+
      '<td>'+idx+'</td>'+
      '<td title="'+escHtml(name)+'" style="cursor:pointer" onclick="showDetail(\''+fid+'\')">'+escHtml(name)+'</td>'+
      '<td>'+escHtml(prov)+'</td>'+
      '<td>'+statusBadge(f.status,f.disabled)+'</td>'+
      '<td title="'+escHtml(email)+'">'+escHtml(email)+'</td>'+
      '<td>'+getPlan(f)+'</td>'+
      '<td>'+succ+'</td>'+
      '<td>'+fail+'</td>'+
      '<td>'+fmtTime(lr)+'</td>'+
      '<td class="actions">'+
        (f.disabled?'<button class="btn btn-xs btn-success" onclick="toggleStatus(\''+fid+'\',false)">Enable</button>':'<button class="btn btn-xs" onclick="toggleStatus(\''+fid+'\',true)">Disable</button>')+
        '<button class="btn btn-xs btn-danger" onclick="deleteFile(\''+fid+'\',\''+escHtml(name).replace(/'/g,"\\'")+'\')">Delete</button>'+
      '</td></tr>')
  }
  tbody.innerHTML=rows.join('');
  updateBatchBar()
}

function toggleSelect(id,checked){
  if(checked)selectedIds[id]=true;
  else delete selectedIds[id];
  updateBatchBar();
  render()
}

function toggleSelectAll(checked){
  var q=document.getElementById('search').value.toLowerCase();
  for(var i=0;i<files.length;i++){
    var f=files[i];
    if(filter==='ok'&&f.status!=='ok'&&f.status!=='active')continue;
    if(filter==='error'&&f.status!=='error')continue;
    if(filter==='disabled'&&!f.disabled)continue;
    if(filter==='rate_limited'&&f.status!=='rate_limited')continue;
    if(q){var s=(f.name+f.provider+f.provider_name+f.email+f.type+(f.status||'')).toLowerCase();if(!s.includes(q))continue}
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
    toast(disable?'Batch disabled':'Batch enabled',true);
    selectedIds={};
    loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function batchDelete(){
  var ids=Object.keys(selectedIds);
  if(!ids.length)return;
  if(!confirm('Delete '+ids.length+' selected file(s)?'))return;
  var promises=[];
  for(var i=0;i<ids.length;i++){
    var f=files.find(function(x){return x.id===ids[i]});
    if(!f)continue;
    var name=f.name||f.id;
    promises.push(api('DELETE','/auth-files?name='+encodeURIComponent(name)))
  }
  try{
    await Promise.all(promises);
    toast('Batch deleted',true);
    selectedIds={};
    loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function deleteAllDisabled(){
  if(!confirm('Delete all disabled auth files?'))return;
  try{
    await api('DELETE','/auth-files?all=true');
    toast('All disabled files deleted',true);
    selectedIds={};
    loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function toggleStatus(id,disable){
  var f=files.find(function(x){return x.id===id});
  if(!f)return;
  var name=f.name||f.id;
  try{
    await api('PATCH','/auth-files/status',{name:name,disabled:disable});
    toast(disable?'Disabled':'Enabled',true);loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function deleteFile(id,name){
  if(!confirm('Delete "'+name+'"?'))return;
  try{await api('DELETE','/auth-files?name='+encodeURIComponent(name));toast('Deleted',true);loadData()}
  catch(e){toast('Failed: '+e.message,false)}
}

function showAddModal(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='addModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>Add Auth File</h3>'+
    '<label>Provider</label><select id="addType">'+
    '<option value="anthropic">Anthropic (Claude)</option>'+
    '<option value="openai">OpenAI</option>'+
    '<option value="gemini">Gemini</option>'+
    '<option value="codex">Codex</option>'+
    '<option value="xai">xAI (Grok)</option>'+
    '<option value="kimi">Kimi</option>'+
    '<option value="openai-compatibility">OpenAI Compatible</option>'+
    '</select>'+
    '<div id="addFields"></div>'+
    '<div id="addCompatFields" style="display:none"></div>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'addModal\')">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitAdd()">Add</button>'+
    '</div></div>';
  document.body.appendChild(m);
  m.querySelector('#addType').onchange=function(){
    var t=this.value;
    var f=m.querySelector('#addFields');
    if(t==='openai-compatibility'){
      f.innerHTML='';
      m.querySelector('#addCompatFields').style.display='';
      m.querySelector('#addCompatFields').innerHTML=
        '<label>Compat Name</label><input id="addCompatName" placeholder="e.g. deepseek">'+
        '<label>Base URL</label><input id="addBaseUrl" placeholder="https://api.deepseek.com/v1">'+
        '<label>API Key</label><textarea id="addApiKey" placeholder="sk-..."></textarea>'
    }else{
      m.querySelector('#addCompatFields').style.display='none';m.querySelector('#addCompatFields').innerHTML='';
      if(t==='anthropic')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="sk-ant-..."></textarea>';
      else if(t==='openai')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="sk-..."></textarea>';
      else if(t==='gemini')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="AIza..."></textarea>';
      else if(t==='codex')f.innerHTML='<p style="color:var(--text2);font-size:13px">Use OAuth login instead</p>';
      else if(t==='xai')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="xai-..."></textarea>';
      else if(t==='kimi')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="..."></textarea>';
      else f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="Enter key..."></textarea>'
    }
  };
  m.querySelector('#addType').onchange()
}

function closeModal(id){var m=document.getElementById(id);if(m)m.remove()}

async function submitAdd(){
  var type=document.getElementById('addType').value;
  if(type==='openai-compatibility'){
    var name=document.getElementById('addCompatName').value.trim();
    var base=document.getElementById('addBaseUrl').value.trim();
    var key=document.getElementById('addApiKey').value.trim();
    if(!name||!base||!key){toast('All fields required',false);return}
    var content=JSON.stringify({provider:"openai-compatibility",compat_name:name,base_url:base,api_key:key});
    var blob=new Blob([content],{type:'application/json'});
    var fd=new FormData();fd.append('file',blob,name+'.json');
    try{await apiUpload(fd);toast('Added',true);closeModal('addModal');loadData()}catch(e){toast('Failed: '+e.message,false)}
  }else{
    var keyEl=document.getElementById('addKey');
    var key=keyEl?keyEl.value.trim():'';
    if(!key&&type!=='codex'){toast('Key required',false);return}
    var content=JSON.stringify({provider:type,api_key:key});
    var blob=new Blob([content],{type:'application/json'});
    var fd=new FormData();fd.append('file',blob,type+'-key.json');
    try{await apiUpload(fd);toast('Added',true);closeModal('addModal');loadData()}catch(e){toast('Failed: '+e.message,false)}
  }
}

function showUploadModal(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='uploadModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>Upload Auth File</h3>'+
    '<label>Select file(s)</label><input type="file" id="uploadInput" multiple accept=".json,.txt">'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'uploadModal\')">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitUpload()">Upload</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitUpload(){
  var input=document.getElementById('uploadInput');
  if(!input.files.length){toast('Select file(s)',false);return}
  var fd=new FormData();
  for(var i=0;i<input.files.length;i++)fd.append('file',input.files[i]);
  try{await apiUpload(fd);toast('Uploaded',true);closeModal('uploadModal');loadData()}catch(e){toast('Failed: '+e.message,false)}
}

function showDetail(id){
  var f=files.find(function(x){return x.id===id});
  if(!f)return;
  var m=document.createElement('div');m.className='modal-overlay';m.id='detailModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  var name=escHtml(f.name||f.id||'-');
  m.innerHTML='<div class="modal modal-lg"><h3>'+name+'</h3>'+
    '<div class="sub-tabs">'+
    '<div class="sub-tab active" onclick="switchSubTab(this,0)">Fields</div>'+
    '<div class="sub-tab" onclick="switchSubTab(this,1)">Models</div>'+
    '<div class="sub-tab" onclick="switchSubTab(this,2)">Usage</div>'+
    '</div>'+
    '<div class="sub-content active" id="sub0"></div>'+
    '<div class="sub-content" id="sub1"></div>'+
    '<div class="sub-content" id="sub2"></div>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'detailModal\')">Close</button>'+
    '</div></div>';
  document.body.appendChild(m);
  renderFields(f);
  loadModelsForFile(f);
  renderUsage(f)
}

function switchSubTab(el,idx){
  var parent=el.parentElement;
  var tabs=parent.querySelectorAll('.sub-tab');
  var contents=parent.parentElement.querySelectorAll('.sub-content');
  for(var i=0;i<tabs.length;i++){
    tabs[i].className='sub-tab'+(i===idx?' active':'');
    contents[i].className='sub-content'+(i===idx?' active':'')
  }
}

function renderFields(f){
  var c=document.getElementById('sub0');
  if(!c)return;
  var fname=f.name||f.id||'';
  var rows=[
    {label:'Label',value:f.label||'',editable:true,key:'label'},
    {label:'Priority',value:f.priority!=null?f.priority:'',editable:true,key:'priority'},
    {label:'ID',value:f.id||'-',editable:false},
    {label:'Path',value:f.path||'-',editable:false},
    {label:'Email',value:f.email||'-',editable:false},
    {label:'Base URL',value:f.base_url||'-',editable:false},
    {label:'Account Type',value:f.account_type||'-',editable:false},
    {label:'Project ID',value:f.project_id||'-',editable:false},
    {label:'Status Message',value:f.status_message||'-',editable:false},
    {label:'Created At',value:fmtTimeFull(f.created_at),editable:false},
    {label:'Updated At',value:fmtTimeFull(f.updated_at),editable:false}
  ];
  var html='';
  for(var i=0;i<rows.length;i++){
    var r=rows[i];
    if(r.editable){
      html+='<div class="field-row"><span class="field-label">'+r.label+'</span>';
      html+='<input class="field-input" id="field_'+r.key+'" value="'+escHtml(String(r.value))+'">';
      html+='<button class="btn btn-xs btn-primary" onclick="saveField(\''+escHtml(fname)+'\',\''+r.key+'\')">Save</button></div>'
    }else{
      html+='<div class="field-row"><span class="field-label">'+r.label+'</span><span class="field-value">'+escHtml(String(r.value))+'</span></div>'
    }
  }
  c.innerHTML=html
}

async function saveField(name,key){
  var el=document.getElementById('field_'+key);
  if(!el)return;
  var body={name:name};
  if(key==='label')body.label=el.value;
  else if(key==='priority'){var v=parseInt(el.value,10);if(!isNaN(v))body.priority=v;else{toast('Invalid number',false);return}}
  try{
    await api('PATCH','/auth-files/fields',body);
    toast('Saved',true);
    loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function loadModelsForFile(f){
  var c=document.getElementById('sub1');
  if(!c)return;
  c.innerHTML='<div class="loading">Loading models...</div>';
  try{
    var data=await api('GET','/auth-files/models?name='+encodeURIComponent(f.name||f.id));
    var models=data.models||[];
    if(!models.length){c.innerHTML='<div class="empty"><p>No models found</p></div>';return}
    var html='<div class="model-list">';
    for(var i=0;i<models.length;i++){
      var m=models[i];
      var display=m.display_name||m.id||m.name||'';
      var owned=m.owned_by||'';
      html+='<div class="model-item"><input type="checkbox" checked disabled> <span>'+escHtml(display)+'</span>';
      if(owned)html+='<span style="color:var(--text2);font-size:11px">'+escHtml(owned)+'</span>';
      html+='</div>'
    }
    html+='</div>';
    c.innerHTML=html
  }catch(e){c.innerHTML='<div class="empty"><p>Failed to load models: '+escHtml(e.message)+'</p></div>'}
}

function renderUsage(f){
  var c=document.getElementById('sub2');
  if(!c)return;
  var reqs=f.recent_requests||[];
  if(!reqs.length){c.innerHTML='<div class="empty"><p>No usage data</p></div>';return}
  var maxVal=1;
  for(var i=0;i<reqs.length;i++){
    var total=(reqs[i].success||0)+(reqs[i].failed||0);
    if(total>maxVal)maxVal=total
  }
  var html='<div class="chart-container">';
  for(var i=0;i<reqs.length;i++){
    var r=reqs[i];
    var succ=r.success||0;
    var fail=r.failed||0;
    var succH=Math.round((succ/maxVal)*100);
    var failH=Math.round((fail/maxVal)*100);
    var label='-';
    if(r.time){var d=new Date(r.time);if(!isNaN(d))label=d.toLocaleString('zh-CN',{hour:'2-digit',minute:'2-digit'})}
    html+='<div class="chart-bar"><div class="chart-bar-inner">';
    html+='<div class="chart-seg chart-seg-ok" style="height:'+succH+'%"></div>';
    html+='<div class="chart-seg chart-seg-fail" style="height:'+failH+'%"></div>';
    html+='</div><span class="chart-label">'+label+'</span></div>'
  }
  html+='</div>';
  html+='<div style="display:flex;gap:16px;margin-top:8px;font-size:12px;color:var(--text2)">';
  html+='<span><span class="dot dot-ok"></span> Success</span>';
  html+='<span><span class="dot dot-err"></span> Failed</span>';
  html+='</div>';
  c.innerHTML=html
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
  if(!path){toast('Unknown provider',false);return}
  try{
    var data=await api('GET',path+'?is_webui=true');
    if(data.url){
      window.open(data.url,'_blank','width=600,height=700');
      toast('OAuth window opened',true);
      if(data.state)pollOAuthStatus(data.state,provider)
    }else{
      toast('No auth URL returned',false)
    }
  }catch(e){toast('OAuth failed: '+e.message,false)}
}

function pollOAuthStatus(state,provider){
  var attempts=0;
  var maxAttempts=120;
  var interval=setInterval(function(){
    attempts++;
    if(attempts>maxAttempts){clearInterval(interval);toast('OAuth timed out',false);return}
    api('GET','/auth-status?state='+encodeURIComponent(state)).then(function(data){
      if(data.status==='ok'){
        clearInterval(interval);
        toast(provider+' OAuth completed',true);
        loadData()
      }else if(data.status==='error'){
        clearInterval(interval);
        toast('OAuth error: '+(data.error||'unknown'),false)
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
  }catch(e){toast('Failed: '+e.message,false)}
}

function renderExcludedModels(){
  var c=document.getElementById('excludedList');
  var keys=Object.keys(excludedData);
  if(!keys.length){c.innerHTML='<div class="empty"><p>No excluded models configured</p></div>';return}
  var html='';
  for(var i=0;i<keys.length;i++){
    var provider=keys[i];
    var models=excludedData[provider]||[];
    html+='<div class="provider-card">';
    html+='<div class="provider-header" onclick="toggleProvider(this)">';
    html+='<span>'+escHtml(provider)+' ('+models.length+' models)</span>';
    html+='<div style="display:flex;gap:4px">';
    html+='<button class="btn btn-xs btn-danger" onclick="event.stopPropagation();deleteExcludedProvider(\''+escHtml(provider)+'\')">Delete</button>';
    html+='<span style="color:var(--text2)">&#9660;</span></div></div>';
    html+='<div class="provider-body" id="exBody_'+i+'">';
    html+='<div style="display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px">';
    for(var j=0;j<models.length;j++){
      html+='<span class="tag">'+escHtml(models[j])+'<span class="tag-remove" onclick="removeExcludedModel(\''+escHtml(provider)+'\',\''+escHtml(models[j]).replace(/'/g,"\\'")+'\')">&times;</span></span>'
    }
    html+='</div>';
    html+='<div class="add-tag-row">';
    html+='<input id="exAdd_'+i+'" placeholder="Model name...">';
    html+='<button class="btn btn-xs btn-primary" onclick="addExcludedModel(\''+escHtml(provider)+'\','+i+')">Add</button>';
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
  if(!input||!input.value.trim()){toast('Enter model name',false);return}
  var model=input.value.trim();
  var models=excludedData[provider]||[];
  models.push(model);
  try{
    await api('PATCH','/oauth-excluded-models',{provider:provider,models:models});
    toast('Added',true);
    loadExcludedModels()
  }catch(e){toast('Failed: '+e.message,false)}
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
    toast('Removed',true);
    loadExcludedModels()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function deleteExcludedProvider(provider){
  if(!confirm('Delete excluded models for "'+provider+'"?'))return;
  try{
    await api('DELETE','/oauth-excluded-models?provider='+encodeURIComponent(provider));
    toast('Deleted',true);
    loadExcludedModels()
  }catch(e){toast('Failed: '+e.message,false)}
}

function addExcludedProvider(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='exAddModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>Add Excluded Models Provider</h3>'+
    '<label>Provider</label><input id="exNewProvider" placeholder="e.g. codex">'+
    '<label>Models (comma separated)</label><textarea id="exNewModels" placeholder="model-1, model-2"></textarea>'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'exAddModal\')">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitExcludedProvider()">Add</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitExcludedProvider(){
  var provider=document.getElementById('exNewProvider').value.trim().toLowerCase();
  var modelsStr=document.getElementById('exNewModels').value.trim();
  if(!provider){toast('Provider required',false);return}
  var models=modelsStr?modelsStr.split(',').map(function(s){return s.trim()}).filter(function(s){return s}):[];
  try{
    await api('PATCH','/oauth-excluded-models',{provider:provider,models:models});
    toast('Added',true);
    closeModal('exAddModal');
    loadExcludedModels()
  }catch(e){toast('Failed: '+e.message,false)}
}

var aliasData={};

async function loadModelAliases(){
  try{
    var data=await api('GET','/oauth-model-alias');
    aliasData=data['oauth-model-alias']||{};
    renderModelAliases()
  }catch(e){toast('Failed: '+e.message,false)}
}

function renderModelAliases(){
  var c=document.getElementById('aliasList');
  var keys=Object.keys(aliasData);
  if(!keys.length){c.innerHTML='<div class="empty"><p>No model aliases configured</p></div>';return}
  var html='';
  for(var i=0;i<keys.length;i++){
    var channel=keys[i];
    var aliases=aliasData[channel]||[];
    html+='<div class="provider-card">';
    html+='<div class="provider-header" onclick="toggleProvider(this)">';
    html+='<span>'+escHtml(channel)+' ('+aliases.length+' aliases)</span>';
    html+='<div style="display:flex;gap:4px">';
    html+='<button class="btn btn-xs btn-danger" onclick="event.stopPropagation();deleteAliasChannel(\''+escHtml(channel)+'\')">Delete</button>';
    html+='<span style="color:var(--text2)">&#9660;</span></div></div>';
    html+='<div class="provider-body" id="aliasBody_'+i+'">';
    html+='<div style="display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px">';
    for(var j=0;j<aliases.length;j++){
      var a=aliases[j];
      var display='';
      if(typeof a==='object'){display=(a.name||'')+' -> '+(a.alias||'');if(a.fork)display+=' (fork)'}
      else display=String(a);
      html+='<span class="tag">'+escHtml(display)+'<span class="tag-remove" onclick="removeAlias(\''+escHtml(channel)+'\','+j+')">&times;</span></span>'
    }
    html+='</div>';
    html+='<div class="add-tag-row">';
    html+='<input id="aliasName_'+i+'" placeholder="Model name">';
    html+='<input id="aliasAlias_'+i+'" placeholder="Alias">';
    html+='<button class="btn btn-xs btn-primary" onclick="addAlias(\''+escHtml(channel)+'\','+i+')">Add</button>';
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
  if(!name||!alias){toast('Both fields required',false);return}
  var aliases=aliasData[channel]||[];
  aliases.push({name:name,alias:alias});
  try{
    await api('PATCH','/oauth-model-alias',{channel:channel,aliases:aliases});
    toast('Added',true);
    loadModelAliases()
  }catch(e){toast('Failed: '+e.message,false)}
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
    toast('Removed',true);
    loadModelAliases()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function deleteAliasChannel(channel){
  if(!confirm('Delete aliases for "'+channel+'"?'))return;
  try{
    await api('DELETE','/oauth-model-alias?channel='+encodeURIComponent(channel));
    toast('Deleted',true);
    loadModelAliases()
  }catch(e){toast('Failed: '+e.message,false)}
}

function addAliasChannel(){
  var m=document.createElement('div');m.className='modal-overlay';m.id='aliasAddModal';
  m.onclick=function(e){if(e.target===m)m.remove()};
  m.innerHTML='<div class="modal"><h3>Add Model Alias Channel</h3>'+
    '<label>Channel</label><input id="aliasNewChannel" placeholder="e.g. codex">'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeModal(\'aliasAddModal\')">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitAliasChannel()">Add</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

async function submitAliasChannel(){
  var channel=document.getElementById('aliasNewChannel').value.trim().toLowerCase();
  if(!channel){toast('Channel required',false);return}
  try{
    await api('PATCH','/oauth-model-alias',{channel:channel,aliases:[]});
    toast('Added',true);
    closeModal('aliasAddModal');
    loadModelAliases()
  }catch(e){toast('Failed: '+e.message,false)}
}

if(getToken())loadData();
</script>
</body>
</html>`

func (h *Handler) ServeAuthFilesPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, authFilesPageHTML)
}
