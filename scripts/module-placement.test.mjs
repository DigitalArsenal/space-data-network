import { test } from 'node:test';
import assert from 'node:assert/strict';
import { assignedNode, canonicalModules, defaultPlacement, validatePlacement } from './module-placement.mjs';

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
