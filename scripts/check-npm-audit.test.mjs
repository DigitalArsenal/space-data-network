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

/** A report containing exactly the currently-allowlisted advisories. */
function baselineReport() {
  const vulnerabilities = {};
  for (const entry of ALLOWLIST) {
    vulnerabilities[entry.package] = {
      severity: 'high',
      via: entry.advisories.length
        ? entry.advisories.map((url) => ({ url, title: 'reviewed' }))
        : ['some-other-package'],
    };
  }
  return { vulnerabilities, metadata: { vulnerabilities: { high: ALLOWLIST.length } } };
}

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
  const { failures, deferred } = evaluate(baselineReport(), { today: TODAY });
  assert.deepEqual(failures, []);
  assert.equal(deferred.length, ALLOWLIST.length);
});

test('a new unreviewed high fails the gate', () => {
  const report = baselineReport();
  report.vulnerabilities['some-new-package'] = {
    severity: 'high',
    via: [{ url: 'https://github.com/advisories/GHSA-fake-0000-0000', title: 'RCE' }],
  };
  const { failures } = evaluate(report, { today: TODAY });
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
  const { failures } = evaluate(report, { today: TODAY });
  assert.equal(failures.length, 1);
  assert.match(failures[0], /brand-new \(critical\)/);
});

test('moderate and low advisories do not fail the gate', () => {
  const report = baselineReport();
  report.vulnerabilities['something-moderate'] = { severity: 'moderate', via: [] };
  report.vulnerabilities['something-low'] = { severity: 'low', via: [] };
  assert.deepEqual(evaluate(report, { today: TODAY }).failures, []);
});

test('a NEW advisory on an already-allowlisted package still fails', () => {
  // The deferral covers the advisories that were read, not the package forever.
  const withAdvisories = ALLOWLIST.find((e) => e.advisories.length > 0);
  const report = baselineReport();
  report.vulnerabilities[withAdvisories.package].via.push({
    url: 'https://github.com/advisories/GHSA-fake-2222-2222',
    title: 'newly disclosed',
  });
  const { failures } = evaluate(report, { today: TODAY });
  assert.equal(failures.length, 1);
  assert.match(failures[0], /NEW advisory not covered by the deferral/);
  assert.match(failures[0], /GHSA-fake-2222-2222/);
});

test('an expired deferral fails — the deadline is enforced, not decorative', () => {
  const soonest = ALLOWLIST.map((e) => e.until).sort()[0];
  const dayAfter = new Date(`${soonest}T23:59:59Z`);
  dayAfter.setUTCDate(dayAfter.getUTCDate() + 1);
  const { failures } = evaluate(baselineReport(), { today: dayAfter });
  assert.ok(failures.length > 0);
  assert.match(failures.join('\n'), /allowlist entry EXPIRED on /);
});

test('a stale allowlist entry fails so deferrals cannot outlive their advisory', () => {
  const report = baselineReport();
  const dropped = ALLOWLIST[0].package;
  delete report.vulnerabilities[dropped];
  const { failures } = evaluate(report, { today: TODAY });
  assert.equal(failures.length, 1);
  assert.match(failures[0], new RegExp(`^${dropped.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:`));
  assert.match(failures[0], /no longer reported at high\/critical\. Remove the entry\./);
});

test('every allowlist entry carries a reason, a review date and an expiry', () => {
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
  const clean = writeReport(t, baselineReport());
  const ok = spawnSync(process.execPath, [checkerPath, '--report', clean], { encoding: 'utf8' });
  assert.equal(ok.status, 0, ok.stderr);
  assert.match(ok.stdout, /PASS: no unreviewed high or critical advisories/);

  const report = baselineReport();
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
