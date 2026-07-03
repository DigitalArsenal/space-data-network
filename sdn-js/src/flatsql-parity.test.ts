// Isomorphism parity gate: the JS/V8 host (flatsql/standalone) must produce
// exactly the outputs recorded in shared-test-vectors/flatsql-parity.json —
// the same file the Go/WasmEdge host asserts against
// (sdn-server/internal/flatsqlrt/parity_test.go). Regenerate expectations
// with scripts/generate-flatsql-parity-vectors.mjs.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { describe, expect, test } from 'vitest';

// @ts-expect-error — plain .mjs helper shared with the generator script.
import { runParityCases } from '../scripts/generate-flatsql-parity-vectors.mjs';

const vectorsPath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../shared-test-vectors/flatsql-parity.json',
);

describe('FlatSQL Go⇄JS isomorphism parity', () => {
  test('JS host reproduces the shared expected outputs byte-for-byte', async () => {
    const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8'));
    expect(vectors.expected).toBeTruthy();

    const actual = await runParityCases(vectors);
    expect(actual).toEqual(vectors.expected);
  });
});
