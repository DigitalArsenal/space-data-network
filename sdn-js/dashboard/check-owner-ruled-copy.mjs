#!/usr/bin/env node
/*
 * Owner-ruled copy guard.
 *
 * The owner reported the same semantic defect THREE times. Two causes were
 * found and fixed; this file exists because of the third: copy that lives in
 * the DESIGN SOURCE and therefore regenerates whatever we delete downstream.
 * IRIS's verdict, verbatim: "a consumer-only strip will not hold — the next
 * .dc.html export re-emits them and he files this a fourth time."
 *
 * So the strip is not a strip. It is a check, it is wired into the build (not a
 * manual step), and it names BOTH sides when it fails.
 *
 * Who wins per change class, and which directory belongs to whom, is ruled in
 * spaceaware-ui/UI_SOURCE_OF_TRUTH.md. A hit in the design tool's territory is
 * NOT fixed by editing that territory — it routes to
 * graph/tasks/design-tool-strip-menu-clutter-at-source.md, because only the
 * design tool writes there.
 *
 * Tasks: ui-menu-label-clutter-purge (closed on a consumer-side strip — this is
 * what makes that closure durable), design-tool-strip-menu-clutter-at-source.
 *
 * Ruled by IRIS 2026-07-30 (ui-design-lib-two-way-sync, acceptance #3).
 *
 * Usage:
 *   node check-owner-ruled-copy.mjs --sources    (before vite)
 *   node check-owner-ruled-copy.mjs --artifact   (after vite)
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const cfg = JSON.parse(fs.readFileSync(path.join(__dirname, 'owner-ruled-copy.json'), 'utf8'));

const designRoot = path.resolve(__dirname, '../spaceaware-ui/src/dashboard');
const artifactPath = path.resolve(
  __dirname,
  '../../sdn-server/cmd/spacedatanetwork/embedded/dashboard.html'
);

const SCOPE_DIRS = {
  lib: path.join(designRoot, 'src/lib'),
  console: path.join(designRoot, 'orbital-console'),
  archive: path.join(designRoot, 'archive')
};

function walk(dir) {
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return e.name === '.git' ? [] : walk(p);
    // Binary export assets carry no copy; skip so a stray byte sequence in a
    // screenshot can never fail the build.
    return /\.(png|jpg|jpeg|gif|webp|woff2?|zip|ico)$/i.test(e.name) ? [] : [p];
  });
}

/** Occurrences of each banned pattern, plus the files they came from. */
function scan(files) {
  const counts = {};
  const where = {};
  for (const { id, pattern } of cfg.banned) {
    const re = new RegExp(pattern, 'g');
    let n = 0;
    for (const f of files) {
      let text;
      try {
        text = fs.readFileSync(f, 'utf8');
      } catch {
        continue;
      }
      const hits = text.match(re);
      if (!hits) continue;
      n += hits.length;
      (where[id] ??= []).push(`${path.relative(process.cwd(), f)} (${hits.length})`);
    }
    if (n) counts[id] = n;
  }
  return { counts, where };
}

const mode = process.argv.includes('--artifact') ? 'artifact' : 'sources';
const scopes =
  mode === 'artifact'
    ? { artifact: fs.existsSync(artifactPath) ? [artifactPath] : [] }
    : Object.fromEntries(Object.entries(SCOPE_DIRS).map(([k, d]) => [k, walk(d)]));

let failed = false;

for (const [scope, files] of Object.entries(scopes)) {
  const { counts, where } = scan(files);
  const verdict = cfg.scopes[scope]?.verdict ?? 'fail';
  const baseline = cfg.baseline.counts[scope] ?? {};

  for (const [id, n] of Object.entries(counts)) {
    const expected = baseline[id];

    if (verdict === 'warn') {
      console.warn(
        `[owner-ruled-copy] WARN ${scope}: "${id}" x${n} — ${cfg.scopes[scope].why}`
      );
      continue;
    }
    if (expected === n) continue; // known, recorded debt — unchanged

    failed = true;
    const kind = expected === undefined ? 'NEW OFFENDER' : `COUNT CHANGED (baseline ${expected})`;
    console.error(`\n[owner-ruled-copy] FAIL ${scope}: "${id}" x${n} — ${kind}`);
    console.error(`  pattern : ${cfg.banned.find((b) => b.id === id).pattern}`);
    console.error(`  found in: ${(where[id] ?? []).join('\n            ')}`);
    console.error(`  why     : ${cfg.scopes[scope].why}`);
  }

  // A baselined offender that DISAPPEARED is good news, but the baseline is now
  // a lie — and a stale baseline is how a guard silently stops guarding.
  for (const [id, expected] of Object.entries(baseline)) {
    if ((counts[id] ?? 0) < expected) {
      failed = true;
      console.error(
        `\n[owner-ruled-copy] FAIL ${scope}: "${id}" is down to x${counts[id] ?? 0} ` +
          `(baseline ${expected}). The debt shrank — delete it from the baseline in ` +
          `owner-ruled-copy.json so the check tightens instead of drifting.`
      );
    }
  }
}

if (failed) {
  console.error(
    `\n[owner-ruled-copy] Owner-ruled copy is LAW (spaceaware-ui/UI_SOURCE_OF_TRUTH.md).\n` +
      `A hit in the design tool's territory (orbital-console/) is NOT fixed here — the\n` +
      `correction goes into the design tool: graph/tasks/design-tool-strip-menu-clutter-at-source.md\n`
  );
  process.exit(1);
}

console.log(`[owner-ruled-copy] ${mode}: clean against baseline ${cfg.baseline.date}`);
