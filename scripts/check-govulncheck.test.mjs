import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { parseGovulncheck, evaluate, BASELINE } from './check-govulncheck.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const checkerPath = resolve(scriptsDir, 'check-govulncheck.mjs');

/**
 * Verbatim shape of govulncheck's default text output, trimmed from a real
 * `govulncheck ./...` run against sdn-server on 2026-09-18. The parser is a text
 * parser, so the fixture has to be the real text.
 */
const REAL_EXCERPT = `=== Symbol Results ===

Vulnerability #1: GO-2026-6218
    Incorrect parsing in net/url
  More info: https://pkg.go.dev/vuln/GO-2026-6218
    Found in: net/url@go1.26.1
    Fixed in: net/url@go1.26.6
    Example traces found:
      #1: internal/api/server.go:12:3: api.parse calls url.Parse

Vulnerability #2: GO-2026-6165
    Panic in github.com/pion/dtls/v3
  More info: https://pkg.go.dev/vuln/GO-2026-6165
  Module: github.com/pion/dtls/v3
    Found in: github.com/pion/dtls/v3@v3.0.6
    Fixed in: github.com/pion/dtls/v3@v3.1.4

Vulnerability #3: GO-2024-3218
    Amino DHT behaviour
  More info: https://pkg.go.dev/vuln/GO-2024-3218
  Module: github.com/libp2p/go-libp2p-kad-dht
    Found in: github.com/libp2p/go-libp2p-kad-dht@v0.36.0
    Fixed in: N/A

Vulnerability #4: GO-2026-5026
    Improper handling in golang.org/x/net
  More info: https://pkg.go.dev/vuln/GO-2026-5026
  Module: golang.org/x/net
    Found in: golang.org/x/net@v0.54.0
    Fixed in: golang.org/x/net@v0.55.0
    Found in: net/http@go1.26.1
    Fixed in: net/http@go1.26.6

Your code is affected by 4 vulnerabilities from 3 modules and the Go standard library.
`;

function reportFile(t, text) {
  const dir = mkdtempSync(join(tmpdir(), 'govulncheck-gate-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const file = join(dir, 'report.txt');
  writeFileSync(file, text);
  return file;
}

/** A synthetic report containing exactly the baselined module findings. */
function baselineReport() {
  let out = '=== Symbol Results ===\n\n';
  BASELINE.forEach((entry, i) => {
    out += `Vulnerability #${i + 1}: ${entry.id}\n`;
    out += `  More info: https://pkg.go.dev/vuln/${entry.id}\n`;
    out += `  Module: ${entry.module}\n`;
    out += `    Found in: ${entry.module}@${entry.found}\n`;
    out += `    Fixed in: ${entry.fixed ? `${entry.module}@${entry.fixed}` : 'N/A'}\n\n`;
  });
  return out;
}

test('splits module findings from toolchain-dependent stdlib findings', () => {
  const { modules, stdlib } = parseGovulncheck(REAL_EXCERPT);
  assert.deepEqual(
    modules.map((m) => m.id),
    ['GO-2026-6165', 'GO-2024-3218', 'GO-2026-5026'],
  );
  assert.deepEqual(
    stdlib.map((s) => s.id),
    ['GO-2026-6218'],
  );
});

test('a Module: line wins even when the block also names a stdlib package', () => {
  // GO-2026-5026 reports golang.org/x/net AND net/http. It is a go.mod lever,
  // so it must be gated, not filed under "upgrade your toolchain".
  const { modules } = parseGovulncheck(REAL_EXCERPT);
  const net = modules.find((m) => m.id === 'GO-2026-5026');
  assert.equal(net.module, 'golang.org/x/net');
  assert.equal(net.found, 'golang.org/x/net@v0.54.0');
});

test('reads "Fixed in: N/A" as no fix upstream', () => {
  const { modules } = parseGovulncheck(REAL_EXCERPT);
  assert.equal(modules.find((m) => m.id === 'GO-2024-3218').fixed, null);
  assert.equal(modules.find((m) => m.id === 'GO-2026-6165').fixed, 'github.com/pion/dtls/v3@v3.1.4');
});

test('an empty report parses to nothing rather than throwing', () => {
  assert.deepEqual(parseGovulncheck('No vulnerabilities found.\n'), { modules: [], stdlib: [] });
});

test('the recorded baseline passes', () => {
  const { failures } = evaluate(baselineReport());
  assert.deepEqual(failures, []);
});

test('a NEW module vulnerability fails the gate', () => {
  const report =
    baselineReport() +
    [
      'Vulnerability #99: GO-2027-9999',
      '  Module: github.com/example/newly-vulnerable',
      '    Found in: github.com/example/newly-vulnerable@v1.0.0',
      '    Fixed in: github.com/example/newly-vulnerable@v1.0.1',
      '',
    ].join('\n');
  const { failures } = evaluate(report);
  assert.equal(failures.length, 1);
  assert.match(failures[0], /NEW module vulnerability GO-2027-9999/);
  assert.match(failures[0], /fixed in github.com\/example\/newly-vulnerable@v1\.0\.1/);
});

test('new stdlib findings do NOT fail the gate — they are runner-dependent', () => {
  // The same commit scanned on two Go patch releases yields different stdlib
  // sets. Gating them would make the lane flaky rather than informative.
  const report =
    baselineReport() +
    ['Vulnerability #99: GO-2027-8888', '    Found in: crypto/tls@go1.26.1', ''].join('\n');
  const { failures, stdlib } = evaluate(report);
  assert.deepEqual(failures, []);
  assert.equal(stdlib.length, 1);
});

test('a baselined finding that disappeared fails so the baseline cannot rot', () => {
  const dropped = BASELINE[0];
  const report = baselineReport().replace(
    new RegExp(`Vulnerability #1: ${dropped.id}[\\s\\S]*?\\n\\n`),
    '',
  );
  const { failures } = evaluate(report);
  assert.equal(failures.length, 1);
  assert.match(failures[0], new RegExp(`${dropped.id}.*no longer reported\\. Remove the entry\\.`));
});

test('every baseline entry names its module, what was found, and a disposition', () => {
  for (const entry of BASELINE) {
    assert.match(entry.id, /^GO-\d{4}-\d+$/);
    assert.ok(entry.module.includes('/'), `${entry.id} needs a module path`);
    assert.match(entry.found, /^v\d/, `${entry.id} needs the found version`);
    assert.ok((entry.note ?? '').length > 30, `${entry.id} needs a disposition`);
    if (entry.fixed === null) {
      assert.match(
        entry.note,
        /NO FIX UPSTREAM|unfixed in every version/,
        `${entry.id} has no fix, so the note must say so`,
      );
    }
  }
});

test('CLI passes on the baseline and fails on a new module finding', (t) => {
  const ok = spawnSync(process.execPath, [checkerPath, '--report', reportFile(t, baselineReport())], {
    encoding: 'utf8',
  });
  assert.equal(ok.status, 0, ok.stderr);
  assert.match(ok.stdout, /PASS: no new module vulnerabilities \(10 baselined\)/);

  const bad = spawnSync(
    process.execPath,
    [
      checkerPath,
      '--report',
      reportFile(
        t,
        baselineReport() +
          'Vulnerability #99: GO-2027-7777\n  Module: github.com/example/x\n    Found in: github.com/example/x@v1.0.0\n    Fixed in: N/A\n',
      ),
    ],
    { encoding: 'utf8' },
  );
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /NEW module vulnerability GO-2027-7777/);
  assert.match(bad.stderr, /NO FIX UPSTREAM/);
});
