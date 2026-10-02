'use strict';
const $ = s => document.querySelector(s);
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));
const quote = value => "'" + String(value).replaceAll("'", "'\"'\"'") + "'";
let state = {servers:[], nodes:[], clients:[]}, page = 'overview', publicURL = '', editor = null, timer = null, loading = false, selectedIDs = new Set();
const names = {
  settings:['后台设置','管理管理员账号和登录密码。','后台设置'],
  notifications:['外部通知','通过 Telegram 接收运行状态、到期及流量提醒。','外部通知'],
  overview:['服务器总览','让服务器、入站和用户保持连接。','总览'],
  servers:['服务器','安装一个 Agent，在这里管理每一台 VPS。','服务器'],
  nodes:['入站','选择服务器，配置 VLESS + REALITY 入站。','入站节点'],
  clients:['客户端','查看每个用户的流量，分配跨服务器节点。','用户与订阅']
};
async function api(path, method = 'GET', data) {
  const response = await fetch(path, {method, headers:{'Content-Type':'application/json','X-VibeXUI':'1'}, body:data === undefined ? undefined : JSON.stringify(data)});
  const body = await response.json().catch(() => ({error:'服务器响应异常'}));
  if (!response.ok) {
    if (response.status === 401 && path !== '/api/login') showLogin();
    throw Error(body.error || `请求失败 ${response.status}`);
  }
  return body;
}
function toast(message) {
  $('#toast').textContent = message; $('#toast').hidden = false;
  clearTimeout(toast.timeout); toast.timeout = setTimeout(() => $('#toast').hidden = true, 4000);
}
function showLogin() {
  if(page==='settings')$('#workspace').innerHTML='';
  clearInterval(timer); timer = null;
  $('#app').hidden = true; $('#login').hidden = false;
  $('#login-form input[name=password]').value = '';
  $('#editor').close(); $('#info').close();
}
async function enter() {
  const me = await api('/api/me'); publicURL = me.publicURL;
  $('.avatar').textContent = me.username.slice(0,1).toUpperCase();
  $('#login').hidden = true; $('#app').hidden = false;
  await refresh();
  if (!timer) timer = setInterval(() => refresh().catch(e => {if (!$('#app').hidden) toast(e.message);}), 10000);
}
async function refresh() {
  if(page==='settings'){if(!$('#account-form'))await loadAccountSettings();else await api('/api/me');return;}
  if (page === 'notifications') {await refreshTelegramStatus(); return;}
  if (loading) return;
  loading = true; $('#refresh-button').disabled = true;
  try {
    state = await api('/api/state'); render();
    $('#sync-indicator').textContent = '已更新 ' + new Date().toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'});
  } catch (error) {$('#sync-indicator').textContent = '更新失败'; throw error;}
  finally {loading = false; $('#refresh-button').disabled = false;}
}
const online = s => Date.now() - new Date(s.lastSeen).getTime() < 35000;
const total = v => (v.upload || 0) + (v.download || 0);
const date = value => new Date(value).getFullYear() > 2000 ? new Date(value).toLocaleString('zh-CN') : '尚无数据';
const bytes = n => {if (!n) return '0 B'; const units=['B','KB','MB','GB','TB','PB']; const i=Math.min(5,Math.floor(Math.log(n)/Math.log(1024))); return (n/1024**i).toFixed(i ? 1 : 0)+' '+units[i];};
const badge = (text, kind='') => `<span class="badge ${kind}">${esc(text)}</span>`;
const btn = (action,id,label,danger=false) => `<button type="button" data-action="${action}" data-id="${esc(id)}" class="${danger?'danger':''}">${esc(label)}</button>`;
const serverName = id => state.servers.find(s => s.id === id)?.name || '已移除服务器';
function connection(s) {
  if (online(s)) return ['在线','good'];
  if (s.registrationState === 'pending') return ['等待注册','warn'];
  if (s.registrationState === 'expired') return ['注册已过期','bad'];
  return ['离线',''];
}
function nodeStatus(n) {
  if (!n.enabled) return ['已停用',''];
  if(new Date(n.expiresAt).getFullYear()>2000&&Date.now()>=new Date(n.expiresAt).getTime())return ['已到期','bad'];
  if(n.quotaBytes&&usage(n)>=n.quotaBytes)return ['额度用尽','bad'];
  const s = state.servers.find(s => s.id === n.serverId);
  if (!s || !online(s)) return ['服务器离线','warn'];
  if (s.error) return ['应用失败','bad'];
  if (s.appliedVersion !== s.version) return ['等待同步','warn'];
  if (!s.running) return ['Xray 已停止','warn'];
  return ['已同步','good'];
}
function visible(kind=page) {
  const source = kind==='overview' || kind==='servers' ? state.servers : kind==='nodes' ? state.nodes : state.clients;
  const query = $('#search').value.trim().toLowerCase(), filter = $('#filter').value;
  return source.filter(item => {
    const text = [item.name,item.host,item.uuid,item.email,item.group,item.comment,item.sni,item.target,item.port,kind==='nodes'?serverName(item.serverId):''].join(' ').toLowerCase();
    if(kind==='clients'&&$('#group-filter').value&&item.group!==$('#group-filter').value)return false;
    if (query && !text.includes(query)) return false;
    if (filter === 'all') return true;
    if (kind==='overview' || kind==='servers') return filter==='online' ? online(item) : filter==='attention' ? !online(item)||Boolean(item.error)||item.version!==item.appliedVersion : !online(item);
    if(kind==='clients'){if(filter==='orphan')return !item.nodeIds.length;if(filter==='online')return clientOnline(item);if(filter==='expired')return clientStatus(item)[0]==='已到期';if(filter==='quota')return clientStatus(item)[0]==='额度用尽';return filter==='enabled'?clientStatus(item)[1]==='good':clientStatus(item)[1]!=='good';}
    return filter==='enabled' ? nodeAllowed(item) : !nodeAllowed(item);
  });
}
function navigate(next) {
  selectedIDs.clear(); $('#group-filter').value=''; page = next; $('#search').value = '';
  const options = page==='overview'||page==='servers' ? [['all','所有状态'],['online','在线'],['offline','离线 / 待注册'],['attention','需要关注']] : [['all','所有状态'],['enabled','允许 / 启用'],['disabled','受限 / 停用'],...(page==='clients'?[['online','在线'],['expired','已到期'],['quota','额度用尽'],['orphan','未分配入站']]:[])];
  $('#filter').innerHTML = options.map(([value,label]) => `<option value="${value}">${label}</option>`).join('');
  render();
}
const rowSelection=id=>`<input type="checkbox" data-select="${esc(id)}" aria-label="选择此条目" ${selectedIDs.has(id)?'checked':''}>`;
function bulkToolbar(items) {
 const valid=new Set((page==='nodes'?state.nodes:state.clients).map(v=>v.id));for(const id of selectedIDs)if(!valid.has(id))selectedIDs.delete(id);
 return `<div class="bulk-toolbar"><label class="checkbox"><input type="checkbox" data-select-all ${items.length&&items.every(v=>selectedIDs.has(v.id))?'checked':''}>选择当前结果</label><span>已选 ${selectedIDs.size} 项</span><div class="actions">${[['enable','启用'],['disable','停用'],['reset','重置流量'],...(page==='clients'?[['add-days','增加有效期'],['add-bytes','增加额度'],['attach','分配入站'],['detach','移除入站'],['group','设置分组'],['flow','修改 Flow']]:[]),['delete','删除']].map(([action,label])=>`<button data-action="bulk-${action}" ${selectedIDs.size?'':'disabled'} class="${action==='delete'?'danger':''}">${label}</button>`).join('')}</div></div>`;
}
function batchDialog(seedNode) {
 info('批量添加客户端',`<form id="batch-form"><label>名称 / Email 前缀<input name="prefix" maxlength="80" required placeholder="例如 user"></label><label>数量（1–100）<input name="count" type="number" min="1" max="100" step="1" value="10" required></label>${selectField('设置模板（复制额度、到期、重置、分组与 IP 限制）','templateId','',[['','默认：启用、不限流量、无到期'],...state.clients.map(c=>[c.id,c.name])])}<p class="hint">每个客户端自动生成独立 UUID 和订阅标识，名称依次为 前缀-001、前缀-002。首次使用、流量和续期计数从零开始。</p><label class="checkbox"><input name="useNodes" type="checkbox" ${seedNode?'checked':''}>使用下方入站分配（否则沿用模板）</label><div class="node-options">${state.nodes.map(n=>`<label class="checkbox"><input type="checkbox" name="nodeIds" value="${esc(n.id)}" ${seedNode===n.id?'checked':''}>${esc(n.name)}</label>`).join('')}</div><p class="error" data-bulk-error></p><button class="primary" type="submit">创建客户端</button></form>`);
}
async function runBulk(action,values={}) {
 const ids=[...selectedIDs];if(!ids.length)return;
 await api('/api/'+(page==='nodes'?'nodes':'clients')+'/bulk','POST',{ids,action,...values});
 selectedIDs.clear();$('#info').close();await refresh();toast('批量操作已保存，等待同步');
}
function bulkDialog(action) {
 let fields='';
 if(action==='add-days')fields='<label>增加天数<input name="days" type="number" min="1" max="3650" step="1" value="30" required></label><p class="hint">未到期用户从原到期时间延长，已到期或不限时间用户从现在开始计时。首次使用有效期尚未激活时增加有效天数。</p>';
 if(action==='add-bytes')fields='<label>增加流量（GiB）<input name="quota" type="number" min="0.001" max="8388607" step="any" value="10" required></label><p class="hint">增加当前限额，保留已用流量。不限流量的用户请先设置限额。</p>';
 if(action==='group')fields=optionalField('分组（留空移出分组）','group','','maxlength="100"');
 if(action==='flow')fields=selectField('Flow','flow','xtls-rprx-vision',[['xtls-rprx-vision','xtls-rprx-vision'],['','无']]);
 if(action==='attach'||action==='detach')fields='<div class="node-options">'+state.nodes.map(n=>`<label class="checkbox"><input name="nodeIds" type="checkbox" value="${esc(n.id)}">${esc(n.name)}</label>`).join('')+'</div>';
 info('批量操作 · '+selectedIDs.size+' 个客户端',`<form id="bulk-form" data-operation="${action}">${fields}<p class="error" data-bulk-error></p><button class="primary" type="submit">应用设置</button></form>`);
}
function empty(title, description, action, label) {
  return `<div class="empty"><div class="empty-icon">◇</div><h3>${esc(title)}</h3><p>${esc(description)}</p><button class="primary" data-action="${action}">${esc(label)}</button></div>`;
}
function panel(title, count, headers, rows) {
  return `<div class="panel"><div class="panel-title"><h2>${esc(title)} <span class="hint">${count}</span></h2><small>配置与状态自动同步</small></div><div class="table-wrap"><table><thead><tr>${headers.map(h=>`<th>${esc(h)}</th>`).join('')}</tr></thead><tbody>${rows || `<tr><td colspan="${headers.length}" class="no-results">没有匹配的结果，请调整搜索或筛选。</td></tr>`}</tbody></table></div></div>`;
}
function renderGuide() {
  if (page !== 'overview') {$('#overview-guide').innerHTML=''; return;}
  const stages=[['连接服务器','安装 Agent 后自动上报',state.servers.some(online),'server-add'],['创建入站','在 VPS 上配置 REALITY',state.nodes.some(n=>n.enabled),'node-add'],['分配用户','生成跨服务器订阅',state.clients.some(c=>c.enabled&&c.nodeIds.length),'client-add']];
  const issues = state.servers.filter(s=>!online(s)||s.error||s.version!==s.appliedVersion);
  $('#overview-guide').innerHTML=`<div class="setup-steps">${stages.map(([title,description,done,action],i)=>`<button data-action="${action}" class="setup-step ${done?'complete':''}"><span class="step-number">${done?'✓':i+1}</span><span><strong>${title}</strong><small>${description}</small></span></button>`).join('')}</div>${issues.length?`<div class="attention"><span>● ${issues.length} 台服务器需要关注</span><button data-action="show-attention">查看状态 →</button></div>`:''}`;
}
function render() {
  const [title,description,crumb]=names[page];
  $('#page-title').textContent=title; $('#page-description').textContent=description; $('#crumb').textContent=crumb;
  document.querySelectorAll('[data-page]').forEach(b=>b.classList.toggle('active',b.dataset.page===page));
  const notifications=page==='notifications'||page==='settings';
  $('#add-button').hidden=notifications; $('#metrics').hidden=notifications; $('.list-toolbar').hidden=notifications; $('#overview-guide').hidden=notifications;
  if(page==='settings'){if(!$('#account-form'))loadAccountSettings();return;}
  if(notifications){if(!$('#telegram-form'))loadTelegramSettings();return;}
  $('#add-button').textContent=page==='clients'?'＋ 添加用户':page==='nodes'?'＋ 添加入站':'＋ 添加服务器';
  $('#export-button').hidden=page!=='clients'; $('#batch-button').hidden=page!=='clients'; $('#import-button').hidden=page!=='nodes'; $('#cleanup-button').hidden=page!=='clients'; $('#group-filter').hidden=page!=='clients';
  const groupValue=$('#group-filter').value; $('#group-filter').innerHTML='<option value="">所有分组</option>'+[...new Set(state.clients.map(c=>c.group).filter(Boolean))].sort().map(g=>`<option value="${esc(g)}">${esc(g)}</option>`).join(''); $('#group-filter').value=groupValue; if($('#group-filter').selectedIndex<0)$('#group-filter').selectedIndex=0;
  $('#metrics').innerHTML=[['在线服务器',state.servers.filter(online).length,`${state.servers.length} 台服务器已添加`,'▤'],['启用入站',state.nodes.filter(n=>n.enabled).length,`${state.nodes.length} 个入站已配置`,'◇'],['策略允许用户',state.clients.filter(c=>clientStatus(c)[1]==='good').length,'独立订阅 · 跨服务器','♙'],['累计代理流量',bytes(state.servers.reduce((sum,s)=>sum+total(s),0)),'服务器累计 · 每 10 秒上报','↗']].map(([name,value,note,icon])=>`<div class="metric"><div class="metric-top">${name}<span class="metric-icon">${icon}</span></div><div class="metric-value">${esc(value)}</div><div class="metric-note">${note}</div></div>`).join('');
  renderGuide(); const items=visible(); $('#result-count').textContent=`${items.length} 条结果`;
  let html='';
  if (page==='overview'||page==='servers') {
    html=state.servers.length?panel('服务器列表',items.length,['服务器','连接状态','Xray / 配置','流量','操作'],items.map(s=>`<tr><td><strong>${esc(s.name)}</strong><small>${esc(s.host)}</small></td><td>${badge(...connection(s))}<small>${online(s)?'最近上报：'+esc(date(s.lastSeen)):s.registrationState==='pending'?'令牌有效至 '+esc(date(s.registrationExpires)):esc(date(s.lastSeen))}</small></td><td>${badge(s.running?'运行中':'已停止',online(s)&&s.running?'good':'')} ${badge(s.error?'应用失败':s.appliedVersion===s.version?'已同步':'待应用',s.error?'bad':s.appliedVersion===s.version?'good':'warn')}<small>v${s.appliedVersion} / v${s.version}${!online(s)?' · 最后上报状态':''}</small>${s.error?`<div class="error-text">${esc(s.error)}</div>`:''}</td><td><strong>${bytes(total(s))}</strong><small>↑ ${bytes(s.upload)}　↓ ${bytes(s.download)}</small>${s.statsError?`<div class="error-text">统计异常</div>`:''}</td><td><div class="actions">${btn('server-details',s.id,'详情')}${btn(s.desiredRunning?'server-stop':'server-start',s.id,s.desiredRunning?'停止':'启动')}${btn('server-more',s.id,'更多')}</div></td></tr>`).join('')):empty('连接你的第一台服务器','添加 VPS，复制安装命令，即可开始统一管理。','server-add','＋ 添加服务器');
  }
  if (page==='nodes') {
    html=state.nodes.length?panel('入站节点',items.length,['选择','入站','服务器','REALITY','流量 / 限额','状态','操作'],items.map(n=>`<tr><td>${rowSelection(n.id)}</td><td><strong>${esc(n.name)}</strong><small>VLESS · TCP · :${n.port}</small></td><td>${esc(serverName(n.serverId))}<small>${state.clients.filter(c=>c.enabled&&c.nodeIds.includes(n.id)).length} 个启用用户</small></td><td>${esc(n.sni)}<small>目标 ${esc(n.target)}</small></td><td>${quotaSummary(n)}</td><td>${badge(...nodeStatus(n))}</td><td><div class="actions">${btn('node-clients',n.id,'客户端')}${btn('node-traffic',n.id,'流量')}${btn('node-reset',n.id,'重置流量')}${btn('node-edit',n.id,'编辑')}${btn('node-clone',n.id,'复制')}${btn('node-export',n.id,'导出')}${btn('node-toggle',n.id,n.enabled?'停用':'启用')}${btn('node-delete',n.id,'删除',true)}</div></td></tr>`).join('')):empty('还没有入站节点','选择服务器，为它创建一个 VLESS + REALITY 入站。','node-add','＋ 添加入站');
    html+='<div class="notice">客户端直接连接入站所在的 VPS。REALITY 密钥自动生成；目标需支持 TLS 1.3，SNI 与证书匹配，监听端口需在 VPS 防火墙中放行。</div>';
  }
  if (page==='clients') {
    html=state.clients.length?panel('客户端列表',items.length,['选择','用户','已分配入站','周期流量 / 限额','累计流量','状态','操作'],items.map(c=>`<tr><td>${rowSelection(c.id)}</td><td><strong>${esc(c.name)}</strong><small>${esc(c.email||c.name)}${c.group?' · '+esc(c.group):''}</small><small>${esc(c.uuid)}</small>${c.comment?`<small title="${esc(c.comment)}">${esc(c.comment.slice(0,40))}</small>`:''}</td><td>${c.nodeIds.length} 个入站<small>${esc(c.nodeIds.map(id=>state.nodes.find(n=>n.id===id)?.name).filter(Boolean).join(' · ')||'尚未分配')}</small></td><td>${quotaSummary(c)}</td><td><strong>${bytes(total(c))}</strong><small>↑ ${bytes(c.upload)}　↓ ${bytes(c.download)}</small><small>${date(c.trafficUpdatedAt)==='尚无数据'?'尚未采集到用户流量':esc(date(c.trafficUpdatedAt))}</small></td><td>${badge(...clientDisplayStatus(c))}<small>${clientOnline(c)?'● 在线':date(c.lastOnlineAt)==='尚无数据'?'未检测到在线连接':'最近在线：'+esc(date(c.lastOnlineAt))}</small></td><td><div class="actions">${btn('client-share',c.id,'订阅')}${btn('client-traffic',c.id,'流量')}${btn('client-reset',c.id,'重置流量')}${btn('client-ips',c.id,'IP 限制')}${btn('client-edit',c.id,'编辑')}${btn('client-clone',c.id,'复制')}${btn('client-toggle',c.id,c.enabled?'停用':'启用')}${btn('client-delete',c.id,'删除',true)}</div></td></tr>`).join('')):empty('添加第一个用户','分配多个入站，一个订阅即可连接多台服务器。','client-add','＋ 添加用户');
    html+='<div class="notice">用户流量由各 VPS 的 Xray 统计，经 Agent 上报后累计。停用用户立即关闭订阅，代理访问在配置同步后停用；离线 VPS 会继续使用旧配置。</div>';
  }
  $('#workspace').innerHTML=(page==='nodes'||page==='clients'?bulkToolbar(items):'')+html;
}
function field(label,name,value='',type='text',extra='') {
  return `<label>${esc(label)}<input name="${name}" type="${type}" value="${esc(value)}" ${extra} required></label>`;
}
const usage=c=>Math.max(0,(c.upload||0)-(c.quotaBaseline?.upload||0))+Math.max(0,(c.download||0)-(c.quotaBaseline?.download||0));
const resetLabels={never:'不自动重置',hourly:'每小时',daily:'每天',weekly:'每周一',monthly:'每月'};
const quotaPeriod=c=>c.trafficReset&&c.trafficReset!=='never'?(resetLabels[c.trafficReset]+(c.trafficReset==='monthly'?' '+c.trafficResetDay+' 日':'')):({0:'累计额度',1:'月流量',3:'季度流量',6:'半年流量',12:'年流量'}[c.quotaPeriodMonths||0]||'累计额度');
const quotaSummary=c=>`<strong>${bytes(usage(c))} / ${c.quotaBytes?bytes(c.quotaBytes):'不限'}</strong><small>${quotaPeriod(c)}</small><small>${(c.quotaPeriodMonths||c.trafficReset&&c.trafficReset!=='never')?'下次重置：'+esc(date(c.quotaResetsAt)):'不自动重置'}</small>`;
const clientStatus=c=>!c.enabled?['手动停用','']:new Date(c.expiresAt).getFullYear()>2000&&Date.now()>=new Date(c.expiresAt).getTime()?['已到期','bad']:c.quotaBytes&&usage(c)>=c.quotaBytes?['额度用尽','bad']:['策略允许','good'];
const nodeAllowed=n=>n.enabled&&!(new Date(n.expiresAt).getFullYear()>2000&&Date.now()>=new Date(n.expiresAt).getTime())&&!(n.quotaBytes&&usage(n)>=n.quotaBytes);
const clientOnline=c=>Object.values(c.nodeIPStates||{}).some(v=>v.statsState==='ok'&&Date.now()-new Date(v.updatedAt).getTime()<35000&&v.onlineIPs?.length);
const ipBlocked=(c,nid)=>(c.nodeIPLimits?.[nid]??c.limitIp??0)>0&&Date.now()<new Date(c.nodeIPStates?.[nid]?.blockedUntil).getTime();
const clientDisplayStatus=c=>{const base=clientStatus(c);if(base[1]!=='good')return base;const blocked=c.nodeIds.filter(nid=>ipBlocked(c,nid)||!nodeAllowed(state.nodes.find(n=>n.id===nid)||{})).length;return blocked?[blocked===c.nodeIds.length?'全部入站受限':'部分入站受限','warn']:base;};
const localTime=value=>{const d=new Date(value);if(!(d.getFullYear()>2000))return '';return new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
const enabledField=on=>`<label class="checkbox"><input type="checkbox" name="enabled" ${on?'checked':''}>启用</label>`;
const optionalField=(label,name,value='',extra='')=>`<label>${esc(label)}<input name="${name}" value="${esc(value)}" ${extra}></label>`;
const selectField=(label,name,value,options)=>`<label>${esc(label)}<select name="${name}">${options.map(([v,text])=>`<option value="${esc(v)}" ${String(value)===String(v)?'selected':''}>${esc(text)}</option>`).join('')}</select></label>`;
const formSection=(title,content,open=false)=>`<details class="settings-section" ${open?'open':''}><summary>${esc(title)}</summary><div>${content}</div></details>`;
function trafficFields(obj,legacy=true) {
 const mode=obj?.quotaPeriodMonths?'legacy-'+obj.quotaPeriodMonths:obj?.trafficReset||'never';
 const options=Object.entries(resetLabels).map(([v,label])=>[v,v==='weekly'?'每周（周一）':label]);
 if(legacy)options.push(...[[1,'每月（从设置日起）'],[3,'每季度'],[6,'每半年'],[12,'每年']].map(([n,label])=>['legacy-'+n,label]));
 return `<div class="form-grid"><label>总流量限额（GiB，0 为不限）<input name="quota" type="number" min="0" max="8388607" step="any" value="${(obj?.quotaBytes||0)/1024**3}" required></label>${selectField('定时重置流量','trafficReset',mode,options)}<label data-reset-day ${mode==='monthly'?'':'hidden'}>每月重置日<input name="trafficResetDay" type="number" min="1" max="31" step="1" value="${obj?.trafficResetDay||1}" required></label></div><p class="hint">流量按上传＋下载计算，0 为不限。定时重置只清零已用额度，保留历史累计及到期时间。日历重置按面板时区执行，月末缺少指定日期时使用最后一天。${obj?.quotaResetsAt&&date(obj.quotaResetsAt)!=='尚无数据'?'下次重置：'+esc(date(obj.quotaResetsAt))+'。':''}</p>`;
}
function nodeFields(obj,id) {
 const basic=`<label>服务器<select name="serverId" ${id?'disabled':''}>${state.servers.map(s=>`<option value="${esc(s.id)}" ${s.id===obj?.serverId?'selected':''}>${esc(s.name)} · ${esc(s.host)}</option>`).join('')}</select></label>`+field('入站名称','name',obj?.name||'','text','maxlength="100"')+enabledField(obj?obj.enabled:true)+`<div class="form-grid">${optionalField('监听地址','listen',obj?.listen||'','placeholder="0.0.0.0"')}${field('监听端口','port',obj?.port||443,'number','min="1" max="65535"')}</div><p class="hint">${obj?.sourceNodeId?'复制此入站也会分配源入站的客户端，并复制其 IP / Flow 覆盖；各客户端的共享额度保留。':''}VLESS · TCP · REALITY。10085 为统计接口保留端口，保存后 Agent 自动同步。</p>`;
 const reality=field('SNI 域名','sni',obj?.sni||'','text','placeholder="与目标证书匹配的域名"')+field('REALITY 目标','target',obj?.target||'','text','placeholder="目标域名:443"')+optionalField('附加 SNI（逗号分隔）','serverNames',(obj?.serverNames||[]).join(', '),'placeholder="备用证书域名"')+`<label>REALITY 私钥<input name="privateKey" maxlength="64" autocomplete="off" placeholder="${id?'留空保留现有私钥':'留空自动生成'}"></label><label>公钥<input name="publicKey" value="${esc(obj?.publicKey||'')}" readonly placeholder="保存或生成密钥后显示"></label><button type="button" data-action="node-keys">生成新的 REALITY 密钥</button>${optionalField('Short ID','shortId',obj?.shortId??Array.from(crypto.getRandomValues(new Uint8Array(8)),b=>b.toString(16).padStart(2,'0')).join(''),'maxlength="16" pattern="([a-fA-F0-9]{2})*"')}<div class="form-grid">${selectField('客户端指纹','fingerprint',obj?.fingerprint||'chrome',['chrome','firefox','safari','ios','android','edge','360','qq','random','randomized'].map(v=>[v,v]))}${optionalField('SpiderX','spiderX',obj?.spiderX||'','maxlength="200" placeholder="/"')}</div><details><summary>REALITY 高级选项</summary><div class="form-grid">${optionalField('最低客户端版本','minClientVersion',obj?.minClientVersion||'','placeholder="x.y.z"')}${optionalField('最高客户端版本','maxClientVersion',obj?.maxClientVersion||'','placeholder="x.y.z"')}<label>最大时间差（毫秒，0 为不限）<input name="maxTimeDiff" type="number" min="0" max="86400000" step="1" value="${obj?.maxTimeDiff||0}" required></label></div><label class="checkbox"><input name="realityShow" type="checkbox" ${obj?.realityShow?'checked':''}>REALITY 调试输出</label></details><p class="hint">更新密钥后，需要重新导入节点分享链接或更新订阅。</p>`;
 const policy=trafficFields(obj,false)+`<label>入站到期时间（留空为不限）<input name="expires" type="datetime-local" value="${localTime(obj?.expiresAt)}"></label><p class="hint">达到入站限额或到期时间时，撤下整个入站。客户端流量策略独立管理，重置入站不会重置客户端。</p>`;
 const extra=`<label class="checkbox"><input name="sniffing" type="checkbox" ${obj?.sniffing?'checked':''}>启用嗅探（HTTP / TLS / QUIC）</label><label class="checkbox"><input name="sniffingRouteOnly" type="checkbox" ${obj?.sniffingRouteOnly?'checked':''}>嗅探仅用于路由</label><label>订阅排序（数值越小越靠前）<input name="subSortIndex" type="number" min="-100000" max="100000" step="1" value="${obj?.subSortIndex||1}" required></label><label class="checkbox"><input name="excludeFromSub" type="checkbox" ${obj?.excludeFromSub?'checked':''}>从订阅中隐藏此入站</label><p class="hint">隐藏订阅不会停止此入站运行。</p>`;
 return formSection('基本设置',basic,true)+formSection('REALITY 与分享参数',reality,true)+formSection('入站流量与到期',policy,true)+formSection('嗅探与订阅',extra);
}
function clientFields(obj,id) {
 const expiryMode=obj?.firstUseDays&&date(obj.firstUsedAt)==='尚无数据'?'first-use':'date';
 const renewalMode=obj?.resetDay?'monthly':obj?.resetWeekday?'weekly':obj?.resetDays?'interval':'disabled';
 const identity=field('客户端名称','name',obj?.name||'','text','maxlength="100"')+enabledField(obj?obj.enabled:true)+optionalField('Email / 用户标识','email',obj?.email||obj?.name||'','maxlength="100" placeholder="留空使用客户端名称"')+`<label>UUID<div class="input-action"><input name="uuid" value="${esc(obj?.uuid||crypto.randomUUID())}" required><button type="button" data-action="generate-value" data-target="uuid">重新生成</button></div></label>`+selectField('Flow','flow',obj?.flow??'xtls-rprx-vision',[['xtls-rprx-vision','xtls-rprx-vision'],['','无']])+optionalField('VLESS 反向代理标签','reverseTag',obj?.reverseTag||'','maxlength="100" placeholder="留空关闭反向代理"')+'<p class="hint">UUID 和 Flow 同时用于服务端配置与分享链接。反向代理还需客户端的反向连接及服务端路由配合。</p>';
 const expiry=selectField('有效期方式','expiryMode',expiryMode,[['date','固定到期时间 / 不限'],['first-use','从首次使用开始计时']])+`<label data-expiry-date ${expiryMode==='date'?'':'hidden'}>到期时间（本机时区，留空为不限）<input name="expires" type="datetime-local" value="${localTime(obj?.expiresAt)}"></label><label data-first-use ${expiryMode==='first-use'?'':'hidden'}>首次使用后有效天数<input name="firstUseDays" type="number" min="1" max="3650" step="1" value="${obj?.firstUseDays||30}" required></label>${obj?.firstUsedAt&&date(obj.firstUsedAt)!=='尚无数据'?'<p class="hint">首次使用：'+esc(date(obj.firstUsedAt))+'；已激活客户端请修改固定到期时间。</p>':''}`+selectField('自动续期','renewalMode',renewalMode,[['disabled','关闭'],['interval','固定天数'],['weekly','每周指定星期'],['monthly','每月指定日期']])+`<div class="form-grid"><label data-renewal="interval" ${renewalMode==='interval'?'':'hidden'}>续期间隔（天）<input name="resetDays" type="number" min="1" max="3650" step="1" value="${obj?.resetDays||30}" required></label><label data-renewal="monthly" ${renewalMode==='monthly'?'':'hidden'}>每月续期日<input name="resetDay" type="number" min="1" max="31" step="1" value="${obj?.resetDay||1}" required></label><div data-renewal="weekly" ${renewalMode==='weekly'?'':'hidden'}>${selectField('续期星期','resetWeekday',obj?.resetWeekday||1,[[1,'星期一'],[2,'星期二'],[3,'星期三'],[4,'星期四'],[5,'星期五'],[6,'星期六'],[7,'星期日']])}</div><label>最多自动续期次数（0 为不限）<input name="resetMax" type="number" min="0" max="1000000" step="1" value="${obj?.resetMax||0}" required></label></div><p class="hint">自动续期在到期时延长有效期并重置已用流量；需先设置到期时间或首次使用有效期。定时清零与自动续期相互独立。已续期 ${obj?.renewalCount||0} 次；更改续期规则或次数上限重新计数，手动停用不会自动启用。</p><div class="actions"><button type="button" data-action="renewal-preview">预览续期</button><button type="button" data-action="renewal-initial">设置首次到期时间</button></div><p class="hint" id="renewal-preview-result"></p>`;
 const subscription=`<label>订阅标识<div class="input-action"><input name="subscriptionId" value="${esc(obj?.token||'')}" minlength="8" maxlength="128" pattern="[A-Za-z0-9_-]+" placeholder="留空自动生成"><button type="button" data-action="generate-value" data-target="subscriptionId">重新生成</button></div></label><p class="hint">修改订阅标识后，旧订阅地址立即失效。相同标识可合并多个客户端到一个订阅，支持 8–128 位字母、数字、下划线或连字符。</p><div class="form-grid">${optionalField('Telegram ID','telegramId',obj?.telegramId||'','inputmode="numeric" pattern="[0-9]+" placeholder="关联用户标识"')}${optionalField('分组','group',obj?.group||'','maxlength="100" list="client-groups"')}</div><datalist id="client-groups">${[...new Set(state.clients.map(c=>c.group).filter(Boolean))].map(g=>`<option value="${esc(g)}">`).join('')}</datalist><p class="hint">Telegram ID 作为关联信息保存；本面板尚未接入 Telegram Bot 通知。</p><label>备注<textarea name="comment" rows="3" maxlength="2000">${esc(obj?.comment||'')}</textarea></label><label>外部节点分享链接（每行一条）<textarea name="externalLinks" rows="4" placeholder="vless://…&#10;trojan://…">${esc((obj?.externalLinks||[]).join('\n'))}</textarea></label><p class="hint">外部分享链接附加到此用户订阅中，流量由外部节点管理。</p>`;
 const assignments=`<label>默认 IP 上限（每个入站独立计数，0 为不限）<input name="limitIp" type="number" min="0" max="128" step="1" value="${obj?.limitIp||0}" required></label><div class="assignment-heading"><label>分配入站</label><div>${btn('select-all','','全选')}${btn('select-none','','清空')}</div></div><div class="node-options">${state.nodes.length?state.nodes.map(n=>`<div class="binding-option"><label class="checkbox"><input type="checkbox" name="nodeIds" value="${esc(n.id)}" ${obj?.nodeIds?.includes(n.id)?'checked':''}><span>${esc(n.name)}<small>${esc(serverName(n.serverId))}${n.enabled?'':' · 已停用'}</small></span></label><label class="ip-limit">限制 IP 数<input name="ipLimit-${esc(n.id)}" type="number" min="0" max="128" step="1" value="${obj?.nodeIPLimits?.[n.id]??''}" placeholder="继承默认" aria-label="${esc(n.name)} 限制 IP 数"></label><label class="ip-limit">Flow 覆盖<select name="nodeFlow-${esc(n.id)}"><option value="inherit" ${obj?.nodeFlows?.[n.id]===undefined?'selected':''}>继承默认</option><option value="xtls-rprx-vision" ${obj?.nodeFlows?.[n.id]==='xtls-rprx-vision'?'selected':''}>Vision</option><option value="none" ${obj?.nodeFlows?.[n.id]===''?'selected':''}>无</option></select></label></div>`).join(''):'<p class="hint">暂无入站，可稍后编辑分配。</p>'}</div><p class="hint">流量额度跨所有入站共享；IP 上限每个入站独立计数，0 为不限。IP 超限暂停该用户在对应入站的访问约 60 秒，再自动恢复，需等待 Agent 同步。</p>`;
 return formSection('身份与连接',identity,true)+formSection('流量限制与重置',trafficFields(obj),true)+formSection('到期与自动续期',expiry,true)+formSection('订阅与用户信息',subscription)+formSection('入站分配与 IP 限制',assignments,true);
}
function edit(kind,id,seedNode,seedObject) {
  let obj=id?(kind==='server'?state.servers:kind==='node'?state.nodes:state.clients).find(v=>v.id===id):null;
  if(seedObject)obj=seedObject;
  if (id&&!obj) return;
  if(kind==='client'&&!id&&seedNode)obj={enabled:true,nodeIds:[seedNode],nodeIPLimits:{}};
  if (kind==='node'&&!state.servers.length) {toast('请先添加服务器'); return;}
  editor={kind,id,sourceNodeId:seedObject?.sourceNodeId}; $('#form-error').textContent='';
  $('#info').close();
  $('#dialog-title').textContent=(id?'编辑':'添加')+({server:'服务器',node:'入站',client:'客户端'}[kind]);
  let fields='';
  if (kind==='server') fields=field('服务器名称','name',obj?.name||'','text','maxlength="100" placeholder="例如：香港 · 01"')+field('公网 IP / 域名','host',obj?.host||'','text','placeholder="用于客户端连接，不含协议和端口"')+'<p class="hint">添加后生成专属安装命令。注册令牌有效期 30 分钟，仅可使用一次。</p>';
  if(kind==='node')fields=nodeFields(obj,id);
  if (kind==='client') fields=clientFields(obj,id);
  $('#editor').classList.toggle('wide',kind==='client'||kind==='node');
  $('#fields').innerHTML=fields; updateSettingInputs(); $('#editor').showModal();
}
function info(title,html) {$('#info-title').textContent=title; $('#info-body').innerHTML=html; $('#info').showModal();}
function registration(data) {
  const automatic=Boolean(data.installCommand);
  const command=data.installCommand||`VIBEXUI_REGISTRATION_TOKEN=${quote(data.token)} ./vibexui-agent -panel ${quote(data.panel)} -server-id ${quote(data.id)} -xray /usr/local/bin/xray`;
  info('连接服务器',`<p>${automatic?'在对应 Debian / Ubuntu VPS 执行，自动安装 Agent、Xray 并配置开机启动：':'本机调试：先放置 Agent 和 Xray-core，再执行启动命令。公网 HTTPS 面板会生成一键安装命令。'}</p><pre>${esc(command)}</pre><button data-action="copy" data-copy="${esc(command)}">复制${automatic?'安装':'启动'}命令</button><p class="hint">令牌有效期 30 分钟，仅可使用一次。${automatic?'主面板需保存对应架构的 Agent 发行文件。已有 Agent 的 VPS 请按 README 重新注册。':'凭据保存到 data/agent。'}成功连接后，回到服务器列表查看在线状态。</p>`);
}
function diagnostics(s) {
  const messages=[];
  if (s.registrationState==='pending') messages.push('等待 Agent 注册：复制安装命令到对应 VPS 执行，30 分钟内完成注册。');
  if (s.registrationState==='expired') messages.push('注册令牌已过期：在“更多”中重新生成注册令牌。');
  if (s.registrationState==='registered'&&!online(s)) messages.push('Agent 未及时上报：检查 VPS 到主面板的 HTTPS 连接，以及 Agent 的 systemd 日志。');
  if (s.error) messages.push('配置未成功应用：'+s.error);
  if (s.statsError) messages.push('流量统计异常：'+s.statsError);
  if(s.ipStatsError&&state.nodes.some(n=>n.serverId===s.id&&state.clients.some(c=>c.nodeIPLimits?.[n.id]>0)))messages.push('在线 IP 采集异常：'+s.ipStatsError);
  if (!s.statsEpoch&&s.registrationState==='registered') messages.push('尚未收到按用户统计：升级 Agent 后会开始采集，历史用户流量无法追溯。');
  if (online(s)&&!s.running&&!s.desiredRunning) messages.push('Xray 已按面板指令停止，可点击“启动”恢复。');
  if (online(s)&&s.version!==s.appliedVersion&&!s.error) messages.push('配置正在等待同步，通常在下一次 Agent 轮询时应用。');
  return messages;
}
function serverDetails(s) {
  const messages=diagnostics(s);
  info(s.name,`<div class="detail-status">${badge(...connection(s))} ${badge(s.running?'Xray 运行中':'Xray 已停止',online(s)&&s.running?'good':'')}</div><dl class="detail-grid"><dt>连接地址</dt><dd>${esc(s.host)}</dd><dt>配置版本</dt><dd>已应用 v${s.appliedVersion} / 期望 v${s.version}</dd><dt>最近上报</dt><dd>${esc(date(s.lastSeen))}</dd><dt>Xray 版本</dt><dd>${esc(s.xrayVersion||'尚未上报')}</dd><dt>累计流量</dt><dd>↑ ${bytes(s.upload)}　↓ ${bytes(s.download)}</dd><dt>用户流量采集</dt><dd>${esc(s.statsEpoch?date(s.statsUpdatedAt):'尚未收到')}</dd></dl>${messages.length?`<div class="diagnostics">${messages.map(v=>`<p>${esc(v)}</p>`).join('')}</div>`:'<div class="notice">服务器在线，配置已同步。</div>'}<p class="hint">在 VPS 上查看 Agent 日志：</p><pre>journalctl -u vibexui-agent -n 50 --no-pager</pre>${btn('copy','','复制排障命令').replace('data-id=""','data-copy="journalctl -u vibexui-agent -n 50 --no-pager"')}`);
}
function clientTraffic(c) {
  const rows=Object.entries(c.serverTraffic||{}).map(([sid,t])=>`<tr><td>${esc(serverName(sid))}</td><td>${bytes(t.upload)}</td><td>${bytes(t.download)}</td><td>${bytes(total(t))}</td></tr>`).join('');
  info(c.name+' · 流量',`<div class="traffic-summary"><strong>${bytes(total(c))}</strong><span>累计上传 ${bytes(c.upload)} · 下载 ${bytes(c.download)}</span></div><p class="hint">最近采集：${esc(date(c.trafficUpdatedAt))}。此数据累计了该用户在所有服务器上的流量。</p>${panel('按服务器累计',Object.keys(c.serverTraffic||{}).length,['服务器','上传','下载','合计'],rows)}<p class="hint">无数据时请确认用户已产生代理流量，且 Agent 已升级并在线。额度已用 ${bytes(usage(c))} / ${c.quotaBytes?bytes(c.quotaBytes):'不限'}；下次自动重置：${(c.quotaPeriodMonths||c.trafficReset&&c.trafficReset!=='never')?esc(date(c.quotaResetsAt)):'不自动重置'}；到期：${new Date(c.expiresAt).getFullYear()>2000?esc(date(c.expiresAt)):'不限'}。</p><p>${badge(...clientDisplayStatus(c))} ${btn('client-reset',c.id,'重置已用额度')}</p><p class="hint">重置保留历史累计及到期时间。晚到的上报计入新额度，离线 VPS 恢复后才能执行停用。</p>`);
}
function nodeTraffic(n){
 const server=state.servers.find(s=>s.id===n.serverId);
 const reported=server?.nodeTraffic&&Object.hasOwn(server.nodeTraffic,n.id);
 info(n.name+' · 入站流量',`<div class="traffic-summary"><strong>${bytes(usage(n))} / ${n.quotaBytes?bytes(n.quotaBytes):'不限'}</strong><span>本期上传 ${bytes(Math.max(0,(n.upload||0)-(n.quotaBaseline?.upload||0)))} · 下载 ${bytes(Math.max(0,(n.download||0)-(n.quotaBaseline?.download||0)))}</span></div><dl class="detail-grid"><dt>累计流量</dt><dd>↑ ${bytes(n.upload)}　↓ ${bytes(n.download)}</dd><dt>最近采集</dt><dd>${esc(date(n.trafficUpdatedAt))}</dd><dt>重置规则</dt><dd>${esc(quotaPeriod(n))}</dd><dt>下次重置</dt><dd>${n.trafficReset&&n.trafficReset!=='never'?esc(date(n.quotaResetsAt)):'不自动重置'}</dd><dt>到期时间</dt><dd>${date(n.expiresAt)==='尚无数据'?'不限':esc(date(n.expiresAt))}</dd></dl>${!reported?'<p class="hint">尚未收到入站流量，请升级此服务器 Agent。旧 Agent 只提供服务器与客户端流量，不能据此追溯入站历史。</p>':''}<p>${badge(...nodeStatus(n))} ${btn('node-reset',n.id,'重置入站流量')}</p><p class="hint">重置入站额度保留历史累计，客户端额度独立保持。配置限制需等待 Agent 同步。</p>`);
}
function nodeClients(n) {
 const clients=state.clients.filter(c=>c.nodeIds.includes(n.id));
 const rows=clients.map(c=>`<tr><td><strong>${esc(c.name)}</strong><small>${esc(c.email||c.name)}${c.group?' · '+esc(c.group):''}</small><small>${esc(c.uuid)}</small></td><td>${quotaSummary(c)}</td><td>${(c.nodeIPLimits?.[n.id]??c.limitIp??0)||'不限'}</td><td>${!nodeAllowed(n)?badge('入站已受限','bad'):ipBlocked(c,n.id)?badge('IP 超限暂停','warn'):badge(...clientStatus(c))}</td><td><div class="actions">${btn('client-edit',c.id,'客户端设置')}${btn('client-traffic',c.id,'流量详情')}${btn('client-reset',c.id,'重置流量')}</div></td></tr>`).join('');
 info(n.name+' · 客户端',`${panel('该入站的客户端',clients.length,['客户端','周期流量 / 限额','限制 IP 数','客户端策略','操作'],rows)}<p class="hint">客户端设置中可配置周期流量限额、自动重置周期及此入站的限制 IP 数，0 为不限。流量额度由该客户端的所有入站共享，重置保留历史累计。</p><button class="primary" data-action="client-add" data-node-id="${esc(n.id)}">＋ 添加客户端</button> <button data-action="client-batch" data-node-id="${esc(n.id)}">批量添加</button> ${clients.length?`<button data-action="node-reset-clients" data-id="${esc(n.id)}">重置所有客户端流量</button>`:''}`);
}
function clientIPs(c) {
 const labels={ok:'已采集',pending:'等待采集 / 升级 Agent',stale:'数据已过期',error:'采集异常',unlimited:'未限制'};
 const rows=c.nodeIds.map(nid=>{
  const n=state.nodes.find(n=>n.id===nid),value=c.nodeIPStates?.[nid],limit=c.nodeIPLimits?.[nid]??c.limitIp??0;
  const blocked=ipBlocked(c,nid),ips=value?.onlineIPs||[];
  const collected=value?.statsState==='ok';
  return `<tr><td><strong>${esc(n?.name||'已移除入站')}</strong><small>${esc(serverName(n?.serverId))}</small></td><td>${limit||'不限'}</td><td>${collected?ips.length>=129?'至少 129':ips.length:'未知'}<small>${esc(labels[value?.statsState]||labels.pending)}</small></td><td>${blocked?badge('暂停至 '+date(value.blockedUntil),'warn'):badge('未因 IP 停用','good')}</td></tr>`;
 }).join('');
 const details=c.nodeIds.map(nid=>{const value=c.nodeIPStates?.[nid];return `<p><strong>${esc(state.nodes.find(n=>n.id===nid)?.name||nid)}</strong> · ${esc(date(value?.updatedAt))}</p><pre>${esc(value?.onlineIPs?.join('\n')||'无有效在线 IP 样本')}</pre>`;}).join('');
 info(c.name+' · 入站 IP 限制',`${panel('各入站独立限制',c.nodeIds.length,['入站','上限','在线 IP','IP 策略'],rows)}<p class="hint">按该用户在该入站的在线连接来源 IP 去重。未知不等于 0，采集失败不触发新的超限停用。面板或 Agent 离线时无法及时执行限制。编辑用户可修改各入站上限。</p>${details}${btn('client-clear-ips',c.id,'清除 IP 记录与暂停')}<p class="hint">现有连接会在 Agent 下次采集时重新出现。</p>`);
}
function downloadJSON(data,name){const url=URL.createObjectURL(new Blob([JSON.stringify(data,null,2)],{type:'application/json'}));const link=document.createElement('a');link.href=url;link.download=name;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
function importDialog(){info('导入入站配置',`<form id="import-form"><label>目标服务器<select name="serverId">${state.servers.map(s=>`<option value="${esc(s.id)}">${esc(s.name)}</option>`).join('')}</select></label><label>选择 VibeXUI 入站导出文件<input name="file" type="file" accept="application/json,.json" required></label><label>监听端口（可修改，不能与目标服务器其他入站冲突）<input name="port" type="number" min="1" max="65535" step="1" placeholder="留空使用文件中的端口"></label><p class="hint">导入入站及客户端设置，历史流量从零开始。相同 UUID 的现有用户会分配到此入站，其原有设置保留。</p><p class="error" data-bulk-error></p><button type="submit" class="primary">导入</button></form>`);}
function exportTraffic() {
  const cell=v=>{let text=String(v??''); if (/^[=+\-@\t\r]/.test(text)) text="'"+text; return '"'+text.replaceAll('"','""')+'"';};
  const rows=[['用户','状态','上传字节','下载字节','合计字节','最近采集'],...visible('clients').map(c=>[c.name,c.enabled?'启用':'停用',c.upload||0,c.download||0,total(c),date(c.trafficUpdatedAt)])];
  const blob=new Blob(['\uFEFF'+rows.map(row=>row.map(cell).join(',')).join('\r\n')],{type:'text/csv;charset=utf-8'});
  const url=URL.createObjectURL(blob),link=document.createElement('a'); link.href=url; link.download='vibexui-user-traffic.csv'; link.click(); setTimeout(()=>URL.revokeObjectURL(url),1000);
}
$('#editor-form').addEventListener('submit',async event=>{
  event.preventDefault(); const form=event.currentTarget,fd=new FormData(form),{kind,id}=editor;
  const data={name:fd.get('name')};
  if (kind==='server') data.host=fd.get('host');
  if (kind==='node') Object.assign(data,{sourceNodeId:editor.sourceNodeId||'',serverId:id?state.nodes.find(n=>n.id===id).serverId:fd.get('serverId'),port:Number(fd.get('port')),sni:fd.get('sni'),target:fd.get('target'),listen:fd.get('listen'),privateKey:fd.get('privateKey'),shortId:fd.get('shortId'),serverNames:String(fd.get('serverNames')||'').split(',').map(v=>v.trim()).filter(Boolean),fingerprint:fd.get('fingerprint'),spiderX:fd.get('spiderX'),minClientVersion:fd.get('minClientVersion'),maxClientVersion:fd.get('maxClientVersion'),maxTimeDiff:Number(fd.get('maxTimeDiff')),realityShow:fd.has('realityShow'),sniffing:fd.has('sniffing'),sniffingRouteOnly:fd.has('sniffingRouteOnly'),subSortIndex:Number(fd.get('subSortIndex')),excludeFromSub:fd.has('excludeFromSub'),enabled:fd.has('enabled'),expiresAt:fd.get('expires')?new Date(fd.get('expires')).toISOString():'0001-01-01T00:00:00Z'});
  if(kind==='node'||kind==='client') {
    const mode=fd.get('trafficReset');
    Object.assign(data,{quotaBytes:Math.round(Number(fd.get('quota'))*1024**3),trafficResetDay:Number(fd.get('trafficResetDay')||1)});
    if(String(mode).startsWith('legacy-'))data.quotaPeriodMonths=Number(mode.slice(7));
    else Object.assign(data,{trafficReset:mode,...(kind==='client'?{quotaPeriodMonths:0}:{})});
  }
  if(kind==='client') {
    const renewal=fd.get('renewalMode'),firstUse=fd.get('expiryMode')==='first-use';
    Object.assign(data,{enabled:fd.has('enabled'),uuid:fd.get('uuid'),email:fd.get('email'),flow:fd.get('flow'),reverseTag:fd.get('reverseTag'),subscriptionId:fd.get('subscriptionId'),telegramId:fd.get('telegramId'),group:fd.get('group'),comment:fd.get('comment'),externalLinks:String(fd.get('externalLinks')||'').split(/\r?\n/).filter(v=>v.trim()),nodeIds:fd.getAll('nodeIds'),limitIp:Number(fd.get('limitIp')),nodeFlows:Object.fromEntries(fd.getAll('nodeIds').filter(nid=>fd.get('nodeFlow-'+nid)!=='inherit').map(nid=>[nid,fd.get('nodeFlow-'+nid)==='none'?'':fd.get('nodeFlow-'+nid)])),nodeIPLimits:Object.fromEntries(fd.getAll('nodeIds').filter(nid=>fd.get('ipLimit-'+nid)!=='').map(nid=>[nid,Number(fd.get('ipLimit-'+nid))])),firstUseDays:firstUse?Number(fd.get('firstUseDays')):0,expiresAt:!firstUse&&fd.get('expires')?new Date(fd.get('expires')).toISOString():'0001-01-01T00:00:00Z',resetDays:renewal==='interval'?Number(fd.get('resetDays')):0,resetDay:renewal==='monthly'?Number(fd.get('resetDay')):0,resetWeekday:renewal==='weekly'?Number(fd.get('resetWeekday')):0,resetMax:Number(fd.get('resetMax'))});
  }
  const submit=form.querySelector('[type=submit]'); submit.disabled=true;
  try {
    const result=await api('/api/'+({server:'servers',node:'nodes',client:'clients'}[kind])+(id?'/'+id:''),id?'PATCH':'POST',data);
    $('#editor').close(); await refresh(); toast(id?'已保存，配置将自动同步':'已创建');
    if (kind==='server'&&!id) registration(result);
  } catch(error) {$('#form-error').textContent=error.message;}
  finally {submit.disabled=false;}
});
function updateSettingInputs(){document.querySelectorAll('#fields [hidden] input,#fields [hidden] select').forEach(el=>el.disabled=true);document.querySelectorAll('#fields input,#fields select').forEach(el=>{if(!el.closest('[hidden]')&&!el.name.startsWith('serverId'))el.disabled=false;});}
$('#fields').addEventListener('change',event=>{
  if(event.target.name==='trafficReset')$('[data-reset-day]').hidden=event.target.value!=='monthly';
  if(event.target.name==='expiryMode') { $('[data-first-use]').hidden=event.target.value!=='first-use'; $('[data-expiry-date]').hidden=event.target.value==='first-use'; }
  if(event.target.name==='renewalMode')document.querySelectorAll('[data-renewal]').forEach(el=>el.hidden=el.dataset.renewal!==event.target.value);
  if (event.target.name==='sni') {const target=$('#fields input[name=target]'); if (target&&!target.value) target.value=event.target.value+':443';}
  updateSettingInputs();
});
$('#login-form').addEventListener('submit',async event=>{
  event.preventDefault(); const data=new FormData(event.currentTarget),button=event.currentTarget.querySelector('button'); button.disabled=true; $('#login-error').textContent='';
  try {await api('/api/login','POST',{username:data.get('username'),password:data.get('password')}); await enter();}
  catch(error) {$('#login-error').textContent=error.message;}
  finally {button.disabled=false;}
});
$('#logout').addEventListener('click',async()=>{try{await api('/api/logout','POST',{}); showLogin();}catch(error){toast(error.message);}});
$('#add-button').addEventListener('click',()=>edit(page==='clients'?'client':page==='nodes'?'node':'server'));
$('#import-button').addEventListener('click',()=>{if(!state.servers.length){toast('请先添加目标服务器');return;}importDialog();});
$('#cleanup-button').addEventListener('click',async()=>{const clients=state.clients.filter(c=>!c.resetDays&&!c.resetDay&&!c.resetWeekday&&(clientStatus(c)[0]==='已到期'||clientStatus(c)[0]==='额度用尽'));if(!clients.length){toast('没有可清理的到期或额度用尽客户端');return;}if(!confirm('删除 '+clients.length+' 个已到期或额度用尽且未设置自动续期的客户端？'))return;await api('/api/clients/bulk','POST',{action:'delete',ids:clients.map(c=>c.id)});await refresh();toast('已清理耗尽客户端');});
$('#batch-button').addEventListener('click',()=>batchDialog());
$('#group-filter').addEventListener('change',render);
$('#refresh-button').addEventListener('click',()=>refresh().catch(error=>toast(error.message)));
$('#search').addEventListener('input',render);
$('#filter').addEventListener('change',render);
$('#export-button').addEventListener('click',exportTraffic);
document.addEventListener('change',event=>{
 if(event.target.dataset.select){if(event.target.checked)selectedIDs.add(event.target.dataset.select);else selectedIDs.delete(event.target.dataset.select);render();}
 if(event.target.hasAttribute('data-select-all')){for(const item of visible()){if(event.target.checked)selectedIDs.add(item.id);else selectedIDs.delete(item.id);}render();}
});
$('#info-body').addEventListener('submit',async event=>{
 const form=event.target;if(form.id!=='bulk-form'&&form.id!=='batch-form'&&form.id!=='import-form')return;event.preventDefault();
 const fd=new FormData(form),submit=form.querySelector('[type=submit]');submit.disabled=true;
 try{
  if(form.id==='import-form'){const file=fd.get('file');if(file.size>1024*1024)throw Error('配置文件不能超过 1 MiB');const data=JSON.parse(await file.text());data.serverId=fd.get('serverId');if(fd.get('port'))data.node.port=Number(fd.get('port'));await api('/api/nodes/import','POST',data);$('#info').close();await refresh();toast('入站及客户端设置已导入');}
  else if(form.id==='batch-form'){const data={prefix:fd.get('prefix'),count:Number(fd.get('count')),templateId:fd.get('templateId')};if(fd.has('useNodes'))data.nodeIds=fd.getAll('nodeIds');await api('/api/clients/batch','POST',data);$('#info').close();await refresh();toast('批量创建完成');}
  else{const action=form.dataset.operation,values={};if(action==='add-days')values.days=Number(fd.get('days'));if(action==='add-bytes')values.bytes=Math.round(Number(fd.get('quota'))*1024**3);if(action==='group')values.group=fd.get('group');if(action==='flow')values.flow=fd.get('flow');if(action==='attach'||action==='detach')values.nodeIds=fd.getAll('nodeIds');await runBulk(action,values);}
 }catch(error){form.querySelector('[data-bulk-error]').textContent=error.message;}finally{submit.disabled=false;}
});
document.addEventListener('click',async event=>{
  const close=event.target.closest('[data-close]'); if (close) {close.closest('dialog').close(); return;}
  const nav=event.target.closest('[data-page]'); if (nav) {navigate(nav.dataset.page); return;}
  const button=event.target.closest('[data-action]'); if (!button) return;
  const {action,id}=button.dataset;
  try {
    if(action==='renewal-preview'||action==='renewal-initial'){
     const fd=new FormData($('#editor-form')),mode=fd.get('renewalMode');if(mode==='disabled'){toast('请先选择自动续期模式');return;}
     button.disabled=true;
     const data={clientId:editor.id||'',expiresAt:action==='renewal-initial'?'0001-01-01T00:00:00Z':fd.get('expires')?new Date(fd.get('expires')).toISOString():'0001-01-01T00:00:00Z',resetDays:mode==='interval'?Number(fd.get('resetDays')):0,resetDay:mode==='monthly'?Number(fd.get('resetDay')):0,resetWeekday:mode==='weekly'?Number(fd.get('resetWeekday')):0,resetMax:Number(fd.get('resetMax'))};
     const preview=await api('/api/clients/renewal-preview','POST',data);
     if(action==='renewal-initial'){ $('#fields select[name=expiryMode]').value='date';$('[data-first-use]').hidden=true;$('[data-expiry-date]').hidden=false;updateSettingInputs();$('#fields input[name=expires]').value=localTime(preview.suggestedExpiresAt);$('#renewal-preview-result').textContent='已填入首次到期时间，点击保存生效。面板时区：'+preview.timezone;}
     else $('#renewal-preview-result').textContent=preview.nextExpiresAt?'本次有效至 '+date(preview.lastValidAt)+'；续期后到期 '+date(preview.nextExpiresAt)+'；需要 '+preview.renewalsNeeded+' 次续期，'+(preview.canRenew?'当前规则允许。':'当前停用状态或次数上限不允许。')+' 面板时区：'+preview.timezone:'请先设置到期时间；首次使用计时的用户在激活后才有截止时间。';return;
    }
    if(action==='node-traffic'){nodeTraffic(state.nodes.find(n=>n.id===id));return;}
    if(action==='node-export'){const data=await api(`/api/nodes/${id}/export`);downloadJSON(data,'vibexui-inbound-'+id+'.json');return;}
    if(action==='client-clear-ips'){await api(`/api/clients/${id}/clear-ips`,'POST',{});$('#info').close();await refresh();toast('已清除 IP 记录和暂停状态，后续连接会重新采集');return;}
    if(action==='node-keys'){button.disabled=true;const keys=await api('/api/nodes/keys','POST',{});for(const name of ['privateKey','publicKey','shortId'])$('#fields input[name='+name+']').value=keys[name];return;}
    if(action.startsWith('bulk-')){const operation=action.slice(5);if(['enable','disable','reset','delete'].includes(operation)){if(['delete','reset'].includes(operation)&&!confirm((operation==='delete'?'删除':'重置流量：')+'所选 '+selectedIDs.size+' 项？'))return;button.disabled=true;await runBulk(operation);}else bulkDialog(operation);return;}
    if(action==='client-batch'){batchDialog(button.dataset.nodeId);return;}
    if(action==='client-clone'||action==='node-clone'){
     const kind=action.split('-')[0],original=(kind==='node'?state.nodes:state.clients).find(v=>v.id===id),copy=structuredClone(original);
     copy.name+=' 副本';copy.upload=0;copy.download=0;copy.quotaBaseline={};copy.trafficUpdatedAt='';copy.quotaResetsAt='';
     if(kind==='client'){copy.uuid=crypto.randomUUID();copy.email=copy.name;copy.token='';copy.firstUsedAt='';if(copy.firstUseDays)copy.expiresAt='';copy.renewalCount=0;copy.telegramId='';}
     else{copy.sourceNodeId=original.id;copy.publicKey='';let port=copy.port+1;while(port<65536&&(port===10085||state.nodes.some(n=>n.serverId===copy.serverId&&n.port===port)))port++;copy.port=Math.min(port,65535);}
     edit(kind,undefined,undefined,copy);return;
    }
    if(action==='generate-value') { const input=$('#fields input[name='+button.dataset.target+']'); input.value=button.dataset.target==='uuid'?crypto.randomUUID():Array.from(crypto.getRandomValues(new Uint8Array(24)),b=>b.toString(16).padStart(2,'0')).join(''); return; }
    if(action==='node-reset-clients'){const ids=state.clients.filter(c=>c.nodeIds.includes(id)).map(c=>c.id);if(!confirm('重置此入站的所有客户端已用额度？各客户端额度由全部入站共享，历史累计保留。'))return;await api('/api/clients/bulk','POST',{action:'reset',ids});$('#info').close();await refresh();toast('客户端流量已重置');return;}
    if(action==='node-reset'){if(!confirm('重置此入站已用流量？客户端额度、历史累计及到期时间保留。'))return;await api(`/api/nodes/${id}/reset-quota`,'POST',{});$('#info').close();await refresh();toast('已重置入站流量，等待同步');return;}
    if (action==='copy') {try{await navigator.clipboard.writeText(button.dataset.copy); toast('已复制');}catch{toast('请手动选择文本复制');}return;}
    if (action==='select-all'||action==='select-none') {document.querySelectorAll('#fields input[name=nodeIds]').forEach(input=>input.checked=action==='select-all'); return;}
    if (action==='show-attention') {navigate('servers'); $('#filter').value='attention'; render(); return;}
    const [kind,verb]=action.split('-');
    if (verb==='add'||verb==='edit') {edit(kind,id,button.dataset.nodeId); return;}
    if(action==='node-clients'){nodeClients(state.nodes.find(n=>n.id===id));return;}
    if (action==='server-details') {serverDetails(state.servers.find(s=>s.id===id)); return;}
    if(action==='client-reset'){if(!confirm('重置已用额度？历史流量及到期时间保留。'))return;await api(`/api/clients/${id}/reset-quota`,'POST',{});$('#info').close();await refresh();toast('已重置额度，等待同步');return;}
    if(action==='client-ips'){clientIPs(state.clients.find(c=>c.id===id));return;}
    if (action==='client-traffic') {clientTraffic(state.clients.find(c=>c.id===id)); return;}
    if (action==='server-more') {
      const s=state.servers.find(v=>v.id===id);
      info(s.name,`<p class="hint">操作在 Agent 下次轮询时执行。</p><div class="actions">${btn('server-edit',id,'编辑服务器')}${btn('server-restart',id,'重启 Xray')}${btn('server-registration',id,'重新注册 Agent')}</div><div class="notice">移除服务器会撤销凭据并移除其入站；VPS 上的 Xray 会继续运行，请先停止服务或卸载 Agent。</div><div class="dialog-actions">${btn('server-delete',id,'移除服务器',true)}</div>`); return;
    }
    if (action==='server-registration') {
      if (!confirm('重新注册会立即撤销当前 Agent 凭据，是否继续？')) return;
      $('#info').close(); registration(await api(`/api/servers/${id}/registration`,'POST',{})); await refresh(); return;
    }
    if (action==='client-share') {
      const data=await api(`/api/clients/${id}/links`),client=state.clients.find(c=>c.id===id);
      info(client.name+' · 订阅',`${clientStatus(client)[1]!=='good'?'<div class="notice">用户策略当前不允许访问订阅，请调整额度、到期或启用状态。</div>':''}<p class="hint">导入支持 VLESS + REALITY 的客户端。二维码包含订阅地址。</p><div class="share-url">${esc(data.subscription)}</div><button data-action="copy" data-copy="${esc(data.subscription)}">复制订阅地址</button><img class="qr" src="/api/clients/${esc(id)}/qr" alt="订阅二维码"><p class="hint">${data.links.length} 个当前可分享入站（IP 超限的入站在暂停期间隐藏）</p>${data.links.map(link=>`<pre>${esc(link)}</pre><button data-action="copy" data-copy="${esc(link)}">复制节点链接</button>`).join('')}`); return;
    }
    button.disabled=true;
    if (verb==='delete') {
      if (!confirm(kind==='server'?'撤销 Agent 凭据并移除其入站，历史用户流量保留。请先停止 VPS 服务。确定移除？':'确定删除？配置变更会自动下发。')) return;
      await api(`/api/${kind==='server'?'servers':kind==='node'?'nodes':'clients'}/${id}`,'DELETE'); $('#info').close();
    } else if (kind==='server') {await api(`/api/servers/${id}`,'PATCH',{action:verb}); $('#info').close();}
    else if (verb==='toggle') {
      const value=(kind==='node'?state.nodes:state.clients).find(v=>v.id===id);
      const data=kind==='node'?{serverId:value.serverId,name:value.name,port:value.port,sni:value.sni,target:value.target,enabled:!value.enabled}:{name:value.name,enabled:!value.enabled,nodeIds:value.nodeIds};
      await api(`/api/${kind==='node'?'nodes':'clients'}/${id}`,'PATCH',data);
    }
    await refresh(); toast('操作已保存，等待 Agent 同步');
  } catch(error) {toast(error.message);}
  finally {button.disabled=false;}
});
enter().catch(error=>{showLogin(); if(error.message!=='请先登录') $('#login-error').textContent=error.message;});


function telegramStatus(data) {
  const el=$('#telegram-status');if(!el)return;
  const status=data.status;
  el.textContent=`最近尝试：${date(status.lastAttempt)} · 最近成功：${date(status.lastSuccess)}${status.lastError?' · '+status.lastError:''}`;
}
async function refreshTelegramStatus() {
  const data=await api('/api/notifications/telegram');
  if(page==='notifications')telegramStatus(data);
}
async function loadTelegramSettings() {
  $('#workspace').innerHTML='<div class="notice">正在读取通知设置…</div>';
  try {
    const data=await api('/api/notifications/telegram');if(page!=='notifications')return;
    const c=data.settings;
    const check=(name,label)=>`<label class="checkbox"><input type="checkbox" name="${name}" ${c[name]?'checked':''}><span>${label}</span></label>`;
    $('#workspace').innerHTML=`<form id="telegram-form" class="panel notification-panel">
      <div class="panel-title"><h2>Telegram Bot</h2><span>仅向下方指定的接收者发送通知</span></div>
      <div class="notification-body">
        ${check('enabled','启用自动通知')}
        <div class="notification-grid">
          <label>Bot Token<input name="token" type="password" autocomplete="new-password" maxlength="121" placeholder="${data.hasToken?'已保存，留空保留现有 Token':'填写 BotFather 提供的 Token'}"><small>Token 保存后不回显。</small></label>
          <label>接收 Chat ID<input name="chatIds" value="${esc(c.chatIds.join(', '))}" placeholder="123456789, -1001234567890"><small>最多 10 个，以逗号分隔；支持个人和群组。</small></label>
        </div>
        <label class="checkbox"><input type="checkbox" name="clearToken"><span>删除已保存的 Token（需关闭自动通知）</span></label>
        <h3>通知内容</h3>
        <div class="notification-grid notification-options">
          ${check('login','管理员登录成功 / 失败（账号、来源 IP）')}
          ${check('servers','服务器离线 / 恢复（离线超过 60 秒）')}
          ${check('xray','Xray 运行或配置异常 / 恢复')}
          ${check('ipLimit','客户端在线 IP 数超限')}
          ${check('expiry','用户和入站即将到期 / 已到期')}
          ${check('traffic','用户和入站剩余流量不足 / 用尽')}
        </div>
        <div class="notification-grid">
          <label>提前几天提醒到期<input name="expiryDays" type="number" min="0" max="365" value="${c.expiryDays}" required><small>0 表示仅提醒已到期。</small></label>
          <label>剩余流量预警（GiB）<input name="remainingGB" type="number" min="0" max="1048576" step="0.01" value="${c.remainingGB}" required><small>0 表示仅提醒额度用尽。</small></label>
        </div>
        <h3>定期报告</h3>
        ${check('daily','每日发送服务器状态、用户数量和累计流量报告')}
        <div class="notification-grid">
          <label>发送时间<input name="reportTime" type="time" value="${esc(c.reportTime)}" required></label>
          <label>时区<input name="timezone" value="${esc(c.timezone)}" placeholder="Asia/Shanghai" required></label>
        </div>
        <p class="hint">报告到点后发送，面板重启会补发当日尚未发送的报告。流量为累计值。自动通知每 30 秒检查，重复告警合并，失败后重试；登录事件最多保留一小时。这里只配置通知，不提供 Telegram 远程管理命令。</p>
        <div class="notice">先在 Telegram 向 @BotFather 发送 /newbot 创建机器人，再向你的机器人发送 /start。群组接收需先将机器人加入群组并允许发消息。请先保存，再发送测试通知。</div>
        <p id="telegram-status" class="hint" role="status"></p>
        <p id="telegram-error" class="error" role="alert"></p>
        <div class="actions"><button class="primary" type="submit">保存设置</button><button type="button" id="telegram-test">发送测试通知</button></div>
        <pre id="telegram-test-results" role="status"></pre>
      </div>
    </form>`;
    telegramStatus(data);
    $('#telegram-form').addEventListener('submit',saveTelegramSettings);
    $('#telegram-test').addEventListener('click',testTelegramSettings);
  } catch(error) {if(page==='notifications')$('#workspace').innerHTML=`<div class="notice">${esc(error.message)}<button type="button" data-page="notifications">重新加载</button></div>`;}
}
async function saveTelegramSettings(event) {
  event.preventDefault();const form=event.currentTarget,fd=new FormData(form),button=form.querySelector('[type=submit]');button.disabled=true;$('#telegram-error').textContent='';
  const data={};for(const name of ['enabled','login','servers','xray','expiry','traffic','ipLimit','daily','clearToken'])data[name]=fd.has(name);
  for(const name of ['token','reportTime','timezone'])data[name]=fd.get(name).trim();
  for(const name of ['expiryDays','remainingGB'])data[name]=Number(fd.get(name));
  data.chatIds=fd.get('chatIds').split(/[,，\s]+/).filter(Boolean);
  try {await api('/api/notifications/telegram','PUT',data);form.elements.token.value='';toast('通知设置已保存');if(page==='notifications')await loadTelegramSettings();}
  catch(error){if($('#telegram-error'))$('#telegram-error').textContent=error.message;}finally{button.disabled=false;}
}
async function testTelegramSettings() {
  const button=$('#telegram-test'),output=$('#telegram-test-results');button.disabled=true;output.textContent='正在向已保存的 Chat ID 发送测试通知…';
  try {const data=await api('/api/notifications/telegram/test','POST',{});output.textContent=data.results.map(r=>`${r.chatId}：${r.result}`).join('\n');await refreshTelegramStatus();}
  catch(error){output.textContent=error.message;}finally{button.disabled=false;}
}


async function loadAccountSettings() {
  $('#workspace').innerHTML='<div class="notice">正在读取后台设置…</div>';
  try {
    const me=await api('/api/me');if(page!=='settings')return;
    $('#workspace').innerHTML=`<form id="account-form" class="panel notification-panel">
      <div class="panel-title"><h2>管理员账号</h2></div>
      <div class="notification-body">
        <div class="notification-grid">
          <label>管理员账号<input name="username" autocomplete="username" value="${esc(me.username)}" maxlength="64" required><small>不含空格，最多 64 字节。</small></label>
          <label>当前密码<input name="currentPassword" type="password" autocomplete="current-password" required><small>修改账号或密码前均需验证。</small></label>
          <label>新密码<input name="newPassword" type="password" autocomplete="new-password"><small>12–72 字节；留空表示仅修改账号。</small></label>
          <label>确认新密码<input name="confirmPassword" type="password" autocomplete="new-password"></label>
        </div>
        <div class="notice">保存成功后，所有设备的登录会话都会失效，请使用新账号和密码重新登录。</div>
        <p id="account-error" class="error" role="alert"></p>
        <button class="primary" type="submit">保存并重新登录</button>
      </div>
    </form>`;
    $('#account-form').addEventListener('submit',saveAccountSettings);
  } catch(error) {if(page==='settings')$('#workspace').innerHTML=`<div class="notice">${esc(error.message)}<button type="button" data-page="settings">重新加载</button></div>`;}
}
async function saveAccountSettings(event) {
  event.preventDefault();const form=event.currentTarget,fd=new FormData(form),button=form.querySelector('[type=submit]'),errorEl=$('#account-error');errorEl.textContent='';
  const password=fd.get('newPassword');
  if(password!==fd.get('confirmPassword')){errorEl.textContent='两次输入的新密码不一致';return;}
  if(password&&(new TextEncoder().encode(password).length<12||new TextEncoder().encode(password).length>72)){errorEl.textContent='新密码须为 12–72 字节';return;}
  button.disabled=true;
  try {
    const username=fd.get('username').trim();
    await api('/api/settings/account','PUT',{username,currentPassword:fd.get('currentPassword'),newPassword:password});
    form.reset();$('#workspace').innerHTML='';page='overview';showLogin();
    $('#login-form input[name=username]').value=username;
    $('#login-form input[name=password]').focus();toast('账号设置已更新，请重新登录');
  } catch(error) {errorEl.textContent=error.message;}
  finally {button.disabled=false;}
}
