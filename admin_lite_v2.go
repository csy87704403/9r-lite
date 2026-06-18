package main

import "strings"

func adminHTMLLiteV2(configJSON string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(configJSON)
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>9Router Lite</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}
main{max-width:1120px;margin:32px auto;padding:0 20px}
h1{font-size:28px;margin:0 0 8px}
h2{font-size:18px;margin:26px 0 10px}
.bar{display:flex;gap:10px;align-items:center;margin:14px 0;flex-wrap:wrap}
a,button{font:inherit}
button{background:#111;color:#fff;border:0;border-radius:6px;padding:9px 14px;cursor:pointer}
button.secondary{background:#fff;color:#111;border:1px solid #ddd}
button.small{padding:7px 10px;font-size:13px}
button.add-auto-model{width:17px;height:17px;padding:0;border-radius:3px;font-size:12px;line-height:1;display:inline-flex;align-items:center;justify-content:center;flex:0 0 17px;margin-top:1px}
button:disabled{opacity:.55;cursor:not-allowed}
textarea{width:100%;min-height:340px;box-sizing:border-box;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace;border:1px solid #ddd;border-radius:6px;padding:14px;background:#fff}
.muted{color:#666;font-size:13px}
.ok{color:#047857}
.err{color:#b91c1c}
code{background:#eee;padding:2px 5px;border-radius:4px}
.panel-grid,.api-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:12px;margin:12px 0 20px}
.card,.api-card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}
.card h3,.api-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin:0 0 8px}
.card h3{justify-content:flex-start;font-size:17px}
.api-head strong{font-size:17px}
.api-meta{font-size:12px;color:#666}
.green-dot,.gray-dot{width:9px;height:9px;border-radius:50%;display:inline-block;flex:0 0 auto}
.green-dot{background:#16a34a}.gray-dot{background:#aaa}
.field{display:grid;gap:6px;margin:10px 0}
.field label{font-size:13px;color:#444}
.field input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#fff;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
.field input[readonly]{background:#f3f4f6;color:#374151}
.inline-field{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.inline-field.grow input{flex:1 1 240px}
.inline-field input[type=number]{width:110px}
.key-list{display:grid;gap:8px}
.key-row input{flex:1 1 260px}
.endpoint-grid{display:grid;grid-template-columns:140px minmax(260px,1fr) auto;gap:8px;align-items:center;margin:8px 0}
.endpoint-grid input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#f3f4f6;color:#374151;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
@media(max-width:720px){.endpoint-grid{grid-template-columns:1fr}.endpoint-grid button{width:max-content}}
.toggle{display:flex;gap:8px;align-items:center;font-size:13px;margin:8px 0}
.model-list{display:grid;gap:8px;margin-top:10px;max-height:280px;overflow:auto;padding-right:4px}
.model-item{display:flex;gap:8px;align-items:flex-start;font-size:13px}
.model-item.off{color:#999}
.model-name{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}
.latency{margin-left:6px;font-weight:700}
.latency.good{color:#047857}.latency.warn{color:#a16207}.latency.bad{color:#b91c1c}
.progress-wrap{display:none;align-items:center;gap:10px;margin:8px 0}
.progress-wrap.active{display:flex}
progress{width:190px;height:12px}
details{margin-top:22px}
.section-note{margin:6px 0 12px}
</style>
</head>
<body>
<main>
<h1>9Router Lite</h1>
<div class="muted">接口基址是 <code>/v1</code>，配置保存在本机 <code>data/config.json</code>。</div>

<div class="bar">
<button onclick="saveGateway()">保存网关设置</button>
<button class="secondary" onclick="openModelsPage()">打开模型页</button>
<a href="/health" target="_blank">/health</a>
<span id="status" class="muted"></span>
</div>

<div class="field">
<label>访问密钥和 Base URL</label>
<div class="inline-field grow">
<input id="accessKey" type="password" placeholder="第三方 Agent 访问 /v1 时需要填写这个 key">
<button class="secondary" onclick="toggleAccessKey()" type="button">显示/隐藏</button>
<input id="baseUrl" type="text" readonly>
<button class="secondary" onclick="copyBaseURL()" type="button">复制 Base URL</button>
</div>
</div>

<div class="field">
<label>程序请求地址</label>
<div class="endpoint-grid"><span class="muted">Base URL</span><input id="programBaseUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programBaseUrl','Base URL')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">模型列表</span><input id="programModelsUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programModelsUrl','模型列表地址')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">健康检查</span><input id="programHealthUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programHealthUrl','健康检查地址')" type="button">复制</button></div>
<div class="muted">第三方 Agent 的 Base URL 填 <code>/v1</code>，API Key 填上面的访问密钥；程序健康检查建议使用 JSON 地址。</div>
</div>

<div class="card">
<h3>导入本地 config.json</h3>
<div class="muted">选择本地 Lite 配置文件，或把 config.json 内容粘贴到下面。导入后会覆盖当前服务器的配置。</div>
<div class="field">
<label>选择文件</label>
<input id="importConfigFile" type="file" accept=".json,application/json" onchange="loadImportConfigFile()">
</div>
<div class="field">
<label>配置内容</label>
<textarea id="importConfigText" style="min-height:160px" spellcheck="false" placeholder="把本地 data/config.json 的内容粘贴到这里"></textarea>
</div>
<div class="bar">
<button class="secondary" onclick="previewImportConfig()" type="button">检查配置</button>
<button onclick="importConfigJSON()" type="button">导入并保存</button>
<span id="importConfigStatus" class="muted"></span>
</div>
</div>

<div class="bar">
<label class="toggle"><input type="checkbox" id="autoProbeEnabled"> 启用定时自动探测</label>
<label class="inline-field muted">基础间隔分钟 <input id="autoProbeInterval" type="number" min="1" step="1" value="60"></label>
<span class="muted">实际间隔会在基础间隔的 70% 到 130% 之间随机浮动，探测后自动把可用模型发布到 <code>/v1/models</code>。</span>
</div>

<h2>Auto 模型</h2>
<div class="card">
<label class="toggle"><input type="checkbox" id="autoModelEnabled"> 启用 <code>auto</code> 模型</label>
<div class="field">
<label>候选模型列表</label>
<div class="inline-field grow">
<input id="autoModelInput" placeholder="例如 oc/big-pickle">
<button class="secondary" onclick="addAutoModel()" type="button">添加</button>
</div>
</div>
<div class="muted">第三方 Agent 继续使用上面的 Base URL 和访问密钥，模型名填写 <code>auto</code>。</div>
<div id="autoModelList" class="model-list"></div>
<div class="bar"><button class="secondary" onclick="saveGateway()">保存 Auto 设置</button><span id="autoModelStatus" class="muted"></span></div>
</div>

<div class="bar">
<h2 style="margin:0">已连接源</h2>
<button class="secondary" onclick="probeAllProviders()">一键探测</button>
<button class="secondary" onclick="stopProbe()">停止探测</button>
<span id="probeAllStatus" class="muted"></span>
</div>
<div id="providerStatus" class="panel-grid"></div>

<h2>API 密钥提供商</h2>
<div class="muted section-note">输入 Base URL 和 API Key 后点击拉取模型；模型会直接显示在对应卡片底部，并可在卡片内探测和发布。</div>
<div id="apiProviders" class="api-grid"></div>

<h2>OAuth 提供商</h2>
<div id="probeAllProgress" class="progress-wrap"><progress value="0" max="1"></progress><span class="muted">0/0</span></div>
<div class="muted section-note">一键探测只会自动发布探测可用的模型；你也可以手动勾选探测失败的模型并保存发布列表。</div>
<div id="publishProviders" class="panel-grid"></div>

<details>
<summary>原始配置</summary>
<div class="bar"><button class="secondary" onclick="save()">保存原始配置</button></div>
<textarea id="cfg" spellcheck="false">` + escaped + `</textarea>
</details>

<script>
const fixedAPIProviderIDs=['glm','groq','deepseek','mimo'];
const publishProviderIDs=['oc','mmf','qoder','gemini','kilo','cline'];
const statusProviderIDs=['oc','mmf','qoder','gemini','kilo','cline','glm','groq','deepseek','mimo'];
let probeStopRequested=false;
function parseConfig(){ try { return JSON.parse(document.getElementById('cfg').value); } catch { return null; } }
function setConfig(cfg){
  ensureBlankCustomProvider(cfg);
  if(!cfg.auto_model) cfg.auto_model={enabled:false,models:[]};
  document.getElementById('cfg').value=JSON.stringify(cfg,null,2);
  document.getElementById('accessKey').value=cfg.access_key || '';
  document.getElementById('baseUrl').value=location.origin+'/v1';
  document.getElementById('programBaseUrl').value=location.origin+'/v1';
  document.getElementById('programModelsUrl').value=location.origin+'/v1/models';
  document.getElementById('programHealthUrl').value=location.origin+'/health?format=json';
  document.getElementById('autoProbeEnabled').checked=!!cfg.auto_probe_enabled;
  document.getElementById('autoProbeInterval').value=cfg.auto_probe_interval_minutes || 60;
  document.getElementById('autoModelEnabled').checked=!!cfg.auto_model.enabled;
  renderAutoModels(cfg);
}
function providerConnected(p){
  if(!p || !p.enabled) return false;
  if(providerAPIKeys(p).length || p.access_token || p.type === 'opencode-free' || p.type === 'mimo-free') return true;
  return isCustomProvider(p) && !!((p.base_url || '').trim()) && p.base_url !== 'https://example.com/v1';
}
function authStatus(p){ return (p && p.provider_specific_data && p.provider_specific_data.authStatus) || 'ok'; }
function authError(p){ return (p && p.provider_specific_data && p.provider_specific_data.lastAuthError) || ''; }
function manualOverride(p){ return !!(p && p.provider_specific_data && p.provider_specific_data.manualPublishOverride==='true'); }
function isCustomProvider(p){ return !!(p && p.type==='openai' && /^custom/.test(p.id || '')); }
function providerAPIKeys(p){ return unique([((p && p.api_key) || ''), ...((p && Array.isArray(p.api_keys)) ? p.api_keys : [])].map(x=>String(x || '').trim()).filter(Boolean)); }
function customHasContent(p){ return !!(p && (providerAPIKeys(p).length || ((p.base_url || '').trim() && p.base_url !== 'https://example.com/v1') || (p.provider_specific_data && p.provider_specific_data.apiModelsFetched==='true'))); }
function nextCustomID(cfg){ const ids=new Set((cfg.providers || []).map(p=>p.id)); let i=1; while(ids.has(i===1?'custom':'custom'+i)) i++; return i===1?'custom':'custom'+i; }
function ensureBlankCustomProvider(cfg){
  if(!Array.isArray(cfg.providers)) cfg.providers=[];
  let blankKept=false;
  cfg.providers=cfg.providers.filter(p=>{
    if(!isCustomProvider(p) || customHasContent(p)) return true;
    if(blankKept) return false;
    p.name=p.name || 'Custom OpenAI Compatible';
    p.type='openai';
    p.enabled=false;
    p.base_url='';
    p.models=[];
    p.provider_specific_data={...(p.provider_specific_data || {}), customProvider:'true'};
    blankKept=true;
    return true;
  });
  if(!blankKept) cfg.providers.push({id:nextCustomID(cfg),name:'Custom OpenAI Compatible',type:'openai',enabled:false,base_url:'',models:[],provider_specific_data:{customProvider:'true'}});
  return cfg;
}
function apiProviderIDs(cfg){ return [...fixedAPIProviderIDs, ...(cfg.providers || []).filter(isCustomProvider).map(p=>p.id)]; }
function esc(v){ return String(v || '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;'); }
function unique(arr){ return [...new Set((arr || []).filter(Boolean))]; }
function selectedModels(p){ if(manualOverride(p) && (!p.enabled_models || !p.enabled_models.length)) return []; return unique((p && p.enabled_models && p.enabled_models.length) ? p.enabled_models : (p && p.models) || []); }
function availableModels(p){ return unique((p && p.available_models) || []); }
function apiModelsLoaded(p){ return !!(p && p.provider_specific_data && p.provider_specific_data.apiModelsFetched==='true' && Array.isArray(p.models) && p.models.length); }
function visibleModels(p){
  const selected=selectedModels(p);
  if(manualOverride(p)) return selected;
  const available=availableModels(p);
  if(!p || (!p.availability_checked_at && !available.length)) return selected;
  const set=new Set(available);
  return selected.filter(x=>set.has(x));
}
function autoModels(cfg){ return unique((cfg && cfg.auto_model && cfg.auto_model.models) || []); }
function renderAutoModels(cfg){
  const root=document.getElementById('autoModelList'); if(!root) return;
  const models=autoModels(cfg);
  root.innerHTML=models.length ? models.map((model,i)=>'<div class="model-item"><span class="model-name">'+esc(model)+'</span><button class="small secondary" onclick="moveAutoModel('+i+',-1)" '+(i===0?'disabled':'')+'>上移</button><button class="small secondary" onclick="moveAutoModel('+i+',1)" '+(i===models.length-1?'disabled':'')+'>下移</button><button class="small secondary" onclick="removeAutoModel('+i+')">删除</button></div>').join('') : '<div class="muted">还没有候选模型。</div>';
}
function updateAutoModels(mutator){
  const cfg=parseConfig(); if(!cfg) return;
  if(!cfg.auto_model) cfg.auto_model={enabled:false,models:[]};
  cfg.auto_model.enabled=!!document.getElementById('autoModelEnabled').checked;
  cfg.auto_model.models=autoModels(cfg);
  mutator(cfg.auto_model.models);
  setConfig(cfg);
}
function addAutoModel(){
  const input=document.getElementById('autoModelInput'); const value=(input && input.value || '').trim();
  if(!value || value==='auto'){ setText('autoModelStatus','err','请输入真实模型 ID，例如 oc/big-pickle'); return; }
  if(!value.includes('/')){ setText('autoModelStatus','err','候选模型需要使用 provider/model 格式'); return; }
  updateAutoModels(models=>{ models.push(value); });
  if(input) input.value='';
  setText('autoModelStatus','ok','已添加候选模型，记得保存');
}
function removeAutoModel(index){ updateAutoModels(models=>{ models.splice(index,1); }); setText('autoModelStatus','ok','已移除候选模型，记得保存'); }
function addAutoModelValue(source){
  const value=(typeof source==='string' ? source : (source && source.getAttribute('data-model')) || '').trim();
  if(!value || value==='auto'){ setText('autoModelStatus','err','请输入真实模型 ID，例如 oc/big-pickle'); return; }
  if(!value.includes('/')){ setText('autoModelStatus','err','候选模型需要使用 provider/model 格式'); return; }
  updateAutoModels(models=>{ models.push(value); });
  setText('autoModelStatus','ok','已加入 Auto 候选，记得保存');
}
function moveAutoModel(index, delta){
  updateAutoModels(models=>{
    const next=index+delta; if(next<0 || next>=models.length) return;
    const item=models[index]; models.splice(index,1); models.splice(next,0,item);
  });
  setText('autoModelStatus','ok','已调整顺序，记得保存');
}
function latencyClass(ms){ if(!ms) return ''; if(ms<=1200) return 'good'; if(ms<=4000) return 'warn'; return 'bad'; }
function latencyHTML(p, model){ const ms=p && p.model_latency_ms ? p.model_latency_ms[model] : 0; return ms ? '<span class="latency '+latencyClass(ms)+'">'+ms+'ms</span>' : ''; }
function modelErrorHTML(p, model){ const err=p && p.model_errors ? p.model_errors[model] : ''; return err ? '<span class="muted"> '+esc(err)+'</span>' : ''; }
function modelStateText(p, model){
  const err=((p && p.model_errors && p.model_errors[model]) || '');
  const lower=err.toLowerCase();
  if(!err) return '';
  if(err.includes('额度不足') || lower.includes('insufficient_credits') || lower.includes('insufficient credits')) return '（额度不足）';
  if(err.includes('免费额度')) return '（免费额度已结束）';
  if(err.includes('请求超时') || lower.includes('timeout') || lower.includes('context deadline exceeded')) return '（请求超时）';
  if(err.includes('请求过于频繁') || lower.includes('rate limited') || lower.includes('http 429')) return '（请求过于频繁）';
  if(err.includes('权限不足') || lower.includes('forbidden') || lower.includes('unauthorized') || lower.includes('http 401') || lower.includes('http 403')) return '（权限不足）';
  if(err.includes('模型暂不可用')) return '（模型暂不可用）';
  if(err.includes('上游响应异常')) return '（上游响应异常）';
  return '（不可用）';
}
function setText(id, cls, text){ const el=document.getElementById(id); if(!el) return; el.className=cls; el.textContent=text; }
function setProgress(id, done, total){
  const wrap=document.getElementById(id); if(!wrap) return;
  const bar=wrap.querySelector('progress'); const text=wrap.querySelector('span');
  bar.max=Math.max(total,1); bar.value=done; text.textContent=done+'/'+total;
  wrap.classList.toggle('active', total>0 && done<total);
}
/*
function modelRows(p){
  const selected=new Set(selectedModels(p));
  const available=new Set(availableModels(p));
  const checkedAt=!!p.availability_checked_at;
  const models=unique(p.models || []);
  return models.map(model=>{
    const usable=!checkedAt || available.has(model);
    const checked=selected.has(model);
    const note=usable ? '' : modelStateText(p, model);
    const toggle='<input type="checkbox" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" '+(checked?'checked':'')+'>';
    const addAuto='<button class="small secondary" type="button" title="加入 Auto 候选" data-model="'+esc(p.id+'/'+model)+'" onclick="addAutoModelValue(this)">+</button>';
    return '<div class="model-item '+(usable?'':'off')+'">'+toggle+addAuto+'<span class="model-name">'+esc(model)+note+latencyHTML(p,model)+(usable?'':modelErrorHTML(p,model))+'</span></div>';
  }).join('');
}
*/
function oauthControls(id){
  if(id==='qoder') return '<div class="bar"><button class="small" onclick="startQoder()">开始登录</button><button class="small secondary" onclick="pollQoder()">轮询令牌</button><span id="qoderStatus" class="muted"></span></div>';
  if(id==='gemini') return '<div class="bar"><button class="small" onclick="startGemini()">开始登录</button><span id="geminiStatus" class="muted"></span></div>';
  if(id==='kilo') return '<div class="bar"><button class="small" onclick="startKilo()">开始登录</button><button class="small secondary" onclick="pollKilo()">轮询令牌</button><span id="kiloStatus" class="muted"></span></div>';
  if(id==='cline') return '<div class="bar"><button class="small" onclick="startCline()">开始登录</button><span id="clineStatus" class="muted"></span></div>';
  return '';
}
function modelRows(p){
  const selected=new Set(selectedModels(p));
  const available=new Set(availableModels(p));
  const checkedAt=!!p.availability_checked_at;
  const models=unique(p.models || []);
  return models.map(model=>{
    const usable=!checkedAt || available.has(model);
    const checked=selected.has(model);
    const note=usable ? '' : modelStateText(p, model);
    const toggle='<input type="checkbox" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" '+(checked?'checked':'')+'>';
    const addAuto='<button class="add-auto-model secondary" type="button" title="Add to Auto" data-model="'+esc(p.id+'/'+model)+'" onclick="addAutoModelValue(this)">+</button>';
    return '<div class="model-item '+(usable?'':'off')+'">'+toggle+addAuto+'<span class="model-name">'+esc(model)+note+latencyHTML(p,model)+(usable?'':modelErrorHTML(p,model))+'</span></div>';
  }).join('');
}
function renderProviderStatus(){
  const root=document.getElementById('providerStatus'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const ids=[...statusProviderIDs, ...cfg.providers.filter(isCustomProvider).map(p=>p.id)];
  const items=ids.map(id=>{
    const p=cfg.providers.find(x=>x.id===id);
    if(!p || !providerConnected(p)) return '';
    const loaded=unique(p.models || []); const available=availableModels(p); const published=visibleModels(p);
    const availableCount=p.availability_checked_at?available.length:loaded.length;
    const auth=authStatus(p); const authText=auth==='needs_login' ? '<div class="err">登录失效，需要重新登录。'+esc(authError(p))+'</div>' : '<div class="muted">已连接</div>';
    return '<div class="card"><h3><span class="green-dot"></span>'+esc(p.name)+'</h3>'+authText+'<div class="muted">已加载 '+loaded.length+' 个模型，可用 '+availableCount+' 个，已发布 '+published.length+' 个</div></div>';
  }).filter(Boolean);
  root.innerHTML=items.join('') || '<div class="muted">当前没有已连接的 provider。</div>';
}
function apiKeyRowHTML(id, value){
  return '<div class="inline-field grow key-row"><input data-api-key-provider="'+esc(id)+'" value="'+esc(value || '')+'" placeholder="sk-..."><button class="small secondary" data-key-remove="1" onclick="removeAPIKeyInput(this,\''+id+'\')" type="button">删除</button></div>';
}
function refreshAPIKeyRemoveButtons(id){
  const root=document.getElementById('keyList_'+id); if(!root) return;
  const rows=[...root.querySelectorAll('.key-row')];
  rows.forEach(row=>{ const btn=row.querySelector('button[data-key-remove]'); if(btn) btn.disabled=rows.length<=1; });
}
function addAPIKeyInput(id){
  const root=document.getElementById('keyList_'+id); if(!root) return;
  root.insertAdjacentHTML('beforeend', apiKeyRowHTML(id,''));
  refreshAPIKeyRemoveButtons(id);
}
function removeAPIKeyInput(button,id){
  const row=button && button.closest ? button.closest('.key-row') : null;
  if(row) row.remove();
  const root=document.getElementById('keyList_'+id);
  if(root && !root.querySelector('.key-row')) root.insertAdjacentHTML('beforeend', apiKeyRowHTML(id,''));
  refreshAPIKeyRemoveButtons(id);
}
function apiKeyValues(id){
  return unique([...document.querySelectorAll('input[data-api-key-provider="'+id+'"]')].map(node=>node.value.trim()).filter(Boolean));
}
function apiKeyEditor(id,p,isCustom){
  if(!isCustom) return '<div class="field"><label>API Key</label><input id="key_'+id+'" value="'+esc(p.api_key || '')+'" placeholder="sk-..."></div>';
  const keys=providerAPIKeys(p); if(!keys.length) keys.push('');
  return '<div class="field"><label>API Keys</label><div id="keyList_'+id+'" class="key-list">'+keys.map(key=>apiKeyRowHTML(id,key)).join('')+'</div><div class="bar"><button class="small secondary" onclick="addAPIKeyInput(\''+id+'\')" type="button">新增 API Key</button><span class="muted">按顺序尝试；额度不足时自动切到下一个。</span></div></div>';
}
function hydrateCustomKeyEditors(cfg){
  (cfg.providers || []).filter(isCustomProvider).forEach(p=>{
    const input=document.getElementById('key_'+p.id);
    if(!input) return;
    const field=input.closest('.field');
    if(!field) return;
    field.outerHTML=apiKeyEditor(p.id,p,true);
    refreshAPIKeyRemoveButtons(p.id);
  });
}
function renderAPIProviders(){
  const root=document.getElementById('apiProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  root.innerHTML=apiProviderIDs(cfg).map(id=>{
    const p=cfg.providers.find(x=>x.id===id); if(!p) return '';
    const isCustom=isCustomProvider(p);
    const listControls=apiModelsLoaded(p) ? '<div class="bar"><button class="small secondary" onclick="selectProviderModels(\''+id+'\',true)">全选</button><button class="small secondary" onclick="selectProviderModels(\''+id+'\',false)">取消所有选择</button></div>' : '';
    const rows=apiModelsLoaded(p) ? '<div class="model-list">'+modelRows(p)+'</div>' : '';
    const count=apiModelsLoaded(p) ? '<div class="muted">已拉取 '+p.models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div>' : '';
    const deleteButton='<div class="bar" style="justify-content:flex-end"><button class="small secondary" onclick="deleteAPIProvider(\''+id+'\')">删除卡片</button></div>';
    return '<div class="api-card"><div class="api-head"><strong>'+esc(p.name)+'</strong><span class="api-meta">'+esc(p.id)+'</span></div>'+(isCustom?'<div class="field"><label>名称</label><input id="name_'+id+'" value="'+esc(p.name || '')+'" placeholder="自定义源"></div>':'')+'<label class="toggle"><input type="checkbox" id="enabled_'+id+'" '+(p.enabled?'checked':'')+'> 启用</label><div class="field"><label>Base URL</label><input id="base_'+id+'" value="'+esc(p.base_url || '')+'" placeholder="https://example.com/v1"></div><div class="field"><label>API Key</label><input id="key_'+id+'" value="'+esc(p.api_key || '')+'" placeholder="sk-..."></div><div class="bar"><button onclick="saveAPIProvider(\''+id+'\')">保存</button><button class="secondary" onclick="fetchAPIProviderModels(\''+id+'\')">拉取模型</button><span id="apiStatus_'+id+'" class="muted"></span></div><div class="field"><label>手动添加模型</label><div class="inline-field grow"><input id="manualModel_'+id+'" placeholder="model-id"><button class="secondary" onclick="addAPIModel(\''+id+'\')" type="button">添加</button></div></div><div class="bar"><button class="small" onclick="probeProvider(\''+id+'\',\'apiStatus_'+id+'\')" '+(!apiModelsLoaded(p)?'disabled':'')+'>探测可用</button><button class="small secondary" onclick="saveModelSelection(\''+id+'\',\'apiStatus_'+id+'\')" '+(!apiModelsLoaded(p)?'disabled':'')+'>保存发布</button></div><div id="probeProgress_'+id+'" class="progress-wrap"><progress value="0" max="'+(p.models||[]).length+'"></progress><span class="muted">0/'+(p.models||[]).length+'</span></div>'+count+listControls+rows+deleteButton+'</div>';
  }).join('');
  hydrateCustomKeyEditors(cfg);
}
function renderPublishProviders(){
  const root=document.getElementById('publishProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const providers=cfg.providers.filter(p=>publishProviderIDs.includes(p.id));
  const items=providers.map(p=>{
    const connected=providerConnected(p);
    const models=unique(p.models || []);
    const auth=authStatus(p);
    const authText=auth==='needs_login' ? '<div class="err">登录失效，需要重新登录。'+esc(authError(p))+'</div>' : '';
    const listControls=models.length ? '<div class="bar"><button class="small secondary" onclick="selectProviderModels(\''+p.id+'\',true)">全选</button><button class="small secondary" onclick="selectProviderModels(\''+p.id+'\',false)">取消所有选择</button></div>' : '';
    const rows=models.length ? '<div class="model-list">'+modelRows(p)+'</div>' : '<div class="muted">登录或拉取后会显示模型。</div>';
    return '<div class="card"><h3><span class="'+(connected?'green-dot':'gray-dot')+'"></span>'+esc(p.name)+'</h3>'+oauthControls(p.id)+authText+'<div class="muted">已加载 '+models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div><div class="bar"><button class="small" onclick="probeProvider(\''+p.id+'\',\'publishStatus_'+p.id+'\')" '+(!connected || !models.length?'disabled':'')+'>探测可用</button><button class="small secondary" onclick="saveModelSelection(\''+p.id+'\',\'publishStatus_'+p.id+'\')" '+(!models.length?'disabled':'')+'>保存发布列表</button><span id="publishStatus_'+p.id+'" class="muted"></span></div><div id="probeProgress_'+p.id+'" class="progress-wrap"><progress value="0" max="'+models.length+'"></progress><span class="muted">0/'+models.length+'</span></div>'+listControls+rows+'<div class="bar" style="justify-content:flex-end"><button class="small secondary" onclick="disableProvider(\''+p.id+'\')">停用</button></div></div>';
  });
  root.innerHTML=items.join('') || '<div class="muted">还没有可发布的模型。</div>';
}
async function reloadConfig(){ const res=await fetch('/api/config'); const cfg=await res.json(); setConfig(cfg); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }
function buildAPIProvider(id){
  const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
  const prev=cfg.providers.find(x=>x.id===id); if(!prev) throw new Error('provider not found: '+id);
  const custom=isCustomProvider(prev);
  const keys=custom?apiKeyValues(id):[];
  const next={ ...prev, name:custom?(document.getElementById('name_'+id).value.trim() || prev.name || 'Custom OpenAI Compatible'):prev.name, enabled:!!document.getElementById('enabled_'+id).checked, base_url:document.getElementById('base_'+id).value.trim(), api_key:custom?(keys[0] || ''):document.getElementById('key_'+id).value.trim() };
  if(custom){
    next.api_keys=keys;
    next.provider_specific_data={...(next.provider_specific_data || {}), customProvider:'true'};
  }else{
    delete next.api_keys;
  }
  return next;
}
async function saveConfigObject(cfg){ const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(cfg)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); return data; }
async function loadImportConfigFile(){
  const input=document.getElementById('importConfigFile');
  const file=input && input.files && input.files[0];
  if(!file) return;
  try{
    document.getElementById('importConfigText').value=await file.text();
    previewImportConfig();
  }catch(e){
    setText('importConfigStatus','err','读取失败：'+e.message);
  }
}
function parseImportConfig(){
  const text=(document.getElementById('importConfigText').value || '').trim();
  if(!text) throw new Error('请先选择或粘贴 config.json');
  const cfg=JSON.parse(text);
  if(!cfg || typeof cfg !== 'object' || !Array.isArray(cfg.providers)) throw new Error('不是有效的 9Router Lite 配置');
  return cfg;
}
function previewImportConfig(){
  try{
    const cfg=parseImportConfig();
    const connected=(cfg.providers || []).filter(providerConnected).length;
    setText('importConfigStatus','ok','配置有效：'+cfg.providers.length+' 个 provider，已连接 '+connected+' 个');
  }catch(e){
    setText('importConfigStatus','err',e.message);
  }
}
async function importConfigJSON(){
  try{
    const cfg=parseImportConfig();
    if(!confirm('导入会覆盖当前服务器的 data/config.json，确定继续？')) return;
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    setText('importConfigStatus','ok','已导入，正在刷新...');
    try{
      await reloadConfig();
      setText('importConfigStatus','ok','已导入并保存');
    }catch(e){
      setText('importConfigStatus','ok','已导入并保存；如果访问密码已变化，请重新登录后台');
    }
  }catch(e){
    setText('importConfigStatus','err','导入失败：'+e.message);
  }
}
function applyGatewaySettings(cfg){
  cfg.access_key=document.getElementById('accessKey').value.trim();
  cfg.auto_probe_enabled=!!document.getElementById('autoProbeEnabled').checked;
  cfg.auto_probe_interval_minutes=parseInt(document.getElementById('autoProbeInterval').value || '60',10);
  cfg.auto_model={enabled:!!document.getElementById('autoModelEnabled').checked,models:autoModels(cfg)};
  return cfg;
}
async function saveGateway(){ try{ const cfg=parseConfig(); if(!cfg) throw new Error('config is invalid'); applyGatewaySettings(cfg); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('status','ok','已保存'); }catch(e){ setText('status','err',e.message); } }
function toggleAccessKey(){ const el=document.getElementById('accessKey'); el.type=el.type==='password'?'text':'password'; }
async function copyTextValue(value){
  if(navigator.clipboard && navigator.clipboard.writeText){
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea=document.createElement('textarea');
  textarea.value=value;
  textarea.setAttribute('readonly','');
  textarea.style.position='fixed';
  textarea.style.left='-9999px';
  document.body.appendChild(textarea);
  textarea.select();
  try{
    if(!document.execCommand('copy')) throw new Error('copy command failed');
  }finally{
    document.body.removeChild(textarea);
  }
}
async function copyBaseURL(){ try{ await copyTextValue(document.getElementById('baseUrl').value); setText('status','ok','Base URL 已复制'); }catch(e){ setText('status','err','复制失败：'+e.message); } }
async function copyEndpoint(id, label){ try{ await copyTextValue(document.getElementById(id).value); setText('status','ok',(label || '地址')+'已复制'); }catch(e){ setText('status','err','复制失败：'+e.message); } }
function openModelsPage(){ const key=document.getElementById('accessKey').value.trim(); if(!key){ setText('status','err','请先设置访问密钥'); return; } window.open('/v1/models?view=html&key='+encodeURIComponent(key),'_blank','noopener,noreferrer'); }
async function saveAPIProvider(id){ try{ const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('apiStatus_'+id,'ok','已保存'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function disableProvider(id){
  try{
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    applyGatewaySettings(cfg);
    cfg.providers=cfg.providers.map(p=>p.id===id?{...p, enabled:false}:p);
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('status','ok','已停用 '+id);
  }catch(e){ setText('status','err',e.message); }
}
async function deleteAPIProvider(id){
  try{
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    const p=cfg.providers.find(x=>x.id===id); if(!p) throw new Error('provider not found');
    cfg.providers=cfg.providers.filter(x=>x.id!==id);
    const deleted=new Set(cfg.deleted_provider_ids || []);
    if(isCustomProvider(p)){
      deleted.delete(id);
    }else{
      deleted.add(id);
    }
    cfg.deleted_provider_ids=[...deleted];
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('status','ok','已删除卡片');
  }catch(e){ setText('apiStatus_'+id,'err',e.message); }
}
async function fetchAPIProviderModels(id){ try{ setText('apiStatus_'+id,'muted','正在保存...'); const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); setText('apiStatus_'+id,'muted','正在拉取模型...'); const res=await fetch('/api/provider/models',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('apiStatus_'+id,'ok','已拉取 '+(data.count || 0)+' 个模型'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function addAPIModel(id){
  try{
    const input=document.getElementById('manualModel_'+id); const model=(input && input.value || '').trim();
    if(!model) throw new Error('请输入模型 ID');
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    applyGatewaySettings(cfg);
    cfg.providers=cfg.providers.map(p=>{
      if(p.id!==id) return p;
      p={...p};
      p.models=unique([...(p.models || []), model]);
      p.enabled_models=unique([...(p.enabled_models || []), model]);
      p.provider_specific_data={...(p.provider_specific_data || {}), apiModelsFetched:'true', manualPublishOverride:'true'};
      return p;
    });
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('apiStatus_'+id,'ok','已添加模型');
  }catch(e){ setText('apiStatus_'+id,'err',e.message); }
}
function selectProviderModels(id, checked){
  document.querySelectorAll('input[data-provider="'+id+'"][data-model]').forEach(node=>{ node.checked=!!checked; });
}
function stopProbe(){
  probeStopRequested=true;
  setText('probeAllStatus','muted','正在停止，当前请求完成后不再继续。');
}
async function probeOneModel(id, model, autoPublish, dropUnavailable){ if(dropUnavailable===undefined) dropUnavailable=!autoPublish; const res=await fetch('/api/provider/probe-model',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, model, auto_publish:!!autoPublish, drop_unavailable_on_failure:!!dropUnavailable})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); return data; }
async function probeProvider(id, statusID){
  probeStopRequested=false;
  const cfg=parseConfig(); const p=cfg.providers.find(x=>x.id===id); if(!p) return;
  const models=unique(p.models || []); let ok=0; statusID=statusID || 'publishStatus_'+id; setProgress('probeProgress_'+id,0,models.length); setText(statusID,'muted','正在探测...');
  let done=0;
  for(let i=0;i<models.length;i++){ if(probeStopRequested) break; const data=await probeOneModel(id,models[i],false); if(data.ok) ok++; done=i+1; setProgress('probeProgress_'+id,done,models.length); setText(statusID,'muted','正在探测 '+done+'/'+models.length); }
  await reloadConfig(); setText(statusID,probeStopRequested?'muted':'ok',(probeStopRequested?'已停止，已探测 ':'可用 ')+ok+' 个模型');
}
async function probeAllProviders(){
  probeStopRequested=false;
  const cfg=parseConfig(); const providers=cfg.providers.filter(p=>providerConnected(p) && visibleModels(p).length);
  const jobs=[]; providers.forEach(p=>visibleModels(p).forEach(model=>jobs.push({id:p.id,model})));
  let ok=0; setProgress('probeAllProgress',0,jobs.length); setText('probeAllStatus','muted','正在探测...');
  let done=0;
  for(let i=0;i<jobs.length;i++){ if(probeStopRequested) break; const data=await probeOneModel(jobs[i].id,jobs[i].model,true); if(data.ok) ok++; done=i+1; setProgress('probeAllProgress',done,jobs.length); setText('probeAllStatus','muted','正在探测 '+done+'/'+jobs.length); }
  await reloadConfig(); setText('probeAllStatus',probeStopRequested?'muted':'ok',probeStopRequested?'已停止，已探测 '+done+'/'+jobs.length+'，可用 '+ok+' 个模型':'探测完成，可用 '+ok+' 个模型，已自动发布');
}
async function saveModelSelection(id,statusID){ try{ statusID=statusID || 'publishStatus_'+id; const nodes=[...document.querySelectorAll('input[data-provider="'+id+'"]:checked')]; const enabled_models=nodes.map(node=>node.getAttribute('data-model')); const res=await fetch('/api/provider/selection',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, enabled_models})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText(statusID,'ok','已保存发布列表'); }catch(e){ setText(statusID || 'status','err',e.message); } }
async function save(){ try{ const body=JSON.parse(document.getElementById('cfg').value); applyGatewaySettings(body); ensureBlankCustomProvider(body); const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); setText('status','ok','已保存'); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }catch(e){ setText('status','err',e.message); } }
async function startQoder(){ try{ const data=await (await fetch('/api/oauth/qoder/device-code')).json(); localStorage.setItem('qoder_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('qoderStatus','ok','已打开 Qoder 登录页，完成登录后点击轮询令牌。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function pollQoder(){ try{ const flow=JSON.parse(localStorage.getItem('qoder_flow')||'{}'); if(!flow.device_code || !flow.codeVerifier) throw new Error('请先开始 Qoder 登录'); const res=await fetch('/api/oauth/qoder/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({ deviceCode: flow.device_code, codeVerifier: flow.codeVerifier, extraData: {_qoderMachineId: flow._qoderMachineId, _qoderNonce: flow._qoderNonce, _qoderVerifier: flow.codeVerifier} })}); const data=await res.json(); if(data.pending){ setText('qoderStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('qoder_flow'); await reloadConfig(); setText('qoderStatus','ok','Qoder 已连接。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function startGemini(){ try{ const data=await (await fetch('/api/oauth/gemini/authorize')).json(); if(!data.authUrl) throw new Error(data.error || 'missing auth url'); window.open(data.authUrl, '_blank', 'noopener,noreferrer'); setText('geminiStatus','ok','已打开 Gemini 登录页，回调完成后会自动保存令牌。'); }catch(e){ setText('geminiStatus','err',e.message); } }
async function startKilo(){ try{ const data=await (await fetch('/api/oauth/kilo/device-code')).json(); localStorage.setItem('kilo_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('kiloStatus','ok','已打开 Kilo 登录页，完成登录后点击轮询令牌。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function pollKilo(){ try{ const flow=JSON.parse(localStorage.getItem('kilo_flow')||'{}'); if(!flow.device_code) throw new Error('请先开始 Kilo 登录'); const res=await fetch('/api/oauth/kilo/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({deviceCode: flow.device_code})}); const data=await res.json(); if(data.pending){ setText('kiloStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('kilo_flow'); await reloadConfig(); setText('kiloStatus','ok','Kilo 已连接。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function startCline(){ try{ const data=await (await fetch('/api/oauth/cline/authorize')).json(); window.open(data.authUrl, '_blank', 'noopener,noreferrer'); setText('clineStatus','ok','已打开 Cline 登录页，回调完成后会自动保存令牌。'); }catch(e){ setText('clineStatus','err',e.message); } }
const initCfg=parseConfig(); if(initCfg){ setConfig(initCfg); }
renderProviderStatus(); renderAPIProviders(); renderPublishProviders();
</script>
</main>
</body>
</html>`
}
