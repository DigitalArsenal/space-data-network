#!/usr/bin/env node

import { createHash } from 'node:crypto';
import {
  copyFileSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const legacyModuleIDs = new Set([
  'com.orbpro.celestrak-supgp',
  'com.orbpro.gps-source',
]);

const legacyFlowIDs = new Set([
  'org.sdn.flows.od-supplemental-omm',
  'com.digitalarsenal.flows.celestrak-gp-ingest',
  'com.digitalarsenal.flows.celestrak-satcat-ingest',
  'com.digitalarsenal.flows.celestrak-spw-ingest',
]);

const legacySources = new Set(['role:omm', 'role:celestrak']);
const allLegacyIDs = new Set([...legacyModuleIDs, ...legacyFlowIDs]);

export function inspectLegacyState(repoInput) {
  const repo = validateRepo(repoInput);
  const sdnDir = join(repo, 'sdn');
  const modulesDir = join(sdnDir, 'modules');
  const flowsDir = join(sdnDir, 'flows');
  const moduleRegistryPath = join(modulesDir, 'installed.json');
  const flowRegistryPath = join(flowsDir, 'installed-flows.json');
  const policyPaths = [
    join(sdnDir, 'capability_policy.json'),
    join(modulesDir, 'capability_policy.json'),
  ].filter((path, index, paths) => paths.indexOf(path) === index);

  const moduleRegistry = readEnvelope(moduleRegistryPath, 'modules');
  const flowRegistry = readEnvelope(flowRegistryPath, 'flows');
  const legacyModules = moduleRegistry.value.modules.filter(isLegacyModule);
  const legacyFlows = flowRegistry.value.flows.filter(isLegacyFlow);
  const legacyHashes = new Set();
  const externalRefs = [];
  const filesToQuarantine = new Set();

  for (const entry of legacyModules) {
    addHash(legacyHashes, entry.content_hash);
  }

  for (const entry of legacyFlows) {
    const resolvedRef = resolveFlowRef(repo, flowsDir, entry.ref);
    if (!resolvedRef) {
      if (typeof entry.ref === 'string' && entry.ref.trim() !== '') {
        externalRefs.push({ id: entry.id, ref: entry.ref, reason: 'not found' });
      }
      continue;
    }
    const runtimePath = join(resolvedRef, 'runtime.wasm');
    if (isRegularFile(runtimePath)) {
      addHash(legacyHashes, hashPortableArtifact(readFileSync(runtimePath)));
    }
    if (isWithin(repo, resolvedRef) && isSafeFlowBundlePath(flowsDir, resolvedRef)) {
      filesToQuarantine.add(resolvedRef);
    } else {
      externalRefs.push({ id: entry.id, ref: entry.ref, resolved: resolvedRef, reason: 'outside node repository' });
    }
  }

  const explicitSupplementalDir = join(flowsDir, 'od-supplemental-omm');
  if (existsNoSymlink(explicitSupplementalDir)) {
    filesToQuarantine.add(explicitSupplementalDir);
    const runtimePath = join(explicitSupplementalDir, 'runtime.wasm');
    if (isRegularFile(runtimePath)) {
      addHash(legacyHashes, hashPortableArtifact(readFileSync(runtimePath)));
    }
  }

  for (const id of new Set([
    ...allLegacyIDs,
    ...legacyModules.map(({ id }) => id),
    ...legacyFlows.map(({ id }) => id),
  ])) {
    if (!isSafeIdentifier(id)) continue;
    const configPath = join(modulesDir, `${id}.json`);
    if (isRegularFile(configPath)) filesToQuarantine.add(configPath);
  }

  const dropinDir = join(modulesDir, 'install');
  if (existsNoSymlink(dropinDir)) {
    for (const entry of readdirSync(dropinDir, { withFileTypes: true })) {
      if (!entry.isFile() || !entry.name.toLowerCase().endsWith('.wasm')) continue;
      const path = join(dropinDir, entry.name);
      const hash = hashPortableArtifact(readFileSync(path));
      if (legacyHashes.has(hash)) filesToQuarantine.add(path);
    }
  }

  const ledgerPath = join(sdnDir, 'celestrak_fetch_ledger.json');
  if (isRegularFile(ledgerPath)) filesToQuarantine.add(ledgerPath);

  const policyFiles = policyPaths.map((path) => {
    const envelope = readEnvelope(path, 'approvals', { version: 1 });
    const revoke = envelope.value.approvals.filter((approval) => isLegacyApproval(approval, legacyHashes));
    return { path, ...envelope, revoke };
  });

  const approvalsToRevoke = policyFiles.reduce((total, policy) => total + policy.revoke.length, 0);
  const report = {
    repo,
    legacyModules: legacyModules.map(summarizeRegistryEntry),
    legacyFlows: legacyFlows.map(summarizeRegistryEntry),
    legacyHashes: [...legacyHashes].sort(),
    approvalsToRevoke,
    filesToQuarantine: [...filesToQuarantine].sort().map((path) => relative(repo, path)),
    externalRefs,
  };
  report.clean = report.legacyModules.length === 0
    && report.legacyFlows.length === 0
    && report.approvalsToRevoke === 0
    && report.filesToQuarantine.length === 0;

  return {
    report,
    state: {
      repo,
      moduleRegistryPath,
      moduleRegistry,
      flowRegistryPath,
      flowRegistry,
      policyFiles,
      legacyModules,
      legacyFlows,
      filesToQuarantine: [...filesToQuarantine].sort(),
    },
  };
}

export function applyLegacyStatePurge(repoInput, backupInput) {
  if (!backupInput) throw new Error('--backup-dir is required with --apply');
  const { report, state } = inspectLegacyState(repoInput);
  const backupDir = validateBackupDir(state.repo, backupInput);

  if (report.clean) {
    return { ...report, mode: 'apply', backupDir, changed: false };
  }

  mkdirSync(backupDir, { recursive: false, mode: 0o700 });
  const quarantine = state.filesToQuarantine.filter((path) => !hasQuarantinedParent(path, state.filesToQuarantine));
  const rewrites = [];

  const moduleIDs = new Set(state.legacyModules.map(({ id }) => id));
  const nextModules = state.moduleRegistry.value.modules.filter((entry) => !moduleIDs.has(entry.id));
  if (state.moduleRegistry.exists && nextModules.length !== state.moduleRegistry.value.modules.length) {
    rewrites.push({
      path: state.moduleRegistryPath,
      value: { ...state.moduleRegistry.value, modules: nextModules },
    });
  }

  const flowIDs = new Set(state.legacyFlows.map(({ id }) => id));
  const nextFlows = state.flowRegistry.value.flows.filter((entry) => !flowIDs.has(entry.id));
  if (state.flowRegistry.exists && nextFlows.length !== state.flowRegistry.value.flows.length) {
    rewrites.push({
      path: state.flowRegistryPath,
      value: { ...state.flowRegistry.value, flows: nextFlows },
    });
  }

  for (const policy of state.policyFiles) {
    if (!policy.exists || policy.revoke.length === 0) continue;
    const revoked = new Set(policy.revoke);
    const approvals = policy.value.approvals.filter((approval) => !revoked.has(approval));
    rewrites.push({ path: policy.path, value: { ...policy.value, approvals } });
  }

  // Complete the recovery copy before changing the live repository. A failed
  // backup leaves the node state untouched.
  for (const { path } of rewrites) backupPath(state.repo, backupDir, path);
  for (const path of quarantine) backupPath(state.repo, backupDir, path);

  for (const { path, value } of rewrites) atomicWriteJSON(path, value);
  for (const path of quarantine) rmSync(path, { recursive: true, force: false });

  return {
    ...report,
    mode: 'apply',
    backupDir,
    changed: rewrites.length > 0 || quarantine.length > 0,
    rewritten: rewrites.map(({ path }) => relative(state.repo, path)),
    quarantined: quarantine.map((path) => relative(state.repo, path)),
  };
}

function parseArgs(argv) {
  const options = { apply: false, json: false };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--apply') options.apply = true;
    else if (arg === '--json') options.json = true;
    else if (arg === '--repo') options.repo = argv[++index];
    else if (arg === '--backup-dir') options.backupDir = argv[++index];
    else if (arg === '--help' || arg === '-h') options.help = true;
    else throw new Error(`unknown argument: ${arg}`);
  }
  if (!options.help && !options.repo) throw new Error('--repo is required');
  if (options.apply && !options.backupDir) throw new Error('--backup-dir is required with --apply');
  return options;
}

function validateRepo(repoInput) {
  const repo = resolve(String(repoInput ?? ''));
  if (repo === dirname(repo)) throw new Error('refusing to use a filesystem root as --repo');
  if (!existsNoSymlink(repo) || !lstatSync(repo).isDirectory()) {
    throw new Error(`node repository does not exist or is not a directory: ${repo}`);
  }
  const sdnDir = join(repo, 'sdn');
  if (!existsNoSymlink(sdnDir) || !lstatSync(sdnDir).isDirectory()) {
    throw new Error(`node repository has no sdn directory: ${sdnDir}`);
  }
  return repo;
}

function validateBackupDir(repo, backupInput) {
  const backupDir = resolve(String(backupInput));
  if (backupDir === dirname(backupDir)) throw new Error('refusing to use a filesystem root as --backup-dir');
  if (isWithin(repo, backupDir)) throw new Error('--backup-dir must be outside the node repository');
  if (existsSync(backupDir)) throw new Error(`--backup-dir must not already exist: ${backupDir}`);
  if (!existsNoSymlink(dirname(backupDir))) throw new Error(`backup parent does not exist: ${dirname(backupDir)}`);
  return backupDir;
}

function readEnvelope(path, listKey, defaults = {}) {
  if (!existsSync(path)) return { exists: false, value: { ...defaults, [listKey]: [] } };
  assertRegularFile(path);
  let value;
  try {
    value = JSON.parse(readFileSync(path, 'utf8'));
  } catch (error) {
    throw new Error(`cannot parse ${path}: ${error.message}`);
  }
  if (!value || typeof value !== 'object' || Array.isArray(value) || !Array.isArray(value[listKey])) {
    throw new Error(`${path} must contain a ${listKey} array`);
  }
  return { exists: true, value };
}

function isLegacyModule(entry) {
  return entry && typeof entry === 'object'
    && (legacyModuleIDs.has(entry.id) || legacySources.has(entry.source));
}

function isLegacyFlow(entry) {
  return entry && typeof entry === 'object'
    && (legacyFlowIDs.has(entry.id) || legacySources.has(entry.source));
}

function isLegacyApproval(approval, legacyHashes) {
  if (!approval || typeof approval !== 'object') return false;
  const hash = normalizeHash(approval.module_hash);
  return (hash !== '' && legacyHashes.has(hash))
    || allLegacyIDs.has(approval.plugin_id)
    || legacySources.has(approval.approved_by);
}

function resolveFlowRef(repo, flowsDir, ref) {
  if (typeof ref !== 'string' || ref.trim() === '') return null;
  const trimmed = ref.trim();
  const candidates = isAbsolute(trimmed)
    ? [resolve(trimmed)]
    : [resolve(repo, trimmed), resolve(flowsDir, trimmed), resolve(flowsDir, basename(trimmed))];
  for (const candidate of candidates) {
    if (existsNoSymlink(candidate) && lstatSync(candidate).isDirectory()) return candidate;
  }
  return null;
}

function hashPortableArtifact(bytes) {
  return createHash('sha256').update(stripPublicationTrailer(bytes)).digest('hex');
}

function stripPublicationTrailer(bytes) {
  if (bytes.length < 8 || bytes.subarray(bytes.length - 4).toString('ascii') !== '$REC') return bytes;
  const recordLength = bytes.readUInt32LE(bytes.length - 8);
  const payloadLength = bytes.length - 8 - recordLength;
  if (recordLength === 0 || payloadLength < 0) return bytes;
  return bytes.subarray(0, payloadLength);
}

function addHash(hashes, value) {
  const hash = normalizeHash(value);
  if (hash !== '') hashes.add(hash);
}

function normalizeHash(value) {
  const hash = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return /^[a-f0-9]{64}$/.test(hash) ? hash : '';
}

function summarizeRegistryEntry(entry) {
  return { id: entry.id, source: entry.source ?? '', ref: entry.ref ?? '', content_hash: entry.content_hash ?? '' };
}

function backupPath(repo, backupDir, source) {
  assertNoSymlinkPath(repo, source);
  const relativePath = relative(repo, source);
  if (relativePath === '' || relativePath.startsWith(`..${sep}`) || relativePath === '..') {
    throw new Error(`refusing to back up path outside node repository: ${source}`);
  }
  const destination = join(backupDir, relativePath);
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  if (lstatSync(source).isDirectory()) {
    cpSync(source, destination, { recursive: true, errorOnExist: true, force: false, dereference: false });
  } else {
    copyFileSync(source, destination);
  }
}

function atomicWriteJSON(path, value) {
  const tempPath = `${path}.purge-${process.pid}.tmp`;
  writeFileSync(tempPath, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  renameSync(tempPath, path);
}

function hasQuarantinedParent(path, paths) {
  return paths.some((candidate) => candidate !== path && isWithin(candidate, path));
}

function isWithin(parent, candidate) {
  const rel = relative(resolve(parent), resolve(candidate));
  return rel === '' || (!rel.startsWith(`..${sep}`) && rel !== '..' && !isAbsolute(rel));
}

function isSafeFlowBundlePath(flowsDir, candidate) {
  return resolve(candidate) !== resolve(flowsDir) && isWithin(flowsDir, candidate);
}

function isSafeIdentifier(id) {
  return typeof id === 'string' && /^[A-Za-z0-9._-]+$/.test(id);
}

function isRegularFile(path) {
  if (!existsSync(path)) return false;
  assertRegularFile(path);
  return true;
}

function assertRegularFile(path) {
  assertNoSymlinkPath(dirname(path), path);
  if (!lstatSync(path).isFile()) throw new Error(`expected a regular file: ${path}`);
}

function existsNoSymlink(path) {
  if (!existsSync(path)) return false;
  if (lstatSync(path).isSymbolicLink()) throw new Error(`refusing symbolic link: ${path}`);
  return true;
}

function assertNoSymlinkPath(root, target) {
  const resolvedRoot = resolve(root);
  const resolvedTarget = resolve(target);
  if (!isWithin(resolvedRoot, resolvedTarget)) throw new Error(`path escapes expected root: ${target}`);
  let current = resolvedRoot;
  const rel = relative(resolvedRoot, resolvedTarget);
  for (const part of rel.split(sep).filter(Boolean)) {
    current = join(current, part);
    if (existsSync(current) && lstatSync(current).isSymbolicLink()) {
      throw new Error(`refusing symbolic link: ${current}`);
    }
  }
}

function printHuman(report) {
  const action = report.mode === 'apply' ? 'APPLIED' : 'DRY RUN';
  console.log(`[legacy-supplemental-state] ${action}`);
  console.log(`repo: ${report.repo}`);
  console.log(`legacy modules: ${report.legacyModules.length}`);
  console.log(`legacy flows: ${report.legacyFlows.length}`);
  console.log(`approvals to revoke: ${report.approvalsToRevoke}`);
  console.log(`files to quarantine: ${report.filesToQuarantine.length}`);
  if (report.externalRefs.length > 0) {
    console.log(`external/missing flow references (registry removal only): ${report.externalRefs.length}`);
  }
  if (report.mode === 'dry-run' && !report.clean) {
    console.log('No files changed. Re-run with --apply --backup-dir <new-path> after stopping the node.');
  }
}

function usage() {
  return `Usage:
  node scripts/purge-legacy-supplemental-omm-state.mjs --repo <node-repo> [--json]
  node scripts/purge-legacy-supplemental-omm-state.mjs --repo <node-repo> --apply --backup-dir <new-path> [--json]

Dry-run is the default. --apply refuses to run without a new backup directory
outside the node repository. Stop the node before applying this migration.`;
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  try {
    const options = parseArgs(process.argv.slice(2));
    if (options.help) {
      console.log(usage());
      process.exitCode = 0;
    } else {
      const result = options.apply
        ? applyLegacyStatePurge(options.repo, options.backupDir)
        : { ...inspectLegacyState(options.repo).report, mode: 'dry-run' };
      if (options.json) console.log(JSON.stringify(result, null, 2));
      else printHuman(result);
    }
  } catch (error) {
    console.error(`[legacy-supplemental-state] ${error.message}`);
    process.exitCode = 1;
  }
}
