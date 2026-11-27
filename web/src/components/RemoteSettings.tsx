import {
  Check, CircleAlert, Copy, KeyRound, Loader2, Lock, Network, QrCode, ShieldCheck, Smartphone, Trash2, WifiOff,
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
  const [realityListen, setRealityListen] = useState(':8443');
  const [p2pListen, setP2PListen] = useState(':51820');
  const [relayAddr, setRelayAddr] = useState('');
  const [relayToken, setRelayToken] = useState('');
  const [e2eeRelayURL, setE2EERelayURL] = useState('');
  const [e2eeRelayToken, setE2EERelayToken] = useState('');
  const [e2eeLANListen, setE2EELANListen] = useState(':8089');
  const [publicHost, setPublicHost] = useState('');
  const [sni, setSNI] = useState('www.apple.com');
  const [pair, setPair] = useState<RemotePairResponse | null>(null);
  const [copied, setCopied] = useState(false);
  const [feedback, setFeedback] = useState<{ error: boolean; text: string } | null>(null);
  const [pairLoading, setPairLoading] = useState(false);

  const syncFromStatus = useCallback((st: RemoteStatus, initInputs = false) => {
    setStatus(st);
    if (initInputs) {
      setMode(st.mode);
      setServerName(st.server_name || '');
      setRealityListen(st.reality_listen || ':8443');
      setP2PListen(st.p2p_listen || ':51820');
      setRelayAddr(st.relay_addr || '');
      setRelayToken(st.relay_token || '');
      setE2EERelayURL(st.e2ee_relay_url || '');
      setE2EERelayToken(st.e2ee_relay_token || '');
      setE2EELANListen(st.e2ee_lan_listen || ':8089');
      setPublicHost(st.public_host || '');
      setSNI(st.sni || 'www.apple.com');
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    remoteAPI.get(ctrl.signal).then((st) => syncFromStatus(st, true)).catch((e: Error) => {
      if (!ctrl.signal.aborted) setFeedback({ error: true, text: e.message });
    });
    return () => ctrl.abort();
  }, [syncFromStatus]);

  const applyMode = async (targetMode = mode) => {
    setSaving(true);
    setFeedback(null);
    setPair(null);
    try {
      const st = await remoteAPI.save({
        mode: targetMode,
        reality_listen: realityListen,
        p2p_listen: p2pListen,
        relay_addr: relayAddr,
        relay_token: relayToken,
        e2ee_relay_url: e2eeRelayURL,
        e2ee_relay_token: e2eeRelayToken,
        e2ee_lan_listen: e2eeLANListen,
        public_host: publicHost,
        sni,
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

  const copyText = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch { /* ignore */ }
  };

  const modes: { id: RemoteMode; icon: typeof ShieldCheck; title: string; sub: string }[] = [
    { id: 'e2ee', icon: Lock, title: 'E2EE 盲中继（推荐）', sub: '免费 CF / WSS 443 + 局域网直连' },
    { id: 'reality', icon: ShieldCheck, title: 'VLESS-Reality', sub: '公网 VPS 直连' },
    { id: 'p2p', icon: Network, title: 'P2P 打洞', sub: '旧版 UDP 穿透' },
    { id: 'off', icon: WifiOff, title: '关闭', sub: '仅本机' },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="settings-remote-title">加密隧道</h3>
        {status?.ready && (
          <button
            type="button"
            onClick={() => void generatePair()}
            disabled={pairLoading}
            className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-3.5 py-1.5 text-xs font-medium text-on-accent transition hover:opacity-90 disabled:opacity-50"
          >
            {pairLoading ? <Loader2 size={14} className="animate-spin" /> : <QrCode size={14} />}
            添加设备
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
          <span className="truncate font-mono">{status.error || status.status_text}</span>
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
              <span className="font-medium text-fg">连接码</span>
              <span className="text-fg4">10 分钟内有效</span>
            </div>
            <div className="flex items-center gap-2">
              <input
                readOnly
                value={pair.pair_code}
                className="min-w-0 flex-1 truncate rounded-xl border border-line bg-canvas px-2.5 py-1.5 font-mono text-xs text-fg2 focus:outline-none"
              />
              <button
                type="button"
                onClick={() => void copyText(pair.pair_code)}
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
            <label htmlFor="remote-server-name">名称</label>
            <input
              id="remote-server-name"
              value={serverName}
              onChange={(e) => setServerName(e.target.value)}
              placeholder="frog"
            />
          </div>

          {mode === 'e2ee' && (
            <div className="space-y-3">
              <p className="text-xs leading-relaxed text-fg3">
                全端原生（iOS / Android / Mac / Win）通过 X25519 + AES-256-GCM 端到端加密通信：在家自动走局域网直连，出门 4G/5G 自动切标准 HTTPS/WSS 443 盲中继（可 0 成本部署在 <code className="font-mono">deploy/cf-relay</code> 免费 Cloudflare Workers，或复用任意公网 Frog VPS 的 <code className="font-mono">/api/v2/relay/ws</code>）。
              </p>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div className="settings-field">
                  <label htmlFor="remote-e2ee-relay">WSS 盲中继地址（外网 4G/5G 穿透）</label>
                  <input
                    id="remote-e2ee-relay"
                    value={e2eeRelayURL}
                    onChange={(e) => setE2EERelayURL(e.target.value)}
                    placeholder="wss://frog-e2ee-relay.你的名字.workers.dev/ws"
                    className="font-mono text-xs"
                  />
                </div>
                <div className="settings-field">
                  <label htmlFor="remote-e2ee-token">中继访问令牌（可选）</label>
                  <input
                    id="remote-e2ee-token"
                    value={e2eeRelayToken}
                    onChange={(e) => setE2EERelayToken(e.target.value)}
                    placeholder="留空或与 RELAY_TOKEN 一致"
                    className="font-mono text-xs"
                  />
                </div>
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div className="settings-field">
                  <label htmlFor="remote-e2ee-lan">局域网直连端口（同 Wi-Fi 秒连）</label>
                  <input
                    id="remote-e2ee-lan"
                    value={e2eeLANListen}
                    onChange={(e) => setE2EELANListen(e.target.value)}
                    placeholder=":8089（填 off 关闭）"
                    className="font-mono text-xs"
                  />
                </div>
                <div className="settings-field">
                  <label htmlFor="remote-e2ee-host">指定局域网 IP（可选）</label>
                  <input
                    id="remote-e2ee-host"
                    value={publicHost}
                    onChange={(e) => setPublicHost(e.target.value)}
                    placeholder={status?.detected_ip || '自动探测本机局域网 IP'}
                    className="font-mono text-xs"
                  />
                </div>
              </div>
            </div>
          )}

          {mode === 'reality' && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <div className="settings-field">
                <label htmlFor="remote-public-host">连接地址</label>
                <input
                  id="remote-public-host"
                  value={publicHost}
                  onChange={(e) => setPublicHost(e.target.value)}
                  placeholder={status?.detected_ip || 'IP / 域名'}
                  className="font-mono text-xs"
                />
              </div>
              <div className="settings-field">
                <label htmlFor="remote-reality-listen">端口</label>
                <input
                  id="remote-reality-listen"
                  value={realityListen}
                  onChange={(e) => setRealityListen(e.target.value)}
                  placeholder=":8443"
                  className="font-mono text-xs"
                />
              </div>
              <div className="settings-field">
                <label htmlFor="remote-reality-sni">SNI</label>
                <input
                  id="remote-reality-sni"
                  value={sni}
                  onChange={(e) => setSNI(e.target.value)}
                  placeholder="www.apple.com"
                  className="font-mono text-xs"
                />
              </div>
            </div>
          )}

          {mode === 'p2p' && (
            <div className="space-y-3">
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div className="settings-field">
                  <label htmlFor="remote-relay-addr">信令 / 中继服务器（5G 穿透推荐）</label>
                  <input
                    id="remote-relay-addr"
                    value={relayAddr}
                    onChange={(e) => setRelayAddr(e.target.value)}
                    placeholder="IP:18443（如 38.47.118.155:18443）"
                    className="font-mono text-xs"
                  />
                </div>
                <div className="settings-field">
                  <label htmlFor="remote-relay-token">中继令牌</label>
                  <input
                    id="remote-relay-token"
                    value={relayToken}
                    onChange={(e) => setRelayToken(e.target.value)}
                    placeholder="与 frogbot relay -token 一致"
                    className="font-mono text-xs"
                  />
                </div>
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div className="settings-field">
                  <label htmlFor="remote-p2p-listen">本地 UDP 端口</label>
                  <input
                    id="remote-p2p-listen"
                    value={p2pListen}
                    onChange={(e) => setP2PListen(e.target.value)}
                    placeholder=":51820"
                    className="font-mono text-xs"
                  />
                </div>
                <div className="settings-field">
                  <label htmlFor="remote-p2p-host">指定直连地址（可选）</label>
                  <input
                    id="remote-p2p-host"
                    value={publicHost}
                    onChange={(e) => setPublicHost(e.target.value)}
                    placeholder="自动探测"
                    className="font-mono text-xs"
                  />
                </div>
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
          disabled={saving}
          onClick={() => void applyMode(mode)}
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : null}
          保存
        </button>
      </div>

      {status && status.devices.length > 0 && (
        <div className="border-t border-line pt-4">
          <div className="mb-2 flex items-center justify-between">
            <h4 className="text-xs font-semibold text-fg3">设备 ({status.devices.length})</h4>
            <button
              type="button"
              onClick={async () => {
                if (!confirm('重置密钥并移除全部设备？')) return;
                const st = await remoteAPI.rotateSecret(true);
                syncFromStatus(st, false);
                setPair(null);
              }}
              className="inline-flex items-center gap-1 text-xs text-fg4 hover:text-danger"
            >
              <KeyRound size={12} />重置密钥
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
