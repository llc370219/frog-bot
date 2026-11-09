import { Trash2 } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { api, type BotProfile } from '../api';

export default function BotEditor({ profile, onClose, onSaved, onDeleted }: {
  profile?: BotProfile; onClose: () => void; onSaved: (profile: BotProfile) => void; onDeleted: (id: string) => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [name, setName] = useState(profile?.name ?? '');
  const [instructions, setInstructions] = useState(profile?.instructions ?? '');
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [purge, setPurge] = useState(false);
  const [paths, setPaths] = useState<{ data_dir: string; workspace: string }>();
  const [error, setError] = useState('');
  const back = useRef<HTMLButtonElement>(null);
  const canDelete = !!profile && profile.id !== 'main';

  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);

  useEffect(() => {
    if (!confirmDelete) return;
    back.current?.focus();
    if (profile && canDelete) api.botPaths(profile.id).then(setPaths).catch(() => {});
  }, [confirmDelete, profile, canDelete]);
  const cancelDelete = () => { setConfirmDelete(false); setPurge(false); setError(''); };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (saving || !name.trim()) return;
    setSaving(true);
    setError('');
    const draft = { name: name.trim(), instructions: instructions.trim() };
    try {
      onSaved(await (profile ? api.editBot(profile.id, draft) : api.createBot(draft)));
    } catch (err) {
      setError((err as Error).message);
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!profile || !canDelete || saving) return;
    setSaving(true);
    setError('');
    try {
      await api.deleteBot(profile.id, purge);
      onDeleted(profile.id);
    } catch (err) {
      setError((err as Error).message);
      setSaving(false);
    }
  };

  const input = 'mt-1.5 w-full rounded-lg border border-line2 bg-canvas px-3 py-2 text-sm text-fg';
  return (
    <dialog ref={ref} aria-labelledby="bot-editor-title"
      className="m-auto w-[calc(100%-2rem)] max-w-md rounded-2xl border border-line bg-s1 p-5 text-fg shadow-xl backdrop:bg-black/40"
      onCancel={(event) => { event.preventDefault(); if (!saving) (confirmDelete ? cancelDelete : onClose)(); }}>
      {confirmDelete ? (
        <div className="flex flex-col gap-4">
          <h2 id="bot-editor-title" className="break-words text-lg font-semibold">删除「{profile?.name}」？</h2>
          <p className="text-sm leading-relaxed text-fg3">该 Bot 会从侧栏移除，后台任务和定时计划会停止。</p>
          <div className="rounded-lg border border-line bg-canvas px-3 py-2.5 text-xs leading-relaxed text-fg3">
            <p className="font-medium text-fg2">默认只是移除，数据仍留在磁盘上：</p>
            <p className="mt-1 break-all">聊天记录、记忆、密钥：{paths?.data_dir ?? '…'}</p>
            <p className="break-all">工作区文件：{paths?.workspace ?? '…'}</p>
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1" checked={purge} disabled={saving} onChange={(e) => setPurge(e.target.checked)} />
            <span><span className="font-medium text-danger">彻底删除</span>：同时删掉上面两个目录（聊天记录、记忆、保存的密钥和工作区文件），无法恢复。</span>
          </label>
          {error && <p className="text-sm text-danger" role="alert">{error}</p>}
          <div className="flex justify-end gap-2">
            <button ref={back} type="button" disabled={saving} onClick={cancelDelete} className="rounded-lg border border-line px-3 py-2 text-sm hover:bg-s2">返回</button>
            <button type="button" disabled={saving} onClick={remove} className="rounded-lg bg-danger-soft px-3 py-2 text-sm font-medium text-danger hover:bg-danger/15 disabled:opacity-50">
              {saving ? '正在删除…' : purge ? '彻底删除' : '移除 Bot'}
            </button>
          </div>
        </div>
      ) : (
      <form onSubmit={submit} className="flex flex-col gap-4">
        <h2 id="bot-editor-title" className="text-lg font-semibold">{profile ? '编辑 Bot' : '创建 Bot'}</h2>
        <label className="text-sm">名称
          <input className={input} required autoFocus maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="text-sm">职责说明（可选）
          <textarea className={input} rows={4} maxLength={4000} value={instructions} onChange={(e) => setInstructions(e.target.value)}
            placeholder="例如：负责代码审查，优先指出会影响用户的问题。" />
        </label>
        {error && <p className="text-sm text-danger" role="alert">{error}</p>}
        <div className="flex justify-end gap-2">
          {canDelete && <button type="button" disabled={saving} onClick={() => { setError(''); setConfirmDelete(true); }}
            className="mr-auto inline-flex items-center gap-1.5 rounded-lg px-2 py-2 text-sm text-danger hover:bg-danger-soft disabled:opacity-50">
            <Trash2 size={14} />删除 Bot
          </button>}
          <button type="button" disabled={saving} onClick={onClose} className="rounded-lg border border-line px-3 py-2 text-sm hover:bg-s2">取消</button>
          <button disabled={saving || !name.trim()} className="rounded-lg bg-accent px-3 py-2 text-sm font-medium text-on-accent disabled:opacity-50">
            {saving ? '正在保存…' : profile ? '保存' : '创建'}
          </button>
        </div>
      </form>
      )}
    </dialog>
  );
}
