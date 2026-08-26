#!/usr/bin/env node
'use strict';

const userArgs = process.argv.slice(2);
const args = userArgs[0] === 'config'
  ? ['map-config', ...userArgs.slice(1)]
  : ['map', ...userArgs];
process.argv.splice(2, userArgs.length, ...args);
require('./gns.js');
