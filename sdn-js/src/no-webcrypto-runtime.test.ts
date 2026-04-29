import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const packageRoot = new URL('..', import.meta.url);
const forbiddenPatterns = [
  /\bcrypto\.subtle\b/,
  /\bsubtle\.deriveBits\b/,
  /\bsubtle\.deriveKey\b/,
  /\bsubtle\.decrypt\b/,
  /\bsubtle\.encrypt\b/,
  /\bsubtle\.digest\b/,
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

async function filesContainingForbiddenWebCrypto(
  root: string,
  options?: { includeDist?: boolean },
): Promise<string[]> {
  const files = await collectFiles(root, options);
  const offenders: string[] = [];
  for (const file of files) {
    const source = await readFile(file, 'utf8');
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
