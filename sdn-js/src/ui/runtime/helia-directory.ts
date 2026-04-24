import {
  cloneDirectorySnapshot,
  createDirectorySnapshot,
  matchesDirectoryRecord,
  normalizeDirectoryImportRequest,
  normalizeDirectoryQuery,
  splitDirectoryRecords,
  type DirectoryAdapter,
  type DirectoryImportRequest,
  type DirectoryImportResult,
  type DirectorySnapshot,
} from './directory';

export interface HeliaDirectoryAdapterDeps {
  listDirectoryRecords: () => Promise<Array<Record<string, unknown>>>;
}

export function createHeliaDirectoryAdapter(
  deps: HeliaDirectoryAdapterDeps,
): DirectoryAdapter {
  let currentSnapshot = createDirectorySnapshot({ query: '', nodes: [], users: [] });
  const importedRecords: Array<Record<string, unknown>> = [];

  return {
    mode: 'helia',

    async search(query: string): Promise<DirectorySnapshot> {
      const normalizedQuery = normalizeDirectoryQuery(query);
      const records = [
        ...(await deps.listDirectoryRecords()),
        ...importedRecords,
      ];
      const snapshot = splitDirectoryRecords(records);
      currentSnapshot = createDirectorySnapshot({
        query: normalizedQuery,
        nodes: snapshot.nodes.filter((record) => matchesDirectoryRecord(record, normalizedQuery)),
        users: snapshot.users.filter((record) => matchesDirectoryRecord(record, normalizedQuery)),
      });
      return cloneDirectorySnapshot(currentSnapshot);
    },

    async importRecord(record: DirectoryImportRequest): Promise<DirectoryImportResult> {
      const result = normalizeDirectoryImportRequest(record);
      importedRecords.push(
        ...result.nodes.map((node) => ({ ...node })),
        ...result.users.map((user) => ({ ...user })),
      );
      return result;
    },
  };
}

export function createLocalDirectoryAdapter(
  deps: HeliaDirectoryAdapterDeps,
): DirectoryAdapter {
  return createHeliaDirectoryAdapter(deps);
}
