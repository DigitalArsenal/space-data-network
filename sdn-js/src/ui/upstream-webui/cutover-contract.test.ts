import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

const uiSrcPath = path.resolve(__dirname, '../../../ui/src');
const mainEntryPath = path.join(uiSrcPath, 'main.ts');

async function listFilesRecursively(rootPath: string, currentPath = rootPath): Promise<string[]> {
  const entries = await fs.readdir(currentPath, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const entryPath = path.join(currentPath, entry.name);
    if (entry.isDirectory()) {
      return listFilesRecursively(rootPath, entryPath);
    }
    return path.relative(rootPath, entryPath);
  }));
  return files.flat().sort();
}

describe('sdn upstream webui cutover contract', () => {
  it('keeps ui/src limited to the upstream webui entry tree', async () => {
    await expect(listFilesRecursively(uiSrcPath)).resolves.toEqual([
      'main.ts',
      'upstream-webui/bundles/index.js',
      'upstream-webui/bundles/peer-locations.js',
      'upstream-webui/bundles/peers.js',
      'upstream-webui/index.js',
      'upstream-webui/peer-source.js',
    ]);
  });

  it('boots the root dashboard from the upstream webui baseline entrypoint', async () => {
    const source = await fs.readFile(mainEntryPath, 'utf8');

    expect(source).toContain('renderUpstreamWebUiBaseline');
    expect(source).not.toContain('bootstrapAdminApp');
    expect(source).not.toContain('renderAppShell');
  });

  it('keeps an exact upstream bootstrap flow while swapping in the SDN bundle entry', async () => {
    const upstreamEntryPath = path.join(uiSrcPath, 'upstream-webui', 'index.js');
    const source = await fs.readFile(upstreamEntryPath, 'utf8');

    expect(source).toContain("import App from '../../../../webui/src/App.js'");
    expect(source).toContain("import getStore from './bundles/index.js'");
    expect(source).toContain("import { I18nextProvider } from 'react-i18next'");
    expect(source).toContain('ReactDOM.render(');
    expect(source).toContain('<DndProvider backend={DndBackend}>');
  });
});
