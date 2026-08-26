#!/usr/bin/env node
'use strict';

// NOTE: this argv mapping duplicates cmd/gns/main.go expandGnmAlias and
// packages/meta/bin/gnm.js - keep the three in sync.

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const exe = path.join(__dirname, 'gns' + (process.platform === 'win32' ? '.exe' : ''));
if (!fs.existsSync(exe)) {
  console.error('gnm: binary not found — re-run `npm install`');
  process.exit(1);
}

const userArgs = process.argv.slice(2);
const args = userArgs[0] === 'config'
  ? ['map-config', ...userArgs.slice(1)]
  : ['map', ...userArgs];
const result = spawnSync(exe, args, { stdio: 'inherit' });
process.exit(result.status === null ? 1 : result.status);
