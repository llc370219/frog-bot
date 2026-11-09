import { Bell, BellOff, Monitor, Moon, PanelLeft, Plus, Sun } from 'lucide-react';
import { useState } from 'react';

type Pref = 'system' | 'light' | 'dark';
const KEY = 'frogbot-theme';

function applyTheme(p: Pref) {
  const root = document.documentElement;
  if (p === 'system') {
    delete root.dataset.theme;
    localStorage.removeItem(KEY);
  } else {
    root.dataset.theme = p;
    localStorage.setItem(KEY, p);
  }
}

export default function Header({
  name,
  status,
  working,
  demo,
  connected,
  notify,
  onNotify,
  sidebarOpen,
  onToggleSidebar,
  onNewChat,
}: {
  name: string;
  status: string;
  working: boolean;
  demo: boolean;
  connected: boolean;
  notify: boolean;
  onNotify: () => void;
  sidebarOpen?: boolean;
  onToggleSidebar?: () => void;
  onNewChat?: () => void;
}) {
  const [pref, setPref] = useState<Pref>(() => (localStorage.getItem(KEY) as Pref) || 'system');
  const next: Record<Pref, Pref> = { system: 'light', light: 'dark', dark: 'system' };
  const Icon = pref === 'light' ? Sun : pref === 'dark' ? Moon : Monitor;

  return (
    <header className="sticky top-0 z-20 flex h-14 w-full shrink-0 items-center justify-between border-b border-line bg-canvas/85 px-4 backdrop-blur-md">
      {/* Left side: Sidebar toggle button, Active Bot name, + button, status */}
      <div className="flex items-center gap-2.5 min-w-0">
        {onToggleSidebar && (
          <button
            onClick={onToggleSidebar}
            className="grid size-9 shrink-0 place-items-center rounded-xl text-fg3 transition hover:bg-s2 hover:text-fg"
            title={sidebarOpen ? '收起侧边栏' : '展开侧边栏'}
            aria-label="切换侧边栏"
          >
            <PanelLeft size={18} />
          </button>
        )}
        <div className="flex items-center gap-2 min-w-0">
          <Avatar size={28} />
          <span className="truncate font-serif text-[16px] font-semibold tracking-tight text-fg">{name}</span>
          {demo && (
            <span className="rounded-full border border-warn/40 bg-warn-soft px-2 py-px text-[11px] font-medium text-warn shrink-0">
              演示 · 假模型
            </span>
          )}
          {onNewChat && (
            <button
              onClick={onNewChat}
              className="grid size-6 shrink-0 place-items-center rounded-md text-fg4 hover:bg-s2 hover:text-fg transition ml-0.5"
              title="新建对话"
              aria-label="新建对话"
            >
              <Plus size={15} />
            </button>
          )}
        </div>
        <div className="hidden md:flex items-center gap-1.5 text-xs text-fg3 ml-3 border-l border-line pl-3 shrink-0">
          <span className={`inline-block size-1.5 rounded-full ${!connected ? 'bg-danger' : working ? 'bg-accent working-dot' : 'bg-ok'}`} />
          <span className="truncate max-w-[220px]">{connected ? status : '连接中断，正在重新连接…'}</span>
        </div>
      </div>

      {/* Right side: 顶部工具放在右上角，黑白切换，通知图标放在右侧 */}
      <div className="flex items-center gap-1 shrink-0">
        <button
          onClick={onNotify}
          className="grid size-9 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
          title={notify ? '页面不在前台时会弹系统通知（点一下关闭）' : '开启系统通知：页面不在前台时，任务完成或需要确认会提醒你'}
          aria-label={notify ? '关闭系统通知' : '开启系统通知'}
          aria-pressed={notify}
        >
          {notify ? <Bell size={17} /> : <BellOff size={17} />}
        </button>
        <button
          onClick={() => {
            const p = next[pref];
            setPref(p);
            applyTheme(p);
          }}
          className="grid size-9 place-items-center rounded-full text-fg3 transition hover:bg-s2 hover:text-fg"
          title={{ system: '跟随系统', light: '浅色', dark: '深色' }[pref]}
          aria-label="切换主题"
        >
          <Icon size={17} />
        </button>
      </div>
    </header>
  );
}

export function Avatar({ size = 28 }: { size?: number }) {
  return (
    <span
      className="grid shrink-0 place-items-center overflow-hidden rounded-full bg-white ring-1 ring-line2"
      style={{ width: size, height: size }}
    >
      <img src="/frog.png" alt="" className="size-[88%] object-contain" />
    </span>
  );
}
