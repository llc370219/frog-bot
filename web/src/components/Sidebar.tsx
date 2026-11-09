import { History, PanelLeft, Pencil, Plus, Settings as SettingsIcon } from 'lucide-react';
import { useState } from 'react';
import type { BotProfile } from '../api';
import { BotAvatar } from './BotAvatar';

export default function Sidebar({
  bots,
  activeBotId,
  schedulerActive,
  onSelectBot,
  onOpenScheduler,
  onOpenSettings,
  onCreateBot,
  onEditBot,
}: {
  bots: BotProfile[];
  activeBotId?: string;
  schedulerActive: boolean;
  onSelectBot: (id: string) => void;
  onOpenScheduler: () => void;
  onOpenSettings: () => void;
  onCreateBot: () => void;
  onEditBot: (bot: BotProfile) => void;
}) {
  const [collapsed, setCollapsed] = useState(() => {
    try { return localStorage.getItem('frogbot-sidebar-collapsed') === '1'; }
    catch { return false; }
  });
  const toggleCollapsed = () => {
    const next = !collapsed;
    setCollapsed(next);
    try { localStorage.setItem('frogbot-sidebar-collapsed', next ? '1' : '0'); }
    catch { /* The toggle still works when browser storage is unavailable. */ }
  };

  return (
    <aside
      className={`flex h-full shrink-0 flex-col border-r border-[#e3e1dc] dark:border-[#2a2926] bg-[#f5f3f2] dark:bg-[#1c1b1a] select-none text-fg transition-[width] duration-200 motion-reduce:transition-none ${collapsed ? 'w-16' : 'w-32 sm:w-64'}`}
      aria-label="侧栏"
    >
      <div className={`flex items-center gap-1 px-2 pt-2 pb-2 text-[13px] font-medium text-fg4 ${collapsed ? 'flex-col' : ''}`}>
        {!collapsed && <span className="min-w-0 flex-1 pl-1.5">Bots</span>}
        <button
          type="button"
          onClick={onCreateBot}
          disabled={!bots.length}
          className="grid size-8 shrink-0 place-items-center rounded-md text-fg4 hover:text-fg hover:bg-black/5 dark:hover:bg-white/10 transition disabled:opacity-40"
          title="创建 Bot"
          aria-label="创建 Bot"
        >
          <Plus size={15} />
        </button>
        <button
          type="button"
          onClick={toggleCollapsed}
          className="grid size-8 shrink-0 place-items-center rounded-md text-fg3 hover:text-fg hover:bg-black/5 dark:hover:bg-white/10 transition"
          title={collapsed ? '展开侧边栏' : '收起侧边栏'}
          aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
          aria-expanded={!collapsed}
        >
          <PanelLeft size={16} strokeWidth={1.7} aria-hidden="true" />
        </button>
      </div>

      {/* 仅保留 Bot 列表 */}
      <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-2 space-y-0.5 scrollbar-thin">
        {bots.map((b) => {
          const active = !schedulerActive && activeBotId === b.id;
          return (
            <div key={b.id} className={`group flex items-center gap-0.5 rounded-2xl transition ${collapsed ? '' : 'pr-2'} ${
              active ? 'bg-[#ebe9e9] text-fg shadow-2xs dark:bg-white/10' : 'text-fg2 hover:bg-black/4 hover:text-fg dark:hover:bg-white/5'
            }`}>
              <button
                type="button"
                onClick={() => onSelectBot(b.id)}
                title={b.name}
                aria-label={b.name}
                className={`flex min-w-0 flex-1 items-center gap-2 rounded-2xl px-2 py-2 text-left text-[14px] ${collapsed ? 'justify-center' : ''} ${active ? 'font-medium' : ''}`}
                aria-current={active ? 'true' : undefined}
              >
                <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={28} />
                {!collapsed && <span className="truncate flex-1 tracking-tight">{b.name}</span>}
              </button>
              {!collapsed && <button type="button" onClick={() => onEditBot(b)} title={`编辑 ${b.name}`} aria-label={`编辑 ${b.name}`}
                className={`grid size-6 shrink-0 place-items-center rounded-md hover:bg-black/5 hover:text-fg dark:hover:bg-white/10 ${active
                  ? 'text-fg3'
                  : 'text-fg4 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100'}`}>
                <Pencil size={13} />
              </button>}
            </div>
          );
        })}
      </div>

      <nav className="space-y-0.5 px-2 pb-3 pt-1" aria-label="应用导航">
        <button
          type="button"
          onClick={onOpenScheduler}
          disabled={!activeBotId}
          title="自动调度器"
          aria-label="自动调度器"
          aria-current={schedulerActive ? 'page' : undefined}
          className={`group flex w-full items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-left text-[12.5px] tracking-tight transition hover:bg-black/5 hover:text-fg dark:hover:bg-white/10 ${collapsed ? 'h-10 justify-center' : ''} ${schedulerActive ? 'bg-black/5 text-fg dark:bg-white/10' : 'text-fg3'}`}
        >
          <History size={15} strokeWidth={1.7} aria-hidden="true" className="shrink-0 text-fg4 transition-colors group-hover:text-fg" />
          {!collapsed && <span>自动调度器</span>}
        </button>
        <button
          type="button"
          onClick={onOpenSettings}
          disabled={!activeBotId}
          className={`group flex items-center gap-2.5 w-full px-2.5 py-1.5 rounded-xl text-left text-[12.5px] text-fg3 hover:bg-black/5 dark:hover:bg-white/10 hover:text-fg transition duration-150 ${collapsed ? 'h-10 justify-center' : ''}`}
          title="设置"
          aria-label="设置"
        >
          <SettingsIcon
            size={15}
            className="text-fg4 group-hover:text-fg transition-colors duration-150 shrink-0"
            strokeWidth={1.7}
          />
          {!collapsed && <span className="font-normal tracking-tight">设置</span>}
        </button>
      </nav>
    </aside>
  );
}
