import { ChevronDown, ChevronRight, History, PanelLeft, Pencil, Plus, Settings as SettingsIcon } from 'lucide-react';
import { useState } from 'react';
import { botKey, type BotProfile, type DeviceGroup } from '../api';
import { BotAvatar } from './BotAvatar';

const foldedKey = 'frogbot-folded-devices';

export default function Sidebar({
  devices,
  activeBotId,
  schedulerActive,
  onSelectBot,
  onOpenScheduler,
  onOpenSettings,
  onCreateBot,
  onEditBot,
}: {
  devices: DeviceGroup[];
  activeBotId?: string;
  schedulerActive: boolean;
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

  return (
    <aside
      className={`app-sidebar flex h-full shrink-0 flex-col border-r border-line bg-sidebar select-none text-fg transition-[width] duration-200 motion-reduce:transition-none ${collapsed ? 'w-16' : 'w-32 sm:w-64'}`}
      aria-label="侧栏"
    >
      <div className="min-h-0 flex-1 space-y-0.5 overflow-y-auto overflow-x-hidden px-2 pt-2 scrollbar-thin">
        {devices.map((device) => {
          const open = !folded.has(device.id);
          const listID = `device-bots-${device.id}`;
          const canCreate = device.online && (device.id !== 'local' || device.bots.length > 0);
          const Chevron = open ? ChevronDown : ChevronRight;
          return <section key={device.id} aria-label={device.name} className="mb-2">
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
            {!collapsed && device.id !== 'local' && !device.bots.length && <p className="px-2 py-1 text-xs text-fg4">{device.online ? '没有 Bot' : '暂时无法连接'}</p>}
            {device.bots.map((b) => {
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
              {!collapsed && device.id === 'local' && <button type="button" onClick={() => onEditBot(b)} title={`编辑 ${b.name}`} aria-label={`编辑 ${b.name}`}
                className={`grid size-6 shrink-0 place-items-center rounded-md hover:bg-sidebar-hover hover:text-fg ${active
                  ? 'text-fg3'
                  : 'text-fg4 sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100'}`}>
                <Pencil size={13} />
              </button>}
            </div>
          );
            })}
          </div>
        </section>})}
      </div>

      <nav className="space-y-0.5 px-2 pb-3 pt-1" aria-label="应用导航">
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
        <button
          type="button"
          onClick={onOpenSettings}
          disabled={!activeBotId}
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
