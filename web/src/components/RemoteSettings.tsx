import {
  Check, ChevronDown, ChevronRight, CircleAlert, Copy, KeyRound, Loader2, Lock, Network, Power, QrCode, RefreshCw, ShieldCheck, Smartphone, Trash2, WifiOff,
} from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { remoteAPI, type RemoteMode, type RemotePairResponse, type RemoteStatus } from '../api';

type RelaySource = 'default' | 'custom' | 'off';

function inferRelaySource(st: RemoteStatus): RelaySource {
  const raw = (st.e2ee_relay_url || '').trim();
  if (raw.toLowerCase() === 'off') return 'off';
  if (!raw || st.using_default_relay) return 'default';
  return 'custom';
}

export default function RemoteSettings({
  saving,
  setSaving,
}: {
  saving: boolean;
  setSaving: (v: boolean) => void;
}) {
  const [status, setStatus] = useState<RemoteStatus | null>(null);
  const [mode, setMode] = useState<RemoteMode>('e2ee');
  const [relaySource, setRelaySource] = useState<RelaySource>('default');
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
  const [showAdvanced, setShowAdvanced] = useState(false);
  const autoPairedRef = useRef(false);

  const syncFromStatus = useCallback((st: RemoteStatus, initInputs = false) => {
    setStatus(st);
    if (initInputs) {
      setMode(st.mode);
      setRelaySource(inferRelaySource(st));
      setServerName(st.server_name || '');
      setRealityListen(st.reality_listen || ':8443');
      setP2PListen(st.p2p_listen || ':51820');
      setRelayAddr(st.relay_addr || '');
      setRelayToken(st.relay_token || '');
      setE2EERelayURL(st.e2ee_relay_url === 'off' ? '' : (st.e2ee_relay_url || ''));
      setE2EERelayToken(st.e2ee_relay_token || '');
      setE2EELANListen(st.e2ee_lan_listen || ':8089');
      setPublicHost(st.public_host || '');
      setSNI(st.sni || 'www.apple.com');
    }
  }, []);

  const generatePair = useCallback(async () => {
    setPairLoading(true);
    setFeedback(null);
    try {
      setPair(await remoteAPI.createPair());
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setPairLoading(false);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    remoteAPI.get(ctrl.signal).then((st) => {
      syncFromStatus(st, true);
      if (st.ready && !autoPairedRef.current) {
        autoPairedRef.current = true;
        void generatePair();
      }
    }).catch((e: Error) => {
      if (!ctrl.signal.aborted) setFeedback({ error: true, text: e.message });
    });
    return () => ctrl.abort();
  }, [syncFromStatus, generatePair]);

  // 公网中继连接建立期间轮询刷新状态，连上后自动停止轮询
  useEffect(() => {
    if (!status || status.mode !== 'e2ee' || !status.effective_relay_url || status.relay_connected) return;
    const timer = window.setInterval(() => {
      remoteAPI.get().then((st) => syncFromStatus(st, false)).catch(() => {});
    }, 2500);
    return () => window.clearInterval(timer);
  }, [status, syncFromStatus]);

  const saveConfig = async (overrides?: {
    targetMode?: RemoteMode;
    targetRelaySource?: RelaySource;
    autoPair?: boolean;
  }) => {
    const nextMode = overrides?.targetMode ?? mode;
    const nextSource = overrides?.targetRelaySource ?? relaySource;
    let nextRelayURL = '';
    let nextRelayToken = '';
    if (nextSource === 'off') {
      nextRelayURL = 'off';
    } else if (nextSource === 'custom') {
      nextRelayURL = e2eeRelayURL.trim();
      nextRelayToken = e2eeRelayToken.trim();
    }

    setSaving(true);
    setFeedback(null);
    setPair(null);
    try {
      const st = await remoteAPI.save({
        mode: nextMode,
        reality_listen: realityListen,
        p2p_listen: p2pListen,
        relay_addr: relayAddr,
        relay_token: relayToken,
        e2ee_relay_url: nextRelayURL,
        e2ee_relay_token: nextRelayToken,
        e2ee_lan_listen: e2eeLANListen,
        public_host: publicHost,
        sni,
        server_name: serverName,
      });
      setMode(st.mode);
      setRelaySource(inferRelaySource(st));
      syncFromStatus(st, false);
      if (st.ready && overrides?.autoPair !== false) {
        autoPairedRef.current = true;
        await generatePair();
      } else if (nextMode === 'off') {
        autoPairedRef.current = false;
      }
    } catch (e) {
      setFeedback({ error: true, text: (e as Error).message });
    } finally {
      setSaving(false);
    }
  };

  const copyText = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    } catch { /* ignore */ }
  };

  const defaultRelayDisplay = status?.default_relay_url || 'wss://relay.632219.xyz/ws';
  const isEnabled = status != null && status.mode !== 'off';
  const isFullyOnline = Boolean(
    status?.ready && (
      status.mode !== 'e2ee'
      || !status.effective_relay_url
      || status.relay_connected
    ),
  );

  const advancedModes: { id: RemoteMode; icon: typeof ShieldCheck; title: string; sub: string }[] = [
    { id: 'e2ee', icon: Lock, title: 'E2EE 穿透（默认）', sub: '公网加密中继 + 同 Wi-Fi 直连' },
    { id: 'reality', icon: ShieldCheck, title: 'VLESS-Reality', sub: '公网 VPS 直连' },
    { id: 'p2p', icon: Network, title: 'P2P 打洞', sub: '旧版 UDP 穿透' },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 id="settings-remote-title">手机 / 远程连接</h3>
          <p className="mt-0.5 text-xs text-fg3">
            开箱即用，无需配置路由器、公网 IP 或中继服务器；全程端到端加密，仅已配对设备可访问。
          </p>
        </div>
        {isEnabled && (
          <button
            type="button"
            onClick={() => void generatePair()}
            disabled={pairLoading || saving}
            className="inline-flex items-center gap-1.5 rounded-xl border border-line2 bg-canvas px-3 py-1.5 text-xs font-medium text-fg transition hover:bg-s2 disabled:opacity-50"
          >
            {pairLoading ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
            刷新连接码
          </button>
        )}
      </div>

      {/* 未开启时的极简一键启用引导 */}
      {status && !isEnabled && (
        <div className="flex flex-col items-start gap-3.5 rounded-2xl border border-line bg-s1 p-5">
          <div className="flex items-center gap-2.5 text-fg">
            <span className="grid size-9 place-items-center rounded-xl bg-accent/12 text-accent">
              <QrCode size={18} />
            </span>
            <div>
              <div className="text-sm font-medium">远程连接当前已关闭</div>
              <div className="text-xs text-fg3">一键开启后立即生成二维码与连接码，出门 4G/5G 也能直接连接这台电脑。</div>
            </div>
          </div>
          <button
            type="button"
            disabled={saving}
            onClick={() => void saveConfig({ targetMode: 'e2ee', targetRelaySource: 'default', autoPair: true })}
            className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-4 py-2 text-xs font-medium text-on-accent transition hover:opacity-90 disabled:opacity-50"
          >
            {saving ? <Loader2 size={14} className="animate-spin" /> : <Power size={14} />}
            一键开启并生成连接码
          </button>
        </div>
      )}

      {/* 已开启状态栏 */}
      {isEnabled && status && (
        <div className={`flex items-center gap-2.5 rounded-2xl border px-3.5 py-2.5 text-xs ${
          status.error ? 'border-danger/35 bg-danger/8 text-danger'
            : isFullyOnline ? 'border-accent/35 bg-accent/8 text-fg'
              : 'border-line bg-s2 text-fg2'
        }`}>
          {status.error ? <CircleAlert size={15} className="shrink-0 text-danger" />
            : isFullyOnline ? <ShieldCheck size={15} className="shrink-0 text-accent" />
              : <Loader2 size={15} className="shrink-0 animate-spin text-accent" />}
          <span className="min-w-0 flex-1 truncate">{status.error || status.status_text}</span>
          {status.mode === 'e2ee' && status.using_default_relay && (
            <span className="shrink-0 rounded-full bg-canvas/80 px-2 py-0.5 font-mono text-[10.5px] text-fg3">
              E2EE
            </span>
          )}
        </div>
      )}

      {/* 核心配对卡片：二维码 + 一键复制连接码 */}
      {isEnabled && (
        <div className="rounded-2xl border border-line2 bg-s1 p-4">
          {pair ? (
            <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
              <div
                className="size-40 shrink-0 overflow-hidden rounded-2xl border border-line bg-white p-2.5 shadow-2xs"
                dangerouslySetInnerHTML={{ __html: pair.qr_svg }}
              />
              <div className="min-w-0 flex-1 w-full space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-fg">扫码或复制连接码</span>
                  <span className="rounded-full bg-s2 px-2 py-0.5 text-[11px] text-fg3">10 分钟内有效</span>
                </div>
                <p className="text-xs leading-relaxed text-fg3">
                  在手机（iOS / Android）或另一台电脑（Mac / Windows）打开 Frog 客户端，点击「添加主机」后扫码或粘贴下方连接码，即可在公网直接连上本机。
                </p>
                <div className="flex items-center gap-2">
                  <input
                    readOnly
                    value={pair.pair_code}
                    onFocus={(e) => e.currentTarget.select()}
                    className="min-w-0 flex-1 truncate rounded-xl border border-line bg-canvas px-3 py-2 font-mono text-xs text-fg2 focus:outline-none"
                  />
                  <button
                    type="button"
                    onClick={() => void copyText(pair.pair_code)}
                    className="inline-flex shrink-0 items-center gap-1.5 rounded-xl bg-accent px-3.5 py-2 text-xs font-medium text-on-accent transition hover:opacity-90"
                  >
                    {copied ? <Check size={14} /> : <Copy size={14} />}
                    {copied ? '已复制' : '复制连接码'}
                  </button>
                </div>
                <p className="text-[11px] text-fg4">
                  端到端加密（X25519 + AES-256-GCM）：中继仅转发密文，无法查看你的聊天、文件或 API 密钥；同 Wi-Fi 下自动走局域网直连。
                </p>
              </div>
            </div>
          ) : (
            <div className="flex items-center justify-center gap-2 py-8 text-xs text-fg3">
              {pairLoading ? (
                <>
                  <Loader2 size={15} className="animate-spin text-accent" />
                  <span>正在生成加密连接码…</span>
                </>
              ) : (
                <button
                  type="button"
                  onClick={() => void generatePair()}
                  className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-4 py-2 text-xs font-medium text-on-accent transition hover:opacity-90"
                >
                  <QrCode size={14} />
                  生成二维码与连接码
                </button>
              )}
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

      {/* 已授权设备列表 */}
      {status && status.devices.length > 0 && (
        <div className="space-y-2 pt-1">
          <div className="flex items-center justify-between">
            <h4 className="text-xs font-semibold text-fg3">已连接设备 ({status.devices.length})</h4>
            <button
              type="button"
              onClick={async () => {
                if (!confirm('重置主机密钥并移除全部已连接设备？')) return;
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

      {/* 折叠的高级配置：普通用户完全不需要展开 */}
      <div className="border-t border-line pt-3">
        <button
          type="button"
          onClick={() => setShowAdvanced((v) => !v)}
          className="flex w-full items-center justify-between rounded-xl px-2 py-1.5 text-xs font-medium text-fg3 transition hover:bg-s1 hover:text-fg"
        >
          <span className="flex items-center gap-1.5">
            {showAdvanced ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            高级设置（设备名称 / 自建私有中继 / 局域网端口 / 关闭远程）
          </span>
        </button>

        {showAdvanced && (
          <div className="mt-3 space-y-4 rounded-2xl border border-line bg-s1/60 p-4">
            <div className="settings-field">
              <label htmlFor="remote-server-name">本机显示名称</label>
              <input
                id="remote-server-name"
                value={serverName}
                onChange={(e) => setServerName(e.target.value)}
                placeholder="例如：家里台式机"
              />
            </div>

            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              {advancedModes.map((c) => {
                const selected = mode === c.id;
                const Icon = c.icon;
                return (
                  <button
                    key={c.id}
                    type="button"
                    onClick={() => setMode(c.id)}
                    className={`flex flex-col items-start rounded-xl border p-2.5 text-left transition ${
                      selected ? 'border-accent bg-canvas shadow-2xs' : 'border-line bg-canvas/50 hover:border-line2'
                    }`}
                  >
                    <Icon size={15} className={selected ? 'text-accent' : 'text-fg3'} />
                    <span className="mt-1.5 text-xs font-medium text-fg">{c.title}</span>
                    <span className="mt-0.5 text-[11px] text-fg4">{c.sub}</span>
                  </button>
                );
              })}
            </div>

            {mode === 'e2ee' && (
              <div className="space-y-3">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-fg2">公网中继节点</label>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                    <button
                      type="button"
                      onClick={() => setRelaySource('default')}
                      className={`rounded-xl border px-3 py-2 text-left text-xs transition ${
                        relaySource === 'default' ? 'border-accent bg-canvas text-fg' : 'border-line bg-canvas/40 text-fg3 hover:border-line2'
                      }`}
                    >
                      <div className="font-medium">官方默认（推荐）</div>
                      <div className="mt-0.5 truncate font-mono text-[10.5px] text-fg4">{defaultRelayDisplay.replace(/^wss?:\/\//, '')}</div>
                    </button>
                    <button
                      type="button"
                      onClick={() => setRelaySource('custom')}
                      className={`rounded-xl border px-3 py-2 text-left text-xs transition ${
                        relaySource === 'custom' ? 'border-accent bg-canvas text-fg' : 'border-line bg-canvas/40 text-fg3 hover:border-line2'
                      }`}
                    >
                      <div className="font-medium">自定义私有中继</div>
                      <div className="mt-0.5 text-[10.5px] text-fg4">填写自己的 VPS / Worker</div>
                    </button>
                    <button
                      type="button"
                      onClick={() => setRelaySource('off')}
                      className={`rounded-xl border px-3 py-2 text-left text-xs transition ${
                        relaySource === 'off' ? 'border-accent bg-canvas text-fg' : 'border-line bg-canvas/40 text-fg3 hover:border-line2'
                      }`}
                    >
                      <div className="font-medium">仅局域网直连</div>
                      <div className="mt-0.5 text-[10.5px] text-fg4">不连公网中继</div>
                    </button>
                  </div>
                </div>

                {relaySource === 'custom' && (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <div className="settings-field">
                      <label htmlFor="remote-e2ee-relay">自定义 WSS 中继地址</label>
                      <input
                        id="remote-e2ee-relay"
                        value={e2eeRelayURL}
                        onChange={(e) => setE2EERelayURL(e.target.value)}
                        placeholder="wss://relay.example.com/ws"
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
                )}

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
                    <label htmlFor="remote-relay-addr">信令 / 中继服务器</label>
                    <input
                      id="remote-relay-addr"
                      value={relayAddr}
                      onChange={(e) => setRelayAddr(e.target.value)}
                      placeholder="host:18443"
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

            <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
              {isEnabled ? (
                <button
                  type="button"
                  disabled={saving}
                  onClick={() => void saveConfig({ targetMode: 'off', autoPair: false })}
                  className="inline-flex items-center gap-1.5 rounded-xl border border-danger/30 px-3 py-1.5 text-xs font-medium text-danger transition hover:bg-danger/8 disabled:opacity-50"
                >
                  <WifiOff size={13} />关闭远程连接
                </button>
              ) : <span />}
              <button
                type="button"
                className="settings-button settings-primary"
                disabled={saving}
                onClick={() => void saveConfig({ autoPair: true })}
              >
                {saving ? <Loader2 size={14} className="animate-spin" /> : null}
                保存高级设置
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
