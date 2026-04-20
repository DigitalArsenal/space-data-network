export type AdminMode = 'local' | 'server';

export type AdminRole =
  | 'guest'
  | 'local'
  | 'limited'
  | 'standard'
  | 'trusted'
  | 'admin';

export type AdminWorkspaceId =
  | 'network'
  | 'directory'
  | 'store'
  | 'frontend'
  | 'wallet'
  | (string & {});

export interface AdminServerTarget {
  baseUrl: string;
  label?: string;
}

export interface AdminNodeContext {
  displayName: string;
  peerId: string | null;
  xpub: string | null;
  transport: string;
  descriptorUrl: string | null;
}

export interface AdminPermissions {
  authenticated: boolean;
  role: AdminRole;
  canManageUsers: boolean;
  canManageFrontend: boolean;
  canManageStore: boolean;
  canOpenWallet: boolean;
}

export interface AdminWorkspace {
  activeId: AdminWorkspaceId;
  available: AdminWorkspaceId[];
}

export interface AdminSnapshot {
  mode: AdminMode;
  serverTarget: AdminServerTarget | null;
  nodeContext: AdminNodeContext;
  permissions: AdminPermissions;
  workspace: AdminWorkspace;
}

export interface AdminAdapter {
  readonly mode: AdminMode;
  connect(): Promise<AdminSnapshot>;
  snapshot(): Promise<AdminSnapshot>;
  setWorkspace(workspaceId: AdminWorkspaceId): Promise<AdminSnapshot>;
}

export const DEFAULT_ADMIN_WORKSPACES: AdminWorkspaceId[] = [
  'network',
  'directory',
  'store',
  'frontend',
  'wallet',
];

export function cloneAdminSnapshot(snapshot: AdminSnapshot): AdminSnapshot {
  return {
    mode: snapshot.mode,
    serverTarget: snapshot.serverTarget ? { ...snapshot.serverTarget } : null,
    nodeContext: { ...snapshot.nodeContext },
    permissions: { ...snapshot.permissions },
    workspace: {
      activeId: snapshot.workspace.activeId,
      available: [...snapshot.workspace.available],
    },
  };
}

export function normalizeServerTarget(
  target: AdminServerTarget | null | undefined,
): AdminServerTarget | null {
  if (!target) {
    return null;
  }
  const baseUrl = target.baseUrl.trim().replace(/\/+$/, '');
  if (!baseUrl) {
    return null;
  }
  return {
    baseUrl,
    ...(target.label?.trim() ? { label: target.label.trim() } : {}),
  };
}

export function createAdminSnapshot(
  input: Partial<AdminSnapshot> & { mode: AdminMode },
): AdminSnapshot {
  return {
    mode: input.mode,
    serverTarget: normalizeServerTarget(input.serverTarget),
    nodeContext: {
      displayName: input.nodeContext?.displayName ?? defaultDisplayName(input.mode),
      peerId: input.nodeContext?.peerId ?? null,
      xpub: input.nodeContext?.xpub ?? null,
      transport: input.nodeContext?.transport ?? defaultTransport(input.mode),
      descriptorUrl: input.nodeContext?.descriptorUrl ?? null,
    },
    permissions: {
      authenticated: input.permissions?.authenticated ?? false,
      role: input.permissions?.role ?? (input.mode === 'local' ? 'local' : 'guest'),
      canManageUsers: input.permissions?.canManageUsers ?? (input.mode === 'local'),
      canManageFrontend: input.permissions?.canManageFrontend ?? (input.mode === 'local'),
      canManageStore: input.permissions?.canManageStore ?? (input.mode === 'local'),
      canOpenWallet: input.permissions?.canOpenWallet ?? true,
    },
    workspace: {
      activeId: input.workspace?.activeId ?? 'network',
      available: [
        ...(input.workspace?.available?.length
          ? input.workspace.available
          : DEFAULT_ADMIN_WORKSPACES),
      ],
    },
  };
}

function defaultDisplayName(mode: AdminMode): string {
  return mode === 'local' ? 'Local backend' : 'Server backend';
}

function defaultTransport(mode: AdminMode): string {
  return mode === 'local' ? 'helia' : 'https';
}
