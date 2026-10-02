'use strict';
let routeServer = '';
const routeItems = () => (state.outbounds || []).filter(o => o.serverId === routeServer);
const routeRules = () => (state.rules || []).filter(r => r.serverId === routeServer);
const routeButton = (action, id, text, disabled=false) => `<button type="button" data-routing-action="${action}" data-id="${esc(id)}" ${disabled?'disabled':''}>${esc(text)}</button>`;
function routeTarget(id) {return id==='direct'?'本服务器直连':id==='block'?'阻断':(state.outbounds||[]).find(o=>o.id===id)?.name||'出口不存在';}
function routeOptions(selected, includeDisabled=false) {return [['direct','本服务器直连'],['block','阻断'],...routeItems().filter(o=>o.enabled||includeDisabled&&o.id===selected).map(o=>[o.id,o.name+(o.enabled?'':'（已停用）')])].map(([id,name])=>`<option value="${esc(id)}" ${id===selected?'selected':''}>${esc(name)}</option>`).join('');}
function renderRouting() {
 if (!state.servers.some(s=>s.id===routeServer)) routeServer=state.servers[0]?.id||'';
 const srv=state.servers.find(s=>s.id===routeServer);
 let html=`<div class="routing-toolbar"><label>配置服务器<select id="routing-server">${state.servers.map(s=>`<option value="${esc(s.id)}" ${s.id===routeServer?'selected':''}>${esc(s.name)} · ${esc(s.host)}</option>`).join('')}</select></label>${srv?`<div>${badge(srv.error?'应用失败':srv.version===srv.appliedVersion?'已同步':'等待同步',srv.error?'bad':srv.version===srv.appliedVersion?'good':'warn')} <small>v${esc(srv.appliedVersion)} / v${esc(srv.version)}</small>${srv.error?`<div class="error-text">${esc(srv.error)}</div>`:''}</div>`:''}</div>`;
 if (!srv) {$('#workspace').innerHTML=html+'<div class="notice">请先添加服务器，再配置出站与分流。</div>';return;}
 if (page==='outbounds') {
  html+=`<div class="routing-toolbar"><div>落地出口 · ${routeItems().length} 个</div><div class="actions">${routeButton('outbound-add','','＋ 添加出口')}${routeButton('from-node','','从现有入站复制')}</div></div>`;
  html+=panel('自定义出站',routeItems().length,['名称','落地地址','协议','状态','操作'],routeItems().map(o=>`<tr><td><strong>${esc(o.name)}</strong></td><td>${esc(o.address)}:${esc(o.port)}</td><td>${esc(o.protocol==='vless'?'VLESS / '+o.security.toUpperCase():o.method)}</td><td>${badge(o.enabled?'已启用':'已停用',o.enabled?'good':'')}</td><td><div class="actions">${routeButton('outbound-edit',o.id,'编辑')}${routeButton('outbound-toggle',o.id,o.enabled?'停用':'启用')}${routeButton('outbound-delete',o.id,'删除')}</div></td></tr>`).join(''));
  html+='<div class="notice">链式路径：客户端 → 当前服务器 A → 此处配置的落地 B → 目标网站。添加出口后，前往“路由分流”选择默认出口或添加规则。支持 RAW TCP 的 VLESS + REALITY/TLS、Shadowsocks（含 2022）；不会自动切换到直连。</div>';
 } else {
  const cfg=srv.routing||{},rules=routeRules();
  html+=`<div class="routing-toolbar"><div><strong>未匹配时：${esc(routeTarget(cfg.defaultOutbound||'direct'))}</strong><small>域名策略：${esc(cfg.domainStrategy||'AsIs')}</small></div><div class="actions">${routeButton('settings','','默认出口设置')}${routeButton('rule-add','','＋ 添加规则')}</div></div>`;
  html+=panel('按顺序匹配，首条命中生效',rules.length,['顺序','规则 / 条件','出口','状态','操作'],rules.map((r,i)=>`<tr><td>${i+1}</td><td><strong>${esc(r.name)}</strong><small class="routing-match">${esc([r.domains?.length?'域名：'+r.domains.join(', '):'',r.ips?.length?'IP：'+r.ips.join(', '):'',r.inboundIds?.length?'入站：'+r.inboundIds.map(id=>state.nodes.find(n=>n.id===id)?.name||id).join(', '):'',r.port?'端口：'+r.port:'',r.network?'网络：'+r.network:''].filter(Boolean).join(' · '))}</small></td><td>${esc(routeTarget(r.outboundId))}</td><td>${badge(r.enabled?'启用':'停用',r.enabled?'good':'')}</td><td><div class="actions">${routeButton('rule-up',r.id,'上移',i===0)}${routeButton('rule-down',r.id,'下移',i===rules.length-1)}${routeButton('rule-edit',r.id,'编辑')}${routeButton('rule-toggle',r.id,r.enabled?'停用':'启用')}${routeButton('rule-delete',r.id,'删除')}</div></td></tr>`).join(''));
  html+='<div class="notice">不同条件同时满足（AND），同一类条件任一满足（OR）。停用规则跳过。直连指当前 VPS 的出口，不是手机本地直连。域名分流需要客户端传入域名，或在对应入站启用嗅探；使用 IPIfNonMatch / IPOnDemand 时由服务器解析域名。目前支持手动域名、IP/CIDR，不含 geosite/geoip 规则库。</div>';
 }
 $('#workspace').innerHTML=html;
}
const routeField=(label,name,value='',type='text',extra='')=>`<label>${esc(label)}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
function routeForm(kind,id,body) {info(kind==='outbound'?'出站设置':kind==='rule'?'分流规则':kind==='from-node'?'复制落地入站':'默认出口设置',`<form id="routing-form" data-kind="${kind}" data-id="${esc(id)}" data-server="${esc(routeServer)}" class="routing-form">${body}<p class="error-text" id="routing-error" role="alert"></p><div class="actions"><button type="button" data-close="info">取消</button><button class="primary" type="submit">保存并下发</button></div></form>`);}
function editOutbound(id) {
 const o=routeItems().find(o=>o.id===id)||{enabled:true,protocol:'vless',port:443,security:'reality',fingerprint:'chrome',flow:'xtls-rprx-vision',method:'2022-blake3-aes-128-gcm'};
 routeForm('outbound',id,`<div class="routing-grid">${routeField('名称','name',o.name,'text','required maxlength="100"')}<label>协议<select name="protocol"><option value="vless" ${o.protocol==='vless'?'selected':''}>VLESS</option><option value="shadowsocks" ${o.protocol==='shadowsocks'?'selected':''}>Shadowsocks</option></select></label>${routeField('落地主机（域名 / IP，不含端口）','address',o.address,'text','required')}${routeField('落地端口','port',o.port,'number','required min="1" max="65535"')}</div><div data-out-protocol="vless" class="routing-grid">${routeField(id?'UUID（留空保留）':'UUID','uuid','','password','autocomplete="new-password"')}<label>Flow<select name="flow">${['','xtls-rprx-vision','xtls-rprx-vision-udp443'].map(v=>`<option ${o.flow===v?'selected':''} value="${v}">${v||'无 Flow'}</option>`).join('')}</select></label><label>传输安全<select name="security"><option value="reality" ${o.security==='reality'?'selected':''}>REALITY</option><option value="tls" ${o.security==='tls'?'selected':''}>TLS（校验证书）</option></select></label>${routeField('SNI / Server Name','serverName',o.serverName)}${routeField('REALITY 公钥','publicKey',o.publicKey)}${routeField('REALITY Short ID','shortId',o.shortId)}<label>TLS 指纹<select name="fingerprint">${['chrome','firefox','safari','ios','android','edge','random','randomized'].map(v=>`<option ${o.fingerprint===v?'selected':''}>${v}</option>`).join('')}</select></label></div><div data-out-protocol="shadowsocks" class="routing-grid"><label>加密方式<select name="method">${['2022-blake3-aes-128-gcm','2022-blake3-aes-256-gcm','2022-blake3-chacha20-poly1305','aes-128-gcm','aes-256-gcm','chacha20-ietf-poly1305'].map(v=>`<option ${o.method===v?'selected':''}>${v}</option>`).join('')}</select></label>${routeField(id?'密码 / Base64 密钥（留空保留）':'密码 / Base64 密钥','password','','password','autocomplete="new-password"')}</div><label class="checkbox"><input type="checkbox" name="enabled" ${o.enabled?'checked':''}>启用出口</label><p class="notice">传输固定为 RAW TCP。参数必须与落地服务端一致。SS2022 的 128 位方式使用 16 字节 Base64 密钥，256 位 / ChaCha20 使用 32 字节；多用户密钥用冒号连接。被默认出口或启用规则引用的出口不能停用。</p>`);
 updateOutboundFields();
}
function updateOutboundFields() {const form=$('#routing-form');if(form?.dataset.kind!=='outbound')return;form.querySelectorAll('[data-out-protocol]').forEach(group=>{const hidden=group.dataset.outProtocol!==form.elements.protocol.value;group.hidden=hidden;group.querySelectorAll('input,select').forEach(e=>e.disabled=hidden);});}
function editRule(id) {
 const r=routeRules().find(r=>r.id===id)||{enabled:true,outboundId:'direct'};
 routeForm('rule',id,`<div class="routing-grid">${routeField('规则名称','name',r.name,'text','required maxlength="100"')}<label>目标出口<select name="outboundId">${routeOptions(r.outboundId,true)}</select></label><label>域名（每行一项）<textarea name="domains" rows="4" placeholder="example.com&#10;full:example.org">${esc((r.domains||[]).join('\n'))}</textarea></label><label>IP / CIDR（每行一项）<textarea name="ips" rows="4" placeholder="1.1.1.1&#10;192.168.0.0/16">${esc((r.ips||[]).join('\n'))}</textarea></label>${routeField('目标端口（如 53,443,1000-2000）','port',r.port)}<label>网络<select name="network">${[['','不限'],['tcp','TCP'],['udp','UDP'],['tcp,udp','TCP + UDP']].map(([v,l])=>`<option value="${v}" ${r.network===v?'selected':''}>${l}</option>`).join('')}</select></label></div><fieldset><legend>来源入站（不选表示不限）</legend>${state.nodes.filter(n=>n.serverId===routeServer).map(n=>`<label class="checkbox"><input type="checkbox" name="inboundIds" value="${esc(n.id)}" ${(r.inboundIds||[]).includes(n.id)?'checked':''}>${esc(n.name)} :${esc(n.port)}${n.enabled?'':'（停用）'}</label>`).join('')||'<p>此服务器暂无入站</p>'}</fieldset><label class="checkbox"><input type="checkbox" name="enabled" ${r.enabled?'checked':''}>启用规则</label><p class="notice">至少填写一种条件。不同类条件须同时匹配；每行域名 / IP 任一匹配。裸域名包含子域，支持 domain:、full:、keyword:、regexp:，不支持 geosite/geoip。域名嗅探请在入站设置中开启。</p>`);
}
function editRoutingSettings() {const cfg=state.servers.find(s=>s.id===routeServer)?.routing||{};routeForm('settings','',`<label>未匹配规则时的出口<select name="defaultOutbound">${routeOptions(cfg.defaultOutbound||'direct')}</select></label><label>域名解析策略<select name="domainStrategy">${[['AsIs','AsIs：不额外解析'],['IPIfNonMatch','IPIfNonMatch：无规则命中后解析，再匹配 IP'],['IPOnDemand','IPOnDemand：遇到 IP 规则时解析']].map(([v,l])=>`<option value="${v}" ${v===(cfg.domainStrategy||'AsIs')?'selected':''}>${l}</option>`).join('')}</select></label><p class="notice">修改只影响此服务器。出口故障不会自动直连；保存后等待 Agent 同步，内核校验失败会保留原配置。</p>`);}
function copyRouteNode() {routeForm('from-node','',`${routeField('出口名称（可选）','name')}<label>落地入站<select name="nodeId" required><option value="">选择另一台服务器的入站</option>${state.nodes.filter(n=>n.serverId!==routeServer&&nodeAllowed(n)).map(n=>`<option value="${esc(n.id)}">${esc(serverName(n.serverId))} / ${esc(n.name)}</option>`).join('')}</select></label><label>落地客户端<select name="clientId" required><option value="">请先选择入站</option></select></label><p class="notice">复制当前连接参数，不自动跟随落地端后续修改。落地端修改 UUID、公钥或端口后，请编辑出口或重新复制。请勿将 A 与 B 相互设为默认落地，以免形成循环。</p>`);}
document.addEventListener('change',event=>{
 if(event.target.id==='routing-server'){routeServer=event.target.value;renderRouting();}
 if(event.target.closest('#routing-form')){updateOutboundFields();if(event.target.name==='nodeId') {const id=event.target.value;$('#routing-form').elements.clientId.innerHTML='<option value="">选择已分配的客户端</option>'+state.clients.filter(c=>c.enabled&&(c.nodeIds||[]).includes(id)).map(c=>`<option value="${esc(c.id)}">${esc(c.name)}</option>`).join('');}}
});
document.addEventListener('click',async event=>{
 const b=event.target.closest('[data-routing-action]');if(!b)return;const action=b.dataset.routingAction,id=b.dataset.id;
 try {
  if(action==='outbound-add'||action==='outbound-edit'){editOutbound(id);return;}if(action==='rule-add'||action==='rule-edit'){editRule(id);return;}if(action==='settings'){editRoutingSettings();return;}if(action==='from-node'){copyRouteNode();return;}
  b.disabled=true;
  if(action==='outbound-toggle'){const o=routeItems().find(o=>o.id===id);await api('/api/outbounds/'+encodeURIComponent(id),'PUT',{...o,enabled:!o.enabled});}
  if(action==='rule-toggle'){const r=routeRules().find(r=>r.id===id);await api('/api/routing/rules/'+encodeURIComponent(id),'PUT',{...r,enabled:!r.enabled});}
  if(action.endsWith('-delete')) {if(!confirm('确认删除此'+(action.startsWith('outbound')?'出口':'规则')+'？'))return;await api((action.startsWith('outbound')?'/api/outbounds/':'/api/routing/rules/')+encodeURIComponent(id),'DELETE');}
  if(action==='rule-up'||action==='rule-down'){const ids=routeRules().map(r=>r.id),i=ids.indexOf(id),j=i+(action==='rule-up'?-1:1);if(i<0||j<0||j>=ids.length)return;[ids[i],ids[j]]=[ids[j],ids[i]];await api('/api/servers/'+encodeURIComponent(routeServer)+'/routing/rules/order','PUT',{ids});}
  await refresh();toast('已保存，等待服务器同步');
 }catch(error){toast(error.message);}finally{b.disabled=false;}
});
document.addEventListener('submit',async event=>{
 const form=event.target;if(form.id!=='routing-form')return;event.preventDefault();const f=new FormData(form),kind=form.dataset.kind,id=form.dataset.id,serverId=form.dataset.server,button=form.querySelector('[type=submit]');button.disabled=true;$('#routing-error').textContent='';
 try {
  let path,method='POST',data;
  if(kind==='outbound'){data=Object.fromEntries(f);data.serverId=serverId;data.enabled=f.has('enabled');data.port=Number(data.port);path='/api/outbounds';}
  if(kind==='rule'){const lines=name=>String(f.get(name)||'').split(/\r?\n/).map(v=>v.trim()).filter(Boolean);data={serverId,name:f.get('name'),enabled:f.has('enabled'),outboundId:f.get('outboundId'),domains:lines('domains'),ips:lines('ips'),inboundIds:f.getAll('inboundIds'),port:f.get('port'),network:f.get('network')};path='/api/routing/rules';}
  if(kind==='settings'){path='/api/servers/'+encodeURIComponent(serverId)+'/routing';method='PUT';data=Object.fromEntries(f);}
  if(kind==='from-node'){path='/api/outbounds/from-node';data={...Object.fromEntries(f),serverId};}
  if(id){path+='/'+encodeURIComponent(id);method='PUT';}
  await api(path,method,data);$('#info').close();await refresh();toast('已保存，等待服务器同步');
 }catch(error){if(form.isConnected)$('#routing-error').textContent=error.message;}finally{button.disabled=false;}
});
