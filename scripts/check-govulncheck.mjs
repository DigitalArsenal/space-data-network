#!/usr/bin/env node
/**
 * check-govulncheck.mjs
 *
 * A gate over govulncheck for sdn-server, built the same way as
 * check-npm-audit.mjs and for the same reason: security.yml ran govulncheck,
 * captured the exit code, and then did `exit 0` with the comment
 * "Don't fail the build, just report". It could not go red.
 *
 * THE SPLIT THAT MAKES THIS GATEABLE
 *
 * govulncheck's "affecting your code" findings come in two kinds:
 *
 *   - MODULE findings (a `Module:` line) are decided by sdn-server/go.mod.
 *     They are identical on every machine and every runner, so they can be
 *     baselined and gated.
 *
 *   - STDLIB findings (no `Module:` line) are decided by the Go TOOLCHAIN that
 *     happens to run the scan. A developer on go1.26.1 and a runner on a
 *     different patch produce different sets, so gating them would be flaky —
 *     and the fix is never a dependency edit, it is moving the toolchain.
 *
 * So: module findings are gated against BASELINE below. Stdlib findings are
 * reported with the toolchain that produced them, and are the Go version's
 * problem, not go.mod's.
 *
 * The gate fails on a NEW module vulnerability, and equally on a baselined one
 * that has disappeared — a baseline that is allowed to rot is the same blindness
 * as `exit 0`, just slower.
 *
 * Usage:
 *   node scripts/check-govulncheck.mjs --report govulncheck-report.txt
 *
 * See docs/sdn-js-helia-libp2p-layering.md ("Go side") for the audit.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = dirname(fileURLToPath(import.meta.url));

/**
 * Module-level vulnerabilities govulncheck reports as reaching sdn-server code,
 * captured 2026-09-18 against go.mod's pins. Each needs a disposition.
 *
 * Do not add an entry to silence a new finding without reading it.
 */
export const BASELINE = [
  {
    id: 'GO-2026-6165',
    module: 'github.com/pion/dtls/v3',
    found: 'v3.0.6',
    fixed: 'v3.1.4',
    note: 'Reached through the WebRTC transport. Fix is available; needs a reviewed go.mod bump.',
  },
  {
    id: 'GO-2026-6099',
    module: 'github.com/quic-go/webtransport-go',
    found: 'v0.9.0',
    fixed: 'v0.11.1',
    note: 'Fix available; bundled with the quic-go/webtransport bump below.',
  },
  {
    id: 'GO-2026-4488',
    module: 'github.com/quic-go/webtransport-go',
    found: 'v0.9.0',
    fixed: 'v0.10.0',
    note: 'Fix available; same bump as GO-2026-6099.',
  },
  {
    id: 'GO-2026-4485',
    module: 'github.com/quic-go/webtransport-go',
    found: 'v0.9.0',
    fixed: 'v0.10.0',
    note: 'Fix available; same bump as GO-2026-6099.',
  },
  {
    id: 'GO-2026-4483',
    module: 'github.com/quic-go/webtransport-go',
    found: 'v0.9.0',
    fixed: 'v0.10.0',
    note: 'Fix available; same bump as GO-2026-6099.',
  },
  {
    id: 'GO-2026-5676',
    module: 'github.com/quic-go/quic-go',
    found: 'v0.58.1',
    fixed: 'v0.59.1',
    note: 'Fix available; needs a reviewed go.mod bump.',
  },
  {
    id: 'GO-2026-5970',
    module: 'golang.org/x/text',
    found: 'v0.37.0',
    fixed: 'v0.39.0',
    note: 'Fix available; routine x/ bump.',
  },
  {
    id: 'GO-2026-5026',
    module: 'golang.org/x/net',
    found: 'v0.54.0',
    fixed: 'v0.55.0',
    note: 'Fix available; routine x/ bump.',
  },
  {
    id: 'GO-2026-4479',
    module: 'github.com/pion/dtls/v2',
    found: 'v2.2.12',
    fixed: null,
    note: 'NO FIX UPSTREAM in the v2 line. Only reachable if something still pulls pion/dtls v2; the v3 bump above is the real lever.',
  },
  {
    id: 'GO-2024-3218',
    module: 'github.com/libp2p/go-libp2p-kad-dht',
    found: 'v0.36.0',
    fixed: null,
    note: 'CVE-2023-26248. The advisory has introduced:0 and NO fixed event — unfixed in every version, a known property of the Amino DHT rather than an upgrade lever. Not actionable by bumping.',
  },
];

/**
 * Parse govulncheck's default text report into its two kinds of finding.
 * Only the "affecting your code" section carries `Vulnerability #N:` headers.
 */
export function parseGovulncheck(text) {
  const blocks = text.split(/Vulnerability #\d+: /).slice(1);
  const modules = [];
  const stdlib = [];
  for (const block of blocks) {
    const id = block.split('\n')[0].trim();
    if (!/^GO-\d{4}-\d+$/.test(id)) continue;
    const moduleMatch = /^\s*Module:\s*(\S+)/m.exec(block);
    const found = /Found in:\s*(\S+)/.exec(block);
    const fixed = /Fixed in:\s*(\S+)/.exec(block);
    const entry = {
      id,
      found: found?.[1] ?? null,
      fixed: fixed?.[1] === 'N/A' ? null : (fixed?.[1] ?? null),
    };
    if (moduleMatch == null) stdlib.push(entry);
    else modules.push({ ...entry, module: moduleMatch[1] });
  }
  return { modules, stdlib };
}

export function evaluate(text) {
  const { modules, stdlib } = parseGovulncheck(text);
  const baseline = new Map(BASELINE.map((e) => [e.id, e]));
  const seen = new Set();
  const failures = [];

  for (const finding of modules) {
    if (!baseline.has(finding.id)) {
      failures.push(
        `NEW module vulnerability ${finding.id} in ${finding.module} ` +
          `(found ${finding.found}${finding.fixed ? `, fixed in ${finding.fixed}` : ', NO FIX UPSTREAM'}).\n` +
          `      Bump it, or add a BASELINE entry in scripts/check-govulncheck.mjs saying why not.`,
      );
      continue;
    }
    seen.add(finding.id);
  }

  for (const entry of BASELINE) {
    if (!seen.has(entry.id)) {
      failures.push(
        `${entry.id} (${entry.module}) is baselined but no longer reported. Remove the entry.`,
      );
    }
  }

  return { failures, modules, stdlib };
}

const invokedDirectly =
  process.argv[1] != null && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));

if (invokedDirectly) {
  const reportFlag = process.argv.indexOf('--report');
  if (reportFlag === -1) {
    console.error('usage: check-govulncheck.mjs --report <govulncheck-report.txt>');
    process.exit(2);
  }
  const text = readFileSync(resolve(process.argv[reportFlag + 1]), 'utf8');
  const { failures, modules, stdlib } = evaluate(text);

  console.log(
    `[govulncheck-gate] affecting sdn-server code: ${modules.length} module, ${stdlib.length} stdlib`,
  );
  if (stdlib.length > 0) {
    console.log(
      `[govulncheck-gate] the ${stdlib.length} stdlib findings are a TOOLCHAIN lever, not a go.mod one:`,
    );
    console.log(
      `    ${stdlib.map((s) => s.id).join(', ')}`.slice(0, 400),
    );
    console.log('    they clear by building on a Go release that carries their fixes.');
  }

  if (failures.length > 0) {
    console.error('\n[govulncheck-gate] FAIL:\n');
    for (const f of failures) console.error(`  - ${f}`);
    process.exit(1);
  }
  console.log(
    `[govulncheck-gate] PASS: no new module vulnerabilities (${BASELINE.length} baselined)`,
  );
}

export { SCRIPTS_DIR };
