import { ArrowLeft, Check, CircleAlert, Download, Eye, EyeOff, FlaskConical, KeyRound, Loader2, Plus, Plug, Search, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { APIChannel, APISettings, DeviceSettingsAPI } from '../api';

const message = (e: unknown) => e instanceof Error ? e.message : '请求失败，请重试';
const visible = (p: APIChannel) => p.models.filter(m => !p.hidden_models.includes(m));
const addressKey = (url: string) => url.trim().replace(/\/+$/, '').replace(/\/(chat\/completions|models)$/, '');
type Notice = { error: boolean; text: string };

export default function APIChannels({ target, saving, setSaving, onSaved }: {
  target: DeviceSettingsAPI; saving: boolean; setSaving: (v: boolean) => void; onSaved: () => void;
}) {
  const [state, setState] = useState<APISettings>();
  const [channels, setChannels] = useState<APIChannel[]>([]);
  const [selected, setSelected] = useState('');
  const [defaultID, setDefaultID] = useState('');
  const [model, setModel] = useState('');
  const [dirty, setDirty] = useState(false);
  const [adding, setAdding] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [query, setQuery] = useState('');
  const [manual, setManual] = useState('');
  const [testModel, setTestModel] = useState('');
  const [remove, setRemove] = useState(false);
  const [work, setWork] = useState('');
  const [notice, setNotice] = useState<Notice>();
  const [attempt, setAttempt] = useState(0);

  function apply(s: APISettings) {
    const rows = (s.providers ?? []).map(p => ({ ...p, models: p.models ?? [], hidden_models: p.hidden_models ?? [] }));
    setState(s); setChannels(rows); setDefaultID(s.default_provider_id ?? rows[0]?.id ?? '');
    setModel(s.model); setDirty(false);
  }
  useEffect(() => {
    const controller = new AbortController();
    target.settings(controller.signal).then(s => { if (!controller.signal.aborted) apply(s); })
      .catch(e => { if (!controller.signal.aborted) setNotice({ error: true, text: message(e) }); });
    return () => controller.abort();
  }, [attempt, target]);
  const provider = channels.find(p => p.id === selected);
  const testingModel = provider ? (visible(provider).includes(testModel) ? testModel : visible(provider)[0] ?? '') : '';
  const route = channels.find(p => p.id === defaultID);
  const busy = saving || !!work;
  function changed() { setDirty(true); setNotice(undefined); }
  function patch(update: Partial<APIChannel>) {
    setChannels(rows => rows.map(p => p.id === selected ? { ...p, ...update } : p)); changed();
  }
  function edit(id: string) { setSelected(id); setAdding(false); setRemove(false); setShowKey(false); setQuery(''); setManual(''); setTestModel(''); setNotice(undefined); }
  function add() {
    const id = crypto.randomUUID();
    const p: APIChannel = { id, name: '新 API 渠道', base_url: '', enabled: true, auth_required: true, has_api_key: false, models: [], hidden_models: [] };
    setChannels(rows => [...rows, p]); if (!defaultID) setDefaultID(id); changed(); edit(id); setAdding(true);
  }
  function payload(p: APIChannel): APISettings {
    return { base_url: p.base_url, model: testingModel, provider_id: p.id, api_key: p.auth_required ? p.api_key : '', has_api_key: p.has_api_key };
  }
  async function discover() {
    if (!provider) return; setWork('models'); setNotice(undefined); setSaving(true);
    try {
      const { models } = await target.providerModels(payload(provider));
      patch({ models, hidden_models: provider.hidden_models.filter(m => models.includes(m)) });
      setNotice({ error: false, text: `已读取 ${models.length} 个模型，可选择需要显示的模型。` });
    } catch (e) { setNotice({ error: true, text: message(e) }); }
    finally { setWork(''); setSaving(false); }
  }
  async function test() {
    if (!provider) return; setWork('test'); setNotice(undefined); setSaving(true);
    try { const r = await target.testProvider(payload(provider)); setNotice({ error: false, text: `连接可用 · ${r.latency_ms} ms（文本调用测试）` }); }
    catch (e) { setNotice({ error: true, text: message(e) }); }
    finally { setWork(''); setSaving(false); }
  }
  async function save() {
    if (!state || (channels.length > 0 && (!route || !model))) return; setSaving(true); setNotice(undefined);
    try {
      const s = await target.saveSettings({ ...state, base_url: route?.base_url ?? '', model, providers: channels, default_provider_id: defaultID });
      apply(s); setAdding(false); setNotice({ error: false, text: '配置已加密保存，下一轮对话和任务使用新配置。' }); onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setSaving(false); }
  }
  function removeProvider() {
    const remaining = channels.filter(p => p.id !== selected); setChannels(remaining);
    if (defaultID === selected) { const next = remaining.find(p => p.enabled); setDefaultID(next?.id ?? ''); setModel(next ? visible(next)[0] ?? '' : ''); }
    setSelected(''); setRemove(false); changed();
  }
  const valid = channels.length === 0 || (!!route?.enabled && visible(route).includes(model) && channels.every(p => p.name.trim() && p.base_url.trim()));
  return <div className="settings-channel-page">
    <div className="settings-channel-title"><div><h3 id="settings-api-title">API 渠道</h3><p className="settings-description">管理模型服务渠道，选择聊天和任务使用的默认模型。</p></div>
      {state && !provider && <button className="settings-button settings-primary" disabled={busy} onClick={add}><Plus size={15} />添加渠道</button>}
    </div>
    {state?.env_override && <p className="settings-env-note">当前模型由启动时的环境变量（BASE_URL / API_KEY / CHAT_MODEL）指定，优先于这里的设置；这里保存的内容要等去掉环境变量、重启后才生效。</p>}
    {notice && <p role={notice.error ? 'alert' : 'status'} className={`settings-feedback${notice.error ? ' is-error' : ''}`}>{notice.error ? <CircleAlert size={15} /> : <Check size={15} />}<span>{notice.text}</span></p>}
    {!state ? <div className="settings-load">{notice ? <button className="settings-button" onClick={() => { setNotice(undefined); setAttempt(n => n + 1); }}>重试</button> : <p className="settings-pending"><Loader2 size={16} className="animate-spin" />正在读取渠道…</p>}</div> : <>
      {provider ? <div className="settings-channel-editor">
        <button type="button" className="settings-editor-back" disabled={busy} onClick={() => { setSelected(''); setNotice(undefined); }}><ArrowLeft size={14} />所有渠道</button>
        <div className="settings-channel-editor-title"><span className="settings-provider-icon"><Plug size={19} /></span><div><h4>{adding ? '添加自定义渠道' : provider.name}</h4><p>连接兼容 OpenAI 的 API 或本地模型服务</p></div>
          <label className="settings-enable-label">启用<input type="checkbox" checked={provider.enabled} disabled={busy} onChange={e => patch({ enabled: e.target.checked })} /></label>
        </div>
        <fieldset disabled={busy} className="settings-fields">
          <div className="settings-channel-form-grid">
            <div className="settings-field"><label htmlFor="channel-name">渠道名称</label><input id="channel-name" value={provider.name} placeholder="例如：主力 API / 本地模型" onChange={e => patch({ name: e.target.value })} /></div>
            <div className="settings-field"><label htmlFor="channel-auth">认证方式</label><select id="channel-auth" value={provider.auth_required ? 'key' : 'none'} onChange={e => patch({ auth_required: e.target.value === 'key', api_key: '' })}><option value="key">Bearer API Key</option><option value="none">无需密钥（本地服务）</option></select></div>
          </div>
          <div className="settings-field"><label htmlFor="settings-api-url">接口基础地址</label><input id="settings-api-url" type="url" value={provider.base_url} placeholder="https://example.com/v1" onChange={e => {
            const reset = addressKey(e.target.value) !== addressKey(provider.base_url);
            patch({ base_url: e.target.value, ...(reset ? { api_key: '', has_api_key: false, models: [], hidden_models: [] } : {}) });
          }} /><p className="settings-field-hint">接口由所选设备访问；localhost 指所选设备。修改地址后需重新填写密钥并拉取模型。</p></div>
          <div className="settings-field"><label htmlFor="channel-protocol">接口协议</label><select id="channel-protocol" value="chat_completions" disabled><option value="chat_completions">Chat Completions</option></select></div>
          {provider.auth_required && <div className="settings-field"><div className="settings-field-head"><label htmlFor="channel-key">API Key</label>{provider.has_api_key && <button type="button" className="settings-link" onClick={() => patch({ api_key: provider.api_key === '' ? undefined : '' })}>{provider.api_key === '' ? '撤销清除' : '清除已存密钥'}</button>}</div>
            <div className="settings-key-input"><input id="channel-key" type={showKey ? 'text' : 'password'} autoComplete="new-password" value={provider.api_key ?? ''} placeholder={provider.has_api_key ? '已加密保存，留空保留' : '输入服务商的 API 密钥'} onChange={e => patch({ api_key: e.target.value || undefined })} /><button type="button" aria-label={showKey ? '隐藏密钥' : '显示密钥'} onClick={() => setShowKey(!showKey)}>{showKey ? <EyeOff size={16} /> : <Eye size={16} />}</button></div>
            <p className="settings-field-hint">{provider.api_key === '' && provider.has_api_key ? '保存时将清除原密钥。' : '密钥加密保存在所选设备，不会回显。'}</p></div>}
        </fieldset>
        <div className="settings-catalog-title"><div><h4>可选模型</h4><p>拉取模型后，开启希望出现在选择菜单中的模型。</p></div><button className="settings-button" disabled={busy || !provider.base_url.trim()} onClick={discover}>{work === 'models' ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}拉取模型</button></div>
        <div className="settings-catalog-toolbar"><span>{provider.models.length} 个模型 · {visible(provider).length} 个显示</span><label className="settings-model-search"><Search size={13} /><input aria-label="搜索模型" placeholder="搜索模型" value={query} onChange={e => setQuery(e.target.value)} /></label></div>
        <div className="settings-channel-catalog">{provider.models.length ? provider.models.filter(m => m.toLowerCase().includes(query.toLowerCase())).map(m => { const shown = !provider.hidden_models.includes(m); return <div className="settings-channel-model" key={m}><code>{m}</code><button type="button" role="switch" aria-checked={shown} aria-label={`显示模型 ${m}`} className={`settings-agent-switch${shown ? ' is-on' : ''}`} disabled={busy} onClick={() => patch({ hidden_models: shown ? provider.hidden_models.filter(id => id !== m).concat(m) : provider.hidden_models.filter(id => id !== m) })}><span /></button></div>; }) : <div className="settings-catalog-empty">还没有模型<p>配置接口和密钥后，点击「拉取模型」。</p></div>}</div>
        <form className="settings-manual-model" onSubmit={e => { e.preventDefault(); const id = manual.trim(); if (id && !provider.models.includes(id)) patch({ models: [...provider.models, id] }); setManual(''); }}><input aria-label="手动添加模型 ID" placeholder="也可手动添加模型 ID" value={manual} disabled={busy} onChange={e => setManual(e.target.value)} /><button className="settings-button" disabled={busy || !manual.trim()}><Plus size={14} />添加</button></form>
        <div className="settings-provider-test"><div className="settings-field"><label htmlFor="channel-test">测试模型</label><select id="channel-test" value={testingModel} disabled={busy} onChange={e => setTestModel(e.target.value)}><option value="" disabled>选择模型</option>{visible(provider).map(m => <option key={m}>{m}</option>)}</select></div><button className="settings-button" disabled={busy || !testingModel} onClick={test}>{work === 'test' ? <Loader2 size={14} className="animate-spin" /> : <FlaskConical size={14} />}测试连接</button></div>
        <p className="settings-field-hint">测试连接会发起一次文本调用，可能产生少量费用。</p>
        <div className="settings-remove-channel">{remove ? <><span>移除后需保存才生效。</span><button className="settings-button" disabled={busy} onClick={removeProvider}>确认移除</button><button className="settings-link" onClick={() => setRemove(false)}>取消</button></> : <button className="settings-link" disabled={busy} onClick={() => setRemove(true)}><Trash2 size={13} />移除此渠道</button>}</div>
      </div> : <>
        <div className="settings-channel-count">{channels.length} 个渠道<span>配置保存后，下一轮任务生效</span></div>
        <ul className="settings-channel-grid">{channels.map(p => <li key={p.id} className="settings-channel-card"><div className="settings-channel-card-head"><Plug size={18} /><h4>{p.name}</h4><span className="settings-agent-status">{p.id === defaultID ? '默认渠道' : p.enabled ? '已启用' : '已停用'}</span></div><p title={p.base_url}>{p.base_url || '尚未配置接口地址'}</p><small>{visible(p).length} 个可选模型 · {p.auth_required ? 'API Key' : '无需密钥'}</small><div className="settings-agent-actions"><button role="switch" aria-checked={p.enabled} aria-label={`启用渠道 ${p.name}`} className={`settings-agent-switch${p.enabled ? ' is-on' : ''}`} disabled={busy} onClick={() => { setChannels(rows => rows.map(c => c.id === p.id ? { ...c, enabled: !c.enabled } : c)); changed(); }}><span /></button><button className="settings-button" disabled={busy} onClick={() => edit(p.id)}>设置</button></div></li>)}
        {!channels.length && <li className="settings-channels-empty"><Plug size={26} /><h4>还没有 API 渠道</h4><p>添加服务商或本地模型接口，然后选择默认模型。</p><button className="settings-button" onClick={add}><Plus size={14} />添加第一个渠道</button></li>}</ul>
      </>}
      <div className="settings-default-route"><div><h4>默认模型</h4><p>用于聊天与后台任务</p></div><div className="settings-channel-form-grid"><div className="settings-field"><label htmlFor="default-channel">API 渠道</label><select id="default-channel" value={defaultID} disabled={busy} onChange={e => { const p = channels.find(c => c.id === e.target.value); setDefaultID(e.target.value); setModel(p ? visible(p)[0] ?? '' : ''); changed(); }}><option value="" disabled>选择渠道</option>{channels.map(p => <option key={p.id} value={p.id} disabled={!p.enabled}>{p.name}{p.enabled ? '' : '（已停用）'}</option>)}</select></div><div className="settings-field"><label htmlFor="default-model">模型</label><select id="default-model" value={model} disabled={busy} onChange={e => { setModel(e.target.value); changed(); }}><option value="">选择模型</option>{route && visible(route).map(m => <option key={m}>{m}</option>)}</select></div></div></div>
      <footer className="settings-channels-footer"><span><KeyRound size={13} />{dirty ? '有未保存的修改' : '配置已保存 · 密钥加密存储'}</span><button type="button" className="settings-button settings-primary" disabled={busy || !dirty || !valid} onClick={save}>{saving && !work && <Loader2 size={14} className="animate-spin" />}保存设置</button></footer>
    </>}
  </div>;
}
