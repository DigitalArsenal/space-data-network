import {
  cloneAdminSnapshot,
  createAdminSnapshot,
  type AdminAdapter,
  type AdminNodeContext,
  type AdminPermissions,
  type AdminSnapshot,
  type AdminWorkspaceId,
} from './admin-adapter';

export interface LocalAdapterDeps {
  getNodeContext?: () => Promise<Partial<AdminNodeContext> | undefined>;
  getPermissions?: () => Promise<Partial<AdminPermissions> | undefined>;
  initialWorkspace?: AdminWorkspaceId;
}

export function createLocalAdapter(deps: LocalAdapterDeps = {}): AdminAdapter {
  let currentSnapshot = createAdminSnapshot({
    mode: 'local',
    workspace: {
      activeId: deps.initialWorkspace ?? 'network',
    },
  });

  return {
    mode: 'local',

    async connect(): Promise<AdminSnapshot> {
      currentSnapshot = createAdminSnapshot({
        mode: 'local',
        nodeContext: {
          ...currentSnapshot.nodeContext,
          ...(await deps.getNodeContext?.()),
        },
        permissions: {
          ...currentSnapshot.permissions,
          ...(await deps.getPermissions?.()),
        },
        workspace: currentSnapshot.workspace,
      });
      return cloneAdminSnapshot(currentSnapshot);
    },

    async snapshot(): Promise<AdminSnapshot> {
      return cloneAdminSnapshot(currentSnapshot);
    },

    async setWorkspace(workspaceId: AdminWorkspaceId): Promise<AdminSnapshot> {
      currentSnapshot = createAdminSnapshot({
        ...currentSnapshot,
        mode: 'local',
        workspace: {
          activeId: workspaceId,
          available: currentSnapshot.workspace.available,
        },
      });
      return cloneAdminSnapshot(currentSnapshot);
    },
  };
}

export const createLocalAdminAdapter = createLocalAdapter;
