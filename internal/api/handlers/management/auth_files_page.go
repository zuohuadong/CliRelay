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
.header-actions{display:flex;gap:8px;align-items:center}
.search-box{background:var(--surface2);border:1px solid var(--border);border-radius:6px;padding:6px 12px;color:var(--text);font-size:13px;width:220px;outline:none}
.search-box:focus{border-color:var(--primary)}
.btn{padding:6px 14px;border-radius:6px;border:1px solid var(--border);background:var(--surface2);color:var(--text);font-size:13px;cursor:pointer;transition:all .15s;display:inline-flex;align-items:center;gap:4px}
.btn:hover{background:var(--border)}
.btn-primary{background:var(--primary);border-color:var(--primary);color:#fff}
.btn-primary:hover{background:var(--primary-hover)}
.btn-danger{background:transparent;border-color:var(--danger);color:var(--danger)}
.btn-danger:hover{background:var(--danger);color:#fff}
.btn-sm{padding:3px 10px;font-size:12px}
.toolbar{padding:10px 20px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;background:var(--surface);border-bottom:1px solid var(--border)}
.filter-chip{padding:4px 12px;border-radius:20px;border:1px solid var(--border);background:transparent;color:var(--text2);font-size:12px;cursor:pointer;transition:all .15s}
.filter-chip.active{background:var(--primary);border-color:var(--primary);color:#fff}
.filter-chip:hover{border-color:var(--primary)}
.stats{padding:8px 20px;display:flex;gap:16px;font-size:12px;color:var(--text2);background:var(--surface);border-bottom:1px solid var(--border)}
.stats span{display:flex;align-items:center;gap:4px}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-ok{background:var(--success)}.dot-err{background:var(--danger)}.dot-dis{background:var(--text2)}.dot-warn{background:var(--warning)}
.table-wrap{overflow-x:auto;padding:0}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;padding:10px 12px;background:var(--surface2);color:var(--text2);font-weight:500;position:sticky;top:0;border-bottom:1px solid var(--border);white-space:nowrap}
td{padding:8px 12px;border-bottom:1px solid var(--border);white-space:nowrap;max-width:200px;overflow:hidden;text-overflow:ellipsis}
tr:hover td{background:var(--surface2)}
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
.modal{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:24px;width:90%;max-width:480px}
.modal h3{margin-bottom:16px;font-size:16px}
.modal label{display:block;margin-bottom:4px;font-size:13px;color:var(--text2)}
.modal input,.modal select,.modal textarea{width:100%;padding:8px 12px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:13px;margin-bottom:12px;outline:none}
.modal input:focus,.modal select:focus,.modal textarea:focus{border-color:var(--primary)}
.modal textarea{min-height:80px;resize:vertical;font-family:monospace}
.modal-actions{display:flex;gap:8px;justify-content:flex-end;margin-top:16px}
.toast{position:fixed;top:20px;right:20px;padding:10px 20px;border-radius:8px;font-size:13px;z-index:200;animation:fadeIn .2s}
.toast-ok{background:var(--success);color:#fff}
.toast-err{background:var(--danger);color:#fff}
@keyframes fadeIn{from{opacity:0;transform:translateY(-10px)}to{opacity:1;transform:translateY(0)}}
@media(max-width:768px){.search-box{width:140px}.header h1{font-size:15px}td,th{padding:6px 8px;font-size:12px}}
</style>
</head>
<body>
<div class="header">
  <h1>Auth Files</h1>
  <div class="header-actions">
    <input class="search-box" id="search" placeholder="Search..." oninput="render()">
    <button class="btn btn-primary" onclick="showAddModal()">+ Add</button>
    <button class="btn" onclick="loadData()">Refresh</button>
  </div>
</div>
<div class="toolbar" id="toolbar"></div>
<div class="stats" id="stats"></div>
<div class="table-wrap">
  <table>
    <thead><tr>
      <th>#</th><th>Name</th><th>Provider</th><th>Status</th><th>Email</th><th>Success</th><th>Failed</th><th>Last Refresh</th><th>Actions</th>
    </tr></thead>
    <tbody id="tbody"></tbody>
  </table>
</div>
<div class="loading" id="loading">Loading...</div>
<div class="empty" id="empty" style="display:none"><p>No auth files found</p></div>

<script>
const API=window.location.origin+'/v0/management';
let token='',files=[],filter='all',sortBy='auth_index',sortDir=1;

function getToken(){
  const p=new URLSearchParams(window.location.search);
  token=p.get('token')||'';
  if(!token){document.getElementById('loading').textContent='Error: token parameter is required';return false}
  return true
}

function api(method,path,body){
  const opts={method,headers:{'Authorization':'Bearer '+token,'Content-Type':'application/json'}};
  if(body)opts.body=JSON.stringify(body);
  return fetch(API+path,opts).then(r=>{if(!r.ok)throw new Error(r.status+' '+r.statusText);return r.json()})
}

function apiUpload(formData){
  return fetch(API+'/auth-files',{method:'POST',headers:{'Authorization':'Bearer '+token},body:formData}).then(r=>{if(!r.ok)throw new Error(r.status+' '+r.statusText);return r.json()})
}

function toast(msg,ok){const d=document.createElement('div');d.className='toast toast-'+(ok?'ok':'err');d.textContent=msg;document.body.appendChild(d);setTimeout(()=>d.remove(),3000)}

async function loadData(){
  try{
    const data=await api('GET','/auth-files');
    files=data.files||data||[];
    render()
  }catch(e){toast('Failed: '+e.message,false);document.getElementById('loading').textContent='Failed: '+e.message}
}

function statusBadge(s,disabled){
  if(disabled)return'<span class="badge badge-dis">Disabled</span>';
  switch(s){
    case'ok':case'active':return'<span class="badge badge-ok">OK</span>';
    case'error':return'<span class="badge badge-err">Error</span>';
    case'rate_limited':return'<span class="badge badge-warn">Rate Limited</span>';
    default:return'<span class="badge badge-warn">'+(s||'Unknown')+'</span>'
  }
}

function fmtTime(t){if(!t)return'-';const d=new Date(t);if(isNaN(d))return t;return d.toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'})}

function render(){
  document.getElementById('loading').style.display='none';
  const q=document.getElementById('search').value.toLowerCase();
  let list=files.filter(f=>{
    if(filter==='ok'&&f.status!=='ok'&&f.status!=='active')return false;
    if(filter==='error'&&f.status!=='error')return false;
    if(filter==='disabled'&&!f.disabled)return false;
    if(filter==='rate_limited'&&f.status!=='rate_limited')return false;
    if(q){const s=(f.name+f.provider+f.provider_name+f.email+f.type+(f.status||'')).toLowerCase();if(!s.includes(q))return false}
    return true
  });
  list.sort((a,b)=>(a[sortBy]||0)>(b[sortBy]||0)?sortDir:-sortDir);

  const ok=files.filter(f=>!f.disabled&&(f.status==='ok'||f.status==='active')).length;
  const err=files.filter(f=>!f.disabled&&f.status==='error').length;
  const dis=files.filter(f=>f.disabled).length;
  const rl=files.filter(f=>f.status==='rate_limited').length;
  document.getElementById('stats').innerHTML=
    '<span><span class="dot dot-ok"></span> OK: '+ok+'</span>'+
    '<span><span class="dot dot-err"></span> Error: '+err+'</span>'+
    '<span><span class="dot dot-warn"></span> Rate Limited: '+rl+'</span>'+
    '<span><span class="dot dot-dis"></span> Disabled: '+dis+'</span>'+
    '<span>Total: '+files.length+'</span>';

  const filters=[['all','All'],['ok','OK'],['error','Error'],['rate_limited','Rate Limited'],['disabled','Disabled']];
  document.getElementById('toolbar').innerHTML=filters.map(([k,l])=>'<button class="filter-chip'+(filter===k?' active':'')+'" onclick="filter=\''+k+'\';render()">'+l+'</button>').join('');

  const tbody=document.getElementById('tbody');
  if(!list.length){tbody.innerHTML='';document.getElementById('empty').style.display='';return}
  document.getElementById('empty').style.display='none';
  tbody.innerHTML=list.map(f=>{
    const idx=f.auth_index!=null?f.auth_index:'-';
    const name=f.name||f.id||'-';
    const prov=f.provider_name||f.provider||f.type||'-';
    const email=f.email||f.account||'-';
    const succ=f.success||0;
    const fail=f.failed||0;
    const lr=f.last_refresh||f.updated_at||'-';
    const dis=f.disabled;
    return'<tr>'+
      '<td>'+idx+'</td>'+
      '<td title="'+name+'">'+name+'</td>'+
      '<td>'+prov+'</td>'+
      '<td>'+statusBadge(f.status,dis)+'</td>'+
      '<td title="'+email+'">'+email+'</td>'+
      '<td>'+succ+'</td>'+
      '<td>'+fail+'</td>'+
      '<td>'+fmtTime(lr)+'</td>'+
      '<td class="actions">'+
        (dis?'<button class="btn btn-sm" onclick="toggleStatus(\''+f.id+'\',false)">Enable</button>':'<button class="btn btn-sm" onclick="toggleStatus(\''+f.id+'\',true)">Disable</button>')+
        '<button class="btn btn-sm btn-danger" onclick="deleteFile(\''+f.id+'\',\''+name.replace(/'/g,"\\'")+'\')">Delete</button>'+
      '</td></tr>'
  }).join('')
}

async function toggleStatus(id,disable){
  try{
    await api('PATCH','/auth-files/'+id+'/status',{disabled:disable});
    toast(disable?'Disabled':'Enabled',true);loadData()
  }catch(e){toast('Failed: '+e.message,false)}
}

async function deleteFile(id,name){
  if(!confirm('Delete "'+name+'"?'))return;
  try{await api('DELETE','/auth-files/'+id);toast('Deleted',true);loadData()}
  catch(e){toast('Failed: '+e.message,false)}
}

function showAddModal(){
  const m=document.createElement('div');m.className='modal-overlay';m.id='addModal';
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
    '<button class="btn" onclick="closeModal()">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitAdd()">Add</button>'+
    '</div></div>';
  document.body.appendChild(m);
  m.querySelector('#addType').onchange=function(){
    const t=this.value;
    const f=m.querySelector('#addFields');
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
      else if(t==='codex')f.innerHTML='<p style="color:var(--text2);font-size:13px">Use OAuth login below</p>';
      else if(t==='xai')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="xai-..."></textarea>';
      else if(t==='kimi')f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="..."></textarea>';
      else f.innerHTML='<label>API Key</label><textarea id="addKey" placeholder="Enter key..."></textarea>'
    }
  };
  m.querySelector('#addType').onchange()
}

function closeModal(){const m=document.getElementById('addModal');if(m)m.remove()}

async function submitAdd(){
  const type=document.getElementById('addType').value;
  if(type==='openai-compatibility'){
    const name=document.getElementById('addCompatName').value.trim();
    const base=document.getElementById('addBaseUrl').value.trim();
    const key=document.getElementById('addApiKey').value.trim();
    if(!name||!base||!key){toast('All fields required',false);return}
    const content=JSON.stringify({provider:"openai-compatibility",compat_name:name,base_url:base,api_key:key});
    const blob=new Blob([content],{type:'application/json'});
    const fd=new FormData();fd.append('file',blob,name+'.json');
    try{await apiUpload(fd);toast('Added',true);closeModal();loadData()}catch(e){toast('Failed: '+e.message,false)}
  }else{
    const keyEl=document.getElementById('addKey');
    const key=keyEl?keyEl.value.trim():'';
    if(!key&&type!=='codex'){toast('Key required',false);return}
    const content=JSON.stringify({provider:type,api_key:key});
    const blob=new Blob([content],{type:'application/json'});
    const fd=new FormData();fd.append('file',blob,type+'-key.json');
    try{await apiUpload(fd);toast('Added',true);closeModal();loadData()}catch(e){toast('Failed: '+e.message,false)}
  }
}

function showUploadModal(){
  const m=document.createElement('div');m.className='modal-overlay';m.id='uploadModal';
  m.innerHTML='<div class="modal"><h3>Upload Auth File</h3>'+
    '<label>Select file(s)</label><input type="file" id="uploadInput" multiple accept=".json,.txt">'+
    '<div class="modal-actions">'+
    '<button class="btn" onclick="closeUploadModal()">Cancel</button>'+
    '<button class="btn btn-primary" onclick="submitUpload()">Upload</button>'+
    '</div></div>';
  document.body.appendChild(m)
}

function closeUploadModal(){const m=document.getElementById('uploadModal');if(m)m.remove()}

async function submitUpload(){
  const input=document.getElementById('uploadInput');
  if(!input.files.length){toast('Select file(s)',false);return}
  const fd=new FormData();
  for(let i=0;i<input.files.length;i++)fd.append('file',input.files[i]);
  try{await apiUpload(fd);toast('Uploaded',true);closeUploadModal();loadData()}catch(e){toast('Failed: '+e.message,false)}
}

if(getToken())loadData();
</script>
</body>
</html>`

func (h *Handler) ServeAuthFilesPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, authFilesPageHTML)
}
