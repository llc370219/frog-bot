const fs = require('node:fs');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const crypto = require('node:crypto');
const { spawn, execFile } = require('node:child_process');
const { app, net: electronNet } = require('electron');

const REPO = 'llc370219/frog-bot';
const PORT_FROM = 8080;
const PORT_TO = 8099;

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * 本机版 frog：下载官方 Windows 发布包，安装到客户端数据目录，作为子进程在后台运行。
 * 只监听 127.0.0.1，没有登录；数据目录沿用 frogbot 默认位置（%APPDATA%\frogbot），
 * 与手动运行 frogbot.exe 共享同一份数据。
 */
class LocalServer {
  constructor(onChange) {
    this.onChange = onChange;
    this.child = null;
    this.stopping = false;
    this.busy = false;
    this.status = { phase: 'idle', progress: 0, error: '' };
  }

  get dir() {
    return path.join(app.getPath('userData'), 'local-server');
  }

  get exe() {
    return path.join(this.dir, 'frogbot.exe');
  }

  get logFile() {
    return path.join(this.dir, 'frogbot.log');
  }

  isInstalled() {
    return fs.existsSync(this.exe);
  }

  isRunning() {
    return Boolean(this.child && this.child.exitCode === null);
  }

  installedVersion() {
    try {
      return fs.readFileSync(path.join(this.dir, 'version.txt'), 'utf8').trim();
    } catch {
      return '';
    }
  }

  setStatus(patch) {
    this.status = { ...this.status, ...patch };
    this.onChange();
  }

  snapshot() {
    return {
      ...this.status,
      busy: this.busy,
      dir: this.dir,
      installed: this.isInstalled(),
      running: this.isRunning(),
      version: this.installedVersion(),
    };
  }

  /** 本机端口上是否有免登录（本机模式）的 frog 在响应 */
  async health(port, timeoutMs = 1500) {
    const get = async (p) => {
      const resp = await fetch(`http://127.0.0.1:${port}${p}`, { signal: AbortSignal.timeout(timeoutMs) });
      return { status: resp.status, data: await resp.json().catch(() => null) };
    };
    try {
      const h = await get('/api/health');
      if (h.status !== 200 || !h.data?.ok) return null;
      const me = await get('/api/auth/me');
      // 服务器模式（要登录）的 frog 不算本机版
      if (me.status === 200 && me.data?.enabled) return null;
      return h.data;
    } catch {
      return null;
    }
  }

  /** 找出 8080-8099 里已经在运行的 frog（例如用户自己双击运行的 frogbot.exe） */
  async findRunning() {
    const ports = [];
    for (let p = PORT_FROM; p <= PORT_TO; p++) ports.push(p);
    const results = await Promise.all(ports.map((p) => this.health(p, 1200)));
    const idx = results.findIndex(Boolean);
    return idx === -1 ? null : ports[idx];
  }

  portFree(port) {
    return new Promise((resolve) => {
      const srv = net.createServer();
      srv.once('error', () => resolve(false));
      srv.once('listening', () => srv.close(() => resolve(true)));
      srv.listen(port, '127.0.0.1');
    });
  }

  async freePort(preferred) {
    if (preferred && (await this.portFree(preferred))) return preferred;
    for (let p = PORT_FROM; p <= PORT_TO; p++) {
      if (await this.portFree(p)) return p;
    }
    throw new Error('8080-8099 端口都被占用了，没法启动本机 frog');
  }

  // ---------- 下载安装 ----------

  async fetchOK(url, accept) {
    const resp = await electronNet.fetch(url, {
      headers: { Accept: accept, 'User-Agent': `frog-windows/${app.getVersion()}` },
    });
    if (!resp.ok) throw new Error(`下载失败（HTTP ${resp.status}）：${url}`);
    return resp;
  }

  async download(url, onProgress) {
    const resp = await this.fetchOK(url, 'application/octet-stream');
    const total = Number(resp.headers.get('content-length')) || 0;
    const reader = resp.body.getReader();
    const chunks = [];
    let got = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(Buffer.from(value));
      got += value.length;
      if (total) onProgress(got / total);
    }
    return Buffer.concat(chunks);
  }

  async install() {
    let rel;
    try {
      const resp = await this.fetchOK(
        `https://api.github.com/repos/${REPO}/releases/latest`,
        'application/vnd.github+json'
      );
      rel = await resp.json();
    } catch (err) {
      throw new Error(`连不上 GitHub，无法获取 frog 发布信息（${err.message}）`);
    }
    const tag = String(rel?.tag_name || '');
    const zipName = `frogbot_${tag}_windows_amd64.zip`;
    const assets = Array.isArray(rel?.assets) ? rel.assets : [];
    const zipAsset = assets.find((a) => a.name === zipName);
    const sumsAsset = assets.find((a) => a.name === 'SHA256SUMS');
    if (!tag || !zipAsset || !sumsAsset) throw new Error('最新发布里没有找到 Windows 版 frog');

    const sumsText = (await (await this.fetchOK(sumsAsset.browser_download_url, 'text/plain')).text());
    const line = sumsText.split(/\r?\n/).find((l) => l.trim().endsWith(` ${zipName}`) || l.trim().endsWith(`*${zipName}`));
    const expected = line?.trim().split(/\s+/)[0]?.toLowerCase();
    if (!expected) throw new Error('SHA256SUMS 里没有这个安装包的校验值');

    this.setStatus({ phase: 'downloading', progress: 0 });
    const zip = await this.download(zipAsset.browser_download_url, (p) =>
      this.setStatus({ progress: p })
    );
    const actual = crypto.createHash('sha256').update(zip).digest('hex');
    if (actual !== expected) throw new Error('安装包校验失败（SHA256 不一致），已放弃安装');

    this.setStatus({ phase: 'installing', progress: 1 });
    const tmp = await fsp.mkdtemp(path.join(os.tmpdir(), 'frog-local-'));
    try {
      const zipPath = path.join(tmp, zipName);
      await fsp.writeFile(zipPath, zip);
      const tar = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'tar.exe');
      await new Promise((resolve, reject) => {
        execFile(tar, ['-xf', zipPath, '-C', tmp], { windowsHide: true }, (err) =>
          err ? reject(new Error(`解压失败：${err.message}`)) : resolve()
        );
      });
      const extracted = path.join(tmp, zipName.replace(/\.zip$/, ''), 'frogbot.exe');
      if (!fs.existsSync(extracted)) throw new Error('安装包里没有 frogbot.exe');
      await fsp.mkdir(this.dir, { recursive: true });
      const staged = `${this.exe}.new`;
      await fsp.copyFile(extracted, staged);
      await fsp.rename(staged, this.exe);
      await fsp.writeFile(path.join(this.dir, 'version.txt'), tag);
    } finally {
      fsp.rm(tmp, { recursive: true, force: true }).catch(() => {});
    }
  }

  // ---------- 运行 ----------

  readLogTail() {
    try {
      const text = fs.readFileSync(this.logFile, 'utf8');
      return text.trim().split(/\r?\n/).slice(-3).join(' ').slice(-300);
    } catch {
      return '';
    }
  }

  /** 启动本机 frog，返回实际端口。preferred 端口上已有 frog 时直接沿用。 */
  async start(preferred) {
    if (this.isRunning() && this.port) return this.port;
    if (preferred && (await this.health(preferred))) return preferred;
    if (!this.isInstalled()) throw new Error('本机 frog 还没有安装');

    const port = await this.freePort(preferred);
    await fsp.mkdir(this.dir, { recursive: true });
    const logFd = fs.openSync(this.logFile, 'a');
    let child;
    try {
      child = spawn(this.exe, ['-addr', `127.0.0.1:${port}`, '-no-browser'], {
        cwd: this.dir, // 避开当前目录下的 data 文件夹，使用 frogbot 默认数据目录
        windowsHide: true,
        stdio: ['ignore', logFd, logFd],
        env: { ...process.env, FROGBOT_NO_BROWSER: '1' },
      });
    } finally {
      fs.closeSync(logFd);
    }
    this.child = child;
    this.port = port;
    this.stopping = false;

    let spawnError = null;
    child.on('error', (err) => {
      spawnError = err;
    });
    child.on('exit', (code) => {
      if (this.child !== child) return;
      this.child = null;
      if (!this.stopping) {
        this.setStatus({ phase: 'error', error: `本机 frog 已退出（代码 ${code}）。${this.readLogTail()}` });
      } else {
        this.onChange();
      }
    });

    const deadline = Date.now() + 20_000;
    while (Date.now() < deadline) {
      if (spawnError) throw new Error(`无法启动 frogbot.exe：${spawnError.message}`);
      if (child.exitCode !== null) {
        throw new Error(`本机 frog 启动后立即退出了。${this.readLogTail()}`);
      }
      if (await this.health(port, 1000)) return port;
      await sleep(300);
    }
    this.stop();
    throw new Error('本机 frog 启动超时（20 秒）');
  }

  stop() {
    if (!this.child) return;
    this.stopping = true;
    try {
      this.child.kill();
    } catch {}
    this.child = null;
  }
}

module.exports = { LocalServer };
