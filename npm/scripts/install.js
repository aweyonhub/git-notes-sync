// Downloads the platform binary during `npm install` and places it at a
// fixed path (bin/gns.exe). The package.json "bin" field points directly at
// that file, so npm links the native binary itself — no JS routing at
// runtime. Override GNS_ASSET_URL to mirror the release assets (e.g. an
// intranet or proxy).
'use strict';

const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');
const os = require('os');

const VERSION = require('../package.json').version;
const REPO = 'aweyonhub/git-notes-sync';

// platform map: npm platform-arch → release asset name
const MAP = {
  'darwin-arm64': 'gns-darwin-arm64',
  'darwin-x64': 'gns-darwin-amd64',
  'win32-x64': 'gns-windows-amd64.exe',
  'linux-x64': 'gns-linux-amd64',
  'linux-arm64': 'gns-linux-arm64',
};

const key = `${process.platform}-${os.arch()}`;
const asset = MAP[key];
if (!asset) {
  console.error(`git-notes-sync: unsupported platform ${key}`);
  console.error('supported: ' + Object.keys(MAP).join(', '));
  process.exit(1);
}

// fixed filename on every platform (npm bin links point here)
const dest = path.join(__dirname, '..', 'bin', 'gns.exe');
const url =
  process.env.GNS_ASSET_URL ||
  `https://github.com/${REPO}/releases/download/v${VERSION}/${asset}`;

fs.mkdirSync(path.dirname(dest), { recursive: true });

console.log(`git-notes-sync: downloading ${asset} (v${VERSION})...`);
const file = fs.createWriteStream(dest);
const client = url.startsWith('https:') ? https : http;

client
  .get(url, (res) => {
    if (res.statusCode !== 200) {
      console.error(`git-notes-sync: download failed (HTTP ${res.statusCode}) from ${url}`);
      process.exit(1);
    }
    res.pipe(file);
    file.on('finish', () => {
      file.close(() => {
        if (process.platform !== 'win32') {
          fs.chmodSync(dest, 0o755);
        }
        console.log(`git-notes-sync: installed ${dest}`);
      });
    });
  })
  .on('error', (e) => {
    console.error('git-notes-sync: ' + e.message);
    process.exit(1);
  });
