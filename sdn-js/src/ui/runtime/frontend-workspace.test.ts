import { describe, expect, it, vi } from 'vitest';

import {
  buildFrontendTree,
  createLocalFrontendTransport,
  createServerFrontendTransport,
  createFrontendWorkspace,
  type FrontendWorkspaceTransport,
} from './frontend-workspace';

describe('buildFrontendTree', () => {
  it('groups nested file entries into a stable directory tree', () => {
    const tree = buildFrontendTree([
      { path: 'src', isDir: true, size: 0, modTime: '2026-04-18T00:00:00Z' },
      { path: 'src/main.ts', isDir: false, size: 42, modTime: '2026-04-18T00:00:00Z' },
      { path: 'index.html', isDir: false, size: 12, modTime: '2026-04-18T00:00:00Z' },
    ]);

    expect(tree.map((node) => node.path)).toEqual(['src', 'index.html']);
    expect(tree[0]?.children?.map((node) => node.path)).toEqual(['src/main.ts']);
  });
});

describe('createFrontendWorkspace', () => {
  it('loads files into a tree and selects the first editable file', async () => {
    const workspace = createFrontendWorkspace({
      mode: 'server',
      transport: createFakeTransport({
        'index.html': '<h1>Hello</h1>',
        'src/main.ts': 'console.log("ok")',
      }),
    });

    await workspace.connect();

    const snapshot = workspace.snapshot();
    expect(snapshot.files.map((entry) => entry.path)).toEqual(['src', 'index.html', 'src/main.ts']);
    expect(snapshot.selectedPath).toBe('index.html');
    expect(snapshot.editor.value).toBe('<h1>Hello</h1>');
    expect(snapshot.editor.language).toBe('html');
  });

  it('tracks dirty edits and persists saves through the transport', async () => {
    const transport = createFakeTransport({
      'src/main.ts': 'console.log("before")',
    });
    const workspace = createFrontendWorkspace({
      mode: 'server',
      transport,
    });

    await workspace.connect();
    await workspace.selectPath('src/main.ts');
    workspace.editContent('console.log("after")');

    expect(workspace.snapshot().editor.dirty).toBe(true);

    await workspace.save();

    expect(transport.contents.get('src/main.ts')).toBe('console.log("after")');
    expect(workspace.snapshot().editor.dirty).toBe(false);
    expect(workspace.snapshot().status).toContain('Saved');
  });

  it('moves the selected file and keeps the editor focused on the renamed path', async () => {
    const transport = createFakeTransport({
      'src/main.ts': 'console.log("ok")',
    });
    const workspace = createFrontendWorkspace({
      mode: 'server',
      transport,
    });

    await workspace.connect();
    await workspace.selectPath('src/main.ts');
    await workspace.moveSelection('src/app.ts');

    const snapshot = workspace.snapshot();
    expect(snapshot.selectedPath).toBe('src/app.ts');
    expect(snapshot.files.some((entry) => entry.path === 'src/app.ts')).toBe(true);
    expect(transport.contents.has('src/app.ts')).toBe(true);
    expect(transport.contents.has('src/main.ts')).toBe(false);
  });
});

describe('createServerFrontendTransport', () => {
  it('targets the authenticated admin frontend endpoints', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      if (input.endsWith('/files') && init?.method !== 'POST') {
        return {
          ok: true,
          status: 200,
          json: async () => [{ path: 'index.html', is_dir: false, size: 12, mod_time: '2026-04-18T00:00:00Z' }],
        };
      }
      return {
        ok: true,
        status: 200,
        json: async () => ({ path: 'index.html', content: '<h1>Hello</h1>', size: 12 }),
      };
    });
    const transport = createServerFrontendTransport({
      baseUrl: 'https://node.example',
      fetch,
    });

    await transport.listFiles();
    await transport.readFile('index.html');

    expect(fetch).toHaveBeenNthCalledWith(
      1,
      'https://node.example/api/admin/frontend/files',
      expect.objectContaining({ credentials: 'include' }),
    );
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      'https://node.example/api/admin/frontend/files/index.html',
      expect.objectContaining({ credentials: 'include' }),
    );
  });
});

describe('createLocalFrontendTransport', () => {
  it('persists file mutations in memory for local mode', async () => {
    const transport = createLocalFrontendTransport({
      'index.html': '<h1>Hello</h1>',
    });

    await transport.writeFile('src/main.ts', 'console.log("ok")');
    await transport.movePath('src/main.ts', 'src/app.ts');
    await transport.uploadFiles([{ name: 'styles.css', text: 'body {}' }], 'assets');

    const files = await transport.listFiles();

    expect(files.some((entry) => entry.path === 'src/app.ts')).toBe(true);
    expect(files.some((entry) => entry.path === 'assets/styles.css')).toBe(true);
    await expect(transport.readFile('src/app.ts')).resolves.toMatchObject({ content: 'console.log("ok")' });
  });
});

function createFakeTransport(
  files: Record<string, string>,
): FrontendWorkspaceTransport & { contents: Map<string, string> } {
  const contents = new Map(Object.entries(files));

  return {
    contents,
    async listFiles() {
      const directories = new Set<string>();
      for (const path of contents.keys()) {
        const segments = path.split('/');
        for (let index = 1; index < segments.length; index += 1) {
          directories.add(segments.slice(0, index).join('/'));
        }
      }
      return [
        ...[...directories].sort().map((path) => ({
          path,
          isDir: true,
          size: 0,
          modTime: '2026-04-18T00:00:00Z',
        })),
        ...[...contents.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([path, content]) => ({
          path,
          isDir: false,
          size: content.length,
          modTime: '2026-04-18T00:00:00Z',
        })),
      ];
    },
    async readFile(path) {
      const content = contents.get(path);
      if (typeof content !== 'string') {
        throw new Error(`missing file ${path}`);
      }
      return {
        path,
        content,
        size: content.length,
      };
    },
    async writeFile(path, content) {
      contents.set(path, content);
    },
    async movePath(fromPath, toPath) {
      const content = contents.get(fromPath);
      if (typeof content !== 'string') {
        throw new Error(`missing file ${fromPath}`);
      }
      contents.delete(fromPath);
      contents.set(toPath, content);
    },
    async deletePath(path) {
      contents.delete(path);
    },
    async uploadFiles(filesToUpload, directory = '') {
      for (const file of filesToUpload) {
        const path = directory ? `${directory}/${file.name}` : file.name;
        contents.set(path, file.text);
      }
    },
  };
}
