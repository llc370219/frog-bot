import {
  AlertTriangle, Check, ChevronDown, Clock, Folder, FolderOpen, Loader2,
  Play, ShieldAlert, Slash, Square, SquareTerminal, Wrench, X, Zap,
} from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import {
  agyAPI, type AgyChatMessage, type AgyCommandOption, type AgyFileEntry, type AgyModeOption, type AgyModelOption,
  type AgyPermission, type AgyProject, type AgyQueuedPrompt, type AgySessionSummary, type AgySnapshot, type AgyStatus, type AgyToolCall,
} from '../api';
import { AssistantBubbles } from './Markdown';
import ThinkingBlock from './ThinkingBlock';

const ACTIVE_PROJ_KEY = 'frogbot-agy-project';
const ACTIVE_SESS_KEY = 'frogbot-agy-session';

type LiveState = {
  ready: boolean;
  connected: boolean;
  session: AgySessionSummary | null;
  messages: AgyChatMessage[];
  permissions: AgyPermission[];
  queue: AgyQueuedPrompt[];
  streaming: string;
  thinking: string;
  thinkStart: number;
  thinkEnd: number;
  liveTools: AgyToolCall[];
  usedTokens: number;
  maxTokens: number;
  modes: AgyModeOption[];
  models: AgyModelOption[];
  commands: AgyCommandOption[];
};

const emptyLive = (): LiveState => ({
  ready: false, connected: false, session: null, messages: [], permissions: [], queue: [],
  streaming: '', thinking: '', thinkStart: 0, thinkEnd: 0, liveTools: [],
  usedTokens: 0, maxTokens: 0, modes: [], models: [], commands: [],
});

export type CodingController = {
  status: AgyStatus | null;
  activeAgent: string;
  selectAgent: (agentId: string) => Promise<void>;
  projects: AgyProject[];
  activeProject: AgyProject | null;
  sessions: AgySessionSummary[];
  sessionId: string;
  loadingSessions: boolean;
  error: string;
  setError: (e: string) => void;
  selectProject: (id: string) => void;
  selectSession: (id: string) => void;
  newSession: (mode?: string, model?: string, agent?: string) => Promise<void>;
  deleteSession: (id: string) => Promise<void>;
  deleteProject: (p: AgyProject) => Promise<void>;
  openNewProject: () => void;
  openFiles: () => void;
  newProjModal: boolean;
  setNewProjModal: (open: boolean) => void;
  filesModal: boolean;
  setFilesModal: (open: boolean) => void;
  onProjectCreated: (p: AgyProject) => void;
  syncSessionUpdate: (fn: (prev: AgySessionSummary[]) => AgySessionSummary[]) => void;
  ensureSession: (mode?: string, model?: string) => Promise<string>;
};

export function useCodingWorkspace(enabled: boolean): CodingController {
  const [status, setStatus] = useState<AgyStatus | null>(null);
  const [projects, setProjects] = useState<AgyProject[]>([]);
  const [projectId, setProjectId] = useState(() => localStorage.getItem(ACTIVE_PROJ_KEY) || '');
  const [sessions, setSessions] = useState<AgySessionSummary[]>([]);
  const [sessionId, setSessionId] = useState(() => localStorage.getItem(ACTIVE_SESS_KEY) || '');
  const [loadingSessions, setLoadingSessions] = useState(false);
  const [error, setError] = useState('');
  const [newProjModal, setNewProjModal] = useState(false);
  const [filesModal, setFilesModal] = useState(false);

  const activeProject = projects.find((p) => p.id === projectId) ?? projects[0] ?? null;

  const refreshProjects = useCallback(async () => {
    try {
      const [st, projs] = await Promise.all([agyAPI.status(), agyAPI.projects()]);
      setStatus(st);
      setProjects(projs);
      if (projs.length > 0 && !projs.some((p) => p.id === projectId)) {
        setProjectId(projs[0].id);
        localStorage.setItem(ACTIVE_PROJ_KEY, projs[0].id);
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    if (enabled) void refreshProjects();
  }, [enabled, refreshProjects]);

  const refreshSessions = useCallback(async (pid: string, autoSelect = true) => {
    if (!pid) return;
    setLoadingSessions(true);
    try {
      const list = await agyAPI.sessions(pid);
      setSessions(list);
      if (autoSelect) {
        setSessionId((cur) => {
          if (cur && list.some((s) => s.id === cur)) return cur;
          const next = list[0]?.id || '';
          if (next) localStorage.setItem(ACTIVE_SESS_KEY, next);
          return next;
        });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoadingSessions(false);
    }
  }, []);

  useEffect(() => {
    if (enabled && activeProject?.id) {
      localStorage.setItem(ACTIVE_PROJ_KEY, activeProject.id);
      void refreshSessions(activeProject.id, true);
    }
  }, [enabled, activeProject?.id, refreshSessions]);

  const selectProject = useCallback((id: string) => {
    setProjectId(id);
    setSessionId('');
  }, []);

  const selectSession = useCallback((id: string) => {
    setSessionId(id);
    if (id) localStorage.setItem(ACTIVE_SESS_KEY, id);
  }, []);

  const selectAgent = useCallback(async (agentId: string) => {
    setError('');
    try {
      const st = await agyAPI.setDefaultAgent(agentId);
      setStatus(st);
      setSessionId('');
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  const newSession = useCallback(async (mode?: string, model?: string, agent?: string) => {
    if (!activeProject) return;
    setError('');
    const targetAgent = agent || status?.default_agent || '';
    try {
      const snap = await agyAPI.createSession(
        activeProject.id,
        mode ?? status?.default_mode ?? '',
        model ?? status?.default_model ?? '',
        targetAgent,
      );
      setSessions((prev) => [snap.session, ...prev.filter((s) => s.id !== snap.session.id)]);
      setSessionId(snap.session.id);
      localStorage.setItem(ACTIVE_SESS_KEY, snap.session.id);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [activeProject, status]);

  const ensureSession = useCallback(async (mode?: string, model?: string) => {
    if (sessionId) return sessionId;
    if (!activeProject) return '';
    const snap = await agyAPI.createSession(
      activeProject.id,
      mode ?? status?.default_mode ?? '',
      model ?? status?.default_model ?? '',
      status?.default_agent || '',
    );
    setSessions((prev) => [snap.session, ...prev]);
    setSessionId(snap.session.id);
    localStorage.setItem(ACTIVE_SESS_KEY, snap.session.id);
    return snap.session.id;
  }, [sessionId, activeProject, status]);

  const deleteSession = useCallback(async (id: string) => {
    try {
      await agyAPI.deleteSession(id);
      setSessions((prev) => {
        const next = prev.filter((s) => s.id !== id);
        if (sessionId === id) setSessionId(next[0]?.id || '');
        return next;
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }, [sessionId]);

  const deleteProject = useCallback(async (p: AgyProject) => {
    const delDisk = p.managed && confirm(`是否同时删除服务器磁盘上的文件夹「${p.path}」？\n点「取消」则仅从列表移除。`);
    if (!p.managed && !confirm(`从列表移除「${p.name}」？（不会删除磁盘文件）`)) return;
    try {
      await agyAPI.deleteProject(p.id, delDisk);
      await refreshProjects();
    } catch (e) {
      setError((e as Error).message);
    }
  }, [refreshProjects]);

  const onProjectCreated = useCallback((p: AgyProject) => {
    setProjects((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
    setProjectId(p.id);
    setSessionId('');
    setNewProjModal(false);
  }, []);

  return {
    status,
    activeAgent: status?.default_agent || 'agy',
    selectAgent,
    projects,
    activeProject,
    sessions,
    sessionId,
    loadingSessions,
    error,
    setError,
    selectProject,
    selectSession,
    newSession,
    deleteSession,
    deleteProject,
    openNewProject: () => setNewProjModal(true),
    openFiles: () => setFilesModal(true),
    newProjModal,
    setNewProjModal,
    filesModal,
    setFilesModal,
    onProjectCreated,
    syncSessionUpdate: setSessions,
    ensureSession,
  };
}

export default function AgyWorkspace({ coding }: { coding: CodingController }) {
  const {
    status, activeAgent, selectAgent, activeProject, sessionId, error, setError,
    newProjModal, setNewProjModal, filesModal, setFilesModal,
    onProjectCreated, syncSessionUpdate, ensureSession,
  } = coding;

  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);

  const liveRef = useRef<LiveState>(emptyLive());
  const [, bump] = useState(0);
  const rerender = useCallback(() => bump((n) => n + 1), []);

  // 连接当前对话的 SSE 事件流
  useEffect(() => {
    liveRef.current = emptyLive();
    rerender();
    if (!sessionId || !activeProject?.id) return;

    let es: EventSource | null = null;
    let retry = 0;
    let closed = false;

    const connect = () => {
      const url = `/api/agy/sessions/${encodeURIComponent(sessionId)}/stream?project_id=${encodeURIComponent(activeProject.id)}`;
      es = new EventSource(url);
      es.onopen = () => {
        liveRef.current.connected = true;
        rerender();
      };
      es.onmessage = (m) => {
        const ev = JSON.parse(m.data) as { kind: string; data: any };
        const s = liveRef.current;
        const d = ev.data;
        switch (ev.kind) {
          case 'snapshot': {
            const snap = d as AgySnapshot;
            s.ready = true;
            s.connected = true;
            s.session = snap.session;
            s.messages = snap.messages || [];
            s.permissions = snap.permissions || [];
            s.queue = snap.queue || [];
            s.streaming = snap.streaming || '';
            s.thinking = snap.thinking || '';
            s.thinkStart = snap.think_start || 0;
            s.thinkEnd = snap.think_end || 0;
            s.liveTools = snap.live_tools || [];
            s.usedTokens = snap.used_tokens || 0;
            s.maxTokens = snap.max_tokens || 0;
            if (snap.modes?.length) s.modes = snap.modes;
            if (snap.models?.length) s.models = snap.models;
            if (snap.commands?.length) s.commands = snap.commands;
            break;
          }
          case 'queue.update':
            s.queue = (d as AgyQueuedPrompt[]) || [];
            break;
          case 'commands':
            s.commands = (d as AgyCommandOption[]) || [];
            break;
          case 'message': {
            const msg = d as AgyChatMessage;
            if (!s.messages.some((x) => x.id === msg.id)) s.messages = [...s.messages, msg];
            if (msg.role === 'assistant') {
              s.streaming = '';
              s.thinking = '';
              s.thinkStart = 0;
              s.thinkEnd = 0;
              s.liveTools = [];
            }
            break;
          }
          case 'thinking.delta':
            if (!s.thinkStart) s.thinkStart = Date.now();
            s.thinkEnd = 0;
            s.thinking += d.text || '';
            break;
          case 'text.delta':
            if (s.thinkStart && !s.thinkEnd) s.thinkEnd = Date.now();
            s.streaming += d.text || '';
            break;
          case 'tool.upsert': {
            if (s.thinkStart && !s.thinkEnd) s.thinkEnd = Date.now();
            const tc = d as AgyToolCall;
            const idx = s.liveTools.findIndex((x) => x.id === tc.id);
            if (idx >= 0) {
              const next = [...s.liveTools];
              next[idx] = tc;
              s.liveTools = next;
            } else {
              s.liveTools = [...s.liveTools, tc];
            }
            break;
          }
          case 'permission.upsert': {
            const pr = d as AgyPermission;
            const idx = s.permissions.findIndex((x) => x.id === pr.id);
            if (idx >= 0) {
              const next = [...s.permissions];
              next[idx] = pr;
              s.permissions = next;
            } else {
              s.permissions = [...s.permissions, pr];
            }
            break;
          }
          case 'status':
            if (s.session) {
              s.session = { ...s.session, busy: !!d.busy, title: d.title || s.session.title };
              syncSessionUpdate((prev) => prev.map((x) => x.id === s.session?.id ? { ...x, busy: !!d.busy, title: s.session!.title } : x));
            }
            break;
          case 'session.meta':
            if (d.session) {
              s.session = d.session;
              syncSessionUpdate((prev) => prev.map((x) => x.id === d.session.id ? d.session : x));
            }
            if (d.used_tokens) s.usedTokens = d.used_tokens;
            if (d.max_tokens) s.maxTokens = d.max_tokens;
            break;
          case 'turn.done':
            if (s.session) s.session = { ...s.session, busy: !!d?.busy };
            s.streaming = '';
            s.thinking = '';
            s.thinkStart = 0;
            s.thinkEnd = 0;
            s.liveTools = [];
            syncSessionUpdate((prev) => prev.map((x) => x.id === sessionId ? { ...x, busy: !!d?.busy } : x));
            break;
        }
        rerender();
      };
      es.onerror = () => {
        liveRef.current.connected = false;
        rerender();
        if (es?.readyState === EventSource.CLOSED && !closed) {
          es.close();
          retry = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      closed = true;
      es?.close();
      window.clearTimeout(retry);
    };
  }, [sessionId, activeProject?.id, rerender, syncSessionUpdate]);

  const live = liveRef.current;
  const modes = live.modes.length ? live.modes : (status?.modes || []);
  const models = live.models.length ? live.models : (status?.models || []);
  const commands = live.commands.length ? live.commands : (status?.commands || []);
  const curMode = live.session?.mode || status?.default_mode || 'auto';
  const curModel = live.session?.model || status?.default_model || '';
  const busy = !!live.session?.busy;
  const [cmdOpen, setCmdOpen] = useState(false);

  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  const handleSend = async (textOverride?: string, behavior?: 'follow_up' | 'steer') => {
    const text = (textOverride ?? draft).trim();
    if (!text || !activeProject || sending) return;
    setError('');
    setSending(true);
    setCmdOpen(false);
    try {
      const targetSession = await ensureSession(curMode, curModel);
      if (!targetSession) return;
      if (textOverride === undefined) setDraft('');
      stick.current = true;
      await agyAPI.send(targetSession, activeProject.id, text, behavior);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSending(false);
    }
  };

  const handleChangeMode = async (mode: string) => {
    if (!sessionId) return;
    try {
      const updated = await agyAPI.updateConfig(sessionId, { mode });
      liveRef.current.session = updated;
      rerender();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const handleChangeModel = async (model: string) => {
    if (!sessionId) return;
    try {
      const updated = await agyAPI.updateConfig(sessionId, { model });
      liveRef.current.session = updated;
      rerender();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="flex min-w-0 flex-1 flex-col overflow-hidden bg-canvas">
      {/* 顶栏：当前项目、对话标题、模式与模型直控 */}
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-line bg-s1/60 px-4 py-2.5 pr-24">
        <div className="flex min-w-0 items-center gap-2.5">
          <SquareTerminal size={17} className="shrink-0 text-accent" />
          <div className="min-w-0">
            <div className="truncate text-[14.5px] font-semibold text-fg">
              {live.session?.title || activeProject?.name || 'Coding'}
            </div>
            {activeProject && (
              <div className="truncate font-mono text-[11px] text-fg4">{activeProject.path}</div>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {activeProject && (
            <button type="button" onClick={() => setFilesModal(true)} title="浏览项目文件"
              className="inline-flex items-center gap-1 rounded-lg border border-line bg-canvas px-2.5 py-1 text-xs text-fg2 transition hover:bg-s2">
              <FolderOpen size={13} />文件
            </button>
          )}
          {(status?.agents || []).filter((a) => a.installed).length > 0 && (
            <select
              aria-label="Coding CLI"
              value={live.session?.agent || activeAgent}
              onChange={(e) => void selectAgent(e.target.value)}
              className="rounded-lg border border-line bg-canvas px-2 py-1 text-xs font-medium text-fg focus:outline-none"
            >
              {(status?.agents || []).filter((a) => a.installed).map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          )}
          {modes.length > 0 && (
            <select aria-label="Coding 权限模式" value={curMode} onChange={(e) => handleChangeMode(e.target.value)}
              disabled={!sessionId}
              className="rounded-lg border border-line bg-canvas px-2 py-1 text-xs text-fg2 focus:outline-none disabled:opacity-50">
              {modes.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
            </select>
          )}
          {models.length > 0 && (
            <select aria-label="Coding 模型" value={curModel} onChange={(e) => handleChangeModel(e.target.value)}
              disabled={!sessionId}
              className="max-w-[190px] truncate rounded-lg border border-line bg-canvas px-2 py-1 text-xs text-fg2 focus:outline-none disabled:opacity-50">
              {models.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
            </select>
          )}
          {live.usedTokens > 0 && (
            <span className="rounded-full bg-s2 px-2 py-0.5 font-mono text-[11px] text-fg4" title="当前会话上下文用量">
              {Math.round(live.usedTokens / 1000)}k{live.maxTokens ? ` / ${Math.round(live.maxTokens / 1000)}k` : ''}
            </span>
          )}
        </div>
      </header>

      {status && (!status.installed || status.error) && (
        <div className="flex items-center gap-2 border-b border-line bg-warn/10 px-4 py-2 text-xs text-fg2">
          <AlertTriangle size={14} className="shrink-0 text-warn" />
          <span className="flex-1">
            {status.error || '未检测到已安装的 Coding CLI'}
          </span>
        </div>
      )}

      {/* 消息流 */}
      <div ref={scroller}
        onScroll={(e) => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; }}
        className="flex-1 overflow-y-auto">
        <main className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 pb-8 pt-6">
          {(!sessionId || (live.ready && live.messages.length === 0 && !busy)) && (
            <div className="flex flex-col items-center px-2 pt-[12vh] text-center">
              <div className="grid size-14 place-items-center rounded-2xl bg-s2 text-accent">
                <SquareTerminal size={28} />
              </div>
              <h2 className="mt-4 font-serif text-2xl font-semibold">{activeProject?.name || 'Coding'}</h2>
              <div className="mt-6 flex flex-wrap justify-center gap-2">
                {['梳理当前项目结构', '运行测试并排查问题', '规划新功能改动方案'].map((q) => (
                  <button key={q} type="button" onClick={() => void handleSend(q)}
                    className="rounded-full border border-line bg-s1 px-3.5 py-1.5 text-xs text-fg2 transition hover:border-line2 hover:bg-s2">
                    {q}
                  </button>
                ))}
              </div>
            </div>
          )}

          {sessionId && !live.ready && (
            <div className="flex justify-center pt-[18vh]"><span className="typing"><i /><i /><i /></span></div>
          )}

          {live.messages.map((m) => (
            <AgyMessageItem key={m.id} m={m} />
          ))}

          {live.permissions.filter((p) => p.status === 'pending').map((p) => (
            <AgyPermCard key={p.id} perm={p} onDecide={async (optId) => {
              try { await agyAPI.decidePermission(sessionId, p.id, optId); } catch (e) { setError((e as Error).message); }
            }} />
          ))}

          {busy && (
            <div className="rise flex flex-col gap-2 rounded-2xl border border-line bg-s1/60 p-4">
              {live.thinking && (
                <ThinkingBlock live text={live.thinking} startedAt={live.thinkStart} endedAt={live.thinkEnd || undefined} />
              )}
              {live.liveTools.length > 0 && <ToolCallList tools={live.liveTools} />}
              {live.streaming ? (
                <AssistantBubbles text={live.streaming} streaming />
              ) : (
                <div className="flex items-center gap-2 text-xs text-fg4">
                  <Loader2 size={13} className="animate-spin text-accent" />
                  <span>Agent 正在执行…</span>
                </div>
              )}
            </div>
          )}
        </main>
      </div>

      {/* 底部输入框 */}
      <div className="border-t border-line bg-canvas px-4 py-3">
        {error && (
          <p className="mx-auto mb-2 max-w-3xl text-center text-xs text-danger">
            {error}
            <button type="button" onClick={() => setError('')} className="ml-2 text-fg4 underline">关闭</button>
          </p>
        )}
        {live.queue.length > 0 && (
          <div className="mx-auto mb-2 flex max-w-3xl flex-wrap gap-1.5">
            {live.queue.map((q) => (
              <span key={q.id}
                className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-line bg-s1 px-2.5 py-1 text-xs text-fg2">
                <Clock size={12} className="shrink-0 text-accent" />
                <span className="shrink-0 text-[11px] text-fg4">{q.behavior === 'steer' ? '插话' : '排队'}</span>
                <span className="max-w-[260px] truncate">{q.text}</span>
                <button type="button" onClick={() => sessionId && void agyAPI.cancelQueued(sessionId, q.id)}
                  title="撤回" className="rounded-full p-0.5 text-fg4 hover:bg-s2 hover:text-danger">
                  <X size={12} />
                </button>
              </span>
            ))}
          </div>
        )}
        {cmdOpen && commands.length > 0 && (
          <div className="mx-auto mb-2 flex max-w-3xl flex-wrap gap-1.5 rounded-2xl border border-line bg-s1 p-2">
            {commands.map((c) => (
              <button key={c.name} type="button"
                onClick={() => {
                  const next = `/${c.name}${c.input_hint ? ' ' : ''}`;
                  setDraft(next);
                  setCmdOpen(false);
                }}
                title={c.description || c.name}
                className="inline-flex items-center gap-1.5 rounded-xl border border-line bg-canvas px-2.5 py-1 text-xs text-fg2 hover:border-line2 hover:bg-s2">
                <span className="font-mono font-medium text-accent">/{c.name}</span>
                {c.description && <span className="max-w-[160px] truncate text-[11px] text-fg4">{c.description}</span>}
              </button>
            ))}
          </div>
        )}
        <div className="mx-auto flex max-w-3xl items-end gap-2 rounded-2xl border border-line2 bg-s1 px-3 py-2 shadow-2xs">
          {commands.length > 0 && (
            <button type="button" onClick={() => setCmdOpen((v) => !v)} title="斜杠命令"
              className={`grid size-8 shrink-0 place-items-center rounded-full text-fg3 transition hover:bg-s2 ${cmdOpen ? 'bg-s2 text-accent' : ''}`}>
              <Slash size={14} />
            </button>
          )}
          <textarea
            rows={1}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void handleSend(undefined, busy ? 'follow_up' : undefined);
              } else if (e.key === 'Escape' && busy && sessionId) {
                e.preventDefault();
                void agyAPI.stop(sessionId);
              }
            }}
            placeholder={activeProject
              ? (busy ? '执行中，发送将自动排队，或点击右侧闪电打断插话' : `在 ${activeProject.name} 中输入 Coding 指令…（Enter 发送，Shift+Enter 换行）`)
              : '请先新建或选择一个项目文件夹'}
            className="max-h-40 min-h-[26px] flex-1 resize-none bg-transparent py-1 text-[14.5px] leading-relaxed text-fg placeholder:text-fg4 focus:outline-none"
          />
          {busy && !draft.trim() ? (
            <button type="button" onClick={() => sessionId && void agyAPI.stop(sessionId)} title="停止当前执行 (Esc)"
              className="grid size-8 shrink-0 place-items-center rounded-full bg-fg text-canvas transition hover:opacity-85">
              <Square size={13} fill="currentColor" />
            </button>
          ) : (
            <div className="flex shrink-0 items-center gap-1.5">
              {busy && draft.trim() && (
                <button type="button" onClick={() => void handleSend(undefined, 'steer')} disabled={sending || !activeProject}
                  title="打断当前步骤并立刻执行这条指令 (Steer)"
                  className="grid size-8 place-items-center rounded-full border border-warn/40 bg-warn/15 text-warn transition hover:bg-warn/25 disabled:opacity-35">
                  <Zap size={14} />
                </button>
              )}
              <button type="button" onClick={() => void handleSend(undefined, busy ? 'follow_up' : undefined)} disabled={!draft.trim() || sending || !activeProject}
                title={busy ? '排队：等本轮完成后继续发送' : '发送'}
                className="grid size-8 place-items-center rounded-full bg-accent text-on-accent transition hover:opacity-90 disabled:opacity-35">
                {busy ? <Clock size={14} /> : <Play size={14} fill="currentColor" />}
              </button>
            </div>
          )}
        </div>
      </div>

      {newProjModal && (
        <NewProjectModal
          projectsRoot={status?.projects_root || ''}
          onClose={() => setNewProjModal(false)}
          onCreated={onProjectCreated}
        />
      )}

      {filesModal && activeProject && (
        <ProjectFilesModal project={activeProject} onClose={() => setFilesModal(false)} />
      )}
    </div>
  );
}

function AgyMessageItem({ m }: { m: AgyChatMessage }) {
  if (m.role === 'user') {
    return (
      <div className="rise flex justify-end">
        <div className="max-w-[85%] whitespace-pre-wrap break-words rounded-[20px] rounded-br-md bg-bubble px-4 py-2.5 text-[15px] leading-relaxed text-fg">
          {m.text}
        </div>
      </div>
    );
  }
  return (
    <div className="rise flex flex-col gap-2">
      {m.thinking && <ThinkingBlock text={m.thinking} ms={m.thinking_ms} />}
      {m.tools && m.tools.length > 0 && <ToolCallList tools={m.tools} />}
      {m.text && <AssistantBubbles text={m.text} />}
      {m.error && (
        <div className="rounded-xl border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {m.error}
        </div>
      )}
    </div>
  );
}

function ToolCallList({ tools }: { tools: AgyToolCall[] }) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap gap-1.5">
        {tools.map((t) => {
          const running = t.status === 'in_progress' || t.status === 'pending';
          const failed = t.status === 'failed' || t.status === 'error';
          const hasExtra = !!(t.detail || t.output);
          const isOpen = !!expanded[t.id];
          return (
            <button key={t.id} type="button"
              onClick={() => hasExtra && setExpanded((prev) => ({ ...prev, [t.id]: !prev[t.id] }))}
              className={`inline-flex max-w-full items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11.5px] transition ${
                failed ? 'border-danger/40 bg-danger/10 text-danger'
                  : 'border-line bg-s2 text-fg3' + (hasExtra ? ' cursor-pointer hover:border-line2 hover:text-fg' : ' cursor-default')
              }`}>
              {running ? <Loader2 size={11} className="shrink-0 animate-spin text-accent" />
                : failed ? <X size={11} className="shrink-0 text-danger" />
                  : <Check size={11} className="shrink-0 text-accent" />}
              <Wrench size={11} className="shrink-0 text-fg4" />
              <span className="truncate font-mono">{t.title || t.name}</span>
              {hasExtra && <ChevronDown size={11} className={`shrink-0 transition-transform ${isOpen ? 'rotate-180' : ''}`} />}
            </button>
          );
        })}
      </div>
      {tools.filter((t) => expanded[t.id] && (t.detail || t.output)).map((t) => (
        <div key={t.id + '-detail'} className="overflow-hidden rounded-xl border border-line bg-s2/60 p-2.5 font-mono text-[11.5px] text-fg2">
          {t.detail && <div className="truncate font-semibold text-fg">{t.detail}</div>}
          {t.output && (
            <pre className="mt-1.5 max-h-48 overflow-auto whitespace-pre-wrap break-all text-[11px] leading-relaxed text-fg3">{t.output}</pre>
          )}
        </div>
      ))}
    </div>
  );
}

function AgyPermCard({ perm, onDecide }: { perm: AgyPermission; onDecide: (optionId: string) => void }) {
  return (
    <div className="rise rounded-2xl border border-warn/40 bg-s1 p-4 shadow-xs">
      <div className="flex items-start gap-2.5">
        <ShieldAlert size={18} className="mt-0.5 shrink-0 text-warn" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium text-fg">请求操作确认：{perm.title}</div>
          {perm.detail && (
            <pre className="mt-1.5 overflow-x-auto rounded-lg bg-s2 p-2 font-mono text-xs text-fg2">{perm.detail}</pre>
          )}
          <div className="mt-3 flex flex-wrap gap-2">
            {perm.options.map((opt) => {
              const reject = opt.optionId.includes('reject') || opt.kind?.includes('reject');
              return (
                <button key={opt.optionId} type="button" onClick={() => onDecide(opt.optionId)}
                  className={`rounded-full px-3.5 py-1.5 text-xs font-medium transition ${
                    reject ? 'bg-s2 text-danger hover:bg-s3' : 'bg-accent text-on-accent hover:opacity-90'
                  }`}>
                  {opt.name}
                </button>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}

function NewProjectModal({ projectsRoot, onClose, onCreated }: {
  projectsRoot: string; onClose: () => void; onCreated: (p: AgyProject) => void;
}) {
  const [tab, setTab] = useState<'managed' | 'existing'>('managed');
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setErr('');
    setBusy(true);
    try {
      const p = await agyAPI.createProject(name, tab === 'existing' ? path : '');
      onCreated(p);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/40 p-4" onClick={onClose}>
      <div className="w-full max-w-md rounded-2xl border border-line bg-canvas p-5 shadow-xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="text-base font-semibold text-fg">添加项目文件夹</h3>
          <button type="button" onClick={onClose} className="rounded-lg p-1 text-fg4 hover:bg-s2"><X size={16} /></button>
        </div>

        <div className="mt-3 grid grid-cols-2 gap-1 rounded-xl bg-s2 p-1 text-xs">
          <button type="button" onClick={() => setTab('managed')}
            className={`rounded-lg py-1.5 font-medium ${tab === 'managed' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3'}`}>
            新建空项目目录
          </button>
          <button type="button" onClick={() => setTab('existing')}
            className={`rounded-lg py-1.5 font-medium ${tab === 'existing' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3'}`}>
            关联服务器已有目录
          </button>
        </div>

        <div className="mt-4 space-y-3">
          {tab === 'managed' ? (
            <div>
              <label className="block text-xs text-fg3">文件夹名称</label>
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 my-webapp"
                className="mt-1 w-full rounded-xl border border-line2 bg-s1 px-3 py-2 text-sm text-fg focus:outline-none" />
              {projectsRoot && <p className="mt-1 text-[11px] text-fg4">将创建在：{projectsRoot}/{name || '<名称>'}</p>}
            </div>
          ) : (
            <>
              <div>
                <label className="block text-xs text-fg3">服务器上的目录路径</label>
                <input value={path} onChange={(e) => setPath(e.target.value)} placeholder="例如 ~/my-repo 或 /srv/app"
                  className="mt-1 w-full rounded-xl border border-line2 bg-s1 px-3 py-2 font-mono text-sm text-fg focus:outline-none" />
              </div>
              <div>
                <label className="block text-xs text-fg3">显示名称（可选）</label>
                <input value={name} onChange={(e) => setName(e.target.value)} placeholder="留空则默认取目录名"
                  className="mt-1 w-full rounded-xl border border-line2 bg-s1 px-3 py-2 text-sm text-fg focus:outline-none" />
              </div>
            </>
          )}
          {err && <p className="text-xs text-danger">{err}</p>}
        </div>

        <div className="mt-5 flex justify-end gap-2">
          <button type="button" onClick={onClose} className="rounded-xl px-3.5 py-1.5 text-sm text-fg3 hover:bg-s2">取消</button>
          <button type="button" onClick={submit}
            disabled={busy || (tab === 'managed' ? !name.trim() : !path.trim())}
            className="rounded-xl bg-accent px-4 py-1.5 text-sm font-medium text-on-accent disabled:opacity-40">
            {busy ? '处理中…' : '确定'}
          </button>
        </div>
      </div>
    </div>
  );
}

function ProjectFilesModal({ project, onClose }: { project: AgyProject; onClose: () => void }) {
  const [dir, setDir] = useState('');
  const [entries, setEntries] = useState<AgyFileEntry[]>([]);
  const [err, setErr] = useState('');

  useEffect(() => {
    setErr('');
    agyAPI.listFiles(project.id, dir).then(setEntries).catch((e: Error) => setErr(e.message));
  }, [project.id, dir]);

  const upDir = () => {
    if (!dir) return;
    const parts = dir.split('/').filter(Boolean);
    parts.pop();
    setDir(parts.join('/'));
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/40 p-4" onClick={onClose}>
      <div className="flex max-h-[75vh] w-full max-w-lg flex-col rounded-2xl border border-line bg-canvas p-5 shadow-xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-base font-semibold text-fg">{project.name} · 文件浏览</h3>
            <p className="font-mono text-xs text-fg4">{project.path}{dir ? `/${dir}` : ''}</p>
          </div>
          <button type="button" onClick={onClose} className="rounded-lg p-1 text-fg4 hover:bg-s2"><X size={16} /></button>
        </div>

        {dir && (
          <button type="button" onClick={upDir} className="mt-2 self-start text-xs text-accent hover:underline">
            ← 返回上级目录
          </button>
        )}

        <div className="mt-3 flex-1 divide-y divide-line overflow-y-auto">
          {err && <p className="py-4 text-center text-xs text-danger">{err}</p>}
          {!err && entries.length === 0 && <p className="py-6 text-center text-xs text-fg4">目录为空</p>}
          {entries.map((e) => {
            const rel = dir ? `${dir}/${e.name}` : e.name;
            return (
              <div key={e.name} className="flex items-center justify-between py-2 text-sm">
                {e.is_dir ? (
                  <button type="button" onClick={() => setDir(rel)} className="flex items-center gap-2 font-medium text-fg hover:text-accent">
                    <Folder size={15} className="text-accent" />{e.name}/
                  </button>
                ) : (
                  <a href={agyAPI.fileURL(project.id, rel)} target="_blank" rel="noreferrer" className="truncate text-fg2 hover:underline">
                    {e.name}
                  </a>
                )}
                {!e.is_dir && typeof e.size === 'number' && (
                  <span className="ml-2 shrink-0 font-mono text-xs text-fg4">{e.size} B</span>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
