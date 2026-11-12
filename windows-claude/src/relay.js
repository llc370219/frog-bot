const { relayServerKey } = require('./store');

/**
 * 客户端 Bot 互联（与 macOS / Android 客户端协议完全一致）：
 * - 只在客户端运行且打开「Bot 互联」总开关时工作；
 * - 每台服务器、每个本机 Bot 均可单独开关；
 * - 对每台参与的服务器长轮询 POST /api/relay/sync（上报 client_id、本机允许的 Bot、其它服务器上的可达 Bot，
 *   取走待转发消息并带上上一轮回执），再转发到目标服务器的 POST /api/relay/inbox；
 * - 对方地址形如 relay:<12位服务器代号>/<BotID>。
 */
class RelayManager {
  constructor(store, fetchServerJSON, onChange) {
    this.store = store;
    this.fetchServerJSON = fetchServerJSON;
    this.onChange = onChange;
    this.states = new Map(); // serverId -> { status, error? }
    this.loops = new Map();  // serverId -> AbortController
    this.pushed = new Map(); // serverId -> signature
    this.joined = new Set(); // serverId set
  }

  get config() {
    return this.store.data.relay;
  }

  isServerOn(serverId) {
    return !this.config.offServers.includes(serverId);
  }

  isBotOn(serverId, botId) {
    return !this.config.offBots.includes(`${serverId}/${botId}`);
  }

  setEnabled(enabled) {
    this.config.enabled = Boolean(enabled);
    this.store.save();
    this.apply();
  }

  setServerOn(serverId, on) {
    const set = new Set(this.config.offServers);
    if (on) set.delete(serverId);
    else set.add(serverId);
    this.config.offServers = [...set];
    this.store.save();
    this.apply();
  }

  setBotOn(serverId, botId, on) {
    const key = `${serverId}/${botId}`;
    const set = new Set(this.config.offBots);
    if (on) set.delete(key);
    else set.add(key);
    this.config.offBots = [...set];
    this.store.save();
    this.apply();
  }

  localBots(server) {
    const groups = Array.isArray(server.devices) ? server.devices : [];
    const local = groups.find((d) => d.id === 'local');
    return Array.isArray(local?.bots) ? local.bots : (Array.isArray(server.bots) ? server.bots : []);
  }

  participants(server) {
    if (!this.config.enabled || !this.isServerOn(server.id) || !server.online) return [];
    return this.localBots(server).filter((b) => this.isBotOn(server.id, b.id));
  }

  syncBody(server, allServers) {
    const mine = this.participants(server);
    if (!mine.length) return null;
    const peers = [];
    for (const other of allServers) {
      if (other.id === server.id) continue;
      const otherKey = relayServerKey(other.id);
      for (const b of this.participants(other)) {
        peers.push({
          address: `relay:${otherKey}/${b.id}`,
          name: b.name,
          server: other.name,
        });
      }
    }
    if (!peers.length) return null;
    return {
      client_id: this.config.clientId,
      enabled: true,
      allowed: mine.map((b) => b.id),
      peers,
    };
  }

  getState(serverId) {
    return this.states.get(serverId) || { status: 'off' };
  }

  apply(runtimeServers = []) {
    this.runtimeServers = runtimeServers;
    const wanted = new Map();

    for (const s of runtimeServers) {
      const body = this.syncBody(s, runtimeServers);
      if (!body) {
        const waiting = this.config.enabled && this.isServerOn(s.id) && s.online;
        this.states.set(s.id, { status: waiting ? 'waiting' : 'off' });
        continue;
      }
      wanted.set(s.id, body);
    }

    const clientId = this.config.clientId;
    for (const id of [...this.joined]) {
      if (!wanted.has(id)) {
        this.loops.get(id)?.abort();
        this.loops.delete(id);
        this.pushed.delete(id);
        this.joined.delete(id);
        this.fetchServerJSON(id, '/api/relay/sync', {
          method: 'POST',
          body: { client_id: clientId, enabled: false },
          timeoutMs: 5000,
        }).catch(() => {});
      }
    }

    for (const s of runtimeServers) {
      const body = wanted.get(s.id);
      if (!body) continue;
      this.joined.add(s.id);
      const sig = JSON.stringify(body);
      if (!this.loops.has(s.id)) {
        this.pushed.set(s.id, sig);
        this.states.set(s.id, { status: 'connecting' });
        const ctrl = new AbortController();
        this.loops.set(s.id, ctrl);
        this._runLoop(s.id, ctrl.signal);
      } else if (this.pushed.get(s.id) !== sig) {
        this.pushed.set(s.id, sig);
        this._syncOnce(s.id, [], false).then(async (items) => {
          if (items && items.length) {
            const acks = await this._forward(items, s.id);
            if (acks.length) await this._syncOnce(s.id, acks, false).catch(() => {});
          }
        }).catch(() => {});
      }
    }

    this.onChange?.();
  }

  async _syncOnce(serverId, acks, wait, signal) {
    const s = (this.runtimeServers || []).find((x) => x.id === serverId);
    if (!s) throw new Error('idle');
    const body = this.syncBody(s, this.runtimeServers || []);
    if (!body) throw new Error('idle');
    body.acks = acks;
    body.wait = wait;
    const out = await this.fetchServerJSON(serverId, '/api/relay/sync', {
      method: 'POST',
      body,
      timeoutMs: wait ? 40_000 : 10_000,
      signal,
    });
    return Array.isArray(out?.items) ? out.items : [];
  }

  async _runLoop(serverId, signal) {
    let acks = [];
    let failures = 0;
    let firstTurn = true;
    while (!signal.aborted) {
      try {
        // 首次连接先做一次非阻塞同步（wait: false），立即上报通讯录并切换为 active，随后进入长轮询
        const wait = !firstTurn;
        firstTurn = false;
        const items = await this._syncOnce(serverId, acks, wait, signal);
        acks = await this._forward(items, serverId);
        failures = 0;
        this.states.set(serverId, { status: 'active' });
        this.onChange?.();
      } catch (err) {
        if (signal.aborted) return;
        if (err && err.message === 'idle') {
          await new Promise((r) => setTimeout(r, 2000));
          continue;
        }
        if (err && err.status === 404) {
          this.states.set(serverId, { status: 'unsupported' });
          this.onChange?.();
          acks = [];
          await new Promise((r) => setTimeout(r, 60_000));
          continue;
        }
        failures += 1;
        acks = [];
        this.states.set(serverId, { status: 'failed', error: err?.message || '连接出错' });
        this.onChange?.();
        const delay = Math.min(30, 1 << Math.min(failures, 5)) * 1000;
        await new Promise((r) => setTimeout(r, delay));
      }
    }
  }

  async _forward(items, fromServerId) {
    const acks = [];
    const fromServer = (this.runtimeServers || []).find((x) => x.id === fromServerId);
    if (!fromServer) return acks;
    const fromKey = relayServerKey(fromServer.id);

    for (const it of items) {
      const id = String(it?.id || '');
      if (!id) continue;
      const toRaw = String(it?.to || '').replace(/^relay:/, '');
      const slash = toRaw.indexOf('/');
      const key = slash > 0 ? toRaw.slice(0, slash) : '';
      const targetBot = slash > 0 ? toRaw.slice(slash + 1) : '';
      const target = (this.runtimeServers || []).find((x) => relayServerKey(x.id) === key);
      const ack = { id };

      if (target && targetBot && this.participants(target).length > 0) {
        const body = {
          client_id: this.config.clientId,
          id,
          from: `relay:${fromKey}/${String(it?.from || '')}`,
          to: targetBot,
          text: String(it?.text || ''),
          request_id: String(it?.request_id || ''),
          reply_to: String(it?.reply_to || ''),
          wants_reply: Boolean(it?.wants_reply),
        };
        try {
          await this.fetchServerJSON(target.id, '/api/relay/inbox', {
            method: 'POST',
            body,
            timeoutMs: 8000,
          });
        } catch (err) {
          ack.error = `转交失败：${err?.message || '未知错误'}`;
        }
      } else {
        ack.error = '对方不在线或没有打开互联';
      }
      acks.push(ack);
    }
    return acks;
  }

  forget(serverId) {
    this.states.delete(serverId);
    this.loops.get(serverId)?.abort();
    this.loops.delete(serverId);
    this.pushed.delete(serverId);
    if (this.joined.delete(serverId)) {
      this.fetchServerJSON(serverId, '/api/relay/sync', {
        method: 'POST',
        body: { client_id: this.config.clientId, enabled: false },
        timeoutMs: 4000,
      }).catch(() => {});
    }
  }

  async shutdown() {
    const ids = [...this.joined];
    for (const ctrl of this.loops.values()) ctrl.abort();
    this.loops.clear();
    this.joined.clear();
    const clientId = this.config.clientId;
    await Promise.allSettled(
      ids.map((id) =>
        this.fetchServerJSON(id, '/api/relay/sync', {
          method: 'POST',
          body: { client_id: clientId, enabled: false },
          timeoutMs: 2000,
        })
      )
    );
  }
}

module.exports = { RelayManager };
