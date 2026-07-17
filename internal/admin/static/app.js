const $ = (s, root=document) => root.querySelector(s);
const $$ = (s, root=document) => [...root.querySelectorAll(s)];
let state = null;
let stats = null;
let logs = [];
let oauthTimer = null;
let busy = { usage:false, models:false, action:null };

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
const dateText = value => value && !String(value).startsWith('0001-')
  ? new Date(value).toLocaleString('zh-CN',{month:'numeric',day:'numeric',hour:'2-digit',minute:'2-digit',hour12:false})
  : '—';
const resetValue = usage => usage?.usage_period_end || usage?.billing_period_end || '';
const remainingText = value => {
  if (!value) return '未知';
  const ms = new Date(value) - Date.now();
  if (ms <= 0) return '即将恢复';
  const h = Math.floor(ms / 3600000), d = Math.floor(h / 24);
  return d > 0 ? `${d} 天 ${h%24} 小时` : `${h} 小时 ${Math.floor(ms%3600000/60000)} 分`;
};
/** Compact number for charts / dense UI */
const fmtNum = n => {
  n = Number(n||0);
  if (!Number.isFinite(n)) return '—';
  const abs = Math.abs(n);
  if (abs >= 1e6) return (n/1e6).toFixed(abs >= 1e7 ? 0 : 1).replace(/\.0$/,'') + 'M';
  if (abs >= 10000) return (n/1e3).toFixed(abs >= 1e5 ? 0 : 1).replace(/\.0$/,'') + 'k';
  return Math.round(n).toLocaleString('zh-CN');
};
/** Full tabular number for primary stats */
const fmtInt = n => {
  n = Number(n||0);
  if (!Number.isFinite(n)) return '—';
  return Math.round(n).toLocaleString('zh-CN');
};
const fmtDuration = ms => {
  if (ms == null || !Number.isFinite(Number(ms))) return '—';
  ms = Number(ms);
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 10000) return `${(ms/1000).toFixed(1)} s`;
  return `${Math.round(ms/1000)} s`;
};
const ICO = {
  refresh: '<span class="btn-ico" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M20 12a8 8 0 1 1-2.3-5.6"/><path d="M20 5v5h-5"/></svg></span>',
  cube: '<span class="btn-ico" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M12 3 4.5 7.5v9L12 21l7.5-4.5v-9L12 3z"/><path d="M12 12 4.8 7.7"/><path d="M12 12v9"/><path d="m12 12 7.2-4.3"/></svg></span>',
};
const openExternal = url => {
  if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url);
  else window.open(url, '_blank', 'noopener');
};

function toast(message, error=false){
  const el=document.createElement('div'); el.className=`toast${error?' error':''}`; el.textContent=message;
  $('#toasts').append(el); setTimeout(()=>el.remove(),4200);
}

function setButtonBusy(btn, busyOn, busyHtml='处理中…'){
  if (!btn) return;
  if (busyOn) {
    if (!btn.dataset.html) btn.dataset.html = btn.innerHTML;
    btn.disabled = true;
    btn.innerHTML = busyHtml;
  } else {
    btn.disabled = false;
    if (btn.dataset.html) {
      btn.innerHTML = btn.dataset.html;
      delete btn.dataset.html;
    }
  }
}

async function refreshState(silent=false){
  try {
    state = await api('/api/state');
    render();
  } catch(error){ if(!silent) toast(error.message,true); }
}

async function refreshStats(silent=false){
  try {
    stats = await api('/api/stats?days=7');
    renderStats();
    renderOverviewExtras();
  } catch(error){ if(!silent) toast(error.message,true); }
}

async function refreshLogs(silent=false){
  try {
    const data = await api('/api/logs?limit=200');
    logs = data.logs || [];
    renderLogs();
  } catch(error){ if(!silent) toast(error.message,true); }
}

function modelsOf(identity){
  return state?.models?.[identity] || null;
}

function render(){
  if(!state) return;
  const accounts=state.accounts||[], available=accounts.filter(a=>a.available);
  $('#side-version').textContent=`v${state.version} · 127.0.0.1`;
  $('#base-url').textContent=state.grok_base_url;
  $('#route-listen').textContent=state.settings.listen;
  $('#route-proxy').textContent=(state.settings.outbound_proxy||'').replace(/^https?:\/\//,'');
  $('#stat-total').textContent = fmtInt(accounts.length);
  $('#stat-total-note').textContent = accounts.length ? `${accounts.filter(a=>a.enabled).length} 个已启用` : '等待添加账号';
  $('#stat-available').textContent = fmtInt(available.length);
  renderMini(accounts);
  renderAccounts(accounts);
  fillSettings();
  renderOverviewExtras();
  updateTopButtons();
}

function updateTopButtons(){
  const usageBtn = $('#refresh-all');
  const modelsBtn = $('#refresh-models');
  if (usageBtn && !busy.usage && !usageBtn.dataset.html) {
    usageBtn.innerHTML = `${ICO.refresh} 刷新用量`;
  }
  if (modelsBtn && !busy.models && !modelsBtn.dataset.html) {
    modelsBtn.innerHTML = `${ICO.cube} 刷新模型`;
  }
  if (usageBtn) usageBtn.disabled = !!busy.usage;
  if (modelsBtn) modelsBtn.disabled = !!busy.models;
}

function renderOverviewExtras(){
  if (!stats) return;
  const today = stats.today || {};
  $('#stat-tokens').textContent = today.total_tokens != null ? fmtNum(today.total_tokens) : '—';
  $('#stat-tokens-note').textContent = `输入 ${fmtNum(today.input_tokens||0)} · 输出 ${fmtNum(today.output_tokens||0)}`;
  $('#stat-requests').textContent = today.requests != null ? fmtInt(today.requests) : '—';
  $('#stat-requests-note').textContent = `${fmtInt(today.success||0)} 成功 · ${fmtInt(today.failed||0)} 失败`;

  const hourly = stats.hourly || [];
  const hours = stats.hourly_hours || hourly.length || 24;
  const title = $( '#overview-chart-title');
  const meta = $('#overview-chart-meta');
  if (title) title.textContent = hours > 24 ? `近 ${hours} 小时 Token` : '近 24 小时 Token';
  const sum = hourly.reduce((a,p)=>a+(p.total_tokens||0),0);
  const req = hourly.reduce((a,p)=>a+(p.requests||0),0);
  if (meta) meta.textContent = `${fmtNum(sum)} tokens · ${fmtInt(req)} 次 · 按小时`;
  // Defer one frame so container has final width after layout.
  requestAnimationFrame(() => {
    drawLineChart($('#overview-chart'), hourly.map(p=>({
      label: p.label || '',
      value: p.total_tokens || 0,
      requests: p.requests || 0,
    })), { labelEvery: hours > 36 ? 8 : hours > 24 ? 6 : 4 });
  });
}

function statusOf(a){
  if(!a.enabled) return ['已停用',''];
  if(a.cooldown_until && new Date(a.cooldown_until)>new Date()) return ['冷却中','amber'];
  if(a.available) return [a.preferred?'首选':'可用','lime'];
  return ['不可用','amber'];
}

function renderMini(accounts){
  const root=$('#overview-accounts');
  if(!accounts.length){
    root.innerHTML='<div class="empty">还没有托管账号。<br>前往「账号池」添加 OAuth 或 API Key。</div>';
    return;
  }
  root.innerHTML=accounts.slice(0,4).map(a=>{
    const [label]=statusOf(a),usage=pct(a.usage?.usage_percent);
    const models = modelsOf(a.identity);
    const modelHint = models?.models?.length
      ? models.models.slice(0,2).map(m=>m.id).join(' · ')
      : (a.kind==='api_key' ? 'API Key 账号' : 'OAuth 账号');
    return `<div class="mini-account"><div class="avatar">${esc(initial(a.name))}</div><div><strong>${esc(a.name)}</strong><span>${esc(modelHint)}</span></div>
    <div class="mini-progress"><i style="width:${usage}%"></i></div><span class="state-label">${esc(label)}</span></div>`;
  }).join('');
}

function renderAccounts(accounts){
  const root=$('#account-grid');
  if(!accounts.length){
    root.innerHTML='<div class="empty">账号池为空。<br>点击「添加 OAuth 账号」或「添加 API Key」开始托管。</div>';
    return;
  }
  root.innerHTML=accounts.map(a=>{
    const [label,tone]=statusOf(a),usage=pct(a.usage?.usage_percent),reset=resetValue(a.usage);
    const models = modelsOf(a.identity);
    const modelTags = models?.models?.length
      ? models.models.slice(0,4).map(m=>`<span class="model-tag">${esc(m.id)}</span>`).join('')
      : (models?.error
        ? `<span class="model-tag amber" title="${esc(models.error)}">${esc(models.error.slice(0,36))}</span>`
        : '<span class="model-tag muted">尚未拉取模型</span>');
    const kind = a.kind === 'api_key' ? 'API Key' : 'OAuth';
    const usagePct = a.usage ? `${Math.round(usage)}%` : '—';
    return `
    <article class="account-card card ${a.preferred?'preferred':''}" data-id="${esc(a.identity)}">
      <div class="account-top"><div class="avatar">${esc(initial(a.name))}</div><div class="account-meta"><strong>${esc(a.name)}</strong><span>${esc(a.email||a.identity)} · ${kind}</span></div><span class="badge ${tone}">${esc(label)}</span></div>
      <div class="model-row">${modelTags}</div>
      <div class="usage-head"><span>${esc(a.usage?.plan_name||'套餐用量未同步')}</span><strong class="num">${usagePct}</strong></div>
      <div class="progress"><i class="${usage>=85?'high':''}" style="width:${usage}%"></i></div>
      <div class="usage-foot"><span>${a.usage?`已用 ${formatUsage(a.usage)}`:'点击「刷新用量」同步 Billing'}</span><span>${reset?`重置倒计时 ${remainingText(reset)}`:'重置时间未知'}</span></div>
      <div class="account-actions">
        <button class="btn ${a.preferred?'btn-ghost':'btn-primary'}" data-action="prefer" type="button">${a.preferred?'当前首选':'设为首选'}</button>
        <button class="btn btn-ghost" data-action="usage" type="button">刷新用量</button>
        <button class="btn btn-ghost" data-action="models" type="button">刷新模型</button>
        ${a.cooldown_until&&new Date(a.cooldown_until)>new Date()?'<button class="btn btn-ghost" data-action="clear" type="button">解除冷却</button>':''}
        <button class="btn btn-ghost" data-action="toggle" type="button">${a.enabled?'停用':'启用'}</button>
        <button class="btn btn-danger" data-action="delete" type="button">删除</button>
      </div>
    </article>`;
  }).join('');
}

function formatUsage(u){
  if(u.monthly_limit>0) return `${u.used.toFixed(1)} / ${u.monthly_limit.toFixed(1)}`;
  if(u.on_demand_cap>0) return `${u.on_demand_used.toFixed(1)} / ${u.on_demand_cap.toFixed(1)}`;
  return `${Math.round(u.usage_percent||0)}%`;
}

function fillSettings(){
  const form=$('#settings-form'); if(!form||form.dataset.dirty==='true') return;
  const s = state.settings || {};
  if (form.elements.listen) form.elements.listen.value = s.listen || '';
  if (form.elements.upstream_base_url) form.elements.upstream_base_url.value = s.upstream_base_url || '';
  if (form.elements.outbound_proxy) form.elements.outbound_proxy.value = s.outbound_proxy || '';
  if (form.elements.cooldown) form.elements.cooldown.value = s.cooldown || '';
  $('#require-api-key').checked = !!s.require_api_key;
  $('#log-bodies').checked = !!s.log_request_bodies;
  $('#key-status').textContent = s.gateway_api_key_set
    ? (s.require_api_key ? '已设置密钥 · 强制校验已开启' : '已设置密钥 · 未强制校验')
    : '未设置网关密钥';
}

function renderStats(){
  if (!stats) return;
  const total = stats.total || {};
  $('#range-requests').textContent = fmtInt(total.requests||0);
  $('#range-success').textContent = `${fmtInt(total.success||0)} 成功 · ${fmtInt(total.failed||0)} 失败`;
  $('#range-input').textContent = fmtNum(total.input_tokens||0);
  $('#range-output').textContent = fmtNum(total.output_tokens||0);
  $('#range-cached').textContent = fmtNum(total.cached_tokens||0);
  drawBars($('#stats-chart'), (stats.series||[]).map(d=>({label:d.date.slice(5), value:d.total_tokens||0})), 200, true);
  const root = $('#stats-accounts');
  const accounts = stats.accounts || [];
  if (!accounts.length) {
    root.innerHTML = '<div class="empty">暂无分账号数据。<br>通过网关发起几次模型请求后即可看到消耗对比。</div>';
    return;
  }
  const max = Math.max(...accounts.map(a=>a.total_tokens||0), 1);
  root.innerHTML = accounts.map(a => `
    <div class="mini-account">
      <div class="avatar">${esc(initial(a.name||a.identity))}</div>
      <div><strong>${esc(a.name||a.identity||'unknown')}</strong><span>${fmtInt(a.requests||0)} 次 · 成功 ${fmtInt(a.success||0)}</span></div>
      <div class="mini-progress"><i style="width:${Math.round((a.total_tokens||0)/max*100)}%"></i></div>
      <span class="state-label">${fmtNum(a.total_tokens||0)}</span>
    </div>`).join('');
}

function renderLogs(){
  const body = $('#log-body');
  if (!logs.length) {
    body.innerHTML = '<tr><td colspan="9" class="empty-cell">暂无请求日志。通过网关调用模型后会显示在这里。</td></tr>';
    return;
  }
  body.innerHTML = logs.map(l => {
    const st = l.status || 0;
    const tone = st >= 200 && st < 400 ? 'ok' : 'bad';
    const tokens = (l.total_tokens||0) > 0
      ? `${fmtNum(l.input_tokens||0)} → ${fmtNum(l.output_tokens||0)}${(l.cached_tokens||0)>0?` · 缓存 ${fmtNum(l.cached_tokens)}`:''}`
      : '—';
    return `<tr>
      <td>${esc(dateText(l.time))}</td>
      <td>${esc(l.method||'')}</td>
      <td class="mono">${esc(l.path||'')}</td>
      <td>${esc(l.account_name||l.account||'—')}</td>
      <td class="mono">${esc(l.model||'—')}</td>
      <td><span class="status-pill ${tone}">${st||'—'}</span></td>
      <td>${fmtDuration(l.duration_ms)}</td>
      <td class="mono">${tokens}</td>
      <td class="err-cell" title="${esc(l.error||l.error_code||'')}">${esc(l.error_code||l.error||'—')}</td>
    </tr>`;
  }).join('');
}

function niceMax(value){
  if (value <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(value)));
  const n = value / exp;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return nice * exp;
}

/** Catmull-Rom → cubic Bezier smooth curve through points. */
function smoothPath(coords, yMin, yMax){
  if (!coords.length) return '';
  if (coords.length === 1) return `M ${coords[0].x} ${coords[0].y}`;
  if (coords.length === 2) {
    return `M ${coords[0].x} ${coords[0].y} L ${coords[1].x} ${coords[1].y}`;
  }
  const clampY = y => Math.min(yMax, Math.max(yMin, y));
  let d = `M ${coords[0].x.toFixed(2)} ${coords[0].y.toFixed(2)}`;
  for (let i = 0; i < coords.length - 1; i++) {
    const p0 = coords[Math.max(0, i - 1)];
    const p1 = coords[i];
    const p2 = coords[i + 1];
    const p3 = coords[Math.min(coords.length - 1, i + 2)];
    // Standard CR tension ~0.5 via /6 control offsets
    const cp1x = p1.x + (p2.x - p0.x) / 6;
    const cp1y = clampY(p1.y + (p2.y - p0.y) / 6);
    const cp2x = p2.x - (p3.x - p1.x) / 6;
    const cp2y = clampY(p2.y - (p3.y - p1.y) / 6);
    d += ` C ${cp1x.toFixed(2)} ${cp1y.toFixed(2)}, ${cp2x.toFixed(2)} ${cp2y.toFixed(2)}, ${p2.x.toFixed(2)} ${p2.y.toFixed(2)}`;
  }
  return d;
}

/**
 * Hourly smooth-curve chart.
 * ViewBox matches container pixel size so the plot is wide/full-bleed
 * without squashing axis text.
 */
function drawLineChart(el, points, opts={}){
  if (!el) return;
  if (!points.length) {
    el.innerHTML = '<div class="empty" style="padding:18px;font-size:12px">暂无分时数据。通过网关发起请求后会按小时汇总。</div>';
    return;
  }
  // Match actual box — chart uses full width of the card section.
  const rect = el.getBoundingClientRect();
  const width = Math.max(Math.round(rect.width || el.clientWidth || 480), 320);
  const height = Math.max(Math.round(rect.height || el.clientHeight || opts.height || 168), 140);
  const pad = { l: 40, r: 14, t: 16, b: 30 };
  const plotW = Math.max(40, width - pad.l - pad.r);
  const plotH = Math.max(40, height - pad.t - pad.b);
  const maxV = niceMax(Math.max(...points.map(p=>p.value||0), 1));
  const n = points.length;
  const xAt = i => pad.l + (n <= 1 ? plotW/2 : (i/(n-1))*plotW);
  const yAt = v => {
    const y = pad.t + plotH - (Math.max(0,v)/maxV)*plotH;
    return Math.min(pad.t + plotH, Math.max(pad.t, y));
  };
  // Fewer x labels on dense 48h series so the axis stays readable
  const labelEvery = opts.labelEvery || (n > 36 ? 8 : n > 24 ? 6 : 4);

  const coords = points.map((p,i)=>({ x: xAt(i), y: yAt(p.value||0), p }));
  const line = smoothPath(coords, pad.t, pad.t + plotH);
  const last = coords[coords.length-1];
  const first = coords[0];
  const area = `${line} L ${last.x.toFixed(2)} ${(pad.t+plotH).toFixed(2)} L ${first.x.toFixed(2)} ${(pad.t+plotH).toFixed(2)} Z`;
  const gradId = `tokenGradient-${Math.random().toString(36).slice(2,8)}`;

  let grid = '';
  for (let g = 0; g <= 3; g++) {
    const v = maxV * (1 - g/3);
    const y = pad.t + (plotH * g/3);
    grid += `<line class="chart-grid" x1="${pad.l}" y1="${y.toFixed(1)}" x2="${width-pad.r}" y2="${y.toFixed(1)}"/>`;
    grid += `<text class="chart-axis" x="${pad.l-8}" y="${(y+3.5).toFixed(1)}" text-anchor="end">${fmtNum(v)}</text>`;
  }

  let xLabels = '';
  const used = new Set();
  points.forEach((p,i) => {
    const isEdge = i === 0 || i === n-1;
    const onStep = i % labelEvery === 0;
    if (!isEdge && !onStep) return;
    // avoid colliding last two labels
    if (i === n-2 && n > 2) return;
    if (used.has(i)) return;
    used.add(i);
    xLabels += `<text class="chart-axis" x="${xAt(i).toFixed(1)}" y="${height-9}" text-anchor="middle">${esc(p.label)}</text>`;
  });

  // Sparse dots: peaks / non-zero / ends only
  const dots = coords.map((c,i) => {
    const v = c.p.value || 0;
    const prev = i > 0 ? (coords[i-1].p.value||0) : v;
    const next = i < n-1 ? (coords[i+1].p.value||0) : v;
    const peak = v > 0 && v >= prev && v >= next;
    const show = peak || i === 0 || i === n-1 || (v > 0 && i % labelEvery === 0);
    if (!show) return '';
    return `<circle class="chart-dot" cx="${c.x.toFixed(1)}" cy="${c.y.toFixed(1)}" r="3"/>`;
  }).join('');

  el.innerHTML = `<svg viewBox="0 0 ${width} ${height}" width="100%" height="100%" preserveAspectRatio="none" class="chart-svg" role="img" aria-label="分时 Token 曲线">
    <defs>
      <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#b8ff5a" stop-opacity="0.32"/>
        <stop offset="100%" stop-color="#b8ff5a" stop-opacity="0.02"/>
      </linearGradient>
    </defs>
    ${grid}
    <path class="chart-area" fill="url(#${gradId})" d="${area}"/>
    <path class="chart-line" d="${line}"/>
    ${dots}
    ${xLabels}
  </svg>`;
}

function drawBars(el, points, height=160, showValue=false){
  if (!el) return;
  if (!points.length) { el.innerHTML = '<div class="empty" style="padding:16px;font-size:12px">暂无数据</div>'; return; }
  const max = niceMax(Math.max(...points.map(p=>p.value), 1));
  const padBottom = 22;
  const padTop = showValue ? 16 : 10;
  const w = Math.max(points.length * 44, 300);
  const slot = w / points.length;
  const barW = Math.min(26, Math.max(12, slot - 14));
  const chartH = Math.max(24, height - padBottom - padTop);
  let bars = '';
  points.forEach((p,i) => {
    const h = p.value > 0 ? Math.max(4, Math.round((p.value/max)*chartH)) : 2;
    const x = i * slot + (slot - barW)/2;
    const y = padTop + chartH - h;
    bars += `<rect class="bar" x="${x.toFixed(1)}" y="${y}" width="${barW}" height="${h}" rx="4"/>`;
    bars += `<text class="bar-label" x="${(x+barW/2).toFixed(1)}" y="${height-5}" text-anchor="middle">${esc(p.label)}</text>`;
    if (showValue && p.value > 0) {
      bars += `<text class="bar-value" x="${(x+barW/2).toFixed(1)}" y="${Math.max(12, y-4)}" text-anchor="middle">${fmtNum(p.value)}</text>`;
    }
  });
  // meet keeps labels readable (not horizontally flattened)
  el.innerHTML = `<svg viewBox="0 0 ${w} ${height}" preserveAspectRatio="xMidYMid meet" class="chart-svg">${bars}</svg>`;
}

function switchView(name){
  $$('.view').forEach(v=>v.classList.toggle('active',v.id===`view-${name}`));
  $$('.nav-item').forEach(v=>v.classList.toggle('active',v.dataset.view===name));
  const labels={
    overview:['控制中心','运行概览'],
    accounts:['账号管理','账号池'],
    stats:['本地计量','用量统计'],
    logs:['调用记录','请求日志'],
    settings:['网关配置','配置']
  };
  $('#page-eyebrow').textContent=labels[name][0];
  $('#page-title').textContent=labels[name][1];
  if (name === 'stats') refreshStats(true);
  if (name === 'logs') refreshLogs(true);
}

$$('.nav-item').forEach(btn=>btn.addEventListener('click',()=>switchView(btn.dataset.view)));
$$('[data-go]').forEach(btn=>btn.addEventListener('click',()=>switchView(btn.dataset.go)));

$('#copy-base').addEventListener('click',async()=>{
  await navigator.clipboard.writeText(state.grok_base_url);
  toast('API 地址已复制');
});

$('#launch-grok').addEventListener('click',async()=>{
  try {
    await api('/api/grok/launch',{method:'POST',body:'{}'});
    toast('Grok Build 已在新窗口启动');
  } catch(e){ toast(e.message,true); }
});

$('#refresh-all').addEventListener('click', async e => {
  const btn = e.currentTarget;
  if (busy.usage) return;
  busy.usage = true;
  setButtonBusy(btn, true, `${ICO.refresh} 同步中…`);
  try {
    const data = await api('/api/accounts/usage-all',{method:'POST',body:'{}'});
    state = data.state || data;
    render();
    toast(data.errors ? '部分账号用量同步失败' : '全部账号用量已刷新', !!data.errors);
  } catch(err) {
    toast(err.message, true);
  } finally {
    busy.usage = false;
    setButtonBusy(btn, false);
  }
});

$('#refresh-models').addEventListener('click', async e => {
  const btn = e.currentTarget;
  if (busy.models) return;
  busy.models = true;
  setButtonBusy(btn, true, `${ICO.cube} 拉取中…`);
  try {
    const data = await api('/api/accounts/models-all',{method:'POST',body:'{}'});
    state = data.state || state;
    if (data.models) state.models = data.models;
    render();
    toast(data.errors ? '部分账号模型拉取失败' : '模型列表已更新', !!data.errors);
  } catch(err) {
    toast(err.message, true);
  } finally {
    busy.models = false;
    setButtonBusy(btn, false);
  }
});

$('#add-account').addEventListener('click', startOAuth);
$('#add-api-key').addEventListener('click', () => { $('#apikey-modal').hidden = false; });
$$('[data-close-apikey]').forEach(btn => btn.addEventListener('click', () => { $('#apikey-modal').hidden = true; }));
$('#apikey-modal').addEventListener('click', e => { if (e.target === e.currentTarget) e.currentTarget.hidden = true; });
$('#apikey-save').addEventListener('click', async () => {
  try {
    const data = await api('/api/accounts/api-key', {
      method:'POST',
      body: JSON.stringify({ name: $('#apikey-name').value, api_key: $('#apikey-value').value })
    });
    state = data.state || state;
    render();
    $('#apikey-modal').hidden = true;
    $('#apikey-name').value = '';
    $('#apikey-value').value = '';
    toast(`账号 ${data.account} 已保存`);
  } catch(err) { toast(err.message, true); }
});

$('#settings-form').addEventListener('input', e => {
  if (e.target.closest('#settings-form')) e.currentTarget.dataset.dirty = 'true';
});
$('#settings-form').addEventListener('submit', async e => {
  e.preventDefault();
  const form = e.currentTarget;
  const data = {
    listen: form.elements.listen.value,
    upstream_base_url: form.elements.upstream_base_url.value,
    outbound_proxy: form.elements.outbound_proxy.value,
    cooldown: form.elements.cooldown.value,
    require_api_key: $('#require-api-key').checked,
    log_request_bodies: $('#log-bodies').checked,
  };
  try {
    const res = await api('/api/settings',{method:'PUT',body:JSON.stringify(data)});
    form.dataset.dirty = 'false';
    if (res.state) state = res.state;
    render();
    toast(res.restart_required ? '配置已保存，网络相关项需重启生效' : '配置已保存');
  } catch(err){ toast(err.message,true); }
});

$('#rotate-key').addEventListener('click', async () => {
  try {
    const data = await api('/api/settings/gateway-key', {
      method:'POST',
      body: JSON.stringify({ require_api_key: $('#require-api-key').checked })
    });
    if (data.state) state = data.state;
    render();
    if (data.gateway_api_key) {
      $('#key-reveal').hidden = false;
      $('#key-once').textContent = data.gateway_api_key;
      toast('新密钥已生成，请立即复制');
    }
  } catch(err){ toast(err.message,true); }
});

$('#clear-key').addEventListener('click', async () => {
  if (!confirm('确定清除网关 API Key？')) return;
  try {
    const data = await api('/api/settings/gateway-key', { method:'POST', body: JSON.stringify({ clear:true }) });
    if (data.state) state = data.state;
    $('#key-reveal').hidden = true;
    render();
    toast('网关密钥已清除');
  } catch(err){ toast(err.message,true); }
});

$('#copy-key').addEventListener('click', async () => {
  const key = $('#key-once').textContent;
  if (!key) return;
  await navigator.clipboard.writeText(key);
  toast('密钥已复制');
});

$('#reload-stats').addEventListener('click', () => refreshStats());
$('#reload-logs').addEventListener('click', () => refreshLogs());
$('#export-csv').addEventListener('click', () => { window.open('/api/stats/export?days=7', '_blank'); });
$('#clear-logs').addEventListener('click', async () => {
  if (!confirm('确定清空全部请求日志与本地日统计？')) return;
  try {
    await api('/api/logs/clear', { method:'POST', body:'{}' });
    logs = [];
    stats = null;
    renderLogs();
    await refreshStats(true);
    toast('日志已清空');
  } catch(err){ toast(err.message,true); }
});

$('#account-grid').addEventListener('click', async e => {
  const button = e.target.closest('[data-action]');
  if (!button || button.disabled) return;
  const card = button.closest('[data-id]');
  const identity = card.dataset.id;
  const account = state.accounts.find(a => a.identity === identity);
  const action = button.dataset.action;
  const label = button.textContent;
  try {
    if (action === 'delete' && !confirm(`确定删除 ${account.name}？本地凭据将被移除。`)) return;
    button.disabled = true;
    button.textContent = '处理中…';
    if (action === 'models') {
      const data = await api('/api/accounts/models', { method:'POST', body: JSON.stringify({ identity }) });
      state = data.state || state;
      if (data.models) state.models = data.models;
      render();
      toast(data.errors ? '模型拉取失败' : '模型已刷新', !!data.errors);
      return;
    }
    const routes = {
      prefer:'/api/accounts/preferred',
      usage:'/api/accounts/usage',
      clear:'/api/accounts/cooldown/clear',
      delete:'/api/accounts/delete'
    };
    let path = routes[action], body = { identity };
    if (action === 'toggle') { path = '/api/accounts/enabled'; body.enabled = !account.enabled; }
    const data = await api(path, { method:'POST', body: JSON.stringify(body) });
    state = data.state || data;
    render();
    toast(action === 'usage' ? '用量已刷新' : '账号池已更新');
  } catch(err) {
    toast(err.message, true);
    button.disabled = false;
    button.textContent = label;
    refreshState(true);
  }
});

async function startOAuth(){
  const modal=$('#oauth-modal'); modal.hidden=false;
  $('#oauth-message').textContent='正在向 xAI 请求安全授权链接…';
  $('#oauth-code').hidden=true; $('#oauth-link').hidden=true; $('#oauth-status').textContent='准备中';
  try{
    const data=await api('/api/oauth/start',{method:'POST',body:'{}'});
    $('#oauth-message').textContent='在新页面完成授权，网关会自动接收并托管账号。';
    $('#oauth-code').hidden=false; $('#oauth-code strong').textContent=data.user_code;
    $('#oauth-link').hidden=false; $('#oauth-link').href=data.verification_url;
    $('#oauth-status').textContent='等待你完成授权';
    openExternal(data.verification_url);
    scheduleOAuthPoll(data.session_id, Math.max(1200, data.interval_ms||5000));
  } catch(err){
    $('#oauth-message').textContent=err.message;
    $('#oauth-status').textContent='授权初始化失败';
    toast(err.message,true);
  }
}
function scheduleOAuthPoll(id,delay){
  clearTimeout(oauthTimer);
  oauthTimer=setTimeout(async()=>{
    if($('#oauth-modal').hidden) return;
    try{
      const data=await api('/api/oauth/poll',{method:'POST',body:JSON.stringify({session_id:id})});
      if(data.status==='complete'){
        toast(`账号 ${data.account} 已添加`);
        $('#oauth-modal').hidden=true;
        await refreshState();
        return;
      }
      scheduleOAuthPoll(id, Math.max(1200, data.retry_after_ms||delay));
    }catch(err){
      toast(err.message,true);
      $('#oauth-status').textContent='授权失败，请关闭后重试';
    }
  }, delay);
}
$$('[data-close-modal]').forEach(btn=>btn.addEventListener('click',()=>{$('#oauth-modal').hidden=true;clearTimeout(oauthTimer)}));
$('#oauth-link').addEventListener('click',e=>{e.preventDefault();openExternal(e.currentTarget.href)});
$('#oauth-modal').addEventListener('click',e=>{if(e.target===e.currentTarget){e.currentTarget.hidden=true;clearTimeout(oauthTimer)}});

(async () => {
  await refreshState();
  await refreshStats(true);
  await refreshLogs(true);
})();
setInterval(()=>refreshState(true), 10000);
setInterval(()=>refreshStats(true), 30000);
