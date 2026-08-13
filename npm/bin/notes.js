#!/usr/bin/env node
// Shim: forwards all args to the downloaded native binary.
'use strict';

const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const exe = process.platform === 'win32' ? 'notes-sync-windows-amd64.exe' : null;
const candidates = [
  path.join(__dirname, '..', 'bin', exe || 'notes-sync-' + process.platform + '-' + (process.arch === 'x64' ? 'amd64' : process.arch)),
];

const bin = candidates.find((p) => {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
});

if (!bin) {
  console.error('git-notes-sync: binary not found, re-run `npm install`');
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });
child.on('exit', (code) => process.exit(code));
