#!/usr/bin/env bash
# Run the full synthetic baseline and print its JSON result.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUTPUT="${1:?usage: publication-baseline.sh RESULT_DIRECTORY}"
mkdir -p "$OUTPUT/logs" "${SDN_E2E_PUBLICATION_STORES:-$OUTPUT/stores}"
OUTPUT="$(cd "$OUTPUT" && pwd)"
export SDN_E2E_PUBLICATION_BASELINE=1
export SDN_E2E_PUBLICATION_RESULT="$OUTPUT/measurements.jsonl"
export SDN_E2E_PUBLICATION_STORES="${SDN_E2E_PUBLICATION_STORES:-$OUTPUT/stores}"
export SDN_E2E_PUBLICATION_LOGS="$OUTPUT/logs"
export TMPDIR="$SDN_E2E_PUBLICATION_STORES"
status=0
(cd "$ROOT/sdn-server" && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run '^TestPublicationBaseline$' -count=1 -timeout 0 -v) > "$OUTPUT/test.log" 2>&1 || status=$?
printf '%s\n' "$status" > "$OUTPUT/exit-code"
node - "$OUTPUT" "$status" <<'JS'
const fs = require('node:fs');
const [root, status] = process.argv.slice(2);
const path = root + '/measurements.jsonl';
const rows = fs.existsSync(path) ? fs.readFileSync(path, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
const result = {exit_code: Number(status), target_bytes_per_second: 250000000, rows};
const json = JSON.stringify(result, null, 2) + '\n';
fs.writeFileSync(root + '/result.json', json);
process.stdout.write(json);
JS
exit "$status"
