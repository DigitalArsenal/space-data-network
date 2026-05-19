#!/usr/bin/env node

import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '..');
const syncPairs = [
  {
    upstream: 'webui/src/components/about-ipfs/AboutIpfs.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/components/about-ipfs/AboutIpfs.js',
  },
  {
    upstream: 'webui/src/components/about-webui/AboutWebUI.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/components/about-webui/AboutWebUI.js',
  },
  {
    upstream: 'webui/src/components/connected/Connected.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/components/connected/Connected.js',
  },
  {
    upstream: 'webui/src/components/is-connected/IsConnected.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/components/is-connected/IsConnected.js',
  },
  {
    upstream: 'webui/src/navigation/NavBar.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/navigation/NavBar.js',
  },
  {
    upstream: 'webui/src/status/StatusConnected.js',
    vendor: 'sdn-js/ui/src/upstream-webui/vendor/status/StatusConnected.js',
  },
];

const checkOnly = process.argv.includes('--check');
const staleFiles = [];

for (const pair of syncPairs) {
  const upstreamPath = path.join(repoRoot, pair.upstream);
  const vendorPath = path.join(repoRoot, pair.vendor);
  const upstreamContents = await fs.readFile(upstreamPath);
  let vendorContents = null;

  try {
    vendorContents = await fs.readFile(vendorPath);
  } catch (error) {
    if (error?.code !== 'ENOENT') {
      throw error;
    }
  }

  const isInSync = vendorContents !== null && Buffer.compare(upstreamContents, vendorContents) === 0;
  if (isInSync) {
    continue;
  }

  if (checkOnly) {
    staleFiles.push(pair.vendor);
    continue;
  }

  await fs.mkdir(path.dirname(vendorPath), { recursive: true });
  await fs.writeFile(vendorPath, upstreamContents);
}

if (staleFiles.length > 0) {
  console.error('Vendored upstream WebUI files are out of sync:');
  for (const staleFile of staleFiles) {
    console.error(`- ${staleFile}`);
  }
  process.exit(1);
}

if (!checkOnly) {
  console.log('Synced upstream WebUI branding slice into sdn-js.');
} else {
  console.log('Vendored upstream WebUI files are in sync.');
}
