import {
  Bell,
  BellOff,
  Cable,
  Download,
  Info as InfoIcon,
  KeyRound,
  Laptop,
  Loader2,
  Moon,
  Plus,
  Save,
  Sliders,
  Sun,
  Trash2,
  X,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { api, type Info, type ProviderSettings, type SettingsState } from '../api';

type Tab = 'general' | 'model' | 'secrets' | 'about';

export default function SettingsDialog({
  open,
  onClose,
  info,
  notify,
  onToggleNotify,
  onSettingsSaved,
}: {
  open: boolean;
  onClose: () => void;
  info: Info;
  notify: boolean;
  onToggleNotify: () => void;
  onSettingsSaved?: () => void;
}) {
  const [tab, setTab] = useState<Tab>('general');
  const [theme, setTheme] = useState<'system' | 'light' | 'dark'>(() => {
    return (localStorage.getItem('frogbot-theme') as 'system' | 'light' | 'dark') || 'system';
  });
  const [secrets, setSecrets] = useState<Array<{ name: string; description?: string; updated_at?: string }>>([]);
  const [loadingSecrets, setLoadingSecrets] = useState(false);

  // 模型与 API 渠道设置状态
  const [settings, setSettings] = useState<SettingsState | null>(null);
  const [selectedProviderId, setSelectedProviderId] = useState<string>('');
  const [fetchingModels, setFetchingModels] = useState<boolean>(false);
  const [savingSettings, setSavingSettings] = useState<boolean>(false);
  const [errorMsg, setErrorMsg] = useState<string>('');
  const [successMsg, setSuccessMsg] = useState<string>('');

  useEffect(() => {
    if (!open) return;
    if (tab === 'secrets') {
      setLoadingSecrets(true);
      api
        .secrets?.()
        .then((list) => setSecrets(list || []))
        .catch(() => {})
        .finally(() => setLoadingSecrets(false));
    } else if (tab === 'model') {
      setErrorMsg('');
      setSuccessMsg('');
      api
        .settings()
        .then((s) => {
          setSettings(s);
          if (s.providers.length > 0 && !selectedProviderId) {
            setSelectedProviderId(s.providers[0].id);
          }
        })
        .catch((e) => setErrorMsg(e.message || '读取设置失败'));
    }
  }, [open, tab]);

  if (!open) return null;

  const setAppTheme = (t: 'system' | 'light' | 'dark') => {
    setTheme(t);
    const root = document.documentElement;
    if (t === 'system') {
      delete root.dataset.theme;
      localStorage.removeItem('frogbot-theme');
    } else {
      root.dataset.theme = t;
      localStorage.setItem('frogbot-theme', t);
    }
  };

  const selectedProvider = settings?.providers.find((p) => p.id === selectedProviderId);

  const updateSelectedProvider = (patch: Partial<ProviderSettings>) => {
    if (!settings) return;
    const nextProviders = settings.providers.map((p) => (p.id === selectedProviderId ? { ...p, ...patch } : p));
    setSettings({ ...settings, providers: nextProviders });
  };

  const handleFetchModels = async () => {
    if (!selectedProvider) return;
    setFetchingModels(true);
    setErrorMsg('');
    setSuccessMsg('');
    try {
      const res = await api.providerModels(selectedProvider);
      if (!res.models || res.models.length === 0) {
        setErrorMsg('获取成功，但该渠道未返回任何可用模型');
      } else {
        updateSelectedProvider({ models: res.models });
        setSuccessMsg(`成功拉取到 ${res.models.length} 个模型！`);
      }
    } catch (err: any) {
      setErrorMsg(err.message || '拉取模型失败，请检查 Base URL 和 API Key');
    } finally {
      setFetchingModels(false);
    }
  };

  const handleSaveSettings = async () => {
    if (!settings) return;
    setSavingSettings(true);
    setErrorMsg('');
    setSuccessMsg('');
    try {
      await api.saveSettings(settings);
      setSuccessMsg('设置已保存并生效！');
      onSettingsSaved?.();
    } catch (err: any) {
      setErrorMsg(err.message || '保存设置失败');
    } finally {
      setSavingSettings(false);
    }
  };

  const handleAddProvider = () => {
    if (!settings) return;
    const newId = 'provider_' + Math.random().toString(36).substring(2, 9);
    const newProvider: ProviderSettings = {
      id: newId,
      name: '自定义 API 渠道',
      base_url: 'https://api.openai.com/v1',
      api_key: '',
      has_api_key: false,
      auth_required: true,
      enabled: true,
      models: [],
      hidden_models: [],
    };
    setSettings({
      ...settings,
      providers: [...settings.providers, newProvider],
    });
    setSelectedProviderId(newId);
  };

  const handleRemoveProvider = (id: string) => {
    if (!settings || settings.providers.length <= 1) {
      setErrorMsg('至少保留一个 API 渠道');
      return;
    }
    const nextProviders = settings.providers.filter((p) => p.id !== id);
    setSettings({
      ...settings,
      providers: nextProviders,
    });
    setSelectedProviderId(nextProviders[0].id);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-xs p-4 animate-in fade-in duration-150">
      <div
        className="relative flex h-[620px] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-line bg-canvas shadow-2xl"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
      >
        {/* Header */}
        <div className="flex h-14 items-center justify-between border-b border-line px-6">
          <div className="flex items-center gap-2.5">
            <Sliders size={18} className="text-accent" />
            <h2 id="settings-title" className="text-[16px] font-semibold text-fg">系统与模型设置</h2>
          </div>
          <button
            onClick={onClose}
            className="grid size-8 place-items-center rounded-lg text-fg3 transition hover:bg-s2 hover:text-fg"
            aria-label="关闭"
          >
            <X size={17} />
          </button>
        </div>

        {/* Content body with side tabs */}
        <div className="flex flex-1 min-h-0">
          {/* Sidebar tabs */}
          <div className="w-44 border-r border-line bg-s2/50 p-3 flex flex-col gap-1">
            <button
              onClick={() => setTab('general')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'general' ? 'bg-s1 text-fg shadow-xs font-semibold' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <Sliders size={15} />
              通用外观
            </button>
            <button
              onClick={() => setTab('model')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'model' ? 'bg-s1 text-fg shadow-xs font-semibold' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <Cable size={15} />
              API 渠道与模型
            </button>
            <button
              onClick={() => setTab('secrets')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'secrets' ? 'bg-s1 text-fg shadow-xs font-semibold' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <KeyRound size={15} />
              密钥保险箱
            </button>
            <button
              onClick={() => setTab('about')}
              className={`flex items-center gap-2.5 w-full px-3 py-2 rounded-xl text-left text-xs font-medium transition ${
                tab === 'about' ? 'bg-s1 text-fg shadow-xs font-semibold' : 'text-fg3 hover:bg-s2 hover:text-fg'
              }`}
            >
              <InfoIcon size={15} />
              关于系统
            </button>
          </div>

          {/* Tab content panel */}
          <div className="flex-1 overflow-y-auto p-6 text-sm text-fg2">
            {tab === 'general' && (
              <div className="flex flex-col gap-6">
                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">主题模式</h3>
                  <div className="grid grid-cols-3 gap-2.5">
                    {[
                      { id: 'system', label: '跟随系统', icon: Laptop },
                      { id: 'light', label: '浅色模式', icon: Sun },
                      { id: 'dark', label: '深色模式', icon: Moon },
                    ].map((item) => (
                      <button
                        key={item.id}
                        onClick={() => setAppTheme(item.id as any)}
                        className={`flex flex-col items-center gap-2 p-3 rounded-xl border text-xs font-medium transition ${
                          theme === item.id
                            ? 'border-accent bg-accent/10 text-accent font-semibold'
                            : 'border-line bg-s1 text-fg3 hover:border-line2 hover:text-fg'
                        }`}
                      >
                        <item.icon size={20} />
                        {item.label}
                      </button>
                    ))}
                  </div>
                </div>

                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-3">系统通知</h3>
                  <div className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3.5">
                    <div className="flex items-center gap-3">
                      <div className="grid size-8 place-items-center rounded-lg bg-s2 text-fg3">
                        {notify ? <Bell size={17} /> : <BellOff size={17} />}
                      </div>
                      <div>
                        <div className="text-xs font-medium text-fg">后台桌面弹窗通知</div>
                        <div className="text-[11.5px] text-fg4">页面在后台时，任务完成或等待审批时会提醒你</div>
                      </div>
                    </div>
                    <button
                      onClick={onToggleNotify}
                      className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out ${
                        notify ? 'bg-accent' : 'bg-s3'
                      }`}
                    >
                      <span className={`pointer-events-none inline-block size-5 transform rounded-full bg-white shadow-sm ring-0 transition duration-200 ease-in-out ${notify ? 'translate-x-5' : 'translate-x-0'}`} />
                    </button>
                  </div>
                </div>
              </div>
            )}

            {tab === 'model' && (
              <div className="flex flex-col gap-5">
                {errorMsg && (
                  <div className="rounded-xl border border-danger/40 bg-danger-soft p-3 text-xs text-danger">
                    {errorMsg}
                  </div>
                )}
                {successMsg && (
                  <div className="rounded-xl border border-ok/40 bg-ok-soft p-3 text-xs text-ok">
                    {successMsg}
                  </div>
                )}

                {/* API 渠道列表 */}
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <h3 className="text-xs font-semibold uppercase tracking-wider text-fg4">API 渠道</h3>
                    <button
                      onClick={handleAddProvider}
                      className="inline-flex items-center gap-1 rounded-lg border border-line bg-s1 px-2 py-1 text-xs text-fg hover:border-line2 hover:bg-s2 transition"
                    >
                      <Plus size={13} />
                      添加渠道
                    </button>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {settings?.providers.map((p) => (
                      <button
                        key={p.id}
                        onClick={() => {
                          setSelectedProviderId(p.id);
                          setErrorMsg('');
                          setSuccessMsg('');
                        }}
                        className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg border text-xs font-medium transition ${
                          selectedProviderId === p.id
                            ? 'border-accent bg-accent/10 text-accent'
                            : 'border-line bg-s1 text-fg3 hover:bg-s2 hover:text-fg'
                        }`}
                      >
                        <span className={`size-1.5 rounded-full ${p.enabled ? 'bg-ok' : 'bg-fg4'}`} />
                        {p.name}
                      </button>
                    ))}
                  </div>
                </div>

                {/* 选中的渠道配置 */}
                {selectedProvider && (
                  <div className="rounded-xl border border-line bg-s1 p-4 flex flex-col gap-3">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <Cable size={15} className="text-accent" />
                        <span className="text-xs font-semibold text-fg">渠道参数配置</span>
                      </div>
                      {settings && settings.providers.length > 1 && (
                        <button
                          onClick={() => handleRemoveProvider(selectedProvider.id)}
                          className="flex items-center gap-1 text-xs text-danger hover:underline"
                        >
                          <Trash2 size={13} />
                          删除此渠道
                        </button>
                      )}
                    </div>

                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="text-[11.5px] text-fg4 mb-1 block">渠道名称</label>
                        <input
                          type="text"
                          value={selectedProvider.name}
                          onChange={(e) => updateSelectedProvider({ name: e.target.value })}
                          className="w-full rounded-lg border border-line bg-canvas px-2.5 py-1.5 text-xs text-fg focus:border-accent focus:outline-none"
                          placeholder="例如：OpenAI / 转发中转"
                        />
                      </div>
                      <div>
                        <label className="text-[11.5px] text-fg4 mb-1 block">接口地址 (Base URL)</label>
                        <input
                          type="text"
                          value={selectedProvider.base_url}
                          onChange={(e) => updateSelectedProvider({ base_url: e.target.value })}
                          className="w-full rounded-lg border border-line bg-canvas px-2.5 py-1.5 text-xs font-mono text-fg focus:border-accent focus:outline-none"
                          placeholder="https://api.openai.com/v1"
                        />
                      </div>
                    </div>

                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <label className="text-[11.5px] text-fg4">API Key</label>
                        {selectedProvider.has_api_key && (
                          <span className="text-[11px] text-ok">已安全加密保存</span>
                        )}
                      </div>
                      <input
                        type="password"
                        value={selectedProvider.api_key || ''}
                        onChange={(e) => updateSelectedProvider({ api_key: e.target.value })}
                        className="w-full rounded-lg border border-line bg-canvas px-2.5 py-1.5 text-xs font-mono text-fg focus:border-accent focus:outline-none"
                        placeholder={selectedProvider.has_api_key ? '已保存密钥；留空保留原密钥' : 'sk-...'}
                      />
                    </div>

                    {/* 拉取模型按钮与列表 */}
                    <div className="pt-2 border-t border-line/60">
                      <div className="flex items-center justify-between mb-2">
                        <span className="text-xs font-medium text-fg">
                          可选模型列表 ({selectedProvider.models.length || 0})
                        </span>
                        <button
                          onClick={handleFetchModels}
                          disabled={fetchingModels}
                          className="inline-flex items-center gap-1 rounded-lg bg-accent px-2.5 py-1 text-xs font-medium text-white hover:bg-accent-soft transition disabled:opacity-50"
                        >
                          {fetchingModels ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}
                          拉取模型
                        </button>
                      </div>

                      {selectedProvider.models.length > 0 ? (
                        <div className="max-h-36 overflow-y-auto rounded-lg border border-line bg-canvas/60 p-1.5 flex flex-wrap gap-1">
                          {selectedProvider.models.map((m) => (
                            <span
                              key={m}
                              className="rounded bg-s2 px-2 py-0.5 text-[11px] font-mono text-fg3"
                            >
                              {m}
                            </span>
                          ))}
                        </div>
                      ) : (
                        <div className="rounded-lg border border-line/50 bg-canvas/40 py-4 text-center text-xs text-fg4">
                          暂无拉取的模型，配置好接口地址与密钥后点击「拉取模型」
                        </div>
                      )}
                    </div>
                  </div>
                )}

                {/* 模型路由选择 */}
                {settings && (
                  <div className="rounded-xl border border-line bg-s1 p-4 flex flex-col gap-3">
                    <div className="text-xs font-semibold uppercase tracking-wider text-fg4 mb-1">模型角色绑定</div>
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="text-[11.5px] text-fg4 mb-1 block">聊天层模型 (Chat Model)</label>
                        <select
                          value={settings.chat_route?.model || ''}
                          onChange={(e) =>
                            setSettings({
                              ...settings,
                              chat_route: {
                                provider_id: selectedProviderId,
                                model: e.target.value,
                              },
                            })
                          }
                          className="w-full rounded-lg border border-line bg-canvas px-2.5 py-1.5 text-xs font-mono text-fg focus:border-accent focus:outline-none"
                        >
                          <option value="">-- 请选择模型 --</option>
                          {selectedProvider?.models.map((m) => (
                            <option key={m} value={m}>
                              {m}
                            </option>
                          ))}
                          {settings.chat_route?.model && !selectedProvider?.models.includes(settings.chat_route.model) && (
                            <option value={settings.chat_route.model}>
                              {settings.chat_route.model} (当前)
                            </option>
                          )}
                        </select>
                      </div>
                      <div>
                        <label className="text-[11.5px] text-fg4 mb-1 block">Agent 执行模型 (Agent Model)</label>
                        <select
                          value={settings.agent_route?.model || ''}
                          onChange={(e) =>
                            setSettings({
                              ...settings,
                              agent_route: {
                                provider_id: selectedProviderId,
                                model: e.target.value,
                              },
                            })
                          }
                          className="w-full rounded-lg border border-line bg-canvas px-2.5 py-1.5 text-xs font-mono text-fg focus:border-accent focus:outline-none"
                        >
                          <option value="">-- 请选择模型 --</option>
                          {selectedProvider?.models.map((m) => (
                            <option key={m} value={m}>
                              {m}
                            </option>
                          ))}
                          {settings.agent_route?.model && !selectedProvider?.models.includes(settings.agent_route.model) && (
                            <option value={settings.agent_route.model}>
                              {settings.agent_route.model} (当前)
                            </option>
                          )}
                        </select>
                      </div>
                    </div>
                  </div>
                )}

                {/* 底部保存按钮 */}
                <div className="flex justify-end pt-2">
                  <button
                    onClick={handleSaveSettings}
                    disabled={savingSettings}
                    className="inline-flex items-center gap-1.5 rounded-xl bg-accent px-4 py-2 text-xs font-medium text-white hover:bg-accent-soft transition disabled:opacity-50"
                  >
                    {savingSettings ? <Loader2 size={14} className="animate-spin" /> : <Save size={14} />}
                    保存配置并生效
                  </button>
                </div>
              </div>
            )}

            {tab === 'secrets' && (
              <div className="flex flex-col gap-4">
                <div className="flex items-center justify-between">
                  <div className="text-xs font-semibold uppercase tracking-wider text-fg4">已保存的密钥</div>
                  <span className="text-[11.5px] text-fg4">AES-256-GCM 本机加密</span>
                </div>

                {loadingSecrets ? (
                  <div className="flex justify-center py-10 text-fg3 text-xs">加载密钥库…</div>
                ) : secrets.length === 0 ? (
                  <div className="rounded-xl border border-line bg-s1 p-8 text-center text-xs text-fg4">
                    保险箱空空如也。聊天中需要使用密钥时，助理会自动弹出安全卡片让你填写。
                  </div>
                ) : (
                  <div className="flex flex-col gap-2">
                    {secrets.map((sec) => (
                      <div
                        key={sec.name}
                        className="flex items-center justify-between rounded-xl border border-line bg-s1 p-3 text-xs"
                      >
                        <div className="flex items-center gap-2.5">
                          <span className="grid size-7 place-items-center rounded-lg bg-s2 text-accent">
                            <KeyRound size={14} />
                          </span>
                          <div>
                            <div className="font-mono font-medium text-fg">{sec.name}</div>
                            {sec.description && <div className="text-[11.5px] text-fg4">{sec.description}</div>}
                          </div>
                        </div>
                        <span className="rounded bg-ok-soft px-2 py-0.5 text-[11px] font-medium text-ok">
                          已加密
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {tab === 'about' && (
              <div className="flex flex-col gap-5">
                <div className="flex items-center gap-3 rounded-2xl border border-line bg-s1 p-4">
                  <div className="grid size-12 shrink-0 place-items-center rounded-full bg-white ring-1 ring-line2">
                    <img src="/frog.png" alt="" className="size-[88%] object-contain" />
                  </div>
                  <div>
                    <h3 className="font-serif text-[17px] font-semibold text-fg">{info.name || 'Frog'}</h3>
                    <p className="text-xs text-fg3">单个 AI 助理 · 快速聊天与后台 Agent 工作引擎</p>
                  </div>
                </div>

                <div className="rounded-xl border border-line bg-s1 p-4 text-xs text-fg3 flex flex-col gap-2.5">
                  <div className="flex justify-between py-1 border-b border-line/40">
                    <span>版本模式</span>
                    <span className="font-mono text-fg">{info.demo ? '演示模式 (Demo)' : '生产运行'}</span>
                  </div>
                  <div className="flex justify-between py-1 border-b border-line/40">
                    <span>当前聊天模型</span>
                    <span className="font-mono text-fg">{info.chat_model || '—'}</span>
                  </div>
                  <div className="flex justify-between py-1 border-b border-line/40">
                    <span>当前 Agent 模型</span>
                    <span className="font-mono text-fg">{info.agent_model || '—'}</span>
                  </div>
                  <div className="flex justify-between py-1">
                    <span>系统时区</span>
                    <span className="font-mono text-fg">Asia/Shanghai</span>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

