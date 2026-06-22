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
button.add-auto-model,button.delete-model{width:17px;height:17px;padding:0;border-radius:3px;font-size:12px;line-height:1;display:inline-flex;align-items:center;justify-content:center;flex:0 0 17px;margin-top:1px}
button.model-lock{width:17px;height:17px;padding:0;border-radius:3px;font-size:12px;line-height:1;display:inline-flex;align-items:center;justify-content:center;flex:0 0 17px;margin-top:1px}
button.model-lock.locked{background:#111;color:#fff;border-color:#111}
button:disabled{opacity:.55;cursor:not-allowed}
textarea{width:100%;min-height:340px;box-sizing:border-box;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace;border:1px solid #ddd;border-radius:6px;padding:14px;background:#fff}
.muted{color:#666;font-size:13px}
.ok{color:#047857}
.err{color:#b91c1c}
code{background:#eee;padding:2px 5px;border-radius:4px}
.panel-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:12px;margin:12px 0 20px}
.api-grid{display:grid;grid-template-columns:minmax(300px,340px) minmax(0,700px);gap:12px;align-items:start;margin:12px 0 20px}
.card,.api-card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}
.api-provider-list{display:grid;gap:8px;position:sticky;top:12px;max-height:calc(100vh - 24px);overflow-y:auto;overflow-x:hidden;padding-right:2px;min-width:0}
.api-provider-detail-pane{min-width:0}
.api-provider-nav{width:100%;box-sizing:border-box;display:flex;align-items:center;justify-content:space-between;gap:10px;padding:11px 12px;background:#fff;color:#111;border:1px solid #ddd;border-radius:8px;text-align:left}
.api-provider-nav:hover{background:#f7f7f7}
.api-provider-nav.active{border-color:#111;background:#f3f4f6}
.api-provider-nav-main,.api-provider-nav-state{display:flex;align-items:center;gap:8px;min-width:0}
.api-provider-nav-main{flex:1 1 auto}
.api-provider-nav-main strong{white-space:normal;overflow-wrap:anywhere}
.api-provider-nav-state{color:#666;font-size:12px;white-space:nowrap}
.api-provider-detail{display:none}
.api-provider-detail.active{display:block}
.card h3,.api-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin:0 0 8px}
.card h3{justify-content:flex-start;font-size:17px}
.api-head strong{font-size:17px}
.api-meta{font-size:12px;color:#666}
.card-title-row{display:flex;justify-content:space-between;align-items:flex-start;gap:10px;margin:0 0 8px}
.card-title-row h3{margin:0}
.provider-badge{font-size:11px;color:#555;background:#f3f4f6;border:1px solid #ddd;border-radius:999px;padding:2px 8px;white-space:nowrap}
.green-dot,.gray-dot{width:9px;height:9px;border-radius:50%;display:inline-block;flex:0 0 auto}
.green-dot{background:#16a34a}.gray-dot{background:#aaa}
.field{display:grid;gap:6px;margin:10px 0}
.field label{font-size:13px;color:#444}
.field input,.field select{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#fff;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
.field textarea{min-height:110px}
.field input[readonly]{background:#f3f4f6;color:#374151}
.inline-field{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.inline-field.grow input{flex:1 1 240px}
.inline-field input[type=number]{width:110px}
.key-list{display:grid;gap:8px}
.key-row{display:grid;grid-template-columns:10px minmax(150px,1fr) auto auto;gap:8px;align-items:center}
.key-row input{min-width:0;width:100%;box-sizing:border-box}
.key-row button{white-space:nowrap}
.key-status-dot{width:8px;height:8px;border-radius:50%;display:inline-block;flex:0 0 auto}
.key-status-dot.active{background:#16a34a}
.key-status-dot.failed{background:#dc2626}
.key-status-dot.empty{background:transparent}
.endpoint-grid{display:grid;grid-template-columns:140px minmax(260px,1fr) auto;gap:8px;align-items:center;margin:8px 0}
.endpoint-grid input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#f3f4f6;color:#374151;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
.media-tool-row{grid-template-columns:140px auto auto minmax(220px,1fr)}
.media-count{white-space:nowrap;color:#047857;font-size:13px}
.media-menu{display:none;gap:8px;align-items:center;flex-wrap:wrap}
.media-menu.active{display:flex}
.media-menu button{max-width:100%;overflow:hidden;text-overflow:ellipsis}
@media(max-width:1050px){.api-grid{grid-template-columns:300px minmax(0,1fr)}}
@media(max-width:720px){.endpoint-grid{grid-template-columns:1fr}.endpoint-grid button{width:max-content}.api-grid{grid-template-columns:1fr}.api-provider-list{position:static;max-height:none;overflow:visible;grid-template-columns:repeat(2,minmax(0,1fr))}}
.toggle{display:flex;gap:8px;align-items:center;font-size:13px;margin:8px 0}
.model-list{display:grid;gap:8px;margin-top:10px;max-height:280px;overflow:auto;padding-right:4px}
.model-item{display:flex;gap:8px;align-items:flex-start;font-size:13px}
.model-kind-select{width:72px;flex:0 0 72px;padding:2px 3px;border:1px solid #ddd;border-radius:4px;background:#fff;font:12px/1.2 system-ui,-apple-system,Segoe UI,sans-serif}
.model-item.off{color:#999}
.model-name{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}
.latency{margin-left:6px;font-weight:700}
.latency.good{color:#047857}.latency.warn{color:#a16207}.latency.bad{color:#b91c1c}
.progress-wrap{display:none;align-items:center;gap:10px;margin:8px 0}
.progress-wrap.active{display:flex}
progress{width:190px;height:12px}
details{margin-top:22px}
.section-note{margin:6px 0 12px}
.tabs{display:flex;gap:8px;align-items:center;margin:18px 0 12px;flex-wrap:wrap}
.tab-button{background:#fff;color:#111;border:1px solid #ddd}
.tab-button.active{background:#111;color:#fff;border-color:#111}
.tab-panel{display:none}
.tab-panel.active{display:block}
</style>
</head>
<body>
<main>
<h1>9Router Lite</h1>
<div class="muted">接口基址是 <code>/v1</code>，配置保存在本机 <code>data/config.json</code>。</div>

<div class="bar">
<button onclick="saveGateway()">保存网关设置</button>
<button class="secondary" onclick="openModelsPage()">打开模型页</button>
<button class="secondary" onclick="window.location.href='/admin/help'" type="button">说明</button>
<a href="/health" target="_blank">/health</a>
<span id="status" class="muted"></span>
</div>

<details class="card">
<summary><strong>修改登录密码</strong></summary>
<div class="muted">修改后会立即退出所有已登录的管理页面，需要使用新密码重新登录。</div>
<div class="field"><label>当前密码</label><input id="adminCurrentPassword" type="password" autocomplete="current-password"></div>
<div class="field"><label>新密码</label><input id="adminNewPassword" type="password" autocomplete="new-password" placeholder="至少 8 个字符"></div>
<div class="field"><label>确认新密码</label><input id="adminConfirmPassword" type="password" autocomplete="new-password"></div>
<div class="bar">
<button onclick="changeAdminPassword()" type="button">保存新密码</button>
<button class="secondary" onclick="toggleAdminPasswordFields()" type="button">显示/隐藏</button>
<span id="adminPasswordStatus" class="muted"></span>
</div>
</details>

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
<div class="endpoint-grid"><span class="muted">OpenAI Base URL</span><input id="programBaseUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programBaseUrl','OpenAI Base URL')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">Anthropic Base URL</span><input id="programAnthropicBaseUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programAnthropicBaseUrl','Anthropic Base URL')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">模型列表</span><input id="programModelsUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programModelsUrl','模型列表地址')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">Claude Code 模型</span><input id="programV2ModelsUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programV2ModelsUrl','Claude Code 模型列表')" type="button">复制</button></div>
<div class="endpoint-grid"><span class="muted">健康检查</span><input id="programHealthUrl" type="text" readonly><button class="secondary" onclick="copyEndpoint('programHealthUrl','健康检查地址')" type="button">复制</button></div>
<div class="endpoint-grid media-tool-row"><span class="muted">图片接口</span><button class="secondary" onclick="toggleMediaCurlMenu('image')" type="button">选择模型复制 curl</button><span id="mediaCount_image" class="media-count"></span><div id="mediaMenu_image" class="media-menu"></div></div>
<div class="endpoint-grid media-tool-row"><span class="muted">视频接口</span><button class="secondary" onclick="toggleMediaCurlMenu('video')" type="button">选择模型复制 curl</button><span id="mediaCount_video" class="media-count"></span><div id="mediaMenu_video" class="media-menu"></div></div>
<div class="endpoint-grid media-tool-row"><span class="muted">音频接口</span><button class="secondary" onclick="toggleMediaCurlMenu('audio')" type="button">选择模型复制 curl</button><span id="mediaCount_audio" class="media-count"></span><div id="mediaMenu_audio" class="media-menu"></div></div>
<div class="endpoint-grid media-tool-row"><span class="muted">TTS 接口</span><button class="secondary" onclick="toggleMediaCurlMenu('tts')" type="button">选择声音复制 curl</button><span id="mediaCount_tts" class="media-count"></span><div id="mediaMenu_tts" class="media-menu"></div></div>
<div class="muted">第三方 Agent 的 Base URL 填 <code>/v1</code>，API Key 填上面的访问密钥；程序健康检查建议使用 JSON 地址。</div>
</div>

<details class="card">
<summary><strong>导入本地 config.json</strong></summary>
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
</details>

<div class="bar">
<label class="toggle"><input type="checkbox" id="autoProbeEnabled"> 启用定时自动探测</label>
<label class="inline-field muted">基础间隔分钟 <input id="autoProbeInterval" type="number" min="1" step="1" value="60"></label>
<span class="muted">实际间隔会在基础间隔的 70% 到 130% 之间随机浮动，探测后自动把可用模型发布到 <code>/v1/models</code>。</span>
</div>

<div class="bar">
<h2 style="margin:0">已连接源</h2>
<button class="secondary" onclick="probeAllProviders()">一键探测</button>
<button class="secondary" onclick="stopProbe()">停止探测</button>
<span id="probeAllStatus" class="muted"></span>
</div>
<div id="probeAllProgress" class="progress-wrap"><progress value="0" max="1"></progress><span class="muted">0/0</span></div>
<div id="providerStatus" class="panel-grid"></div>

<div class="tabs">
<button id="tab_api" class="tab-button active" onclick="showMainTab('api')" type="button">API 密钥提供商</button>
<button id="tab_oauth" class="tab-button" onclick="showMainTab('oauth')" type="button">OAuth 提供商</button>
<button id="tab_auto" class="tab-button" onclick="showMainTab('auto')" type="button">Auto 模型</button>
<button id="tab_groups" class="tab-button" onclick="showMainTab('groups')" type="button">模型分组</button>
</div>

<section id="panel_auto" class="tab-panel">
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
<div class="field">
<label>多模态候选模型</label>
<div class="inline-field grow">
<input id="autoVisionModelInput" placeholder="例如 Cline/google/gemini-3.1-pro-preview">
<button class="secondary" onclick="addAutoVisionModel()" type="button">添加</button>
</div>
</div>
<div class="muted">完整对话历史中存在图片时，由这里第一个可用模型接管；图片退出上下文后恢复普通候选。</div>
<div id="autoVisionModelList" class="model-list"></div>
<div class="bar"><button class="secondary" onclick="saveGateway()">保存 Auto 设置</button><span id="autoModelStatus" class="muted"></span></div>
</div>
</section>

<section id="panel_api" class="tab-panel active">
<h2>API 密钥提供商</h2>
<div class="muted section-note">输入 Base URL 和 API Key 后点击拉取模型；模型会直接显示在对应卡片底部，并可在卡片内探测和发布。</div>
<div id="apiProviders" class="api-grid"></div>
</section>

<section id="panel_oauth" class="tab-panel">
<h2>OAuth 提供商</h2>
<div class="muted section-note">一键探测只会自动发布探测可用的模型；你也可以手动勾选探测失败的模型并保存发布列表。</div>
<div id="publishProviders" class="panel-grid"></div>
</section>

<section id="panel_groups" class="tab-panel">
<div class="bar">
<h2 style="margin:0">模型分组</h2>
<button class="secondary" onclick="addModelGroup()" type="button">新建分组</button>
<span id="modelGroupStatus" class="muted"></span>
</div>
<div class="muted section-note">每个分组使用独立 API Key；Base URL 仍然使用上面的 <code>/v1</code>。分组 Key 只能看到并调用已勾选的模型。</div>
<div id="modelGroups" class="panel-grid"></div>
</section>

<details>
<summary>原始配置</summary>
<div class="bar"><button class="secondary" onclick="save()">保存原始配置</button></div>
<textarea id="cfg" spellcheck="false">` + escaped + `</textarea>
</details>

<script>
const fixedAPIProviderIDs=['glm','groq','deepseek','mimo'];
const publishProviderIDs=['oc','mmf','qoder','kilo','cline'];
const statusProviderIDs=['oc','mmf','qoder','kilo','cline','glm','groq','deepseek','mimo'];
let probeStopRequested=false;
let expandedAPIProviderID='';
const providerProbeControllers=new Map();
function parseConfig(){ try { return JSON.parse(document.getElementById('cfg').value); } catch { return null; } }
function showMainTab(name){
  ['api','oauth','auto','groups'].forEach(id=>{
    const panel=document.getElementById('panel_'+id); if(panel) panel.classList.toggle('active',id===name);
    const tab=document.getElementById('tab_'+id); if(tab) tab.classList.toggle('active',id===name);
  });
}
function ensureEndpointRow(afterID,id,label,path){
  if(!document.getElementById(id)){
    const after=document.getElementById(afterID);
    const parent=after && after.closest ? after.closest('.endpoint-grid') : null;
    if(parent){
      parent.insertAdjacentHTML('afterend','<div class="endpoint-grid"><span class="muted">'+esc(label)+'</span><input id="'+esc(id)+'" type="text" readonly><button class="secondary" onclick="copyEndpoint(\''+esc(id)+'\',\''+esc(label)+'\')" type="button">复制</button></div>');
    }
  }
  const input=document.getElementById(id);
  if(input) input.value=location.origin+path;
}
function pathWithKey(path,cfg){
  const key=(cfg && cfg.access_key) || '';
  return key ? path+(path.includes('?')?'&':'?')+'key='+encodeURIComponent(key) : path;
}
function setConfig(cfg){
  ensureBlankCustomProvider(cfg);
  if(!cfg.auto_model) cfg.auto_model={enabled:false,models:[],vision_models:[]};
  if(!Array.isArray(cfg.auto_model.vision_models)) cfg.auto_model.vision_models=[];
  if(!Array.isArray(cfg.model_groups)) cfg.model_groups=[];
  document.getElementById('cfg').value=JSON.stringify(cfg,null,2);
  document.getElementById('accessKey').value=cfg.access_key || '';
  document.getElementById('baseUrl').value=location.origin+'/v1';
  document.getElementById('programBaseUrl').value=location.origin+'/v1';
  document.getElementById('programAnthropicBaseUrl').value=location.origin+'/anthropic';
  document.getElementById('programModelsUrl').value=location.origin+'/v1/models';
  document.getElementById('programV2ModelsUrl').value=location.origin+'/v2/models';
  document.getElementById('programHealthUrl').value=location.origin+'/health?format=json';
  document.getElementById('autoProbeEnabled').checked=!!cfg.auto_probe_enabled;
  document.getElementById('autoProbeInterval').value=cfg.auto_probe_interval_minutes || 60;
  document.getElementById('autoModelEnabled').checked=!!cfg.auto_model.enabled;
  renderAutoModels(cfg);
  renderMediaCurlMenus(cfg);
  renderModelGroups(cfg);
}
function providerConnected(p){
  if(!p || !p.enabled) return false;
  if(providerAPIKeys(p).length || p.access_token || p.type === 'opencode-free' || p.type === 'mimo-free') return true;
  return isCustomProvider(p) && !!((p.base_url || '').trim()) && p.base_url !== 'https://example.com/v1';
}
function authStatus(p){ return (p && p.provider_specific_data && p.provider_specific_data.authStatus) || 'ok'; }
function authError(p){ return (p && p.provider_specific_data && p.provider_specific_data.lastAuthError) || ''; }
function manualOverride(p){ return !!(p && p.provider_specific_data && p.provider_specific_data.manualPublishOverride==='true'); }
function isCustomProvider(p){ return !!(p && ['openai','anthropic'].includes(p.type) && /^custom/.test(p.id || '')); }
function isClaudeCodeProvider(p){ return !!(p && p.type==='anthropic' && providerDataValue(p,'anthropicRequestMode')==='claude-code'); }
function isResponsesProvider(p){ return !!(p && p.type==='openai' && providerDataValue(p,'openaiRequestMode')==='responses'); }
function providerRouteID(p){ return isCustomProvider(p) && String(p.name || '').trim() ? String(p.name).trim() : String((p && p.id) || ''); }
function providerAPIKeys(p){ return unique([((p && p.api_key) || ''), ...((p && Array.isArray(p.api_keys)) ? p.api_keys : [])].map(x=>String(x || '').trim()).filter(Boolean)); }
function hasMediaEndpoint(p){ return !!(p && ((p.image_endpoint || p.image_base_url || '').trim() || (p.image_edit_endpoint || '').trim() || (p.video_endpoint || p.video_base_url || '').trim() || (p.audio_endpoint || p.audio_base_url || '').trim() || (p.tts_endpoint || '').trim())); }
function customHasContent(p){ return !!(p && (providerAPIKeys(p).length || hasMediaEndpoint(p) || ((p.base_url || '').trim() && p.base_url !== 'https://example.com/v1') || (p.provider_specific_data && p.provider_specific_data.apiModelsFetched==='true'))); }
function nextCustomID(cfg){ const ids=new Set((cfg.providers || []).map(p=>p.id)); let i=1; while(ids.has(i===1?'custom':'custom'+i)) i++; return i===1?'custom':'custom'+i; }
function ensureBlankCustomProvider(cfg){
  if(!Array.isArray(cfg.providers)) cfg.providers=[];
  let blankKept=false;
  cfg.providers=cfg.providers.filter(p=>{
    if(!isCustomProvider(p) || customHasContent(p)) return true;
    if(blankKept) return false;
    p.name=p.name || 'Custom Compatible';
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
function mediaKindInfo(kind){
  if(kind==='image') return {path:'/v1/images', promptKey:'prompt', payload:{prompt:'替换为用户最终生图提示词',size:'替换为用户需要的图片尺寸，例如 1024x768',extra_body:{response_format:'url'}}};
  if(kind==='video') return {path:'/v1/videos', promptKey:'prompt', payload:{prompt:'替换为用户最终视频提示词',height:768,width:1152,num_frames:121,frame_rate:24}};
  if(kind==='audio') return {path:'/v1/audio', promptKey:'file'};
  if(kind==='tts') return {path:'/v1/tts', promptKey:'text'};
  return {path:'/v1/images', payload:{prompt:'用户提示词'}};
}
function mediaEndpointValue(p,kind){ return (p && (p[kind+'_endpoint'] || p[kind+'_base_url']) || '').trim(); }
function mediaModelMatches(kind,model){
  const lower=String(model || '').toLowerCase();
  const words=kind==='image' ? ['image','imagen','flux','sdxl','dall-e','gpt-image','agnes-image'] : kind==='video' ? ['video','veo','sora','kling','runway','agnes-video'] : kind==='tts' ? ['tts','voice','neural','zh-','en-'] : ['audio','speech','whisper','transcrib'];
  return words.some(word=>lower.includes(word));
}
function isMediaModel(model){ return mediaModelMatches('image',model) || mediaModelMatches('video',model) || mediaModelMatches('audio',model) || mediaModelMatches('tts',model); }
function providerModelKind(p,model){
  const value=String((p && p.model_kinds && p.model_kinds[model]) || 'auto').toLowerCase();
  return ['text','image','video','audio','tts'].includes(value) ? value : 'auto';
}
function providerModelMatchesKind(p,kind,model){
  const explicit=providerModelKind(p,model);
  if(explicit==='text') return false;
  if(explicit!=='auto') return explicit===kind;
  return mediaModelMatches(kind,model);
}
function providerIsMediaModel(p,model){ return ['image','video','audio','tts'].some(kind=>providerModelMatchesKind(p,kind,model)); }
function chatProbeModels(p){ return unique((p && p.models) || []).filter(model=>!providerIsMediaModel(p,model)); }
function mediaModelsForKind(cfg,kind){
  const out=[];
  ((cfg && cfg.providers) || []).forEach(p=>{
    if(!p || !p.enabled || !mediaEndpointValue(p,kind)) return;
    const route=providerRouteID(p);
    selectedModels(p).filter(model=>providerModelMatchesKind(p,kind,model)).forEach(model=>out.push({provider:p.id,name:p.name || p.id,model,full:route+'/'+model}));
  });
  return out;
}
function renderMediaCurlMenus(cfg){
  ['image','video','audio','tts'].forEach(kind=>{
    const root=document.getElementById('mediaMenu_'+kind); if(!root) return;
    const models=mediaModelsForKind(cfg,kind);
    const count=document.getElementById('mediaCount_'+kind);
    if(count){
      const label=kind==='image' ? '图片模型' : kind==='video' ? '视频模型' : kind==='audio' ? '音频模型' : 'TTS 声音';
      count.textContent=models.length ? models.length+' 个可用'+label : '';
    }
    root.innerHTML=models.length ? models.map(item=>'<button class="small secondary" title="'+esc(item.full)+'" data-kind="'+esc(kind)+'" data-provider="'+esc(item.provider)+'" data-model="'+esc(item.model)+'" onclick="copyMediaCurl(this)" type="button">'+esc(item.full)+'</button>').join('') : '<span class="muted">暂无已配置的媒体模型</span>';
  });
}
function toggleMediaCurlMenu(kind){
  const root=document.getElementById('mediaMenu_'+kind); if(!root) return;
  root.classList.toggle('active');
}
function providerByID(cfg,id){ return ((cfg && cfg.providers) || []).find(p=>p && p.id===id) || null; }
function shellSingleQuote(value){ return '\''+String(value || '').replaceAll('\'','\'"\'"\'')+'\''; }
function mediaJSONCurl(endpoint,key,body){
  return 'curl -X POST "'+endpoint+'" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer '+key+'" \\\n  -d '+shellSingleQuote(JSON.stringify(body,null,2));
}
function providerDataValue(p,key){ return (p && p.provider_specific_data && p.provider_specific_data[key]) || ''; }
function templateFieldValue(id,key){
  const el=document.getElementById(key+'_'+id);
  return el ? el.value.trim() : '';
}
function replaceAllTemplate(value, vars){
  let out=String(value || '');
  Object.keys(vars).forEach(key=>{ out=out.replaceAll('{{'+key+'}}', vars[key]); });
  return out;
}
function renderCurlTemplate(template, vars){ return replaceAllTemplate(template, vars); }
function isAgnesImage(endpoint,model){
  return String(endpoint || '').toLowerCase().includes('agnes-ai.com') || String(model || '').toLowerCase().includes('agnes-image');
}
function transparentBackgroundModel(model){
  const lower=String(model || '').toLowerCase();
  if(lower==='gpt-image-1' || lower==='gpt-image-2') return 'gpt-image-1';
  if(lower==='codex-gpt-image-1' || lower==='codex-gpt-image-2') return 'codex-gpt-image-1';
  return '';
}
function mediaImageCurl(endpoint,key,model,editEndpoint){
  const agnes=isAgnesImage(endpoint,model);
  const body=agnes
    ? {model:model,prompt:'替换为用户最终生图提示词',size:'替换为用户需要的图片尺寸，例如 1024x768',extra_body:{response_format:'url'}}
    : {model:model,prompt:'替换为用户最终生图提示词',n:1};
  let out='# 文生图\n'+mediaJSONCurl(endpoint,key,body);
  if(agnes){
    out += '\n# 生成图片 URL 位于 data[0].url';
    const imageToImageURL={model:model,prompt:'替换为用户最终图片编辑提示词',size:'替换为用户需要的图片尺寸，例如 1024x768',extra_body:{image:['https://example.com/input-image.png'],response_format:'url'}};
    out += '\n\n# 图生图 / 图片编辑：URL 输入，URL 输出\n'+mediaJSONCurl(endpoint,key,imageToImageURL)+'\n# 生成图片 URL 位于 data[0].url';
    const imageToImageBase64={model:model,prompt:'替换为用户最终图片编辑提示词',size:'替换为用户需要的图片尺寸，例如 1024x768',extra_body:{image:['data:image/png;base64,BASE64_HERE'],response_format:'b64_json'}};
    out += '\n\n# 图生图 / 图片编辑：Data URI Base64 输入，Base64 输出\n'+mediaJSONCurl(endpoint,key,imageToImageBase64)+'\n# 生成图片 Base64 位于 data[0].b64_json';
    const multiImage={model:model,prompt:'替换为用户最终多图合成提示词',size:'替换为用户需要的图片尺寸，例如 1024x768',extra_body:{image:['https://example.com/input-image-1.png','https://example.com/input-image-2.png'],response_format:'url'}};
    out += '\n\n# 多图合成：多个 URL 输入，URL 输出\n'+mediaJSONCurl(endpoint,key,multiImage)+'\n# 生成图片 URL 位于 data[0].url';
  }
  const transparentModel=transparentBackgroundModel(model);
  if(!agnes && transparentModel){
    const transparentBody={model:transparentModel,prompt:'替换为用户最终透明背景生图提示词',n:1,background:'transparent',output_format:'png'};
    out += '\n\n# 透明背景 PNG 生图\n'+mediaJSONCurl(endpoint,key,transparentBody);
  }
  if(editEndpoint){
    const editBody={model:model,prompt:'替换为用户最终图片编辑提示词',images:[{image_url:'https://example.com/input.png'}]};
    out += '\n\n# 图片编辑：上传本地图片文件\n'
      +'curl -X POST "'+editEndpoint+'" \\\n'
      +'  -H "Authorization: Bearer '+key+'" \\\n'
      +'  -F "model='+model+'" \\\n'
      +'  -F "prompt=替换为用户最终图片编辑提示词" \\\n'
      +'  -F "n=1" \\\n'
      +'  -F "image=@替换为本地图片路径，例如 input.png"';
    out += '\n\n# 图片编辑：使用图片 URL\n'+mediaJSONCurl(editEndpoint,key,editBody);
  }
  return out;
}
function mediaAudioCurl(endpoint,key,model){
  return 'curl -X POST "'+endpoint+'" \\\n  -H "Authorization: Bearer '+key+'" \\\n  -F "file=@替换为本地音频文件路径，例如 audio.mp3" \\\n  -F "model='+model+'" \\\n  -F "response_format=json" \\\n  -F "language=替换为音频语言代码，例如 zh 或 en"';
}
function mediaVideoQueryCurl(endpoint,key){
  const queryEndpoint=videoQueryEndpoint(endpoint);
  return 'curl -X GET "'+queryEndpoint+'" \\\n  -H "Authorization: Bearer '+key+'"';
}
function isAgnesVideo(endpoint,model){
  return String(endpoint || '').toLowerCase().includes('agnes-ai.com') || String(model || '').toLowerCase().includes('agnes-video');
}
function mediaVideoCurl(endpoint,key,model){
  const textBody={model:model,prompt:'替换为用户最终文生视频提示词',height:768,width:1152,num_frames:121,frame_rate:24};
  let out='# 文生视频：响应里的 video_id 用于查询结果\n'+mediaJSONCurl(endpoint,key,textBody);
  if(isAgnesVideo(endpoint,model)){
    const singleImage={model:model,prompt:'替换为用户最终图生视频提示词',image:'https://example.com/input-image.png',num_frames:121,frame_rate:24};
    out += '\n\n# 单图生视频\n'+mediaJSONCurl(endpoint,key,singleImage);
    const multiImage={model:model,prompt:'替换为用户最终多图视频提示词',extra_body:{image:['https://example.com/input-image-1.png','https://example.com/input-image-2.png']},num_frames:121,frame_rate:24};
    out += '\n\n# 多图视频生成\n'+mediaJSONCurl(endpoint,key,multiImage);
    const keyframes={model:model,prompt:'替换为用户最终关键帧动画提示词',extra_body:{image:['https://example.com/keyframe-1.png','https://example.com/keyframe-2.png'],mode:'keyframes'},num_frames:121,frame_rate:24};
    out += '\n\n# 关键帧动画\n'+mediaJSONCurl(endpoint,key,keyframes);
  }
  return out+'\n\n# 建议每 5 秒轮询一次，直到 status 为 completed\n'+mediaVideoQueryCurl(endpoint,key);
}
function videoQueryEndpoint(endpoint){
  let queryEndpoint='https://apihub.agnes-ai.com/agnesapi?video_id=替换为创建任务返回的 video_id';
  try{
    const url=new URL(endpoint);
    if(url.hostname.includes('agnes-ai.com')) queryEndpoint=url.origin+'/agnesapi?video_id=替换为创建任务返回的 video_id';
  }catch(e){}
  return queryEndpoint;
}
function customCurlTemplate(p,kind){
  const key=kind==='image'?'curlTemplateImage':kind==='video'?'curlTemplateVideo':'curlTemplateAudio';
  return providerDataValue(p,key).trim();
}
function mediaCurl(kind,providerID,model){
  const cfg=parseConfig() || {};
  const p=providerByID(cfg,providerID);
  if(!p) throw new Error('provider not found: '+providerID);
  const endpoint=mediaEndpointValue(p,kind);
  if(!endpoint) throw new Error('该 provider 没有配置 '+kind+' Endpoint');
  const key=providerAPIKeys(p)[0] || 'YOUR_UPSTREAM_API_KEY';
  const customTemplate=customCurlTemplate(p,kind);
  if(customTemplate){
    return renderCurlTemplate(customTemplate,{
      endpoint:endpoint,
      image_edit_endpoint:mediaEndpointValue(p,'image_edit'),
      key:key,
      model:model,
      transparent_model:transparentBackgroundModel(model) || model,
      prompt:'替换为用户最终提示词',
      image_prompt:'替换为用户最终生图提示词',
      transparent_prompt:'替换为用户最终透明背景生图提示词',
      edit_prompt:'替换为用户最终图片编辑提示词',
      video_prompt:'替换为用户最终视频提示词',
      audio_file:'替换为本地音频文件路径，例如 audio.mp3',
      image_file:'替换为本地图片路径，例如 input.png',
      tts_text:'你好世界',
      video_query_endpoint:videoQueryEndpoint(endpoint)
    });
  }
  if(kind==='image') return mediaImageCurl(endpoint,key,model,mediaEndpointValue(p,'image_edit'));
  if(kind==='audio') return mediaAudioCurl(endpoint,key,model);
  if(kind==='tts') return renderCurlTemplate(defaultCurlTemplate('tts'),{endpoint:endpoint,key:key,model:model,tts_text:'你好世界'});
  const body={model:model,...mediaKindInfo(kind).payload};
  if(kind==='video') return mediaVideoCurl(endpoint,key,model);
  return mediaJSONCurl(endpoint,key,body);
}
async function copyMediaCurl(button){
  try{
    const kind=button.getAttribute('data-kind');
    const provider=button.getAttribute('data-provider');
    const model=button.getAttribute('data-model');
    await copyTextValue(mediaCurl(kind,provider,model));
    setText('status','ok',provider+'/'+model+' 调用格式已复制');
    const root=document.getElementById('mediaMenu_'+kind); if(root) root.classList.remove('active');
  }catch(e){ setText('status','err','复制失败：'+e.message); }
}
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
function lockedModels(p){ return unique((p && p.locked_models) || []); }
function isLockedModel(p,model){ return lockedModels(p).includes(model); }
function autoModels(cfg){ return unique((cfg && cfg.auto_model && cfg.auto_model.models) || []); }
function autoVisionModels(cfg){ return unique((cfg && cfg.auto_model && cfg.auto_model.vision_models) || []); }
function renderAutoModels(cfg){
  const root=document.getElementById('autoModelList'); if(!root) return;
  const models=autoModels(cfg);
  root.innerHTML=models.length ? models.map((model,i)=>'<div class="model-item"><span class="model-name">'+esc(model)+'</span><button class="small secondary" onclick="moveAutoModel('+i+',-1)" '+(i===0?'disabled':'')+'>上移</button><button class="small secondary" onclick="moveAutoModel('+i+',1)" '+(i===models.length-1?'disabled':'')+'>下移</button><button class="small secondary" onclick="removeAutoModel('+i+')">删除</button></div>').join('') : '<div class="muted">还没有候选模型。</div>';
  const visionRoot=document.getElementById('autoVisionModelList'); if(!visionRoot) return;
  const visionModels=autoVisionModels(cfg);
  visionRoot.innerHTML=visionModels.length ? visionModels.map((model,i)=>'<div class="model-item"><span class="model-name">'+esc(model)+'</span><button class="small secondary" onclick="moveAutoVisionModel('+i+',-1)" '+(i===0?'disabled':'')+'>上移</button><button class="small secondary" onclick="moveAutoVisionModel('+i+',1)" '+(i===visionModels.length-1?'disabled':'')+'>下移</button><button class="small secondary" onclick="removeAutoVisionModel('+i+')">删除</button></div>').join('') : '<div class="muted">还没有多模态候选模型。</div>';
}
function updateAutoModelList(key,mutator){
  const cfg=parseConfig(); if(!cfg) return;
  if(!cfg.auto_model) cfg.auto_model={enabled:false,models:[],vision_models:[]};
  cfg.auto_model.enabled=!!document.getElementById('autoModelEnabled').checked;
  cfg.auto_model.models=autoModels(cfg);
  cfg.auto_model.vision_models=autoVisionModels(cfg);
  mutator(cfg.auto_model[key]);
  setConfig(cfg);
}
function updateAutoModels(mutator){ updateAutoModelList('models',mutator); }
function updateAutoVisionModels(mutator){ updateAutoModelList('vision_models',mutator); }
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
function addAutoVisionModel(){
  const input=document.getElementById('autoVisionModelInput'); const value=(input && input.value || '').trim();
  if(!value || value==='auto'){ setText('autoModelStatus','err','请输入支持图片理解的真实模型 ID'); return; }
  if(!value.includes('/')){ setText('autoModelStatus','err','多模态候选需要使用 provider/model 格式'); return; }
  updateAutoVisionModels(models=>{ models.push(value); });
  if(input) input.value='';
  setText('autoModelStatus','ok','已添加多模态候选，记得保存');
}
function addAutoVisionModelValue(source){
  const value=(typeof source==='string' ? source : (source && source.getAttribute('data-model')) || '').trim();
  if(!value || value==='auto' || !value.includes('/')){ setText('autoModelStatus','err','多模态候选需要使用 provider/model 格式'); return; }
  updateAutoVisionModels(models=>{ models.push(value); });
  setText('autoModelStatus','ok','已加入多模态候选，记得保存');
}
function removeAutoVisionModel(index){ updateAutoVisionModels(models=>{ models.splice(index,1); }); setText('autoModelStatus','ok','已移除多模态候选，记得保存'); }
function moveAutoVisionModel(index,delta){
  updateAutoVisionModels(models=>{ const next=index+delta; if(next<0 || next>=models.length) return; const item=models[index]; models.splice(index,1); models.splice(next,0,item); });
  setText('autoModelStatus','ok','已调整多模态候选顺序，记得保存');
}
function publishedGroupModels(cfg){
  const out=[];
  ((cfg && cfg.providers) || []).filter(p=>p && p.enabled && !isClaudeCodeProvider(p)).forEach(p=>{
    visibleModels(p).filter(model=>!providerIsMediaModel(p,model)).forEach(model=>out.push(providerRouteID(p)+'/'+model));
  });
  if(cfg && cfg.auto_model && cfg.auto_model.enabled) out.push('auto');
  return unique(out).sort();
}
function newGroupKey(){
  const bytes=new Uint8Array(18);
  crypto.getRandomValues(bytes);
  return '9r_'+[...bytes].map(x=>x.toString(16).padStart(2,'0')).join('');
}
function renderModelGroups(cfg){
  const root=document.getElementById('modelGroups'); if(!root) return;
  const groups=(cfg && Array.isArray(cfg.model_groups)) ? cfg.model_groups : [];
  const models=publishedGroupModels(cfg);
  root.innerHTML=groups.length ? groups.map(group=>{
    const id=group.id;
    const selected=new Set(group.models || []);
    const rows=models.length ? models.map(model=>'<label class="model-item"><input type="checkbox" data-group-model="'+esc(id)+'" value="'+esc(model)+'" '+(selected.has(model)?'checked':'')+'><span class="model-name">'+esc(model)+'</span></label>').join('') : '<div class="muted">当前没有已发布的文本模型。</div>';
    return '<div class="card"><div class="card-title-row"><h3>'+esc(group.name || '未命名分组')+'</h3><span class="provider-badge">Model Group</span></div>'
      +'<label class="toggle"><input id="groupEnabled_'+esc(id)+'" type="checkbox" '+(group.enabled?'checked':'')+'> 启用</label>'
      +'<div class="field"><label>分组名称</label><input id="groupName_'+esc(id)+'" value="'+esc(group.name || '')+'" placeholder="例如：免费模型组"></div>'
      +'<div class="field"><label>独立 API Key</label><div class="inline-field grow"><input id="groupKey_'+esc(id)+'" value="'+esc(group.api_key || '')+'"><button class="small secondary" onclick="copyModelGroupKey(\''+esc(id)+'\')" type="button">复制</button></div></div>'
      +'<div class="bar"><button class="small secondary" onclick="selectModelGroupModels(\''+esc(id)+'\',true)" type="button">全选</button><button class="small secondary" onclick="selectModelGroupModels(\''+esc(id)+'\',false)" type="button">取消所有选择</button><span class="muted">已选择 '+selected.size+' 个</span></div>'
      +'<div class="model-list">'+rows+'</div>'
      +'<div class="bar"><button onclick="saveModelGroup(\''+esc(id)+'\')" type="button">保存分组</button><button class="secondary" onclick="deleteModelGroup(\''+esc(id)+'\')" type="button">删除分组</button><span id="groupStatus_'+esc(id)+'" class="muted"></span></div></div>';
  }).join('') : '<div class="muted">还没有模型分组。</div>';
}
function addModelGroup(){
  const cfg=parseConfig(); if(!cfg) return;
  if(!Array.isArray(cfg.model_groups)) cfg.model_groups=[];
  const id='group_'+Date.now().toString(36);
  cfg.model_groups.push({id,name:'新模型分组',api_key:newGroupKey(),enabled:true,models:[]});
  setConfig(cfg);
  setText('modelGroupStatus','ok','已新建分组，请选择模型后保存');
}
function selectModelGroupModels(id,checked){
  document.querySelectorAll('input[data-group-model="'+id+'"]').forEach(node=>{ node.checked=!!checked; });
}
async function copyModelGroupKey(id){
  try{
    const input=document.getElementById('groupKey_'+id);
    await copyTextValue(input ? input.value : '');
    setText('groupStatus_'+id,'ok','API Key 已复制');
  }catch(e){ setText('groupStatus_'+id,'err','复制失败：'+e.message); }
}
async function saveModelGroup(id){
  try{
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.model_groups)) throw new Error('config is invalid');
    applyGatewaySettings(cfg);
    cfg.model_groups=cfg.model_groups.map(group=>{
      if(group.id!==id) return group;
      return {...group,
        name:(document.getElementById('groupName_'+id).value || '').trim(),
        api_key:(document.getElementById('groupKey_'+id).value || '').trim(),
        enabled:!!document.getElementById('groupEnabled_'+id).checked,
        models:[...document.querySelectorAll('input[data-group-model="'+id+'"]:checked')].map(node=>node.value)
      };
    });
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('groupStatus_'+id,'ok','分组已保存');
  }catch(e){ setText('groupStatus_'+id,'err',e.message); }
}
async function deleteModelGroup(id){
  try{
    if(!confirm('确定删除这个模型分组？')) return;
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.model_groups)) throw new Error('config is invalid');
    cfg.model_groups=cfg.model_groups.filter(group=>group.id!==id);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('modelGroupStatus','ok','分组已删除');
  }catch(e){ setText('modelGroupStatus','err',e.message); }
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
  if(id==='kilo') return '<div class="bar"><button class="small" onclick="startKilo()">开始登录</button><button class="small secondary" onclick="pollKilo()">轮询令牌</button><span id="kiloStatus" class="muted"></span></div>';
  if(id==='cline') return '<div class="bar"><button class="small" onclick="startCline()">开始登录</button><span id="clineStatus" class="muted"></span></div>';
  return '';
}
function canFetchOAuthModels(id){ return ['oc','mmf','qoder','kilo'].includes(id); }
function fetchOAuthModelsButton(p){
  if(!canFetchOAuthModels(p.id)) return '';
  return '<button class="small secondary" onclick="fetchOAuthProviderModels(\''+p.id+'\')" '+(!providerConnected(p)?'disabled':'')+'>拉取模型</button>';
}
function providerCategory(p){
  if(!p) return 'Provider';
  if(isCustomProvider(p)) return p.type==='anthropic' ? (providerDataValue(p,'anthropicRequestMode')==='claude-code' ? 'Custom Claude Code' : 'Custom Anthropic') : (isResponsesProvider(p) ? 'Custom Responses' : 'Custom OpenAI');
  if(['kilo','cline'].includes(p.id)) return 'OAuth';
  if(['oc','mmf','qoder'].includes(p.id) || String(p.type || '').includes('free')) return 'Free';
  if(p.type==='openai') return 'API Key';
  return p.type || 'Provider';
}
function providerTitleHTML(p,dotClass){
  return '<div class="card-title-row"><h3><span class="'+dotClass+'"></span>'+esc(p.name)+'</h3><span class="provider-badge">'+esc(providerCategory(p))+'</span></div>';
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
    const remove='<button class="delete-model secondary" type="button" title="删除模型" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" onclick="deleteProviderModel(this)">−</button>';
    const addAuto='<button class="add-auto-model secondary" type="button" title="Add to Auto" data-model="'+esc(providerRouteID(p)+'/'+model)+'" onclick="addAutoModelValue(this)">+</button>';
    const addVision=providerIsMediaModel(p,model)?'':'<button class="add-auto-model secondary" type="button" title="加入多模态候选" data-model="'+esc(providerRouteID(p)+'/'+model)+'" onclick="addAutoVisionModelValue(this)">图</button>';
    const locked=isLockedModel(p,model);
    const lock='<button class="model-lock secondary '+(locked?'locked':'')+'" type="button" title="'+(locked?'已上锁：一键/定时探测会跳过':'未上锁：一键/定时探测会包含')+'" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" onclick="toggleModelLock(this)">'+(locked?'🔒':'🔓')+'</button>';
    const kind=providerModelKind(p,model);
    const kindSelect='<select class="model-kind-select" title="模型类型" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" onchange="saveModelKind(this)">'+[['auto','自动'],['text','文本'],['image','图片'],['video','视频'],['audio','音频'],['tts','TTS']].map(item=>'<option value="'+item[0]+'" '+(kind===item[0]?'selected':'')+'>'+item[1]+'</option>').join('')+'</select>';
    return '<div class="model-item '+(usable?'':'off')+'">'+toggle+remove+addAuto+addVision+lock+kindSelect+'<span class="model-name">'+esc(model)+note+latencyHTML(p,model)+(usable?'':modelErrorHTML(p,model))+'</span></div>';
  }).join('');
}
function renderProviderStatus(){
  const root=document.getElementById('providerStatus'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const order=new Map(statusProviderIDs.map((id,index)=>[id,index]));
  const providers=cfg.providers.filter(providerConnected).sort((a,b)=>(order.has(a.id)?order.get(a.id):999)-(order.has(b.id)?order.get(b.id):999));
  const items=providers.map(p=>{
    const loaded=unique(p.models || []); const available=availableModels(p); const published=visibleModels(p);
    const availableCount=p.availability_checked_at?available.length:loaded.length;
    const auth=authStatus(p); const authText=auth==='needs_login' ? '<div class="err">登录失效，需要重新登录。'+esc(authError(p))+'</div>' : '<div class="muted">已连接</div>';
    return '<div class="card">'+providerTitleHTML(p,'green-dot')+authText+'<div class="muted">已加载 '+loaded.length+' 个模型，可用 '+availableCount+' 个，已发布 '+published.length+' 个</div></div>';
  });
  root.innerHTML=items.join('') || '<div class="muted">当前没有已连接的 provider。</div>';
}
function csvIndexSet(value){
  const out=new Set();
  String(value || '').split(',').map(x=>x.trim()).filter(Boolean).forEach(x=>{ const n=parseInt(x,10); if(!Number.isNaN(n)) out.add(n); });
  return out;
}
function apiKeyStatusClass(p,index,total){
  if(!p || total<=1) return 'empty';
  const failed=csvIndexSet(providerDataValue(p,'failed_key_indexes'));
  if(failed.has(index)) return 'failed';
  const activeRaw=providerDataValue(p,'active_key_index');
  const active=activeRaw==='' ? 0 : parseInt(activeRaw,10);
  return active===index ? 'active' : 'empty';
}
function apiKeyRowHTML(id, value, statusClass){
  statusClass=statusClass || 'empty';
  return '<div class="inline-field grow key-row"><span class="key-status-dot '+esc(statusClass)+'"></span><input data-api-key-provider="'+esc(id)+'" value="'+esc(value || '')+'" placeholder="sk-..."><button class="small secondary" onclick="probeAPIKey(this,\''+id+'\')" type="button">测试</button><button class="small secondary" data-key-remove="1" onclick="removeAPIKeyInput(this,\''+id+'\')" type="button">删除</button></div>';
}
function refreshAPIKeyRemoveButtons(id){
  const root=document.getElementById('keyList_'+id); if(!root) return;
  const rows=[...root.querySelectorAll('.key-row')];
  rows.forEach(row=>{ const btn=row.querySelector('button[data-key-remove]'); if(btn) btn.disabled=rows.length<=1; });
}
function addAPIKeyInput(id){
  const root=document.getElementById('keyList_'+id); if(!root) return;
  root.insertAdjacentHTML('beforeend', apiKeyRowHTML(id,'','empty'));
  refreshAPIKeyRemoveButtons(id);
}
function removeAPIKeyInput(button,id){
  const row=button && button.closest ? button.closest('.key-row') : null;
  if(row) row.remove();
  const root=document.getElementById('keyList_'+id);
  if(root && !root.querySelector('.key-row')) root.insertAdjacentHTML('beforeend', apiKeyRowHTML(id,'','empty'));
  refreshAPIKeyRemoveButtons(id);
}
function apiKeyValues(id){
  return unique([...document.querySelectorAll('input[data-api-key-provider="'+id+'"]')].map(node=>node.value.trim()).filter(Boolean));
}
function apiKeyEditor(id,p){
  const keys=providerAPIKeys(p); if(!keys.length) keys.push('');
  const probeModel=(chatProbeModels(p)[0] || '');
  return '<div class="field"><label>API Keys</label><div id="keyList_'+id+'" class="key-list">'+keys.map((key,index)=>apiKeyRowHTML(id,key,apiKeyStatusClass(p,index,keys.length))).join('')+'</div><div class="bar"><button class="small secondary" onclick="addAPIKeyInput(\''+id+'\')" type="button">新增 API Key</button><span class="muted">按顺序尝试；额度不足只会跳过当前模型的这个 key。</span></div><div class="field"><label>Key 测试模型</label><input id="keyProbeModel_'+id+'" value="'+esc(probeModel)+'" placeholder="填写要用这个 key 测试的文本模型"></div></div>';
}
function hydrateCustomKeyEditors(cfg){
  (cfg.providers || []).forEach(p=>{
    const input=document.getElementById('key_'+p.id);
    if(!input) return;
    const field=input.closest('.field');
    if(!field) return;
    field.outerHTML=apiKeyEditor(p.id,p);
    const base=document.getElementById('base_'+p.id);
    const baseField=base && base.closest ? base.closest('.field') : null;
    if(baseField && !document.getElementById('mediaBase_'+p.id)){
      baseField.insertAdjacentHTML('afterend', mediaBaseEditor(p.id,p));
    }
    refreshAPIKeyRemoveButtons(p.id);
  });
}
function mediaBaseValue(p, kind){ return (p && (p[kind+'_endpoint'] || p[kind+'_base_url'])) || ''; }
function mediaBaseEditor(id,p){
  const kinds=[['image','图片'],['image_edit','图片编辑'],['video','视频'],['audio','音频'],['tts','TTS']];
  return '<div id="mediaBase_'+id+'" class="field"><label>媒体完整 Endpoint</label><div class="bar">'+kinds.map(([kind,label])=>'<button class="small secondary" type="button" onclick="showMediaBaseInput(\''+id+'\',\''+kind+'\')">新增'+label+' Endpoint</button>').join('')+'</div><div class="key-list">'+kinds.map(([kind,label])=>mediaBaseInputHTML(id,kind,label,mediaBaseValue(p,kind))).join('')+'</div><div class="muted">填写上游文档里的完整请求地址，例如 https://apihub.agnes-ai.com/v1/videos。</div>'+mediaTemplateEditor(id,p)+'</div>';
}
function mediaBaseInputHTML(id,kind,label,value){
  const example=kind==='image'?'images/generations':kind==='image_edit'?'images/edits':kind==='video'?'videos':kind==='tts'?'tts':'audio';
  return '<div id="'+kind+'BaseRow_'+id+'" class="field" style="'+(value?'':'display:none')+'"><label>'+label+' Endpoint</label><div class="inline-field grow"><input id="'+kind+'Base_'+id+'" value="'+esc(value || '')+'" placeholder="https://example.com/v1/'+example+'"><button class="small secondary" type="button" onclick="clearMediaBaseInput(\''+id+'\',\''+kind+'\')">删除</button></div></div>';
}
function showMediaBaseInput(id,kind){
  const row=document.getElementById(kind+'BaseRow_'+id);
  if(row) row.style.display='';
  const input=document.getElementById(kind+'Base_'+id);
  if(input) input.focus();
}
function clearMediaBaseInput(id,kind){
  const input=document.getElementById(kind+'Base_'+id);
  if(input) input.value='';
  const row=document.getElementById(kind+'BaseRow_'+id);
  if(row) row.style.display='none';
}
function mediaBaseInputValue(id,kind){
  const input=document.getElementById(kind+'Base_'+id);
  return input ? input.value.trim() : '';
}
function mediaTemplateEditor(id,p){
  return '<details><summary>自定义 curl 模板</summary>'
    +templateTextArea(id,'curlTemplateImage','图片 curl 模板',providerDataValue(p,'curlTemplateImage') || defaultCurlTemplate('image'))
    +templateTextArea(id,'curlTemplateVideo','视频 curl 模板',providerDataValue(p,'curlTemplateVideo') || defaultCurlTemplate('video'))
    +templateTextArea(id,'curlTemplateAudio','音频 curl 模板',providerDataValue(p,'curlTemplateAudio') || defaultCurlTemplate('audio'))
    +templateTextArea(id,'curlTemplateTTS','TTS curl 模板',providerDataValue(p,'curlTemplateTTS') || defaultCurlTemplate('tts'))
    +'</details>';
}
function templateTextArea(id,key,label,value){
  return '<div class="field"><label>'+label+'</label><textarea id="'+key+'_'+id+'" spellcheck="false">'+esc(value || '')+'</textarea></div>';
}
function defaultCurlTemplate(kind){
  if(kind==='image') return '# 文生图\ncurl -X POST "{{endpoint}}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer {{key}}" \\\n  -d \'{\n  "model": "{{model}}",\n  "prompt": "{{image_prompt}}",\n  "n": 1\n}\'\n\n# 透明背景 PNG 生图\ncurl -X POST "{{endpoint}}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer {{key}}" \\\n  -d \'{\n  "model": "{{transparent_model}}",\n  "prompt": "{{transparent_prompt}}",\n  "n": 1,\n  "background": "transparent",\n  "output_format": "png"\n}\'\n\n# 图片编辑：使用图片 URL\ncurl -X POST "{{image_edit_endpoint}}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer {{key}}" \\\n  -d \'{\n  "model": "{{model}}",\n  "prompt": "{{edit_prompt}}",\n  "images": [\n    {"image_url": "https://example.com/input.png"}\n  ]\n}\'';
  if(kind==='video') return '# 创建视频任务\ncurl -X POST "{{endpoint}}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer {{key}}" \\\n  -d \'{\n  "model": "{{model}}",\n  "prompt": "{{video_prompt}}",\n  "height": 768,\n  "width": 1152,\n  "num_frames": 121,\n  "frame_rate": 24\n}\'\n\n# 查询视频结果\ncurl -X GET "{{video_query_endpoint}}" \\\n  -H "Authorization: Bearer {{key}}"';
  if(kind==='audio') return 'curl -X POST "{{endpoint}}" \\\n  -H "Authorization: Bearer {{key}}" \\\n  -F "file=@{{audio_file}}" \\\n  -F "model={{model}}" \\\n  -F "response_format=json" \\\n  -F "language=zh"';
  if(kind==='tts') return 'curl -G "{{endpoint}}" \\\n  --data-urlencode "text={{tts_text}}" \\\n  --data-urlencode "voice={{model}}" \\\n  --output output.mp3';
  return '';
}
function customProtocolField(p,id){
  if(!isCustomProvider(p)) return '';
  const mode=providerDataValue(p,'anthropicRequestMode')==='claude-code' ? 'claude-code' : 'standard';
  const protocol=p.type==='anthropic' ? 'anthropic' : (isResponsesProvider(p) ? 'responses' : 'openai');
  return '<div class="field"><label>上游协议</label><select id="protocol_'+esc(id)+'" onchange="syncAnthropicRequestMode(\''+esc(id)+'\')"><option value="openai" '+(protocol==='openai'?'selected':'')+'>OpenAI Compatible</option><option value="responses" '+(protocol==='responses'?'selected':'')+'>OpenAI Responses</option><option value="anthropic" '+(protocol==='anthropic'?'selected':'')+'>Anthropic Compatible</option></select></div>'
    +'<div class="field"><label>Anthropic 请求模式</label><select id="anthropicMode_'+esc(id)+'" '+(p.type==='anthropic'?'':'disabled')+'><option value="standard" '+(mode==='standard'?'selected':'')+'>标准 Anthropic</option><option value="claude-code" '+(mode==='claude-code'?'selected':'')+'>Claude Code 兼容</option></select><span class="muted">兼容模式保真转发 Claude Code；模型探测只校验上游模型列表。</span></div>';
}
function syncAnthropicRequestMode(id){
  const protocol=document.getElementById('protocol_'+id); const mode=document.getElementById('anthropicMode_'+id);
  if(mode) mode.disabled=!(protocol && protocol.value==='anthropic');
}
function renderAPIProviders(){
  const root=document.getElementById('apiProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const providerIDs=apiProviderIDs(cfg);
  if(!expandedAPIProviderID || !providerIDs.includes(expandedAPIProviderID)) expandedAPIProviderID=providerIDs[0] || '';
  const navItems=[]; const detailItems=[];
  providerIDs.forEach(id=>{
    const p=cfg.providers.find(x=>x.id===id); if(!p) return '';
    const isCustom=isCustomProvider(p);
    const listControls=apiModelsLoaded(p) ? '<div class="bar"><button class="small secondary" onclick="selectProviderModels(\''+id+'\',true)">全选</button><button class="small secondary" onclick="selectProviderModels(\''+id+'\',false)">取消所有选择</button></div>' : '';
    const rows=apiModelsLoaded(p) ? '<div class="model-list">'+modelRows(p)+'</div>' : '';
    const count=apiModelsLoaded(p) ? '<div class="muted">已拉取 '+p.models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div>' : '';
    const deleteButton='<div class="bar" style="justify-content:flex-end"><button class="small secondary" onclick="deleteAPIProvider(\''+id+'\')">删除卡片</button></div>';
    const probeCount=chatProbeModels(p).length;
    const selected=expandedAPIProviderID===id;
    const stateDot=p.enabled?'green-dot':'gray-dot';
    const stateText=p.enabled?'已启用':'未启用';
    navItems.push('<button class="api-provider-nav'+(selected?' active':'')+'" type="button" data-api-provider-nav="'+esc(id)+'" onclick="toggleAPIProviderCard(\''+id+'\')" aria-selected="'+(selected?'true':'false')+'"><span class="api-provider-nav-main"><span class="'+stateDot+'"></span><strong>'+esc(p.name)+'</strong></span><span class="api-provider-nav-state">'+stateText+'</span></button>');
    const details='<div class="api-head"><strong>'+esc(p.name)+'</strong><span class="api-meta">'+esc(p.id)+'</span></div>'+(isCustom?'<div class="field"><label>名称</label><input id="name_'+id+'" value="'+esc(p.name || '')+'" placeholder="自定义源"></div>':'')+customProtocolField(p,id)+'<label class="toggle"><input type="checkbox" id="enabled_'+id+'" onchange="saveAPIProvider(\''+id+'\')" '+(p.enabled?'checked':'')+'> 启用</label><div class="field"><label>Base URL</label><input id="base_'+id+'" value="'+esc(p.base_url || '')+'" placeholder="https://example.com/v1"></div><div class="field"><label>API Key</label><input id="key_'+id+'" value="'+esc(p.api_key || '')+'" placeholder="sk-..."></div><div class="bar"><button onclick="saveAPIProvider(\''+id+'\')">保存</button><button class="secondary" onclick="fetchAPIProviderModels(\''+id+'\')">保存并拉取模型</button><span id="apiStatus_'+id+'" class="muted"></span></div><div class="field"><label>手动添加模型</label><div class="inline-field grow"><input id="manualModel_'+id+'" placeholder="model-id"><button class="secondary" onclick="addAPIModel(\''+id+'\')" type="button">添加</button></div></div><div class="bar"><button id="probeStart_'+id+'" class="small" onclick="probeProvider(\''+id+'\',\'apiStatus_'+id+'\')" '+(!providerConnected(p) || !apiModelsLoaded(p) || !probeCount || providerProbeControllers.has(id)?'disabled':'')+'>探测可用</button><button id="probeStop_'+id+'" class="small secondary" onclick="stopProviderProbe(\''+id+'\',\'apiStatus_'+id+'\')" '+(providerProbeControllers.has(id)?'':'disabled')+'>停止探测</button><button class="small secondary" onclick="saveModelSelection(\''+id+'\',\'apiStatus_'+id+'\')" '+(!apiModelsLoaded(p)?'disabled':'')+'>保存发布</button></div><div id="probeProgress_'+id+'" class="progress-wrap"><progress value="0" max="'+probeCount+'"></progress><span class="muted">0/'+probeCount+'</span></div>'+count+listControls+rows+deleteButton;
    detailItems.push('<div class="api-card api-provider-detail'+(selected?' active':'')+'" data-api-provider-detail="'+esc(id)+'">'+details+'</div>');
  });
  root.innerHTML='<div class="api-provider-list">'+navItems.join('')+'</div><div class="api-provider-detail-pane">'+detailItems.join('')+'</div>';
  hydrateCustomKeyEditors(cfg);
}
function toggleAPIProviderCard(id){
  expandedAPIProviderID=id;
  document.querySelectorAll('[data-api-provider-nav]').forEach(button=>{
    const selected=button.getAttribute('data-api-provider-nav')===id;
    button.classList.toggle('active',selected);
    button.setAttribute('aria-selected',selected?'true':'false');
  });
  document.querySelectorAll('[data-api-provider-detail]').forEach(detail=>{
    detail.classList.toggle('active',detail.getAttribute('data-api-provider-detail')===id);
  });
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
    const probeCount=chatProbeModels(p).length;
    return '<div class="card">'+providerTitleHTML(p,connected?'green-dot':'gray-dot')+oauthControls(p.id)+authText+'<div class="muted">已加载 '+models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div><div class="bar">'+fetchOAuthModelsButton(p)+'<button id="probeStart_'+p.id+'" class="small" onclick="probeProvider(\''+p.id+'\',\'publishStatus_'+p.id+'\')" '+(!connected || !probeCount || providerProbeControllers.has(p.id)?'disabled':'')+'>探测可用</button><button id="probeStop_'+p.id+'" class="small secondary" onclick="stopProviderProbe(\''+p.id+'\',\'publishStatus_'+p.id+'\')" '+(providerProbeControllers.has(p.id)?'':'disabled')+'>停止探测</button><button class="small secondary" onclick="saveModelSelection(\''+p.id+'\',\'publishStatus_'+p.id+'\')" '+(!models.length?'disabled':'')+'>保存发布列表</button><span id="publishStatus_'+p.id+'" class="muted"></span></div><div id="probeProgress_'+p.id+'" class="progress-wrap"><progress value="0" max="'+probeCount+'"></progress><span class="muted">0/'+probeCount+'</span></div>'+listControls+rows+'<div class="bar" style="justify-content:flex-end"><button class="small secondary" onclick="disableProvider(\''+p.id+'\')">停用</button></div></div>';
  });
  root.innerHTML=items.join('') || '<div class="muted">还没有可发布的模型。</div>';
}
async function reloadConfig(){ const res=await fetch('/api/config'); const cfg=await res.json(); setConfig(cfg); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }
function buildAPIProvider(id){
  const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
  const prev=cfg.providers.find(x=>x.id===id); if(!prev) throw new Error('provider not found: '+id);
  const custom=isCustomProvider(prev);
  const keys=apiKeyValues(id);
  const protocol=custom ? document.getElementById('protocol_'+id) : null;
  const protocolValue=protocol ? protocol.value : '';
  const next={ ...prev, name:custom?(document.getElementById('name_'+id).value.trim() || prev.name || 'Custom Compatible'):prev.name, enabled:!!document.getElementById('enabled_'+id).checked, base_url:document.getElementById('base_'+id).value.trim(), api_key:keys[0] || '' };
  if(custom) next.type=protocolValue==='anthropic' ? 'anthropic' : 'openai';
  if(custom && next.name.includes('/')) throw new Error('自定义渠道名称不能包含 /');
  next.image_endpoint=mediaBaseInputValue(id,'image');
  next.image_edit_endpoint=mediaBaseInputValue(id,'image_edit');
  next.video_endpoint=mediaBaseInputValue(id,'video');
  next.audio_endpoint=mediaBaseInputValue(id,'audio');
  next.tts_endpoint=mediaBaseInputValue(id,'tts');
  delete next.image_base_url;
  delete next.video_base_url;
  delete next.audio_base_url;
  if(keys.length>1){
    next.api_keys=keys;
  }else{
    delete next.api_keys;
  }
  const psd={...(next.provider_specific_data || {})};
  delete psd.active_key_index;
  delete psd.failed_key_indexes;
  Object.keys(psd).forEach(key=>{ if(key.startsWith('failed_key_indexes_model_')) delete psd[key]; });
  const anthropicMode=document.getElementById('anthropicMode_'+id);
  if(custom && next.type==='anthropic' && anthropicMode && anthropicMode.value==='claude-code') psd.anthropicRequestMode='claude-code'; else delete psd.anthropicRequestMode;
  if(custom && next.type==='openai' && protocolValue==='responses') psd.openaiRequestMode='responses'; else delete psd.openaiRequestMode;
  [['curlTemplateImage','image'],['curlTemplateVideo','video'],['curlTemplateAudio','audio'],['curlTemplateTTS','tts']].forEach(([key,kind])=>{
    const value=templateFieldValue(id,key);
    if(value && value!==defaultCurlTemplate(kind)) psd[key]=value; else delete psd[key];
  });
  if(custom){
    psd.customProvider='true';
  }
  next.provider_specific_data=psd;
  return next;
}
function remapProviderRouteRefs(cfg,prev,next){
  const oldRoute=providerRouteID(prev); const newRoute=providerRouteID(next);
  if(!oldRoute || !newRoute || oldRoute===newRoute) return;
  const remap=value=>String(value || '').startsWith(oldRoute+'/') ? newRoute+String(value).slice(oldRoute.length) : value;
  if(cfg.auto_model && Array.isArray(cfg.auto_model.models)) cfg.auto_model.models=unique(cfg.auto_model.models.map(remap));
  if(cfg.auto_model && Array.isArray(cfg.auto_model.vision_models)) cfg.auto_model.vision_models=unique(cfg.auto_model.vision_models.map(remap));
  (cfg.model_groups || []).forEach(group=>{ group.models=unique((group.models || []).map(remap)); });
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
  cfg.auto_model={enabled:!!document.getElementById('autoModelEnabled').checked,models:autoModels(cfg),vision_models:autoVisionModels(cfg)};
  return cfg;
}
async function saveGateway(){ try{ const cfg=parseConfig(); if(!cfg) throw new Error('config is invalid'); applyGatewaySettings(cfg); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('status','ok','已保存'); }catch(e){ setText('status','err',e.message); } }
function toggleAdminPasswordFields(){
  ['adminCurrentPassword','adminNewPassword','adminConfirmPassword'].forEach(id=>{
    const el=document.getElementById(id); if(el) el.type=el.type==='password'?'text':'password';
  });
}
async function changeAdminPassword(){
  try{
    const current=document.getElementById('adminCurrentPassword').value;
    const next=document.getElementById('adminNewPassword').value;
    const confirmPassword=document.getElementById('adminConfirmPassword').value;
    if(next!==confirmPassword) throw new Error('两次输入的新密码不一致');
    if(next.trim().length<8) throw new Error('新密码至少需要 8 个字符');
    setText('adminPasswordStatus','muted','正在保存...');
    const res=await fetch('/api/admin/password',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({current_password:current,new_password:next})});
    const data=await res.json();
    if(!res.ok) throw new Error(data.error || res.statusText);
    setText('adminPasswordStatus','ok','密码已修改，正在返回登录页...');
    window.setTimeout(()=>{ window.location.href='/admin'; },500);
  }catch(e){ setText('adminPasswordStatus','err',e.message); }
}
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
async function saveAPIProvider(id){ try{ const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const prev=cfg.providers.find(p=>p.id===id); const next=buildAPIProvider(id); remapProviderRouteRefs(cfg,prev,next); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('apiStatus_'+id,'ok','已保存'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function probeAPIKey(button,id){
  let statusID='apiStatus_'+id;
  try{
    const row=button && button.closest ? button.closest('.key-row') : null;
    const rows=[...document.querySelectorAll('#keyList_'+id+' .key-row')];
    const keyIndex=rows.indexOf(row);
    if(keyIndex<0) throw new Error('找不到要测试的 key 行');
    const modelInput=document.getElementById('keyProbeModel_'+id);
    const model=(modelInput && modelInput.value || '').trim();
    if(!model) throw new Error('请先填写 Key 测试模型');
    setText(statusID,'muted','正在保存当前卡片...');
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    applyGatewaySettings(cfg);
    const next=buildAPIProvider(id);
    const keys=providerAPIKeys(next);
    if(!keys[keyIndex]) throw new Error('这一行 key 为空');
    cfg.providers=cfg.providers.map(p=>p.id===id?next:p);
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    setText(statusID,'muted','正在测试第 '+(keyIndex+1)+' 个 key：'+model);
    const res=await fetch('/api/provider/probe-key',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, model, key_index:keyIndex})});
    const data=await res.json();
    if(!res.ok) throw new Error(data.error || res.statusText);
    await reloadConfig();
    if(data.ok){
      setText(statusID,'ok','第 '+(keyIndex+1)+' 个 key 可用，延迟 '+data.latency_ms+'ms');
    }else{
      setText(statusID,'err','第 '+(keyIndex+1)+' 个 key 不可用：'+(data.error || '探测失败'));
    }
  }catch(e){
    setText(statusID,'err',e.message);
  }
}
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
async function fetchAPIProviderModels(id){ try{ setText('apiStatus_'+id,'muted','正在保存...'); const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyGatewaySettings(cfg); const prev=cfg.providers.find(p=>p.id===id); const next=buildAPIProvider(id); remapProviderRouteRefs(cfg,prev,next); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); ensureBlankCustomProvider(cfg); await saveConfigObject(cfg); setText('apiStatus_'+id,'muted','正在拉取模型...'); const res=await fetch('/api/provider/models',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('apiStatus_'+id,'ok','已拉取 '+(data.count || 0)+' 个模型'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function fetchOAuthProviderModels(id){ try{ const statusID='publishStatus_'+id; setText(statusID,'muted','正在拉取模型...'); const res=await fetch('/api/provider/models',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText(statusID,'ok','已拉取 '+(data.count || 0)+' 个模型'); }catch(e){ setText('publishStatus_'+id,'err',e.message); } }
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
async function saveModelKind(select){
  const id=select.getAttribute('data-provider');
  const model=select.getAttribute('data-model');
  const kind=select.value;
  const statusID=document.getElementById('publishStatus_'+id)?'publishStatus_'+id:'apiStatus_'+id;
  try{
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    cfg.providers=cfg.providers.map(p=>{
      if(p.id!==id) return p;
      p={...p};
      const kinds={...(p.model_kinds || {})};
      if(kind==='auto') delete kinds[model]; else kinds[model]=kind;
      if(Object.keys(kinds).length) p.model_kinds=kinds; else delete p.model_kinds;
      return p;
    });
    await saveConfigObject(cfg);
    await reloadConfig();
    const label={auto:'自动',text:'文本',image:'图片',video:'视频',audio:'音频',tts:'TTS'}[kind] || kind;
    setText(statusID,'ok',model+' 已设为 '+label+' 类型');
  }catch(e){
    setText(statusID,'err','保存模型类型失败：'+e.message);
  }
}
async function toggleModelLock(button){
  try{
    const id=button.getAttribute('data-provider');
    const model=button.getAttribute('data-model');
    const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
    applyGatewaySettings(cfg);
    cfg.providers=cfg.providers.map(p=>{
      if(p.id!==id) return p;
      p={...p};
      const locked=new Set(lockedModels(p));
      if(locked.has(model)) locked.delete(model); else locked.add(model);
      p.locked_models=[...locked];
      return p;
    });
    ensureBlankCustomProvider(cfg);
    await saveConfigObject(cfg);
    await reloadConfig();
    setText('status','ok','模型锁定状态已保存');
  }catch(e){ setText('status','err','锁定失败：'+e.message); }
}
function stopProbe(){
  probeStopRequested=true;
  setText('probeAllStatus','muted','正在停止，当前请求完成后不再继续。');
}
async function probeOneModel(id, model, autoPublish, dropUnavailable, signal){ if(dropUnavailable===undefined) dropUnavailable=!autoPublish; const options={method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, model, auto_publish:!!autoPublish, drop_unavailable_on_failure:!!dropUnavailable})}; if(signal) options.signal=signal; const res=await fetch('/api/provider/probe-model',options); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); return data; }
function setProviderProbeButtons(id,running){ const start=document.getElementById('probeStart_'+id); const stop=document.getElementById('probeStop_'+id); if(start) start.disabled=!!running; if(stop) stop.disabled=!running; }
function stopProviderProbe(id,statusID){ const controller=providerProbeControllers.get(id); if(!controller) return; controller.abort(); const stop=document.getElementById('probeStop_'+id); if(stop) stop.disabled=true; setText(statusID || 'publishStatus_'+id,'muted','正在停止当前卡片探测...'); }
async function probeProvider(id, statusID){
  if(providerProbeControllers.has(id)) return;
  const cfg=parseConfig(); const p=cfg.providers.find(x=>x.id===id); if(!p) return;
  const models=chatProbeModels(p); let ok=0; let failed=0; statusID=statusID || 'publishStatus_'+id; setProgress('probeProgress_'+id,0,models.length);
  if(!models.length){ setText(statusID,'muted','没有可探测的文本模型，多媒体模型已跳过。'); return; }
  const controller=new AbortController(); providerProbeControllers.set(id,controller); setProviderProbeButtons(id,true);
  setText(statusID,'muted','正在探测...');
  let done=0; let stopped=false;
  for(let i=0;i<models.length;i++){
    setText(statusID,'muted','正在探测 '+(i+1)+'/'+models.length+'：'+models[i]);
    try{
      const data=await probeOneModel(id,models[i],false,undefined,controller.signal);
      if(data.ok) ok++;
    }catch(e){
      if(e && e.name==='AbortError'){ stopped=true; break; }
      failed++;
      setText(statusID,'err','探测失败：'+e.message);
    }
    done=i+1; setProgress('probeProgress_'+id,done,models.length);
  }
  providerProbeControllers.delete(id); await reloadConfig(); setText(statusID,stopped?'muted':(failed?'err':'ok'),stopped?'已停止，已探测 '+done+'/'+models.length+'，可用 '+ok+' 个模型':'可用 '+ok+' 个模型'+(failed?'，失败 '+failed+' 个':''));
}
async function probeAllProviders(){
  probeStopRequested=false;
  const cfg=parseConfig(); const providers=cfg.providers.filter(p=>providerConnected(p) && visibleModels(p).length);
  const jobs=[]; providers.forEach(p=>{
    const locked=new Set(lockedModels(p));
    visibleModels(p).filter(model=>!locked.has(model) && !providerIsMediaModel(p,model)).forEach(model=>jobs.push({id:p.id,model}));
  });
  let ok=0; let failed=0; setProgress('probeAllProgress',0,jobs.length); setText('probeAllStatus','muted','正在探测...');
  if(!jobs.length){ setText('probeAllStatus','muted','没有可探测的文本模型，或模型都已上锁。'); return; }
  let done=0;
  for(let i=0;i<jobs.length;i++){
    if(probeStopRequested) break;
    setText('probeAllStatus','muted','正在探测 '+(i+1)+'/'+jobs.length+'：'+jobs[i].id+'/'+jobs[i].model);
    try{
      const data=await probeOneModel(jobs[i].id,jobs[i].model,true);
      if(data.ok) ok++;
    }catch(e){
      failed++;
      setText('probeAllStatus','err','探测失败：'+e.message);
    }
    done=i+1; setProgress('probeAllProgress',done,jobs.length);
  }
  await reloadConfig(); setText('probeAllStatus',probeStopRequested?'muted':(failed?'err':'ok'),probeStopRequested?'已停止，已探测 '+done+'/'+jobs.length+'，可用 '+ok+' 个模型':'探测完成，可用 '+ok+' 个模型'+(failed?'，失败 '+failed+' 个':'，已自动发布'));
}
async function saveModelSelection(id,statusID){ try{ statusID=statusID || 'publishStatus_'+id; const nodes=[...document.querySelectorAll('input[data-provider="'+id+'"]:checked')]; const enabled_models=nodes.map(node=>node.getAttribute('data-model')); const res=await fetch('/api/provider/selection',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, enabled_models})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText(statusID,'ok','已保存发布列表'); }catch(e){ setText(statusID || 'status','err',e.message); } }
async function deleteProviderModel(button){
  const id=button.getAttribute('data-provider');
  const model=button.getAttribute('data-model');
  const statusID=document.getElementById('publishStatus_'+id)?'publishStatus_'+id:'apiStatus_'+id;
  try{
    button.disabled=true;
    setText(statusID,'muted','正在删除 '+model+'...');
    const res=await fetch('/api/provider/model/delete',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id,model})});
    const data=await res.json();
    if(!res.ok) throw new Error(data.error || res.statusText);
    await reloadConfig();
    setText(statusID,'ok','已删除模型 '+model);
  }catch(e){
    button.disabled=false;
    setText(statusID,'err',e.message);
  }
}
async function save(){ try{ const body=JSON.parse(document.getElementById('cfg').value); applyGatewaySettings(body); ensureBlankCustomProvider(body); const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); setText('status','ok','已保存'); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }catch(e){ setText('status','err',e.message); } }
async function startQoder(){ try{ const data=await (await fetch('/api/oauth/qoder/device-code')).json(); localStorage.setItem('qoder_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('qoderStatus','ok','已打开 Qoder 登录页，完成登录后点击轮询令牌。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function pollQoder(){ try{ const flow=JSON.parse(localStorage.getItem('qoder_flow')||'{}'); if(!flow.device_code || !flow.codeVerifier) throw new Error('请先开始 Qoder 登录'); const res=await fetch('/api/oauth/qoder/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({ deviceCode: flow.device_code, codeVerifier: flow.codeVerifier, extraData: {_qoderMachineId: flow._qoderMachineId, _qoderNonce: flow._qoderNonce, _qoderVerifier: flow.codeVerifier} })}); const data=await res.json(); if(data.pending){ setText('qoderStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('qoder_flow'); await reloadConfig(); setText('qoderStatus','ok','Qoder 已连接。'); }catch(e){ setText('qoderStatus','err',e.message); } }
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
