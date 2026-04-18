import type { AdminMode } from './admin-adapter';

export interface FrontendFileEntry {
  path: string;
  isDir: boolean;
  size: number;
  modTime: string;
}

export interface FrontendFileDocument {
  path: string;
  content: string;
  size: number;
}

export interface FrontendUploadFile {
  name: string;
  text: string;
}

export interface FrontendWorkspaceTransport {
  listFiles(): Promise<FrontendFileEntry[]>;
  readFile(path: string): Promise<FrontendFileDocument>;
  writeFile(path: string, content: string): Promise<void>;
  movePath(fromPath: string, toPath: string): Promise<void>;
  deletePath(path: string): Promise<void>;
  uploadFiles(files: FrontendUploadFile[], directory?: string): Promise<void>;
}

export interface FrontendTreeNode {
  name: string;
  path: string;
  isDir: boolean;
  children?: FrontendTreeNode[];
}

export interface FrontendWorkspaceSnapshot {
  mode: AdminMode;
  files: FrontendFileEntry[];
  tree: FrontendTreeNode[];
  selectedPath: string | null;
  status: string;
  editor: {
    value: string;
    dirty: boolean;
    language: string;
  };
}

export interface FrontendWorkspace {
  connect(): Promise<FrontendWorkspaceSnapshot>;
  selectPath(path: string): Promise<FrontendWorkspaceSnapshot>;
  editContent(content: string): FrontendWorkspaceSnapshot;
  save(): Promise<FrontendWorkspaceSnapshot>;
  moveSelection(nextPath: string): Promise<FrontendWorkspaceSnapshot>;
  deleteSelection(): Promise<FrontendWorkspaceSnapshot>;
  upload(files: FrontendUploadFile[], directory?: string): Promise<FrontendWorkspaceSnapshot>;
  refresh(): Promise<FrontendWorkspaceSnapshot>;
  snapshot(): FrontendWorkspaceSnapshot;
}

export interface FrontendWorkspaceOptions {
  mode: AdminMode;
  transport: FrontendWorkspaceTransport;
}

interface ResponseLike {
  ok: boolean;
  status: number;
  json(): Promise<unknown>;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<ResponseLike>;

export interface ServerFrontendTransportOptions {
  baseUrl: string;
  fetch?: FetchLike;
}

export function createFrontendWorkspace(
  options: FrontendWorkspaceOptions,
): FrontendWorkspace {
  let snapshot = createEmptySnapshot(options.mode);

  return {
    async connect(): Promise<FrontendWorkspaceSnapshot> {
      snapshot = await refreshSnapshot(snapshot.selectedPath);
      return cloneSnapshot(snapshot);
    },

    async selectPath(path: string): Promise<FrontendWorkspaceSnapshot> {
      snapshot = await loadSelection(path, snapshot.files);
      return cloneSnapshot(snapshot);
    },

    editContent(content: string): FrontendWorkspaceSnapshot {
      snapshot = {
        ...snapshot,
        editor: {
          ...snapshot.editor,
          value: content,
          dirty: snapshot.selectedPath !== null,
        },
        status: snapshot.selectedPath ? `Unsaved changes in ${snapshot.selectedPath}` : snapshot.status,
      };
      return cloneSnapshot(snapshot);
    },

    async save(): Promise<FrontendWorkspaceSnapshot> {
      if (!snapshot.selectedPath) {
        return cloneSnapshot(snapshot);
      }
      await options.transport.writeFile(snapshot.selectedPath, snapshot.editor.value);
      snapshot = await refreshSnapshot(snapshot.selectedPath);
      snapshot = {
        ...snapshot,
        status: `Saved ${snapshot.selectedPath}`,
      };
      return cloneSnapshot(snapshot);
    },

    async moveSelection(nextPath: string): Promise<FrontendWorkspaceSnapshot> {
      if (!snapshot.selectedPath) {
        return cloneSnapshot(snapshot);
      }
      const targetPath = nextPath.trim();
      if (!targetPath) {
        throw new Error('target path is required');
      }
      await options.transport.movePath(snapshot.selectedPath, targetPath);
      snapshot = await refreshSnapshot(targetPath);
      snapshot = {
        ...snapshot,
        status: `Moved to ${targetPath}`,
      };
      return cloneSnapshot(snapshot);
    },

    async deleteSelection(): Promise<FrontendWorkspaceSnapshot> {
      if (!snapshot.selectedPath) {
        return cloneSnapshot(snapshot);
      }
      const removedPath = snapshot.selectedPath;
      await options.transport.deletePath(removedPath);
      snapshot = await refreshSnapshot(null);
      snapshot = {
        ...snapshot,
        status: `Deleted ${removedPath}`,
      };
      return cloneSnapshot(snapshot);
    },

    async upload(files: FrontendUploadFile[], directory?: string): Promise<FrontendWorkspaceSnapshot> {
      await options.transport.uploadFiles(files, directory);
      const preferredPath = files[0]
        ? [directory?.trim(), files[0].name].filter(Boolean).join('/')
        : snapshot.selectedPath;
      snapshot = await refreshSnapshot(preferredPath);
      snapshot = {
        ...snapshot,
        status: `Uploaded ${files.length} file${files.length === 1 ? '' : 's'}`,
      };
      return cloneSnapshot(snapshot);
    },

    async refresh(): Promise<FrontendWorkspaceSnapshot> {
      snapshot = await refreshSnapshot(snapshot.selectedPath);
      return cloneSnapshot(snapshot);
    },

    snapshot(): FrontendWorkspaceSnapshot {
      return cloneSnapshot(snapshot);
    },
  };

  async function refreshSnapshot(preferredPath: string | null): Promise<FrontendWorkspaceSnapshot> {
    const files = await listFilesSorted(options.transport);
    const nextPath = resolvePreferredPath(files, preferredPath);
    if (!nextPath) {
      return {
        mode: options.mode,
        files,
        tree: buildFrontendTree(files),
        selectedPath: null,
        status: files.length === 0 ? 'No frontend files available' : 'Directory contains folders only',
        editor: {
          value: '',
          dirty: false,
          language: 'plaintext',
        },
      };
    }
    return loadSelection(nextPath, files);
  }

  async function loadSelection(
    path: string,
    files: FrontendFileEntry[],
  ): Promise<FrontendWorkspaceSnapshot> {
    const document = await options.transport.readFile(path);
    return {
      mode: options.mode,
      files,
      tree: buildFrontendTree(files),
      selectedPath: document.path,
      status: `Loaded ${document.path}`,
      editor: {
        value: document.content,
        dirty: false,
        language: detectLanguage(document.path),
      },
    };
  }
}

export function createServerFrontendTransport(
  options: ServerFrontendTransportOptions,
): FrontendWorkspaceTransport {
  const baseUrl = options.baseUrl.trim().replace(/\/+$/, '');
  if (!baseUrl) {
    throw new Error('server baseUrl is required');
  }
  const fetcher = options.fetch ?? (globalThis.fetch.bind(globalThis) as FetchLike);

  return {
    async listFiles(): Promise<FrontendFileEntry[]> {
      const response = await fetcher(`${baseUrl}/api/admin/frontend/files`, {
        credentials: 'include',
      });
      const payload = await readJson(response, 'list frontend files');
      if (!Array.isArray(payload)) {
        return [];
      }
      return payload
        .filter(isRecord)
        .map((entry) => ({
          path: readString(entry, ['path']) ?? '',
          isDir: readBoolean(entry, ['isDir', 'is_dir']) ?? false,
          size: readNumber(entry, ['size']) ?? 0,
          modTime: readString(entry, ['modTime', 'mod_time']) ?? '',
        }))
        .filter((entry) => Boolean(entry.path));
    },

    async readFile(path: string): Promise<FrontendFileDocument> {
      const response = await fetcher(`${baseUrl}/api/admin/frontend/files/${encodePath(path)}`, {
        credentials: 'include',
      });
      const payload = await readJson(response, `read frontend file ${path}`);
      if (!isRecord(payload)) {
        throw new Error(`invalid file payload for ${path}`);
      }
      return {
        path: readString(payload, ['path']) ?? path,
        content: readString(payload, ['content']) ?? '',
        size: readNumber(payload, ['size']) ?? 0,
      };
    },

    async writeFile(path: string, content: string): Promise<void> {
      const response = await fetcher(`${baseUrl}/api/admin/frontend/files/${encodePath(path)}`, {
        method: 'PUT',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ content }),
      });
      await readJson(response, `write frontend file ${path}`);
    },

    async movePath(fromPath: string, toPath: string): Promise<void> {
      const response = await fetcher(`${baseUrl}/api/admin/frontend/move`, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ from: fromPath, to: toPath }),
      });
      await readJson(response, `move frontend file ${fromPath}`);
    },

    async deletePath(path: string): Promise<void> {
      const response = await fetcher(`${baseUrl}/api/admin/frontend/files/${encodePath(path)}`, {
        method: 'DELETE',
        credentials: 'include',
      });
      await readJson(response, `delete frontend file ${path}`);
    },

    async uploadFiles(files: FrontendUploadFile[], directory = ''): Promise<void> {
      const form = new FormData();
      if (directory.trim()) {
        form.set('path', directory.trim());
      }
      for (const file of files) {
        form.append('files', new Blob([file.text], { type: 'text/plain' }), file.name);
      }
      const response = await fetcher(`${baseUrl}/api/admin/frontend/upload`, {
        method: 'POST',
        credentials: 'include',
        body: form,
      });
      await readJson(response, 'upload frontend files');
    },
  };
}

export function createLocalFrontendTransport(
  files: Record<string, string> = {},
): FrontendWorkspaceTransport {
  const contents = new Map(Object.entries(files));
  return {
    async listFiles(): Promise<FrontendFileEntry[]> {
      const directories = new Set<string>();
      for (const path of contents.keys()) {
        const segments = path.split('/');
        for (let index = 1; index < segments.length; index += 1) {
          directories.add(segments.slice(0, index).join('/'));
        }
      }
      return [
        ...[...directories].map((path) => ({
          path,
          isDir: true,
          size: 0,
          modTime: '',
        })),
        ...[...contents.entries()].map(([path, content]) => ({
          path,
          isDir: false,
          size: content.length,
          modTime: '',
        })),
      ];
    },

    async readFile(path: string): Promise<FrontendFileDocument> {
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

    async writeFile(path: string, content: string): Promise<void> {
      contents.set(path, content);
    },

    async movePath(fromPath: string, toPath: string): Promise<void> {
      const content = contents.get(fromPath);
      if (typeof content !== 'string') {
        throw new Error(`missing file ${fromPath}`);
      }
      contents.delete(fromPath);
      contents.set(toPath, content);
    },

    async deletePath(path: string): Promise<void> {
      contents.delete(path);
    },

    async uploadFiles(filesToUpload: FrontendUploadFile[], directory = ''): Promise<void> {
      for (const file of filesToUpload) {
        const path = directory ? `${directory}/${file.name}` : file.name;
        contents.set(path, file.text);
      }
    },
  };
}

export function buildFrontendTree(files: FrontendFileEntry[]): FrontendTreeNode[] {
  const roots: FrontendTreeNode[] = [];
  const directories = new Map<string, FrontendTreeNode>();

  for (const entry of files) {
    const segments = entry.path.split('/').filter(Boolean);
    if (segments.length === 0) {
      continue;
    }

    let currentChildren = roots;
    let currentPath = '';
    for (let index = 0; index < segments.length; index += 1) {
      const segment = segments[index] as string;
      currentPath = currentPath ? `${currentPath}/${segment}` : segment;
      const isLeaf = index === segments.length - 1;
      const isDir = isLeaf ? entry.isDir : true;
      let node = currentChildren.find((candidate) => candidate.path === currentPath);
      if (!node) {
        node = {
          name: segment,
          path: currentPath,
          isDir,
          ...(isDir ? { children: [] } : {}),
        };
        currentChildren.push(node);
      }
      if (isDir) {
        if (!node.children) {
          node.children = [];
        }
        directories.set(currentPath, node);
        currentChildren = node.children;
      }
    }
  }

  sortTree(roots);
  return roots;
}

function sortTree(nodes: FrontendTreeNode[]): void {
  nodes.sort((left, right) => {
    if (left.isDir !== right.isDir) {
      return left.isDir ? -1 : 1;
    }
    return left.path.localeCompare(right.path);
  });
  for (const node of nodes) {
    if (node.children) {
      sortTree(node.children);
    }
  }
}

function createEmptySnapshot(mode: AdminMode): FrontendWorkspaceSnapshot {
  return {
    mode,
    files: [],
    tree: [],
    selectedPath: null,
    status: 'Idle',
    editor: {
      value: '',
      dirty: false,
      language: 'plaintext',
    },
  };
}

async function listFilesSorted(
  transport: FrontendWorkspaceTransport,
): Promise<FrontendFileEntry[]> {
  const files = await transport.listFiles();
  return [...files].sort((left, right) => {
    if (left.isDir !== right.isDir) {
      return left.isDir ? -1 : 1;
    }
    return left.path.localeCompare(right.path);
  });
}

function resolvePreferredPath(
  files: FrontendFileEntry[],
  preferredPath: string | null,
): string | null {
  if (preferredPath && files.some((entry) => !entry.isDir && entry.path === preferredPath)) {
    return preferredPath;
  }
  return files.find((entry) => !entry.isDir)?.path ?? null;
}

function detectLanguage(path: string): string {
  const normalized = path.toLowerCase();
  if (normalized.endsWith('.ts') || normalized.endsWith('.tsx')) {
    return 'typescript';
  }
  if (normalized.endsWith('.js') || normalized.endsWith('.mjs') || normalized.endsWith('.cjs')) {
    return 'javascript';
  }
  if (normalized.endsWith('.json')) {
    return 'json';
  }
  if (normalized.endsWith('.css')) {
    return 'css';
  }
  if (normalized.endsWith('.html')) {
    return 'html';
  }
  if (normalized.endsWith('.md')) {
    return 'markdown';
  }
  return 'plaintext';
}

function cloneSnapshot(snapshot: FrontendWorkspaceSnapshot): FrontendWorkspaceSnapshot {
  return {
    mode: snapshot.mode,
    files: snapshot.files.map((entry) => ({ ...entry })),
    tree: cloneTree(snapshot.tree),
    selectedPath: snapshot.selectedPath,
    status: snapshot.status,
    editor: { ...snapshot.editor },
  };
}

function cloneTree(nodes: FrontendTreeNode[]): FrontendTreeNode[] {
  return nodes.map((node) => ({
    name: node.name,
    path: node.path,
    isDir: node.isDir,
    ...(node.children ? { children: cloneTree(node.children) } : {}),
  }));
}

async function readJson(response: ResponseLike, action: string): Promise<unknown> {
  if (!response.ok) {
    throw new Error(`${action} failed (${response.status})`);
  }
  return response.json();
}

function encodePath(path: string): string {
  return path.split('/').map((segment) => encodeURIComponent(segment)).join('/');
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}

function readString(record: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string') {
      return value;
    }
  }
  return null;
}

function readBoolean(record: Record<string, unknown>, keys: string[]): boolean | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'boolean') {
      return value;
    }
  }
  return null;
}

function readNumber(record: Record<string, unknown>, keys: string[]): number | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'number') {
      return value;
    }
  }
  return null;
}
