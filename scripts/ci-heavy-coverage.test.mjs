import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

const definitions = readFileSync(new URL('./ci-local.sh', import.meta.url), 'utf8').split('case "$MODE" in')[0];
function run(mode) {
  const dir = mkdtempSync(join(tmpdir(), 'ci-heavy-coverage-'));
  try {
    mkdirSync(join(dir, 'scripts'));
    writeFileSync(join(dir, 'scripts/go-with-wasmedge.sh'), '#!/bin/bash\nprintf "TestA\\nTestB\\nTestC\\nTestD\\n"\n' + (mode === 'unknown-long' ? '' : 'printf "TestPublicationCatalogScaleDaily\\n"\n') + (mode === 'unsupported' ? 'printf "TestÉ\\n"\n' : ''), { mode: 0o755 });
    const script = definitions + `
ROOT="$1"
prepare_go_toolchain() { :; }
go_test_with_timings() {
  local group="\${1##*-}" name log="$ROOT/.ci-artifacts/gotest-$1.log"
  local budget=25m
  if [[ "$group" == long ]]; then budget=60m; fi
  [[ " $* " == *" -timeout=$budget "* ]] || return 1
  : > "$log"
  while read -r name; do
    [[ "$2" != "" ]] || return 1
    if [[ "$TEST_MODE" == missing && "$name" == TestC ]]; then continue; fi
    if [[ "$TEST_MODE" == missing-long && "$group" == long ]]; then continue; fi
    if [[ "$TEST_MODE" == incomplete-long && "$group" == long ]]; then
      printf '=== RUN   %s\\n' "$name" >> "$log"
      continue
    fi
    printf '=== RUN   %s\\n--- PASS: %s (0.00s)\\n' "$name" "$name" >> "$log"
    if [[ "$TEST_MODE" == duplicate && "$name" == TestB || "$TEST_MODE" == duplicate-long && "$group" == long ]]; then
      printf '=== RUN   %s\\n--- PASS: %s (0.00s)\\n' "$name" "$name" >> "$log"
    fi
    printf '=== RUN   %s/child\\n--- PASS: %s/child (0.00s)\\n' "$name" "$name" >> "$log"
  done < "$ROOT/.ci-artifacts/heavy-storage-$group.txt"
  if [[ "$TEST_MODE" == failed-long && "$group" == long ]]; then return 1; fi
}
run_go_heavy
`;
    return spawnSync('bash', ['-c', script, 'coverage-test', dir], { env: { ...process.env, TEST_MODE: mode }, encoding: 'utf8' });
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
test('accepts exactly one execution per listed test and ignores subtests', () => {
  const result = run('valid');
  assert.equal(result.status, 0, result.stderr + result.stdout);
});
for (const mode of ['missing', 'duplicate', 'unsupported', 'unknown-long', 'missing-long', 'incomplete-long', 'duplicate-long', 'failed-long']) {
  test(`rejects ${mode} tests`, () => {
    const result = run(mode);
    assert.notEqual(result.status, 0, result.stderr + result.stdout);
  });
}
