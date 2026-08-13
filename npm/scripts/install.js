// Downloads the platform binary from GitHub Releases during `npm install`.
// The npm package itself contains no binary, only this installer + a shim.
'use strict';

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');

const VERSION = require('../package.json').version;
const REPO = 'git-notes-sync/git-notes-sync'; // TODO: replace with your GitHub user/org

// platform map: npm platform-arch → release asset name
const MAP = {
  'darwin-arm64': 'notes-sync-darwin-arm64',
  'darwin-x64': 'notes-sync-darwin-amd64',
  'win32-x64': 'notes-sync-windows-amd64.exe',
  'linux-x64': 'notes-sync-linux-amd64',
  'linux-arm64': 'notes-sync-linux-arm64',
};

const key = `${process.platform}-${os.arch()}`;
const asset = MAP[key];
if (!asset) {
  console.error(`git-notes-sync: unsupported platform ${key}`);
  console.error('supported: ' + Object.keys(MAP).join(', '));
  process.exit(1);
}

const dest = path.join(__dirname, '..', 'bin', asset);
const url = `https://github.com/${REPO}/releases/download/v${VERSION}/${asset}`;

fs.mkdirSync(path.dirname(dest), { recursive: true });

console.log(`git-notes-sync: downloading ${asset} (v${VERSION})...`);
const file = fs.createWriteStream(dest);

https
  .get(url, (res) => {
    if (res.statusCode !== 200) {
      console.error(`git-notes-sync: download failed (HTTP ${res.statusCode}) from ${url}`);
      process.exit(1);
    }
    res.pipe(file);
    file.on('finish', () => {
      file.close(() => {
        try {
          fs.chmodSync(dest, 0o755);
        } catch (e) {
          /* Windows: chmod is a no-op */
        }
        console.log(`git-notes-sync: installed ${dest}`);
      });
    });
  })
  .on('error', (e) => {
    console.error('git-notes-sync: ' + e.message);
    process.exit(1);
  });
