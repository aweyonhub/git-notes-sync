#!/usr/bin/env node
// git-notes-sync platform shim (npm registry distribution).
//
// The meta package has no binary and no install scripts. The platform
// sub-package (@aweyonhub/git-notes-sync-<os>-<arch>) carries the native binary
// and is pulled automatically by npm via optionalDependencies + os/cpu
// filtering. This shim locates it and spawns it.

const { spawnSync } = require('child_process');
const path = require('path');

function platform() {
  const map = {
    darwin: { x64: 'darwin-x64', arm64: 'darwin-arm64' },
    linux: { x64: 'linux-x64', arm64: 'linux-arm64' },
    win32: { x64: 'win32-x64', arm64: 'win32-arm64' },
  };
  const os = map[process.platform];
  if (!os) {
    console.error(`gns: unsupported platform ${process.platform}/${process.arch}`);
    process.exit(1);
  }
  const tag = os[process.arch];
  if (!tag) {
    console.error(`gns: unsupported architecture ${process.platform}/${process.arch}`);
    process.exit(1);
  }
  return tag;
}

let binPath;
try {
  const pkg = '@aweyonhub/git-notes-sync-' + platform();
  binPath = require.resolve(pkg + '/bin/gns' + (process.platform === 'win32' ? '.exe' : ''));
} catch (e) {
  console.error(
    'gns: native binary not found. The platform package did not install.\n' +
    '  Reinstall with: npm install -g @aweyonhub/git-notes-sync (registry) or\n' +
    '  npm install -g --install-links=true --foreground-scripts --allow-scripts=git-notes-sync github:aweyonhub/git-notes-sync\n' +
    '  (do NOT pass --no-optional — that disables the platform binary;\n' +
    '  verify with: npm ls @aweyonhub/git-notes-sync-' + platform() + ' -g)\n'
  );
  process.exit(1);
}

const r = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });
if (r.error) {
  console.error('gns: failed to run binary:', r.error.message);
  process.exit(1);
}
process.exit(r.status == null ? 1 : r.status);
