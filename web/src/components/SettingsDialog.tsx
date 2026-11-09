import { Eye, EyeOff, KeyRound, Lock, PlugZap, Settings as Gear, ShieldCheck, X } from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type Settings, type SettingsInput } from '../api';

// 常见的 OpenAI 兼容服务：点一下填好 API 地址（模型名仍需自己确认）。
const presets = [
  { name: 'OpenAI', url: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
  { name: 'xAI', url: 'https://api.x.ai/v1', model: 'grok-3-mini' },
  { name: 'DeepSeek', url: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
  { name: 'OpenRouter', url: 'https://openrouter.ai/api/v1', model: '' },
  { name: 'Ollama（本机）', url: 'http://127.0.0.1:11434/v1', model: '' },
];

// SettingsDialog：在网页里配置模型。API Key 只发给后端加密保存，页面上永远不回显。
export default function SettingsDialog({ firstRun, onClose, onSaved }: { firstRun: boolean; onClose: () => void; onSaved: () => void }) {
  const [cur, setCur] = useState<Settings | null>(null);
  const [form, setForm] = useState<SettingsInput>({ base_url: '', chat_model: '', agent_model: '' });
  const [key, setKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [sameModel, setSameModel] = useState(true);
  const [busy, setBusy] = useState<'' | 'save' | 'test'>('');
  const [err, setErr] = useState('');
  const [ok, setOk] = useState('');

  useEffect(() => {
    api.settings().then((s) => {
      setCur(s);
      setForm({ base_url: s.base_url, agent_model: s.agent_model, chat_model: s.chat_model });
      setSameModel(!s.chat_model || s.chat_model === s.agent_model);
    }).catch((e) => setErr((e as Error).message));
  }, []);

  const input = (): SettingsInput => ({ ...form, chat_model: sameModel ? '' : form.chat_model, api_key: key || undefined });
  const run = async (kind: 'save' | 'test') => {
    setBusy(kind); setErr(''); setOk('');
    try {
      if (kind === 'test') {
        const r = await api.testSettings(input());
        setOk(`连接成功${r.reply ? `，模型回复：${r.reply}` : ''}`);
      } else {
        const s = await api.saveSettings(input());
        setCur(s); setKey('');
        onSaved();
        onClose();
      }
    } catch (e) { setErr((e as Error).message); }
    finally { setBusy(''); }
  };

  const locked = cur?.locked;
  const field = 'h-10 w-full rounded-xl border border-line2 bg-composer px-3 text-[13.5px] text-fg outline-none placeholder:text-fg4 focus:border-accent disabled:opacity-60';
  const label = 'mb-1 block text-[12.5px] font-medium text-fg2';
  const envNote = <span className="ml-1.5 text-[11px] font-normal text-fg4">（由环境变量提供）</span>;

  return (
    <div className="fixed inset-0 z-40 grid place-items-center bg-black/30 p-4" role="dialog" aria-modal="true" aria-labelledby="settings-title"
      onMouseDown={(e) => { if (e.target === e.currentTarget && !firstRun) onClose(); }}>
      <div className="rise max-h-[92vh] w-full max-w-lg overflow-y-auto rounded-2xl border border-line bg-canvas shadow-[0_20px_60px_-20px_var(--shadow)]">
        <div className="flex items-start gap-3 border-b border-line px-5 py-4">
          <span className="grid size-9 shrink-0 place-items-center rounded-full bg-accent-soft text-accent"><Gear size={17} /></span>
          <div className="min-w-0 flex-1">
            <h2 id="settings-title" className="font-serif text-[18px] font-semibold tracking-tight">{firstRun ? '先连接一个模型' : '模型设置'}</h2>
            <p className="mt-0.5 text-[12.5px] leading-relaxed text-fg3">支持任何 OpenAI 兼容接口。保存后立即生效，不用重启。</p>
          </div>
          {!firstRun && <button onClick={onClose} aria-label="关闭" className="grid size-8 place-items-center rounded-full text-fg4 hover:bg-s2 hover:text-fg"><X size={16} /></button>}
        </div>
        <form className="flex flex-col gap-4 px-5 py-4" onSubmit={(e) => { e.preventDefault(); run('save'); }}>
          <div>
            <label className={label} htmlFor="s-url">API 地址{locked?.base_url && envNote}</label>
            <input id="s-url" className={field} value={form.base_url} disabled={!!locked?.base_url} spellCheck={false} autoCapitalize="off"
              placeholder="https://api.openai.com/v1" onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
            {!locked?.base_url && (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {presets.map((p) => (
                  <button key={p.name} type="button" onClick={() => setForm({ ...form, base_url: p.url, agent_model: form.agent_model || p.model })}
                    className={`rounded-full border px-2.5 py-0.5 text-[11.5px] transition ${form.base_url === p.url ? 'border-accent text-accent' : 'border-line2 text-fg3 hover:border-accent/50 hover:text-fg'}`}>{p.name}</button>
                ))}
              </div>
            )}
          </div>
          <div>
            <label className={label} htmlFor="s-key">API Key{locked?.api_key && envNote}</label>
            <div className="flex items-center gap-2 rounded-xl border border-line2 bg-composer px-3 focus-within:border-accent">
              <KeyRound size={14} className="shrink-0 text-fg4" />
              <input id="s-key" type={showKey ? 'text' : 'password'} value={key} disabled={!!locked?.api_key} onChange={(e) => setKey(e.target.value)}
                autoComplete="new-password" spellCheck={false} autoCapitalize="off" autoCorrect="off" name="frogbot-model-key"
                placeholder={locked?.api_key ? '已由环境变量提供' : cur?.has_api_key ? '已保存（留空表示不修改）' : '粘贴 API Key（本机模型可以留空）'}
                className="h-10 min-w-0 flex-1 bg-transparent font-mono text-[13.5px] text-fg outline-none placeholder:font-sans placeholder:text-fg4" />
              <button type="button" onClick={() => setShowKey(!showKey)} aria-label={showKey ? '隐藏' : '显示'} className="grid size-7 shrink-0 place-items-center rounded-full text-fg4 hover:bg-s2 hover:text-fg">{showKey ? <EyeOff size={15} /> : <Eye size={15} />}</button>
            </div>
            <p className="mt-1.5 flex items-start gap-1.5 text-[11.5px] leading-relaxed text-fg4">
              <Lock size={11} className="mt-[3px] shrink-0" />加密保存在本机数据目录，页面不会再显示它，助理和工具也读不到。
              {cur?.has_api_key && !locked?.api_key && <button type="button" className="ml-auto shrink-0 underline hover:text-danger"
                onClick={() => { if (confirm('删除已保存的 API Key？')) api.saveSettings({ ...input(), api_key: undefined, clear_api_key: true }).then((s) => { setCur(s); onSaved(); }).catch((e) => setErr((e as Error).message)); }}>删除已保存的 Key</button>}
            </p>
          </div>
          <div>
            <label className={label} htmlFor="s-model">模型名{locked?.agent_model && envNote}</label>
            <input id="s-model" className={field} value={form.agent_model} disabled={!!locked?.agent_model} spellCheck={false} autoCapitalize="off"
              placeholder="例如 gpt-4o-mini、grok-3-mini、deepseek-chat" onChange={(e) => setForm({ ...form, agent_model: e.target.value })} />
            <label className="mt-2 flex items-center gap-2 text-[12.5px] text-fg3">
              <input type="checkbox" checked={sameModel} disabled={!!locked?.chat_model} onChange={(e) => setSameModel(e.target.checked)} className="accent-[var(--accent)]" />
              聊天和后台任务用同一个模型
            </label>
            {!sameModel && (
              <div className="mt-2">
                <label className={label} htmlFor="s-chat">聊天模型（建议用快一点的）{locked?.chat_model && envNote}</label>
                <input id="s-chat" className={field} value={form.chat_model} disabled={!!locked?.chat_model} spellCheck={false} autoCapitalize="off"
                  placeholder="留空 = 和上面一样" onChange={(e) => setForm({ ...form, chat_model: e.target.value })} />
              </div>
            )}
          </div>
          {err && <p className="rounded-xl bg-danger-soft px-3 py-2 text-[12.5px] text-danger" role="alert">{err}</p>}
          {ok && <p className="flex items-start gap-1.5 rounded-xl bg-accent-soft px-3 py-2 text-[12.5px] text-ok" role="status"><ShieldCheck size={14} className="mt-px shrink-0" />{ok}</p>}
          <div className="flex flex-wrap items-center gap-2">
            <button type="submit" disabled={!!busy || !cur}
              className="inline-flex items-center gap-1.5 rounded-full bg-accent px-4 py-1.5 text-[13px] font-medium text-on-accent transition hover:bg-accent-hover disabled:opacity-50">
              <ShieldCheck size={14} />{busy === 'save' ? '保存中…' : '保存'}
            </button>
            <button type="button" onClick={() => run('test')} disabled={!!busy || !cur}
              className="inline-flex items-center gap-1.5 rounded-full border border-line2 px-4 py-1.5 text-[13px] text-fg2 transition hover:border-accent hover:text-accent disabled:opacity-50">
              <PlugZap size={14} />{busy === 'test' ? '测试中…' : '测试连接'}
            </button>
          </div>
          {cur && (
            <p className="border-t border-line pt-3 text-[11.5px] leading-relaxed text-fg4">
              数据目录：<code className="break-all font-mono">{cur.data_dir}</code><br />
              工作区（助理读写文件的地方）：<code className="break-all font-mono">{cur.workspace}</code><br />
              只在本机使用：没有登录功能，不要暴露到公网。执行命令没有沙箱，有风险的操作默认会先问你。
            </p>
          )}
        </form>
      </div>
    </div>
  );
}
