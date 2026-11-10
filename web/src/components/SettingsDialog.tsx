import { ArrowUpRight, Check, ChevronDown, CircleAlert, KeyRound, Link2, LogOut, Monitor, Plug, RefreshCw, X } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api, createDeviceSettingsAPI, type AgentConnections, type AuthInfo, type DeviceGroup, type DeviceSettingsAPI, type UpdateStatus } from '../api';
import APIChannels from './APIChannels';
import { BotAvatar } from './BotAvatar';

const releasesPage = 'https://github.com/llc370219/frog-bot/releases';
const message = (err: unknown) => err instanceof TypeError ? '无法连接 Frog Bot 后端，请确认服务已启动'
  : err instanceof Error ? err.message : '请求失败，请重试';

type Tab = 'api' | 'connections' | 'update' | 'account';
type Feedback = { error: boolean; text: string };

export default function SettingsDialog({ onClose, devices, initialDevice, initialBot, onSettingsSaved }: {
  onClose: () => void;
  devices: DeviceGroup[];
  initialDevice: string;
  initialBot: string;
  onSettingsSaved: (device: string) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const [tab, setTab] = useState<Tab>('api');
  const [saving, setSaving] = useState(false);
  const [auth, setAuth] = useState<AuthInfo>();
  const [selectedDevice, setSelectedDevice] = useState(initialDevice);
  const device = devices.find(d => d.id === selectedDevice) ?? devices.find(d => d.id === initialDevice) ?? devices[0];
  const [selectedBot, setSelectedBot] = useState(initialBot);
  const bot = device.bots.find(b => b.id === selectedBot) ?? device.bots[0];
  const target = useMemo(() => createDeviceSettingsAPI(device.id, bot?.id ?? 'main'), [device.id, bot?.id]);
  const showAccount = device.id === 'local' && auth?.enabled;

  useEffect(() => { api.me().then(setAuth).catch(() => {}); }, []);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    title.current?.focus();
    return () => el?.close();
  }, []);

  return (
    <dialog ref={dialog} className="settings-dialog" aria-labelledby="settings-title"
      onCancel={(e) => { e.preventDefault(); if (!saving) onClose(); }}>
      <aside className="settings-sidebar">
        <div className="settings-sidebar-heading">
          <h2 ref={title} id="settings-title" tabIndex={-1}>设置</h2>
          {devices.length > 1 && (
            <label className="sidebar-device-picker" title={`管理设备：${device.name}`}>
              <Monitor className="sidebar-device-icon" size={13} aria-hidden="true" />
              <span className="sidebar-device-name">{device.name}</span>
              <ChevronDown className="sidebar-device-chevron" size={12} aria-hidden="true" />
              <select aria-label="管理设备" value={device.id} disabled={saving} onChange={e => { const next = devices.find(d => d.id === e.target.value); setSelectedDevice(e.target.value); setSelectedBot(next?.bots[0]?.id ?? 'main'); }}>
                {devices.map(d => <option key={d.id} value={d.id}>{d.name}{d.id === 'local' ? '（本机）' : d.online ? '' : '（离线）'}</option>)}
              </select>
            </label>
          )}
          {device.bots.length > 1 && (
            <label className="sidebar-device-picker" title={`管理 Bot：${bot?.name ?? 'Frog'}`}>
              <BotAvatar id={bot?.id ?? 'frog'} slotIndex={bot?.avatarSlot ?? 0} size={15} />
              <span className="sidebar-device-name">{bot?.name ?? 'Frog'}</span>
              <ChevronDown className="sidebar-device-chevron" size={12} aria-hidden="true" />
              <select aria-label="管理 Bot" value={bot?.id ?? 'main'} disabled={saving} onChange={e => setSelectedBot(e.target.value)}>
                {device.bots.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
            </label>
          )}
        </div>
        <nav className="settings-nav" aria-label="设置分类">
          <button type="button" aria-current={tab === 'api' ? 'page' : undefined} aria-controls="settings-api" disabled={saving} onClick={() => setTab('api')}>
            <Plug size={16} aria-hidden="true" /> API 渠道
          </button>
          <button type="button" aria-current={tab === 'connections' ? 'page' : undefined} aria-controls="settings-connections" disabled={saving} onClick={() => setTab('connections')}>
            <Link2 size={16} aria-hidden="true" /> 连接
          </button>
          <button type="button" aria-current={tab === 'update' ? 'page' : undefined} aria-controls="settings-update" disabled={saving} onClick={() => setTab('update')}>
            <RefreshCw size={15} aria-hidden="true" /> 版本更新
          </button>
          {showAccount && <button type="button" aria-current={tab === 'account' ? 'page' : undefined} aria-controls="settings-account" disabled={saving} onClick={() => setTab('account')}>
            <KeyRound size={15} aria-hidden="true" /> 账号与登录
          </button>}
        </nav>
      </aside>
      <button type="button" className="settings-close" aria-label="关闭设置" disabled={saving} onClick={onClose}>
        <X size={19} />
      </button>
      <div className="settings-content">
        <section id="settings-api" className="settings-pane" hidden={tab !== 'api'} aria-labelledby="settings-api-title">
          <APIChannels key={device.id} target={target} saving={saving} setSaving={setSaving}
            onSaved={() => onSettingsSaved(device.id)} />
        </section>
        <section id="settings-update" className="settings-pane" hidden={tab !== 'update'} aria-labelledby="settings-update-title">
          {tab === 'update' && <UpdatePanel key={device.id} target={target} deviceName={device.name} />}
        </section>
        {showAccount && <section id="settings-account" className="settings-pane" hidden={tab !== 'account'} aria-labelledby="settings-account-title">
          <AccountPanel username={auth?.username ?? ''} />
        </section>}
        <section id="settings-connections" className="settings-pane" hidden={tab !== 'connections'} aria-labelledby="settings-connections-title">
          {tab === 'connections' && <ConnectionsPanel key={device.id} target={target} deviceName={device.name} saving={saving} setSaving={setSaving} />}
        </section>
      </div>
    </dialog>
  );
}

const builtinAgentName = 'Frog';
const agentIcons: Record<string, string> = {
  codex: 'codex.png', claude: 'claude.png', pi: 'pi.svg', grok: 'grok.svg', agy: 'agy.svg',
};

function ConnectionsPanel({ target, deviceName, saving, setSaving }: {
  target: DeviceSettingsAPI; deviceName: string; saving: boolean; setSaving: (value: boolean) => void;
}) {
  const [connections, setConnections] = useState<AgentConnections>();
  const [checking, setChecking] = useState(true);
  const [feedback, setFeedback] = useState<Feedback>();
  const busy = checking || saving;

  async function detect(signal?: AbortSignal) {
    setChecking(true);
    setFeedback(undefined);
    try {
      const result = await target.connections(signal);
      if (!Array.isArray(result.clis)) throw new Error('请更新并重启 Frog Bot 后端后重新检测');
      setConnections(result);
    } catch (err) {
      if (!signal?.aborted) setFeedback({ error: true, text: message(err) });
    } finally {
      if (!signal?.aborted) setChecking(false);
    }
  }

  useEffect(() => {
    const request = new AbortController();
    void detect(request.signal);
    return () => request.abort();
  }, []);

  async function select(id: string) {
    setSaving(true);
    setFeedback(undefined);
    try {
      const result = await target.saveConnection(id);
      setConnections(result);
      const name = result.clis.find(cli => cli.id === result.cli)?.name ?? builtinAgentName;
      setFeedback({ error: false, text: `「${deviceName}」上的新任务将使用 ${name}` });
    } catch (err) {
      setFeedback({ error: true, text: message(err) });
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <h3 id="settings-connections-title">连接</h3>
      <p className="settings-description">为当前 Bot 的后台任务选择执行方式。选择 Codex CLI 或 Claude Code 时，任务通过该设备上的 CLI 登录运行，可使用对应官方账户订阅；CLI 登录需先在服务器终端完成。</p>
      <div className="settings-agents-heading">
        <h4>Agent 工具</h4>
        <button type="button" className="settings-button" disabled={busy} onClick={() => void detect()}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : ''} aria-hidden="true" />
          {checking ? '正在检测…' : '重新检测'}
        </button>
      </div>
      {connections && <ul className="settings-agents" aria-label="Agent 工具">
        {[{ id: '', name: builtinAgentName, installed: true }, ...connections.clis].map((agent) => {
          const icon = agentIcons[agent.id];
          const active = agent.id === connections.cli;
          return <li key={agent.id} className="settings-agent">
            <div className="settings-agent-head">
              <span className="settings-agent-icon" aria-hidden="true">
                {!agent.id ? <BotAvatar id="frog" slotIndex={2} size={22} />
                  : icon ? <img src={`/assets/agent-icons/${icon}`} alt="" width={22} height={22} />
                  : <Plug size={20} />}
              </span>
              <h5>{agent.name}</h5>
            </div>
            <p className="settings-agent-description">{agent.id ? <><code>{agent.id}</code> · {agent.installed ? '已安装 · 使用 CLI 自身认证' : '未检测到'}</> : '使用上方 Agent API 模型'}</p>
            <div className="settings-agent-actions">
              {active
                ? <span className="settings-agent-status"><Check size={12} aria-hidden="true" /> 当前选择</span>
                : <button type="button" className="settings-button settings-agent-connect" disabled={busy || !agent.installed} aria-label={`使用 ${agent.name}`} onClick={() => void select(agent.id)}>使用</button>}
            </div>
          </li>;
        })}
      </ul>}
      {feedback && <p className={`settings-agents-note ${feedback.error ? 'text-danger' : 'text-ok'}`} role={feedback.error ? 'alert' : 'status'}>{feedback.text}</p>}
      <p className="settings-agents-note">选择 CLI 会让该 Bot 的后台任务使用 CLI 自身的认证与模型设置；聊天回复仍使用 API 渠道页中单独配置的聊天模型。API 密钥与 Bot 配置彼此隔离。</p>
      <div className="settings-agents-note">
        <h4 className="font-medium text-fg">设备互联</h4>
        <p>在各台设备的数据目录配置 <code>devices.json</code> 并重启 Frog Bot。连通后，侧栏会按设备显示 Bot。</p>
      </div>
    </>
  );
}

function UpdatePanel({ target, deviceName }: { target: DeviceSettingsAPI; deviceName: string }) {
  const [version, setVersion] = useState<string>();
  const [status, setStatus] = useState<UpdateStatus>();
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    const request = new AbortController();
    target.version(request.signal).then(v => { if (!request.signal.aborted) setVersion(v); }).catch(() => {});
    return () => request.abort();
  }, [target]);

  async function check() {
    setChecking(true);
    setError('');
    try {
      setStatus(await target.checkUpdate());
    } catch (err) {
      setStatus(undefined);
      setError(message(err));
    } finally {
      setChecking(false);
    }
  }

  return (
    <>
      <h3 id="settings-update-title">版本更新</h3>
      <p className="settings-description">查看「{deviceName}」正在运行的版本，并检查 GitHub 上的最新发布。</p>
      <dl className="settings-versions">
        <div><dt>当前版本</dt><dd>{status?.current ?? version ?? '—'}</dd></div>
        {status?.latest && <div><dt>最新发布</dt><dd>{status.latest}</dd></div>}
      </dl>
      {error && <FeedbackLine feedback={{ error: true, text: error }} />}
      {status && !checking && <p role="status" className="settings-update-note">{{
        current: '当前版本无需更新。',
        available: '有新版本可用。请在目标设备安装新版并重启。',
        no_release: 'GitHub 上暂无可用的正式发布。',
        unversioned: '可以前往发布页查看最新版本。',
      }[status.state]}</p>}
      <div className="settings-actions settings-update-actions">
        <a className="settings-link" href={status?.release_url?.startsWith(releasesPage + '/tag/') ? status.release_url : releasesPage} target="_blank" rel="noreferrer">
          查看发布记录 <ArrowUpRight size={14} aria-hidden="true" />
        </a>
        <button type="button" className="settings-button settings-primary" onClick={check} disabled={checking}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : undefined} aria-hidden="true" />
          {checking ? '检查中…' : '检查更新'}
        </button>
      </div>
      <p className="settings-update-note">这里可以检查版本。安装新版和重启仍在「{deviceName}」上完成。</p>
    </>
  );
}

function AccountPanel({ username }: { username: string }) {
  const [oldPw, setOldPw] = useState('');
  const [newPw, setNewPw] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback>();

  async function change(e: React.FormEvent) {
    e.preventDefault();
    if (newPw !== again) { setFeedback({ error: true, text: '两次输入的新密码不一样' }); return; }
    setBusy(true);
    setFeedback(undefined);
    try {
      await api.changePassword(oldPw, newPw);
      setOldPw(''); setNewPw(''); setAgain('');
      setFeedback({ error: false, text: '密码已修改。其他设备上的登录已失效，需要用新密码重新登录。' });
    } catch (err) {
      setFeedback({ error: true, text: message(err) });
    } finally {
      setBusy(false);
    }
  }

  async function logout() {
    await api.logout().catch(() => {});
    location.assign('/login');
  }

  return (
    <>
      <h3 id="settings-account-title">账号与登录</h3>
      <p className="settings-description">服务器模式下，打开管理页面需要登录。当前用户：<b>{username}</b></p>
      <form className="settings-account-form" onSubmit={change}>
        <div className="settings-field"><label htmlFor="pw-old">当前密码</label>
          <input id="pw-old" type="password" autoComplete="current-password" required value={oldPw} onChange={(e) => setOldPw(e.target.value)} /></div>
        <div className="settings-field"><label htmlFor="pw-new">新密码</label>
          <input id="pw-new" type="password" autoComplete="new-password" required minLength={8} value={newPw} onChange={(e) => setNewPw(e.target.value)} />
          <p className="settings-field-hint">至少 8 位。忘记密码时可以在服务器上运行 sudo frogbot reset-password。</p></div>
        <div className="settings-field"><label htmlFor="pw-again">再输一次新密码</label>
          <input id="pw-again" type="password" autoComplete="new-password" required minLength={8} value={again} onChange={(e) => setAgain(e.target.value)} /></div>
        {feedback && <FeedbackLine feedback={feedback} />}
        <div className="settings-actions">
          <button type="button" className="settings-button" onClick={logout}><LogOut size={14} aria-hidden="true" />退出登录</button>
          <button type="submit" className="settings-button settings-primary" disabled={busy || !oldPw || !newPw}>{busy ? '正在修改…' : '修改密码'}</button>
        </div>
      </form>
    </>
  );
}

function FeedbackLine({ feedback }: { feedback: Feedback }) {
  return (
    <p role={feedback.error ? 'alert' : 'status'} className={feedback.error ? 'settings-feedback is-error' : 'settings-feedback'}>
      {feedback.error ? <CircleAlert size={15} aria-hidden="true" /> : <Check size={15} aria-hidden="true" />}
      <span>{feedback.text}</span>
    </p>
  );
}
