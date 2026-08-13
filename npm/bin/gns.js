#!/usr/bin/env node
// gns — launcher for the binary downloaded by scripts/install.js.
// (go-npm style: postinstall places the platform binary at ./gns[.exe])
'use strict';

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const exe = path.join(__dirname, 'gns' + (process.platform === 'win32' ? '.exe' : ''));
if (!fs.existsSync(exe)) {
  console.error('gns: binary not found — re-run `npm install` (postinstall downloads it)');
  process.exit(1);
}

const r = spawnSync(exe, process.argv.slice(2), { stdio: 'inherit' });
process.exit(r.status === null ? 1 : r.status);
