import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { assignedNode, canonicalModules, defaultPlacement, validatePlacement, sealVerifiedArtifact } from './module-placement.mjs';

test('science and provider flows stay with their designated nodes', () => {
  const assignments = {
    'propagator/hpop': 'tu-delft',
    'analysis/od': 'cu-boulder',
    'analysis/conjunction-assessment': 'ut-austin',
    'maneuver/star-search': 'ut-austin',
    'flows/celestrak-ingest/dist/socrates': 'celestrak',
    'flows/celestrak-reference/dist/satcat-reference': 'celestrak',
    'packages/rf-fspl': 'ut-austin',
    'packages/sensor-coverage': 'cu-boulder',
    'packages/poly-coverage': 'tu-delft',
    'hostcap/flatsql-query': 'local',
    'licensing/core': 'local',
  };
  for (const [source, owner] of Object.entries(assignments)) assert.equal(assignedNode(source), owner, source);
});

test('duplicate build/flow manifests do not become duplicate listings or downgrade protection', () => {
  const make = (manifestPath, version = '1.0.0', protectedModule = false) => ({ manifestPath, protected: protectedModule, manifest: { pluginId: 'orbit-test', version } });
  const rows = canonicalModules([
    make('flows/demo/nodes/od/plugin-manifest.json'),
    make('analysis/od/dist/plugin-manifest.json'),
    make('analysis/od/plugin-manifest.json'),
    make('packages/od/plugin-manifest.json', '1.0.0', true),
    make('analysis/od/plugin-manifest.json', '2.0.0'),
  ]);
  assert.equal(rows.length, 2);
  assert.equal(rows[0].protected, true);
  assert.equal(rows[0].copies.length, 4);
  assert.equal(rows[1].manifest.version, '2.0.0');
});

test('unidentified modules and duplicate node identities fail instead of producing placement', () => {
  assert.throws(() => canonicalModules([{ manifestPath: 'bad.json', manifest: {} }]), /missing pluginId/);
  const placement = structuredClone(defaultPlacement);
  placement.nodes['ut-austin'].peerId = placement.nodes.local.peerId;
  assert.throws(() => validatePlacement(placement), /distinct/);
  assert.doesNotThrow(() => validatePlacement(defaultPlacement));
});

test('a shared module keeps its functional owner when its source lives inside an ingestion flow', () => {
  assert.equal(assignedNode('flows/supplemental-omm/nodes/flatsql', defaultPlacement, 'com.digitalarsenal.flatsql.store'), 'local');
  assert.equal(assignedNode('flows/supplemental-omm/nodes/od', defaultPlacement, 'org.sdn.flows.supplemental-omm.od'), 'cu-boulder');
  assert.equal(assignedNode('data-source/spk-source', defaultPlacement, 'com.digitalarsenal.propagator.ephemeris'), 'tu-delft');
  const bad = structuredClone(defaultPlacement);
  bad.moduleOwners['com.digitalarsenal.flatsql.store'] = 'missing-node';
  assert.throws(() => validatePlacement(bad), /explicit module owner/);
});

test('customer sealing retains the entire SDK bundle and hides auxiliary application content', async () => {
  const require = createRequire(new URL('../sdn-js/package.json', import.meta.url));
  const sdk = await import(pathToFileURL(require.resolve('space-data-module-sdk')));
  const wasmBytes = Uint8Array.from([0,97,115,109,1,0,0,0]);
  const secretPage = new TextEncoder().encode('private-editor-sentinel');
  const source = await sdk.createSingleFileBundle({ wasmBytes, manifest: {pluginId:'test.editor',name:'Test Editor',version:'1.0.0',pluginFamily:'analysis',capabilities:[],methods:[]}, entries:[{entryId:'app.app',role:'auxiliary',sectionName:'sdn.app.record',payloadEncoding:'raw',payload:secretPage}] });
  const recipient = await sdk.generateX25519Keypair(), other = await sdk.generateX25519Keypair();
  try {
    const encrypted = await sealVerifiedArtifact(source.wasmBytes, source.canonicalModuleHashHex, recipient.publicKey, sdk);
    const outside = sdk.extractPublicationRecordCollection(encrypted);
    assert.ok(outside.enc); assert.equal(outside.mbl,null);
    assert.equal(encrypted.includes(Buffer.from(secretPage)),false);
    const decrypted = await sdk.decryptProtectedBytes({protectedBytes:encrypted,recipientPrivateKey:recipient.privateKey});
    assert.deepEqual(new Uint8Array(decrypted),new Uint8Array(source.wasmBytes));
    assert.ok((await sdk.parseSingleFileBundle(decrypted)).entries.some(entry=>entry.entryId==='app.app'));
    await assert.rejects(sdk.decryptProtectedBytes({protectedBytes:encrypted,recipientPrivateKey:other.privateKey}));
    await assert.rejects(sealVerifiedArtifact(source.wasmBytes,'0'.repeat(64),recipient.publicKey,sdk),/differs/);
  } finally { recipient.privateKey.fill(0);other.privateKey.fill(0); }
});
