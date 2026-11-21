import {
  Check, CircleAlert, Cloud, Copy, KeyRound, Loader2, QrCode, RefreshCw, Server, ShieldCheck, Smartphone, Trash2, WifiOff,
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

  // 隧道正在准备时自动每 2 秒轮询直到就绪
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
      setFeedback({
        error: false,
        text: st.mode === 'off' ? '已关闭远程访问' : (st.ready ? '加密通道已就绪，现在可以生成手机配对码' : '已启动，正在建立加密通道…'),
      });
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
      const res = await remoteAPI.createPair();
      setPair(res);
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
      window.setTimeout(() => setter(false), 1800);
    } catch { /* ignore */ }
  };

  const modeCards: { id: RemoteMode; icon: typeof Cloud; title: string; tag?: string; desc: string }[] = [
    {
      id: 'quick',
      icon: Cloud,
      title: '一键云穿透',
      tag: '推荐 · 免公网 / 免配置',
      desc: '适合家里电脑：无需公网 IP、无需开端口、无需装任何外置软件，一键建立 HTTPS 加密隧道。',
    },
    {
      id: 'bridge',
      icon: Server,
      title: '自建加密中转穿透',
      tag: '免公网端口 · 自有 VPS',
      desc: '适合家里电脑 + 自有轻量 VPS：本机零端口暴露，主动连向你的中转节点，数据全程不经过第三方。',
    },
    {
      id: 'direct',
      icon: ShieldCheck,
      title: '直连加密模式',
      tag: '云服务器 / 局域网',
      desc: '适合有公网 IP 的服务器或局域网：自动生成 TLS 1.3 证书并锁定指纹 + 随机隐藏路径，免域名免 Nginx 防扫描。',
    },
    {
      id: 'off',
      icon: WifiOff,
      title: '关闭远程访问',
      desc: '仅允许当前监听方式访问，断开所有远程隧道与独立加密网关。',
    },
  ];

  return (
    <div className="space-y-5">
      <div>
        <h3 id="settings-remote-title">远程安全连接</h3>
        <p className="settings-description">
          内置加密穿透与设备配对，无需外置安装 VPN 或反向代理。开启后在手机 App 扫码或粘贴配对码即可一键安全连回本机。
        </p>
      </div>

      {/* 当前运行状态条 */}
      {status && (
        <div className={`flex flex-wrap items-center justify-between gap-3 rounded-2xl border p-3.5 ${
          status.mode === 'off'
            ? 'border-line bg-s2/50'
            : status.ready
              ? 'border-accent/35 bg-accent/8'
              : status.error
                ? 'border-danger/35 bg-danger/8'
                : 'border-line2 bg-s2'
        }`}>
          <div className="flex min-w-0 items-center gap-2.5">
            {status.mode === 'off' ? (
              <WifiOff size={16} className="shrink-0 text-fg4" />
            ) : status.ready ? (
              <ShieldCheck size={17} className="shrink-0 text-accent" />
            ) : (
              <Loader2 size={16} className="shrink-0 animate-spin text-accent" />
            )}
            <div className="min-w-0 text-xs">
              <div className="font-medium text-fg">{status.status_text}</div>
              {status.error && <div className="mt-0.5 text-danger">{status.error}</div>}
            </div>
          </div>
          {status.ready && (
            <button
              type="button"
              onClick={() => void generatePair()}
              disabled={pairLoading}
              className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-3.5 py-1.5 text-xs font-medium text-on-accent transition hover:opacity-90 disabled:opacity-50"
            >
              {pairLoading ? <Loader2 size={14} className="animate-spin" /> : <QrCode size={14} />}
              生成手机配对码
            </button>
          )}
        </div>
      )}

      {/* 配对二维码与一键复制面板 */}
      {pair && (
        <div className="rounded-2xl border border-accent/40 bg-s1 p-4 shadow-xs">
          <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
            <div
              className="size-40 shrink-0 overflow-hidden rounded-xl border border-line bg-white p-2"
              dangerouslySetInnerHTML={{ __html: pair.qr_svg }}
            />
            <div className="min-w-0 flex-1 space-y-2.5 text-center sm:text-left">
              <div className="text-sm font-semibold text-fg">用 iPhone 扫码或粘贴配对码连接</div>
              <p className="text-xs leading-relaxed text-fg3">
                打开手机 frog App → 添加服务器 → 点击「扫码配对」或「粘贴配对码一键连接」。此配对码仅限单次使用，10 分钟内有效。
              </p>
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
                  {copied ? '已复制' : '复制配对码'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* 模式选择卡片 */}
      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
        {modeCards.map((c) => {
          const selected = mode === c.id;
          const Icon = c.icon;
          return (
            <button
              key={c.id}
              type="button"
              onClick={() => setMode(c.id)}
              className={`flex flex-col items-start rounded-2xl border p-3.5 text-left transition ${
                selected ? 'border-accent bg-s1 shadow-2xs' : 'border-line bg-canvas hover:border-line2 hover:bg-s1/60'
              }`}
            >
              <div className="flex w-full items-center justify-between gap-2">
                <div className="flex items-center gap-2 font-medium text-fg">
                  <Icon size={15} className={selected ? 'text-accent' : 'text-fg3'} />
                  <span className="text-[13.5px]">{c.title}</span>
                </div>
                {c.tag && (
                  <span className="rounded-full bg-s2 px-2 py-0.5 text-[10.5px] text-fg3">{c.tag}</span>
                )}
              </div>
              <p className="mt-1.5 text-xs leading-relaxed text-fg3">{c.desc}</p>
            </button>
          );
        })}
      </div>

      {/* 按所选模式展开参数 */}
      {mode !== 'off' && (
        <div className="space-y-3 rounded-2xl border border-line bg-s1/60 p-4">
          <div className="settings-field">
            <label htmlFor="remote-server-name">设备显示名称（手机上看到的名字）</label>
            <input
              id="remote-server-name"
              value={serverName}
              onChange={(e) => setServerName(e.target.value)}
              placeholder="例如：家里 MacBook / 东京服务器"
            />
          </div>

          {mode === 'bridge' && (
            <div className="space-y-2">
              <div className="settings-field">
                <label htmlFor="remote-bridge-uri">自建中转连接串（frog-bridge://...）</label>
                <input
                  id="remote-bridge-uri"
                  value={bridgeURI}
                  onChange={(e) => setBridgeURI(e.target.value)}
                  placeholder="frog-bridge://1.2.3.4:8443#令牌.证书指纹"
                  className="font-mono text-xs"
                />
              </div>
              <div className="rounded-xl bg-s2 p-2.5 text-xs text-fg3">
                <div className="flex items-center justify-between gap-2">
                  <span>在你的轻量公网 VPS 上只需运行这一行命令即可启动中转：</span>
                  <button
                    type="button"
                    onClick={() => void copyText(status?.bridge_command || 'frogbot bridge -listen :8443', setCmdCopied)}
                    className="inline-flex items-center gap-1 text-accent hover:underline"
                  >
                    {cmdCopied ? <Check size={12} /> : <Copy size={12} />}
                    {cmdCopied ? '已复制' : '复制命令'}
                  </button>
                </div>
                <code className="mt-1 block font-mono text-[11.5px] text-fg">{status?.bridge_command || 'frogbot bridge -listen :8443'}</code>
              </div>
            </div>
          )}

          {mode === 'direct' && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="settings-field">
                <label htmlFor="remote-public-host">对外访问 IP 或域名</label>
                <input
                  id="remote-public-host"
                  value={publicHost}
                  onChange={(e) => setPublicHost(e.target.value)}
                  placeholder={status?.detected_ip ? `留空默认使用 ${status.detected_ip}` : '例如 203.0.113.10'}
                  className="font-mono text-xs"
                />
              </div>
              <div className="settings-field">
                <label htmlFor="remote-direct-listen">加密监听端口</label>
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
          {saving ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
          {mode === 'off' ? '保存并关闭' : '保存并启用'}
        </button>
      </div>

      {/* 已配对设备管理 */}
      {status && (status.devices.length > 0 || status.mode !== 'off') && (
        <div className="border-t border-line pt-4">
          <div className="mb-2.5 flex items-center justify-between">
            <h4 className="text-sm font-semibold text-fg">已授权的远程设备（{status.devices.length}）</h4>
            <button
              type="button"
              onClick={async () => {
                if (!confirm('重置安全路径会让之前复制但未使用的地址立即失效，是否同时踢出所有已绑定手机？\n点「确定」同时清空已授权设备。')) return;
                const st = await remoteAPI.rotateSecret(true);
                syncFromStatus(st, false);
                setPair(null);
              }}
              className="inline-flex items-center gap-1 text-xs text-fg3 hover:text-danger"
            >
              <KeyRound size={12} />重置安全路径与全部授权
            </button>
          </div>
          {status.devices.length === 0 ? (
            <p className="text-xs text-fg4">还没有配对过远程设备。</p>
          ) : (
            <div className="divide-y divide-line rounded-2xl border border-line bg-s1">
              {status.devices.map((d) => (
                <div key={d.id} className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-xs">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <Smartphone size={15} className="shrink-0 text-fg3" />
                    <div className="min-w-0">
                      <div className="truncate font-medium text-fg">{d.name}</div>
                      <div className="text-[11px] text-fg4">
                        最近活跃：{new Date(d.last_seen_at).toLocaleString('zh-CN')}
                      </div>
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={async () => {
                      const st = await remoteAPI.revokeDevice(d.id);
                      syncFromStatus(st, false);
                    }}
                    title="移除该设备授权"
                    className="inline-flex items-center gap-1 rounded-lg px-2 py-1 text-danger transition hover:bg-s2"
                  >
                    <Trash2 size={13} />移除
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
