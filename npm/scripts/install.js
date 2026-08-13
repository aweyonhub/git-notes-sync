// gns postinstall downloader — go-npm style.
//
// Downloads the platform binary from GitHub Releases into npm/bin/,
// following the implementation notes of go-npm (URL template), Sentry CLI
// (redirects, proxy, temp file, checksum, version verify) and CodeWhale
// (overrides, failure guidance). Pure Node standard library — no deps.
//
// URL template (go-npm style):
//   https://github.com/<repo>/releases/download/v<version>/<asset>
//   <asset> = gns-<platform>-<arch>[.exe]
//   <platform> = darwin|linux|windows, <arch> = amd64|arm64
//
// Overrides (environment variables):
//   GNS_VERSION            force version (default: package.json version)
//   GNS_REPO               GitHub repo "owner/name"
//   GNS_RELEASE_BASE_URL   full base URL (mirrors/intranets)
//   GNS_CHECKSUM_URL       checksums.txt URL (default: <base>/checksums.txt)
//   GNS_SKIP_INSTALL=1     skip download (bin already present)
//   HTTPS_PROXY / HTTP_PROXY   forward proxy (CONNECT tunnel)
'use strict';

const https = require('https');
const http = require('http');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { spawnSync } = require('child_process');

const PKG = require('../../package.json');
const env = (k) => (process.env[k] || '').trim();
const VERSION = (env('GNS_VERSION') || PKG.version).replace(/^v/, '');
const REPO = env('GNS_REPO') || 'aweyonhub/git-notes-sync';
const BASE_URL =
  env('GNS_RELEASE_BASE_URL') || `https://github.com/${REPO}/releases/download/v${VERSION}`;
const CHECKSUM_URL = env('GNS_CHECKSUM_URL') || `${BASE_URL}/checksums.txt`;

// npm platform-arch → release asset name (go-npm platform map)
const MAP = {
  'darwin-arm64': 'gns-darwin-arm64',
  'darwin-x64': 'gns-darwin-amd64',
  'linux-arm64': 'gns-linux-arm64',
  'linux-x64': 'gns-linux-amd64',
  'win32-arm64': 'gns-windows-arm64.exe',
  'win32-x64': 'gns-windows-amd64.exe',
};

const key = `${process.platform}-${os.arch()}`;
const asset = MAP[key];
if (!asset) {
  console.error(`gns: unsupported platform ${key}`);
  console.error(`  supported: ${Object.keys(MAP).join(', ')}`);
  console.error('  install a compatible Node.js or download the binary manually from:');
  console.error(`  ${BASE_URL}`);
  process.exit(1);
}

if (process.env.GNS_SKIP_INSTALL === '1') {
  console.log(`gns: GNS_SKIP_INSTALL=1, skipping download of ${asset}`);
  process.exit(0);
}

const dest = path.join(__dirname, '..', 'bin', 'gns' + (process.platform === 'win32' ? '.exe' : ''));
const downloadUrl = `${BASE_URL}/${asset}`;
const tmp = dest + `.tmp-${process.pid}`;

// ---- proxy (CONNECT tunnel, standard library only) ----
function proxyFor(url) {
  const proxy = process.env.HTTPS_PROXY || process.env.https_proxy || process.env.HTTP_PROXY || process.env.http_proxy;
  if (!proxy) return null;
  try {
    const u = new URL(proxy);
    return { host: u.hostname, port: u.port || 8080 };
  } catch {
    return null;
  }
}

// get follows redirects (up to 5) and streams the response to onResponse.
function get(currentUrl, redirectsLeft, onResponse) {
  const u = new URL(currentUrl);
  const proxy = u.protocol === 'https:' ? proxyFor(currentUrl) : null;
  const isHttps = u.protocol === 'https:';

  const request = (socket) => {
    const mod = isHttps ? https : http;
    const opts = {
      hostname: u.hostname,
      port: u.port || (isHttps ? 443 : 80),
      path: u.pathname + u.search,
      headers: { 'user-agent': `git-notes-sync/${VERSION} (install)` },
      ...(socket ? { createConnection: () => socket } : {}),
    };
    const req = mod.request(opts, (res) => {
      const loc = res.headers.location;
      if (res.statusCode >= 300 && res.statusCode < 400 && loc && redirectsLeft > 0) {
        res.resume();
        return get(new URL(loc, currentUrl).toString(), redirectsLeft - 1, onResponse);
      }
      onResponse(res);
    });
    req.on('error', (e) => fail(`request to ${currentUrl} failed: ${e.message}`));
    req.end();
  };

  if (proxy) {
    // CONNECT tunnel through the proxy
    const preq = http.request({
      hostname: proxy.host,
      port: proxy.port,
      method: 'CONNECT',
      path: `${u.hostname}:${u.port || (isHttps ? 443 : 80)}`,
    });
    preq.on('connect', (res, socket) => {
      if (res.statusCode !== 200) {
        socket.destroy();
        return fail(`proxy CONNECT failed: ${res.statusCode}`);
      }
      socket.on('error', (e) => fail(`proxy tunnel error: ${e.message}`));
      request(socket);
    });
    preq.on('error', (e) => fail(`proxy connection failed: ${e.message}`));
    preq.end();
  } else {
    request();
  }
}

function fail(message) {
  console.error(`gns: ${message}`);
  console.error('');
  console.error('  Possible causes:');
  console.error(`  - no network access to ${BASE_URL}`);
  console.error('  - release v' + VERSION + ' does not exist or has no asset ' + asset);
  console.error('  - corporate proxy: set HTTPS_PROXY / HTTP_PROXY');
  console.error('  - mirror: set GNS_RELEASE_BASE_URL / GNS_CHECKSUM_URL');
  console.error('  - skip: set GNS_SKIP_INSTALL=1 (binary must already exist)');
  process.exit(1);
}

function sha256(file) {
  return new Promise((resolve, reject) => {
    const h = crypto.createHash('sha256');
    const s = fs.createReadStream(file);
    s.on('error', reject);
    s.on('data', (d) => h.update(d));
    s.on('end', () => resolve(h.digest('hex')));
  });
}

// fetchExpectedChecksum downloads checksums.txt and returns the sha256 for
// our asset (or null when the manifest is unavailable — degraded but allowed).
function fetchExpectedChecksum() {
  return new Promise((resolve) => {
    get(CHECKSUM_URL, 5, (res) => {
      if (res.statusCode !== 200) return resolve(null);
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (d) => (body += d));
      res.on('end', () => {
        for (const line of body.split('\n')) {
          const [hash, name] = line.trim().split(/\s+/);
          if (name && name.replace(/^\*/, '') === asset && /^[0-9a-f]{64}$/i.test(hash)) {
            return resolve(hash.toLowerCase());
          }
        }
        resolve(null);
      });
    });
  });
}

function verify() {
  // run the downloaded binary to confirm it works and reports the version
  const r = spawnSync(dest, ['--version'], { encoding: 'utf8' });
  if (r.status !== 0 || !r.stdout) {
    fs.rmSync(dest, { force: true });
    return fail(`downloaded binary failed to run: ${(r.stderr || r.stdout || 'no output').trim()}`);
  }
  if (!r.stdout.includes(VERSION)) {
    fs.rmSync(dest, { force: true });
    return fail(`binary version mismatch: got "${r.stdout.trim()}", expected ${VERSION}`);
  }
  console.log(`gns: ${r.stdout.trim()} installed at ${dest}`);
}

console.log(`gns: downloading ${asset} (v${VERSION})...`);
fs.mkdirSync(path.dirname(dest), { recursive: true });

get(downloadUrl, 5, (res) => {
  if (res.statusCode !== 200) {
    return fail(`download failed (HTTP ${res.statusCode}) from ${downloadUrl}`);
  }
  const file = fs.createWriteStream(tmp);
  res.pipe(file);
  file.on('finish', async () => {
    file.close(async () => {
      try {
        const expected = await fetchExpectedChecksum();
        const actual = await sha256(tmp);
        if (expected && actual !== expected) {
          fs.rmSync(tmp, { force: true });
          return fail(`checksum mismatch for ${asset}:\n  expected ${expected}\n  actual   ${actual}`);
        }
        fs.renameSync(tmp, dest);
        if (process.platform !== 'win32') fs.chmodSync(dest, 0o755);
        verify();
      } catch (e) {
        fs.rmSync(tmp, { force: true });
        fail(`install error: ${e.message}`);
      }
    });
  });
  file.on('error', (e) => {
    fs.rmSync(tmp, { force: true });
    fail(`write error: ${e.message}`);
  });
});
