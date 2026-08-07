// Pin guard for the defect that killed browser module delivery in sdn-js 2.0.14.
//
// sdn-js delegates the licensing challenge to `space-data-module-sdk`. The 0.8.5
// pin called `LCH.createLCH` with 17 arguments against an 18-field table ($LCH
// grew REQUESTER_EPM in SDS 1.134.0). flatbuffers-js turns the missing trailing
// argument into a PRESENT vtable slot holding a ZERO uoffset, which FlatBuffers
// forbids, so the provider's C++ `VerifyLCHBuffer` rejected the buffer and
// answered with 0 bytes. Nothing in sdn-js noticed, because the test fixtures
// that simulate a provider were short by exactly the same argument and the JS
// decoder is lenient about absent fields.
//
// Two guards, both structural against sdn-js's OWN SDS pin:
//   1. the challenge bytes the pinned SDK produces must contain no zero-uoffset
//      offset field -- a downgraded or drifted module-sdk pin fails the build;
//   2. every hand-written `<T>.create<T>(builder, ...)` in src/, fixtures
//      included, must match its generated arity -- the next field appended to
//      any table fails the build instead of the browser.

import { readFileSync, readdirSync, statSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';
import { LCH } from 'spacedatastandards.org/lib/js/REC/LCH.js';
import { encodeLicensingChallengeRequest } from 'space-data-module-sdk/licensing';

const SRC_DIR = path.dirname(fileURLToPath(import.meta.url));

// module-sdk resolves `spacedatastandards.org` from its OWN nested install, so
// the class it hands to flatbuffers is not the one sdn-js imports above. Reach
// the SDK's copy the way node does, or the guards below watch the wrong object.
const requireFromSdk = createRequire(
  createRequire(import.meta.url).resolve('space-data-module-sdk/licensing'),
);
const { LCH: sdkLCH } = (await import(
  pathToFileURL(requireFromSdk.resolve('spacedatastandards.org/lib/js/LCH/main.js')).href
)) as { LCH: typeof LCH };

const CHALLENGE_OPTIONS = {
  reqId: 'req-123',
  moduleId: 'com.space-data-network.rf-empirical',
  moduleVersion: '0.5.22',
  requesterPeerId: 'requester-peer-id',
  requesterXpub: 'xpub-requester',
  requesterSigningPublicKey: new Uint8Array(32).fill(6),
  requesterEphemeralPublicKey: new Uint8Array(32).fill(8),
  requesterDomain: 'app.example.com',
  requestedTimeoutMs: 300_000,
  requestedAtMs: 1_700_000_000_000,
  providerPeerId: 'provider-peer-id',
};

/**
 * Discovers which vtable slots the generated builder writes as OFFSETS by
 * replaying `create<T>` against a recording Builder. The generated code is the
 * only source of truth; nothing about the schema is hardcoded here.
 */
function offsetFieldIndexes(
  create: (builder: flatbuffers.Builder, ...args: never[]) => number,
  args: unknown[],
): Set<number> {
  const recorder = new flatbuffers.Builder(1024) as flatbuffers.Builder & {
    addFieldOffset: (voffset: number, value: number, defaultValue: number) => void;
  };
  const indexes = new Set<number>();
  const original = recorder.addFieldOffset.bind(recorder);
  recorder.addFieldOffset = (voffset, value, defaultValue) => {
    indexes.add(voffset);
    original(voffset, value, defaultValue);
  };
  (create as (...a: unknown[]) => number)(recorder, ...args);
  return indexes;
}

/** Mirrors C++ `Verifier::VerifyOffset`: a present offset field is never zero. */
function zeroUoffsetFields(bytes: Uint8Array, offsetIndexes: Set<number>): number[] {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const rootPos = view.getUint32(0, true);
  const vtablePos = rootPos - view.getInt32(rootPos, true);
  const vtableBytes = view.getUint16(vtablePos, true);
  const offenders: number[] = [];

  for (const fieldIndex of offsetIndexes) {
    if (4 + fieldIndex * 2 + 2 > vtableBytes) continue; // trimmed => absent
    const slot = view.getUint16(vtablePos + 4 + fieldIndex * 2, true);
    if (slot === 0) continue; // absent
    const fieldPos = rootPos + slot;
    if (fieldPos + 4 > bytes.byteLength || view.getUint32(fieldPos, true) === 0) {
      offenders.push(fieldIndex);
    }
  }
  return offenders;
}

function collectSourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === 'node_modules' || entry === 'generated') continue;
      collectSourceFiles(full, out);
      continue;
    }
    if (entry.endsWith('.ts') && !entry.endsWith('.d.ts')) out.push(full);
  }
  return out;
}

/** Splits a `create...(` argument list into top-level arguments. */
function splitTopLevelArgs(source: string, openParenIndex: number): string[] {
  const args: string[] = [];
  let depth = 0;
  let current = '';
  let quote: string | null = null;

  for (let i = openParenIndex + 1; i < source.length; i += 1) {
    const ch = source[i];
    if (quote) {
      if (ch === '\\') {
        current += ch + source[i + 1];
        i += 1;
        continue;
      }
      if (ch === quote) quote = null;
      current += ch;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === '`') {
      quote = ch;
      current += ch;
      continue;
    }
    if (ch === '(' || ch === '[' || ch === '{') depth += 1;
    if (ch === ')' || ch === ']' || ch === '}') {
      if (ch === ')' && depth === 0) {
        if (current.trim()) args.push(current.trim());
        return args;
      }
      depth -= 1;
    }
    if (ch === ',' && depth === 0) {
      args.push(current.trim());
      current = '';
      continue;
    }
    current += ch;
  }
  throw new Error('unterminated argument list');
}

function resolveImportSpecifier(source: string, symbol: string): string | null {
  const pattern = /import\s*\{([^}]*)\}\s*from\s*["']([^"']+)["']/g;
  for (const match of source.matchAll(pattern)) {
    const names = match[1]
      .split(',')
      .map((name) => name.trim().split(/\s+as\s+/).pop());
    if (names.includes(symbol)) return match[2];
  }
  return null;
}

describe('FlatBuffers table-builder arity', () => {
  it('the module-sdk pin and the sdn-js pin agree on the $LCH table shape', async () => {
    // npm gives module-sdk its own nested `spacedatastandards.org`, so the two
    // packages can silently disagree about $LCH. That disagreement is what an
    // encoder short by one argument looks like from sdn-js's side, and nothing
    // else in this repo would notice it.
    expect(sdkLCH.createLCH.length).toBe(LCH.createLCH.length);
  });

  it('the pinned module-sdk emits a challenge with no zero-uoffset offset field', async () => {
    const original = sdkLCH.createLCH;
    let captured: unknown[] | null = null;
    (sdkLCH as unknown as Record<string, unknown>).createLCH = function spy(
      this: unknown,
      ...args: unknown[]
    ) {
      captured = args;
      return (original as (...a: unknown[]) => number).apply(this, args);
    };

    let bytes: Uint8Array;
    try {
      bytes = encodeLicensingChallengeRequest({ ...CHALLENGE_OPTIONS });
    } finally {
      (sdkLCH as unknown as Record<string, unknown>).createLCH = original;
    }

    // One argument per table field -- this is the 0.8.5 defect, asserted
    // structurally so appending a field to $LCH fails the build.
    expect(captured).not.toBeNull();
    expect((captured as unknown[]).length).toBe(original.length);
    for (const [index, value] of (captured as unknown[]).entries()) {
      expect(value, `LCH.createLCH argument ${index} is undefined`).not.toBe(undefined);
    }

    // ...and the bytes on the wire must survive the provider's verifier.
    const offsetIndexes = offsetFieldIndexes(
      original as never,
      (captured as unknown[]).slice(1),
    );
    expect(zeroUoffsetFields(bytes, offsetIndexes)).toEqual([]);
    expect(LCH.bufferHasIdentifier(new flatbuffers.ByteBuffer(bytes))).toBe(true);
  });

  it('every hand-written SDS table builder in src/ matches its generated arity', async () => {
    const callPattern = /\b([A-Z][A-Za-z0-9]{1,15})\.create([A-Z][A-Za-z0-9]*)\s*\(/g;
    const checked: string[] = [];

    for (const file of collectSourceFiles(SRC_DIR)) {
      const source = readFileSync(file, 'utf8');
      for (const match of source.matchAll(callPattern)) {
        const [, symbol, tableName] = match;
        if (tableName.endsWith('Vector')) continue; // vectors take (builder, data)
        const specifier = resolveImportSpecifier(source, symbol);
        if (!specifier?.startsWith('spacedatastandards.org/')) continue;

        const namespace = (await import(specifier)) as Record<string, unknown>;
        const table = namespace[symbol] as Record<string, unknown> | undefined;
        const method = table?.[`create${tableName}`] as ((...a: never[]) => number) | undefined;
        if (typeof method !== 'function') continue;

        const openParen = (match.index ?? 0) + match[0].length - 1;
        const args = splitTopLevelArgs(source, openParen);
        const relative = path.relative(SRC_DIR, file);
        expect(
          args.length,
          `${relative}: ${symbol}.create${tableName} called with ${args.length} args but the ` +
            `generated builder takes ${method.length} (builder + one per table field). ` +
            `A field was added to $${symbol}; update the call site.`,
        ).toBe(method.length);
        checked.push(`${relative}:${symbol}.create${tableName}`);
      }
    }

    // A silent zero would make this guard useless.
    expect(checked.some((entry) => entry.endsWith('LCH.createLCH'))).toBe(true);
  });
});
