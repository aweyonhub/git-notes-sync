#!/usr/bin/env node
'use strict';

// NOTE: this argv mapping duplicates cmd/gns/main.go expandGnmAlias and
// npm/bin/gnm.js - keep the three in sync.

const userArgs = process.argv.slice(2);
const args = userArgs[0] === 'config'
  ? ['map-config', ...userArgs.slice(1)]
  : ['map', ...userArgs];
process.argv.splice(2, userArgs.length, ...args);
require('./gns.js');
