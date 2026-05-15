import fs from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const PACKAGE_LOCK_PATH = path.resolve(__dirname, '../package-lock.json');

type PackageLock = {
  packages?: Record<string, { version?: string }>;
};

function compareVersions(left: string, right: string): number {
  const leftParts = left.split('.').map((part) => Number.parseInt(part, 10));
  const rightParts = right.split('.').map((part) => Number.parseInt(part, 10));

  for (let index = 0; index < Math.max(leftParts.length, rightParts.length); index += 1) {
    const leftPart = leftParts[index] ?? 0;
    const rightPart = rightParts[index] ?? 0;
    if (leftPart !== rightPart) return leftPart - rightPart;
  }

  return 0;
}

describe('sdn-js production dependency audit floor', () => {
  it('keeps audit-sensitive transitive packages above known vulnerable ranges', async () => {
    const packageLock = JSON.parse(await fs.readFile(PACKAGE_LOCK_PATH, 'utf8')) as PackageLock;
    const requiredVersions = new Map([
      ['node_modules/axios', '1.16.1'],
      ['node_modules/dompurify', '3.4.3'],
      ['node_modules/follow-redirects', '1.16.0'],
      ['node_modules/picomatch', '4.0.4'],
    ]);

    for (const [packagePath, minimumVersion] of requiredVersions) {
      const actualVersion = packageLock.packages?.[packagePath]?.version;
      expect(actualVersion, `${packagePath} should be present in package-lock.json`).toBeTruthy();
      expect(
        compareVersions(actualVersion ?? '0.0.0', minimumVersion),
        `${packagePath} should be >= ${minimumVersion}; found ${actualVersion}`,
      ).toBeGreaterThanOrEqual(0);
    }
  });
});
