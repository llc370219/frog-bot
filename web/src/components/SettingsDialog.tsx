import { ArrowUpRight, Check, ChevronDown, CircleAlert, Cpu, KeyRound, LogOut, Monitor, Plug, RefreshCw, X } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api, createDeviceSettingsAPI, type AuthInfo, type DeviceGroup, type DeviceSettingsAPI, type UpdateStatus } from '../api';
import BotModelSettings from './BotModelSettings';
import ChannelsPage from './ChannelsPage';

const releasesPage = 'https://github.com/llc370219/frog-bot/releases';
const message = (err: unknown) => err instanceof TypeError ? '无法连接 frog 后端，请确认服务已启动'
  : err instanceof Error ? err.message : '请求失败，请重试';

type Tab = 'models' | 'api' | 'update' | 'account';
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
  const [tab, setTab] = useState<Tab>('models');
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
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

  function go(next: Tab) {
    if (next === tab || saving) return;
    if (dirty && !window.confirm('当前页有未保存的修改，确定离开吗？')) return;
    setDirty(false); setTab(next);
  }

  function close() {
    if (saving) return;
    if (dirty && !window.confirm('有未保存的修改，确定关闭设置吗？')) return;
    onClose();
  }

  const navButton = (id: Tab, icon: React.ReactNode, label: string) => (
    <button type="button" aria-current={tab === id ? 'page' : undefined} aria-controls={`settings-${id}`} disabled={saving} onClick={() => go(id)}>
      {icon} {label}
    </button>
  );

  return (
    <dialog ref={dialog} className="settings-dialog" aria-labelledby="settings-title"
      onCancel={(e) => { e.preventDefault(); close(); }}>
      <aside className="settings-sidebar">
        <div className="settings-sidebar-heading">
          <h2 ref={title} id="settings-title" tabIndex={-1}>设置</h2>
          {devices.length > 1 && (
            <label className="sidebar-device-picker" title={dirty ? '先保存或放弃修改，再切换设备' : `管理设备：${device.name}`}>
              <Monitor className="sidebar-device-icon" size={13} aria-hidden="true" />
              <span className="sidebar-device-name">{device.name}</span>
              <ChevronDown className="sidebar-device-chevron" size={12} aria-hidden="true" />
              <select aria-label="管理设备" value={device.id} disabled={saving || dirty} onChange={e => { const next = devices.find(d => d.id === e.target.value); setSelectedDevice(e.target.value); setSelectedBot(next?.bots[0]?.id ?? 'main'); }}>
                {devices.map(d => <option key={d.id} value={d.id}>{d.name}{d.id === 'local' ? '（本机）' : d.online ? '' : '（离线）'}</option>)}
              </select>
            </label>
          )}
        </div>
        <nav className="settings-nav" aria-label="设置分类">
          {navButton('models', <Cpu size={16} aria-hidden="true" />, '模型与 Agent')}
          {navButton('api', <Plug size={16} aria-hidden="true" />, 'API 渠道')}
          {navButton('update', <RefreshCw size={15} aria-hidden="true" />, '版本与互联')}
          {showAccount && navButton('account', <KeyRound size={15} aria-hidden="true" />, '账号与登录')}
        </nav>
      </aside>
      <button type="button" className="settings-close" aria-label="关闭设置" disabled={saving} onClick={close}>
        <X size={19} />
      </button>
      <div className="settings-content">
        <section id="settings-models" className="settings-pane" hidden={tab !== 'models'} aria-labelledby="settings-models-title">
          {tab === 'models' && bot && <BotModelSettings key={`${device.id}/${bot.id}`} target={target}
            bot={bot} bots={device.bots} deviceName={device.name} saving={saving} setSaving={setSaving} onDirty={setDirty}
            onSelectBot={setSelectedBot} onOpenChannels={() => go('api')} onSaved={() => onSettingsSaved(device.id)} />}
        </section>
        <section id="settings-api" className="settings-pane" hidden={tab !== 'api'} aria-labelledby="settings-api-title">
          {tab === 'api' && <ChannelsPage key={device.id} target={target} deviceName={device.name} saving={saving} setSaving={setSaving}
            onDirty={setDirty} onSaved={() => onSettingsSaved(device.id)} />}
        </section>
        <section id="settings-update" className="settings-pane" hidden={tab !== 'update'} aria-labelledby="settings-update-title">
          {tab === 'update' && <UpdatePanel key={device.id} target={target} deviceName={device.name} />}
        </section>
        {showAccount && <section id="settings-account" className="settings-pane" hidden={tab !== 'account'} aria-labelledby="settings-account-title">
          <AccountPanel username={auth?.username ?? ''} />
        </section>}
      </div>
    </dialog>
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
      <div className="settings-devices-note">
        <h4>设备互联</h4>
        <p>在各台设备的数据目录配置 <code>devices.json</code> 并重启 frog。连通后，侧栏会按设备显示 Bot，也可以在这里切换设备，分别设置每台设备上每个 Bot 的模型。</p>
      </div>
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
