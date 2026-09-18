import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { evaluate, ALLOWLIST, GATED_SEVERITIES } from './check-npm-audit.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const checkerPath = resolve(scriptsDir, 'check-npm-audit.mjs');
const TODAY = new Date('2026-09-18T12:00:00Z');

/**
 * A fixture allowlist.
 *
 * These tests are about the GATE, not about what happens to be deferred today —
 * and the live ALLOWLIST is legitimately EMPTY now that the libp2p 3 upgrade
 * fixed every advisory it deferred. Deriving the fixtures from the live list
 * made the gate's own behaviour untestable in exactly that state.
 */
const FIXTURE_ALLOWLIST = [
  {
    package: 'deferred-with-advisory',
    advisories: ['https://github.com/advisories/GHSA-fake-aaaa-aaaa'],
    reviewed: '2026-09-01',
    until: '2026-12-31',
    reason:
      'Fixture entry: a reviewed advisory with a stated stopping point and a deadline, long enough to satisfy the shape check.',
  },
  {
    package: 'deferred-carrier',
    advisories: [],
    reviewed: '2026-09-01',
    until: '2027-03-31',
    reason:
      'Fixture entry: a carrier package reported only because it depends on the one above, with a reason long enough for the shape check.',
  },
];

/** A report containing exactly the fixture-allowlisted advisories. */
function baselineReport() {
  const vulnerabilities = {};
  for (const entry of FIXTURE_ALLOWLIST) {
    vulnerabilities[entry.package] = {
      severity: 'high',
      via: entry.advisories.length
        ? entry.advisories.map((url) => ({ url, title: 'reviewed' }))
        : ['some-other-package'],
    };
  }
  return {
    vulnerabilities,
    metadata: { vulnerabilities: { high: FIXTURE_ALLOWLIST.length } },
  };
}

const GATE = { today: TODAY, allowlist: FIXTURE_ALLOWLIST };

function writeReport(t, report) {
  const dir = mkdtempSync(join(tmpdir(), 'npm-audit-gate-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const file = join(dir, 'report.json');
  writeFileSync(file, JSON.stringify(report));
  return file;
}

test('gates high and critical only', () => {
  assert.deepEqual([...GATED_SEVERITIES].sort(), ['critical', 'high']);
});

test('the baseline of reviewed deferrals passes', () => {
  const { failures, deferred } = evaluate(baselineReport(), GATE);
  assert.deepEqual(failures, []);
  assert.equal(deferred.length, FIXTURE_ALLOWLIST.length);
});

test('a new unreviewed high fails the gate', () => {
  const report = baselineReport();
  report.vulnerabilities['some-new-package'] = {
    severity: 'high',
    via: [{ url: 'https://github.com/advisories/GHSA-fake-0000-0000', title: 'RCE' }],
  };
  const { failures } = evaluate(report, GATE);
  assert.equal(failures.length, 1);
  assert.match(failures[0], /some-new-package \(high\)/);
  assert.match(failures[0], /not allowlisted/);
});

test('a critical fails even when a package is otherwise allowlisted', () => {
  const report = baselineReport();
  report.vulnerabilities['brand-new'] = {
    severity: 'critical',
    via: [{ url: 'https://github.com/advisories/GHSA-fake-1111-1111' }],
  };
  const { failures } = evaluate(report, GATE);
  assert.equal(failures.length, 1);
  assert.match(failures[0], /brand-new \(critical\)/);
});

test('moderate and low advisories do not fail the gate', () => {
  const report = baselineReport();
  report.vulnerabilities['something-moderate'] = { severity: 'moderate', via: [] };
  report.vulnerabilities['something-low'] = { severity: 'low', via: [] };
  assert.deepEqual(evaluate(report, GATE).failures, []);
});

test('a NEW advisory on an already-allowlisted package still fails', () => {
  // The deferral covers the advisories that were read, not the package forever.
  const withAdvisories = FIXTURE_ALLOWLIST.find((e) => e.advisories.length > 0);
  const report = baselineReport();
  report.vulnerabilities[withAdvisories.package].via.push({
    url: 'https://github.com/advisories/GHSA-fake-2222-2222',
    title: 'newly disclosed',
  });
  const { failures } = evaluate(report, GATE);
  assert.equal(failures.length, 1);
  assert.match(failures[0], /NEW advisory not covered by the deferral/);
  assert.match(failures[0], /GHSA-fake-2222-2222/);
});

test('an expired deferral fails — the deadline is enforced, not decorative', () => {
  const soonest = FIXTURE_ALLOWLIST.map((e) => e.until).sort()[0];
  const dayAfter = new Date(`${soonest}T23:59:59Z`);
  dayAfter.setUTCDate(dayAfter.getUTCDate() + 1);
  const { failures } = evaluate(baselineReport(), {
    today: dayAfter,
    allowlist: FIXTURE_ALLOWLIST,
  });
  assert.ok(failures.length > 0);
  assert.match(failures.join('\n'), /allowlist entry EXPIRED on /);
});

test('a stale allowlist entry fails so deferrals cannot outlive their advisory', () => {
  const report = baselineReport();
  const dropped = FIXTURE_ALLOWLIST[0].package;
  delete report.vulnerabilities[dropped];
  const { failures } = evaluate(report, GATE);
  assert.equal(failures.length, 1);
  assert.match(failures[0], new RegExp(`^${dropped.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:`));
  assert.match(failures[0], /no longer reported at high\/critical\. Remove the entry\./);
});

test('every LIVE allowlist entry carries a reason, a review date and an expiry', () => {
  // Vacuously true while the list is empty, which is the point: an entry may
  // only come back with a stopping point and a deadline attached.
  for (const entry of ALLOWLIST) {
    assert.match(entry.reviewed, /^\d{4}-\d{2}-\d{2}$/, `${entry.package} needs a review date`);
    assert.match(entry.until, /^\d{4}-\d{2}-\d{2}$/, `${entry.package} needs an expiry`);
    assert.ok(entry.until > entry.reviewed, `${entry.package} expiry must follow its review`);
    assert.ok(
      (entry.reason ?? '').length > 60,
      `${entry.package} needs a reason that says where the advisory stops`,
    );
  }
});

test('CLI scores a captured report and exits non-zero on an unreviewed high', (t) => {
  // The CLI scores against the LIVE allowlist, so "clean" here means a report
  // with nothing in it that the live list has to excuse.
  const liveClean = { vulnerabilities: {}, metadata: { vulnerabilities: {} } };
  const clean = writeReport(t, liveClean);
  const ok = spawnSync(process.execPath, [checkerPath, '--report', clean], { encoding: 'utf8' });
  assert.equal(ok.status, 0, ok.stderr);
  assert.match(ok.stdout, /PASS: no unreviewed high or critical advisories/);

  const report = { vulnerabilities: {}, metadata: { vulnerabilities: {} } };
  report.vulnerabilities['sneaky'] = {
    severity: 'high',
    via: [{ url: 'https://github.com/advisories/GHSA-fake-3333-3333' }],
  };
  const bad = spawnSync(process.execPath, [checkerPath, '--report', writeReport(t, report)], {
    encoding: 'utf8',
  });
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /sneaky \(high\)/);
});
