// CROSS-IMPLEMENTATION PARITY GATE for the update-manifest signature.
//
// Three implementations must agree on one preimage or the update lane silently
// splits in half:
//   - the SIGNER    sdn-server/internal/updatesign (Go, runs on host-01)
//   - the FLEET     sdn-server/internal/update/manifest.go (Go, runs on nodes)
//   - the DESKTOP   desktop/src/sdn-updater/manifest.js (Node, runs in Electron)
//
// The Go halves share sigdomain.Statement and cannot drift from each other. The
// JavaScript half reimplements it, and a reimplementation is exactly where a
// silent divergence lives — a different NUL handling, a UTF-8 vs ASCII domain
// encoding, base64 vs base64url. This test pins the JS verifier against a
// GOLDEN VECTOR PRODUCED BY THE GO SIGNER (fixtures-update-manifest-golden.json,
// generated from a fixed Ed25519 seed so it is reproducible), so a divergence
// fails here rather than on every box in the cluster.
//
// Regenerate the fixture only when the statement construction itself changes,
// and treat a regeneration as a wire-format change requiring Seal Council
// concurrence — not as a test fix.

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const { validateUpdateManifest } = require('../../desktop/src/sdn-updater/manifest');

const golden = JSON.parse(await readFile(join(here, 'fixtures-update-manifest-golden.json'), 'utf8'));

function options(manifest, overrides = {}) {
  return {
    platform: 'linux',
    arch: 'amd64',
    currentSequence: 1,
    trustedRoots: { goldenroot: golden.trusted_root_spki_b64 },
    now: new Date('2027-01-01T00:00:00Z'),
    bundleHash: manifest.bundle.hash,
    ...overrides,
  };
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

test('the desktop verifier accepts a signature produced by the Go node signer', () => {
  const manifest = clone(golden.domain_signed);
  assert.equal(manifest.signing.statement_domain, 'SDN-UPDATE-MANIFEST-V1');
  const result = validateUpdateManifest(manifest, options(manifest));
  assert.equal(result.ok, true);
  assert.equal(result.updateId, 'sdn-cli-golden');
});

test('legacy manifests with no statement domain still verify — the change is additive', () => {
  const manifest = clone(golden.legacy_signed);
  assert.equal(manifest.signing.statement_domain, undefined);
  assert.equal(validateUpdateManifest(manifest, options(manifest)).ok, true);
});

test('a foreign statement domain is refused, never resolved through the registry', () => {
  for (const domain of [
    'SDN-MODULE-PUBLICATION-V1', // registered, but for a different statement kind
    'SDN-UPDATE-MANIFEST-V2',
    'sdn-update-manifest-v1',
    'SDN-DPM-PNM',
  ]) {
    const manifest = clone(golden.domain_signed);
    manifest.signing.statement_domain = domain;
    assert.throws(
      () => validateUpdateManifest(manifest, options(manifest)),
      /unsupported update signature statement domain|invalid update signature/,
      `statement domain ${domain} was not refused`
    );
  }
});

test('there is no downgrade path in either direction', () => {
  // Domain-signed bytes with the label stripped: the canonical bytes change,
  // so the signature no longer covers them.
  const stripped = clone(golden.domain_signed);
  delete stripped.signing.statement_domain;
  assert.throws(() => validateUpdateManifest(stripped, options(stripped)), /invalid update signature/);

  // Legacy-signed bytes with a label bolted on: same, mirrored.
  const relabelled = clone(golden.legacy_signed);
  relabelled.signing.statement_domain = 'SDN-UPDATE-MANIFEST-V1';
  assert.throws(() => validateUpdateManifest(relabelled, options(relabelled)), /invalid update signature/);
});

test('a tampered payload hash fails even with a valid signature attached', () => {
  const manifest = clone(golden.domain_signed);
  manifest.bundle.hash = 'a'.repeat(64);
  assert.throws(() => validateUpdateManifest(manifest, options(manifest)), /invalid update signature/);
});

test('an untrusted key id is refused before any signature math', () => {
  const manifest = clone(golden.domain_signed);
  assert.throws(
    () => validateUpdateManifest(manifest, options(manifest, { trustedRoots: { someoneelse: golden.trusted_root_spki_b64 } })),
    /untrusted update signing key/
  );
});
