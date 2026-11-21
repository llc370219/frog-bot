import { Bot, Check, CircleAlert, Copy, KeyRound, Loader2, MessageSquare, Plug, Save, Undo2, Workflow } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { APIChannel, APISettings, BotProfile, DeviceSettingsAPI, PrivilegeStatus } from '../api';
import { BotAvatar } from './BotAvatar';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 frog 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';
const visible = (p: APIChannel) => p.models.filter(m => !p.hidden_models.includes(m));
type Notice = { error: boolean; text: string };

/**
 * 一个 Bot 的模型设置：聊天模型和后台 Agent 模型。API 渠道整台设备共用，
 * 在「API 渠道」页管理；这里只选每个 Bot 用哪个渠道的哪个模型。
 */
export default function BotModelSettings({ target, bot, bots, deviceName, saving, setSaving, onDirty, onSelectBot, onOpenChannels, onSaved }: {
  target: DeviceSettingsAPI;
  bot: BotProfile;
  bots: BotProfile[];
  deviceName: string;
  saving: boolean;
  setSaving: (v: boolean) => void;
  onDirty: (dirty: boolean) => void;
  onSelectBot: (id: string) => void;
  onOpenChannels: () => void;
  onSaved: () => void;
}) {
  const [state, setState] = useState<APISettings>();
  const [channels, setChannels] = useState<APIChannel[]>([]);
  const [defaultID, setDefaultID] = useState('');
  const [model, setModel] = useState('');
  const [agentProviderID, setAgentProviderID] = useState('');
  const [agentModel, setAgentModel] = useState('');
  const [dirty, setDirty] = useState(false);
  const [copyFrom, setCopyFrom] = useState('');
  const [work, setWork] = useState('');
  const [notice, setNotice] = useState<Notice>();
  const [attempt, setAttempt] = useState(0);

  function applySettings(s: APISettings) {
    const rows = (s.providers ?? []).map(p => ({ ...p, models: p.models ?? [], hidden_models: p.hidden_models ?? [] }));
    setState(s); setChannels(rows); setDefaultID(s.default_provider_id ?? '');
    setModel(s.model); setAgentProviderID(s.agent_provider_id || s.default_provider_id || '');
    setAgentModel(s.agent_model || s.model); setDirty(false);
  }
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    target.settings(signal).then(s => {
      if (!signal.aborted) applySettings(s);
    }).catch(e => { if (!signal.aborted) setNotice({ error: true, text: message(e) }); });
    return () => controller.abort();
  }, [attempt, target]);

  useEffect(() => { onDirty(dirty); }, [dirty]);

  const route = channels.find(p => p.id === defaultID);
  const agentRoute = channels.find(p => p.id === agentProviderID);
  const busy = saving || !!work;
  const chatSet = !!defaultID || !!model;
  const chatValid = !chatSet || (!!route?.enabled && visible(route).includes(model));
  const agentSet = !!agentProviderID || !!agentModel;
  const agentValid = !agentSet || (!!agentRoute?.enabled && visible(agentRoute).includes(agentModel));
  const valid = chatValid && agentValid;
  const invalidReason = !chatValid ? '聊天：请选择已启用渠道里的模型'
    : !agentValid ? 'Agent：请选择已启用渠道里的模型' : '';
  const others = bots.filter(b => b.id !== bot.id);

  function changed() { setDirty(true); setNotice(undefined); }

  async function save() {
    if (!state || !valid) return;
    setSaving(true); setNotice(undefined);
    try {
      applySettings(await target.saveRoutes({ default_provider_id: defaultID, model, agent_provider_id: agentProviderID, agent_model: agentModel }));
      setNotice({ error: false, text: `「${bot.name}」的设置已保存，下一条消息和新任务使用新配置。` });
      onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setSaving(false); }
  }
  function discard() {
    setNotice(undefined); setAttempt(n => n + 1);
  }
  async function copySettings() {
    if (!copyFrom) return;
    setWork('copy'); setSaving(true); setNotice(undefined);
    try {
      applySettings(await target.copySettings(copyFrom));
      setNotice({ error: false, text: `已复制「${bots.find(b => b.id === copyFrom)?.name}」的模型设置，之后两个 Bot 各自独立。` });
      setCopyFrom(''); onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setWork(''); setSaving(false); }
  }

  const scope = (
    <div className="bot-scope">
      <div className="bot-scope-label"><span>正在设置</span>{dirty && <em>先保存或放弃修改，再切换 Bot</em>}</div>
      <div className="bot-scope-chips" role="radiogroup" aria-label="选择要设置的 Bot">
        {bots.map(b => <button key={b.id} type="button" role="radio" aria-checked={b.id === bot.id} className="bot-scope-chip"
          disabled={busy || (dirty && b.id !== bot.id)} onClick={() => onSelectBot(b.id)}>
          <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={18} /><span>{b.name}</span>
        </button>)}
      </div>
    </div>
  );

  const feedback = notice && <p role={notice.error ? 'alert' : 'status'} className={`settings-feedback${notice.error ? ' is-error' : ''}`}>{notice.error ? <CircleAlert size={15} /> : <Check size={15} />}<span>{notice.text}</span></p>;

  if (!state) {
    return <div className="settings-channel-page">
      <h3 id="settings-models-title">模型与 Agent</h3>
      {scope}{feedback}
      <div className="settings-load">{notice ? <button className="settings-button" onClick={() => { setNotice(undefined); setAttempt(n => n + 1); }}>重试</button> : <p className="settings-pending"><Loader2 size={16} className="animate-spin" />正在读取「{bot.name}」的设置…</p>}</div>
    </div>;
  }

  const footer = <footer className="settings-channels-footer">
    <span className={dirty && invalidReason ? 'is-warn' : undefined}>{dirty && invalidReason ? <CircleAlert size={13} /> : <Save size={13} />}{dirty ? invalidReason || '有未保存的修改' : `已保存 · 只影响「${bot.name}」`}</span>
    <div className="settings-footer-actions">
      {dirty && <button type="button" className="settings-button" disabled={busy} onClick={discard}><Undo2 size={14} />放弃修改</button>}
      <button type="button" className="settings-button settings-primary" disabled={busy || !dirty || !valid} onClick={save}>{saving && !work && <Loader2 size={14} className="animate-spin" />}保存设置</button>
    </div>
  </footer>;

  const routePicker = (id: string, value: string, setID: (v: string) => void, m: string, setM: (v: string) => void, label: string) => (
    <div className="settings-channel-form-grid">
      <div className="settings-field"><label htmlFor={`${label}-channel`}>API 渠道</label>
        <select id={`${label}-channel`} value={value} disabled={busy} onChange={e => { const p = channels.find(c => c.id === e.target.value); setID(e.target.value); setM(p ? visible(p)[0] ?? '' : ''); changed(); }}>
          <option value="">{label === 'chat' ? '不设置' : '选择渠道'}</option>
          {channels.map(p => <option key={p.id} value={p.id} disabled={!p.enabled && p.id !== value}>{p.name}{p.enabled ? '' : '（已停用）'}</option>)}
        </select></div>
      <div className="settings-field"><label htmlFor={`${label}-model`}>模型</label>
        <select id={`${label}-model`} value={m} disabled={busy} onChange={e => { setM(e.target.value); changed(); }}>
          <option value="">选择模型</option>
          {channels.find(p => p.id === id) && visible(channels.find(p => p.id === id)!).map(x => <option key={x}>{x}</option>)}
        </select></div>
    </div>
  );

  const noChannels = channels.length === 0;
  const fallback = state.env_override ? null : noChannels && <p className="model-card-warn"><CircleAlert size={14} />「{deviceName}」还没有 API 渠道。<button type="button" className="settings-link" onClick={onOpenChannels}>去添加 API 渠道</button></p>;
  const stale = (id: string) => { const c = channels.find(p => p.id === id); return id && (!c || !c.enabled); };
  const staleWarn = (id: string) => stale(id) && <p className="model-card-warn is-error"><CircleAlert size={14} />之前选的渠道已被停用或删除，请重新选择。</p>;
  const manage = !noChannels && <button type="button" className="settings-link model-manage" onClick={onOpenChannels}><Plug size={12} />管理 API 渠道</button>;

  return <div className="settings-channel-page">
    <div className="settings-channel-title"><div><h3 id="settings-models-title">模型与 Agent</h3></div></div>
    {scope}
    {state.env_override && <p className="settings-env-note">「{deviceName}」启动时设置了环境变量 BASE_URL / CHAT_MODEL，所有 Bot 的 API 模型暂时固定为：聊天 <code>{state.env_model}</code>、Agent <code>{state.env_agent_model}</code>。这里保存的内容要去掉环境变量并重启后才生效。</p>}
    {feedback}
    {others.length > 0 && target.local && <div className="model-copy">
      <Copy size={14} aria-hidden="true" /><span>沿用其他 Bot 的选择</span>
      <select aria-label="复制来源" value={copyFrom} disabled={busy} onChange={e => setCopyFrom(e.target.value)}>
        <option value="">选择 Bot…</option>
        {others.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
      </select>
      <button type="button" className="settings-button" disabled={busy || !copyFrom || dirty} title={dirty ? '先保存或放弃当前修改' : undefined} onClick={copySettings}>
        {work === 'copy' ? <Loader2 size={14} className="animate-spin" /> : <Copy size={14} />}复制
      </button>
      <small>复制聊天模型与 Agent 模型，会替换「{bot.name}」当前的选择。</small>
    </div>}

    <section className="model-card" aria-labelledby="chat-model-title">
      <header className="model-card-head"><span className="model-card-icon"><MessageSquare size={17} /></span>
        <div><h4 id="chat-model-title">聊天模型</h4><p>回复你的消息、决定什么时候派出后台任务。</p></div>
        <span className="settings-agent-status">{chatSet && chatValid && route ? `${route.name} · ${model}` : '未设置'}</span>
      </header>
      {noChannels ? fallback : <>{staleWarn(defaultID)}{routePicker(defaultID, defaultID, setDefaultID, model, setModel, 'chat')}{manage}</>}
      {!noChannels && !chatSet && <p className="settings-field-hint">没有聊天模型时，这个 Bot 不能回复消息。</p>}
    </section>

    <section className="model-card" aria-labelledby="agent-model-title">
      <header className="model-card-head"><span className="model-card-icon"><Workflow size={17} /></span>
        <div><h4 id="agent-model-title">后台 Agent 模型</h4><p>frog 内置 Agent，读写工作区文件、运行命令、跨设备协作。</p></div>
        <span className="settings-agent-status"><Bot size={13} style={{ display: 'inline', verticalAlign: '-2px', marginRight: 4 }} />{agentSet && agentValid && agentRoute ? `${agentRoute.name} · ${agentModel}` : '未设置'}</span>
      </header>
      <div className="agent-mode-body">
        {noChannels ? fallback : <>
          {staleWarn(agentProviderID)}
          {routePicker(agentProviderID, agentProviderID, setAgentProviderID, agentModel, setAgentModel, 'agent')}
          {chatValid && (agentProviderID !== defaultID || agentModel !== model) && <button type="button" className="settings-link agent-same" disabled={busy} onClick={() => { setAgentProviderID(defaultID); setAgentModel(model); changed(); }}>改为与聊天模型相同</button>}
        </>}
      </div>
    </section>
    {target.local && <PrivilegePanel target={target} deviceName={deviceName} />}
    {footer}
  </div>;
}

/** 提权：Agent 需要 root 时弹卡片，用户输入登录密码授权。这里显示能不能用、开关和免密窗口。 */
function PrivilegePanel({ target, deviceName }: { target: DeviceSettingsAPI; deviceName: string }) {
  const [st, setSt] = useState<PrivilegeStatus>();
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => {
    const c = new AbortController();
    target.privilege(c.signal).then(setSt).catch(() => {});
    return () => c.abort();
  }, [target]);
  if (!st) return null;
  const s = st.status;
  const grant = s?.grant_left_sec ? new Date(Date.now() + s.grant_left_sec * 1000) : undefined;
  const act = async (action: 'enable' | 'disable' | 'revoke') => {
    setBusy(true); setErr('');
    try { setSt(await target.privilegeAction(action, action === 'enable' ? password : undefined)); setPassword(''); }
    catch (e) { setErr(message(e)); } finally { setBusy(false); }
  };
  const text = st.available ? (grant ? `已开启 · 免密窗口到 ${grant.toLocaleTimeString()}` : '已开启 · 每条 root 命令都要输入登录密码')
    : st.reason || st.error || '不可用';
  return <div className="cli-installs">
    <div className="cli-installs-head"><h4><KeyRound size={12} /> 提权到 root</h4>
      <small>Agent 需要 root 时（例如装系统软件）会弹出卡片，你输入登录密码后由「{deviceName}」上的提权助手执行，并记入审计日志。</small></div>
    <ul><li className={st.available ? '' : 'is-failed'}>
      <div className="cli-install-text"><b>{st.sandbox.available ? 'Agent 沙箱已启用' : 'Agent 沙箱不可用'}</b><small title={text}>{text}</small></div>
      {st.available && grant && <button type="button" className="settings-button" disabled={busy} onClick={() => act('revoke')}>结束免密窗口</button>}
      {st.available && <button type="button" className="settings-button" disabled={busy} onClick={() => act('disable')}>关闭提权</button>}
      {!st.available && s?.configured && !s.enabled && <form className="cli-install-text" onSubmit={e => { e.preventDefault(); void act('enable'); }}>
        <input type="password" autoComplete="current-password" placeholder="登录密码" value={password} disabled={busy} onChange={e => setPassword(e.target.value)} />
        <button type="submit" className="settings-button" disabled={busy || !password}>开启提权</button>
      </form>}
    </li></ul>
    {err && <p className="settings-field-hint" style={{ color: 'var(--danger)' }}>{err}</p>}
  </div>;
}
