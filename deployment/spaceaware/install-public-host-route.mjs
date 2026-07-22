#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import {
  constants,
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  unlink,
} from 'node:fs/promises';
import { basename, dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

const oldMap = `map $http_upgrade $sdn_upgrade_backend {
    default http://127.0.0.1:5020;
    websocket http://127.0.0.1:18080;
}`;

const hostAwareMaps = `map $host $sdn_http_backend {
    default http://127.0.0.1:5020;
    sdn.spaceaware.io https://127.0.0.1:18443;
}

map $http_upgrade $sdn_upgrade_backend {
    default $sdn_http_backend;
    websocket http://127.0.0.1:18080;
}`;

const routedLocations = [
  'location = /index.html',
  'location ^~ /assets/',
  'location ^~ /TestData/',
  'location /',
];
const lockHeldEnvironment = 'SDN_PUBLIC_HOST_ROUTE_LOCK_HELD';

function fail(message) {
  throw new Error(message);
}

function formatError(error, indent = '') {
  const own = `${error?.stack || error}`
    .split('\n')
    .map((line) => indent + line)
    .join('\n');
  if (!(error instanceof AggregateError)) return own;
  return [
    own,
    ...error.errors.map((nested, index) => (
      `${indent}cause ${index + 1}:\n${formatError(nested, indent + '  ')}`
    )),
  ].join('\n');
}

function count(text, value) {
  return text.split(value).length - 1;
}

function locateBlock(text, header) {
  const marker = `    ${header} {`;
  if (count(text, marker) !== 1) {
    fail(`unexpected nginx config: expected exactly one ${header} block`);
  }
  const start = text.indexOf(marker);
  let depth = 0;
  let opened = false;
  for (let index = start; index < text.length; index += 1) {
    if (text[index] === '{') {
      depth += 1;
      opened = true;
    } else if (text[index] === '}') {
      depth -= 1;
      if (opened && depth === 0) {
        return { start, end: index + 1, value: text.slice(start, index + 1) };
      }
      if (depth < 0) break;
    }
  }
  fail(`unexpected nginx config: unterminated ${header} block`);
}

function routeLocation(text, header) {
  const block = locateBlock(text, header);
  const oldProxy = 'proxy_pass http://127.0.0.1:5020;';
  const newProxy = 'proxy_pass $sdn_http_backend;';
  const oldCount = count(block.value, oldProxy);
  const newCount = count(block.value, newProxy);
  if (oldCount === 1 && newCount === 0) {
    const nextBlock = block.value.replace(oldProxy, newProxy);
    return text.slice(0, block.start) + nextBlock + text.slice(block.end);
  }
  if (oldCount === 0 && newCount === 1) return text;
  fail(`unexpected nginx config: ${header} must contain exactly one recognized proxy_pass`);
}

export function transformConfig(text) {
  if (count(text, 'server_name spaceaware.io www.spaceaware.io sdn.spaceaware.io;') !== 1) {
    fail('unexpected nginx config: shared SpaceAware/SDN server_name is missing or duplicated');
  }

  let next = text;
  const oldMapCount = count(next, oldMap);
  const newMapCount = count(next, hostAwareMaps);
  if (oldMapCount === 1 && newMapCount === 0) {
    next = next.replace(oldMap, hostAwareMaps);
  } else if (oldMapCount === 0 && newMapCount === 1) {
    // Already transformed; validate every routed location below.
  } else {
    fail('unexpected nginx config: public backend map is missing, duplicated, or partially edited');
  }

  for (const header of routedLocations) next = routeLocation(next, header);

  const root = locateBlock(next, 'location = /').value;
  if (count(root, 'proxy_pass $sdn_upgrade_backend;') !== 1) {
    fail('unexpected nginx config: websocket-aware root route is missing');
  }
  const moduleDelivery = locateBlock(next, 'location ^~ /api/module-delivery/').value;
  if (count(moduleDelivery, 'proxy_pass https://127.0.0.1:18443;') !== 1) {
    fail('unexpected nginx config: module-delivery route is missing');
  }
  return next;
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

async function readRegularFileNoFollow(path) {
  const before = await lstat(path);
  if (!before.isFile() || before.isSymbolicLink()) {
    fail(`refusing non-regular nginx config: ${path}`);
  }
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const opened = await handle.stat();
    if (!opened.isFile() || opened.dev !== before.dev || opened.ino !== before.ino) {
      fail(`nginx config changed while opening: ${path}`);
    }
    return { bytes: await handle.readFile(), stat: opened };
  } finally {
    await handle.close();
  }
}

async function writeReplacement(path, bytes, sourceStat) {
  const handle = await open(
    path,
    constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
    sourceStat.mode & 0o7777,
  );
  try {
    await handle.chown(sourceStat.uid, sourceStat.gid);
    await handle.writeFile(bytes);
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function ensureBackupDirectory(configPath, backupDir) {
  if (!isAbsolute(backupDir)) fail(`backup directory must be absolute: ${backupDir}`);
  const configDirectory = resolve(dirname(configPath));
  const resolvedBackup = resolve(backupDir);
  if (resolvedBackup === configDirectory || resolvedBackup.startsWith(configDirectory + sep)) {
    fail(`backup directory must be outside the Nginx include directory: ${resolvedBackup}`);
  }
  await mkdir(resolvedBackup, { recursive: true, mode: 0o700 });
  const backupStat = await lstat(resolvedBackup);
  if (!backupStat.isDirectory() || backupStat.isSymbolicLink()) {
    fail(`refusing non-directory backup target: ${resolvedBackup}`);
  }
  if ((backupStat.mode & 0o077) !== 0) {
    fail(`backup directory must not grant group or other access: ${resolvedBackup}`);
  }
  return resolvedBackup;
}

async function validateLockPath(configPath, lockPath) {
  if (!isAbsolute(lockPath)) fail(`installer lock path must be absolute: ${lockPath}`);
  const configDirectory = resolve(dirname(configPath));
  const resolvedLock = resolve(lockPath);
  if (resolvedLock === configDirectory || resolvedLock.startsWith(configDirectory + sep)) {
    fail(`installer lock must be outside the Nginx include directory: ${resolvedLock}`);
  }
  const parent = await lstat(dirname(resolvedLock));
  if (!parent.isDirectory() || parent.isSymbolicLink()) {
    fail(`refusing unsafe installer lock directory: ${dirname(resolvedLock)}`);
  }
  try {
    const existing = await lstat(resolvedLock);
    if (!existing.isFile() || existing.isSymbolicLink()) {
      fail(`refusing unsafe installer lock target: ${resolvedLock}`);
    }
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  return resolvedLock;
}

function runChecked(command, args, description) {
  const result = spawnSync(command, args, { encoding: 'utf8' });
  if (result.error || result.status !== 0) {
    const detail = [result.error?.message, result.stdout, result.stderr]
      .filter(Boolean)
      .join('\n')
      .trim();
    fail(`${description} failed${detail ? `: ${detail}` : ''}`);
  }
}

async function assertCurrentDigest(configPath, expectedDigest, phase) {
  const current = await readRegularFileNoFollow(configPath);
  const actualDigest = sha256(current.bytes);
  if (actualDigest !== expectedDigest) {
    fail(`nginx config changed ${phase}: expected ${expectedDigest}, got ${actualDigest}`);
  }
  return current;
}

async function replaceWithBytesIfCurrent(
  configPath,
  bytes,
  sourceStat,
  suffix,
  expectedCurrentDigest,
) {
  const temporary = join(
    dirname(configPath),
    `.${basename(configPath)}.${suffix}.${process.pid}.${Date.now()}`,
  );
  try {
    await writeReplacement(temporary, bytes, sourceStat);
    if (expectedCurrentDigest) {
      await assertCurrentDigest(configPath, expectedCurrentDigest, 'immediately before replacement');
    }
    await rename(temporary, configPath);
    await assertCurrentDigest(configPath, sha256(bytes), 'immediately after replacement');
  } finally {
    await unlink(temporary).catch((error) => {
      if (error.code !== 'ENOENT') throw error;
    });
  }
}

async function rollbackOwnedConfig({
  trigger,
  description,
  configPath,
  original,
  candidateDigest,
  reload,
}) {
  try {
    await replaceWithBytesIfCurrent(
      configPath,
      original.bytes,
      original.stat,
      'rollback',
      candidateDigest,
    );
    if (reload) {
      runChecked('nginx', ['-t'], 'rollback nginx validation');
      runChecked('systemctl', ['reload', 'nginx'], 'rollback nginx reload');
      await assertCurrentDigest(configPath, sha256(original.bytes), 'after rollback reload');
    }
  } catch (rollbackError) {
    throw new AggregateError([trigger, rollbackError], `${description} and rollback failed`);
  }
  throw trigger;
}

async function install(configPath, backupDir) {
  const original = await readRegularFileNoFollow(configPath);
  const originalText = original.bytes.toString('utf8');
  const transformedText = transformConfig(originalText);
  const originalDigest = sha256(original.bytes);
  if (transformedText === originalText) {
    await assertCurrentDigest(configPath, originalDigest, 'before validation');
    runChecked('nginx', ['-t'], 'nginx validation');
    await assertCurrentDigest(configPath, originalDigest, 'between validation and reload');
    runChecked('systemctl', ['reload', 'nginx'], 'nginx reload');
    await assertCurrentDigest(configPath, originalDigest, 'after reload');
    process.stdout.write(`SDN public host route already installed and reloaded in ${configPath}\n`);
    return;
  }

  await assertCurrentDigest(configPath, originalDigest, 'during preflight');

  const digest = sha256(original.bytes).slice(0, 12);
  const safeBackupDir = await ensureBackupDirectory(configPath, backupDir);
  const backupPath = join(
    safeBackupDir,
    `${basename(configPath)}.pre-sdn-public-route.${digest}.${Date.now()}`,
  );
  await writeReplacement(backupPath, original.bytes, original.stat);
  const candidateBytes = Buffer.from(transformedText);
  const candidateDigest = sha256(candidateBytes);
  await replaceWithBytesIfCurrent(
    configPath,
    candidateBytes,
    original.stat,
    'candidate',
    originalDigest,
  );

  try {
    runChecked('nginx', ['-t'], 'nginx validation');
    await assertCurrentDigest(configPath, candidateDigest, 'between validation and reload');
  } catch (error) {
    await rollbackOwnedConfig({
      trigger: error,
      description: 'nginx validation',
      configPath,
      original,
      candidateDigest,
      reload: false,
    });
  }

  try {
    runChecked('systemctl', ['reload', 'nginx'], 'nginx reload');
    await assertCurrentDigest(configPath, candidateDigest, 'after reload');
  } catch (error) {
    await rollbackOwnedConfig({
      trigger: error,
      description: 'nginx reload',
      configPath,
      original,
      candidateDigest,
      reload: true,
    });
  }

  process.stdout.write(`Installed SDN public host route in ${configPath}; backup: ${backupPath}\n`);
}

function parseArgs(argv) {
  let configPath = '/etc/nginx/sites-enabled/spaceaware';
  let backupDir = '/var/backups/spacedatanetwork/nginx';
  let lockPath = '/run/sdn-public-host-route.lock';
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === '--config' && argv[index + 1]) {
      configPath = argv[index + 1];
      index += 1;
    } else if (argv[index] === '--backup-dir' && argv[index + 1]) {
      backupDir = argv[index + 1];
      index += 1;
    } else if (argv[index] === '--lock-path' && argv[index + 1]) {
      lockPath = argv[index + 1];
      index += 1;
    } else {
      fail(`usage: ${basename(process.argv[1])} [--config PATH] [--backup-dir PATH] [--lock-path PATH]`);
    }
  }
  return { configPath, backupDir, lockPath };
}

async function main(argv) {
  const { configPath, backupDir, lockPath } = parseArgs(argv);
  if (process.env[lockHeldEnvironment] === '1') {
    await install(configPath, backupDir);
    return;
  }

  const safeLockPath = await validateLockPath(configPath, lockPath);
  const result = spawnSync('flock', [
    '--exclusive',
    '--nonblock',
    '--conflict-exit-code', '75',
    safeLockPath,
    process.execPath,
    process.argv[1],
    ...argv,
  ], {
    env: { ...process.env, [lockHeldEnvironment]: '1' },
    stdio: 'inherit',
  });
  if (result.error) fail(`could not execute installer lock: ${result.error.message}`);
  if (result.status === 75) fail(`installer lock is busy: ${safeLockPath}`);
  if (result.status !== 0) process.exitCode = result.status ?? 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await main(process.argv.slice(2));
  } catch (error) {
    process.stderr.write(`${formatError(error)}\n`);
    process.exitCode = 1;
  }
}
