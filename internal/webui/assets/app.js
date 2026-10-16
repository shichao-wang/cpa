'use strict';
const $ = id => document.getElementById(id);
const slots = ['opus', 'sonnet', 'haiku'];
let token = '', state = null, editingConnection = null, editingProfile = null;
$('address').textContent = location.host;
function message(text, error = false) { $('message').textContent = text; $('message').classList.toggle('error', error); }
function clearProfileKey() { $('profile-key').value = ''; $('agent-url').textContent = ''; $('profile-created').hidden = true; $('new-profile').disabled = false; }
function logout() { clearProfileKey(); token = ''; state = null; $('workspace').hidden = true; $('logout').hidden = true; $('login').hidden = false; $('connection-form').reset(); $('profile-form').reset(); $('connections').replaceChildren(); $('profiles').replaceChildren(); $('bindings').replaceChildren(); }
async function api(path, method = 'GET', body) {
  const response = await fetch('/admin/api/' + path, { method, headers: {Authorization: 'Bearer ' + token, ...(body === undefined ? {} : {'Content-Type': 'application/json'})}, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'omit', cache: 'no-store', redirect: 'error'});
  const result = await response.json();
  if (!response.ok) { if (response.status === 401) logout(); throw new Error(result.error || '操作失败，请刷新后重试'); }
  return result;
}
async function refresh() { state = await api('state'); render(); }
async function save(path, method, body) {
  const result = await api(path, method, body);
  if (result.profileKey) {
    $('profile-key').value = result.profileKey; $('agent-url').textContent = location.origin; $('profile-created').hidden = false; $('new-profile').disabled = true;
  }
  try { await refresh(); } catch (error) { throw new Error('配置已保存，但刷新失败。请先保存页面上的新 key，再刷新确认。' + error.message); }
  message(result.active ? '配置已保存，并已在网关生效。' : '配置已保存，网关尚未确认生效。请稍后刷新状态。', !result.active);
}
function element(tag, text, className) { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (className) el.className = className; return el; }
function button(text, action, className) { const el = element('button', text, className); el.type = 'button'; el.addEventListener('click', () => guarded(el, action)); return el; }
async function guarded(el, action) { el.disabled = true; try { await action(); } catch (error) { message(error.message, true); } finally { el.disabled = false; } }
function render() {
  $('login').hidden = true; $('workspace').hidden = false; $('logout').hidden = false;
  $('status').textContent = `远端监听 ${state.listen} · 已保存 r${state.revision} · 已生效 r${state.activeRevision} · ${state.ready && state.activeRevision >= state.revision ? '运行中' : '等待网关加载配置'}`;
  const connections = $('connections'); connections.replaceChildren();
  for (const c of Object.values(state.connections).sort((a,b) => a.name.localeCompare(b.name))) {
    const item = element('article', undefined, 'item'); const heading = element('h3', c.name);
    heading.append(element('span', c.authType === 'codex-oauth' ? 'OAuth' : 'API key', 'tag'));
    item.append(heading, element('p', c.protocol + ' · ' + (c.credentialConfigured ? '凭证已配置' : '凭证未配置')), element('p', c.baseURL), element('p', c.models.map(m => m.name ? `${m.name} (${m.id})` : m.id).join('、')));
    const actions = element('div', undefined, 'actions');
    actions.append(button('编辑', () => openConnection(c.name)));
    if (c.authType === 'api-key') actions.append(button('删除', async () => { if (confirm(`删除连接 ${c.name}？被 profile 引用时将拒绝删除。`)) await save('connections/' + encodeURIComponent(c.name), 'DELETE'); }, 'danger'));
    else item.append(element('p', '登录、退出、认证切换和删除请在停止服务后使用 CLI。'));
    item.append(actions); connections.append(item);
  }
  if (!connections.children.length) connections.append(element('p', '尚无上游连接。先添加一个连接和模型清单。', 'muted'));
  const profiles = $('profiles'); profiles.replaceChildren();
  for (const p of Object.values(state.profiles).sort((a,b) => a.name.localeCompare(b.name))) {
    const item = element('article', undefined, 'item'); const heading = element('h3', p.name);
    if (p.managed) heading.append(element('span', '远端配置受管', 'tag'));
    item.append(heading);
    for (const slot of slots) { const b = p.models[slot]; item.append(element('p', `${slot} · ${b.public_model} → ${b.connection} / ${b.target_model}`)); }
    const actions = element('div', undefined, 'actions'); actions.append(button('编辑映射', () => openProfile(p.name)), button('删除', async () => { if (confirm(`删除 profile ${p.name}？正在被远端配置使用时将拒绝删除。`)) await save('profiles/' + encodeURIComponent(p.name), 'DELETE'); }, 'danger')); item.append(actions); profiles.append(item);
  }
  if (!profiles.children.length) profiles.append(element('p', '尚无 profile。添加连接后，为三个模型档位设置映射。', 'muted'));
}
function showEditor(id) { $('connection-editor').hidden = id !== 'connection-editor'; $('profile-editor').hidden = id !== 'profile-editor'; $(id).scrollIntoView({behavior:'smooth', block:'start'}); }
function openConnection(name = null) {
  editingConnection = name; const form = $('connection-form'); form.reset();
  const c = name ? state.connections[name] : null; const f = form.elements;
  $('connection-heading').textContent = c ? '编辑连接 · ' + name : '添加 API key 连接';
  f.name.disabled = !!c; f.protocol.disabled = !!c; f.apiKey.required = !c; f.apiKey.disabled = c?.authType === 'codex-oauth';
  if (c) { f.name.value = c.name; f.protocol.value = c.protocol; f.baseURL.value = c.baseURL; f.models.value = c.models.map(m => m.id + (m.name ? '=' + m.name : '')).join('\n'); }
  showEditor('connection-editor');
}
function labeledInput(parent, label, name, value = '', required = false) { const wrap = element('label', label); const input = document.createElement('input'); input.name = name; input.value = value; input.required = required; wrap.append(input); parent.append(wrap); return input; }
function openProfile(name = null) {
  if (!name && !$('profile-created').hidden) { message('请先保存新建 profile 的 key 并关闭提示。', true); return; }
  if (!Object.keys(state.connections).length) { message('请先添加上游连接。', true); return; }
  editingProfile = name; const form = $('profile-form'); form.reset(); const p = name ? state.profiles[name] : null;
  form.elements.name.disabled = !!p; form.elements.name.value = name || '';
  $('profile-heading').textContent = p ? '编辑 profile · ' + name : '创建 profile'; $('bindings').replaceChildren();
  for (const slot of slots) {
    const b = p?.models[slot]; const fieldset = element('fieldset'); fieldset.append(element('legend', slot.toUpperCase())); const grid = element('div', undefined, 'binding-grid');
    labeledInput(grid, '公开模型 ID', slot + '-public', b?.public_model || 'claude-' + slot, true);
    const label = element('label', '上游连接'); const conn = document.createElement('select'); conn.name = slot + '-connection'; conn.required = true;
    for (const c of Object.values(state.connections)) { const option = element('option', c.name); option.value = c.name; conn.append(option); }
    label.append(conn); grid.append(label); if (b) conn.value = b.connection;
    const modelLabel = element('label', '上游模型'); const model = document.createElement('select'); model.name = slot + '-model'; model.required = true; modelLabel.append(model); grid.append(modelLabel);
    const updateModels = () => { model.replaceChildren(); for (const m of state.connections[conn.value]?.models || []) { const option = element('option', m.name ? `${m.name} (${m.id})` : m.id); option.value = m.id; model.append(option); } };
    conn.addEventListener('change', updateModels); updateModels(); if (b) model.value = b.target_model;
    labeledInput(grid, '展示名（可选）', slot + '-label', b?.label || ''); labeledInput(grid, '说明（可选）', slot + '-description', b?.description || ''); fieldset.append(grid); $('bindings').append(fieldset);
  }
  showEditor('profile-editor');
}
$('login-form').addEventListener('submit', event => { event.preventDefault(); const value = $('token').value.trim(); $('token').value = ''; guarded(event.submitter, async () => { token = value; try { await refresh(); message('已连接当前服务器。'); } catch(error) { logout(); throw error; } }); });
$('logout').addEventListener('click', () => { logout(); message('已退出管理。'); });
$('refresh').addEventListener('click', event => guarded(event.currentTarget, refresh));
$('new-connection').addEventListener('click', () => openConnection());
$('new-profile').addEventListener('click', () => openProfile());
for (const el of document.querySelectorAll('[data-close]')) el.addEventListener('click', () => { $(el.dataset.close).hidden = true; if (el.dataset.close === 'connection-editor') $('connection-form').elements.apiKey.value = ''; });
$('connection-form').addEventListener('submit', event => {
  event.preventDefault(); const f = event.currentTarget.elements;
  const models = f.models.value.split('\n').map(s => s.trim()).filter(Boolean).map(line => { const index = line.indexOf('='); return index < 0 ? {id:line} : {id:line.slice(0,index).trim(),name:line.slice(index+1).trim()}; });
  const body = {baseURL:f.baseURL.value.trim(),models}; if (f.apiKey.value) body.apiKey = f.apiKey.value;
  if (!editingConnection) { body.name = f.name.value.trim(); body.protocol = f.protocol.value; }
  guarded(event.submitter, async () => { try { await save(editingConnection ? 'connections/' + encodeURIComponent(editingConnection) : 'connections', editingConnection ? 'PATCH' : 'POST', body); $('connection-editor').hidden = true; } finally { f.apiKey.value = ''; } });
});
$('profile-form').addEventListener('submit', event => {
  event.preventDefault(); const f = event.currentTarget.elements; const models = {};
  for (const slot of slots) models[slot] = {public_model:f[slot+'-public'].value.trim(),connection:f[slot+'-connection'].value,target_model:f[slot+'-model'].value,label:f[slot+'-label'].value.trim(),description:f[slot+'-description'].value.trim()};
  const body = {models}; if (!editingProfile) body.name = f.name.value.trim();
  guarded(event.submitter, async () => { await save(editingProfile ? 'profiles/' + encodeURIComponent(editingProfile) : 'profiles', editingProfile ? 'PATCH' : 'POST', body); $('profile-editor').hidden = true; });
});

$('copy-profile-key').addEventListener('click', event => guarded(event.currentTarget, async () => {
  try { await navigator.clipboard.writeText($('profile-key').value); message('已复制下游 key。请妥善保存后关闭提示。'); }
  catch { $('profile-key').focus(); $('profile-key').select(); message('无法自动复制，请复制已选中的下游 key。', true); }
}));
$('dismiss-profile-key').addEventListener('click', clearProfileKey);
