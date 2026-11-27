// Frog Bot V2 Single-Pane Multiplexed WebSocket Gateway + REST API client for Web UI.

export interface V2ApprovalCard {
  id: string;
  bot_id: string;
  action: string;
  trigger_source: string;
  tainted: boolean;
  taint_sources?: { kind: string; origin: string }[];
  target_domain?: string;
  recipient?: string;
  summary: string;
  secret_handles?: string[];
  reason: string;
  can_whitelist: boolean;
  whitelist_secret?: string;
  whitelist_domain?: string;
}

export interface V2VerifySession {
  id: string;
  bot_id: string;
  url: string;
  reason: string;
  status: string;
  viewport_width: number;
  viewport_height: number;
}

export interface V2CLITask {
  id: string;
  bot_id: string;
  cli: string;
  mode: string;
  status: string;
  prompt_brief?: string;
  summary?: string;
  changed_files?: string[];
}

export interface V2QuotaStatus {
  cli: string;
  health: string;
  estimated_remaining_pct: number;
  active_slots: number;
  max_slots: number;
  window_calls_5h: number;
}

export interface V2MeshPeer {
  node_id: string;
  name: string;
  network_tag?: string;
  online: boolean;
  connected_via?: string;
  bots?: string[];
}

export interface V2DoctorReport {
  os: string;
  arch: string;
  go_version: string;
  healthy: boolean;
  checks: { id: string; name: string; status: string; detail: string; install_cmd?: string }[];
}

export interface V2Status {
  node_id: string;
  node_name: string;
  network_tag?: string;
  ws_clients: number;
  browser_active: number;
  cli_quotas: V2QuotaStatus[];
  cli_tasks: V2CLITask[];
  pending_approvals: V2ApprovalCard[];
  pending_verifications: V2VerifySession[];
  peers: V2MeshPeer[];
}

export interface CLIAuthSessionView {
  id: string;
  cli: string;
  mode: 'device' | 'oauth';
  status: 'running' | 'waiting_user' | 'succeeded' | 'failed' | 'cancelled';
  auth_url?: string;
  user_code?: string;
  needs_input: boolean;
  input_prompt?: string;
  output?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface CLIAuthStatus {
  id: string;
  name: string;
  installed: boolean;
  path?: string;
  logged_in: boolean;
  auth_method?: string;
  account_hint?: string;
  supports_device_auth: boolean;
  supports_oauth_bridge: boolean;
  supports_oauth_token: boolean;
  oauth_token_env?: string;
  api_key_env: string;
  login_hint: string;
  install_hint: string;
  active_session?: CLIAuthSessionView;
}

export interface ProxyEngineDetected {
  id: string;
  name: string;
  installed: boolean;
  binary_path?: string;
  default_url: string;
  default_args?: string;
  probe_alive: boolean;
  repo_url?: string;
  install_hint?: string;
  description?: string;
}

export interface ProxyGatewayConfig {
  enabled: boolean;
  disclaimer_accepted: boolean;
  disclaimer_accepted_at?: string;
  disclaimer_version?: string;
  engine: string;
  base_url: string;
  channel_name: string;
  auth_required: boolean;
  has_api_key: boolean;
  auto_start: boolean;
  binary_path?: string;
  start_args?: string;
  models?: string[];
}

export interface ProxyGatewayStatus {
  config: ProxyGatewayConfig;
  disclaimer_version: string;
  disclaimer_text: string;
  detected_engines: ProxyEngineDetected[];
  managed_running: boolean;
  managed_pid?: number;
  last_logs?: string[];
  probe_ok: boolean;
  probe_latency_ms?: number;
  probe_error?: string;
  live_models?: string[];
  channel_synced: boolean;
  channel_id: string;
}

export interface ProxyGatewaySaveInput {
  enabled: boolean;
  disclaimer_accepted: boolean;
  engine: string;
  base_url: string;
  channel_name: string;
  auth_required: boolean;
  api_key?: string;
  auto_start: boolean;
  binary_path?: string;
  start_args?: string;
  models?: string[];
  sync_channel: boolean;
}

async function reqJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    credentials: 'same-origin',
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
    ...init,
  });
  if (!res.ok) {
    const raw = await res.text();
    try {
      const parsed = JSON.parse(raw) as { error?: string };
      if (parsed.error) throw new Error(parsed.error);
    } catch (e) {
      if (e instanceof Error && e.message && !raw.startsWith('{')) throw e;
    }
    throw new Error(raw || `HTTP ${res.status}`);
  }
  return res.json() as Promise<T>;
}

export const v2API = {
  status: () => reqJSON<V2Status>('/api/v2/status'),
  doctor: (fix = false) => reqJSON<V2DoctorReport>(`/api/v2/doctor${fix ? '?fix=1' : ''}`),
  decideApproval: (id: string, choice: 'allow_once' | 'allow_whitelist' | 'deny', whitelist_domain = '') =>
    reqJSON('/api/v2/approvals/' + encodeURIComponent(id), {
      method: 'POST',
      body: JSON.stringify({ choice, whitelist_domain }),
    }),
  cliLog: (id: string, offset = 0) =>
    reqJSON<{ task_id: string; content: string; offset: number }>(`/api/v2/cli/tasks/${encodeURIComponent(id)}/log?offset=${offset}`),
  cancelCLI: (id: string) => reqJSON(`/api/v2/cli/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: '{}' }),
  pairCode: () => reqJSON<{ code: string; node_id: string; expires_at: string }>('/api/v2/mesh/pair-code', { method: 'POST', body: '{}' }),
  runGC: () => reqJSON<{ pointers_removed: number; logs_removed_by_age: number; logs_removed_by_cap: number; bytes_reclaimed: number }>('/api/v2/gc', { method: 'POST', body: '{}' }),
  exportBackup: async (passphrase: string) => {
    const res = await fetch('/api/v2/backup/export', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ passphrase }),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.blob();
  },

  // Official CLI Headless Login (separate from reverse proxy)
  cliAuthList: () => reqJSON<CLIAuthStatus[]>('/api/v2/cli/auth'),
  cliAuthStart: (cli: string, mode: 'auto' | 'device' | 'oauth' = 'auto') =>
    reqJSON<CLIAuthSessionView>(`/api/v2/cli/auth/${encodeURIComponent(cli)}/start`, {
      method: 'POST',
      body: JSON.stringify({ mode }),
    }),
  cliAuthSession: (id: string) =>
    reqJSON<CLIAuthSessionView>(`/api/v2/cli/auth/sessions/${encodeURIComponent(id)}`),
  cliAuthSubmitInput: (id: string, input: string) =>
    reqJSON<CLIAuthSessionView>(`/api/v2/cli/auth/sessions/${encodeURIComponent(id)}/input`, {
      method: 'POST',
      body: JSON.stringify({ input }),
    }),
  cliAuthCancel: (id: string) =>
    reqJSON<{ ok: boolean }>(`/api/v2/cli/auth/sessions/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
      body: '{}',
    }),
  cliAuthSaveCredential: (cli: string, kind: 'oauth_token' | 'api_key', value: string) =>
    reqJSON<CLIAuthStatus>(`/api/v2/cli/auth/${encodeURIComponent(cli)}/credential`, {
      method: 'POST',
      body: JSON.stringify({ kind, value }),
    }),
  cliAuthLogout: (cli: string, clearNative = false) =>
    reqJSON<CLIAuthStatus>(`/api/v2/cli/auth/${encodeURIComponent(cli)}/logout`, {
      method: 'POST',
      body: JSON.stringify({ clear_native: clearNative }),
    }),

  // Optional Front-Bot Reverse Proxy Gateway (CLIProxyAPI / OpenCodex / Magpie / Custom)
  proxyStatus: () => reqJSON<ProxyGatewayStatus>('/api/v2/proxy/status'),
  proxySave: (input: ProxyGatewaySaveInput) =>
    reqJSON<ProxyGatewayStatus>('/api/v2/proxy/config', {
      method: 'POST',
      body: JSON.stringify(input),
    }),
  proxyStart: () => reqJSON<ProxyGatewayStatus>('/api/v2/proxy/start', { method: 'POST', body: '{}' }),
  proxyStop: () => reqJSON<ProxyGatewayStatus>('/api/v2/proxy/stop', { method: 'POST', body: '{}' }),
  proxySync: () => reqJSON<ProxyGatewayStatus>('/api/v2/proxy/sync', { method: 'POST', body: '{}' }),
};

export function openV2WebSocket(onMessage: (frame: { type: string; payload?: unknown; error?: string }) => void): WebSocket {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${window.location.host}/api/v2/ws`);
  ws.onmessage = (ev) => {
    try {
      onMessage(JSON.parse(String(ev.data)));
    } catch {
      /* ignore */
    }
  };
  return ws;
}
