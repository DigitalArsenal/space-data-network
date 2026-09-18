import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const packageRoot = new URL('..', import.meta.url);
// FORBID THE INTERFACE, NOT A LIST OF ITS METHODS.
//
// This used to enumerate deriveBits/deriveKey/decrypt/encrypt/digest, and a
// libp2p major then shipped 12 SubtleCrypto calls into dist/ that it waved
// through — importKey, generateKey, sign, verify, exportKey, none of which were
// on the list. A bundle either reaches for WebCrypto or it does not; which
// METHOD it reaches for is not the property being defended, and enumerating
// them is a game this check can only lose as upstream code changes.
//
// `crypto.subtle` alone is not enough either: a bundler hoists the object into
// a local (`const subtle = globalThis.crypto.subtle`) and every later call
// reads `subtle.x`, which no `crypto.subtle` pattern sees.
const forbiddenPatterns = [
  /\bcrypto\.subtle\b/,
  /\bsubtle\.[A-Za-z_$][\w$]*\s*\(/,
  /\bSubtleCrypto\b/,
];

async function collectFiles(
  root: string,
  {
    includeDist = false,
  }: {
    includeDist?: boolean;
  } = {},
): Promise<string[]> {
  const files: string[] = [];

  async function visit(directory: string): Promise<void> {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const filePath = join(directory, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === 'node_modules') {
          continue;
        }
        await visit(filePath);
        continue;
      }
      if (!entry.isFile()) {
        continue;
      }
      if (!/\.(?:ts|mts|mjs|js)$/.test(entry.name)) {
        continue;
      }
      if (!includeDist && /\.test\.(?:ts|mts|mjs|js)$/.test(entry.name)) {
        continue;
      }
      files.push(filePath);
    }
  }

  await visit(root);
  return files;
}

/**
 * Strip comments before matching, so a file can EXPLAIN why it avoids WebCrypto
 * without being accused of using it. The shims in src/shims exist precisely to
 * keep SubtleCrypto out of the bundle, and every one of them says so in prose.
 *
 * Deliberately not an exemption list for those files: a shim is exactly where a
 * real WebCrypto call would be least noticed, so they stay subject to the rule
 * and only their COMMENTS are removed. String literals are preserved — a URL's
 * "//" must not start a comment, and a forbidden call hidden in a string should
 * still be found.
 */
function stripComments(source: string): string {
  let out = '';
  let i = 0;
  let quote: string | null = null;
  while (i < source.length) {
    const c = source[i];
    const next = source[i + 1];
    if (quote != null) {
      out += c;
      if (c === '\\') {
        out += next ?? '';
        i += 2;
        continue;
      }
      if (c === quote) quote = null;
      i += 1;
      continue;
    }
    if (c === '"' || c === "'" || c === '`') {
      quote = c;
      out += c;
      i += 1;
      continue;
    }
    if (c === '/' && next === '/') {
      while (i < source.length && source[i] !== '\n') i += 1;
      continue;
    }
    if (c === '/' && next === '*') {
      i += 2;
      while (i < source.length && !(source[i] === '*' && source[i + 1] === '/')) i += 1;
      i += 2;
      continue;
    }
    out += c;
    i += 1;
  }
  return out;
}

async function filesContainingForbiddenWebCrypto(
  root: string,
  options?: { includeDist?: boolean },
): Promise<string[]> {
  const files = await collectFiles(root, options);
  const offenders: string[] = [];
  for (const file of files) {
    const source = stripComments(await readFile(file, 'utf8'));
    if (forbiddenPatterns.some((pattern) => pattern.test(source))) {
      offenders.push(relative(packageRoot.pathname, file));
    }
  }
  return offenders.sort();
}

describe('native/WASM crypto runtime boundary', () => {
  it('keeps browser WebCrypto out of production source', async () => {
    await expect(
      filesContainingForbiddenWebCrypto(join(packageRoot.pathname, 'src')),
    ).resolves.toEqual([]);
  });

  it('keeps browser WebCrypto out of published package bundles', async () => {
    await expect(
      filesContainingForbiddenWebCrypto(join(packageRoot.pathname, 'dist'), {
        includeDist: true,
      }),
    ).resolves.toEqual([]);
  });
});
