import { ArrowUpRight, Atom, Check, CircleAlert, Code2, KeyRound, Link2, LogOut, Orbit, Plug, RefreshCw, SquareTerminal, X, Zap } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { api, type AuthInfo, type UpdateStatus } from '../api';
import APIChannels from './APIChannels';

const releasesPage = 'https://github.com/llc370219/frog-bot/releases';
const message = (err: unknown) => err instanceof TypeError ? '无法连接 Frog Bot 后端，请确认服务已启动'
  : err instanceof Error ? err.message : '请求失败，请重试';

type Tab = 'api' | 'connections' | 'update' | 'account';
type Feedback = { error: boolean; text: string };

export default function SettingsDialog({ onClose, version, onSettingsSaved }: {
  onClose: () => void;
  version?: string;
  onSettingsSaved: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [tab, setTab] = useState<Tab>('api');
  const [saving, setSaving] = useState(false);
  const [auth, setAuth] = useState<AuthInfo>();

  useEffect(() => { api.me().then(setAuth).catch(() => {}); }, []);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => el?.close();
  }, []);

  return (
    <dialog ref={dialog} className="settings-dialog" aria-labelledby="settings-title"
      onCancel={(e) => { e.preventDefault(); if (!saving) onClose(); }}>
      <aside className="settings-sidebar">
        <h2 id="settings-title">通用</h2>
        <nav className="settings-nav" aria-label="设置分类">
          <button type="button" aria-current={tab === 'api' ? 'page' : undefined} aria-controls="settings-api" onClick={() => setTab('api')}>
            <Plug size={16} aria-hidden="true" /> API 渠道
          </button>
          <button type="button" aria-current={tab === 'connections' ? 'page' : undefined} aria-controls="settings-connections" disabled={saving} onClick={() => setTab('connections')}>
            <Link2 size={16} aria-hidden="true" /> 连接
          </button>
          <button type="button" aria-current={tab === 'update' ? 'page' : undefined} aria-controls="settings-update" disabled={saving} onClick={() => setTab('update')}>
            <RefreshCw size={15} aria-hidden="true" /> 版本更新
          </button>
          {auth?.enabled && <button type="button" aria-current={tab === 'account' ? 'page' : undefined} aria-controls="settings-account" disabled={saving} onClick={() => setTab('account')}>
            <KeyRound size={15} aria-hidden="true" /> 账号与登录
          </button>}
        </nav>
      </aside>
      <button type="button" className="settings-close" aria-label="关闭设置" disabled={saving} onClick={onClose}>
        <X size={19} />
      </button>
      <div className="settings-content">
        <section id="settings-api" className="settings-pane" hidden={tab !== 'api'} aria-labelledby="settings-api-title">
          <APIChannels saving={saving} setSaving={setSaving} onSaved={onSettingsSaved} />
        </section>
        <section id="settings-update" className="settings-pane" hidden={tab !== 'update'} aria-labelledby="settings-update-title">
          <UpdatePanel version={version} />
        </section>
        {auth?.enabled && <section id="settings-account" className="settings-pane" hidden={tab !== 'account'} aria-labelledby="settings-account-title">
          <AccountPanel username={auth.username ?? ''} />
        </section>}
        <section id="settings-connections" className="settings-pane" hidden={tab !== 'connections'} aria-labelledby="settings-connections-title">
          <ConnectionsPanel />
        </section>
      </div>
    </dialog>
  );
}

const agentTools = [
  { name: 'Claude Code', description: 'Anthropic 的终端编程 Agent', icon: Atom, tone: 'claude' },
  { name: 'Codex CLI', description: 'OpenAI 的命令行编程 Agent', icon: SquareTerminal, tone: 'codex' },
  { name: 'Grok CLI', description: '在命令行中使用 Grok 协助编程', icon: Zap, tone: 'grok' },
  { name: 'AGY', description: '通过 Frog Bot 统一调度 Agent', icon: Orbit, tone: 'agy' },
  { name: 'Pi', description: '轻量的编程 Agent 工具', icon: Code2, tone: 'pi' },
];

function ConnectionsPanel() {
  return (
    <>
      <h3 id="settings-connections-title">连接</h3>
      <p className="settings-description">将 Agent 工具连接到 Frog Bot，统一调度与协作。</p>
      <div className="settings-agents-heading">
        <h4>Agent 工具</h4>
        <span>即将支持</span>
      </div>
      <ul className="settings-agents" aria-label="Agent 工具">
        {agentTools.map((agent) => (
          <li key={agent.name} className="settings-agent">
            <div className="settings-agent-head">
              <span className={`settings-agent-icon ${agent.tone}`} aria-hidden="true"><agent.icon size={20} /></span>
              <h5>{agent.name}</h5>
              <span className="settings-agent-status">待接入</span>
            </div>
            <p className="settings-agent-description">{agent.description}</p>
            <div className="settings-agent-actions">
              <button type="button" role="switch" aria-checked={false} className="settings-agent-switch" disabled aria-label={`启用 ${agent.name}（即将支持）`}><span /></button>
              <button type="button" className="settings-button settings-agent-connect" disabled aria-label={`设置 ${agent.name}（即将支持）`}>设置</button>
            </div>
          </li>
        ))}
      </ul>
      <p className="settings-agents-note">连接功能暂未开放，后续可通过 Frog Bot 调用和控制这些工具。</p>
    </>
  );
}

function UpdatePanel({ version }: { version?: string }) {
  const [status, setStatus] = useState<UpdateStatus>();
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);

  async function check() {
    setChecking(true);
    setError('');
    try {
      setStatus(await api.checkUpdate());
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
      <p className="settings-description">查看 Frog Bot 的最新发布版本。</p>
      <dl className="settings-versions">
        <div><dt>当前版本</dt><dd>{status?.current ?? version ?? '—'}</dd></div>
        {status?.latest && <div><dt>最新发布</dt><dd>{status.latest}</dd></div>}
      </dl>
      {error && <FeedbackLine feedback={{ error: true, text: error }} />}
      {status && !checking && <p role="status" className="settings-update-note">{{
        current: '当前版本无需更新。',
        available: '有新版本可用，请前往发布页查看。',
        no_release: 'GitHub 上暂无可用的正式发布。',
        unversioned: '可以前往发布页查看最新版本。',
      }[status.state]}</p>}
      <div className="settings-actions settings-update-actions">
        <a className="settings-link" href={status?.release_url ?? releasesPage} target="_blank" rel="noreferrer">
          查看发布记录 <ArrowUpRight size={14} aria-hidden="true" />
        </a>
        <button type="button" className="settings-button settings-primary" onClick={check} disabled={checking}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : undefined} aria-hidden="true" />
          {checking ? '检查中…' : '检查更新'}
        </button>
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
