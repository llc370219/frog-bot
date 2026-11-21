import {
  Check, CircleAlert, Cloud, Copy, KeyRound, Loader2, QrCode, Server, ShieldCheck, Smartphone, Trash2, WifiOff,
} from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { remoteAPI, type RemoteMode, type RemotePairResponse, type RemoteStatus } from '../api';

export default function RemoteSettings({
  saving,
  setSaving,
}: {
  saving: boolean;
  setSaving: (v: boolean) => void;
}) {
  const [status, setStatus] = useState<RemoteStatus | null>(null);
  const [mode, setMode] = useState<RemoteMode>('off');
  const [serverName, setServerName] = useState('');
  const [bridgeURI, setBridgeURI] = useState('');
  const [directListen, setDirectListen] = useState(':8443');
  const [publicHost, setPublicHost] = useState('');
  const [pair, setPair] = useState<RemotePairResponse | null>(null);
  const [copied, setCopied] = useState(false);
  const [cmdCopied, setCmdCopied] = useState(false);
  const [feedback, setFeedback] = useState<{ error: boolean; text: string } | null>(null);
  const [pairLoading, setPairLoading] = useState(false);

  const syncFromStatus = useCallback((st: RemoteStatus, initInputs = false) => {
    setStatus(st);
    if (initInputs) {
      setMode(st.mode);
      setServerName(st.server_name || '');
      setBridgeURI(st.bridge_uri || '');
      setDirectListen(st.direct_listen || ':8443');
      setPublicHost(st.public_host || '');
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    remoteAPI.get(ctrl.signal).then((st) => syncFromStatus(st, true)).catch((e: Error) => {
      if (!ctrl.signal.aborted) setFeedback({ error: true, text: e.message });
    });
    return () => ctrl.abort();
  }, [syncFromStatus]);

  useEffect(() => {
    if (!status || status.mode === 'off' || status.ready) return;
    const t = window.setInterval(() => {
      remoteAPI.get().then((st) => syncFromStatus(st, false)).catch(() => {});
    }, 2000);
    return () => window.clearInterval(t);
  }, [status, syncFromStatus]);

  const applyMode = async (targetMode = mode) => {
    setSaving(true);
    setFeedback(null);
    setPair(null);
    try {
      const st = await remoteAPI.save({
        mode: targetMode,
        direct_listen: directListen,
        public_host: publicHost,
        bridge_uri: bridgeURI,
        server_name: serverName,
      });
      setMode(st.mode);
      syncFromStatus(st, false);
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setSaving(false);
    }
  };

  const generatePair = async () => {
    setPairLoading(true);
    setFeedback(null);
    try {
      setPair(await remoteAPI.createPair());
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setPairLoading(false);
    }
  };

  const copyText = async (text: string, setter: (v: boolean) => void) => {
    try {
      await navigator.clipboard.writeText(text);
      setter(true);
      window.setTimeout(() => setter(false), 1600);
    } catch { /* ignore */ }
  };

  const modes: { id: RemoteMode; icon: typeof Cloud; title: string; sub: string }[] = [
    { id: 'quick', icon: Cloud, title: '云穿透', sub: 'Cloudflare Tunnel' },
    { id: 'bridge', icon: Server, title: '自建中转', sub: 'frogbot bridge' },
    { id: 'direct', icon: ShieldCheck, title: '直连 TLS', sub: '端口加密监听' },
    { id: 'off', icon: WifiOff, title: '关闭', sub: '停用远程通道' },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="settings-remote-title">远程连接</h3>
        {status?.ready && (
          <button
            type="button"
            onClick={() => void generatePair()}
            disabled={pairLoading}
            className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-3.5 py-1.5 text-xs font-medium text-on-accent transition hover:opacity-90 disabled:opacity-50"
          >
            {pairLoading ? <Loader2 size={14} className="animate-spin" /> : <QrCode size={14} />}
            配对二维码
          </button>
        )}
      </div>

      {status && status.mode !== 'off' && (
        <div className={`flex items-center gap-2.5 rounded-2xl border px-3.5 py-2.5 text-xs ${
          status.ready ? 'border-accent/35 bg-accent/8 text-fg'
            : status.error ? 'border-danger/35 bg-danger/8 text-danger'
              : 'border-line bg-s2 text-fg2'
        }`}>
          {status.ready ? <ShieldCheck size={15} className="shrink-0 text-accent" />
            : status.error ? <CircleAlert size={15} className="shrink-0 text-danger" />
              : <Loader2 size={15} className="shrink-0 animate-spin text-accent" />}
          <span className="truncate">{status.error || status.status_text}</span>
        </div>
      )}

      {pair && (
        <div className="flex flex-col items-center gap-4 rounded-2xl border border-line2 bg-s1 p-4 sm:flex-row">
          <div
            className="size-36 shrink-0 overflow-hidden rounded-xl border border-line bg-white p-2"
            dangerouslySetInnerHTML={{ __html: pair.qr_svg }}
          />
          <div className="min-w-0 flex-1 w-full space-y-2.5">
            <div className="flex items-center justify-between text-xs">
              <span className="font-medium text-fg">单次配对码</span>
              <span className="text-fg4">10 分钟有效</span>
            </div>
            <div className="flex items-center gap-2">
              <input
                readOnly
                value={pair.pair_code}
                className="min-w-0 flex-1 truncate rounded-xl border border-line bg-canvas px-2.5 py-1.5 font-mono text-xs text-fg2 focus:outline-none"
              />
              <button
                type="button"
                onClick={() => void copyText(pair.pair_code, setCopied)}
                className="inline-flex shrink-0 items-center gap-1 rounded-xl border border-line2 bg-canvas px-3 py-1.5 text-xs font-medium text-fg transition hover:bg-s2"
              >
                {copied ? <Check size={13} className="text-accent" /> : <Copy size={13} />}
                {copied ? '已复制' : '复制'}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {modes.map((c) => {
          const selected = mode === c.id;
          const Icon = c.icon;
          return (
            <button
              key={c.id}
              type="button"
              onClick={() => setMode(c.id)}
              className={`flex flex-col items-start rounded-2xl border p-3 text-left transition ${
                selected ? 'border-accent bg-s1 shadow-2xs' : 'border-line bg-canvas hover:border-line2'
              }`}
            >
              <Icon size={16} className={selected ? 'text-accent' : 'text-fg3'} />
              <span className="mt-2 text-[13.5px] font-medium text-fg">{c.title}</span>
              <span className="mt-0.5 text-[11px] text-fg4">{c.sub}</span>
            </button>
          );
        })}
      </div>

      {mode !== 'off' && (
        <div className="space-y-3 rounded-2xl border border-line bg-s1/60 p-4">
          <div className="settings-field">
            <label htmlFor="remote-server-name">主机名称</label>
            <input
              id="remote-server-name"
              value={serverName}
              onChange={(e) => setServerName(e.target.value)}
              placeholder="MacBook"
            />
          </div>

          {mode === 'bridge' && (
            <div className="space-y-2">
              <div className="settings-field">
                <label htmlFor="remote-bridge-uri">中转地址</label>
                <input
                  id="remote-bridge-uri"
                  value={bridgeURI}
                  onChange={(e) => setBridgeURI(e.target.value)}
                  placeholder="frog-bridge://host:8443#token.pin"
                  className="font-mono text-xs"
                />
              </div>
              <div className="flex items-center justify-between gap-2 rounded-xl bg-s2 px-3 py-2 text-xs">
                <code className="truncate font-mono text-fg2">{status?.bridge_command || 'frogbot bridge -listen :8443'}</code>
                <button
                  type="button"
                  onClick={() => void copyText(status?.bridge_command || 'frogbot bridge -listen :8443', setCmdCopied)}
                  className="inline-flex shrink-0 items-center gap-1 text-accent hover:underline"
                >
                  {cmdCopied ? <Check size={12} /> : <Copy size={12} />}
                  {cmdCopied ? '已复制' : '复制服务端命令'}
                </button>
              </div>
            </div>
          )}

          {mode === 'direct' && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="settings-field">
                <label htmlFor="remote-public-host">访问地址</label>
                <input
                  id="remote-public-host"
                  value={publicHost}
                  onChange={(e) => setPublicHost(e.target.value)}
                  placeholder={status?.detected_ip || 'IP / 域名'}
                  className="font-mono text-xs"
                />
              </div>
              <div className="settings-field">
                <label htmlFor="remote-direct-listen">端口</label>
                <input
                  id="remote-direct-listen"
                  value={directListen}
                  onChange={(e) => setDirectListen(e.target.value)}
                  placeholder=":8443"
                  className="font-mono text-xs"
                />
              </div>
            </div>
          )}
        </div>
      )}

      {feedback && (
        <p role={feedback.error ? 'alert' : 'status'} className={feedback.error ? 'settings-feedback is-error' : 'settings-feedback'}>
          {feedback.error ? <CircleAlert size={15} /> : <Check size={15} />}
          <span>{feedback.text}</span>
        </p>
      )}

      <div className="settings-actions">
        <button
          type="button"
          className="settings-button settings-primary"
          disabled={saving || (mode === 'bridge' && !bridgeURI.trim())}
          onClick={() => void applyMode(mode)}
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : null}
          保存
        </button>
      </div>

      {status && status.devices.length > 0 && (
        <div className="border-t border-line pt-4">
          <div className="mb-2 flex items-center justify-between">
            <h4 className="text-xs font-semibold text-fg3">已配对设备 ({status.devices.length})</h4>
            <button
              type="button"
              onClick={async () => {
                if (!confirm('清空所有已配对设备并重置连接路径？')) return;
                const st = await remoteAPI.rotateSecret(true);
                syncFromStatus(st, false);
                setPair(null);
              }}
              className="inline-flex items-center gap-1 text-xs text-fg4 hover:text-danger"
            >
              <KeyRound size={12} />全部移除
            </button>
          </div>
          <div className="divide-y divide-line rounded-2xl border border-line bg-s1">
            {status.devices.map((d) => (
              <div key={d.id} className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-xs">
                <div className="flex min-w-0 items-center gap-2.5">
                  <Smartphone size={14} className="shrink-0 text-fg3" />
                  <span className="truncate font-medium text-fg">{d.name}</span>
                </div>
                <button
                  type="button"
                  onClick={async () => {
                    const st = await remoteAPI.revokeDevice(d.id);
                    syncFromStatus(st, false);
                  }}
                  className="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-danger transition hover:bg-s2"
                >
                  <Trash2 size={12} />移除
                </button>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
