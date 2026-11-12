const path = require('node:path');
const crypto = require('node:crypto');
const {
  app,
  BrowserWindow,
  WebContentsView,
  session,
  ipcMain,
  shell,
  Tray,
  Menu,
  nativeTheme,
  nativeImage,
  Notification,
} = require('electron');
const {
  ConfigStore,
  normalizeServerURL,
  isInsecureURL,
  defaultServerName,
  relayServerKey,
} = require('./store');
const { RelayManager } = require('./relay');
const { LocalServer } = require('./local-server');

const TITLEBAR_HEIGHT = 42;
const RAIL_WIDTH = 62;
const DRAWER_WIDTH = 340;
const GITHUB_RELEASES_API = 'https://api.github.com/repos/llc370219/frog-bot/releases/latest';
const GITHUB_RELEASES_PAGE = 'https://github.com/llc370219/frog-bot/releases/latest';

app.setAppUserModelId('io.github.llc370219.frog');
app.setName('frog');

let store;
let relay;
let localServer;
let mainWindow = null;
let tray = null;
let isQuitting = false;
let modalActive = false;

// serverId -> { view: WebContentsView, session: Session, online: boolean, authRequired: boolean, authenticated: boolean, username: string, version: string, devices: [], bots: [], badge: { unread: number, waiting: boolean, working: boolean }, lastError: string }
const serverRuntime = new Map();

let updateInfo = {
  current: app.getVersion(),
  latest: null,
  available: false,
  url: GITHUB_RELEASES_PAGE,
  notes: '',
  checking: false,
};

function isDarkEffective() {
  const pref = store?.data?.theme || 'system';
  if (pref === 'dark') return true;
  if (pref === 'light') return false;
  return nativeTheme.shouldUseDarkColors;
}

function themeColors() {
  const dark = isDarkEffective();
  return dark
    ? { bg: '#1b1b1b', canvas: '#232323', symbol: '#f2f2f2', overlayBg: '#00000000' }
    : { bg: '#f3f3f3', canvas: '#ffffff', symbol: '#171717', overlayBg: '#00000000' };
}

function getServerPartition(serverId) {
  return `persist:frog-server-${serverId}`;
}

function getServerSession(serverId) {
  return session.fromPartition(getServerPartition(serverId));
}

async function fetchServerJSON(serverId, apiPath, opts = {}) {
  const srv = store.data.servers.find((s) => s.id === serverId);
  if (!srv) throw new Error('找不到这台服务器');
  const ses = getServerSession(serverId);
  const url = `${srv.url}${apiPath}`;
  const ctrl = new AbortController();
  const timeout = setTimeout(() => ctrl.abort(), opts.timeoutMs || 15_000);
  if (opts.signal) {
    opts.signal.addEventListener('abort', () => ctrl.abort(), { once: true });
  }
  try {
    const headers = { Accept: 'application/json', ...(opts.headers || {}) };
    let body;
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(opts.body);
    }
    const resp = await ses.fetch(url, {
      method: opts.method || 'GET',
      headers,
      body,
      credentials: 'include',
      signal: ctrl.signal,
    });
    const text = await resp.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : null;
    } catch {
      data = null;
    }
    if (!resp.ok) {
      const err = new Error(data?.error || describeStatus(resp.status));
      err.status = resp.status;
      throw err;
    }
    if (data && typeof data === 'object' && data.error) {
      const err = new Error(data.error);
      err.status = 400;
      throw err;
    }
    return data;
  } catch (err) {
    if (err?.name === 'AbortError') {
      const e = new Error('连接超时，请检查网络或防火墙');
      e.code = 'ETIMEDOUT';
      throw e;
    }
    if (err?.status) throw err;
    throw new Error(describeNetworkError(err));
  } finally {
    clearTimeout(timeout);
  }
}

function describeStatus(status) {
  switch (status) {
    case 401: return '请先登录或登录已过期';
    case 403: return '服务器拒绝了请求（403）';
    case 404: return '找不到这个接口（404），请确认地址指向 frog';
    case 429: return '尝试太频繁，请稍后再试';
    case 502:
    case 503:
    case 504: return `服务器暂时不可用（${status}）`;
    default: return `请求失败（${status}）`;
  }
}

function describeNetworkError(err) {
  const msg = String(err?.message || err || '');
  if (/ECONNREFUSED|ERR_CONNECTION_REFUSED/i.test(msg)) return '连不上服务器：端口没开或服务没启动';
  if (/ENOTFOUND|ERR_NAME_NOT_RESOLVED/i.test(msg)) return '找不到这个地址，请检查域名或 IP';
  if (/ETIMEDOUT|TIMED_OUT/i.test(msg)) return '连接超时：请检查网络或防火墙';
  if (/CERT|SSL|TLS/i.test(msg)) return 'HTTPS 证书校验未通过，无法建立安全连接';
  return `网络错误：${msg}`;
}

function ensureRuntime(server) {
  let rt = serverRuntime.get(server.id);
  if (!rt) {
    rt = {
      id: server.id,
      view: null,
      online: false,
      authRequired: false,
      authenticated: false,
      username: server.username || '',
      version: '',
      devices: [],
      bots: [],
      badge: { unread: 0, waiting: false, working: false },
      lastError: '',
      currentTitle: server.name,
    };
    serverRuntime.set(server.id, rt);
  }
  return rt;
}

async function probeServer(serverId) {
  const srv = store.data.servers.find((s) => s.id === serverId);
  if (!srv) return null;
  const rt = ensureRuntime(srv);
  try {
    const health = await fetchServerJSON(serverId, '/api/health', { timeoutMs: 8000 });
    rt.online = Boolean(health?.ok);
    rt.version = health?.version || '';
    rt.lastError = '';

    let me = { enabled: false, authenticated: true };
    try {
      me = await fetchServerJSON(serverId, '/api/auth/me', { timeoutMs: 8000 });
    } catch (e) {
      if (e.status !== 404) throw e;
    }
    rt.authRequired = Boolean(me?.enabled);
    rt.authenticated = me?.enabled ? Boolean(me?.authenticated) : true;
    if (me?.username) {
      rt.username = me.username;
      if (srv.username !== me.username) {
        srv.username = me.username;
        store.save();
      }
    }

    if (rt.authenticated) {
      try {
        const devices = await fetchServerJSON(serverId, '/api/devices', { timeoutMs: 10_000 });
        if (Array.isArray(devices)) {
          rt.devices = devices;
          const local = devices.find((d) => d.id === 'local');
          rt.bots = Array.isArray(local?.bots) ? local.bots : [];
        }
      } catch (e) {
        if (e.status === 401) {
          rt.authenticated = false;
        }
      }
    } else {
      rt.devices = [];
      rt.bots = [];
    }
  } catch (err) {
    rt.online = false;
    rt.lastError = err?.message || '无法连接';
  }
  syncRelayRuntime();
  broadcastState();
  return rt;
}

function syncRelayRuntime() {
  if (!relay) return;
  const list = store.data.servers.map((s) => {
    const rt = ensureRuntime(s);
    return {
      id: s.id,
      name: s.name,
      url: s.url,
      online: rt.online && rt.authenticated,
      devices: rt.devices,
      bots: rt.bots,
    };
  });
  relay.apply(list);
}

function parseTitleBadge(title = '') {
  let t = String(title || '');
  let unread = 0;
  const m = t.match(/^\((\d+)\)\s*/);
  if (m) {
    unread = Number(m[1]) || 0;
    t = t.slice(m[0].length);
  }
  let waiting = false;
  let working = false;
  if (t.startsWith('⚠ ')) {
    waiting = true;
    t = t.slice(2);
  } else if (t.startsWith('● ')) {
    working = true;
    t = t.slice(2);
  }
  return { unread, waiting, working, cleanTitle: t.trim() };
}

function syncThemeToServerView(view) {
  if (!view || view.webContents.isDestroyed()) return;
  const pref = store.data.theme || 'system';
  const script = `
    try {
      if (${JSON.stringify(pref)} === 'system') {
        delete document.documentElement.dataset.theme;
        localStorage.removeItem('frogbot-theme');
      } else {
        document.documentElement.dataset.theme = ${JSON.stringify(pref)};
        localStorage.setItem('frogbot-theme', ${JSON.stringify(pref)});
      }
    } catch {}
  `;
  view.webContents.executeJavaScript(script).catch(() => {});
}

function injectDesktopPolishIntoServerView(view) {
  if (!view || view.webContents.isDestroyed()) return;
  const css = `
    ::-webkit-scrollbar { width: 7px; height: 7px; }
    ::-webkit-scrollbar-track { background: transparent; }
    ::-webkit-scrollbar-thumb { background: color-mix(in srgb, var(--ink, #666) 18%, transparent); border-radius: 99px; }
    ::-webkit-scrollbar-thumb:hover { background: color-mix(in srgb, var(--ink, #666) 32%, transparent); }

    /* 桌面端中性灰白配色：覆盖网页端的暖米黄底色，与外壳保持一致。
       叠加 :root 提高优先级，深色规则比浅色规则更高一级。 */
    :root:root:root {
      --canvas:#ffffff; --s1:#ffffff; --s2:#f5f5f5; --s3:#ebebeb;
      --composer:#ffffff; --bubble:#f0f0f0; --code-bg:#f5f5f5;
      --sidebar:#f7f7f7; --sidebar-selected:#ebebeb;
      --fg:#171717; --fg2:#404040; --fg3:#6b6b6b; --fg4:#9a9a9a; --ink:#171717;
      --shadow:rgb(0 0 0/.08);
    }
    :root:root:root[data-theme=dark] {
      --canvas:#232323; --s1:#282828; --s2:#2f2f2f; --s3:#393939;
      --composer:#2f2f2f; --bubble:#1b1b1b; --code-bg:#1b1b1b;
      --sidebar:#1e1e1e; --sidebar-selected:#353535;
      --fg:#f2f2f2; --fg2:#cfcfcf; --fg3:#9f9f9f; --fg4:#8a8a8a; --ink:#f5f5f5;
      --shadow:rgb(0 0 0/.35);
    }
    @media (prefers-color-scheme: dark) {
      :root:root:root:not([data-theme=light]) {
        --canvas:#232323; --s1:#282828; --s2:#2f2f2f; --s3:#393939;
        --composer:#2f2f2f; --bubble:#1b1b1b; --code-bg:#1b1b1b;
        --sidebar:#1e1e1e; --sidebar-selected:#353535;
        --fg:#f2f2f2; --fg2:#cfcfcf; --fg3:#9f9f9f; --fg4:#8a8a8a; --ink:#f5f5f5;
        --shadow:rgb(0 0 0/.35);
      }
    }
  `;
  view.webContents.insertCSS(css).catch(() => {});
  syncThemeToServerView(view);
}

function ensureServerView(server) {
  const rt = ensureRuntime(server);
  if (rt.view && !rt.view.webContents.isDestroyed()) return rt.view;

  const ses = getServerSession(server.id);
  const view = new WebContentsView({
    webPreferences: {
      session: ses,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      spellcheck: false,
    },
  });

  const colors = themeColors();
  view.setBackgroundColor(colors.canvas);
  if (typeof view.setBorderRadius === 'function') {
    try { view.setBorderRadius(12); } catch {}
  }

  view.webContents.setWindowOpenHandler(({ url }) => {
    try {
      const target = new URL(url);
      const base = new URL(server.url);
      if (target.origin === base.origin && target.pathname.includes('/files')) {
        view.webContents.downloadURL(url);
        return { action: 'deny' };
      }
    } catch {}
    shell.openExternal(url).catch(() => {});
    return { action: 'deny' };
  });

  view.webContents.on('did-finish-load', () => {
    injectDesktopPolishIntoServerView(view);
    const currentURL = view.webContents.getURL();
    if (currentURL.includes('/login')) {
      rt.authRequired = true;
      rt.authenticated = false;
      broadcastState();
    } else {
      probeServer(server.id).catch(() => {});
    }
  });

  view.webContents.on('page-title-updated', (_event, title) => {
    const parsed = parseTitleBadge(title);
    const prevWaiting = rt.badge.waiting;
    rt.badge = {
      unread: parsed.unread,
      waiting: parsed.waiting,
      working: parsed.working,
    };
    rt.currentTitle = parsed.cleanTitle || server.name;
    if (!prevWaiting && parsed.waiting && (!mainWindow || !mainWindow.isFocused())) {
      showNativeNotification(
        `${server.name} · 需要批准操作`,
        `${rt.currentTitle} 有一个高风险工具调用等待你确认`,
        server.id
      );
    }
    updateTaskbarBadge();
    broadcastState();
  });

  view.webContents.on('did-fail-load', (_e, errorCode, errorDescription, validatedURL, isMainFrame) => {
    if (!isMainFrame || errorCode === -3) return;
    rt.online = false;
    rt.lastError = errorDescription || `加载失败 (${errorCode})`;
    broadcastState();
  });

  rt.view = view;
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.contentView.addChildView(view);
  }
  view.webContents.loadURL(server.url).catch(() => {});
  return view;
}

function layoutViews() {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  const [contentWidth, contentHeight] = mainWindow.getContentSize();
  const activeId = store.data.activeServerId;
  const activeServer = store.data.servers.find((s) => s.id === activeId);
  const activeRt = activeServer ? serverRuntime.get(activeServer.id) : null;

  // Hide all views when modal is open or when a server is not active/ready
  const leftOffset = RAIL_WIDTH + (store.data.drawerOpen ? DRAWER_WIDTH : 0);
  const margin = 8;
  const x = leftOffset;
  const y = TITLEBAR_HEIGHT;
  const w = Math.max(0, contentWidth - x - margin);
  const h = Math.max(0, contentHeight - y - margin);

  for (const [id, rt] of serverRuntime.entries()) {
    if (!rt.view || rt.view.webContents.isDestroyed()) continue;
    const shouldShow =
      !modalActive &&
      id === activeId &&
      rt.online &&
      rt.authenticated &&
      w > 100 &&
      h > 100;
    if (shouldShow) {
      rt.view.setBounds({ x, y, width: w, height: h });
      rt.view.setVisible(true);
    } else {
      rt.view.setVisible(false);
      rt.view.setBounds({ x: 0, y: 0, width: 0, height: 0 });
    }
  }
}

function showNativeNotification(title, body, serverId) {
  try {
    if (!Notification.isSupported()) return;
    const n = new Notification({
      title,
      body,
      icon: path.join(__dirname, '..', 'build', 'icon.png'),
    });
    n.on('click', () => {
      if (serverId) selectServer(serverId);
      if (mainWindow) {
        if (mainWindow.isMinimized()) mainWindow.restore();
        mainWindow.show();
        mainWindow.focus();
      }
    });
    n.show();
  } catch {}
}

function updateTaskbarBadge() {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  let totalUnread = 0;
  let anyWaiting = false;
  let anyWorking = false;
  for (const s of store.data.servers) {
    const rt = serverRuntime.get(s.id);
    if (!rt) continue;
    totalUnread += rt.badge.unread || 0;
    if (rt.badge.waiting) anyWaiting = true;
    if (rt.badge.working) anyWorking = true;
  }
  const active = store.data.servers.find((s) => s.id === store.data.activeServerId);
  const activeRt = active ? serverRuntime.get(active.id) : null;
  const prefix = anyWaiting ? '⚠ ' : totalUnread > 0 ? `(${totalUnread}) ` : anyWorking ? '● ' : '';
  const suffix = active ? `${activeRt?.currentTitle || active.name} · frog` : 'frog';
  mainWindow.setTitle(`${prefix}${suffix}`);
  if (tray) {
    tray.setToolTip(`${prefix}${suffix}`);
  }
}

function serializeState() {
  const servers = store.data.servers.map((s) => {
    const rt = ensureRuntime(s);
    const relayState = relay ? relay.getState(s.id) : { status: 'off' };
    return {
      id: s.id,
      name: s.name,
      url: s.url,
      username: rt.username || s.username || '',
      insecure: isInsecureURL(s.url),
      online: rt.online,
      authRequired: rt.authRequired,
      authenticated: rt.authenticated,
      version: rt.version,
      lastError: rt.lastError,
      currentTitle: rt.currentTitle || s.name,
      badge: rt.badge,
      bots: rt.bots,
      devices: rt.devices,
      relayKey: relayServerKey(s.id),
      relayOn: relay ? relay.isServerOn(s.id) : true,
      relayState,
      botRelayStates: Object.fromEntries(
        (rt.bots || []).map((b) => [b.id, relay ? relay.isBotOn(s.id, b.id) : true])
      ),
    };
  });

  return {
    version: app.getVersion(),
    theme: store.data.theme,
    dark: isDarkEffective(),
    drawerOpen: store.data.drawerOpen,
    closeToTray: store.data.closeToTray,
    autoCheckUpdates: store.data.autoCheckUpdates,
    activeServerId: store.data.activeServerId,
    servers,
    relay: {
      enabled: store.data.relay.enabled,
      clientId: store.data.relay.clientId,
    },
    update: updateInfo,
    local: localServer ? localServer.snapshot() : null,
  };
}

function broadcastState() {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  layoutViews();
  updateTaskbarBadge();
  updateTrayMenu();
  mainWindow.webContents.send('frog:state', serializeState());
}

function selectServer(serverId) {
  if (!store.data.servers.some((s) => s.id === serverId)) return;
  store.data.activeServerId = serverId;
  store.save();
  const srv = store.data.servers.find((s) => s.id === serverId);
  if (srv) {
    ensureServerView(srv);
    probeServer(srv.id).catch(() => {});
  }
  broadcastState();
}

async function addOrLoginServer({ id, name, url, username, password }) {
  const normalized = normalizeServerURL(url);
  if (!normalized) throw new Error('服务器地址格式不正确');

  let srv = id ? store.data.servers.find((s) => s.id === id) : null;
  const isNew = !srv;
  const serverId = srv ? srv.id : crypto.randomUUID();

  if (!srv) {
    srv = {
      id: serverId,
      name: String(name || '').trim() || defaultServerName(normalized),
      url: normalized,
      username: String(username || '').trim(),
    };
    store.data.servers.push(srv);
  } else {
    srv.name = String(name || '').trim() || defaultServerName(normalized);
    srv.url = normalized;
    if (username !== undefined) srv.username = String(username || '').trim();
  }
  store.data.activeServerId = srv.id;
  store.save();

  try {
    // Check health and auth mode
    const health = await fetchServerJSON(srv.id, '/api/health', { timeoutMs: 10_000 });
    if (!health?.ok) throw new Error('地址没有返回有效的 frog 健康状态');

    let me = { enabled: false, authenticated: true };
    try {
      me = await fetchServerJSON(srv.id, '/api/auth/me', { timeoutMs: 8000 });
    } catch (e) {
      if (e.status !== 404) throw e;
    }

    if (me.enabled && (!me.authenticated || password)) {
      const u = String(username || srv.username || '').trim();
      const p = String(password || '');
      if (!u || !p) {
        await probeServer(srv.id);
        throw new Error('这台服务器需要用户名和密码登录');
      }
      await fetchServerJSON(srv.id, '/api/auth/login', {
        method: 'POST',
        body: { username: u, password: p },
        timeoutMs: 10_000,
      });
      srv.username = u;
      store.save();
    }

    await probeServer(srv.id);
    const view = ensureServerView(srv);
    view.webContents.loadURL(srv.url).catch(() => {});
    broadcastState();
    return serializeState();
  } catch (err) {
    if (isNew && !password) {
      // Still keep the server entry if it's reachable and just needs login, otherwise remove if totally invalid
    }
    await probeServer(srv.id).catch(() => {});
    broadcastState();
    throw err;
  }
}

// ---------- 本机使用：安装并运行本机版 frog ----------

function localURL(port) {
  return `http://127.0.0.1:${port}`;
}

/** 客户端启动时把「本机」服务器对应的 frog 拉起来（已安装才会启动） */
async function startLocalForServer(srv) {
  let port = 8080;
  try {
    port = Number(new URL(srv.url).port) || 8080;
  } catch {}
  if (!localServer.isInstalled() && !(await localServer.health(port))) return;
  try {
    const actual = await localServer.start(port);
    if (actual !== port) {
      srv.url = localURL(actual);
      store.save();
      const rt = serverRuntime.get(srv.id);
      if (rt?.view && !rt.view.webContents.isDestroyed()) rt.view.webContents.loadURL(srv.url).catch(() => {});
    }
  } catch (err) {
    const rt = ensureRuntime(srv);
    rt.lastError = err.message;
  }
  await probeServer(srv.id).catch(() => {});
  broadcastState();
}

async function setupLocalServer() {
  if (localServer.busy) return serializeState();
  localServer.busy = true;
  localServer.setStatus({ phase: 'checking', progress: 0, error: '' });
  try {
    let port = await localServer.findRunning();
    if (!port) {
      if (!localServer.isInstalled()) await localServer.install();
      localServer.setStatus({ phase: 'starting' });
      port = await localServer.start();
    }
    const url = localURL(port);
    let srv = store.data.servers.find((s) => s.url === url);
    if (srv) {
      selectServer(srv.id);
    } else {
      await addOrLoginServer({ name: '本机', url });
      srv = store.data.servers.find((s) => s.id === store.data.activeServerId);
    }
    if (srv && !srv.local) {
      srv.local = true;
      store.save();
    }
    localServer.setStatus({ phase: 'done', progress: 1 });
  } catch (err) {
    localServer.setStatus({ phase: 'error', error: err.message || String(err) });
  } finally {
    localServer.busy = false;
    broadcastState();
  }
  return serializeState();
}

async function removeServer(serverId) {
  const idx = store.data.servers.findIndex((s) => s.id === serverId);
  if (idx === -1) return;
  if (store.data.servers[idx].local) localServer.stop();
  relay?.forget(serverId);
  const rt = serverRuntime.get(serverId);
  if (rt?.view && !rt.view.webContents.isDestroyed()) {
    try {
      mainWindow?.contentView.removeChildView(rt.view);
      rt.view.webContents.close();
    } catch {}
  }
  serverRuntime.delete(serverId);
  try {
    const ses = getServerSession(serverId);
    await ses.clearStorageData();
  } catch {}
  store.data.servers.splice(idx, 1);
  if (store.data.activeServerId === serverId) {
    store.data.activeServerId = store.data.servers[0]?.id || null;
  }
  store.save();
  if (store.data.activeServerId) {
    const next = store.data.servers.find((s) => s.id === store.data.activeServerId);
    if (next) ensureServerView(next);
  }
  syncRelayRuntime();
  broadcastState();
}

function navigateServerAction(serverId, action, botId, deviceId = 'local') {
  const srv = store.data.servers.find((s) => s.id === serverId);
  if (!srv) return;
  selectServer(serverId);
  const view = ensureServerView(srv);
  const u = new URL(srv.url);
  if (action === 'scheduler') {
    u.searchParams.set('mac_action', 'scheduler');
  } else if (action === 'settings') {
    u.searchParams.set('mac_action', 'settings');
  } else if (action === 'bot' && botId) {
    u.searchParams.set('mac_device', deviceId);
    u.searchParams.set('mac_bot', botId);
  }
  view.webContents.loadURL(u.toString()).catch(() => {});
}

async function checkClientUpdates(manual = false) {
  if (updateInfo.checking) return updateInfo;
  updateInfo = { ...updateInfo, checking: true };
  broadcastState();
  try {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 10_000);
    const resp = await fetch(GITHUB_RELEASES_API, {
      headers: { Accept: 'application/vnd.github+json', 'User-Agent': `frog-windows/${app.getVersion()}` },
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (resp.ok) {
      const release = await resp.json();
      const tag = String(release?.tag_name || '').replace(/^v/, '');
      const current = app.getVersion().replace(/^v/, '');
      const available = Boolean(tag && compareSemver(tag, current) > 0);
      const winAsset = Array.isArray(release?.assets)
        ? release.assets.find((a) => a?.name === 'frog-windows-setup.exe')
        : null;
      updateInfo = {
        current: app.getVersion(),
        latest: tag || current,
        available,
        url: winAsset?.browser_download_url || release?.html_url || GITHUB_RELEASES_PAGE,
        notes: String(release?.body || '').slice(0, 600),
        checking: false,
      };
      if (available && manual) {
        showNativeNotification('发现新版本 v' + tag, '点击前往下载最新 Windows 客户端');
      }
    } else {
      updateInfo = { ...updateInfo, checking: false };
    }
  } catch {
    updateInfo = { ...updateInfo, checking: false };
  }
  broadcastState();
  return updateInfo;
}

function compareSemver(a, b) {
  const pa = a.split('-')[0].split('.').map((n) => Number(n) || 0);
  const pb = b.split('-')[0].split('.').map((n) => Number(n) || 0);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0);
  }
  return 0;
}

function applyThemeEverywhere() {
  const pref = store.data.theme || 'system';
  nativeTheme.themeSource = pref;
  const colors = themeColors();
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.setBackgroundColor(colors.bg);
    if (typeof mainWindow.setTitleBarOverlay === 'function') {
      try {
        mainWindow.setTitleBarOverlay({
          color: colors.overlayBg,
          symbolColor: colors.symbol,
          height: TITLEBAR_HEIGHT,
        });
      } catch {}
    }
  }
  for (const rt of serverRuntime.values()) {
    if (rt.view && !rt.view.webContents.isDestroyed()) {
      rt.view.setBackgroundColor(colors.canvas);
      syncThemeToServerView(rt.view);
    }
  }
  broadcastState();
}

function createTray() {
  if (tray) return;
  try {
    const iconPath = path.join(__dirname, '..', 'build', 'tray.png');
    const img = nativeImage.createFromPath(iconPath);
    tray = new Tray(img);
    tray.setToolTip('frog');
    tray.on('click', () => {
      if (!mainWindow) return;
      if (mainWindow.isVisible()) {
        if (mainWindow.isMinimized()) mainWindow.restore();
        mainWindow.focus();
      } else {
        mainWindow.show();
        mainWindow.focus();
      }
    });
    updateTrayMenu();
  } catch {}
}

function updateTrayMenu() {
  if (!tray) return;
  const serverItems = store.data.servers.map((s) => {
    const rt = serverRuntime.get(s.id);
    const status = rt?.online ? (rt.authenticated ? '在线' : '待登录') : '离线';
    return {
      label: `${s.name} (${status})`,
      type: 'radio',
      checked: s.id === store.data.activeServerId,
      click: () => {
        selectServer(s.id);
        mainWindow?.show();
        mainWindow?.focus();
      },
    };
  });

  const menu = Menu.buildFromTemplate([
    {
      label: '打开 frog 主窗口',
      click: () => {
        mainWindow?.show();
        mainWindow?.focus();
      },
    },
    ...(serverItems.length ? [{ type: 'separator' }, ...serverItems] : []),
    { type: 'separator' },
    {
      label: `Bot 互联：${store.data.relay.enabled ? '已开启' : '已关闭'}`,
      type: 'checkbox',
      checked: Boolean(store.data.relay.enabled),
      click: () => relay?.setEnabled(!store.data.relay.enabled),
    },
    {
      label: '检查客户端更新…',
      click: () => {
        mainWindow?.show();
        checkClientUpdates(true);
      },
    },
    { type: 'separator' },
    {
      label: '退出 frog',
      click: () => {
        isQuitting = true;
        app.quit();
      },
    },
  ]);
  tray.setContextMenu(menu);
}

function createMainWindow() {
  const colors = themeColors();
  const bounds = store.data.windowBounds || { width: 1260, height: 840 };

  mainWindow = new BrowserWindow({
    width: Math.max(960, bounds.width || 1260),
    height: Math.max(640, bounds.height || 840),
    minWidth: 880,
    minHeight: 600,
    title: 'frog',
    icon: path.join(__dirname, '..', 'build', 'icon.ico'),
    backgroundColor: colors.bg,
    backgroundMaterial: 'mica',
    titleBarStyle: 'hidden',
    titleBarOverlay: {
      color: colors.overlayBg,
      symbolColor: colors.symbol,
      height: TITLEBAR_HEIGHT,
    },
    autoHideMenuBar: true,
    show: false,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
      spellcheck: false,
    },
  });

  Menu.setApplicationMenu(null);

  mainWindow.once('ready-to-show', () => {
    mainWindow.show();
    if (store.data.activeServerId) {
      const active = store.data.servers.find((s) => s.id === store.data.activeServerId);
      if (active) ensureServerView(active);
    }
    layoutViews();
    broadcastState();
  });

  const saveBounds = () => {
    if (!mainWindow || mainWindow.isDestroyed() || mainWindow.isMaximized() || mainWindow.isMinimized()) return;
    store.data.windowBounds = mainWindow.getBounds();
    store.save();
  };

  mainWindow.on('resize', () => {
    layoutViews();
    saveBounds();
  });
  mainWindow.on('move', saveBounds);

  mainWindow.on('close', (e) => {
    if (!isQuitting && store.data.closeToTray) {
      e.preventDefault();
      mainWindow.hide();
    }
  });

  mainWindow.loadFile(path.join(__dirname, 'renderer', 'index.html'));
}

function registerIPC() {
  ipcMain.handle('frog:get-state', () => serializeState());
  ipcMain.handle('frog:set-modal', (_e, open) => {
    modalActive = Boolean(open);
    layoutViews();
    return true;
  });
  ipcMain.handle('frog:set-drawer', (_e, open) => {
    store.data.drawerOpen = Boolean(open);
    store.save();
    broadcastState();
    return serializeState();
  });
  ipcMain.handle('frog:set-theme', (_e, theme) => {
    store.data.theme = ['system', 'light', 'dark'].includes(theme) ? theme : 'system';
    store.save();
    applyThemeEverywhere();
    return serializeState();
  });
  ipcMain.handle('frog:set-pref', (_e, { closeToTray, autoCheckUpdates }) => {
    if (closeToTray !== undefined) store.data.closeToTray = Boolean(closeToTray);
    if (autoCheckUpdates !== undefined) store.data.autoCheckUpdates = Boolean(autoCheckUpdates);
    store.save();
    broadcastState();
    return serializeState();
  });
  ipcMain.handle('frog:select-server', (_e, serverId) => {
    selectServer(serverId);
    return serializeState();
  });
  ipcMain.handle('frog:save-server', (_e, payload) => addOrLoginServer(payload));
  ipcMain.handle('frog:remove-server', async (_e, serverId) => {
    await removeServer(serverId);
    return serializeState();
  });
  ipcMain.handle('frog:reload-server', async (_e, serverId) => {
    const id = serverId || store.data.activeServerId;
    const srv = store.data.servers.find((s) => s.id === id);
    if (srv) {
      await probeServer(srv.id);
      const view = ensureServerView(srv);
      view.webContents.reload();
    }
    return serializeState();
  });
  ipcMain.handle('frog:logout-server', async (_e, serverId) => {
    const srv = store.data.servers.find((s) => s.id === serverId);
    if (srv) {
      await fetchServerJSON(srv.id, '/api/auth/logout', { method: 'POST' }).catch(() => {});
      const ses = getServerSession(srv.id);
      await ses.clearStorageData({ storages: ['cookies'] }).catch(() => {});
      await probeServer(srv.id);
    }
    return serializeState();
  });
  ipcMain.handle('frog:nav-server', (_e, { serverId, action, botId, deviceId }) => {
    navigateServerAction(serverId || store.data.activeServerId, action, botId, deviceId);
    return true;
  });
  ipcMain.handle('frog:relay-toggle', (_e, { enabled, serverId, botId, on }) => {
    if (enabled !== undefined) relay.setEnabled(enabled);
    else if (serverId && botId) relay.setBotOn(serverId, botId, on);
    else if (serverId) relay.setServerOn(serverId, on);
    syncRelayRuntime();
    broadcastState();
    return serializeState();
  });
  ipcMain.handle('frog:local-setup', () => setupLocalServer());
  ipcMain.handle('frog:check-updates', () => checkClientUpdates(true));
  ipcMain.handle('frog:open-external', (_e, url) => {
    if (typeof url === 'string' && /^https?:\/\//i.test(url)) {
      shell.openExternal(url).catch(() => {});
    }
    return true;
  });
}

const gotSingleInstance = app.requestSingleInstanceLock();
if (!gotSingleInstance) {
  app.quit();
} else {
  app.on('second-instance', () => {
    if (mainWindow) {
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.show();
      mainWindow.focus();
    }
  });

  app.whenReady().then(async () => {
    store = new ConfigStore();
    nativeTheme.themeSource = store.data.theme || 'system';
    relay = new RelayManager(store, fetchServerJSON, () => broadcastState());
    localServer = new LocalServer(() => broadcastState());

    registerIPC();
    createTray();
    createMainWindow();

    nativeTheme.on('updated', () => {
      if ((store.data.theme || 'system') === 'system') {
        applyThemeEverywhere();
      }
    });

    // Probe all configured servers on startup and every 15s
    const probeAll = () => {
      for (const s of store.data.servers) {
        probeServer(s.id).catch(() => {});
      }
    };
    probeAll();
    setInterval(probeAll, 15_000);
    for (const s of store.data.servers) {
      if (s.local) startLocalForServer(s).catch(() => {});
    }

    if (store.data.autoCheckUpdates) {
      setTimeout(() => checkClientUpdates(false), 4000);
    }

    // Headless smoke-test hook for CI / local verification
    if (process.env.FROG_SMOKE_TEST === '1') {
      const testUrl = process.env.FROG_SMOKE_URL;
      const testUrl2 = process.env.FROG_SMOKE_URL2;
      const testUser = process.env.FROG_SMOKE_USER;
      const testPass = process.env.FROG_SMOKE_PASS;
      const shotPath = process.env.FROG_SMOKE_SHOT;
      try {
        if (testUrl) {
          await addOrLoginServer({
            name: '主力云主机',
            url: testUrl,
            username: testUser,
            password: testPass,
          });
        }
        if (testUrl2) {
          await addOrLoginServer({
            name: '家里 NAS',
            url: testUrl2,
            username: testUser,
            password: testPass,
          });
          relay.setEnabled(true);
          store.data.drawerOpen = true;
          syncRelayRuntime();
          broadcastState();
        }
        setTimeout(async () => {
          if (shotPath && mainWindow && !mainWindow.isDestroyed()) {
            const img = await mainWindow.capturePage();
            require('node:fs').writeFileSync(shotPath, img.toPNG());
            const activeRt = serverRuntime.get(store.data.activeServerId);
            if (activeRt?.view && !activeRt.view.webContents.isDestroyed()) {
              const vImg = await activeRt.view.webContents.capturePage();
              require('node:fs').writeFileSync(shotPath + '.view.png', vImg.toPNG());
            }
          }
          const st = serializeState();
          console.log('FROG_SMOKE_STATE=' + JSON.stringify({
            version: st.version,
            serversCount: st.servers.length,
            servers: st.servers.map((s) => ({
              name: s.name,
              online: s.online,
              auth: s.authenticated,
              bots: s.bots?.length || 0,
              relay: s.relayState?.status,
            })),
          }));
          isQuitting = true;
          await relay.shutdown();
          app.quit();
        }, 3500);
      } catch (err) {
        console.error('FROG_SMOKE_ERROR=', err);
        isQuitting = true;
        app.exit(1);
      }
    }
  });

  // 本机 frog 随客户端退出
  process.on('exit', () => localServer?.stop());
  app.on('before-quit', async (e) => {
    localServer?.stop();
    if (!isQuitting) {
      isQuitting = true;
      e.preventDefault();
      await relay?.shutdown().catch(() => {});
      app.exit(0);
    }
  });
}
