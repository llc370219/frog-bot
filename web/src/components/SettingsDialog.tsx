import { ArrowUpRight, Check, ChevronDown, CircleAlert, Cpu, Download, KeyRound, Loader2, LogOut, Monitor, Plug, RefreshCw, ShieldCheck, X } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, createDeviceSettingsAPI, type AuthInfo, type DeviceGroup, type DeviceSettingsAPI, type UpdateChannel, type UpdateStatus } from '../api';
import BotModelSettings from './BotModelSettings';
import ChannelsPage from './ChannelsPage';
import RemoteSettings from './RemoteSettings';

const releasesPage = 'https://github.com/llc370219/frog-bot/releases';
const message = (err: unknown) => err instanceof TypeError ? '无法连接后端服务'
  : err instanceof Error ? err.message : '请求失败';

type Tab = 'models' | 'api' | 'remote' | 'update' | 'account';
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
    if (dirty && !window.confirm('放弃未保存的修改？')) return;
    setDirty(false); setTab(next);
  }

  function close() {
    if (saving) return;
    if (dirty && !window.confirm('放弃未保存的修改？')) return;
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
            <label className="sidebar-device-picker" title={device.name}>
              <Monitor className="sidebar-device-icon" size={13} aria-hidden="true" />
              <span className="sidebar-device-name">{device.name}</span>
              <ChevronDown className="sidebar-device-chevron" size={12} aria-hidden="true" />
              <select aria-label="管理设备" value={device.id} disabled={saving || dirty} onChange={e => { const next = devices.find(d => d.id === e.target.value); setSelectedDevice(e.target.value); setSelectedBot(next?.bots[0]?.id ?? 'main'); }}>
                {devices.map(d => <option key={d.id} value={d.id}>{d.name}{d.online ? '' : ' · 离线'}</option>)}
              </select>
            </label>
          )}
        </div>
        <nav className="settings-nav" aria-label="设置分类">
          {navButton('models', <Cpu size={16} aria-hidden="true" />, '模型与 Agent')}
          {navButton('api', <Plug size={16} aria-hidden="true" />, 'API 渠道')}
          {device.id === 'local' && navButton('remote', <ShieldCheck size={15} aria-hidden="true" />, '加密隧道')}
          {navButton('update', <RefreshCw size={15} aria-hidden="true" />, '软件版本')}
          {showAccount && navButton('account', <KeyRound size={15} aria-hidden="true" />, '账号')}
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
        {device.id === 'local' && (
          <section id="settings-remote" className="settings-pane" hidden={tab !== 'remote'} aria-labelledby="settings-remote-title">
            {tab === 'remote' && <RemoteSettings saving={saving} setSaving={setSaving} />}
          </section>
        )}
        <section id="settings-update" className="settings-pane" hidden={tab !== 'update'} aria-labelledby="settings-update-title">
          {tab === 'update' && <UpdatePanel key={device.id} target={target} />}
        </section>
        {showAccount && <section id="settings-account" className="settings-pane" hidden={tab !== 'account'} aria-labelledby="settings-account-title">
          <AccountPanel username={auth?.username ?? ''} />
        </section>}
      </div>
    </dialog>
  );
}

function UpdatePanel({ target }: { target: DeviceSettingsAPI }) {
  const [channel, setChannel] = useState<UpdateChannel>('stable');
  const [status, setStatus] = useState<UpdateStatus>();
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [askPassword, setAskPassword] = useState(false);
  const [password, setPassword] = useState('');
  const [restarting, setRestarting] = useState(false);

  const check = useCallback(async (ch?: UpdateChannel) => {
    setChecking(true);
    setError('');
    try {
      const st = await target.checkUpdate(ch);
      setStatus(st);
      if (st.channel) setChannel(st.channel);
    } catch (err) {
      setError(message(err));
    } finally {
      setChecking(false);
    }
  }, [target]);

  useEffect(() => { void check(); }, [check]);

  const switchChannel = (next: UpdateChannel) => {
    if (next === channel || checking || upgrading) return;
    setChannel(next);
    setAskPassword(false);
    void check(next);
  };

  const applyUpgrade = async () => {
    setUpgrading(true);
    setError('');
    try {
      const res = await target.applyUpdate(channel, password);
      if (res.need_password) {
        setAskPassword(true);
        setUpgrading(false);
        return;
      }
      setRestarting(true);
      setPassword('');
      window.setTimeout(() => {
        const timer = window.setInterval(() => {
          fetch('/api/health').then((r) => {
            if (r.ok) {
              window.clearInterval(timer);
              window.location.reload();
            }
          }).catch(() => {});
        }, 1500);
      }, 1500);
    } catch (err) {
      setError(message(err));
      setUpgrading(false);
    }
  };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="settings-update-title">软件版本</h3>
        <div className="grid grid-cols-2 gap-1 rounded-xl bg-s2 p-1 text-xs" role="tablist" aria-label="更新通道">
          <button
            type="button"
            role="tab"
            aria-selected={channel === 'stable'}
            onClick={() => switchChannel('stable')}
            className={`rounded-lg px-3.5 py-1.5 font-medium transition ${channel === 'stable' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3 hover:text-fg'}`}
          >
            正式版
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={channel === 'beta'}
            onClick={() => switchChannel('beta')}
            className={`rounded-lg px-3.5 py-1.5 font-medium transition ${channel === 'beta' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3 hover:text-fg'}`}
          >
            Beta
          </button>
        </div>
      </div>

      <dl className="settings-versions">
        <div><dt>当前版本</dt><dd>{status?.current ?? '—'}</dd></div>
        <div><dt>{channel === 'beta' ? '最新 Beta' : '最新正式版'}</dt><dd>{checking ? '…' : (status?.latest ?? '—')}</dd></div>
      </dl>

      {error && <FeedbackLine feedback={{ error: true, text: error }} />}
      {restarting && <FeedbackLine feedback={{ error: false, text: '正在升级并重启，完成后自动刷新…' }} />}

      {askPassword && !restarting && (
        <div className="flex items-center gap-2 rounded-2xl border border-line bg-s1 p-3">
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="输入管理员密码确认升级"
            className="min-w-0 flex-1 rounded-xl border border-line2 bg-canvas px-3 py-1.5 text-sm text-fg focus:outline-none"
          />
          <button
            type="button"
            className="settings-button settings-primary"
            disabled={!password || upgrading}
            onClick={() => void applyUpgrade()}
          >
            {upgrading ? <Loader2 size={14} className="animate-spin" /> : null}
            确认升级
          </button>
        </div>
      )}

      <div className="settings-actions settings-update-actions">
        <a className="settings-link" href={status?.release_url?.startsWith(releasesPage + '/tag/') ? status.release_url : releasesPage} target="_blank" rel="noreferrer">
          发布记录 <ArrowUpRight size={14} aria-hidden="true" />
        </a>
        <div className="flex items-center gap-2">
          <button type="button" className="settings-button" onClick={() => void check(channel)} disabled={checking || upgrading || restarting}>
            <RefreshCw size={14} className={checking ? 'animate-spin' : undefined} aria-hidden="true" />
            刷新
          </button>
          {target.local && status?.state === 'available' && status.can_apply && status.latest && (
            <button
              type="button"
              className="settings-button settings-primary"
              disabled={upgrading || restarting}
              onClick={() => void applyUpgrade()}
            >
              {upgrading ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
              升级至 {status.latest}
            </button>
          )}
        </div>
      </div>
    </div>
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
    if (newPw !== again) { setFeedback({ error: true, text: '两次输入的新密码不一致' }); return; }
    setBusy(true);
    setFeedback(undefined);
    try {
      await api.changePassword(oldPw, newPw);
      setOldPw(''); setNewPw(''); setAgain('');
      setFeedback({ error: false, text: '密码已更新' });
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
      <div className="flex items-center justify-between">
        <h3 id="settings-account-title">账号 · {username}</h3>
        <button type="button" className="settings-button" onClick={logout}>
          <LogOut size={14} aria-hidden="true" />退出登录
        </button>
      </div>
      <form className="settings-account-form mt-4" onSubmit={change}>
        <div className="settings-field"><label htmlFor="pw-old">当前密码</label>
          <input id="pw-old" type="password" autoComplete="current-password" required value={oldPw} onChange={(e) => setOldPw(e.target.value)} /></div>
        <div className="settings-field"><label htmlFor="pw-new">新密码</label>
          <input id="pw-new" type="password" autoComplete="new-password" required minLength={8} value={newPw} onChange={(e) => setNewPw(e.target.value)} /></div>
        <div className="settings-field"><label htmlFor="pw-again">确认新密码</label>
          <input id="pw-again" type="password" autoComplete="new-password" required minLength={8} value={again} onChange={(e) => setAgain(e.target.value)} /></div>
        {feedback && <FeedbackLine feedback={feedback} />}
        <div className="settings-actions">
          <button type="submit" className="settings-button settings-primary" disabled={busy || !oldPw || !newPw}>
            {busy ? '保存中…' : '更新密码'}
          </button>
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
