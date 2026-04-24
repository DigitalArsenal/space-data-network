import {
  cloneDirectorySnapshot,
  createDirectorySnapshot,
  normalizeDirectoryImportResult,
  normalizeDirectoryQuery,
  normalizeDirectoryRecord,
  pickDirectoryItems,
  type DirectoryAdapter,
  type DirectoryImportRequest,
  type DirectoryImportResult,
  type DirectoryNodeRecord,
  type DirectorySnapshot,
  type DirectoryUserRecord,
} from './directory';

interface ResponseLike {
  ok: boolean;
  status: number;
  json(): Promise<unknown>;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<ResponseLike>;

export interface ServerDirectoryAdapterDeps {
  baseUrl: string;
  fetch?: FetchLike;
}

export function createServerDirectoryAdapter(
  deps: ServerDirectoryAdapterDeps,
): DirectoryAdapter {
  const baseUrl = deps.baseUrl.trim().replace(/\/+$/, '');
  if (!baseUrl) {
    throw new Error('baseUrl is required');
  }

  const fetcher = deps.fetch ?? (globalThis.fetch.bind(globalThis) as FetchLike);
  let currentSnapshot = createDirectorySnapshot({ query: '', nodes: [], users: [] });

  return {
    mode: 'server',

    async search(query: string): Promise<DirectorySnapshot> {
      const normalizedQuery = normalizeDirectoryQuery(query);
      const encoded = encodeURIComponent(normalizedQuery);
      const [nodesPayload, usersPayload] = await Promise.all([
        readJson(fetcher, `${baseUrl}/api/directory/nodes?q=${encoded}`),
        readJson(fetcher, `${baseUrl}/api/directory/users?q=${encoded}`),
      ]);

      currentSnapshot = createDirectorySnapshot({
        query: normalizedQuery,
        nodes: normalizeDirectoryItems(nodesPayload, 'node'),
        users: normalizeDirectoryItems(usersPayload, 'user'),
      });
      return cloneDirectorySnapshot(currentSnapshot);
    },

    async importRecord(record: DirectoryImportRequest): Promise<DirectoryImportResult> {
      const payload = await readJson(fetcher, `${baseUrl}/api/v1/admin/directory/import`, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(record),
      });
      return normalizeDirectoryImportResult(payload);
    },
  };
}

function normalizeDirectoryItems(
  payload: unknown,
  kind: 'node',
): DirectoryNodeRecord[];
function normalizeDirectoryItems(
  payload: unknown,
  kind: 'user',
): DirectoryUserRecord[];
function normalizeDirectoryItems(
  payload: unknown,
  kind: 'node' | 'user',
): Array<DirectoryNodeRecord | DirectoryUserRecord> {
  return pickDirectoryItems(payload)
    .map((record) => kind === 'node'
      ? normalizeDirectoryRecord(record, 'node')
      : normalizeDirectoryRecord(record, 'user'));
}

async function readJson(
  fetcher: FetchLike,
  url: string,
  init?: RequestInit,
): Promise<unknown> {
  const response = await fetcher(url, {
    credentials: 'include',
    ...init,
  });
  if (!response.ok) {
    throw new Error(`request failed (${response.status}) for ${url}`);
  }
  return response.json();
}
