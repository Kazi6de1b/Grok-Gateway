const $ = (s, root=document) => root.querySelector(s);
const $$ = (s, root=document) => [...root.querySelectorAll(s)];
let state = null;
let oauthTimer = null;

const api = async (path, options={}) => {
  const response = await fetch(path, {headers:{"Content-Type":"application/json"}, ...options});
  let data = {};
  try { data = await response.json(); } catch {}
  if (!response.ok && response.status !== 207) throw new Error(data?.error?.message || `请求失败 (${response.status})`);
  return data;
};

const esc = value => String(value ?? "").replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const pct = value => Math.max(0, Math.min(100, Number(value || 0)));
const initial = value => [...String(value || 'G')][0].toUpperCase();
const dateText = value => value && !String(value).startsWith('0001-') ? new Date(value).toLocaleString('zh-CN',{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}) : '—';
const resetValue = usage => usage?.usage_period_end || usage?.billing_period_end || '';
const remainingText = value => {
  if (!value) return '未知';
  const ms = new Date(value) - Date.now();
  if (ms <= 0) return '即将恢复';
  const h = Math.floor(ms / 3600000), d = Math.floor(h / 24);
  return d > 0 ? `${d}天 ${h%24}小时` : `${h}小时 ${Math.floor(ms%3600000/60000)}分钟`;
};
const openExternal = url => {
  if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url);
  else window.open(url, '_blank', 'noopener');
};

function toast(message, error=false){
  const el=document.createElement('div'); el.className=`toast${error?' error':''}`; el.textContent=message;
  $('#toasts').append(el); setTimeout(()=>el.remove(),4200);
}

async function refreshState(silent=false){
  try { state=await api('/api/state'); render(); }
  catch(error){ if(!silent) toast(error.message,true); }
}

function render(){
  if(!state) return;
  const accounts=state.accounts||[], available=accounts.filter(a=>a.available), usages=accounts.map(a=>a.usage?.usage_percent).filter(v=>Number.isFinite(v));
  $('#side-version').textContent=`v${state.version} · 127.0.0.1`;
  $('#base-url').textContent=state.grok_base_url;
  $('#route-listen').textContent=state.settings.listen;
  $('#route-proxy').textContent=state.settings.outbound_proxy.replace(/^https?:\/\//,'');
  $('#stat-total').textContent=accounts.length;
  $('#stat-total-note').textContent=accounts.length?`${accounts.filter(a=>a.enabled).length} 个已启用`:'等待添加账号';
  $('#stat-available').textContent=available.length;
  $('#stat-usage').textContent=usages.length?`${Math.round(usages.reduce((a,b)=>a+b,0)/usages.length)}%`:'—';
  const resets=accounts.map(a=>resetValue(a.usage)).filter(Boolean).sort();
  $('#stat-reset').textContent=resets.length?remainingText(resets[0]):'—';
  $('#stat-reset-note').textContent=resets.length?dateText(resets[0]):'尚未同步';
  renderMini(accounts);
  renderAccounts(accounts);
  fillSettings();
}

function statusOf(a){
  if(!a.enabled) return ['已停用',''];
  if(a.cooldown_until && new Date(a.cooldown_until)>new Date()) return ['冷却中','amber'];
  if(a.available) return [a.preferred?'首选账号':'可用','lime'];
  return ['不可用','amber'];
}

function renderMini(accounts){
  const root=$('#overview-accounts');
  if(!accounts.length){root.innerHTML='<div class="empty">还没有托管账号，前往账号池添加 OAuth 账号。</div>';return;}
  root.innerHTML=accounts.slice(0,4).map(a=>{const [label]=statusOf(a),usage=pct(a.usage?.usage_percent);return `
    <div class="mini-account"><div class="avatar">${esc(initial(a.name))}</div><div><strong>${esc(a.name)}</strong><span>${esc(a.usage?.plan_name||a.email||'Grok Build')}</span></div>
    <div class="mini-progress"><i style="width:${usage}%"></i></div><span class="state-label">${esc(label)}</span></div>`}).join('');
}

function renderAccounts(accounts){
  const root=$('#account-grid');
  if(!accounts.length){root.innerHTML='<div class="empty">账号池为空。点击“添加账号”，通过 Device OAuth 托管第一个 Grok Build 账号。</div>';return;}
  root.innerHTML=accounts.map(a=>{const [label,tone]=statusOf(a),usage=pct(a.usage?.usage_percent),reset=resetValue(a.usage);return `
    <article class="account-card card ${a.preferred?'preferred':''}" data-id="${esc(a.identity)}">
      <div class="account-top"><div class="avatar">${esc(initial(a.name))}</div><div class="account-meta"><strong>${esc(a.name)}</strong><span>${esc(a.email||a.identity)}</span></div><span class="badge ${tone}">${esc(label)}</span></div>
      <div class="usage-head"><span>${esc(a.usage?.plan_name||'用量尚未同步')}</span><strong>${a.usage?Math.round(usage)+'%':'—'}</strong></div>
      <div class="progress"><i class="${usage>=85?'high':''}" style="width:${usage}%"></i></div>
      <div class="usage-foot"><span>${a.usage?`已用 ${formatUsage(a.usage)}`:'点击刷新读取 /usage'}</span><span>${reset?`重置 ${remainingText(reset)}`:'重置时间未知'}</span></div>
      <div class="account-actions">
        <button class="btn ${a.preferred?'btn-ghost':'btn-primary'}" data-action="prefer">${a.preferred?'当前首选':'切换到此账号'}</button>
        <button class="btn btn-ghost" data-action="usage">刷新用量</button>
        ${a.cooldown_until&&new Date(a.cooldown_until)>new Date()?'<button class="btn btn-ghost" data-action="clear">解除冷却</button>':''}
        <button class="btn btn-ghost" data-action="toggle">${a.enabled?'停用':'启用'}</button>
        <button class="btn btn-danger" data-action="delete">删除</button>
      </div>
    </article>`}).join('');
}

function formatUsage(u){
  if(u.monthly_limit>0) return `${u.used.toFixed(1)} / ${u.monthly_limit.toFixed(1)}`;
  if(u.on_demand_cap>0) return `${u.on_demand_used.toFixed(1)} / ${u.on_demand_cap.toFixed(1)}`;
  return `${Math.round(u.usage_percent||0)}%`;
}

function fillSettings(){
  const form=$('#settings-form'); if(!form||form.dataset.dirty==='true') return;
  Object.entries(state.settings).forEach(([k,v])=>{if(form.elements[k])form.elements[k].value=v||''});
}

function switchView(name){
  $$('.view').forEach(v=>v.classList.toggle('active',v.id===`view-${name}`));
  $$('.nav-item').forEach(v=>v.classList.toggle('active',v.dataset.view===name));
  const labels={overview:['CONTROL CENTER','运行概览'],accounts:['ACCOUNT MANAGEMENT','账号池'],settings:['GATEWAY CONFIGURATION','配置']};
  $('#page-eyebrow').textContent=labels[name][0]; $('#page-title').textContent=labels[name][1];
}

$$('.nav-item').forEach(btn=>btn.addEventListener('click',()=>switchView(btn.dataset.view)));
$$('[data-go]').forEach(btn=>btn.addEventListener('click',()=>switchView(btn.dataset.go)));
$('#copy-base').addEventListener('click',async()=>{await navigator.clipboard.writeText(state.grok_base_url);toast('API 地址已复制')});
$('#launch-grok').addEventListener('click',async()=>{try{await api('/api/grok/launch',{method:'POST',body:'{}'});toast('Grok Build 已在新窗口启动')}catch(e){toast(e.message,true)}});
$('#refresh-all').addEventListener('click',async e=>{const old=e.currentTarget.textContent;e.currentTarget.textContent='同步中…';try{const data=await api('/api/accounts/usage-all',{method:'POST',body:'{}'});state=data.state||data;render();toast(data.errors?'部分账号同步失败':'所有账号用量已刷新',!!data.errors)}catch(err){toast(err.message,true)}finally{e.currentTarget.textContent=old}});
$('#add-account').addEventListener('click',startOAuth);
$('#settings-form').addEventListener('input',e=>e.currentTarget.dataset.dirty='true');
$('#settings-form').addEventListener('submit',async e=>{e.preventDefault();const form=e.currentTarget,data=Object.fromEntries(new FormData(form));try{await api('/api/settings',{method:'PUT',body:JSON.stringify(data)});form.dataset.dirty='false';toast('配置已保存，请重启程序使网络设置生效')}catch(err){toast(err.message,true)}});
$('#account-grid').addEventListener('click',async e=>{
  const button=e.target.closest('[data-action]'); if(!button)return;
  const card=button.closest('[data-id]'), identity=card.dataset.id, account=state.accounts.find(a=>a.identity===identity), action=button.dataset.action;
  try{
    if(action==='delete'&&!confirm(`确定删除 ${account.name}？本地 OAuth 凭据将被移除。`))return;
    const routes={prefer:'/api/accounts/preferred',usage:'/api/accounts/usage',clear:'/api/accounts/cooldown/clear',delete:'/api/accounts/delete'};
    let path=routes[action],body={identity};
    if(action==='toggle'){path='/api/accounts/enabled';body.enabled=!account.enabled}
    button.textContent='处理中…'; const data=await api(path,{method:'POST',body:JSON.stringify(body)}); state=data.state||data; render(); toast(action==='usage'?'用量已刷新':'账号池已更新');
  }catch(err){toast(err.message,true);refreshState(true)}
});

async function startOAuth(){
  const modal=$('#oauth-modal'); modal.hidden=false; $('#oauth-message').textContent='正在向 xAI 请求安全授权链接…'; $('#oauth-code').hidden=true; $('#oauth-link').hidden=true; $('#oauth-status').textContent='准备中';
  try{const data=await api('/api/oauth/start',{method:'POST',body:'{}'});$('#oauth-message').textContent='在新页面完成授权，网关会自动接收并托管账号。';$('#oauth-code').hidden=false;$('#oauth-code strong').textContent=data.user_code;$('#oauth-link').hidden=false;$('#oauth-link').href=data.verification_url;$('#oauth-status').textContent='等待你完成授权';openExternal(data.verification_url);scheduleOAuthPoll(data.session_id,Math.max(1200,data.interval_ms||5000));}
  catch(err){$('#oauth-message').textContent=err.message;$('#oauth-status').textContent='授权初始化失败';toast(err.message,true)}
}
function scheduleOAuthPoll(id,delay){clearTimeout(oauthTimer);oauthTimer=setTimeout(async()=>{if($('#oauth-modal').hidden)return;try{const data=await api('/api/oauth/poll',{method:'POST',body:JSON.stringify({session_id:id})});if(data.status==='complete'){toast(`账号 ${data.account} 已添加`);$('#oauth-modal').hidden=true;await refreshState();return}scheduleOAuthPoll(id,Math.max(1200,data.retry_after_ms||delay))}catch(err){toast(err.message,true);$('#oauth-status').textContent='授权失败，请关闭后重试'}},delay)}
$$('[data-close-modal]').forEach(btn=>btn.addEventListener('click',()=>{$('#oauth-modal').hidden=true;clearTimeout(oauthTimer)}));
$('#oauth-link').addEventListener('click',e=>{e.preventDefault();openExternal(e.currentTarget.href)});
$('#oauth-modal').addEventListener('click',e=>{if(e.target===e.currentTarget){e.currentTarget.hidden=true;clearTimeout(oauthTimer)}});

refreshState();
setInterval(()=>refreshState(true),10000);
