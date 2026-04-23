import {
  cloneDirectorySnapshot,
  createDirectorySnapshot,
  matchesDirectoryRecord,
  normalizeDirectoryQuery,
  normalizeDirectoryRecord,
  splitDirectoryRecords,
  type DirectoryAdapter,
  type DirectorySnapshot,
} from './directory';

export interface HeliaDirectoryAdapterDeps {
  listDirectoryRecords: () => Promise<Array<Record<string, unknown>>>;
}

export function createHeliaDirectoryAdapter(
  deps: HeliaDirectoryAdapterDeps,
): DirectoryAdapter {
  let currentSnapshot = createDirectorySnapshot({ query: '', nodes: [], users: [] });

  return {
    mode: 'helia',

    async search(query: string): Promise<DirectorySnapshot> {
      const normalizedQuery = normalizeDirectoryQuery(query);
      const records = await deps.listDirectoryRecords();
      const snapshot = splitDirectoryRecords(records);
      currentSnapshot = createDirectorySnapshot({
        query: normalizedQuery,
        nodes: snapshot.nodes.filter((record) => matchesDirectoryRecord(record, normalizedQuery)),
        users: snapshot.users.filter((record) => matchesDirectoryRecord(record, normalizedQuery)),
      });
      return cloneDirectorySnapshot(currentSnapshot);
    },
  };
}

export function createLocalDirectoryAdapter(
  deps: HeliaDirectoryAdapterDeps,
): DirectoryAdapter {
  return createHeliaDirectoryAdapter(deps);
}
