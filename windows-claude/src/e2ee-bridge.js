'use strict';

const crypto = require('node:crypto');
const http = require('node:http');
const os = require('node:os');

const HKDF_SALT = Buffer.from('frog-e2ee-v1', 'utf8');
// Standard DER SubjectPublicKeyInfo prefix for a 32-byte raw X25519 public key (RFC 8410)
const X25519_SPKI_PREFIX = Buffer.from('302a300506032b656e032100', 'hex');

function b64urlEncode(buf) {
  return Buffer.from(buf).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

function b64urlDecode(str) {
  let s = String(str || '').trim().replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4 !== 0) s += '=';
  return Buffer.from(s, 'base64');
}

function extractTunnelURI(raw) {
  const s = String(raw || '').trim();
  const idx = s.toLowerCase().indexOf('froge2ee://');
  if (idx === -1) return null;
  const tail = s.slice(idx);
  const end = tail.search(/[\s\r\n]/);
  const uri = end === -1 ? tail : tail.slice(0, end);
  let serverName = '';
  const hashIdx = uri.indexOf('#');
  if (hashIdx !== -1) {
    try {
      serverName = decodeURIComponent(uri.slice(hashIdx + 1)).trim();
    } catch {
      serverName = uri.slice(hashIdx + 1).trim();
    }
  }
  return { uri, serverName };
}

function parseE2EEURI(raw) {
  const extracted = extractTunnelURI(raw);
  if (!extracted) throw new Error('未识别到 froge2ee:// 连接码');
  let u;
  try {
    u = new URL(extracted.uri);
  } catch (err) {
    throw new Error(`无效的 E2EE 链接: ${err.message}`);
  }
  const peerId = decodeURIComponent(u.username || '').trim().toLowerCase();
  if (!/^[0-9a-f]{16}$/.test(peerId)) {
    throw new Error('E2EE PeerID 无效');
  }
  const pskRaw = decodeURIComponent(u.password || '').trim();
  let pskBuf;
  if (/^[0-9a-fA-F]{64}$/.test(pskRaw)) {
    pskBuf = Buffer.from(pskRaw, 'hex');
  } else {
    pskBuf = b64urlDecode(pskRaw);
  }
  if (pskBuf.length !== 32) {
    throw new Error('E2EE PSK 密钥无效');
  }
  const hostId = (u.hostname || '').trim().toLowerCase();
  if (hostId.length < 8) {
    throw new Error('E2EE HostID 无效');
  }
  const pubRaw = (u.searchParams.get('pub') || '').trim();
  const serverPubBuf = b64urlDecode(pubRaw);
  if (serverPubBuf.length !== 32) {
    throw new Error('E2EE 主机公钥无效');
  }
  const relayUrl = (u.searchParams.get('relay') || '').trim();
  const lanAddr = (u.searchParams.get('lan') || '').trim();
  if (!relayUrl && !lanAddr) {
    throw new Error('E2EE 链接缺少中继或局域网地址');
  }
  const relayToken = (u.searchParams.get('rt') || '').trim();
  const serverName = extracted.serverName || hostId.slice(0, 8);
  return {
    uri: extracted.uri,
    peerId,
    pskBuf,
    hostId,
    serverPubBuf,
    serverPubB64: b64urlEncode(serverPubBuf),
    relayUrl,
    relayToken,
    lanAddr,
    serverName,
    badge: relayUrl ? 'E2EE·WSS' : 'E2EE·局域网',
  };
}

function generateEphemeralX25519() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('x25519');
  const spki = publicKey.export({ format: 'der', type: 'spki' });
  const rawPub = spki.subarray(spki.length - 32);
  return { privateKey, rawPub, pubB64: b64urlEncode(rawPub) };
}

function deriveE2EESessionKey(myPrivKey, peerPubRaw32, psk32, hostId, peerId) {
  if (peerPubRaw32.length !== 32 || psk32.length !== 32) {
    throw new Error('invalid X25519 pubkey or PSK length');
  }
  const peerPubObj = crypto.createPublicKey({
    key: Buffer.concat([X25519_SPKI_PREFIX, peerPubRaw32]),
    format: 'der',
    type: 'spki',
  });
  const shared = crypto.diffieHellman({ privateKey: myPrivKey, publicKey: peerPubObj });
  const ikm = Buffer.concat([shared, psk32]);
  const info = Buffer.from(`${hostId}|${peerId}`, 'utf8');
  return Buffer.from(crypto.hkdfSync('sha256', ikm, HKDF_SALT, info, 32));
}

function sealE2EE(key32, from, to, ephPubB64, plaintextBuf) {
  const nonce = crypto.randomBytes(12);
  const aad = Buffer.from(`${from}|${to}|${ephPubB64}`, 'utf8');
  const cipher = crypto.createCipheriv('aes-256-gcm', key32, nonce);
  cipher.setAAD(aad);
  const enc = Buffer.concat([cipher.update(plaintextBuf), cipher.final()]);
  const tag = cipher.getAuthTag();
  return {
    to,
    from,
    ep: ephPubB64,
    n: b64urlEncode(nonce),
    c: b64urlEncode(Buffer.concat([enc, tag])),
  };
}

function openE2EE(key32, env) {
  const nonce = b64urlDecode(env.n);
  if (nonce.length !== 12) throw new Error('invalid E2EE nonce');
  const ctAndTag = b64urlDecode(env.c);
  if (ctAndTag.length < 16) throw new Error('invalid E2EE ciphertext');
  const ct = ctAndTag.subarray(0, ctAndTag.length - 16);
  const tag = ctAndTag.subarray(ctAndTag.length - 16);
  const aad = Buffer.from(`${env.from}|${env.to}|${env.ep}`, 'utf8');
  const decipher = crypto.createDecipheriv('aes-256-gcm', key32, nonce);
  decipher.setAAD(aad);
  decipher.setAuthTag(tag);
  return Buffer.concat([decipher.update(ct), decipher.final()]);
}

function buildRelayClientURL(cfg) {
  let raw = cfg.relayUrl.trim();
  if (raw.startsWith('https://')) raw = 'wss://' + raw.slice(8);
  else if (raw.startsWith('http://')) raw = 'ws://' + raw.slice(7);
  else if (!raw.includes('://')) raw = 'wss://' + raw;
  const u = new URL(raw);
  if (!u.pathname || u.pathname === '/') u.pathname = '/ws';
  u.searchParams.set('role', 'client');
  u.searchParams.set('room', cfg.hostId);
  u.searchParams.set('peer', cfg.peerId);
  if (cfg.relayToken) u.searchParams.set('token', cfg.relayToken);
  return u.toString();
}

function dialWS(url, timeoutMs) {
  const WS = globalThis.WebSocket;
  if (typeof WS !== 'function') {
    return Promise.reject(new Error('当前运行环境缺少内置 WebSocket 支持'));
  }
  return new Promise((resolve, reject) => {
    let settled = false;
    const ws = new WS(url);
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      try { ws.close(); } catch {}
      reject(new Error('WebSocket 连接超时'));
    }, timeoutMs);

    ws.addEventListener('open', () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(ws);
    });
    ws.addEventListener('error', () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error('WebSocket 连接失败'));
    });
    ws.addEventListener('close', () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error('WebSocket 连接已关闭'));
    });
  });
}

/**
 * 纯 Node.js 零依赖 E2EE 本地代理桥：
 * 在 127.0.0.1 随机端口起一个仅供 Electron WebContentsView 访问的 HTTP 代理，
 * 将普通 HTTP 与 SSE 流经由 X25519 + PSK + AES-256-GCM 端到端加密后，
 * 优先走局域网直连、自动回退免费 WSS 盲中继转发至远端 Frog Bot 主机。
 */
class E2EEBridge {
  constructor(tunnelUri) {
    this.cfg = parseE2EEURI(tunnelUri);
    this.from = `client:${this.cfg.peerId}`;
    this.to = 'host';
    this.deviceName = `${os.hostname()} (Windows)`;

    this.server = null;
    this.localUrl = null;
    this.ws = null;
    this.wsViaRelay = false;
    this.eph = null;
    this.sessionKey = null;
    this.connectPromise = null;
    this.pingTimer = null;
    this.closed = false;

    // streamId -> { res, started, isSSE, timeout, resolveDone }
    this.streams = new Map();
  }

  async start() {
    if (this.localUrl) return this.localUrl;
    this.server = http.createServer((req, res) => {
      this._handleLocalHTTP(req, res).catch((err) => {
        if (!res.headersSent) {
          res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' });
          res.end(JSON.stringify({ error: err?.message || 'E2EE 隧道请求失败' }));
        } else {
          try { res.end(); } catch {}
        }
      });
    });

    await new Promise((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(0, '127.0.0.1', () => {
        const addr = this.server.address();
        this.localUrl = `http://127.0.0.1:${addr.port}`;
        resolve();
      });
    });
    return this.localUrl;
  }

  close() {
    this.closed = true;
    this._teardownWS('隧道已关闭');
    if (this.server) {
      try { this.server.close(); } catch {}
      this.server = null;
    }
    this.localUrl = null;
  }

  async _ensureWS() {
    if (this.closed) throw new Error('E2EE 隧道已关闭');
    if (this.ws && this.ws.readyState === 1) return this.ws;
    if (this.connectPromise) return this.connectPromise;

    this.connectPromise = (async () => {
      let ws = null;
      let viaRelay = false;

      // 1. 局域网直连优先（650ms 快速探测）
      if (this.cfg.lanAddr) {
        try {
          const lanURL = this.cfg.lanAddr.includes('://')
            ? this.cfg.lanAddr
            : `ws://${this.cfg.lanAddr}/ws`;
          ws = await dialWS(lanURL, 650);
          viaRelay = false;
        } catch {
          ws = null;
        }
      }

      // 2. 自动回退到免费 WSS 盲中继
      if (!ws && this.cfg.relayUrl) {
        const relayURL = buildRelayClientURL(this.cfg);
        ws = await dialWS(relayURL, 10_000);
        viaRelay = true;
      }

      if (!ws) {
        throw new Error('无法连接远端主机的局域网或加密中继通道');
      }

      // 为本次会话生成前向安全临时 X25519 密钥对
      const eph = generateEphemeralX25519();
      const sessionKey = deriveE2EESessionKey(
        eph.privateKey,
        this.cfg.serverPubBuf,
        this.cfg.pskBuf,
        this.cfg.hostId,
        this.cfg.peerId
      );

      this.ws = ws;
      this.wsViaRelay = viaRelay;
      this.eph = eph;
      this.sessionKey = sessionKey;

      ws.addEventListener('message', (ev) => {
        this._onWSMessage(ev.data);
      });
      ws.addEventListener('close', () => {
        if (this.ws === ws) this._teardownWS('加密通道已断开，正在重连');
      });
      ws.addEventListener('error', () => {
        if (this.ws === ws) this._teardownWS('加密通道传输错误');
      });

      if (this.pingTimer) clearInterval(this.pingTimer);
      this.pingTimer = setInterval(() => {
        if (this.ws && this.ws.readyState === 1) {
          try { this.ws.send('ping'); } catch {}
        }
      }, 25_000);

      return ws;
    })();

    try {
      return await this.connectPromise;
    } finally {
      this.connectPromise = null;
    }
  }

  _teardownWS(reason) {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
    const ws = this.ws;
    this.ws = null;
    this.eph = null;
    this.sessionKey = null;
    if (ws) {
      try { ws.close(); } catch {}
    }
    for (const [sid, st] of this.streams.entries()) {
      this.streams.delete(sid);
      if (st.timeout) clearTimeout(st.timeout);
      try {
        if (!st.res.headersSent) {
          st.res.writeHead(503, { 'Content-Type': 'application/json; charset=utf-8' });
          st.res.end(JSON.stringify({ error: reason || '主机已断开连接' }));
        } else {
          st.res.end();
        }
      } catch {}
    }
  }

  _sendEncryptedFrame(frameObj) {
    if (!this.ws || this.ws.readyState !== 1 || !this.sessionKey || !this.eph) {
      throw new Error('加密通道未就绪');
    }
    const plain = Buffer.from(JSON.stringify(frameObj), 'utf8');
    const env = sealE2EE(this.sessionKey, this.from, this.to, this.eph.pubB64, plain);
    this.ws.send(JSON.stringify(env));
  }

  _onWSMessage(rawData) {
    const text = typeof rawData === 'string' ? rawData : Buffer.from(rawData).toString('utf8');
    const trimmed = text.trim();
    if (!trimmed || trimmed === 'pong' || trimmed === 'ping') {
      if (trimmed === 'ping' && this.ws?.readyState === 1) {
        try { this.ws.send('pong'); } catch {}
      }
      return;
    }

    let env;
    try {
      env = JSON.parse(trimmed);
    } catch {
      return;
    }

    if (env.err) {
      const msg = env.err === 'host_offline'
        ? '远端电脑上的 Frog Bot 当前不在线，请确认主机已启动'
        : `中继返回错误：${env.err}`;
      for (const [sid, st] of this.streams.entries()) {
        this.streams.delete(sid);
        if (st.timeout) clearTimeout(st.timeout);
        try {
          if (!st.res.headersSent) {
            st.res.writeHead(503, { 'Content-Type': 'application/json; charset=utf-8' });
            st.res.end(JSON.stringify({ error: msg }));
          } else {
            st.res.end();
          }
        } catch {}
      }
      return;
    }

    if (!env.c || !this.sessionKey) return;

    let frame;
    try {
      const plain = openE2EE(this.sessionKey, env);
      frame = JSON.parse(plain.toString('utf8'));
    } catch {
      return;
    }

    if (!frame || frame.t === 'pong') return;
    const st = this.streams.get(frame.s);
    if (!st) return;

    if (frame.t === 'resp') {
      const headers = {};
      if (frame.h && typeof frame.h === 'object') {
        for (const [k, v] of Object.entries(frame.h)) {
          const lk = k.toLowerCase();
          if (lk === 'transfer-encoding' || lk === 'connection' || lk === 'content-length') continue;
          headers[k] = v;
        }
      }
      const ct = String(headers['Content-Type'] || headers['content-type'] || '').toLowerCase();
      if (ct.includes('text/event-stream')) {
        st.isSSE = true;
        headers['Cache-Control'] = 'no-cache';
        headers['Connection'] = 'keep-alive';
        if (st.timeout) {
          clearTimeout(st.timeout);
          st.timeout = null;
        }
      }
      st.started = true;
      st.res.writeHead(frame.st || 200, headers);
      if (frame.b) {
        st.res.write(Buffer.from(frame.b, 'base64'));
      }
      if (frame.eof) {
        this._finishStream(frame.s);
      }
    } else if (frame.t === 'chunk') {
      if (!st.started) {
        st.started = true;
        st.res.writeHead(200);
      }
      if (frame.b) {
        st.res.write(Buffer.from(frame.b, 'base64'));
      }
      if (frame.eof) {
        this._finishStream(frame.s);
      }
    } else if (frame.t === 'end') {
      if (frame.err && !st.res.headersSent) {
        st.res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' });
        st.res.end(JSON.stringify({ error: frame.err }));
      }
      this._finishStream(frame.s);
    }
  }

  _finishStream(streamId) {
    const st = this.streams.get(streamId);
    if (!st) return;
    this.streams.delete(streamId);
    if (st.timeout) clearTimeout(st.timeout);
    try { st.res.end(); } catch {}
  }

  async _handleLocalHTTP(req, res) {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const bodyBuf = chunks.length ? Buffer.concat(chunks) : null;

    await this._ensureWS();

    const streamId = crypto.randomBytes(8).toString('hex');
    const headers = {};
    for (const [k, v] of Object.entries(req.headers || {})) {
      const lk = k.toLowerCase();
      if (lk === 'host' || lk === 'connection' || lk === 'content-length' || lk === 'origin' || lk === 'referer') continue;
      headers[k] = Array.isArray(v) ? v.join(', ') : String(v);
    }
    headers['X-Frog-Client'] = this.deviceName;

    const accept = String(req.headers?.accept || '').toLowerCase();
    const isLikelySSE = accept.includes('text/event-stream') || String(req.url || '').includes('/stream') || String(req.url || '').includes('/events');

    const st = {
      res,
      started: false,
      isSSE: isLikelySSE,
      timeout: isLikelySSE ? null : setTimeout(() => {
        if (!this.streams.has(streamId)) return;
        this.streams.delete(streamId);
        try {
          if (!res.headersSent) {
            res.writeHead(504, { 'Content-Type': 'application/json; charset=utf-8' });
            res.end(JSON.stringify({ error: 'E2EE 请求超时，请检查远端主机是否在线' }));
          } else {
            res.end();
          }
        } catch {}
        try { this._sendEncryptedFrame({ t: 'cancel', s: streamId }); } catch {}
      }, 45_000),
    };
    this.streams.set(streamId, st);

    req.on('close', () => {
      if (this.streams.has(streamId)) {
        this.streams.delete(streamId);
        if (st.timeout) clearTimeout(st.timeout);
        try { this._sendEncryptedFrame({ t: 'cancel', s: streamId }); } catch {}
      }
    });

    const frame = {
      t: 'req',
      s: streamId,
      m: req.method || 'GET',
      p: req.url || '/',
      h: headers,
    };
    if (bodyBuf && bodyBuf.length > 0) {
      frame.b = bodyBuf.toString('base64');
    }
    this._sendEncryptedFrame(frame);
  }
}

module.exports = {
  E2EEBridge,
  extractTunnelURI,
  parseE2EEURI,
  generateEphemeralX25519,
  deriveE2EESessionKey,
  sealE2EE,
  openE2EE,
  b64urlEncode,
  b64urlDecode,
};
