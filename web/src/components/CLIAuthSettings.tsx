import { Check, CircleAlert, Copy, ExternalLink, KeyRound, Loader2, LogOut, RefreshCw, Send, Terminal, X } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { v2API, type CLIAuthSessionView, type CLIAuthStatus } from '../v2api';

/**
 * 官方 Coding CLI 远程登录管理（Codex / Claude Code / Gemini CLI / Grok CLI）。
 * 与前置 Bot 的账号反代完全独立，登录凭证由官方 CLI 原生保存或加密托管在宿主密钥库。
 */
export default function CLIAuthSettings() {
  const [items, setItems] = useState<CLIAuthStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [busyCLI, setBusyCLI] = useState('');
  const [activeSess, setActiveSess] = useState<CLIAuthSessionView | null>(null);
  const [pasteInput, setPasteInput] = useState('');
  const [credCLI, setCredCLI] = useState<string>('');
  const [credKind, setCredKind] = useState<'oauth_token' | 'api_key'>('api_key');
  const [credValue, setCredValue] = useState('');
  const [copiedCode, setCopiedCode] = useState(false);
  const [feedback, setFeedback] = useState<{ error: boolean; text: string } | null>(null);

  const load = useCallback(async () => {
    try {
      const list = await v2API.cliAuthList();
      setItems(list);
      const running = list.find((c) => c.active_session && (c.active_session.status === 'running' || c.active_session.status === 'waiting_user'));
      if (running?.active_session) setActiveSess(running.active_session);
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // Poll active login session until completed
  useEffect(() => {
    if (!activeSess || (activeSess.status !== 'running' && activeSess.status !== 'waiting_user')) return;
    const timer = window.setInterval(async () => {
      try {
        const next = await v2API.cliAuthSession(activeSess.id);
        setActiveSess(next);
        if (next.status === 'succeeded') {
          setFeedback({ error: false, text: `${next.cli.toUpperCase()} 官方登录已完成！` });
          void load();
        } else if (next.status === 'failed') {
          setFeedback({ error: true, text: next.error || '登录未完成，请重试' });
        }
      } catch {
        /* ignore transient poll error */
      }
    }, 1800);
    return () => window.clearInterval(timer);
  }, [activeSess, load]);

  const startLogin = async (cli: string, mode: 'device' | 'oauth') => {
    setBusyCLI(cli);
    setFeedback(null);
    setPasteInput('');
    try {
      const sess = await v2API.cliAuthStart(cli, mode);
      setActiveSess(sess);
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusyCLI('');
    }
  };

  const submitInput = async () => {
    if (!activeSess || !pasteInput.trim()) return;
    setBusyCLI(activeSess.cli);
    setFeedback(null);
    try {
      const next = await v2API.cliAuthSubmitInput(activeSess.id, pasteInput.trim());
      setActiveSess(next);
      setPasteInput('');
      if (next.status === 'succeeded') {
        setFeedback({ error: false, text: `${next.cli.toUpperCase()} 官方登录已完成！` });
        void load();
      }
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusyCLI('');
    }
  };

  const cancelSess = async () => {
    if (!activeSess) return;
    await v2API.cliAuthCancel(activeSess.id).catch(() => {});
    setActiveSess(null);
    void load();
  };

  const saveCred = async (cli: string) => {
    if (!credValue.trim()) return;
    setBusyCLI(cli);
    setFeedback(null);
    try {
      await v2API.cliAuthSaveCredential(cli, credKind, credValue.trim());
      setCredCLI('');
      setCredValue('');
      setFeedback({ error: false, text: '官方 CLI 凭证已加密保存到宿主密钥库。' });
      void load();
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusyCLI('');
    }
  };

  const logoutCLI = async (cli: string) => {
    if (!window.confirm(`确认清除本机 ${cli} 的登录凭证吗？`)) return;
    setBusyCLI(cli);
    setFeedback(null);
    try {
      await v2API.cliAuthLogout(cli, true);
      setFeedback({ error: false, text: `已退出 ${cli} 登录。` });
      void load();
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusyCLI('');
    }
  };

  const copyCode = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code);
      setCopiedCode(true);
      window.setTimeout(() => setCopiedCode(false), 1500);
    } catch { /* ignore */ }
  };

  if (loading) {
    return (
      <div className="settings-channel-page">
        <h3 id="settings-cli-title">官方 CLI 登录</h3>
        <p className="settings-pending"><Loader2 size={16} className="animate-spin" />正在检测本机官方 CLI 状态…</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 id="settings-cli-title">官方 CLI 账户登录</h3>
          <p className="text-xs font-medium text-fg-2 mt-1">
            本处仅支持以下远程登录，其他工具请自行登录，支持识别使用
          </p>
          <p className="text-xs text-fg-3 mt-0.5">
            在无桌面 VPS / 本机上直接完成 Antigravity (agy)、Codex、Claude Code、Gemini CLI、Grok CLI 的官方登录；Frog Bot 直接复用您本机外部安装的官方 CLI（与前置 Bot 反代无关）。
          </p>
        </div>
        <button type="button" onClick={() => void load()} className="settings-button">
          <RefreshCw size={13} /> 刷新状态
        </button>
      </div>

      {feedback && (
        <p role={feedback.error ? 'alert' : 'status'} className={`settings-feedback${feedback.error ? ' is-error' : ''}`}>
          {feedback.error ? <CircleAlert size={15} /> : <Check size={15} />}
          <span>{feedback.text}</span>
        </p>
      )}

      {activeSess && (activeSess.status === 'running' || activeSess.status === 'waiting_user') && (
        <div className="rounded-2xl border border-accent/40 bg-accent/5 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-sm font-medium">
              <Loader2 size={15} className="animate-spin text-accent" />
              <span>正在登录 {activeSess.cli.toUpperCase()}（{activeSess.mode === 'device' ? '设备码授权' : 'OAuth 无头向导'}）</span>
            </div>
            <button type="button" onClick={() => void cancelSess()} className="settings-link">
              <X size={14} /> 取消
            </button>
          </div>

          {activeSess.user_code && (
            <div className="flex flex-wrap items-center gap-3 rounded-xl bg-surface p-3 border border-border">
              <div>
                <div className="text-[11px] text-fg-3">一次性设备验证码</div>
                <div className="font-mono text-xl font-bold tracking-widest text-accent">{activeSess.user_code}</div>
              </div>
              <button type="button" onClick={() => void copyCode(activeSess.user_code!)} className="settings-button ml-auto">
                <Copy size={13} /> {copiedCode ? '已复制' : '复制验证码'}
              </button>
            </div>
          )}

          {activeSess.auth_url && (
            <div className="flex flex-wrap items-center gap-2">
              <a
                href={activeSess.auth_url}
                target="_blank"
                rel="noopener noreferrer"
                className="settings-button settings-primary"
              >
                <ExternalLink size={14} /> 打开官方授权页面
              </a>
              <code className="text-[11px] text-fg-3 break-all">{activeSess.auth_url}</code>
            </div>
          )}

          {activeSess.needs_input && (
            <div className="space-y-2">
              {activeSess.input_prompt && <p className="text-xs text-fg-2">{activeSess.input_prompt}</p>}
              <div className="flex gap-2">
                <input
                  value={pasteInput}
                  onChange={(e) => setPasteInput(e.target.value)}
                  placeholder="粘贴授权码，或复制 http://localhost:xxxx/... 回调完整链接"
                  className="flex-1 rounded-xl border border-border bg-surface px-3 py-2 text-xs font-mono"
                />
                <button
                  type="button"
                  disabled={!pasteInput.trim() || busyCLI === activeSess.cli}
                  onClick={() => void submitInput()}
                  className="settings-button settings-primary"
                >
                  <Send size={13} /> 回传完成
                </button>
              </div>
            </div>
          )}

          {activeSess.output && (
            <pre className="max-h-32 overflow-auto rounded-xl bg-black/80 p-2.5 text-[11px] text-white/85 whitespace-pre-wrap break-all">
              {activeSess.output}
            </pre>
          )}
        </div>
      )}

      <div className="space-y-3">
        {items.map((item) => {
          const busy = busyCLI === item.id;
          const editingCred = credCLI === item.id;
          return (
            <div key={item.id} className="rounded-2xl border border-border bg-surface p-4 space-y-2.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <Terminal size={16} className="text-accent" />
                  <strong className="text-sm">{item.name}</strong>
                  <span className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${
                    item.logged_in
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
                      : item.installed
                        ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400'
                        : 'bg-muted text-fg-3'
                  }`}>
                    {item.logged_in
                      ? `已登录 · ${item.auth_method || 'oauth'}`
                      : item.installed
                        ? '未登录'
                        : '未安装'}
                  </span>
                </div>
                {item.account_hint && <code className="text-[11px] text-fg-3">{item.account_hint}</code>}
              </div>

              <p className="text-xs text-fg-3">{item.login_hint}</p>
              {item.installed && item.path && (
                <p className="text-[11px] text-fg-3">
                  已识别程序：<code className="rounded bg-muted px-1.5 py-0.5 break-all">{item.path}</code>
                </p>
              )}
              {!item.installed && (
                <p className="text-[11px] text-fg-3">
                  安装命令：<code className="rounded bg-muted px-1.5 py-0.5">{item.install_hint}</code>
                </p>
              )}

              <div className="flex flex-wrap gap-2 pt-1">
                {item.installed && item.supports_device_auth && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void startLogin(item.id, 'device')}
                    className="settings-button settings-primary"
                  >
                    {busy ? <Loader2 size={13} className="animate-spin" /> : null}
                    设备码登录
                  </button>
                )}
                {item.installed && item.supports_oauth_bridge && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void startLogin(item.id, 'oauth')}
                    className={`settings-button${!item.supports_device_auth ? ' settings-primary' : ''}`}
                  >
                    OAuth 链接登录
                  </button>
                )}
                {(item.api_key_env || item.supports_oauth_token) && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      if (editingCred) {
                        setCredCLI('');
                      } else {
                        setCredCLI(item.id);
                        setCredKind(item.supports_oauth_token ? 'oauth_token' : 'api_key');
                        setCredValue('');
                      }
                    }}
                    className="settings-button"
                  >
                    <KeyRound size={13} /> {item.supports_oauth_token ? 'Token / API Key' : '填 API Key'}
                  </button>
                )}
                {item.logged_in && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void logoutCLI(item.id)}
                    className="settings-link ml-auto"
                  >
                    <LogOut size={13} /> 退出登录
                  </button>
                )}
              </div>

              {editingCred && (
                <div className="rounded-xl border border-border bg-muted/30 p-3 space-y-2 mt-2">
                  {item.supports_oauth_token && (
                    <div className="flex gap-3 text-xs">
                      <label className="flex items-center gap-1.5 cursor-pointer">
                        <input
                          type="radio"
                          checked={credKind === 'oauth_token'}
                          onChange={() => setCredKind('oauth_token')}
                        />
                        <span>{item.oauth_token_env}（claude setup-token）</span>
                      </label>
                      <label className="flex items-center gap-1.5 cursor-pointer">
                        <input
                          type="radio"
                          checked={credKind === 'api_key'}
                          onChange={() => setCredKind('api_key')}
                        />
                        <span>{item.api_key_env}</span>
                      </label>
                    </div>
                  )}
                  <div className="flex gap-2">
                    <input
                      type="password"
                      value={credValue}
                      onChange={(e) => setCredValue(e.target.value)}
                      placeholder={`粘贴 ${credKind === 'oauth_token' ? item.oauth_token_env : item.api_key_env}`}
                      className="flex-1 rounded-xl border border-border bg-surface px-3 py-1.5 text-xs font-mono"
                    />
                    <button
                      type="button"
                      disabled={!credValue.trim() || busy}
                      onClick={() => void saveCred(item.id)}
                      className="settings-button settings-primary"
                    >
                      加密保存
                    </button>
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
