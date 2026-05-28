import fs from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT_PACKAGE_JSON_PATH = path.resolve(__dirname, '../../package.json');

type PackageJson = {
  scripts?: Record<string, string>;
  dependencies?: Record<string, string>;
  devDependencies?: Record<string, string>;
};

describe('root package CI install contract', () => {
  it('installs the binary used by the root prepare script', async () => {
    const packageJson = JSON.parse(await fs.readFile(ROOT_PACKAGE_JSON_PATH, 'utf8')) as PackageJson;
    const prepareCommand = packageJson.scripts?.prepare?.trim().split(/\s+/)[0];

    expect(prepareCommand).toBeTruthy();
    expect({
      ...packageJson.dependencies,
      ...packageJson.devDependencies,
    }).toHaveProperty(prepareCommand ?? '');
  });
});
