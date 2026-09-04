import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { sha384Digest } from './crypto/sha384';

const here = dirname(fileURLToPath(import.meta.url));
const engineDir = join(here, '..', '..', 'sdn-server', 'cmd', 'spacedatanetwork', 'embedded', 'sdn-js');

describe('flatsql wasm integrity digest', () => {
  it('reproduces the shipped integrity manifest without WebCrypto', async () => {
    const manifest = JSON.parse(await readFile(join(engineDir, 'integrity.json'), 'utf8')) as { hash: string; sri: string; size: number };
    const wasm = await readFile(join(engineDir, 'flatsql.wasm'));
    expect(wasm.byteLength).toBe(manifest.size);
    const digest = Buffer.from(sha384Digest(new Uint8Array(wasm))).toString('base64');
    expect(digest).toBe(manifest.hash);
    expect(manifest.sri).toBe(`sha384-${digest}`);
  });

  it('is SHA-384 (known answer)', () => {
    const digest = Buffer.from(sha384Digest(new TextEncoder().encode('abc'))).toString('hex');
    expect(digest).toBe('cb00753f45a35e8bb5a03d699ac65007272c32ab0eded1631a8b605a43ff5bed8086072ba1e7cc2358baeca134c825a7');
  });
});
