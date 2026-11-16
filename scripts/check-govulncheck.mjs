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
 * captured 2026-09-18 against go.mod's pins. Each needs a disposition. Linking
 * Kubo 0.43.1 (2026-10-05) moved pion/dtls, quic-go, webtransport-go, x/net and
 * x/text to fixed releases and cleared nine entries. Findings with fixes that
 * change Kubo's compiled dependency versions retain explicit dispositions.
 *
 * Do not add an entry to silence a new finding without reading it.
 */
export const BASELINE = [
  {
    id: 'GO-2024-3218',
    module: 'github.com/libp2p/go-libp2p-kad-dht',
    found: 'v0.42.2',
    fixed: null,
    note: 'CVE-2023-26248. The advisory has introduced:0 and NO fixed event — unfixed in every version, a known property of the Amino DHT rather than an upgrade lever. Not actionable by bumping.',
  },
  {
    id: 'GO-2026-6865',
    module: 'github.com/klauspost/compress',
    found: 'v1.19.1',
    fixed: 'v1.20.2',
    reviewed: '2026-11-16',
    note: "Panic on two-block frames. govulncheck reports symbol reachability from update.extractBundleArchive through zstd.NewReader to fse.Decompress and huff0.ReadTable. Kubo v0.43.1 compiles github.com/klauspost/compress v1.19.1; selecting v1.20.2 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
  },
  {
    id: 'GO-2026-6617',
    module: 'golang.org/x/net',
    found: 'v0.59.0',
    fixed: 'v0.60.0',
    reviewed: '2026-11-16',
    note: "HTTP/2 server crash due to an HPACK encoder race. govulncheck reports symbol reachability through the node HTTP/2 code, including MountedFlow.ServeHTTP to http2.ConnectionError.Error. Kubo v0.43.1 compiles golang.org/x/net v0.59.0; selecting v0.60.0 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
  },
  {
    id: 'GO-2026-6612',
    module: 'golang.org/x/net',
    found: 'v0.59.0',
    fixed: 'v0.60.0',
    reviewed: '2026-11-16',
    note: "Double flow control refund on HTTP/2 server streams. govulncheck reports symbol reachability through the node HTTP/2 code, including MountedFlow.ServeHTTP to http2.ConnectionError.Error. Kubo v0.43.1 compiles golang.org/x/net v0.59.0; selecting v0.60.0 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
  },
  {
    id: 'GO-2026-6611',
    module: 'golang.org/x/net',
    found: 'v0.59.0',
    fixed: 'v0.60.0',
    reviewed: '2026-11-16',
    note: "Excessive CPU consumption from repeated HTTP/2 initial window changes. govulncheck reports symbol reachability through the node HTTP/2 code, including MountedFlow.ServeHTTP to http2.ConnectionError.Error. Kubo v0.43.1 compiles golang.org/x/net v0.59.0; selecting v0.60.0 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
  },
  {
    id: 'GO-2026-6610',
    module: 'golang.org/x/net',
    found: 'v0.59.0',
    fixed: 'v0.60.0',
    reviewed: '2026-11-16',
    note: "HTTP/2 transport accepts malformed framing-related headers. The symbol finding names both x/net and net/http; its example traces show ipfsTipFetcher.Fetch calling http.Client.Do, without a separate x/net trace. Kubo v0.43.1 compiles golang.org/x/net v0.59.0; selecting v0.60.0 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
  },
  {
    id: 'GO-2026-6603',
    module: 'golang.org/x/net',
    found: 'v0.59.0',
    fixed: 'v0.60.0',
    reviewed: '2026-11-16',
    note: "HTTP/2 server memory exhaustion due to Trailer headers. govulncheck reports symbol reachability through the node HTTP/2 code, including Node.StartBackgroundEngineHydration through sync.Once.Do to http2.Framer.ReadFrame. Kubo v0.43.1 compiles golang.org/x/net v0.59.0; selecting v0.60.0 fails check-kubo-pin.js --build-list. This baseline records reachable exposure constrained by Kubo dependency lockstep.",
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
