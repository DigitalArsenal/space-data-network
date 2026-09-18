#!/usr/bin/env node
/**
 * check-npm-audit.mjs
 *
 * A real gate over `npm audit` for the published sdn-js package.
 *
 * What it replaces: .github/workflows/security.yml used to run
 *
 *     npm audit --json > report || true
 *     ... if high/critical: echo "::warning::..."
 *
 * which can never fail, inside a workflow that only ran on workflow_dispatch.
 * It audited the FULL tree and printed 13 high and 1 critical, and reported
 * green anyway, because nothing was ever asked to be red — including an 8.2
 * address-poisoning primitive against libp2p's PeerStore.
 *
 * THIS GATE DELIBERATELY SCORES A NARROWER SET: the production closure
 * (--omit=dev), which is what a consumer of the published sdn-js package
 * actually installs. That is 0 critical and 7 high against the full tree's 1
 * and 13. The difference is NOT fixed, it is out of this gate's scope, and
 * saying so here is the point — a reader who finds "7 high" in this file must
 * be able to tell that 6 high and 1 critical were scoped out rather than
 * resolved. The dev-tree advisories (vitest, vite, postcss, js-yaml and
 * friends) are reported by the auditDevTree step in .github/workflows/
 * security.yml, which prints them and uploads them without blocking, so
 * narrowing the gate does not make them invisible.
 *
 * What this does instead: every high/critical advisory must be either FIXED or
 * explicitly, datedly deferred in ALLOWLIST below. A deferral needs a reason, a
 * review date and an expiry, so "we'll get to it" has a deadline instead of
 * becoming permanent blindness one severity notch down.
 *
 * The allowlist is also checked for rot in both directions:
 *   - an entry whose advisory no longer appears fails (delete the entry)
 *   - an entry past its `until` date fails (fix it, or re-review and re-date it)
 *   - a NEW advisory on an allowlisted package fails (the deferral covered the
 *     advisories that were reviewed, not the package forever)
 *
 * Usage:
 *   node scripts/check-npm-audit.mjs                  # runs npm audit in sdn-js
 *   node scripts/check-npm-audit.mjs --report x.json  # score a captured report
 */

import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(SCRIPTS_DIR, '..');

export const GATED_SEVERITIES = new Set(['high', 'critical']);

/**
 * Deferred advisories. Each entry states WHY the advisory does not reach this
 * package's users, and when that judgement expires.
 *
 * Do not add an entry to make a lane green. Add one when you have read the
 * advisory and can say where it stops.
 */
export const ALLOWLIST = [
  // EMPTY, and that is the result of the libp2p 3 upgrade (2026-09-18).
  //
  // Every entry that stood here deferred an advisory that could not be fixed
  // while sdn-js built its node on libp2p 1.9.4:
  //
  //   @libp2p/peer-store  GHSA-vrf4-mx87-p53w (8.2) — no in-range fix existed;
  //                       libp2p 1.x is EOL at 1.9.4 and the fix lands in
  //                       @libp2p/peer-store >=12.0.24. Now resolves 12.0.28.
  //   libp2p              carrier of the above. Now 3.3.11.
  //   @libp2p/kad-dht     GHSA-32mq-hpph-xfvr (7.5) — needed >=16.2.6.
  //                       Now 16.4.5.
  //   image-size + metro* React Native bundler chain reached through
  //                       @libp2p/webrtc -> react-native-webrtc. The
  //                       @libp2p/webrtc 6 bump moved it off the vulnerable
  //                       versions; react-native-webrtc is still installed and
  //                       still never runs in a browser or in dist/.
  //
  // `npm audit --omit=dev` reports 0 vulnerabilities. Do not add an entry to
  // make a lane green - add one when you have read the advisory and can say
  // where it stops, with a date on which that judgement expires.
];

export function runAudit({ cwd = resolve(REPO_ROOT, 'sdn-js') } = {}) {
  const result = spawnSync('npm', ['audit', '--omit=dev', '--json'], {
    cwd,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  });
  // npm audit exits non-zero whenever it finds anything; the JSON is what matters.
  if (result.stdout == null || result.stdout.trim() === '') {
    throw new Error(`npm audit produced no JSON report (stderr: ${result.stderr ?? ''})`);
  }
  return JSON.parse(result.stdout);
}

/** Advisory URLs directly attached to a vulnerability entry. */
function advisoryUrls(entry) {
  return (entry.via ?? [])
    .filter((via) => typeof via === 'object' && via !== null && typeof via.url === 'string')
    .map((via) => via.url)
    .sort();
}

export function evaluate(report, { today = new Date(), allowlist = ALLOWLIST } = {}) {
  const allowed = new Map(allowlist.map((e) => [e.package, e]));
  const seen = new Set();
  const failures = [];
  const deferred = [];

  for (const [name, entry] of Object.entries(report.vulnerabilities ?? {})) {
    if (!GATED_SEVERITIES.has(entry.severity)) continue;
    const urls = advisoryUrls(entry);
    const allowEntry = allowed.get(name);

    if (allowEntry == null) {
      failures.push(
        `${name} (${entry.severity}): ${urls.join(', ') || 'via ' + (entry.via ?? []).join(', ')}\n` +
          `      not allowlisted. Fix it, or add a dated ALLOWLIST entry saying where it stops.`,
      );
      continue;
    }

    seen.add(name);

    const expiry = new Date(`${allowEntry.until}T23:59:59Z`);
    if (today > expiry) {
      failures.push(
        `${name} (${entry.severity}): allowlist entry EXPIRED on ${allowEntry.until}.\n` +
          `      Fix the advisory or re-review and re-date the deferral.`,
      );
      continue;
    }

    const known = new Set(allowEntry.advisories ?? []);
    const novel = urls.filter((url) => !known.has(url));
    if (novel.length > 0) {
      failures.push(
        `${name} (${entry.severity}): NEW advisory not covered by the deferral: ${novel.join(', ')}\n` +
          `      The allowlist defers reviewed advisories, not the package forever.`,
      );
      continue;
    }

    deferred.push(`${name} (${entry.severity}) until ${allowEntry.until}`);
  }

  for (const entry of allowlist) {
    if (!seen.has(entry.package)) {
      failures.push(
        `${entry.package}: allowlisted but no longer reported at high/critical. Remove the entry.`,
      );
    }
  }

  const counts = report.metadata?.vulnerabilities ?? {};
  return { failures, deferred, counts };
}

const invokedDirectly =
  process.argv[1] != null && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));

if (invokedDirectly) {
  const reportFlag = process.argv.indexOf('--report');
  const report =
    reportFlag === -1
      ? runAudit()
      : JSON.parse(readFileSync(resolve(process.argv[reportFlag + 1]), 'utf8'));
  // runAudit() is the only path that is definitely the production closure; a
  // supplied report is whatever the caller captured, and may well be the full
  // tree.
  const scopeLabel = reportFlag === -1 ? 'production closure' : 'supplied report';

  const { failures, deferred, counts } = evaluate(report);

  console.log(
    // Names the scope it ACTUALLY scored. Hardcoding "production closure" made
    // a --report of the full tree print that phrase over dev-inclusive numbers,
    // which is the one string a later reader would trust when asking whether
    // the scope question had been addressed.
    `[npm-audit-gate] ${scopeLabel}: ${counts.critical ?? 0} critical, ${counts.high ?? 0} high, ` +
      `${counts.moderate ?? 0} moderate, ${counts.low ?? 0} low`,
  );
  if (deferred.length > 0) {
    console.log('[npm-audit-gate] deferred by dated allowlist:');
    for (const d of deferred) console.log(`    - ${d}`);
  }

  if (failures.length > 0) {
    console.error('\n[npm-audit-gate] FAIL:\n');
    for (const f of failures) console.error(`  - ${f}`);
    console.error('\n  Allowlist lives in scripts/check-npm-audit.mjs.');
    process.exit(1);
  }
  console.log('[npm-audit-gate] PASS: no unreviewed high or critical advisories');
}
