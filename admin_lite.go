package main

import "strings"

func adminHTMLLite(configJSON string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(configJSON)
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>9Router Lite</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}
main{max-width:1080px;margin:32px auto;padding:0 20px}
h1{font-size:28px;margin:0 0 8px}
h2{font-size:18px;margin:26px 0 10px}
.bar{display:flex;gap:10px;align-items:center;margin:16px 0;flex-wrap:wrap}
a,button{font:inherit}
button{background:#111;color:#fff;border:0;border-radius:6px;padding:9px 14px;cursor:pointer}
button.secondary{background:#fff;color:#111;border:1px solid #ddd}
button.small{padding:7px 10px;font-size:13px}
textarea{width:100%;min-height:420px;box-sizing:border-box;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace;border:1px solid #ddd;border-radius:6px;padding:14px;background:#fff}
.muted{color:#666;font-size:13px}
.ok{color:#047857}
.err{color:#b91c1c}
code{background:#eee;padding:2px 5px;border-radius:4px}
.panel-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px;margin:12px 0 18px}
.card,.api-card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}
.card h3{display:flex;align-items:center;gap:8px;margin:0 0 6px;font-size:17px}
.green-dot{width:9px;height:9px;border-radius:50%;background:#16a34a;display:inline-block;flex:0 0 auto}
.api-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px;margin:10px 0 22px}
.api-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:10px}
.api-meta{font-size:12px;color:#666}
.field{display:grid;gap:6px;margin:10px 0}
.field label{font-size:13px;color:#444}
.field input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#fff;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
.inline-field{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.inline-field input[type=number]{width:110px}
.toggle{display:flex;gap:8px;align-items:center;font-size:13px;margin:8px 0}
.model-list{display:grid;gap:8px;margin-top:10px;max-height:280px;overflow:auto;padding-right:4px}
.model-item{display:flex;gap:8px;align-items:flex-start;font-size:13px}
.model-item.off{color:#999}
.model-name{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}
.latency{color:#666;margin-left:6px}
.progress-wrap{display:none;align-items:center;gap:10px;margin:8px 0}
.progress-wrap.active{display:flex}
progress{width:190px;height:12px}
details{margin-top:22px}
</style>
</head>
<body>
<main>
<h1>9Router Lite</h1>
<div class="muted">接口基址：<code>/v1</code>。配置保存在 <code>data/config.json</code>。</div>

<div class="bar">
<button onclick="saveGateway()">保存网关设置</button>
<button class="secondary" onclick="openModelsPage()">打开模型页</button>
<a href="/health" target="_blank">/health</a>
<span id="status" class="muted"></span>
</div>

<div class="field">
<label>访问密钥</label>
<div class="inline-field">
<input id="accessKey" type="password" placeholder="第三方 Agent 访问 /v1 时需要带这个 key">
<button class="secondary" onclick="toggleAccessKey()" type="button">显示/隐藏</button>
</div>
</div>

<div class="bar">
<label class="toggle"><input type="checkbox" id="autoProbeEnabled"> 启用定时自动探测</label>
<label class="inline-field muted">间隔分钟 <input id="autoProbeInterval" type="number" min="1" step="1" value="60"></label>
<span class="muted">定时探测会自动把可用模型发布到 <code>/v1/models</code>。</span>
</div>

<h2>已连接源</h2>
<div id="providerStatus" class="panel-grid"></div>

<h2>OAuth 登录</h2>
<div class="bar">
<button onclick="startQoder()">开始 Qoder 登录</button>
<button onclick="pollQoder()">轮询 Qoder 令牌</button>
<span id="qoderStatus" class="muted"></span>
</div>
<div class="bar">
</div>
<div class="bar">
<button onclick="startKilo()">开始 Kilo 登录</button>
<button onclick="pollKilo()">轮询 Kilo 令牌</button>
<span id="kiloStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startCline()">开始 Cline 登录</button>
<span id="clineStatus" class="muted"></span>
</div>

<h2>API 密钥提供商</h2>
<div class="muted">支持 <code>GLM</code>、<code>Groq</code>、<code>DeepSeek</code>、<code>Xiaomi MiMo</code> 和 <code>自定义 OpenAI Compatible</code>。</div>
<div id="apiProviders" class="api-grid"></div>

<div class="bar">
<h2 style="margin:0">模型发布</h2>
<button class="secondary" onclick="probeAllProviders()">一键探测</button>
<span id="probeAllStatus" class="muted"></span>
</div>
<div id="probeAllProgress" class="progress-wrap"><progress value="0" max="1"></progress><span class="muted">0/0</span></div>
<div class="muted">只会把你勾选且探测可用的模型加入 <code>/v1/models</code>。探测会真实请求模型，并记录延迟。</div>
<div id="publishProviders" class="panel-grid"></div>

<details>
<summary>原始配置</summary>
<div class="bar"><button class="secondary" onclick="save()">保存原始配置</button></div>
<textarea id="cfg" spellcheck="false">` + escaped + `</textarea>
</details>

<script>
const apiProviderIDs=['glm','groq','deepseek','mimo','custom'];
const statusProviderIDs=['oc','mmf','qoder','kilo','cline','glm','groq','deepseek','mimo','custom'];
function parseConfig(){ try { return JSON.parse(document.getElementById('cfg').value); } catch { return null; } }
function setConfig(cfg){
  document.getElementById('cfg').value=JSON.stringify(cfg,null,2);
  document.getElementById('accessKey').value=cfg.access_key || '';
  document.getElementById('autoProbeEnabled').checked=!!cfg.auto_probe_enabled;
  document.getElementById('autoProbeInterval').value=cfg.auto_probe_interval_minutes || 60;
}
function providerConnected(p){ return !!(p && p.enabled && (p.api_key || p.access_token || p.type === 'opencode-free' || p.type === 'mimo-free')); }
function authStatus(p){ return (p && p.provider_specific_data && p.provider_specific_data.authStatus) || 'ok'; }
function authError(p){ return (p && p.provider_specific_data && p.provider_specific_data.lastAuthError) || ''; }
function esc(v){ return String(v || '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;'); }
function unique(arr){ return [...new Set((arr || []).filter(Boolean))]; }
function selectedModels(p){ return unique((p && p.enabled_models && p.enabled_models.length) ? p.enabled_models : (p && p.models) || []); }
function availableModels(p){ return unique((p && p.available_models) || []); }
function visibleModels(p){ const selected=selectedModels(p); const available=availableModels(p); if(!p || (!p.availability_checked_at && !available.length)) return selected; const set=new Set(available); return selected.filter(x=>set.has(x)); }
function latencyText(p, model){ const ms=p && p.model_latency_ms ? p.model_latency_ms[model] : 0; return ms ? ' · '+ms+'ms' : ''; }
function setText(id, cls, text){ const el=document.getElementById(id); if(!el) return; el.className=cls; el.textContent=text; }
function setProgress(id, done, total){
  const wrap=document.getElementById(id); if(!wrap) return;
  const bar=wrap.querySelector('progress'); const text=wrap.querySelector('span');
  bar.max=Math.max(total,1); bar.value=done; text.textContent=done+'/'+total;
  wrap.classList.toggle('active', total>0 && done<total);
}
function renderProviderStatus(){
  const root=document.getElementById('providerStatus'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const items=statusProviderIDs.map(id=>{
    const p=cfg.providers.find(x=>x.id===id);
    if(!p || !providerConnected(p)) return '';
    const loaded=unique(p.models || []); const available=availableModels(p); const published=visibleModels(p); const availableCount=p.availability_checked_at?available.length:loaded.length;
    const auth=authStatus(p); const authText=auth==='needs_login' ? '<div class="err">登录失效，需要重新登录。'+esc(authError(p))+'</div>' : '<div class="muted">已连接</div>';
    return '<div class="card"><h3><span class="green-dot"></span>'+esc(p.name)+'</h3>'+authText+'<div class="muted">已加载 '+loaded.length+' 个模型，可用 '+availableCount+' 个，已发布 '+published.length+' 个</div></div>';
  }).filter(Boolean);
  root.innerHTML=items.join('') || '<div class="muted">当前没有已连接的 provider。</div>';
}
function renderAPIProviders(){
  const root=document.getElementById('apiProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  root.innerHTML=apiProviderIDs.map(id=>{
    const p=cfg.providers.find(x=>x.id===id); if(!p) return '';
    return '<div class="api-card"><div class="api-head"><strong>'+esc(p.name)+'</strong><span class="api-meta">'+esc(p.id)+'</span></div>'+(id==='custom'?'<div class="field"><label>名称</label><input id="name_'+id+'" value="'+esc(p.name || '')+'" placeholder="自定义源"></div>':'')+'<label class="toggle"><input type="checkbox" id="enabled_'+id+'" '+(p.enabled?'checked':'')+'> 启用</label><div class="field"><label>Base URL</label><input id="base_'+id+'" value="'+esc(p.base_url || '')+'" placeholder="https://example.com/v1"></div><div class="field"><label>API Key</label><input id="key_'+id+'" type="password" value="'+esc(p.api_key || '')+'" placeholder="sk-..."></div><div class="bar"><button onclick="saveAPIProvider(\''+id+'\')">保存</button><button class="secondary" onclick="fetchAPIProviderModels(\''+id+'\')">拉取模型</button><span id="apiStatus_'+id+'" class="muted"></span></div>'+(Array.isArray(p.models) && p.models.length ? '<div class="muted">已加载 '+p.models.length+' 个模型，可在下方“模型发布”里选择。</div>' : '')+'</div>';
  }).join('');
}
function renderPublishProviders(){
  const root=document.getElementById('publishProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const items=cfg.providers.filter(p=>providerConnected(p) && Array.isArray(p.models) && p.models.length).map(p=>{
    const selected=new Set(selectedModels(p)); const available=new Set(availableModels(p)); const hasAvailability=!!p.availability_checked_at; const models=unique(p.models);
    const rows=models.map(model=>{
      const usable=!hasAvailability || available.has(model); const checked=selected.has(model);
      return '<label class="model-item '+(usable?'':'off')+'"><input type="checkbox" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" '+(checked?'checked':'')+' '+(usable?'':'disabled')+'><span class="model-name">'+esc(model)+(usable?'':'（不可用）')+'<span class="latency">'+latencyText(p,model)+'</span></span></label>';
    }).join('');
    const auth=authStatus(p); const authText=auth==='needs_login' ? '<div class="err">登录失效，需要重新登录。'+esc(authError(p))+'</div>' : '';
    return '<div class="card"><h3><span class="green-dot"></span>'+esc(p.name)+'</h3>'+authText+'<div class="muted">已加载 '+models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div><div class="bar"><button class="small" onclick="probeProvider(\''+p.id+'\')">探测可用</button><button class="small secondary" onclick="saveModelSelection(\''+p.id+'\')">保存发布列表</button><span id="publishStatus_'+p.id+'" class="muted"></span></div><div id="probeProgress_'+p.id+'" class="progress-wrap"><progress value="0" max="'+models.length+'"></progress><span class="muted">0/'+models.length+'</span></div><div class="model-list">'+rows+'</div></div>';
  });
  root.innerHTML=items.join('') || '<div class="muted">还没有可发布的模型。</div>';
}
async function reloadConfig(){ const res=await fetch('/api/config'); const cfg=await res.json(); setConfig(cfg); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }
function buildAPIProvider(id){
  const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
  const prev=cfg.providers.find(x=>x.id===id); if(!prev) throw new Error('provider not found: '+id);
  return { ...prev, name:id==='custom'?(document.getElementById('name_'+id).value.trim() || prev.name || 'Custom OpenAI Compatible'):prev.name, enabled:!!document.getElementById('enabled_'+id).checked, base_url:document.getElementById('base_'+id).value.trim(), api_key:document.getElementById('key_'+id).value.trim(), fetch_models:!!((prev && prev.models && prev.models.length) || prev.fetch_models) };
}
async function saveConfigObject(cfg){ const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(cfg)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); return data; }
function applyGatewaySettings(cfg){ cfg.access_key=document.getElementById('accessKey').value.trim(); cfg.auto_probe_enabled=!!document.getElementById('autoProbeEnabled').checked; cfg.auto_probe_interval_minutes=parseInt(document.getElementById('autoProbeInterval').value || '60',10); return cfg; }
async function saveGateway(){ try{ const cfg=parseConfig(); if(!cfg) throw new Error('config is invalid'); applyGatewaySettings(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('status','ok','已保存'); }catch(e){ setText('status','err',e.message); } }
function toggleAccessKey(){ const el=document.getElementById('accessKey'); el.type=el.type==='password'?'text':'password'; }
function openModelsPage(){ const key=document.getElementById('accessKey').value.trim(); if(!key){ setText('status','err','请先设置访问密钥'); return; } window.open('/v1/models?view=html&key='+encodeURIComponent(key),'_blank','noopener,noreferrer'); }
async function saveAPIProvider(id){ try{ const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); await saveConfigObject(cfg); await reloadConfig(); setText('apiStatus_'+id,'ok','已保存'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function fetchAPIProviderModels(id){ try{ setText('apiStatus_'+id,'muted','正在保存...'); const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); await saveConfigObject(cfg); setText('apiStatus_'+id,'muted','正在拉取模型...'); const res=await fetch('/api/provider/models',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('apiStatus_'+id,'ok','已拉取 '+(data.count || 0)+' 个模型'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function probeOneModel(id, model, autoPublish){
  const res=await fetch('/api/provider/probe-model',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, model, auto_publish:!!autoPublish})});
  const data=await res.json();
  if(!res.ok) throw new Error(data.error || res.statusText);
  return data;
}
async function probeProvider(id){
  const cfg=parseConfig(); const p=cfg.providers.find(x=>x.id===id); if(!p) return;
  const models=unique(p.models || []); let ok=0; setProgress('probeProgress_'+id,0,models.length); setText('publishStatus_'+id,'muted','正在探测...');
  for(let i=0;i<models.length;i++){ const data=await probeOneModel(id,models[i],false); if(data.ok) ok++; setProgress('probeProgress_'+id,i+1,models.length); setText('publishStatus_'+id,'muted','正在探测 '+(i+1)+'/'+models.length); }
  await reloadConfig(); setText('publishStatus_'+id,'ok','可用 '+ok+' 个模型');
}
async function probeAllProviders(){
  const cfg=parseConfig(); const providers=cfg.providers.filter(p=>providerConnected(p) && Array.isArray(p.models) && p.models.length);
  const jobs=[]; providers.forEach(p=>unique(p.models).forEach(model=>jobs.push({id:p.id,model})));
  let ok=0; setProgress('probeAllProgress',0,jobs.length); setText('probeAllStatus','muted','正在探测...');
  for(let i=0;i<jobs.length;i++){ const data=await probeOneModel(jobs[i].id,jobs[i].model,true); if(data.ok) ok++; setProgress('probeAllProgress',i+1,jobs.length); setText('probeAllStatus','muted','正在探测 '+(i+1)+'/'+jobs.length); }
  await reloadConfig(); setText('probeAllStatus','ok','探测完成，可用 '+ok+' 个模型，已自动发布');
}
async function saveModelSelection(id){ try{ const nodes=[...document.querySelectorAll('input[data-provider="'+id+'"]:checked')]; const enabled_models=nodes.map(node=>node.getAttribute('data-model')); const res=await fetch('/api/provider/selection',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, enabled_models})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('publishStatus_'+id,'ok','已保存发布列表'); }catch(e){ setText('publishStatus_'+id,'err',e.message); } }
async function save(){ try{ const body=JSON.parse(document.getElementById('cfg').value); applyGatewaySettings(body); const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); setText('status','ok','已保存'); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }catch(e){ setText('status','err',e.message); } }
async function startQoder(){ try{ const data=await (await fetch('/api/oauth/qoder/device-code')).json(); localStorage.setItem('qoder_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('qoderStatus','ok','已打开 Qoder 登录页，完成登录后点击轮询。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function pollQoder(){ try{ const flow=JSON.parse(localStorage.getItem('qoder_flow')||'{}'); if(!flow.device_code || !flow.codeVerifier) throw new Error('请先开始 Qoder 登录'); const res=await fetch('/api/oauth/qoder/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({ deviceCode: flow.device_code, codeVerifier: flow.codeVerifier, extraData: {_qoderMachineId: flow._qoderMachineId, _qoderNonce: flow._qoderNonce, _qoderVerifier: flow.codeVerifier} })}); const data=await res.json(); if(data.pending){ setText('qoderStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('qoder_flow'); await reloadConfig(); setText('qoderStatus','ok','Qoder 已连接。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function startKilo(){ try{ const data=await (await fetch('/api/oauth/kilo/device-code')).json(); localStorage.setItem('kilo_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('kiloStatus','ok','已打开 Kilo 登录页，完成登录后点击轮询。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function pollKilo(){ try{ const flow=JSON.parse(localStorage.getItem('kilo_flow')||'{}'); if(!flow.device_code) throw new Error('请先开始 Kilo 登录'); const res=await fetch('/api/oauth/kilo/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({deviceCode: flow.device_code})}); const data=await res.json(); if(data.pending){ setText('kiloStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('kilo_flow'); await reloadConfig(); setText('kiloStatus','ok','Kilo 已连接。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function startCline(){ try{ const data=await (await fetch('/api/oauth/cline/authorize')).json(); window.open(data.authUrl, '_blank', 'noopener,noreferrer'); setText('clineStatus','ok','已打开 Cline 登录页，回调完成后会自动保存令牌。'); }catch(e){ setText('clineStatus','err',e.message); } }
const initCfg=parseConfig(); if(initCfg){ setConfig(initCfg); }
renderProviderStatus(); renderAPIProviders(); renderPublishProviders();
</script>
</main>
</body>
</html>`
}
