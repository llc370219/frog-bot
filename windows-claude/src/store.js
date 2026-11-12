const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { app } = require('electron');

function isLoopback(host = '') {
  const h = host.toLowerCase();
  return h === 'localhost' || h.endsWith('.localhost') || h.startsWith('127.') || h === '::1' || h === '[::1]';
}

/**
 * 与 macOS / Android 客户端一致的地址规范化：
 * 没写协议时，IP、带端口或本机地址默认 http://，纯域名默认 https://。
 */
function normalizeServerURL(raw) {
  let s = String(raw || '').trim();
  while (s.endsWith('/')) s = s.slice(0, -1);
  if (!s) return null;
  if (!s.includes('://')) {
    const hostPort = s.split('/')[0] || s;
    const host = hostPort.split(':')[0] || hostPort;
    const isIP = host.length > 0 && /^[0-9.]+$/.test(host);
    const plain = hostPort.includes(':') || isIP || isLoopback(host);
    s = (plain ? 'http://' : 'https://') + s;
  }
  try {
    const u = new URL(s);
    const scheme = u.protocol.toLowerCase();
    if ((scheme !== 'http:' && scheme !== 'https:') || !u.hostname) return null;
    u.hash = '';
    u.search = '';
    let out = u.toString();
    while (out.endsWith('/')) out = out.slice(0, -1);
    return out;
  } catch {
    return null;
  }
}

function isInsecureURL(raw) {
  try {
    const u = new URL(raw);
    return u.protocol === 'http:' && !isLoopback(u.hostname);
  } catch {
    return false;
  }
}

function defaultServerName(urlStr) {
  try {
    const u = new URL(urlStr);
    if (isLoopback(u.hostname)) return u.port ? `本机 :${u.port}` : '本机';
    return u.host;
  } catch {
    return urlStr;
  }
}

function relayServerKey(id) {
  return String(id || '').replace(/[^a-zA-Z0-9]/g, '').toLowerCase().slice(0, 12);
}

class ConfigStore {
  constructor() {
    this.file = path.join(app.getPath('userData'), 'frog-config.json');
    this.data = this._load();
  }

  _defaults() {
    return {
      servers: [],
      activeServerId: null,
      drawerOpen: false,
      theme: 'system', // 'system' | 'light' | 'dark'
      closeToTray: true,
      autoCheckUpdates: true,
      windowBounds: { width: 1260, height: 840 },
      relay: {
        clientId: 'win-' + crypto.randomUUID().replace(/-/g, '').toLowerCase().slice(0, 24),
        enabled: false,
        offServers: [],
        offBots: [],
      },
    };
  }

  _load() {
    const def = this._defaults();
    try {
      const raw = fs.readFileSync(this.file, 'utf8');
      const parsed = JSON.parse(raw);
      return {
        ...def,
        ...parsed,
        relay: { ...def.relay, ...(parsed.relay || {}) },
        servers: Array.isArray(parsed.servers) ? parsed.servers : [],
      };
    } catch {
      return def;
    }
  }

  save() {
    try {
      fs.mkdirSync(path.dirname(this.file), { recursive: true });
      const tmp = `${this.file}.tmp`;
      fs.writeFileSync(tmp, JSON.stringify(this.data, null, 2), { mode: 0o600 });
      fs.renameSync(tmp, this.file);
    } catch {
      // Ignore write failures on read-only media
    }
  }
}

module.exports = {
  ConfigStore,
  normalizeServerURL,
  isInsecureURL,
  isLoopback,
  defaultServerName,
  relayServerKey,
};
