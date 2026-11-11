import { BadgeCheck, Bot, Check, CircleAlert, Copy, Loader2, MessageSquare, Plug, RefreshCw, Save, Terminal, Undo2, Workflow } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { AgentCLI, AgentConnection, AgentConnections, APIChannel, APISettings, BotProfile, CLIAuth, DeviceSettingsAPI } from '../api';
import { BotAvatar } from './BotAvatar';

const message = (e: unknown) => e instanceof TypeError ? '无法连接 frog 后端，请确认服务已启动'
  : e instanceof Error ? e.message : '请求失败，请重试';
const visible = (p: APIChannel) => p.models.filter(m => !p.hidden_models.includes(m));
const agentIcons: Record<string, string> = { codex: 'codex.png', claude: 'claude.png', pi: 'pi.svg', grok: 'grok.svg', agy: 'agy.svg' };
type Notice = { error: boolean; text: string };
type AgentMode = 'api' | 'cli';

function sameConnection(a?: AgentConnection, b?: AgentConnection) {
  return (a?.cli ?? '') === (b?.cli ?? '') && (a?.cli_model ?? '') === (b?.cli_model ?? '') && (a?.cli_auth ?? 'subscription') === (b?.cli_auth ?? 'subscription');
}

/**
 * 一个 Bot 的模型设置：聊天模型和 Agent（API 模型或官方订阅 CLI）。API 渠道是整台设备共用的，
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
  const [settingsDirty, setSettingsDirty] = useState(false);
  const [conn, setConn] = useState<AgentConnections>();
  const [mode, setMode] = useState<AgentMode>('api');
  const [cli, setCli] = useState('');
  const [cliModel, setCliModel] = useState('');
  const [cliAuth, setCliAuth] = useState<CLIAuth>('subscription');
  const [detecting, setDetecting] = useState(false);
  const [copyFrom, setCopyFrom] = useState('');
  const [work, setWork] = useState('');
  const [notice, setNotice] = useState<Notice>();
  const [attempt, setAttempt] = useState(0);

  function applySettings(s: APISettings) {
    const rows = (s.providers ?? []).map(p => ({ ...p, models: p.models ?? [], hidden_models: p.hidden_models ?? [] }));
    setState(s); setChannels(rows); setDefaultID(s.default_provider_id ?? '');
    setModel(s.model); setAgentProviderID(s.agent_provider_id || s.default_provider_id || '');
    setAgentModel(s.agent_model || s.model); setSettingsDirty(false);
  }
  function applyConnection(c: AgentConnections) {
    setConn(c);
    const first = c.clis.find(x => x.installed)?.id ?? c.clis[0]?.id ?? '';
    setMode(c.cli ? 'cli' : 'api'); setCli(c.cli || first); setCliModel(c.cli_model ?? ''); setCliAuth(c.cli_auth ?? 'subscription');
  }
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    Promise.all([target.settings(signal), target.connections(signal)]).then(([s, c]) => {
      if (signal.aborted) return;
      if (!Array.isArray(c.clis)) throw new Error('请更新并重启 frog 后端后重试');
      applySettings(s); applyConnection(c);
    }).catch(e => { if (!signal.aborted) setNotice({ error: true, text: message(e) }); });
    return () => controller.abort();
  }, [attempt, target]);

  const draftConnection: AgentConnection = mode === 'cli' ? { cli, cli_model: cliModel.trim(), cli_auth: cliAuth } : { cli: '' };
  const connDirty = !!conn && !sameConnection(conn, draftConnection);
  const dirty = settingsDirty || connDirty;
  useEffect(() => { onDirty(dirty); }, [dirty]);

  const route = channels.find(p => p.id === defaultID);
  const agentRoute = channels.find(p => p.id === agentProviderID);
  const selectedCLI = conn?.clis.find(x => x.id === cli);
  const busy = saving || !!work;
  const chatSet = !!defaultID || !!model;
  const chatValid = !chatSet || (!!route?.enabled && visible(route).includes(model));
  const agentSet = mode === 'api' && (!!agentProviderID || !!agentModel);
  const agentValid = !agentSet || (!!agentRoute?.enabled && visible(agentRoute).includes(agentModel));
  const cliValid = mode === 'api' || (!!selectedCLI?.installed);
  const valid = chatValid && agentValid && cliValid;
  const invalidReason = !chatValid ? '聊天：请选择已启用渠道里的模型'
    : !agentValid ? 'Agent：请选择已启用渠道里的模型' : !cliValid ? '所选 CLI 未安装' : '';
  const others = bots.filter(b => b.id !== bot.id);

  function changed() { setSettingsDirty(true); setNotice(undefined); }

  async function save() {
    if (!state || !valid) return;
    setSaving(true); setNotice(undefined);
    try {
      if (settingsDirty) {
        applySettings(await target.saveRoutes({ default_provider_id: defaultID, model, agent_provider_id: agentProviderID, agent_model: agentModel }));
      }
      if (connDirty) applyConnection(await target.saveConnection(draftConnection));
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
      applyConnection(await target.connections());
      setNotice({ error: false, text: `已复制「${bots.find(b => b.id === copyFrom)?.name}」的模型和 Agent 设置，之后两个 Bot 各自独立。` });
      setCopyFrom(''); onSaved();
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setWork(''); setSaving(false); }
  }
  async function detect() {
    setDetecting(true);
    try {
      const c = await target.connections();
      setConn(prev => prev ? { ...prev, clis: c.clis } : c);
    } catch (e) { setNotice({ error: true, text: message(e) }); } finally { setDetecting(false); }
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

  if (!state || !conn) {
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
    <div className="settings-channel-title"><div><h3 id="settings-models-title">模型与 Agent</h3>
      <p className="settings-description">API 渠道在整台设备共用，这里为每个 Bot 单独选择聊天模型和后台 Agent，互不影响。</p></div></div>
    {scope}
    {state.env_override && <p className="settings-env-note">「{deviceName}」启动时设置了环境变量 BASE_URL / CHAT_MODEL，所有 Bot 的 API 模型暂时固定为：聊天 <code>{state.env_model}</code>、Agent <code>{state.env_agent_model}</code>。这里保存的内容要去掉环境变量并重启后才生效；官方订阅 CLI 不受影响。</p>}
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
      <small>复制聊天模型、Agent 模型和执行方式，会替换「{bot.name}」当前的选择。</small>
    </div>}

    <section className="model-card" aria-labelledby="chat-model-title">
      <header className="model-card-head"><span className="model-card-icon"><MessageSquare size={17} /></span>
        <div><h4 id="chat-model-title">聊天模型</h4><p>回复你的消息、决定什么时候派出后台任务。</p></div>
        <span className="settings-agent-status">{chatSet && chatValid && route ? `${route.name} · ${model}` : !chatSet ? (mode === 'cli' ? '未设置 · 消息直接交给 CLI' : '未设置') : '请选择模型'}</span>
      </header>
      {noChannels ? fallback : <>{staleWarn(defaultID)}{routePicker(defaultID, defaultID, setDefaultID, model, setModel, 'chat')}{manage}</>}
      {!noChannels && !chatSet && mode === 'api' && <p className="settings-field-hint">没有聊天模型时，这个 Bot 不能回复消息；只用官方订阅 CLI 时可以不设置。</p>}
    </section>

    <section className="model-card" aria-labelledby="agent-model-title">
      <header className="model-card-head"><span className="model-card-icon"><Workflow size={17} /></span>
        <div><h4 id="agent-model-title">Agent</h4><p>执行后台任务：读写工作区文件、运行命令、调用工具。</p></div>
      </header>
      <div className="agent-mode" role="radiogroup" aria-label="Agent 执行方式">
        <button type="button" role="radio" aria-checked={mode === 'api'} disabled={busy} onClick={() => setMode('api')}>
          <Bot size={16} /><span><b>API 模型</b><small>frog 内置 Agent，按 API 用量计费</small></span>
        </button>
        <button type="button" role="radio" aria-checked={mode === 'cli'} disabled={busy} onClick={() => setMode('cli')}>
          <BadgeCheck size={16} /><span><b>官方订阅</b><small>Claude Code、Codex 等 CLI，用登录的订阅账户</small></span>
        </button>
      </div>
      {mode === 'api' ? <div className="agent-mode-body">
        {noChannels ? fallback : <>
          {staleWarn(agentProviderID)}
          {routePicker(agentProviderID, agentProviderID, setAgentProviderID, agentModel, setAgentModel, 'agent')}
          {chatValid && (agentProviderID !== defaultID || agentModel !== model) && <button type="button" className="settings-link agent-same" disabled={busy} onClick={() => { setAgentProviderID(defaultID); setAgentModel(model); changed(); }}>改为与聊天模型相同</button>}
        </>}
      </div> : <div className="agent-mode-body">
        <div className="settings-agents-heading"><h4>选择 CLI</h4>
          <button type="button" className="settings-button" disabled={busy || detecting} onClick={detect}><RefreshCw size={14} className={detecting ? 'animate-spin' : ''} />{detecting ? '正在检测…' : '重新检测'}</button></div>
        <ul className="agent-cli-grid" role="radiogroup" aria-label="CLI">
          {conn.clis.map(c => <li key={c.id}><button type="button" role="radio" aria-checked={c.id === cli} className="agent-cli" disabled={busy || !c.installed}
            onClick={() => { setCli(c.id); if (c.id !== cli) setCliModel(c.id === conn.cli ? conn.cli_model ?? '' : ''); }}>
            <span className="settings-agent-icon">{agentIcons[c.id] ? <img src={`/assets/agent-icons/${agentIcons[c.id]}`} alt="" width={22} height={22} /> : <Terminal size={18} />}</span>
            <span className="agent-cli-text"><b>{c.name}</b><small>{c.installed ? c.subscription || '已安装' : '未检测到'}</small></span>
            {c.id === cli && <Check size={14} className="agent-cli-check" />}
          </button></li>)}
        </ul>
        {selectedCLI && <CLIDetails c={selectedCLI} model={cliModel} setModel={setCliModel} auth={cliAuth} setAuth={setCliAuth} busy={busy} deviceName={deviceName} />}
        {!chatSet && <p className="settings-field-hint">这个 Bot 没有聊天模型时，你发的消息会直接交给所选 CLI 处理。</p>}
      </div>}
    </section>
    {footer}
  </div>;
}

function CLIDetails({ c, model, setModel, auth, setAuth, busy, deviceName }: {
  c: AgentCLI; model: string; setModel: (v: string) => void; auth: CLIAuth; setAuth: (v: CLIAuth) => void; busy: boolean; deviceName: string;
}) {
  const listID = `cli-models-${c.id}`;
  return <div className="agent-cli-details">
    <div className="settings-field">
      <label htmlFor="cli-model">模型</label>
      {c.model_option ? <>
        <input id="cli-model" list={c.models?.length ? listID : undefined} value={model} disabled={busy} spellCheck={false} autoComplete="off"
          placeholder={`留空使用 ${c.name} 的默认模型`} onChange={e => setModel(e.target.value)} />
        {c.models?.length ? <datalist id={listID}>{c.models.map(m => <option key={m} value={m} />)}</datalist> : null}
        <p className="settings-field-hint">{c.models?.length ? `可填 ${c.models.join(' / ')} 或完整模型 ID。` : '填写该 CLI 支持的模型 ID。'}订阅可用的模型以你的套餐为准。</p>
      </> : <p className="settings-field-hint agent-cli-static">使用 {c.name} 自己的模型设置（在 CLI 内调整）。</p>}
    </div>
    <fieldset className="agent-auth" disabled={busy}>
      <legend>认证</legend>
      <label className={auth === 'subscription' ? 'is-on' : ''}><input type="radio" name="cli-auth" checked={auth === 'subscription'} onChange={() => setAuth('subscription')} />
        <span><b>官方订阅（推荐）</b><small>只用 CLI 登录的 {c.subscription ?? '官方'} 账户；即使设备上设置了 API Key 环境变量也不会改走按量计费。</small></span></label>
      <label className={auth === 'env' ? 'is-on' : ''}><input type="radio" name="cli-auth" checked={auth === 'env'} onChange={() => setAuth('env')} />
        <span><b>沿用设备环境变量</b><small>保留 frog 进程里的 API Key / Base URL 环境变量，由 CLI 自行决定用哪种认证。</small></span></label>
    </fieldset>
    {c.login && <p className="agent-login"><Terminal size={13} />还没登录？在「{deviceName}」的终端运行 <code>{c.login}</code> 完成登录。</p>}
  </div>;
}
