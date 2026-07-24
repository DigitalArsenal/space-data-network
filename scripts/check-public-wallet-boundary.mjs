import { readFile } from 'node:fs/promises';
const safeBaseline = process.argv.slice(2).includes('--safe-baseline');
const unknownArguments = process.argv.slice(2).filter((argument) => argument !== '--safe-baseline');

if (unknownArguments.length > 0) {
  throw new Error(`unknown argument: ${unknownArguments[0]}`);
}

const forbidden = [
  'wallet-ui.css',
  'walletModal',
  'walletPassword',
  'walletSeed',
  'rememberWallet',
  'walletPin',
  'credentialId',
  'deriveWallet',
  'privateKey',
  'generateKeyPair',
  'deriveSharedSecret',
  'initPKIDemo',
  'eciesEncrypt',
  'eciesDecrypt',
  'Math.random',
  '@noble/curves',
];

const scannedPaths = ['docs/index.html', 'docs/app.mjs'];
const scanned = new Map(await Promise.all(scannedPaths.map(async (path) => [
  path,
  await readFile(new URL(`../${path}`, import.meta.url), 'utf8'),
])));
const violations = [];

for (const [path, source] of scanned) {
  for (const token of forbidden) {
    if (source.includes(token)) violations.push(`${path}: forbidden token ${token}`);
  }
}

const landingPaths = ['docs/index.html', 'docs/onboarding.html'];
const landingPages = new Map(await Promise.all(landingPaths.map(async (path) => [
  path,
  await readFile(new URL(`../${path}`, import.meta.url), 'utf8'),
])));

if (safeBaseline) {
  for (const [path, source] of [...scanned, ...landingPages]) {
    for (const token of ['SDNWalletPublicClient', 'sdn-landing-web-v1']) {
      if (source.includes(token)) violations.push(`${path}: safe baseline contains ${token}`);
    }
  }
} else {
  for (const [path, source] of landingPages) {
    for (const token of [
      '<!-- SDN_CONSUMER_ASSETS_START -->',
      '<!-- SDN_CONSUMER_ASSETS_END -->',
      'sdn-landing-web-v1',
      'https://spacedatanetwork.org/wallet-callback.html',
    ]) {
      if (!source.includes(token)) violations.push(`${path}: missing immutable wallet marker ${token}`);
    }
    if (source.includes('https://spacedatastandards.org/sdn-stack-nav.js')) {
      violations.push(`${path}: mutable shared navigation URL is forbidden`);
    }
  }
}

if (violations.length > 0) {
  console.error(violations.join('\n'));
  process.exitCode = 1;
} else {
  console.log(`public wallet boundary: ${safeBaseline ? 'safe baseline' : 'pinned consumer'} PASS`);
}
