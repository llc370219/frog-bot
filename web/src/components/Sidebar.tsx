import { Plus, Settings as SettingsIcon } from 'lucide-react';
import { BotAvatar } from './BotAvatar';

export type BotItem = {
  id: string;
  name: string;
  avatarSlot: number;
};

// 严格按照图一的 Bot 列表，图标使用 onefrog 的 bot 库 (bot-avatars webp 头像)
export const DEFAULT_BOTS: BotItem[] = [
  { id: 'grok-ref', name: 'Grok Bot项目借鉴', avatarSlot: 0 },
  { id: 'onefrog', name: 'Onefrog', avatarSlot: 1 },
  { id: 'relay', name: '中转站', avatarSlot: 2 },
  { id: 'frog-co-im', name: '一只青蛙frog.co.im管理', avatarSlot: 3 },
  { id: 'server-mgmt', name: '服务器管理', avatarSlot: 4 },
  { id: 'grok-bot', name: 'Grok Bot', avatarSlot: 5 },
];

export default function Sidebar({
  activeBotId,
  onSelectBot,
  onOpenSettings,
  onNewChat,
}: {
  activeBotId: string;
  onSelectBot: (id: string, name: string) => void;
  onOpenSettings: () => void;
  onNewChat?: () => void;
}) {
  return (
    <aside
      className="flex h-full w-64 shrink-0 flex-col border-r border-line bg-[#f5f3f2] dark:bg-[#1c1b1a] select-none text-fg"
      style={{ width: '256px' }}
      aria-label="Bots 状态栏"
    >
      {/* 顶部 Bots 标题与 + 号，严格按照图一样式 */}
      <div className="flex items-center justify-between px-3.5 pt-3 pb-2 text-[13.5px] font-medium text-fg4">
        <span>Bots</span>
        <button
          type="button"
          onClick={onNewChat}
          className="grid size-6 place-items-center rounded-md text-fg4 hover:text-fg hover:bg-black/5 dark:hover:bg-white/10 transition"
          title="新建 Bot 对话"
          aria-label="添加"
        >
          <Plus size={16} />
        </button>
      </div>

      {/* 只有 Bots 列表，无上方选项，无下方对话 */}
      <div className="flex-1 overflow-y-auto px-2 space-y-1 scrollbar-thin">
        {DEFAULT_BOTS.map((b) => {
          const active = activeBotId === b.id;
          return (
            <button
              key={b.id}
              onClick={() => onSelectBot(b.id, b.name)}
              className={`flex items-center gap-3 w-full px-2.5 py-2 rounded-2xl text-left text-[14px] transition ${
                active
                  ? 'bg-[#ebe9e9] dark:bg-white/10 font-medium text-fg shadow-2xs'
                  : 'text-fg2 hover:bg-black/4 dark:hover:bg-white/5 hover:text-fg'
              }`}
              aria-current={active ? 'true' : undefined}
            >
              <BotAvatar id={b.id} slotIndex={b.avatarSlot} size={28} />
              <span className="truncate flex-1 tracking-tight">{b.name}</span>
            </button>
          );
        })}
      </div>

      {/* 设置放在左下角状态栏里 */}
      <div className="border-t border-line/60 p-2.5 bg-[#f0eee9] dark:bg-[#181716]">
        <button
          type="button"
          onClick={onOpenSettings}
          className="flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-[13.5px] text-fg font-medium hover:bg-black/5 dark:hover:bg-white/10 transition"
          title="系统与偏好设置"
          aria-label="系统与偏好设置"
        >
          <SettingsIcon size={17} className="text-accent shrink-0" />
          <span className="flex-1">设置</span>
        </button>
      </div>
    </aside>
  );
}
