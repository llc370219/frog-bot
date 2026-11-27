import { AlertTriangle, Check, CircleAlert, Loader2, Play, RefreshCw, ShieldAlert, Square } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { v2API, type ProxyGatewayStatus } from '../v2api';

/**
 * 前置 Bot 层的可选订阅账号反代网关（路线 A：对接 CLIProxyAPI / OpenCodex / Magpie / 自定义本地反代）。
 * 强制展示风险免责声明；同步为独立 API 渠道，与自定义 API 渠道完全并存、自由切换。
 */
export default function ProxyGatewayPanel({ onChannelSynced }: { onChannelSynced: () => void }) {
  const [status, setStatus] = useState<ProxyGatewayStatus | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [accepted, setAccepted] = useState(false);
  const [engine, setEngine] = useState('cliproxyapi');
  const [baseURL, setBaseURL] = useState('http://127.0.0.1:8317/v1');
  const [channelName, setChannelName] = useState('账号反代 · CLIProxyAPI');
  const [authRequired, setAuthRequired] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [binaryPath, setBinaryPath] = useState('');
  const [startArgs, setStartArgs] = useState('');
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<{ error: boolean; text: string } | null>(null);

  const hydrate = useCallback((st: ProxyGatewayStatus, initFields = false) => {
    setStatus(st);
    if (initFields) {
      setEnabled(st.config.enabled);
      setAccepted(st.config.disclaimer_accepted);
      setEngine(st.config.engine || 'cliproxyapi');
      setBaseURL(st.config.base_url || 'http://127.0.0.1:8317/v1');
      setChannelName(st.config.channel_name || '账号反代 · CLIProxyAPI');
      setAuthRequired(st.config.auth_required);
      setBinaryPath(st.config.binary_path || '');
      setStartArgs(st.config.start_args || '');
    }
  }, []);

  const load = useCallback(async (initFields = false) => {
    try {
      const st = await v2API.proxyStatus();
      hydrate(st, initFields);
    } catch {
      /* ignore if host older than V2 */
    }
  }, [hydrate]);

  useEffect(() => { void load(true); }, [load]);

  const selectEngine = (id: string) => {
    setEngine(id);
    const preset = status?.detected_engines.find((e) => e.id === id);
    if (preset) {
      setBaseURL(preset.default_url);
      setChannelName(`账号反代 · ${preset.name}`);
      if (preset.binary_path) setBinaryPath(preset.binary_path);
      setStartArgs(preset.default_args || '');
    }
  };

  const save = async (nextEnabled = enabled) => {
    if (nextEnabled && !accepted) {
      setFeedback({ error: true, text: '启用账号反代前，请先阅读并勾选同意下方免责声明。' });
      return;
    }
    setBusy(true);
    setFeedback(null);
    try {
      const st = await v2API.proxySave({
        enabled: nextEnabled,
        disclaimer_accepted: accepted,
        engine,
        base_url: baseURL,
        channel_name: channelName,
        auth_required: authRequired,
        api_key: authRequired ? (apiKey || undefined) : '',
        auto_start: false,
        binary_path: binaryPath,
        start_args: startArgs,
        sync_channel: true,
      });
      setEnabled(st.config.enabled);
      setApiKey('');
      hydrate(st, false);
      onChannelSynced();
      setFeedback({
        error: false,
        text: st.config.enabled
          ? (st.probe_ok
              ? `反代网关已启用并同步为前置 Bot 渠道（已拉取 ${st.live_models?.length ?? 0} 个模型，${st.probe_latency_ms}ms）。`
              : '反代配置已保存并同步渠道；尚未探测到本地反代端口，请先启动反代程序后点击刷新。')
          : '已停用反代渠道（你的其他自定义 API 渠道不受影响）。',
      });
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  const startManaged = async () => {
    if (!accepted) {
      setFeedback({ error: true, text: '请先勾选同意免责声明后再启动反代进程。' });
      return;
    }
    setBusy(true);
    setFeedback(null);
    try {
      await v2API.proxySave({
        enabled: true,
        disclaimer_accepted: true,
        engine,
        base_url: baseURL,
        channel_name: channelName,
        auth_required: authRequired,
        api_key: authRequired ? (apiKey || undefined) : '',
        auto_start: false,
        binary_path: binaryPath,
        start_args: startArgs,
        sync_channel: false,
      });
      const st = await v2API.proxyStart();
      const synced = await v2API.proxySync();
      hydrate(synced || st, false);
      setEnabled(true);
      onChannelSynced();
      setFeedback({ error: false, text: '已启动反代进程并同步到前置 Bot 渠道列表。' });
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  const stopManaged = async () => {
    setBusy(true);
    setFeedback(null);
    try {
      const st = await v2API.proxyStop();
      hydrate(st, false);
      setFeedback({ error: false, text: '已停止托管的反代进程。' });
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  if (!status) return null;

  return (
    <section className="mt-6 rounded-2xl border border-amber-500/35 bg-amber-500/5 p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <ShieldAlert size={17} className="text-amber-500" />
          <div>
            <h4 className="text-sm font-semibold">前置 Bot 订阅账号反代网关（可选 · 实验性）</h4>
            <p className="text-xs text-fg-3">
              对接 CLIProxyAPI / OpenCodex / Magpie 等外部开源反代，与上方「自定义 API 渠道」完全并存、按 Bot 自由切换。
            </p>
          </div>
        </div>
        <button type="button" onClick={() => void load(false)} disabled={busy} className="settings-button">
          <RefreshCw size={13} /> 探测状态
        </button>
      </div>

      {/* 强制免责声明 */}
      <div className="rounded-xl border border-amber-500/40 bg-surface p-3 space-y-2">
        <div className="flex items-center gap-1.5 text-xs font-semibold text-amber-600 dark:text-amber-400">
          <AlertTriangle size={14} /> 风险提示与免责声明（启用前必读）
        </div>
        <p className="text-[11.5px] leading-relaxed text-fg-2 whitespace-pre-line">{status.disclaimer_text}</p>
        <label className="flex items-start gap-2 text-xs font-medium cursor-pointer pt-1">
          <input
            type="checkbox"
            checked={accepted}
            disabled={busy}
            onChange={(e) => setAccepted(e.target.checked)}
            className="mt-0.5"
          />
          <span>我已阅读并同意上述免责声明，了解使用第三方反代可能违反厂商条款并存在封号风险，自愿承担全部后果。</span>
        </label>
      </div>

      {feedback && (
        <p role={feedback.error ? 'alert' : 'status'} className={`settings-feedback${feedback.error ? ' is-error' : ''}`}>
          {feedback.error ? <CircleAlert size={15} /> : <Check size={15} />}
          <span>{feedback.text}</span>
        </p>
      )}

      {/* 引擎选择与本机探测 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
        {status.detected_engines.map((eng) => {
          const active = engine === eng.id;
          return (
            <button
              key={eng.id}
              type="button"
              onClick={() => selectEngine(eng.id)}
              className={`text-left rounded-xl border p-2.5 transition ${
                active ? 'border-accent bg-accent/10' : 'border-border bg-surface hover:border-fg-4'
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-semibold">{eng.name}</span>
                <span className={`text-[10.5px] px-1.5 py-0.5 rounded-full ${
                  eng.probe_alive
                    ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
                    : eng.installed
                      ? 'bg-sky-500/15 text-sky-600 dark:text-sky-400'
                      : 'bg-muted text-fg-3'
                }`}>
                  {eng.probe_alive ? '端口运行中' : eng.installed ? '已安装' : '未检测到'}
                </span>
              </div>
              <div className="text-[11px] text-fg-3 mt-1 line-clamp-2">{eng.description}</div>
              {eng.install_hint && !eng.installed && eng.id !== 'custom' && (
                <div className="text-[10.5px] text-fg-4 font-mono mt-1 truncate">{eng.install_hint}</div>
              )}
            </button>
          );
        })}
      </div>

      {/* 网关参数 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
        <div className="settings-field">
          <label>同步渠道名称</label>
          <input value={channelName} onChange={(e) => setChannelName(e.target.value)} disabled={busy} />
        </div>
        <div className="settings-field">
          <label>本地反代地址（OpenAI /v1）</label>
          <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} disabled={busy} placeholder="http://127.0.0.1:8317/v1" />
        </div>
        <div className="settings-field">
          <label>可执行文件路径（可选，用于一键托管启停）</label>
          <input value={binaryPath} onChange={(e) => setBinaryPath(e.target.value)} disabled={busy} placeholder="留空则自动在 PATH 查找或连接外部已运行进程" />
        </div>
        <div className="settings-field">
          <label>反代访问密钥（可选）</label>
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={authRequired}
              onChange={(e) => setAuthRequired(e.target.checked)}
              disabled={busy}
              aria-label="需要反代密钥"
            />
            <input
              type="password"
              value={apiKey}
              disabled={busy || !authRequired}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={status.config.has_api_key ? '已加密保存，留空保留' : '若反代配置了 API Key 请填写'}
              className="flex-1"
            />
          </div>
        </div>
      </div>

      {/* 实时状态与操作按钮 */}
      <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
        <div className="text-xs text-fg-3">
          {status.probe_ok
            ? `✓ 已连通反代（${status.probe_latency_ms}ms · ${status.live_models?.length ?? 0} 个模型）`
            : status.config.enabled
              ? `未连通：${status.probe_error || '请确认本地反代服务已运行'}`
              : '当前未启用（默认只使用自定义 API 渠道）'}
        </div>
        <div className="flex flex-wrap gap-2">
          {status.managed_running ? (
            <button type="button" disabled={busy} onClick={() => void stopManaged()} className="settings-button">
              <Square size={12} /> 停止进程 (PID {status.managed_pid})
            </button>
          ) : (
            <button type="button" disabled={busy || !accepted} onClick={() => void startManaged()} className="settings-button">
              <Play size={12} /> 托管启动并同步
            </button>
          )}
          {enabled ? (
            <>
              <button type="button" disabled={busy || !accepted} onClick={() => void save(true)} className="settings-button settings-primary">
                {busy ? <Loader2 size={13} className="animate-spin" /> : null}
                保存并同步渠道
              </button>
              <button type="button" disabled={busy} onClick={() => void save(false)} className="settings-button">
                停用反代
              </button>
            </>
          ) : (
            <button type="button" disabled={busy || !accepted} onClick={() => void save(true)} className="settings-button settings-primary">
              {busy ? <Loader2 size={13} className="animate-spin" /> : null}
              同意免责声明并启用
            </button>
          )}
        </div>
      </div>

      {status.last_logs && status.last_logs.length > 0 && (
        <pre className="max-h-24 overflow-auto rounded-xl bg-black/80 p-2 text-[11px] text-white/80 whitespace-pre-wrap">
          {status.last_logs.slice(-8).join('\n')}
        </pre>
      )}
    </section>
  );
}
