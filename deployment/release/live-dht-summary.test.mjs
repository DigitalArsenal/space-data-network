import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { DEFAULT_DHT_REGISTRATION_WAIT_MS, DEFAULT_EXPECTED_ROLES } from './live-dht-client-smoke.mjs';
import { summarizeReports } from './live-dht-summary.mjs';

function writeReport(dir, role, overrides = {}) {
  const report = {
    success: true,
    role,
    peerID: `peer-${role}`,
    seenRoles: DEFAULT_EXPECTED_ROLES,
    maxConnectedPeers: 2,
    dhtRegistrationWaitMs: DEFAULT_DHT_REGISTRATION_WAIT_MS,
    providerSearch: { success: true, count: 2, mode: 'live-dht' },
    dataSearch: { success: true, count: 1, mode: 'live-dht' },
    retrievalQuery: { success: true, count: 3, command: 'dataset-pnms export' },
    ...overrides
  };
  writeFileSync(join(dir, `${role}.json`), `${JSON.stringify(report, null, 2)}\n`);
}

test('live DHT summary accepts reports that prove every required category', () => {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-live-dht-summary-ok-'));
  for (const role of DEFAULT_EXPECTED_ROLES) {
    writeReport(dir, role);
  }

  const { failures } = summarizeReports({ reportDir: dir, expectedRoles: DEFAULT_EXPECTED_ROLES });

  assert.deepEqual(failures, []);
});

test('live DHT summary rejects successful reports missing proof categories', () => {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-live-dht-summary-fail-'));
  for (const role of DEFAULT_EXPECTED_ROLES) {
    writeReport(dir, role);
  }
  writeReport(dir, 'linux-docker', {
    maxConnectedPeers: 0,
    providerSearch: { success: false, count: 0, mode: 'local' }
  });

  const { failures } = summarizeReports({ reportDir: dir, expectedRoles: DEFAULT_EXPECTED_ROLES });

  assert(failures.some((failure) => failure.includes('linux-docker did not prove peer-discovery')));
  assert(failures.some((failure) => failure.includes('linux-docker did not prove provider-search')));
});
