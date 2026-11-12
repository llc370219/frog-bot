/* global frogDesktop */

let state = null;
let activeDrawerTab = 'servers';

const el = (id) => document.getElementById(id);

const DRAWER_TITLES = { servers: '服务器', relay: 'Bot 互联', client: '通用设置' };

// ---------- 小工具 ----------

function escapeHTML(str = '') {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function serverInitials(name = '') {
  const s = String(name || '').trim();
  if (!s) return '🐸';
  if (/^[\u4e00-\u9fa5]/.test(s)) return s.slice(0, 2);
  const words = s.split(/[\s._-]+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
  return s.slice(0, 2).toUpperCase();
}

function avatarHTML(s) {
  return `<span class="rail-avatar">${escapeHTML(serverInitials(s.name))}</span>`;
}

function serverStatus(s) {
  if (!s.online) return { cls: 'offline', tag: 'danger', text: '离线' };
  if (!s.authenticated) return { cls: 'warn', tag: 'warn', text: '待登录' };
  if (s.badge?.waiting) return { cls: 'warn', tag: 'warn', text: '待审批' };
  return { cls: 'online', tag: 'ok', text: '在线' };
}

const ICONS = {
  sun: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>',
  moon: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5Z"/></svg>',
  system: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="4" width="20" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></svg>',
  warn: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 9v4M12 17h.01"/><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/></svg>',
  error: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/></svg>',
  edit: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>',
  gear: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h10M18 7h2M4 17h4M12 17h8"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="17" r="2"/></svg>',
  clock: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
  trash: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg>',
  plus: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
  link: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8.5 6h7M7.2 8.2l3.6 7.6M16.8 8.2l-3.6 7.6"/></svg>',
  bot: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="8" width="16" height="12" rx="3"/><path d="M12 4v4M9 13h.01M15 13h.01"/></svg>',
  tray: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M3 15h5l1.5 2.5h5L16 15h5"/><path d="M5.5 5h13L21 15v4a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-4Z"/></svg>',
  update: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12M7 10l5 5 5-5M5 21h14"/></svg>',
  refresh: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 0 0-15.5-6.2L3 8M3 3v5h5M3 12a9 9 0 0 0 15.5 6.2L21 16M16 16h5v5"/></svg>',
  server: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/></svg>',
  shield: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 4 6v6c0 4.5 3.4 8.3 8 9 4.6-.7 8-4.5 8-9V6Z"/><path d="m9 12 2 2 4-4"/></svg>',
  bell: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.9 1.9 0 0 0 3.4 0"/></svg>',
  lock: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/></svg>',
  offline: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>',
};

// ---------- 确认对话框（替代 window.confirm，保持外观统一） ----------

let confirmResolver = null;

function askConfirm({ title, body, okText = '确定', danger = true }) {
  return new Promise((resolve) => {
    confirmResolver = resolve;
    el('confirm-title').textContent = title;
    el('confirm-body').textContent = body;
    el('btn-confirm-ok').textContent = okText;
    el('btn-confirm-ok').className = `btn ${danger ? 'btn-danger' : 'btn-primary'}`;
    frogDesktop.setModal(true);
    el('confirm-backdrop').classList.remove('hidden');
    setTimeout(() => el('btn-confirm-cancel').focus(), 30);
  });
}

function closeConfirm(result) {
  if (el('confirm-backdrop').classList.contains('hidden')) return;
  el('confirm-backdrop').classList.add('hidden');
  if (el('modal-backdrop').classList.contains('hidden')) frogDesktop.setModal(false);
  const r = confirmResolver;
  confirmResolver = null;
  if (r) r(result);
}

// ---------- 渲染 ----------

function applyRootTheme(st) {
  if (!st) return;
  document.documentElement.dataset.theme = st.dark ? 'dark' : 'light';
  const themeBtn = el('btn-theme');
  const labels = {
    system: '外观：跟随系统（点击切换为浅色）',
    light: '外观：浅色（点击切换为深色）',
    dark: '外观：深色（点击切换为跟随系统）',
  };
  const mode = labels[st.theme] ? st.theme : 'system';
  themeBtn.title = labels[mode];
  themeBtn.innerHTML = mode === 'light' ? ICONS.sun : mode === 'dark' ? ICONS.moon : ICONS.system;
}

function renderTitlebar(st) {
  const active = st.servers.find((s) => s.id === st.activeServerId);
  const ctx = el('titlebar-context');
  if (!active) {
    ctx.innerHTML = `<span class="tb-ctx-dot"></span><span class="tb-ctx-name">未连接服务器</span>`;
  } else {
    const status = serverStatus(active);
    const title =
      active.online && active.authenticated && active.currentTitle && active.currentTitle !== active.name
        ? active.currentTitle
        : status.text;
    ctx.innerHTML = `
      <span class="tb-ctx-dot ${status.cls}"></span>
      <span class="tb-ctx-name">${escapeHTML(active.name)}</span>
      <span class="tb-ctx-title">${escapeHTML(title)}</span>
    `;
  }

  const dot = el('relay-pill-dot');
  const label = el('relay-pill-label');
  dot.className = 'relay-dot';
  if (st.relay?.enabled) {
    const anyActive = st.servers.some((s) => s.relayState?.status === 'active');
    dot.classList.add(anyActive ? 'active' : 'waiting');
    label.textContent = anyActive ? 'Bot 互联已连通' : 'Bot 互联待命';
  } else {
    label.textContent = 'Bot 互联';
  }

  const upPill = el('btn-update-pill');
  if (st.update?.available) {
    upPill.classList.remove('hidden');
    el('update-pill-text').textContent = `更新到 v${st.update.latest}`;
  } else {
    upPill.classList.add('hidden');
  }
}

function renderRail(st) {
  const box = el('rail-servers');
  box.innerHTML = st.servers
    .map((s, i) => {
      const isActive = s.id === st.activeServerId;
      const status = serverStatus(s);
      const unread = s.badge?.unread || 0;
      const hotkey = i < 9 ? ` · Ctrl+${i + 1}` : '';
      return `
        <button class="rail-item ${isActive ? 'active' : ''}" data-server-id="${escapeHTML(s.id)}"
          title="${escapeHTML(s.name)}（${status.text}）&#10;${escapeHTML(s.url)}${hotkey}">
          <span class="rail-indicator"></span>
          ${avatarHTML(s)}
          <span class="rail-status ${status.cls}"></span>
          ${unread > 0 ? `<span class="rail-unread">${unread > 99 ? '99+' : unread}</span>` : ''}
        </button>
      `;
    })
    .join('');

  box.querySelectorAll('.rail-item').forEach((btn) => {
    btn.addEventListener('click', () => {
      const id = btn.getAttribute('data-server-id');
      if (id === st.activeServerId && !st.drawerOpen) {
        // 再次点击当前服务器可展开服务器管理抽屉
        activeDrawerTab = 'servers';
        frogDesktop.setDrawer(true);
      } else {
        frogDesktop.selectServer(id);
      }
    });
  });

  el('btn-drawer-servers').classList.toggle('active', st.drawerOpen && activeDrawerTab === 'servers');
  el('btn-drawer-relay').classList.toggle('active', st.drawerOpen && activeDrawerTab === 'relay');
  el('btn-drawer-settings').classList.toggle('active', st.drawerOpen && activeDrawerTab === 'client');
}

function describeRelayState(rs) {
  switch (rs?.status) {
    case 'active': return { text: '已接通', cls: 'ok' };
    case 'connecting': return { text: '正在同步', cls: 'warn' };
    case 'waiting': return { text: '等待其它服务器', cls: 'warn' };
    case 'unsupported': return { text: '后端不支持', cls: 'danger' };
    case 'failed': return { text: rs.error || '同步失败', cls: 'danger' };
    default: return { text: '未开启', cls: '' };
  }
}

function switchHTML(attrs, checked, disabled = false) {
  return `
    <label class="switch">
      <input type="checkbox" ${attrs} ${checked ? 'checked' : ''} ${disabled ? 'disabled' : ''} />
      <span class="slider"></span>
    </label>
  `;
}

function renderServersTab(st, body) {
  const cards = st.servers
    .map((s) => {
      const isCurrent = s.id === st.activeServerId;
      const status = serverStatus(s);
      const botCount = s.bots?.length || 0;
      const tagText = status.cls === 'online' ? `在线 · ${botCount} 个 Bot` : status.text;
      return `
        <div class="card srv-card ${isCurrent ? 'current' : ''}">
          <div class="srv-head">
            ${avatarHTML(s)}
            <div class="srv-meta">
              <div class="srv-name">
                <span>${escapeHTML(s.name)}</span>
                ${isCurrent ? '<span class="tag plain ok">当前</span>' : ''}
              </div>
              <div class="srv-url" title="${escapeHTML(s.url)}">${escapeHTML(s.url.replace(/^https?:\/\//, ''))}${s.version ? ` · ${escapeHTML(s.version)}` : ''}</div>
            </div>
            <span class="tag ${status.tag}">${escapeHTML(tagText)}</span>
          </div>
          ${s.insecure ? `<div class="srv-note warn">${ICONS.warn}<span>HTTP 明文连接，密码与对话不加密</span></div>` : ''}
          ${s.lastError ? `<div class="srv-note danger">${ICONS.error}<span>${escapeHTML(s.lastError)}</span></div>` : ''}
          <div class="srv-actions">
            ${
              isCurrent
                ? `<button class="btn btn-sm btn-secondary" data-act="edit" data-id="${escapeHTML(s.id)}">${s.authenticated ? '编辑' : '登录'}</button>`
                : `<button class="btn btn-sm btn-primary" data-act="switch" data-id="${escapeHTML(s.id)}">切换</button>`
            }
            <span class="grow"></span>
            ${!isCurrent ? `<button class="icon-btn" title="编辑 / 登录" data-act="edit" data-id="${escapeHTML(s.id)}">${ICONS.edit}</button>` : ''}
            <button class="icon-btn" title="服务端设置" data-act="settings" data-id="${escapeHTML(s.id)}">${ICONS.gear}</button>
            <button class="icon-btn" title="定时计划" data-act="scheduler" data-id="${escapeHTML(s.id)}">${ICONS.clock}</button>
            <button class="icon-btn danger" title="从客户端移除" data-act="remove" data-id="${escapeHTML(s.id)}">${ICONS.trash}</button>
          </div>
        </div>
      `;
    })
    .join('');

  body.innerHTML = `
    <div class="section-label">
      <span>已连接 ${st.servers.length} 台服务器</span>
      <button class="btn btn-sm btn-ghost" id="drawer-add-srv">${ICONS.plus}添加</button>
    </div>
    ${
      st.servers.length === 0
        ? `<div class="empty-card">还没有添加服务器。<br/>连接一台运行 frog 的 Linux 服务器即可开始。<br/><button class="btn btn-sm btn-primary" id="drawer-add-srv-2">${ICONS.plus}添加服务器</button></div>`
        : cards
    }
  `;

  el('drawer-add-srv')?.addEventListener('click', () => openServerModal());
  el('drawer-add-srv-2')?.addEventListener('click', () => openServerModal());
  body.querySelectorAll('[data-act]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const act = btn.getAttribute('data-act');
      const id = btn.getAttribute('data-id');
      const srv = st.servers.find((x) => x.id === id);
      if (!srv) return;
      if (act === 'switch') {
        await frogDesktop.selectServer(id);
      } else if (act === 'edit') {
        openServerModal(srv);
      } else if (act === 'settings' || act === 'scheduler') {
        await frogDesktop.setDrawer(false);
        await frogDesktop.navServer({ serverId: id, action: act });
      } else if (act === 'remove') {
        const ok = await askConfirm({
          title: `移除「${srv.name}」？`,
          body: '只会从本客户端移除这台服务器和它的登录会话，不会删除服务器上的任何数据。',
          okText: '移除',
        });
        if (ok) await frogDesktop.removeServer(id);
      }
    });
  });
}

function renderRelayTab(st, body) {
  const enabled = !!st.relay?.enabled;
  const serverCards = st.servers
    .map((s) => {
      const rs = describeRelayState(s.relayState);
      const bots = Array.isArray(s.bots) ? s.bots : [];
      const botRows = bots.length
        ? bots
            .map(
              (b) => `
            <div class="list-row sub">
              <div class="list-text">
                <div class="list-title">${escapeHTML(b.name)}</div>
                <div class="list-desc"><span class="mono">${escapeHTML(b.id)}</span></div>
              </div>
              ${switchHTML(
                `data-relay-bot="${escapeHTML(b.id)}" data-server="${escapeHTML(s.id)}"`,
                s.botRelayStates?.[b.id] !== false,
                !enabled || !s.relayOn
              )}
            </div>`
            )
            .join('')
        : `<div class="list-row sub"><div class="list-desc">登录后可选择这台服务器上参与互联的 Bot</div></div>`;
      return `
        <div class="card list">
          <div class="list-row">
            ${avatarHTML(s).replace('rail-avatar', 'rail-avatar list-avatar')}
            <div class="list-text">
              <div class="list-title strong">${escapeHTML(s.name)}</div>
              <div class="list-desc"><span class="tag ${rs.cls}">${escapeHTML(rs.text)}</span></div>
            </div>
            ${switchHTML(`data-relay-server="${escapeHTML(s.id)}"`, s.relayOn, !enabled)}
          </div>
          <div class="list-row sub" style="min-height:0;padding-top:8px;padding-bottom:8px">
            <div class="list-desc">代号 <span class="mono">relay:${escapeHTML(s.relayKey)}/&lt;BotID&gt;</span></div>
          </div>
          ${botRows}
        </div>
      `;
    })
    .join('');

  body.innerHTML = `
    <div class="card list">
      <div class="list-row">
        <span class="list-icon ${enabled ? 'accent' : ''}">${ICONS.link}</span>
        <div class="list-text">
          <div class="list-title strong">客户端 Bot 互联</div>
          <div class="list-desc">让不同服务器上的 Bot 互相发消息、提问和托付任务</div>
        </div>
        ${switchHTML('id="chk-relay-master"', enabled)}
      </div>
      <div class="list-row" style="min-height:0">
        <div class="list-desc">
          服务器之间不直接建连，消息只在本客户端运行期间转交。<br/>
          客户端标识 <span class="mono">${escapeHTML(st.relay?.clientId || '')}</span>
        </div>
      </div>
    </div>
    ${st.servers.length ? `<div class="section-label"><span>参与互联的服务器</span></div>${serverCards}` : `<div class="empty-card">添加至少两台服务器后即可使用 Bot 互联。</div>`}
  `;

  el('chk-relay-master')?.addEventListener('change', (e) => {
    frogDesktop.toggleRelay({ enabled: e.target.checked });
  });
  body.querySelectorAll('[data-relay-server]').forEach((inp) => {
    inp.addEventListener('change', () => {
      frogDesktop.toggleRelay({ serverId: inp.getAttribute('data-relay-server'), on: inp.checked });
    });
  });
  body.querySelectorAll('[data-relay-bot]').forEach((inp) => {
    inp.addEventListener('change', () => {
      frogDesktop.toggleRelay({
        serverId: inp.getAttribute('data-server'),
        botId: inp.getAttribute('data-relay-bot'),
        on: inp.checked,
      });
    });
  });
}

function renderClientTab(st, body) {
  const themeOpt = (key, label) => `
    <button class="theme-opt ${st.theme === key ? 'active' : ''}" data-theme-pick="${key}">
      <span class="theme-swatch ${key}"></span>${label}
    </button>`;

  const updateDesc = st.update?.checking
    ? '正在检查 GitHub Releases…'
    : st.update?.available
    ? `发现新版本 v${escapeHTML(st.update.latest)}`
    : st.update?.latest
    ? '已是最新版本'
    : '从 GitHub Releases 获取正式版更新';

  body.innerHTML = `
    <div class="section-label"><span>外观</span></div>
    <div class="card">
      <div class="theme-picker">
        ${themeOpt('system', '跟随系统')}
        ${themeOpt('light', '浅色')}
        ${themeOpt('dark', '深色')}
      </div>
    </div>

    <div class="section-label"><span>窗口与更新</span></div>
    <div class="card list">
      <div class="list-row">
        <span class="list-icon">${ICONS.tray}</span>
        <div class="list-text">
          <div class="list-title">关闭时最小化到托盘</div>
          <div class="list-desc">后台继续接收审批提醒，维持 Bot 互联</div>
        </div>
        ${switchHTML('id="chk-close-tray"', st.closeToTray)}
      </div>
      <div class="list-row">
        <span class="list-icon ${st.update?.available ? 'accent' : ''}">${ICONS.update}</span>
        <div class="list-text">
          <div class="list-title">版本 v${escapeHTML(st.version)}</div>
          <div class="list-desc">${updateDesc}</div>
        </div>
        <button class="btn btn-sm btn-secondary" id="btn-manual-check" ${st.update?.checking ? 'disabled' : ''}>${ICONS.refresh}检查</button>
      </div>
      ${
        st.update?.available
          ? `<div class="list-row" style="min-height:0"><button class="btn btn-primary btn-block" id="btn-download-update">${ICONS.update}下载 v${escapeHTML(st.update.latest)}</button></div>`
          : ''
      }
      <div class="list-row">
        <div class="list-text"><div class="list-title">启动时自动检查更新</div></div>
        ${switchHTML('id="chk-auto-update"', st.autoCheckUpdates)}
      </div>
    </div>

    <div class="section-label"><span>快捷键</span></div>
    <div class="card list">
      <div class="kbd-row"><span>聚焦聊天输入框</span><span class="kbd-keys"><kbd>Ctrl</kbd><kbd>K</kbd></span></div>
      <div class="kbd-row"><span>刷新当前服务器</span><span class="kbd-keys"><kbd>Ctrl</kbd><kbd>R</kbd></span></div>
      <div class="kbd-row"><span>切换到第 N 台服务器</span><span class="kbd-keys"><kbd>Ctrl</kbd><kbd>1–9</kbd></span></div>
      <div class="kbd-row"><span>停止生成回复</span><span class="kbd-keys"><kbd>Esc</kbd></span></div>
    </div>
  `;

  body.querySelectorAll('[data-theme-pick]').forEach((btn) => {
    btn.addEventListener('click', () => frogDesktop.setTheme(btn.getAttribute('data-theme-pick')));
  });
  el('chk-close-tray')?.addEventListener('change', (e) => {
    frogDesktop.setPref({ closeToTray: e.target.checked });
  });
  el('chk-auto-update')?.addEventListener('change', (e) => {
    frogDesktop.setPref({ autoCheckUpdates: e.target.checked });
  });
  el('btn-manual-check')?.addEventListener('click', () => frogDesktop.checkUpdates());
  el('btn-download-update')?.addEventListener('click', () => {
    if (st.update?.url) frogDesktop.openExternal(st.update.url);
  });
}

function renderDrawer(st) {
  const drawer = el('drawer');
  drawer.classList.toggle('hidden', !st.drawerOpen);
  if (!st.drawerOpen) return;

  el('drawer-title').textContent = DRAWER_TITLES[activeDrawerTab] || '服务器';
  drawer.querySelectorAll('.seg-item').forEach((t) => {
    t.classList.toggle('active', t.getAttribute('data-tab') === activeDrawerTab);
  });

  const body = el('drawer-body');
  const scroll = body.scrollTop;
  if (activeDrawerTab === 'servers') renderServersTab(st, body);
  else if (activeDrawerTab === 'relay') renderRelayTab(st, body);
  else renderClientTab(st, body);
  body.scrollTop = scroll; // 状态推送频繁，避免滚动位置跳回顶部
}

function renderStage(st) {
  const box = el('stage-content');
  const active = st.servers.find((s) => s.id === st.activeServerId);

  if (!active) {
    box.innerHTML = `
      <div class="hero">
        <div class="hero-mark"><img src="./frog.png" alt="frog" /></div>
        <div class="hero-eyebrow">FROG FOR WINDOWS</div>
        <h1 class="hero-title">连接你的第一台 frog 服务器</h1>
        <p class="hero-desc">自托管的多 Bot AI 助理。在这里同时管理多台服务器，聊天、派发后台任务、审批高危命令。</p>
        <div class="hero-actions">
          <button class="btn btn-primary btn-lg" id="hero-add-btn">${ICONS.plus}添加服务器</button>
          <button class="btn btn-secondary btn-lg" id="hero-doc-btn">部署文档</button>
        </div>
        <div class="feature-grid">
          <div class="feature"><span class="feature-icon">${ICONS.server}</span><div><b>多服务器</b><span>左侧栏一键切换，各自保存 7 天登录会话。</span></div></div>
          <div class="feature"><span class="feature-icon">${ICONS.link}</span><div><b>Bot 互联</b><span>不同服务器上的 Bot 互相提问、协作完成跨机任务。</span></div></div>
          <div class="feature"><span class="feature-icon">${ICONS.bell}</span><div><b>实时通知</b><span>驻留托盘，有待批准命令或新消息时弹出 Windows 通知。</span></div></div>
          <div class="feature"><span class="feature-icon">${ICONS.shield}</span><div><b>密钥不落地</b><span>模型 API Key 与 CLI 登录只保存在服务端。</span></div></div>
        </div>
      </div>
      <div class="local-entry">
        <span id="local-status" class="local-status"></span>
        <button type="button" class="link-btn" id="hero-local-btn">本机使用</button>
      </div>
    `;
    el('hero-local-btn')?.addEventListener('click', startLocalSetup);
    renderLocalStatus(st);
    el('hero-add-btn')?.addEventListener('click', () => openServerModal());
    el('hero-doc-btn')?.addEventListener('click', () =>
      frogDesktop.openExternal('https://github.com/llc370219/frog-bot#readme')
    );
    return;
  }

  if (!active.online) {
    box.innerHTML = `
      <div class="hero">
        <div class="hero-mark"><img src="./frog.png" alt="frog" /><span class="hero-badge danger">${ICONS.offline}</span></div>
        <h1 class="hero-title" style="margin-top:24px">连不上「${escapeHTML(active.name)}」</h1>
        <p class="hero-desc">${escapeHTML(active.lastError || '请确认服务器正在运行、端口已放行，并检查网络连接。')}</p>
        <div class="hero-url">${escapeHTML(active.url)}</div>
        <div class="hero-actions">
          <button class="btn btn-primary btn-lg" id="stage-retry-btn">${ICONS.refresh}重新连接</button>
          <button class="btn btn-secondary btn-lg" id="stage-edit-btn">修改地址</button>
        </div>
      </div>
    `;
    el('stage-retry-btn')?.addEventListener('click', () => frogDesktop.reloadServer(active.id));
    el('stage-edit-btn')?.addEventListener('click', () => openServerModal(active));
    return;
  }

  if (!active.authenticated) {
    box.innerHTML = `
      <div class="hero">
        <div class="hero-mark"><img src="./frog.png" alt="frog" /><span class="hero-badge warn">${ICONS.lock}</span></div>
        <h1 class="hero-title" style="margin-top:24px">登录「${escapeHTML(active.name)}」</h1>
        <p class="hero-desc">这台服务器开启了登录保护，或上次的 7 天会话已过期。</p>
        <div class="hero-url">${escapeHTML(active.url)}</div>
        <div class="hero-actions">
          <button class="btn btn-primary btn-lg" id="stage-login-btn">输入密码登录</button>
        </div>
      </div>
    `;
    el('stage-login-btn')?.addEventListener('click', () => openServerModal(active));
    return;
  }

  // 在线且已登录：WebContentsView 覆盖此区域，加载期间显示占位
  box.innerHTML = `<div class="loading"><div class="spinner"></div>正在打开「${escapeHTML(active.name)}」…</div>`;
}

// ---------- 本机使用 ----------

const LOCAL_PHASES = {
  checking: '正在检查…',
  downloading: '正在下载 frog',
  installing: '正在安装…',
  starting: '正在启动本机 frog…',
};

function renderLocalStatus(st) {
  const box = el('local-status');
  const btn = el('hero-local-btn');
  if (!box || !btn) return;
  const lc = st?.local;
  const busy = Boolean(lc?.busy);
  btn.disabled = busy;
  btn.classList.toggle('hidden', busy);
  box.classList.remove('error');
  if (busy) {
    let text = LOCAL_PHASES[lc.phase] || '正在准备…';
    if (lc.phase === 'downloading' && lc.progress > 0) text += ` ${Math.round(lc.progress * 100)}%`;
    box.innerHTML = `<span class="spinner spinner-sm"></span>${escapeHTML(text)}`;
  } else if (lc?.phase === 'error' && lc.error) {
    box.classList.add('error');
    box.textContent = lc.error;
  } else {
    box.textContent = '';
  }
}

async function startLocalSetup() {
  if (state?.local?.busy) return;
  const installed = Boolean(state?.local?.installed);
  const ok = await askConfirm({
    title: '在这台电脑上使用 frog？',
    body: installed
      ? '将在后台启动已安装的本机 frog（只监听 127.0.0.1，不需要登录），并添加为服务器。它会随客户端一起启动和退出。'
      : '将从 GitHub 下载最新的 frog Windows 版（约 6 MB，校验 SHA256），安装到客户端数据目录，在后台以本机模式运行（只监听 127.0.0.1，不需要登录），并自动添加为服务器。它会随客户端一起启动和退出。',
    okText: installed ? '启动' : '下载并安装',
    danger: false,
  });
  if (!ok) return;
  await frogDesktop.setupLocal();
}

// renderStage 每次状态推送都会重建 DOM；内容不变时跳过，避免动画反复播放
let lastStageKey = '';
function stageKey(st) {
  const a = st.servers.find((s) => s.id === st.activeServerId);
  if (!a) return 'none';
  return [a.id, a.name, a.url, a.online, a.authenticated, a.lastError].join('|');
}

function render(st) {
  state = st;
  applyRootTheme(st);
  renderTitlebar(st);
  renderRail(st);
  renderDrawer(st);
  const key = stageKey(st);
  if (key !== lastStageKey) {
    lastStageKey = key;
    renderStage(st);
  } else if (key === 'none') {
    renderLocalStatus(st);
  }
}

// ---------- 添加 / 编辑服务器弹窗 ----------

function checkUrlInsecureInput() {
  const raw = el('f-url').value.trim();
  const warn = el('f-insecure-warn');
  if (!raw) {
    warn.classList.add('hidden');
    return;
  }
  const lower = raw.toLowerCase();
  const isLocal =
    lower.includes('localhost') || lower.includes('127.0.0.1') || lower.includes('[::1]');
  const looksHttp =
    lower.startsWith('http://') || (!lower.includes('://') && (lower.includes(':') || /^[0-9.]+$/.test(lower)));
  warn.classList.toggle('hidden', !looksHttp || isLocal);
}

function setPassVisible(visible) {
  el('f-pass').type = visible ? 'text' : 'password';
  const btn = el('btn-toggle-pass');
  btn.classList.toggle('on', visible);
  btn.title = visible ? '隐藏密码' : '显示密码';
}

function openServerModal(srv = null) {
  frogDesktop.setModal(true);
  el('modal-backdrop').classList.remove('hidden');
  el('f-error').classList.add('hidden');
  el('f-id').value = srv?.id || '';
  el('f-url').value = srv?.url || '';
  el('f-name').value = srv?.name || '';
  el('f-user').value = srv?.username || 'admin';
  el('f-pass').value = '';
  setPassVisible(false);
  el('modal-title').textContent = srv ? `连接「${srv.name}」` : '添加 frog 服务器';
  el('btn-modal-submit').textContent = srv ? '保存并登录' : '连接并登录';
  checkUrlInsecureInput();
  setTimeout(() => {
    (srv ? el('f-pass') : el('f-url')).focus();
  }, 30);
}

function closeServerModal() {
  el('modal-backdrop').classList.add('hidden');
  frogDesktop.setModal(false);
}

// ---------- 事件绑定 ----------

function toggleDrawerTab(tab) {
  const toggleOff = state?.drawerOpen && activeDrawerTab === tab;
  activeDrawerTab = tab;
  if (state?.drawerOpen && !toggleOff) {
    renderDrawer(state);
    renderRail(state);
  }
  frogDesktop.setDrawer(!toggleOff);
}

window.addEventListener('DOMContentLoaded', async () => {
  el('btn-add-server').addEventListener('click', () => openServerModal());
  el('btn-modal-close').addEventListener('click', closeServerModal);
  el('btn-modal-cancel').addEventListener('click', closeServerModal);
  el('f-url').addEventListener('input', checkUrlInsecureInput);
  el('btn-toggle-pass').addEventListener('click', () => setPassVisible(el('f-pass').type === 'password'));

  el('modal-backdrop').addEventListener('mousedown', (e) => {
    if (e.target === e.currentTarget) closeServerModal();
  });
  el('confirm-backdrop').addEventListener('mousedown', (e) => {
    if (e.target === e.currentTarget) closeConfirm(false);
  });
  el('btn-confirm-cancel').addEventListener('click', () => closeConfirm(false));
  el('btn-confirm-ok').addEventListener('click', () => closeConfirm(true));

  el('server-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const submitBtn = el('btn-modal-submit');
    const errBanner = el('f-error');
    const idleText = submitBtn.textContent;
    submitBtn.disabled = true;
    submitBtn.textContent = '正在连接…';
    errBanner.classList.add('hidden');
    try {
      const next = await frogDesktop.saveServer({
        id: el('f-id').value || undefined,
        url: el('f-url').value.trim(),
        name: el('f-name').value.trim(),
        username: el('f-user').value.trim(),
        password: el('f-pass').value,
      });
      render(next);
      closeServerModal();
    } catch (err) {
      errBanner.textContent = String(err?.message || err || '连接失败').replace(/^Error invoking remote method '[^']+': Error:\s*/, '');
      errBanner.classList.remove('hidden');
    } finally {
      submitBtn.disabled = false;
      submitBtn.textContent = idleText;
    }
  });

  el('btn-reload').addEventListener('click', () => frogDesktop.reloadServer());

  el('btn-theme').addEventListener('click', () => {
    const order = { system: 'light', light: 'dark', dark: 'system' };
    const next = order[state?.theme || 'system'] || 'system';
    frogDesktop.setTheme(next);
  });

  el('btn-relay-pill').addEventListener('click', () => toggleDrawerTab('relay'));
  el('btn-update-pill').addEventListener('click', () => {
    if (state?.update?.url) frogDesktop.openExternal(state.update.url);
  });

  el('btn-drawer-servers').addEventListener('click', () => toggleDrawerTab('servers'));
  el('btn-drawer-relay').addEventListener('click', () => toggleDrawerTab('relay'));
  el('btn-drawer-settings').addEventListener('click', () => toggleDrawerTab('client'));
  el('btn-close-drawer').addEventListener('click', () => frogDesktop.setDrawer(false));

  document.querySelectorAll('.seg-item').forEach((tab) => {
    tab.addEventListener('click', () => {
      activeDrawerTab = tab.getAttribute('data-tab');
      if (state) {
        renderDrawer(state);
        renderRail(state);
      }
    });
  });

  window.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'r') {
      e.preventDefault();
      frogDesktop.reloadServer();
    } else if ((e.ctrlKey || e.metaKey) && /^[1-9]$/.test(e.key)) {
      const idx = Number(e.key) - 1;
      const target = state?.servers?.[idx];
      if (target) {
        e.preventDefault();
        frogDesktop.selectServer(target.id);
      }
    } else if (e.key === 'Escape') {
      if (!el('confirm-backdrop').classList.contains('hidden')) closeConfirm(false);
      else if (!el('modal-backdrop').classList.contains('hidden')) closeServerModal();
      else if (state?.drawerOpen) frogDesktop.setDrawer(false);
    }
  });

  frogDesktop.onState((next) => render(next));
  const initial = await frogDesktop.getState();
  render(initial);
});
