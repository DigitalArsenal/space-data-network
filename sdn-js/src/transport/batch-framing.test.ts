import { describe, it, expect } from 'vitest';
import { encodeSizePrefixedFrames, iterateSizePrefixedFrames } from './http.js';

/** Decode one prefix exactly as the server does (`binary.LittleEndian.Uint32`). */
const goLE = (b: Uint8Array, at: number) =>
  (b[at] | (b[at + 1] << 8) | (b[at + 2] << 16) | (b[at + 3] << 24)) >>> 0;

describe('size-prefixed frame stream', () => {
  const records = [new Uint8Array(200).fill(1), new Uint8Array(4).fill(2), new Uint8Array(65_536).fill(3)];

  it('encoder and iterator are inverses', () => {
    const back = [...iterateSizePrefixedFrames(encodeSizePrefixedFrames(records))];
    expect(back.map((f) => Array.from(f))).toEqual(records.map((r) => Array.from(r)));
  });

  it('prefix decodes with the server algorithm to the record length', () => {
    const stream = encodeSizePrefixedFrames(records);
    let off = 0;
    for (const rec of records) {
      expect(goLE(stream, off)).toBe(rec.byteLength);
      off += 4 + rec.byteLength;
    }
    expect(off).toBe(stream.byteLength);
  });

  it('never announces the byte-swapped length that broke batch publish', () => {
    // 200 written big-endian reads as 0xC8000000 little-endian — the bug.
    expect(goLE(encodeSizePrefixedFrames([new Uint8Array(200)]), 0)).toBe(200);
  });

  it('empty input is an empty stream', () => {
    expect(encodeSizePrefixedFrames([]).byteLength).toBe(0);
    expect([...iterateSizePrefixedFrames(new Uint8Array(0))]).toEqual([]);
  });
});
