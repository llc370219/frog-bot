import { Shield, Terminal, Hand, Network, Stethoscope, Download, Trash2, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import {
  openV2WebSocket,
  v2API,
  type V2ApprovalCard,
  type V2CLITask,
  type V2DoctorReport,
  type V2MeshPeer,
  type V2QuotaStatus,
  type V2VerifySession,
} from '../v2api';

type Tab = 'approvals' | 'verify' | 'cli' | 'mesh' | 'ops';

export default function V2ControlCenter({ onClose }: { onClose: () => void }) {
  const [tab, setTab] = useState<Tab>('approvals');
  const [nodeID, setNodeID] = useState('');
  const [wsOnline, setWsOnline] = useState(false);
  const [approvals, setApprovals] = useState<V2ApprovalCard[]>([]);
  const [verifies, setVerifies] = useState<V2VerifySession[]>([]);
  const [cliTasks, setCliTasks] = useState<V2CLITask[]>([]);
  const [quotas, setQuotas] = useState<V2QuotaStatus[]>([]);
  const [peers, setPeers] = useState<V2MeshPeer[]>([]);
  const [doctor, setDoctor] = useState<V2DoctorReport | null>(null);
  const [selectedTask, setSelectedTask] = useState('');
  const [cliLogs, setCliLogs] = useState<Record<string, string>>({});
  const [activeVerify, setActiveVerify] = useState('');
  const [frameSrc, setFrameSrc] = useState('');
  const [captchaText, setCaptchaText] = useState('');
  const [pairCode, setPairCode] = useState('');
  const [backupPass, setBackupPass] = useState('');
  const [statusMsg, setStatusMsg] = useState('');
  const wsRef = useRef<WebSocket | null>(null);
  const dragStart = useRef<{ x: number; y: number } | null>(null);

  const refresh = async () => {
    try {
      const st = await v2API.status();
      setNodeID(st.node_id || '');
      setApprovals(st.pending_approvals || []);
      setVerifies(st.pending_verifications || []);
      setCliTasks(st.cli_tasks || []);
      setQuotas(st.cli_quotas || []);
      setPeers(st.peers || []);
    } catch (e) {
      setStatusMsg((e as Error).message);
    }
  };

  useEffect(() => {
    void refresh();
    const ws = openV2WebSocket((f) => {
      if (f.type === 'hello') setWsOnline(true);
      if (f.type === 'approval.pending' && f.payload) {
        const card = f.payload as V2ApprovalCard;
        setApprovals((prev) => (prev.some((a) => a.id === card.id) ? prev : [card, ...prev]));
      }
      if (f.type === 'verify.waiting' && f.payload) {
        const s = f.payload as V2VerifySession;
        setVerifies((prev) => [s, ...prev.filter((x) => x.id !== s.id)]);
      }
      if (f.type === 'verify.frame' && f.payload) {
        const p = f.payload as { data_base64?: string };
        if (p.data_base64) setFrameSrc(`data:image/jpeg;base64,${p.data_base64}`);
      }
      if (f.type === 'cli.log.snapshot' && f.payload) {
        const p = f.payload as { task_id: string; content: string };
        setCliLogs((prev) => ({ ...prev, [p.task_id]: p.content }));
      }
      if (f.type === 'cli.log.chunk' && f.payload) {
        const p = f.payload as { task_id: string; chunk: string };
        setCliLogs((prev) => ({ ...prev, [p.task_id]: (prev[p.task_id] || '') + p.chunk }));
      }
    });
    ws.onopen = () => setWsOnline(true);
    ws.onclose = () => setWsOnline(false);
    wsRef.current = ws;
    return () => ws.close();
  }, []);

  const sendWS = (type: string, payload: unknown) => {
    wsRef.current?.send(JSON.stringify({ type, payload }));
  };

  const handleDecide = async (card: V2ApprovalCard, choice: 'allow_once' | 'allow_whitelist' | 'deny') => {
    await v2API.decideApproval(card.id, choice, card.whitelist_domain || '');
    setApprovals((prev) => prev.filter((a) => a.id !== card.id));
  };

  const handleBackup = async () => {
    if (backupPass.length < 6) {
      setStatusMsg('加密口令至少需要 6 位');
      return;
    }
    try {
      const blob = await v2API.exportBackup(backupPass);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `frogbot-backup-${Date.now()}.frogbak`;
      a.click();
      URL.revokeObjectURL(url);
      setStatusMsg('加密资产包已导出下载');
    } catch (e) {
      setStatusMsg((e as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
      <div className="flex h-[82vh] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-line bg-canvas shadow-2xl">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <div className="flex items-center gap-2">
            <Shield size={17} />
            <span className="font-semibold text-sm">Frog Bot V2 总控中心</span>
            <span className={`inline-block h-2 w-2 rounded-full ${wsOnline ? 'bg-green-500' : 'bg-amber-500'}`} />
            {nodeID && <span className="font-mono text-xs text-fg3">{nodeID}</span>}
          </div>
          <button onClick={onClose} className="rounded p-1 text-fg3 hover:bg-s2"><X size={16} /></button>
        </div>

        <div className="flex gap-1 border-b border-line px-4 py-2 text-xs">
          {([
            ['approvals', `安全审批 (${approvals.length})`, Shield],
            ['verify', `远程过验证 (${verifies.length})`, Hand],
            ['cli', `CLI 日志与额度 (${cliTasks.length})`, Terminal],
            ['mesh', `FrogLink Mesh (${peers.length})`, Network],
            ['ops', 'Host Doctor / 灾备', Stethoscope],
          ] as const).map(([id, label, Icon]) => (
            <button key={id} onClick={() => { setTab(id); if (id === 'ops' && !doctor) v2API.doctor().then(setDoctor).catch(() => {}); }}
              className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 ${tab === id ? 'bg-s2 font-medium text-fg' : 'text-fg3 hover:bg-s2/60'}`}>
              <Icon size={13} />{label}
            </button>
          ))}
        </div>

        {statusMsg && <div className="bg-s2 px-4 py-1.5 text-xs text-fg2">{statusMsg}</div>}

        <div className="flex-1 overflow-y-auto p-4 text-sm">
          {tab === 'approvals' && (
            <div className="flex flex-col gap-3">
              {approvals.length === 0 && <p className="py-12 text-center text-fg3">暂无待处理安全审批卡片</p>}
              {approvals.map((c) => (
                <div key={c.id} className="rounded-xl border border-line bg-s1 p-3.5">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2 font-medium">
                      <span>{c.action}</span>
                      {c.tainted && <span className="rounded bg-amber-500/20 px-1.5 py-0.5 text-[11px] text-amber-600">含外部污点</span>}
                    </div>
                    <span className="text-xs text-fg3">来源：{c.trigger_source}</span>
                  </div>
                  {c.target_domain && <div className="mt-1 font-mono text-xs text-fg3">目标域名：{c.target_domain}</div>}
                  <div className="mt-1.5 text-xs">{c.summary}</div>
                  <div className="mt-1 text-xs text-fg3">{c.reason}</div>
                  <div className="mt-3 flex flex-wrap gap-2">
                    <button onClick={() => handleDecide(c, 'allow_once')} className="rounded-lg bg-fg px-3 py-1 text-xs text-canvas">批准一次</button>
                    {c.can_whitelist && c.whitelist_domain && (
                      <button onClick={() => handleDecide(c, 'allow_whitelist')} className="rounded-lg border border-line px-3 py-1 text-xs">
                        批准并加白名单 ({c.whitelist_domain})
                      </button>
                    )}
                    <button onClick={() => handleDecide(c, 'deny')} className="ml-auto rounded-lg border border-red-400/50 px-3 py-1 text-xs text-red-500">拒绝</button>
                  </div>
                </div>
              ))}
            </div>
          )}

          {tab === 'verify' && (
            <div className="flex flex-col gap-3">
              {activeVerify ? (
                <div className="flex flex-col gap-2">
                  <div className="relative flex h-80 items-center justify-center overflow-hidden rounded-xl bg-black">
                    {frameSrc ? (
                      <img src={frameSrc} alt="CDP Live Verify" className="max-h-full max-w-full cursor-crosshair select-none" draggable={false}
                        onMouseDown={(e) => {
                          const r = e.currentTarget.getBoundingClientRect();
                          dragStart.current = { x: (e.clientX - r.left) / r.width, y: (e.clientY - r.top) / r.height };
                        }}
                        onMouseUp={(e) => {
                          const r = e.currentTarget.getBoundingClientRect();
                          const end = { x: (e.clientX - r.left) / r.width, y: (e.clientY - r.top) / r.height };
                          const start = dragStart.current;
                          dragStart.current = null;
                          if (!start) return;
                          const dist = Math.hypot((end.x - start.x) * r.width, (end.y - start.y) * r.height);
                          if (dist < 6) {
                            sendWS('verify.input', { session_id: activeVerify, event: { type: 'click', x: end.x, y: end.y, normalized: true } });
                          } else {
                            sendWS('verify.input', { session_id: activeVerify, event: { type: 'drag', x: start.x, y: start.y, end_x: end.x, end_y: end.y, normalized: true, duration_ms: 240 } });
                          }
                        }} />
                    ) : <span className="text-xs text-white/70">正在冷启浏览器并连接 CDP 画面流…</span>}
                  </div>
                  <div className="flex gap-2">
                    <input value={captchaText} onChange={(e) => setCaptchaText(e.target.value)} placeholder="输入验证码文本…"
                      className="flex-1 rounded-lg border border-line bg-s1 px-3 py-1.5 text-xs" />
                    <button onClick={() => { if (captchaText) { sendWS('verify.input', { session_id: activeVerify, event: { type: 'text', text: captchaText } }); setCaptchaText(''); } }}
                      className="rounded-lg border border-line px-3 py-1.5 text-xs">发送文本</button>
                    <button onClick={() => { sendWS('verify.complete', { session_id: activeVerify }); setActiveVerify(''); setFrameSrc(''); void refresh(); }}
                      className="rounded-lg bg-fg px-3 py-1.5 text-xs text-canvas">完成验证</button>
                    <button onClick={() => { sendWS('verify.cancel', { session_id: activeVerify }); setActiveVerify(''); setFrameSrc(''); }}
                      className="rounded-lg border border-line px-3 py-1.5 text-xs">取消</button>
                  </div>
                </div>
              ) : (
                <>
                  {verifies.length === 0 && <p className="py-12 text-center text-fg3">暂无等待过验证的打卡任务（浏览器闲置 0 内存）</p>}
                  {verifies.map((s) => (
                    <div key={s.id} className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3">
                      <div>
                        <div className="font-medium text-xs">{s.reason}</div>
                        <div className="text-xs text-fg3">{s.bot_id} · {s.url}</div>
                      </div>
                      <button onClick={() => { setActiveVerify(s.id); setFrameSrc(''); sendWS('verify.ready', { session_id: s.id }); }}
                        className="rounded-lg bg-fg px-3 py-1.5 text-xs text-canvas">我现在有空，开始验证</button>
                    </div>
                  ))}
                </>
              )}
            </div>
          )}

          {tab === 'cli' && (
            <div className="flex flex-col gap-3">
              <div className="grid grid-cols-3 gap-2">
                {quotas.map((q) => (
                  <div key={q.cli} className="rounded-xl border border-line bg-s1 p-2.5 text-xs">
                    <div className="flex justify-between font-medium"><span>{q.cli}</span><span>{q.health}</span></div>
                    <div className="mt-1 text-fg3">估算余量 {q.estimated_remaining_pct.toFixed(0)}% · 槽位 {q.active_slots}/{q.max_slots}</div>
                  </div>
                ))}
              </div>
              <div className="grid grid-cols-3 gap-3">
                <div className="flex flex-col gap-1.5">
                  {cliTasks.length === 0 && <div className="text-xs text-fg3">暂无 CLI 任务</div>}
                  {cliTasks.map((t) => (
                    <button key={t.id} onClick={() => { setSelectedTask(t.id); sendWS('cli.log.subscribe', { task_id: t.id }); }}
                      className={`rounded-lg border p-2 text-left text-xs ${selectedTask === t.id ? 'border-fg bg-s2' : 'border-line'}`}>
                      <div className="flex justify-between font-medium"><span>{t.cli}/{t.mode}</span><span>{t.status}</span></div>
                      <div className="truncate text-fg3">{t.prompt_brief || t.id}</div>
                    </button>
                  ))}
                </div>
                <div className="col-span-2 flex flex-col rounded-xl border border-line bg-black/90 text-gray-200">
                  <div className="flex items-center justify-between border-b border-white/10 px-3 py-2 text-xs">
                    <span className="font-mono">{selectedTask || '选择任务查看实时日志'}</span>
                    {selectedTask && (
                      <button onClick={() => v2API.cancelCLI(selectedTask).then(refresh)} className="rounded bg-red-600 px-2 py-0.5 text-[11px] text-white">紧急叫停 Cancel</button>
                    )}
                  </div>
                  <pre className="h-64 overflow-auto p-3 font-mono text-[11px] whitespace-pre-wrap">{selectedTask ? (cliLogs[selectedTask] || '等待日志流…') : ''}</pre>
                </div>
              </div>
            </div>
          )}

          {tab === 'mesh' && (
            <div className="flex flex-col gap-3">
              <div className="flex items-center gap-2">
                <button onClick={() => v2API.pairCode().then((r) => setPairCode(r.code))}
                  className="rounded-lg bg-fg px-3 py-1.5 text-xs text-canvas">生成 Ed25519 一键配对码</button>
                {pairCode && <span className="rounded bg-s2 px-2.5 py-1 font-mono text-xs">{pairCode}</span>}
              </div>
              {peers.length === 0 && <p className="py-8 text-center text-fg3">暂未配对其他 FrogLink 节点</p>}
              {peers.map((p) => (
                <div key={p.node_id} className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3 text-xs">
                  <div>
                    <div className="font-medium">{p.node_id} {p.network_tag}</div>
                    <div className="text-fg3">Bots: {(p.bots || []).join(', ')}</div>
                  </div>
                  <span className={p.online ? 'text-green-600' : 'text-fg3'}>{p.online ? `在线 (${p.connected_via || 'mesh'})` : '离线'}</span>
                </div>
              ))}
            </div>
          )}

          {tab === 'ops' && (
            <div className="flex flex-col gap-4">
              <div className="rounded-xl border border-line bg-s1 p-3">
                <div className="mb-2 flex items-center justify-between">
                  <span className="font-medium text-xs">Host Doctor 环境自检</span>
                  <button onClick={() => v2API.doctor(true).then(setDoctor)} className="rounded border border-line px-2 py-0.5 text-xs">自检并修复权限</button>
                </div>
                {doctor?.checks.map((c) => (
                  <div key={c.id} className="border-t border-line/60 py-1.5 text-xs">
                    <div className="flex justify-between"><span className="font-medium">{c.name}</span><span>{c.status}</span></div>
                    <div className="text-fg3">{c.detail}</div>
                    {c.install_cmd && <code className="mt-1 block rounded bg-s2 px-2 py-1 text-[11px]">{c.install_cmd}</code>}
                  </div>
                ))}
              </div>
              <div className="flex flex-wrap items-center gap-2 rounded-xl border border-line bg-s1 p-3 text-xs">
                <button onClick={() => v2API.runGC().then((r) => setStatusMsg(`磁盘 GC 完成：清理指针 ${r.pointers_removed}、日志 ${r.logs_removed_by_age + r.logs_removed_by_cap}，回收 ${(r.bytes_reclaimed / 1024).toFixed(1)} KB`))}
                  className="flex items-center gap-1 rounded-lg border border-line px-3 py-1.5"><Trash2 size={13} />立即执行磁盘 GC</button>
                <input type="password" value={backupPass} onChange={(e) => setBackupPass(e.target.value)} placeholder="加密迁移口令（≥6位）"
                  className="rounded-lg border border-line bg-canvas px-2.5 py-1.5" />
                <button onClick={handleBackup} className="flex items-center gap-1 rounded-lg bg-fg px-3 py-1.5 text-canvas">
                  <Download size={13} />一键导出加密资产包 (.frogbak)
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
