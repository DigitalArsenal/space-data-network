import fs from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const UI_MAIN_PATH = path.resolve(__dirname, '../../ui/src/main.ts');

describe('ui main runtime loading', () => {
  it('lazy-loads the heavy SDN runtime modules instead of statically importing them at startup', async () => {
    const source = await fs.readFile(UI_MAIN_PATH, 'utf8');

    expect(source).not.toContain("from '../../src/node'");
    expect(source).not.toContain("from '../../src/discovery'");
    expect(source).not.toContain("from '../../src/module-delivery'");
    expect(source).not.toContain("from '../../src/crypto'");

    expect(source).toContain("import('../../src/node')");
    expect(source).toContain("import('../../src/discovery')");
    expect(source).toContain("import('../../src/module-delivery')");
    expect(source).toContain("import('../../src/crypto')");
  });
});
