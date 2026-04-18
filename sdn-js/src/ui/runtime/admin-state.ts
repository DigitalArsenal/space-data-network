import {
  cloneAdminSnapshot,
  createAdminSnapshot,
  normalizeServerTarget,
  type AdminAdapter,
  type AdminMode,
  type AdminServerTarget,
  type AdminSnapshot,
  type AdminWorkspaceId,
} from './admin-adapter';

export interface AdminStateOptions {
  localAdapter: () => AdminAdapter;
  serverAdapter: (target: AdminServerTarget) => AdminAdapter;
  initialMode?: AdminMode;
  initialServerTarget?: AdminServerTarget | null;
  initialWorkspace?: AdminWorkspaceId;
}

export interface AdminState {
  connectLocal(): Promise<AdminSnapshot>;
  connectServer(target: AdminServerTarget): Promise<AdminSnapshot>;
  setMode(mode: AdminMode, target?: AdminServerTarget): Promise<AdminSnapshot>;
  setWorkspace(workspaceId: AdminWorkspaceId): Promise<AdminSnapshot>;
  refresh(): Promise<AdminSnapshot>;
  snapshot(): AdminSnapshot;
}

export function createAdminState(options: AdminStateOptions): AdminState {
  const initialMode = options.initialMode ?? 'local';
  const initialTarget = normalizeServerTarget(options.initialServerTarget);
  const initialWorkspace = options.initialWorkspace ?? 'network';

  let localAdapter = options.localAdapter();
  let activeAdapter = initialMode === 'server' && initialTarget
    ? options.serverAdapter(initialTarget)
    : localAdapter;
  let lastServerTarget = initialTarget;
  let currentSnapshot = createAdminSnapshot({
    mode: activeAdapter.mode,
    serverTarget: lastServerTarget,
    workspace: {
      activeId: initialWorkspace,
      available: [],
    },
  });

  return {
    async connectLocal(): Promise<AdminSnapshot> {
      activeAdapter = localAdapter;
      currentSnapshot = await connectAdapter(activeAdapter);
      return cloneAdminSnapshot(currentSnapshot);
    },

    async connectServer(target: AdminServerTarget): Promise<AdminSnapshot> {
      const normalizedTarget = normalizeServerTarget(target);
      if (!normalizedTarget) {
        throw new Error('server target baseUrl is required');
      }
      lastServerTarget = normalizedTarget;
      activeAdapter = options.serverAdapter(normalizedTarget);
      currentSnapshot = await connectAdapter(activeAdapter);
      return cloneAdminSnapshot(currentSnapshot);
    },

    async setMode(mode: AdminMode, target?: AdminServerTarget): Promise<AdminSnapshot> {
      if (mode === 'local') {
        return this.connectLocal();
      }

      const resolvedTarget = normalizeServerTarget(target) ?? lastServerTarget;
      if (!resolvedTarget) {
        throw new Error('server target required');
      }
      return this.connectServer(resolvedTarget);
    },

    async setWorkspace(workspaceId: AdminWorkspaceId): Promise<AdminSnapshot> {
      currentSnapshot = cloneAdminSnapshot(await activeAdapter.setWorkspace(workspaceId));
      return cloneAdminSnapshot(currentSnapshot);
    },

    async refresh(): Promise<AdminSnapshot> {
      currentSnapshot = await connectAdapter(activeAdapter);
      return cloneAdminSnapshot(currentSnapshot);
    },

    snapshot(): AdminSnapshot {
      return cloneAdminSnapshot(currentSnapshot);
    },
  };

  async function connectAdapter(adapter: AdminAdapter): Promise<AdminSnapshot> {
    const snapshot = await adapter.connect();
    currentSnapshot = cloneAdminSnapshot(snapshot);
    if (snapshot.mode === 'local') {
      localAdapter = adapter;
    } else if (snapshot.serverTarget) {
      lastServerTarget = normalizeServerTarget(snapshot.serverTarget);
    }
    return currentSnapshot;
  }
}
