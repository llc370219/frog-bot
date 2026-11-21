import {
  AlertTriangle, Check, Folder, FolderOpen, FolderPlus, Loader2, MessageSquarePlus,
  PanelLeftOpen, Play, ShieldAlert, Square, SquareTerminal, Trash2, Wrench, X,
} from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import {
  agyAPI, type AgyChatMessage, type AgyFileEntry, type AgyModeOption, type AgyModelOption,
  type AgyPermission, type AgyProject, type AgySessionSummary, type AgySnapshot, type AgyStatus, type AgyToolCall,
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
  streaming: string;
  thinking: string;
  thinkStart: number;
  thinkEnd: number;
  liveTools: AgyToolCall[];
  usedTokens: number;
  maxTokens: number;
  modes: AgyModeOption[];
  models: AgyModelOption[];
};

const emptyLive = (): LiveState => ({
  ready: false, connected: false, session: null, messages: [], permissions: [],
  streaming: '', thinking: '', thinkStart: 0, thinkEnd: 0, liveTools: [],
  usedTokens: 0, maxTokens: 0, modes: [], models: [],
});

export default function AgyWorkspace() {
  const [status, setStatus] = useState<AgyStatus | null>(null);
  const [projects, setProjects] = useState<AgyProject[]>([]);
  const [projectId, setProjectId] = useState(() => localStorage.getItem(ACTIVE_PROJ_KEY) || '');
  const [sessions, setSessions] = useState<AgySessionSummary[]>([]);
  const [sessionId, setSessionId] = useState(() => localStorage.getItem(ACTIVE_SESS_KEY) || '');
  const [loadingSessions, setLoadingSessions] = useState(false);
  const [error, setError] = useState('');
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [newProjModal, setNewProjModal] = useState(false);
  const [filesModal, setFilesModal] = useState(false);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);

  const liveRef = useRef<LiveState>(emptyLive());
  const [, bump] = useState(0);
  const rerender = useCallback(() => bump((n) => n + 1), []);

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

  useEffect(() => { void refreshProjects(); }, [refreshProjects]);

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
    if (activeProject?.id) {
      localStorage.setItem(ACTIVE_PROJ_KEY, activeProject.id);
      void refreshSessions(activeProject.id, true);
    }
  }, [activeProject?.id, refreshSessions]);

  // 连接当前对话的 SSE 事件流
  useEffect(() => {
    liveRef.current = emptyLive();
    rerender();
    if (!sessionId || !activeProject?.id) return;
    localStorage.setItem(ACTIVE_SESS_KEY, sessionId);

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
            s.streaming = snap.streaming || '';
            s.thinking = snap.thinking || '';
            s.thinkStart = snap.think_start || 0;
            s.thinkEnd = snap.think_end || 0;
            s.liveTools = snap.live_tools || [];
            s.usedTokens = snap.used_tokens || 0;
            s.maxTokens = snap.max_tokens || 0;
            if (snap.modes?.length) s.modes = snap.modes;
            if (snap.models?.length) s.models = snap.models;
            break;
          }
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
              setSessions((prev) => prev.map((x) => x.id === s.session?.id ? { ...x, busy: !!d.busy, title: s.session!.title } : x));
            }
            break;
          case 'session.meta':
            if (d.session) {
              s.session = d.session;
              setSessions((prev) => prev.map((x) => x.id === d.session.id ? d.session : x));
            }
            if (d.used_tokens) s.usedTokens = d.used_tokens;
            if (d.max_tokens) s.maxTokens = d.max_tokens;
            break;
          case 'turn.done':
            if (s.session) s.session = { ...s.session, busy: false };
            s.streaming = '';
            s.thinking = '';
            s.thinkStart = 0;
            s.thinkEnd = 0;
            s.liveTools = [];
            setSessions((prev) => prev.map((x) => x.id === sessionId ? { ...x, busy: false } : x));
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
  }, [sessionId, activeProject?.id, rerender]);

  const live = liveRef.current;
  const modes = live.modes.length ? live.modes : (status?.modes || []);
  const models = live.models.length ? live.models : (status?.models || []);
  const curMode = live.session?.mode || status?.default_mode || 'auto';
  const curModel = live.session?.model || status?.default_model || '';
  const busy = !!live.session?.busy;

  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  const handleNewSession = async () => {
    if (!activeProject) return;
    setError('');
    try {
      const snap = await agyAPI.createSession(activeProject.id, curMode, curModel);
      setSessions((prev) => [snap.session, ...prev.filter((s) => s.id !== snap.session.id)]);
      setSessionId(snap.session.id);
      setDrawerOpen(false);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const handleDeleteSession = async (id: string) => {
    try {
      await agyAPI.deleteSession(id);
      const next = sessions.filter((s) => s.id !== id);
      setSessions(next);
      if (sessionId === id) setSessionId(next[0]?.id || '');
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const handleSend = async (textOverride?: string) => {
    const text = (textOverride ?? draft).trim();
    if (!text || !activeProject || sending) return;
    setError('');
    setSending(true);
    try {
      let targetSession = sessionId;
      if (!targetSession) {
        const snap = await agyAPI.createSession(activeProject.id, curMode, curModel);
        setSessions((prev) => [snap.session, ...prev]);
        targetSession = snap.session.id;
        setSessionId(targetSession);
      }
      if (textOverride === undefined) setDraft('');
      stick.current = true;
      await agyAPI.send(targetSession, activeProject.id, text);
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

  const sidebarContent = (
    <div className="flex h-full flex-col bg-s1">
      {/* 项目文件夹管理 */}
      <div className="border-b border-line p-3">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-[11.5px] font-medium text-fg4">项目文件夹</span>
          <div className="flex items-center gap-1">
            {activeProject && (
              <button type="button" onClick={() => setFilesModal(true)} title="浏览项目文件"
                className="rounded-md p-1 text-fg3 transition hover:bg-s2 hover:text-fg">
                <FolderOpen size={14} />
              </button>
            )}
            <button type="button" onClick={() => setNewProjModal(true)} title="新建或关联项目文件夹"
              className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-accent transition hover:bg-s2">
              <FolderPlus size={13} />新建
            </button>
          </div>
        </div>
        <div className="flex items-center gap-1">
          <select value={activeProject?.id || ''}
            onChange={(e) => { setProjectId(e.target.value); setSessionId(''); }}
            className="min-w-0 flex-1 truncate rounded-xl border border-line bg-canvas px-2.5 py-1.5 text-[13.5px] font-medium text-fg focus:outline-none">
            {projects.map((p) => (
              <option key={p.id} value={p.id}>{p.name}{p.managed ? '' : ' (外部)'}</option>
            ))}
          </select>
          {activeProject && projects.length > 1 && (
            <button type="button" title="移除该项目"
              onClick={async () => {
                const delDisk = activeProject.managed && confirm(`是否同时删除服务器磁盘上的文件夹「${activeProject.path}」？\n点「取消」则仅从列表移除。`);
                if (!activeProject.managed && !confirm(`从列表移除「${activeProject.name}」？（不会删除磁盘文件）`)) return;
                try {
                  await agyAPI.deleteProject(activeProject.id, delDisk);
                  await refreshProjects();
                } catch (e) { setError((e as Error).message); }
              }}
              className="rounded-lg p-1.5 text-fg4 transition hover:bg-s2 hover:text-danger">
              <Trash2 size={14} />
            </button>
          )}
        </div>
        {activeProject && (
          <p className="mt-1.5 truncate font-mono text-[11px] text-fg4" title={activeProject.path}>{activeProject.path}</p>
        )}
      </div>

      {/* 对话列表 */}
      <div className="flex items-center justify-between px-3 pb-1.5 pt-3">
        <span className="text-[11.5px] font-medium text-fg4">对话会话</span>
        <button type="button" onClick={handleNewSession} disabled={!activeProject || status?.installed === false}
          className="inline-flex items-center gap-1 rounded-lg bg-accent px-2.5 py-1 text-xs font-medium text-on-accent transition hover:opacity-90 disabled:opacity-40">
          <MessageSquarePlus size={13} />新建对话
        </button>
      </div>

      <div className="min-h-0 flex-1 space-y-1 overflow-y-auto px-2 pb-3 scrollbar-thin">
        {loadingSessions && sessions.length === 0 && (
          <p className="px-2 py-4 text-center text-xs text-fg4">正在读取对话…</p>
        )}
        {!loadingSessions && sessions.length === 0 && (
          <p className="px-3 py-6 text-center text-xs leading-relaxed text-fg4">
            该项目下还没有对话。<br />点击右上角「新建对话」或直接在下方发消息即可开始。
          </p>
        )}
        {sessions.map((s) => {
          const selected = s.id === sessionId;
          return (
            <div key={s.id} className={`group flex items-center gap-1 rounded-xl px-2.5 py-2 transition ${
              selected ? 'bg-sidebar-selected text-fg shadow-2xs' : 'text-fg2 hover:bg-sidebar-hover'
            }`}>
              <button type="button" onClick={() => { setSessionId(s.id); setDrawerOpen(false); }}
                className="min-w-0 flex-1 text-left">
                <div className="flex items-center gap-1.5">
                  {s.pending_permissions > 0 ? <span className="size-2 shrink-0 rounded-full bg-warn" title="等待确认" />
                    : s.busy ? <span className="size-2 shrink-0 animate-pulse rounded-full bg-accent" title="运行中" /> : null}
                  <span className={`truncate text-[13.5px] ${selected ? 'font-medium' : ''}`}>{s.title || '新对话'}</span>
                </div>
                <div className="mt-0.5 flex items-center gap-2 text-[11px] text-fg4">
                  <span>{s.mode}</span>
                  <span>·</span>
                  <span>{new Date(s.updated_at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</span>
                </div>
              </button>
              <button type="button" onClick={() => handleDeleteSession(s.id)} title="删除对话"
                className="rounded-md p-1 text-fg4 opacity-0 transition hover:bg-s2 hover:text-danger group-hover:opacity-100">
                <Trash2 size={13} />
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );

  return (
    <div className="flex h-full flex-1 min-w-0 overflow-hidden bg-canvas">
      {/* 桌面端二级项目/会话栏 */}
      <div className="hidden w-64 shrink-0 border-r border-line md:block">
        {sidebarContent}
      </div>

      {/* 移动端抽屉 */}
      {drawerOpen && (
        <div className="fixed inset-0 z-40 flex md:hidden">
          <div className="w-72 max-w-[82vw] border-r border-line shadow-xl">{sidebarContent}</div>
          <div className="flex-1 bg-black/30" onClick={() => setDrawerOpen(false)} />
        </div>
      )}

      {/* 右侧主对话面板 */}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* 顶栏：模式与模型直控 */}
        <header className="flex flex-wrap items-center justify-between gap-2 border-b border-line bg-s1/60 px-3 py-2 pr-24">
          <div className="flex min-w-0 items-center gap-2">
            <button type="button" onClick={() => setDrawerOpen(true)} className="rounded-lg p-1.5 text-fg3 hover:bg-s2 md:hidden" aria-label="展开会话列表">
              <PanelLeftOpen size={17} />
            </button>
            <SquareTerminal size={17} className="shrink-0 text-accent" />
            <div className="min-w-0">
              <div className="truncate text-[14px] font-semibold text-fg">
                {live.session?.title || activeProject?.name || 'Antigravity CLI'}
              </div>
              {activeProject && (
                <div className="truncate font-mono text-[11px] text-fg4">{activeProject.path}</div>
              )}
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {modes.length > 0 && (
              <select aria-label="agy 权限模式" value={curMode} onChange={(e) => handleChangeMode(e.target.value)}
                disabled={!sessionId}
                className="rounded-lg border border-line bg-canvas px-2 py-1 text-xs text-fg2 focus:outline-none disabled:opacity-50">
                {modes.map((m) => <option key={m.id} value={m.id}>模式：{m.name}</option>)}
              </select>
            )}
            {models.length > 0 && (
              <select aria-label="agy 模型" value={curModel} onChange={(e) => handleChangeModel(e.target.value)}
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
              {status.error || '服务器上未检测到 agy CLI。请先在服务器安装并运行 `agy acp --login` 登录。'}
            </span>
          </div>
        )}

        {/* 消息流 */}
        <div ref={scroller}
          onScroll={(e) => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; }}
          className="flex-1 overflow-y-auto">
          <main className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 pb-8 pt-6">
            {(!sessionId || (live.ready && live.messages.length === 0 && !busy)) && (
              <div className="flex flex-col items-center px-2 pt-[10vh] text-center">
                <div className="grid size-14 place-items-center rounded-2xl bg-s2 text-accent">
                  <SquareTerminal size={28} />
                </div>
                <h2 className="mt-4 font-serif text-2xl font-semibold">直连 Antigravity CLI</h2>
                <p className="mt-2 max-w-md text-sm leading-relaxed text-fg3">
                  绕过聊天 Bot，直接在服务器项目目录「{activeProject?.name || '默认项目'}」中驱动 <code>agy</code> 读写代码与执行命令。权限完全由上方所选的 agy 模式控制。
                </p>
                <div className="mt-6 flex flex-wrap justify-center gap-2">
                  {['看看当前项目结构并总结主要模块', '运行测试并排查报错', '帮我规划一个新功能的改动方案'].map((q) => (
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
                    <span>agy 正在执行…</span>
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
          <div className="mx-auto flex max-w-3xl items-end gap-2 rounded-2xl border border-line2 bg-s1 px-3 py-2 shadow-2xs">
            <textarea
              rows={1}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  void handleSend();
                } else if (e.key === 'Escape' && busy && sessionId) {
                  e.preventDefault();
                  void agyAPI.stop(sessionId);
                }
              }}
              placeholder={activeProject ? `在 ${activeProject.name} 中直接给 agy 发指令…（Enter 发送，Shift+Enter 换行）` : '请先新建或选择一个项目文件夹'}
              className="max-h-40 min-h-[26px] flex-1 resize-none bg-transparent py-1 text-[14.5px] leading-relaxed text-fg placeholder:text-fg4 focus:outline-none"
            />
            {busy && !draft.trim() ? (
              <button type="button" onClick={() => sessionId && void agyAPI.stop(sessionId)} title="停止当前执行 (Esc)"
                className="grid size-8 shrink-0 place-items-center rounded-full bg-fg text-canvas transition hover:opacity-85">
                <Square size={13} fill="currentColor" />
              </button>
            ) : (
              <button type="button" onClick={() => void handleSend()} disabled={!draft.trim() || sending || !activeProject}
                title="发送"
                className="grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition hover:opacity-90 disabled:opacity-35">
                <Play size={14} fill="currentColor" />
              </button>
            )}
          </div>
        </div>
      </div>

      {newProjModal && (
        <NewProjectModal
          projectsRoot={status?.projects_root || ''}
          onClose={() => setNewProjModal(false)}
          onCreated={(p) => {
            setProjects((prev) => prev.some((x) => x.id === p.id) ? prev : [...prev, p]);
            setProjectId(p.id);
            setSessionId('');
            setNewProjModal(false);
          }}
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
  return (
    <div className="flex flex-wrap gap-1.5">
      {tools.map((t) => {
        const running = t.status === 'in_progress' || t.status === 'pending';
        return (
          <span key={t.id}
            className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-line bg-s2 px-2.5 py-1 text-[11.5px] text-fg3">
            {running ? <Loader2 size={11} className="shrink-0 animate-spin text-accent" />
              : <Check size={11} className="shrink-0 text-accent" />}
            <Wrench size={11} className="shrink-0 text-fg4" />
            <span className="truncate font-mono">{t.title || t.name}</span>
          </span>
        );
      })}
    </div>
  );
}

function AgyPermCard({ perm, onDecide }: { perm: AgyPermission; onDecide: (optionId: string) => void }) {
  return (
    <div className="rise rounded-2xl border border-warn/40 bg-s1 p-4 shadow-xs">
      <div className="flex items-start gap-2.5">
        <ShieldAlert size={18} className="mt-0.5 shrink-0 text-warn" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium text-fg">agy 请求操作确认：{perm.title}</div>
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
