#!/usr/bin/env node
/** Placement is deployment data. Module identity and metadata come from PLG. */
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
export const defaultPlacement = JSON.parse(fs.readFileSync(path.join(root, 'deployment/module-placement.json')));

export function assignedNode(modulePath, placement = defaultPlacement) {
  const normalized = `${modulePath.replaceAll('\\', '/').replace(/\/$/, '')}/`;
  return placement.rules.find(rule => rule.paths.some(prefix => normalized.startsWith(prefix)))?.node ?? placement.defaultNode;
}

export function validatePlacement(placement) {
  if (placement.version !== 1 || !placement.nodes[placement.customer] || !placement.nodes[placement.defaultNode]) throw new Error('Invalid placement configuration');
  const peers = Object.values(placement.nodes).map(node => node.peerId);
  if (peers.some(peer => !peer) || new Set(peers).size !== peers.length) throw new Error('Every role needs a distinct existing peer');
  for (const rule of placement.rules) if (!placement.nodes[rule.node] || !rule.paths?.length) throw new Error('Invalid placement rule');
}

const excluded = /(^|\/)(node_modules|test|tests|fixtures|examples|\.build|build|deps)(\/|$)/;
const generated = /(^|\/)dist\//;
const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');

export function canonicalModules(manifests) {
  const groups = new Map();
  for (const item of manifests) {
    if (!item.manifest.pluginId || !item.manifest.version) throw new Error(`${item.manifestPath}: missing pluginId/version`);
    const key = `${item.manifest.pluginId}@${item.manifest.version}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(item);
  }
  return [...groups.values()].map(copies => {
    // An authored manifest outranks a generated copy, then the shortest source
    // path wins over copies embedded into a flow. Never downgrade protection.
    copies.sort((a, b) => Number(b.protected) - Number(a.protected) || Number(generated.test(a.manifestPath)) - Number(generated.test(b.manifestPath)) || a.manifestPath.length - b.manifestPath.length || a.manifestPath.localeCompare(b.manifestPath));
    return { ...copies[0], protected: copies.some(copy => copy.protected), copies };
  }).sort((a, b) => `${a.manifest.pluginId}@${a.manifest.version}`.localeCompare(`${b.manifest.pluginId}@${b.manifest.version}`));
}

function loadSource(repoRoot, isProtected) {
  const revision = execFileSync('git', ['-C', repoRoot, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
  const files = execFileSync('git', ['-C', repoRoot, 'ls-files', '-z', '--', '*plugin-manifest.json'], { encoding: 'utf8' }).split('\0').filter(Boolean);
  return files.filter(file => !excluded.test(file)).map(file => ({
    repoRoot, revision, protected: isProtected, manifestPath: file,
    manifest: JSON.parse(fs.readFileSync(path.join(repoRoot, file))),
  }));
}

function artifactCandidates(item) {
  const base = path.dirname(path.join(item.repoRoot, item.manifestPath));
  const candidates = (item.manifest.buildArtifacts ?? [])
    .filter(artifact => artifact.kind === 'wasm' && artifact.path)
    .map(artifact => path.resolve(base, artifact.path));
  candidates.push(path.join(base, 'dist/isomorphic/module.wasm'));
  if (generated.test(item.manifestPath)) candidates.push(path.join(base, 'isomorphic/module.wasm'), path.join(base, 'runtime.wasm'));
  for (const file of ['dist/manifest.json', 'manifest.json']) {
    const deliveryPath = path.join(base, file);
    if (!fs.existsSync(deliveryPath)) continue;
    const delivery = JSON.parse(fs.readFileSync(deliveryPath));
    if (delivery.wasmModule?.file) candidates.push(path.resolve(path.dirname(deliveryPath), delivery.wasmModule.file));
  }
  return [...new Set(candidates)].filter(file => fs.existsSync(file) && path.relative(item.repoRoot, file).split(path.sep)[0] !== '..');
}

function declaredLibraryExports(item) {
  const base = path.dirname(path.join(item.repoRoot, item.manifestPath));
  for (const file of ['dist/manifest.json', 'manifest.json']) {
    const target = path.join(base, file);
    if (fs.existsSync(target)) {
      const names = JSON.parse(fs.readFileSync(target)).wasmModule?.exports;
      if (Array.isArray(names)) return names;
    }
  }
  return [];
}

export async function inventory({ publicRoot, closedRoot, placement = defaultPlacement }) {
  validatePlacement(placement);
  const require = createRequire(path.join(publicRoot, 'package.json'));
  const { stripPublicationRecordCollection, extractPublicationRecordCollection } = await import(pathToFileURL(require.resolve('space-data-module-sdk/transport')));
  const { decodePluginManifest, computeCanonicalModuleHash } = await import(pathToFileURL(require.resolve('space-data-module-sdk')));
  const { createBrowserModuleHarness } = await import(pathToFileURL(require.resolve('space-data-module-sdk/host/browser-module')));
  const { classifyArtifactImports } = await import(pathToFileURL(require.resolve('space-data-module-sdk/testing')));
  const { createFlowRuntimeHost } = await import(pathToFileURL(require.resolve('space-data-module-sdk/flow')));
  const items = canonicalModules([...loadSource(publicRoot, false), ...loadSource(closedRoot, true)]);
  const modules = [];
  for (const item of items) {
    const m = item.manifest;
    const modulePath = path.dirname(item.manifestPath).replace(/\/dist$/, '');
    const owner = assignedNode(modulePath, placement);
    const row = {
      pluginId: m.pluginId, version: m.version, name: m.name || m.pluginId,
      owner, providerPeerId: placement.nodes[owner].peerId, protected: item.protected,
      sourceRepository: item.protected ? 'space-data-network-closed-modules' : 'space-data-network-modules',
      sourceRevision: item.revision, manifestPath: item.manifestPath,
      sourceCopies: item.copies.map(copy => `${copy.protected ? 'closed' : 'public'}:${copy.manifestPath}`),
      runtimeTargets: m.runtimeTargets ?? [], dependencies: m.dependencies ?? [],
      artifact: null, status: 'build-required', issues: [],
    };
    for (const candidate of artifactCandidates(item)) {
      try {
        const bytes = fs.readFileSync(candidate);
        const core = stripPublicationRecordCollection(bytes);
        const compiled = await WebAssembly.compile(core);
        const exports = WebAssembly.Module.exports(compiled).map(entry => entry.name);
        const imports = WebAssembly.Module.imports(compiled);
        const missing = ['plugin_get_manifest_flatbuffer', 'plugin_get_manifest_flatbuffer_size', 'plugin_alloc', 'plugin_free', 'plugin_invoke_stream'].filter(name => !exports.includes(name));
        const embedded = extractPublicationRecordCollection(bytes)?.mbl?.entries?.find(entry => entry.entryId === 'manifest');
        if (embedded?.payload) {
          const plg = decodePluginManifest(new Uint8Array(embedded.payload));
          if (plg.pluginId !== m.pluginId || plg.version !== m.version) throw new Error('Embedded PLG identity/version does not match source');
        }
        row.artifact = { path: path.relative(item.repoRoot, candidate), size: bytes.length, sha256: hash(bytes), coreSha256: hash(core), canonicalSha256: (await computeCanonicalModuleHash(core)).hashHex, exports, imports };
        row.status = missing.length ? 'abi-repair-required' : 'artifact-verified';
        row.issues = missing.map(name => `Missing ${name}`);
        if (exports.includes('space_data_module_runtime_get_node_descriptor_count') && exports.includes('flow_get_manifest_flatbuffer')) {
          const leg = row.runtimeTargets.includes('browser') ? 'browser' : row.runtimeTargets.includes('wasmedge') ? 'wasmedge' : null;
          if (!leg) throw new Error('Composed flow has no declared supported runtime');
          const extraImports = { space_data_module_host: {} };
          for (const entry of imports.filter(entry => entry.module === 'space_data_module_host' && entry.kind === 'function')) {
            extraImports.space_data_module_host[entry.name] = () => { throw new Error('Unexpected host operation while reading the flow manifest'); };
          }
          const host = await createFlowRuntimeHost({ wasmModule: compiled, manifest: m, runtimeTarget: leg, extraImports });
          const e = host.instance.exports;
          const plg = decodePluginManifest(new Uint8Array(host.memory.buffer, e.plugin_get_manifest_flatbuffer(), e.plugin_get_manifest_flatbuffer_size()));
          if (plg.pluginId !== m.pluginId || plg.version !== m.version) throw new Error('Composed flow PLG identity/version does not match source');
          row.status = 'artifact-verified'; row.runtimeSurface = 'composed-flow'; row.issues = []; break;
        }
        const declared = declaredLibraryExports(item);
        const bytesSymbol = declared.find(name => name.endsWith('_plugin_manifest_bytes'));
        const sizeSymbol = declared.find(name => name.endsWith('_plugin_manifest_size'));
        if (missing.length && item.protected && bytesSymbol && sizeSymbol) {
          const contract = classifyArtifactImports(core, 'module');
          if (contract.verdict !== 'in-surface' || declared.some(name => !exports.includes(name))) throw new Error('Direct library does not satisfy its declared export/host contract');
          const { createWasiPluginModule } = await import(pathToFileURL(path.join(closedRoot, 'packages/wasiPluginModule.js')));
          const library = await createWasiPluginModule({ wasmBinary: core });
          const payload = new Uint8Array(library.wasmMemory.buffer, library[bytesSymbol](), library[sizeSymbol]()).slice();
          const embeddedManifest = decodePluginManifest(payload);
          if (embeddedManifest.pluginId !== m.pluginId || embeddedManifest.version !== m.version) throw new Error('Direct library embedded PLG identity/version does not match source');
          row.status = 'artifact-verified';
          row.runtimeSurface = 'direct-library';
          row.issues = [];
          break;
        }
        if (!row.runtimeTargets.includes('browser') || !row.runtimeTargets.includes('wasmedge')) {
          row.status = 'runtime-verification-required';
          row.issues.push('PLG must declare browser and WasmEdge support');
        }
        if (row.status === 'artifact-verified') {
          const harness = await createBrowserModuleHarness({ wasmSource: new Uint8Array(core), manifest: m });
          try {
            const embeddedManifest = harness.readManifest();
            if (!embeddedManifest) throw new Error('The WASM manifest accessor returned no PLG');
            const plg = decodePluginManifest(embeddedManifest);
            if (plg.pluginId !== m.pluginId || plg.version !== m.version) throw new Error('The WASM manifest accessor disagrees with source PLG identity/version');
          } finally { harness.destroy(); }
        }
        if (row.status === 'artifact-verified') break;
      } catch (error) { row.status = 'verification-required'; row.issues.push(`${path.relative(item.repoRoot, candidate)}: ${error.message}`); }
    }
    modules.push(row);
  }
  return { version: 1, customerPeerId: placement.nodes[placement.customer].peerId, nodes: placement.nodes, modules };
}

export async function stageArtifacts({ report, publicRoot, closedRoot, outputRoot, customerXpub }) {
  if (!/^xpub[1-9A-HJ-NP-Za-km-z]+$/.test(customerXpub ?? '')) throw new Error('A customer public xpub is required; protected modules never default to open access');
  if (fs.existsSync(outputRoot)) throw new Error('Choose a new staging directory; existing staged keys must not be overwritten');
  const require = createRequire(path.join(publicRoot, 'package.json'));
  const { protectModuleArtifact } = await import(pathToFileURL(require.resolve('space-data-module-sdk')));
  const { stripPublicationRecordCollection, generateX25519Keypair } = await import(pathToFileURL(require.resolve('space-data-module-sdk/transport')));
  fs.mkdirSync(outputRoot, { recursive: true, mode: 0o700 });
  const staged = [];
  for (const [owner] of Object.entries(report.nodes)) {
    const ownerRoot = path.join(outputRoot, owner);
    fs.mkdirSync(ownerRoot, { mode: 0o700 });
    const plugins = [];
    for (const row of report.modules.filter(module => module.owner === owner && module.status === 'artifact-verified')) {
      const repoRoot = row.sourceRepository === 'space-data-network-closed-modules' ? closedRoot : publicRoot;
      const manifest = JSON.parse(fs.readFileSync(path.join(repoRoot, row.manifestPath)));
      const disk = fs.readFileSync(path.join(repoRoot, row.artifact.path));
      if (hash(disk) !== row.artifact.sha256) throw new Error(`${row.pluginId}: artifact changed since verification`);
      const keypair = await generateX25519Keypair();
      try {
        const artifact = await protectModuleArtifact({
          manifest, wasmBytes: stripPublicationRecordCollection(disk),
          recipientPublicKeyHex: Buffer.from(keypair.publicKey).toString('hex'),
          singleFileBundle: true, artifactId: row.pluginId, programId: row.pluginId,
        });
        const stem = hash(`${row.pluginId}@${row.version}`).slice(0, 24);
        const encrypted = Buffer.from(artifact.protectedArtifactBytes);
        fs.writeFileSync(path.join(ownerRoot, `${stem}.wasm.enc`), encrypted, { mode: 0o600 });
        fs.writeFileSync(path.join(ownerRoot, `${stem}.key`), Buffer.from(keypair.privateKey).toString('hex'), { mode: 0o600 });
        plugins.push({ id: row.pluginId, version: row.version, required_scope: row.protected ? 'sdn:test-customer' : 'sdn:public', encrypted_path: `${stem}.wasm.enc`, key_path: `${stem}.key`, content_type: 'application/wasm+encrypted', grant_policy: row.protected ? 'allowlist' : 'open', allowed_xpubs: row.protected ? [customerXpub] : [], dependencies: (row.dependencies ?? []).map(dep => ({ plugin_id: dep.pluginId, min_version: dep.minVersion, max_version: dep.maxVersion })).filter(dep => dep.plugin_id) });
        staged.push({ pluginId: row.pluginId, version: row.version, owner, protected: row.protected, encryptedSha256: hash(encrypted), bytes: encrypted.length });
      } finally { keypair.privateKey.fill(0); }
    }
    fs.writeFileSync(path.join(ownerRoot, 'catalog.json'), `${JSON.stringify({ plugins }, null, 2)}\n`, { mode: 0o600 });
  }
  fs.writeFileSync(path.join(outputRoot, 'receipt.json'), `${JSON.stringify({ customerPeerId: report.customerPeerId, staged }, null, 2)}\n`, { mode: 0o600 });
  return staged;
}

// No-charge checkout fixture: the provider hosts ciphertext sealed directly to
// the customer's real node key. No recipient private key is generated/exported.
export async function stageCustomerArtifacts({ report, publicRoot, closedRoot, outputRoot, customerPublicKey }) {
  if (!/^[0-9a-f]{64}$/i.test(customerPublicKey ?? '')) throw new Error('A 32-byte customer encryption public key is required');
  if (fs.existsSync(outputRoot)) throw new Error('Customer staging directory already exists');
  const require = createRequire(path.join(publicRoot, 'package.json'));
  const { protectModuleArtifact } = await import(pathToFileURL(require.resolve('space-data-module-sdk')));
  const { stripPublicationRecordCollection } = await import(pathToFileURL(require.resolve('space-data-module-sdk/transport')));
  fs.mkdirSync(outputRoot, { recursive: true, mode: 0o700 });
  const staged = [];
  for (const row of report.modules.filter(m => m.status === 'artifact-verified')) {
    const repo = row.sourceRepository === 'space-data-network-closed-modules' ? closedRoot : publicRoot;
    const disk = fs.readFileSync(path.join(repo, row.artifact.path));
    if (hash(disk) !== row.artifact.sha256) throw new Error(`${row.pluginId}: artifact changed after verification`);
    const manifest = JSON.parse(fs.readFileSync(path.join(repo, row.manifestPath)));
    const wrapped = await protectModuleArtifact({ manifest, wasmBytes: stripPublicationRecordCollection(disk), recipientPublicKeyHex: customerPublicKey, singleFileBundle: true, artifactId: row.pluginId, programId: row.pluginId });
    if (wrapped.singleFileBundle.canonicalModuleHashHex !== row.artifact.canonicalSha256) throw new Error(`${row.pluginId}: packaged plaintext differs from the verified canonical artifact`);
    const bytes = Buffer.from(wrapped.protectedArtifactBytes);
    const owner = path.join(outputRoot, row.owner); fs.mkdirSync(owner, { recursive: true, mode: 0o700 });
    const file = `${hash(`${row.pluginId}@${row.version}`).slice(0, 24)}.wasm.enc`;
    fs.writeFileSync(path.join(owner, file), bytes, { mode: 0o600 });
    staged.push({ pluginId: row.pluginId, version: row.version, owner: row.owner, file, customerPublicKey, sha256: hash(bytes), plaintextSha256: wrapped.singleFileBundle.canonicalModuleHashHex, bytes: bytes.length });
  }
  fs.writeFileSync(path.join(outputRoot, 'receipt.json'), JSON.stringify({ customerPeerId: report.customerPeerId, staged }, null, 2));
  return staged;
}

async function main() {
  const args = process.argv.slice(2);
  const option = name => args[args.indexOf(name) + 1];
  for (const required of ['--public', '--closed', '--out']) if (!args.includes(required)) throw new Error(`Required: ${required}`);
  const report = await inventory({ publicRoot: path.resolve(option('--public')), closedRoot: path.resolve(option('--closed')) });
  fs.writeFileSync(path.resolve(option('--out')), `${JSON.stringify(report, null, 2)}\n`);
  if (args.includes('--customer-stage')) {
    if (!args.includes('--customer-public-key-file')) throw new Error('--customer-stage requires --customer-public-key-file (public material only)');
    const staged = await stageCustomerArtifacts({ report, publicRoot: path.resolve(option('--public')), closedRoot: path.resolve(option('--closed')), outputRoot: path.resolve(option('--customer-stage')), customerPublicKey: fs.readFileSync(option('--customer-public-key-file'), 'utf8').trim() });
    console.log(`${staged.length} artifacts sealed for the customer. Pin ciphertext on each assigned provider before enabling checkout.`);
  }
  if (args.includes('--stage')) {
    if (!args.includes('--customer-xpub-file')) throw new Error('--stage requires --customer-xpub-file (public material only)');
    const staged = await stageArtifacts({ report, publicRoot: path.resolve(option('--public')), closedRoot: path.resolve(option('--closed')), outputRoot: path.resolve(option('--stage')), customerXpub: fs.readFileSync(option('--customer-xpub-file'), 'utf8').trim() });
    console.log(`${staged.length} encrypted artifacts staged. This is not a publication receipt.`);
  }
  for (const [owner, node] of Object.entries(report.nodes)) {
    const rows = report.modules.filter(module => module.owner === owner);
    console.log(`${node.name}: ${rows.length} assigned, ${rows.filter(module => module.protected).length} protected, ${rows.filter(module => module.status === 'artifact-verified').length} artifacts verified`);
  }
  console.log(`${report.modules.length} unique module versions assigned. Artifact verification is not an execution or deployment receipt.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main().catch(error => { console.error(error.message); process.exitCode = 1; });
