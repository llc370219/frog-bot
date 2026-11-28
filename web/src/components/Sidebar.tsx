import {
  Bot, ChevronDown, ChevronRight, FolderOpen, FolderPlus, History, MessageSquarePlus,
  PanelLeft, Pencil, Plus, Settings as SettingsIcon, SquareTerminal, Trash2,
} from 'lucide-react';
import { useState } from 'react';
import { botKey, type BotProfile, type DeviceGroup } from '../api';
import { ALL_AGENTS_ID, ALL_PROJECTS_ID, type CodingController } from './AgyWorkspace';
import { BotAvatar } from './BotAvatar';

const foldedKey = 'frogbot-folded-devices';

export type UIMode = 'bot' | 'coding';

export default function Sidebar({
  mode,
  onChangeMode,
  devices,
  activeBotId,
  schedulerActive,
  coding,
  onSelectBot,
  onOpenScheduler,
  onOpenSettings,
  onCreateBot,
  onEditBot,
}: {
  mode: UIMode;
  onChangeMode: (m: UIMode) => void;
  devices: DeviceGroup[];
  activeBotId?: string;
  schedulerActive: boolean;
  coding: CodingController;
  onSelectBot: (id: string) => void;
  onOpenScheduler: () => void;
  onOpenSettings: () => void;
  onCreateBot: (device: DeviceGroup) => void;
  onEditBot: (bot: BotProfile) => void;
}) {
  const [collapsed, setCollapsed] = useState(() => {
    try { return localStorage.getItem('frogbot-sidebar-collapsed') === '1'; }
    catch { return false; }
  });
  const [folded, setFolded] = useState(() => {
    try {
      const ids: unknown = JSON.parse(localStorage.getItem(foldedKey) || '[]');
      return new Set(Array.isArray(ids) ? ids.filter((id): id is string => typeof id === 'string') : []);
    } catch { return new Set<string>(); }
  });

  const fold = (id: string, hide: boolean) => {
    const next = new Set(folded);
    if (hide) next.add(id); else next.delete(id);
    setFolded(next);
    try { localStorage.setItem(foldedKey, JSON.stringify([...next])); }
    catch { /* Groups still work without storage. */ }
  };

  const toggleCollapsed = () => {
    const next = !collapsed;
    setCollapsed(next);
    try { localStorage.setItem('frogbot-sidebar-collapsed', next ? '1' : '0'); }
    catch { /* The toggle still works when browser storage is unavailable. */ }
  };

  const singleLocalDevice = devices.length === 1 && devices[0].id === 'local';

  const renderBotRow = (device: DeviceGroup, b: BotProfile) => {
    const id = botKey(device.id, b.id);
    const active = !schedulerActive && activeBotId === id;
    return (
      <div key={id} className={`group flex items-center gap-0.5 rounded-2xl transition ${collapsed ? '' : 'pr-2'} ${
        active ? 'bg-sidebar-selected text-fg shadow-2xs' : 'text-fg2 hover:bg-sidebar-hover hover:text-fg'
      }`}>
        <button
          type="button"
          onClick={() => onSelectBot(id)}
          title={device.id === 'local' ? b.name : `${b.name} · ${id}`}
          aria-label={`${device.name}：${b.name}`}
          className={`flex min-w-0 flex-1 items-center gap-2 rounded-2xl px-2 py-2 text-left text-[14px] ${collapsed ? 'justify-center' : ''} ${active ? 'font-medium' : ''}`}
          aria-current={active ? 'true' : undefined}
        >
          <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={28} />
          {!collapsed && <span className="truncate flex-1 tracking-tight">{b.name}</span>}
        </button>
        {!collapsed && device.id === 'local' && (
          <button type="button" onClick={() => onEditBot(b)} title={`编辑 ${b.name}`} aria-label={`编辑 ${b.name}`}
            className={`grid size-6 shrink-0 place-items-center rounded-md hover:bg-sidebar-hover hover:text-fg ${active
              ? 'text-fg3'
              : 'text-fg4 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100'}`}>
            <Pencil size={13} />
          </button>
        )}
      </div>
    );
  };

  return (
    <aside
      className={`app-sidebar flex h-full shrink-0 flex-col border-r border-line bg-sidebar select-none text-fg transition-[width] duration-200 motion-reduce:transition-none ${collapsed ? 'w-16' : 'w-32 sm:w-64'}`}
      aria-label="侧栏"
    >
      {/* 顶部 Bot ↔ Coding 模式切换 */}
      <div className="px-2 pb-1.5 pt-2.5">
        {collapsed ? (
          <button
            type="button"
            onClick={() => onChangeMode(mode === 'bot' ? 'coding' : 'bot')}
            title={`当前：${mode === 'bot' ? 'Bot 模式' : 'Coding 模式'}（点击切换）`}
            aria-label="切换 Bot / Coding 模式"
            className="mx-auto grid size-10 place-items-center rounded-xl bg-s2 text-accent transition hover:bg-sidebar-hover"
          >
            {mode === 'bot' ? <Bot size={17} /> : <SquareTerminal size={17} />}
          </button>
        ) : (
          <div className="grid grid-cols-2 gap-1 rounded-xl bg-s2 p-1 text-[12.5px]" role="tablist" aria-label="工作模式">
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'bot'}
              onClick={() => onChangeMode('bot')}
              className={`flex items-center justify-center gap-1.5 rounded-lg py-1.5 font-medium transition ${
                mode === 'bot' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3 hover:text-fg'
              }`}
            >
              <Bot size={13} />
              <span>Bot</span>
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'coding'}
              onClick={() => onChangeMode('coding')}
              className={`flex items-center justify-center gap-1.5 rounded-lg py-1.5 font-medium transition ${
                mode === 'coding' ? 'bg-canvas text-fg shadow-2xs' : 'text-fg3 hover:text-fg'
              }`}
            >
              <SquareTerminal size={13} />
              <span>Coding</span>
            </button>
          </div>
        )}
      </div>

      {/* 主列表区：仅显示当前模式对应的内容 */}
      {mode === 'bot' ? (
        <div className="min-h-0 flex-1 space-y-0.5 overflow-y-auto overflow-x-hidden px-2 pt-1 scrollbar-thin">
          {singleLocalDevice ? (
            <>
              {!collapsed && (
                <div className="flex items-center justify-between px-1 pb-1 pt-0.5">
                  <span className="text-[11.5px] font-medium text-fg4">Bot 列表</span>
                  <button
                    type="button"
                    disabled={!devices[0].online}
                    onClick={() => onCreateBot(devices[0])}
                    title="新建 Bot"
                    aria-label="新建 Bot"
                    className="grid size-6 place-items-center rounded-md text-fg4 transition hover:bg-sidebar-hover hover:text-fg disabled:opacity-40"
                  >
                    <Plus size={15} />
                  </button>
                </div>
              )}
              {devices[0].bots.map((b) => renderBotRow(devices[0], b))}
            </>
          ) : (
            devices.map((device) => {
              const open = !folded.has(device.id);
              const listID = `device-bots-${device.id}`;
              const canCreate = device.online && (device.id !== 'local' || device.bots.length > 0);
              const Chevron = open ? ChevronDown : ChevronRight;
              return (
                <section key={device.id} aria-label={device.name} className="mb-2">
                  <header className="flex min-w-0 items-center py-0.5">
                    <button type="button" aria-expanded={open} aria-controls={listID} onClick={() => fold(device.id, open)}
                      aria-label={`${open ? '折叠' : '展开'} ${device.name} 的 Bot`}
                      title={`${device.name}${device.online ? '' : '（离线）'}`}
                      className={`flex h-7 min-w-0 flex-1 items-center gap-1 rounded-md text-left text-[13px] font-medium text-fg3 transition hover:bg-sidebar-hover hover:text-fg ${collapsed ? 'justify-center' : 'px-1'}`}>
                      <Chevron size={14} className="shrink-0 text-fg4" aria-hidden="true" />
                      {!collapsed && <><span className="min-w-0 flex-1 truncate">{device.name}</span>{!device.online && <span className="shrink-0 text-[10px] text-fg4">离线</span>}</>}
                    </button>
                    <button type="button" disabled={!canCreate}
                      onClick={() => { fold(device.id, false); onCreateBot(device); }}
                      title={device.online ? `在 ${device.name} 新建 Bot` : `${device.name} 离线`}
                      aria-label={`在 ${device.name} 新建 Bot`}
                      className="grid size-6 shrink-0 place-items-center rounded-md text-fg4 transition hover:bg-sidebar-hover hover:text-fg disabled:opacity-40">
                      <Plus size={15} aria-hidden="true" />
                    </button>
                  </header>
                  <div id={listID} hidden={!open}>
                    {!collapsed && device.id !== 'local' && !device.bots.length && (
                      <p className="px-2 py-1 text-xs text-fg4">{device.online ? '没有 Bot' : '暂时无法连接'}</p>
                    )}
                    {device.bots.map((b) => renderBotRow(device, b))}
                  </div>
                </section>
              );
            })
          )}
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden px-2 pt-1">
          {collapsed ? (
            <div className="flex flex-1 flex-col items-center gap-1 pt-1">
              <button
                type="button"
                onClick={() => void coding.newSession()}
                disabled={!coding.defaultTargetProject || coding.status?.installed === false}
                title="新建 Coding 对话"
                aria-label="新建 Coding 对话"
                className="grid size-10 place-items-center rounded-xl text-accent transition hover:bg-sidebar-hover disabled:opacity-40"
              >
                <MessageSquarePlus size={17} />
              </button>
              <button
                type="button"
                onClick={coding.openNewProject}
                title="新建或关联项目文件夹"
                aria-label="新建或关联项目文件夹"
                className="grid size-10 place-items-center rounded-xl text-fg3 transition hover:bg-sidebar-hover hover:text-fg"
              >
                <FolderPlus size={17} />
              </button>
              {coding.activeProject && (
                <button
                  type="button"
                  onClick={coding.openFiles}
                  title="浏览项目文件"
                  aria-label="浏览项目文件"
                  className="grid size-10 place-items-center rounded-xl text-fg3 transition hover:bg-sidebar-hover hover:text-fg"
                >
                  <FolderOpen size={17} />
                </button>
              )}
            </div>
          ) : (
            <>
              {/* 项目文件夹与 CLI 筛选 */}
              <div className="border-b border-line pb-2.5 space-y-2">
                {(coding.status?.agents || []).filter((a) => a.installed).length > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {(coding.status?.agents || []).filter((a) => a.installed).length > 1 && (
                      <button
                        type="button"
                        onClick={() => coding.setAgentFilter(ALL_AGENTS_ID)}
                        className={`rounded-full px-2 py-0.5 text-[11px] transition ${
                          coding.agentFilter === ALL_AGENTS_ID
                            ? 'bg-accent/15 font-medium text-accent'
                            : 'bg-s2 text-fg3 hover:text-fg'
                        }`}
                      >
                        全部
                      </button>
                    )}
                    {(coding.status?.agents || []).filter((a) => a.installed).map((a) => {
                      const active = coding.agentFilter === a.id;
                      return (
                        <button
                          key={a.id}
                          type="button"
                          onClick={() => void coding.selectAgent(a.id)}
                          className={`rounded-full px-2 py-0.5 text-[11px] transition ${
                            active
                              ? 'bg-accent/15 font-medium text-accent'
                              : 'bg-s2 text-fg3 hover:text-fg'
                          }`}
                        >
                          {a.name}
                        </button>
                      );
                    })}
                  </div>
                )}
                <div>
                  <div className="mb-1 flex items-center justify-between px-0.5">
                    <span className="text-[11px] font-medium text-fg4">项目文件夹</span>
                    <div className="flex items-center gap-0.5">
                      {coding.activeProject && (
                        <button type="button" onClick={coding.openFiles} title="浏览项目文件"
                          className="rounded-md p-1 text-fg3 transition hover:bg-sidebar-hover hover:text-fg">
                          <FolderOpen size={13} />
                        </button>
                      )}
                      <button type="button" onClick={coding.openNewProject} title="新建或关联项目文件夹"
                        className="inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-[11.5px] text-accent transition hover:bg-sidebar-hover">
                        <FolderPlus size={12} />新建
                      </button>
                    </div>
                  </div>
                  <div className="flex items-center gap-1">
                    <select value={coding.selectedProjectId}
                      onChange={(e) => coding.selectProject(e.target.value)}
                      aria-label="当前项目"
                      className="min-w-0 flex-1 truncate rounded-xl border border-line bg-canvas px-2 py-1.5 text-[12.5px] font-medium text-fg focus:outline-none">
                      <option value={ALL_PROJECTS_ID}>全部目录（汇总历史）</option>
                      {coding.projects.map((p) => (
                        <option key={p.id} value={p.id}>{p.name}{p.managed ? '' : ' (外)'}</option>
                      ))}
                    </select>
                    {coding.activeProject && coding.projects.length > 1 && (
                      <button type="button" title="移除该项目"
                        onClick={() => coding.activeProject && void coding.deleteProject(coding.activeProject)}
                        className="rounded-lg p-1.5 text-fg4 transition hover:bg-sidebar-hover hover:text-danger">
                        <Trash2 size={13} />
                      </button>
                    )}
                  </div>
                </div>
              </div>

              {/* 对话列表 */}
              <div className="flex items-center justify-between px-0.5 pb-1 pt-2.5">
                <span className="text-[11px] font-medium text-fg4">对话列表</span>
                <button type="button" onClick={() => void coding.newSession()}
                  disabled={!coding.defaultTargetProject || coding.status?.installed === false}
                  className="inline-flex items-center gap-1 rounded-lg bg-accent px-2 py-1 text-[11.5px] font-medium text-on-accent transition hover:opacity-90 disabled:opacity-40">
                  <MessageSquarePlus size={12} />新建
                </button>
              </div>

              <div className="min-h-0 flex-1 space-y-0.5 overflow-y-auto pb-2 scrollbar-thin">
                {coding.loadingSessions && coding.sessions.length === 0 && (
                  <p className="px-2 py-4 text-center text-xs text-fg4">正在读取对话…</p>
                )}
                {!coding.loadingSessions && coding.filteredSessions.length === 0 && (
                  <div className="px-2 py-5 text-center text-xs leading-relaxed text-fg4 space-y-2">
                    <p>
                      {coding.sessions.length > 0 && coding.agentFilter !== ALL_AGENTS_ID
                        ? `当前 CLI 暂无对话（其他 CLI 共 ${coding.sessions.length} 条）`
                        : '还没有对话，点「新建」或右侧直接提问即可开始。'}
                    </p>
                    {coding.sessions.length > 0 && coding.agentFilter !== ALL_AGENTS_ID && (
                      <button
                        type="button"
                        onClick={() => coding.setAgentFilter(ALL_AGENTS_ID)}
                        className="rounded-lg bg-s2 px-2.5 py-1 text-[11.5px] text-fg2 hover:bg-sidebar-hover"
                      >
                        查看全部 CLI
                      </button>
                    )}
                  </div>
                )}
                {coding.filteredSessions.map((s) => {
                  const selected = s.id === coding.sessionId;
                  const dirBase = coding.isAllProjects && s.cwd
                    ? s.cwd.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || ''
                    : '';
                  return (
                    <div key={s.id} className={`group flex items-center gap-1 rounded-xl px-2 py-1.5 transition ${
                      selected ? 'bg-sidebar-selected text-fg shadow-2xs' : 'text-fg2 hover:bg-sidebar-hover'
                    }`}>
                      <button type="button" onClick={() => coding.selectSession(s.id)} className="min-w-0 flex-1 text-left">
                        <div className="flex items-center gap-1.5">
                          {s.pending_permissions > 0 ? <span className="size-2 shrink-0 rounded-full bg-warn" title="等待确认" />
                            : s.busy ? <span className="size-2 shrink-0 animate-pulse rounded-full bg-accent" title="运行中" /> : null}
                          <span className={`truncate text-[13px] ${selected ? 'font-medium' : ''}`}>{s.title || '新对话'}</span>
                        </div>
                        <div className="mt-0.5 flex items-center gap-1.5 text-[10.5px] text-fg4">
                          {dirBase && <><span className="truncate">{dirBase}</span><span>·</span></>}
                          <span className="truncate">{s.agent_name || s.mode}</span>
                          <span>·</span>
                          <span>{new Date(s.updated_at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</span>
                        </div>
                      </button>
                      <button type="button" onClick={() => void coding.deleteSession(s.id)} title="删除对话"
                        className="rounded-md p-1 text-fg4 opacity-0 transition hover:bg-s2 hover:text-danger group-hover:opacity-100">
                        <Trash2 size={12} />
                      </button>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
      )}

      <nav className="space-y-0.5 border-t border-line/60 px-2 pb-3 pt-1.5" aria-label="应用导航">
        {mode === 'bot' && (
          <button
            type="button"
            onClick={onOpenScheduler}
            disabled={!activeBotId}
            title="自动调度器"
            aria-label="自动调度器"
            aria-current={schedulerActive ? 'page' : undefined}
            className={`group flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] tracking-tight transition hover:text-fg ${collapsed ? 'h-10 justify-center' : ''} ${schedulerActive ? 'bg-sidebar-selected text-fg' : 'text-fg3 hover:bg-sidebar-hover'}`}
          >
            <History size={15} strokeWidth={1.7} aria-hidden="true" className="shrink-0 text-fg4 transition-colors group-hover:text-fg" />
            {!collapsed && <span>自动调度器</span>}
          </button>
        )}
        <button
          type="button"
          onClick={onOpenSettings}
          disabled={!activeBotId && mode === 'bot'}
          className={`group flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] text-fg3 transition duration-150 hover:bg-sidebar-hover hover:text-fg disabled:opacity-40 ${collapsed ? 'h-10 justify-center' : ''}`}
          title="设置"
          aria-label="设置"
        >
          <SettingsIcon
            size={15}
            className="shrink-0 text-fg4 transition-colors duration-150 group-hover:text-fg"
            strokeWidth={1.7}
          />
          {!collapsed && <span className="font-normal tracking-tight">设置</span>}
        </button>
        <button type="button" onClick={toggleCollapsed} aria-expanded={!collapsed}
          title={collapsed ? '展开侧边栏' : '收起侧边栏'} aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
          className={`flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] text-fg3 transition hover:bg-sidebar-hover hover:text-fg ${collapsed ? 'h-10 justify-center' : ''}`}>
          <PanelLeft size={15} strokeWidth={1.7} className="shrink-0 text-fg4" aria-hidden="true" />
          {!collapsed && <span>收起侧边栏</span>}
        </button>
      </nav>
    </aside>
  );
}
