import { CalendarClock, Pause, Play, Trash2, Zap } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useActiveBot, type Schedule } from '../api';

const actions = {
  pause: { icon: Pause, label: '暂停', result: '已暂停' },
  resume: { icon: Play, label: '恢复', result: '已恢复' },
  run: { icon: Zap, label: '立即执行一次', result: '已提交到任务队列' },
  delete: { icon: Trash2, label: '删除', result: '已删除' },
};
type Action = keyof typeof actions;

export default function Scheduler({ revision }: { revision: number }) {
  const { api, bot } = useActiveBot();
  const [schedules, setSchedules] = useState<Schedule[]>();
  const [loadError, setLoadError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<{ text: string; error?: boolean }>();

  // 事件只通知变更，由列表接口提供最新的完整计划（包括删除最后一项）。
  useEffect(() => {
    const controller = new AbortController();
    setLoadError('');
    api.schedules(controller.signal).then((list) => {
      if (!controller.signal.aborted) setSchedules(list);
    }).catch(() => {
      if (!controller.signal.aborted) setLoadError('无法加载定时任务，请重试。');
    });
    return () => controller.abort();
  }, [api, revision, refresh]);

  async function act(schedule: Schedule, action: Action) {
    if (busy || (action === 'delete' && !confirm(`删除定时任务「${schedule.title}」？`))) return;
    setBusy(true);
    setFeedback(undefined);
    try {
      await api.schedule(schedule.id, action);
      setFeedback({ text: `「${schedule.title}」${actions[action].result}` });
    } catch (e) {
      setFeedback({ text: `${actions[action].label}失败：${(e as Error).message}`, error: true });
    } finally {
      setRefresh((n) => n + 1);
      setBusy(false);
    }
  }

  return (
    <main className="min-h-0 flex-1 overflow-y-auto" aria-labelledby="scheduler-title">
      <div className="mx-auto w-full max-w-3xl px-4 pb-8 pt-16 sm:px-6">
        <h1 id="scheduler-title" className="text-2xl font-semibold tracking-tight text-fg">自动调度器</h1>
        <p className="mt-1.5 break-words text-sm text-fg3">{bot.name} 的定时任务</p>

        {loadError && (
          <div className="mt-5 flex flex-wrap items-center gap-2 rounded-xl bg-danger-soft p-3 text-sm text-danger">
            <p role="alert">{loadError}</p>
            <button type="button" className="underline" onClick={() => setRefresh((n) => n + 1)}>重试</button>
          </div>
        )}
        {feedback && <p role={feedback.error ? 'alert' : 'status'} className={`mt-4 break-words text-sm ${feedback.error ? 'text-danger' : 'text-ok'}`}>{feedback.text}</p>}

        {!schedules && !loadError && <p role="status" className="mt-6 text-sm text-fg3">正在加载定时任务…</p>}
        {schedules?.length === 0 && !loadError && (
          <div className="mt-6 flex flex-col items-center rounded-2xl border border-line bg-s1 px-4 py-10 text-center">
            <CalendarClock size={24} className="mb-3 text-fg4" aria-hidden="true" />
            <p className="text-sm font-medium">暂无定时任务</p>
            <p className="mt-1 text-sm text-fg3">在对话中告诉助理要做什么、何时执行，即可安排定时任务。</p>
          </div>
        )}
        {!!schedules?.length && (
          <ul className="mt-6 divide-y divide-line rounded-2xl border border-line bg-s1" aria-label="定时任务">
            {schedules.map((schedule) => (
              <li key={schedule.id} className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-1 px-3 py-3.5 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center sm:px-4">
                <CalendarClock size={17} aria-hidden="true" className={`shrink-0 ${schedule.enabled ? 'text-accent' : 'text-fg4'}`} />
                <div className="min-w-0 break-words">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <h2 className="min-w-0 text-sm font-medium">{schedule.title}</h2>
                    <span className={`rounded-full px-2 py-0.5 text-xs ${schedule.enabled ? 'bg-accent-soft text-ok' : 'bg-s2 text-fg3'}`}>
                      {schedule.enabled ? '已启用' : '已停用'}
                    </span>
                  </div>
                  <p className="mt-1 text-xs text-fg3">{describe(schedule)} · {schedule.timezone}</p>
                  {schedule.enabled && <p className="mt-1 text-xs text-fg3">{schedule.next_run_local ? `下次执行：${schedule.next_run_local}` : '暂无下次执行时间'}</p>}
                </div>
                <div className="col-start-2 flex justify-self-end gap-1 sm:col-start-auto">
                  {([schedule.enabled ? 'pause' : 'resume', 'run', 'delete'] as const).map((action) => {
                    const { icon: Icon, label } = actions[action];
                    return <button key={action} type="button" title={label} aria-label={`${label}：${schedule.title}`} disabled={busy}
                      onClick={() => void act(schedule, action)} className="grid size-8 place-items-center rounded-lg text-fg3 hover:bg-s2 hover:text-fg disabled:opacity-50">
                      <Icon size={15} aria-hidden="true" />
                    </button>;
                  })}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </main>
  );
}

function describe(schedule: Schedule) {
  const time = `${pad(schedule.hour)}:${pad(schedule.minute)}`;
  if (schedule.kind === 'once') return schedule.at_local ? `单次 ${schedule.at_local.slice(0, 16)}` : '单次';
  if (schedule.kind === 'daily') return `每天 ${time}`;
  if (schedule.kind === 'weekly') {
    const weekdays = schedule.weekdays?.map((day) => `周${'日一二三四五六'[day]}`).join('、');
    return `每周${weekdays ? `（${weekdays}）` : ''} ${time}`;
  }
  return schedule.kind;
}

const pad = (n = 0) => String(n).padStart(2, '0');
