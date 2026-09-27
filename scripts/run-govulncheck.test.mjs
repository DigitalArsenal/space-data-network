import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const report = '=== Symbol Results ===\nVulnerability #1: GO-2024-3218\n';

for (const mode of ['relative', 'absolute', 'default']) {
  test(`writes the ${mode} report path while scanning from sdn-server`, (t) => {
    const root = mkdtempSync(join(tmpdir(), 'sdn-govulncheck-'));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    for (const dir of ['scripts', 'sdn-server', 'wasmedge/bin', 'wasmedge/lib', 'wasmedge/include/wasmedge']) {
      mkdirSync(join(root, dir), { recursive: true });
    }
    copyFileSync(join(scriptsDir, 'run-govulncheck.sh'), join(root, 'scripts/run-govulncheck.sh'));
    writeFileSync(join(root, 'wasmedge/include/wasmedge/wasmedge.h'), '');
    writeFileSync(join(root, 'wasmedge/bin/govulncheck'), `#!/usr/bin/env bash
[[ "$#" == 1 && "$1" == ./... ]] || exit 99
printf '%s' "$PWD" > "$SCAN_CWD"
printf '%s' "$SCAN_REPORT"
exit 3
`, { mode: 0o755 });
    const relative = 'sdn-server/scan report.txt';
    const out = mode === 'default' ? join(root, 'sdn-server/govulncheck-report.txt') : join(root, relative);
    const args = mode === 'default' ? [] : ['--out', mode === 'absolute' ? out : relative];
    const result = spawnSync('bash', [join(root, 'scripts/run-govulncheck.sh'), ...args], {
      cwd: root,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${join(root, 'wasmedge/bin')}:${process.env.PATH}`,
        WASMEDGE_DIR: join(root, 'wasmedge'),
        SCAN_CWD: join(root, 'scan-cwd'),
        SCAN_REPORT: report,
      },
    });
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
    assert.equal(readFileSync(out, 'utf8'), report);
    assert.equal(readFileSync(join(root, 'scan-cwd'), 'utf8'), join(root, 'sdn-server'));
  });
}
