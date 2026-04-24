import {
  cloneAdminSnapshot,
  createAdminSnapshot,
  normalizeServerTarget,
  type AdminAdapter,
  type AdminPermissions,
  type AdminServerTarget,
  type AdminSnapshot,
  type AdminWorkspaceId,
} from './admin-adapter';
import { createLocalAdapter } from './local-adapter';
import type { DirectoryAdapter } from './directory';
import { createHeliaDirectoryAdapter } from './helia-directory';
import { createServerDirectoryAdapter } from './server-directory';
import { readHostedServerBaseUrl, type HostedRuntimeConfigWindow } from './runtime-config';

interface ResponseLike {
  ok: boolean;
  status: number;
  json(): Promise<unknown>;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<ResponseLike>;

export interface ServerAdapterDeps {
  target: AdminServerTarget;
  fetch?: FetchLike;
  initialWorkspace?: AdminWorkspaceId;
}

export interface ServerRuntimeAdapter extends AdminAdapter {
  directory: DirectoryAdapter;
}

export interface UiRuntimeAdapterDeps {
  config?: HostedRuntimeConfigWindow | null;
  fetch?: FetchLike;
  initialWorkspace?: AdminWorkspaceId;
  listDirectoryRecords?: () => Promise<Array<Record<string, unknown>>>;
}

export interface UiRuntimeAdapter extends AdminAdapter {
  directory: DirectoryAdapter;
}

export interface HostedDirectoryWindow {
  __SDN_DIRECTORY__?: {
    records?: Array<Record<string, unknown>>;
    listDirectoryRecords?: () =>
      | Array<Record<string, unknown>>
      | Promise<Array<Record<string, unknown>>>;
  };
}

export interface SharedUiRuntimeAdapterDeps {
  source?: (HostedRuntimeConfigWindow & HostedDirectoryWindow) | null;
  fetch?: FetchLike;
  initialWorkspace?: AdminWorkspaceId;
}

interface AuthStatusResponse {
  wallet_ui_configured?: boolean;
}

interface AuthMeResponse {
  name?: string;
  trust_level?: string;
}

let sharedUiRuntimeAdapter: UiRuntimeAdapter | null = null;

export function createServerAdapter(deps: ServerAdapterDeps): ServerRuntimeAdapter {
  const target = normalizeServerTarget(deps.target);
  if (!target) {
    throw new Error('server target baseUrl is required');
  }

  const fetcher = deps.fetch ?? (globalThis.fetch.bind(globalThis) as FetchLike);
  const directory = createServerDirectoryAdapter({
    baseUrl: target.baseUrl,
    fetch: fetcher,
  });
  let currentSnapshot = createAdminSnapshot({
    mode: 'server',
    serverTarget: target,
    nodeContext: {
      displayName: target.label ?? target.baseUrl,
      peerId: null,
      xpub: null,
      transport: 'https',
      descriptorUrl: `${target.baseUrl}/api/module-delivery/provider`,
    },
    workspace: {
      activeId: deps.initialWorkspace ?? 'network',
      available: [],
    },
  });

  return {
    mode: 'server',
    directory,

    async connect(): Promise<AdminSnapshot> {
      const [nodeInfo, authStatus, authMe] = await Promise.all([
        readJson(fetcher, `${target.baseUrl}/api/node/info`),
        readJson(fetcher, `${target.baseUrl}/api/auth/status`),
        readJson(fetcher, `${target.baseUrl}/api/auth/me`, { allowUnauthorized: true }),
      ]);

      currentSnapshot = createAdminSnapshot({
        mode: 'server',
        serverTarget: target,
        nodeContext: {
          displayName: pickString(nodeInfo, [
            'DISPLAY_NAME',
            'display_name',
            'dn',
            'name',
          ]) ?? target.label ?? target.baseUrl,
          peerId: pickString(nodeInfo, ['peer_id', 'peerId']),
          xpub: null,
          transport: 'https',
          descriptorUrl: `${target.baseUrl}/api/module-delivery/provider`,
        },
        permissions: buildPermissions(authStatus as AuthStatusResponse, authMe as AuthMeResponse | null),
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
        mode: 'server',
        serverTarget: target,
        workspace: {
          activeId: workspaceId,
          available: currentSnapshot.workspace.available,
        },
      });
      return cloneAdminSnapshot(currentSnapshot);
    },
  };
}

export const createServerAdminAdapter = createServerAdapter;

export function createUiRuntimeAdapter(deps: UiRuntimeAdapterDeps = {}): UiRuntimeAdapter {
  const serverBaseUrl = readHostedServerBaseUrl(deps.config);
  if (serverBaseUrl) {
    const serverAdapter = createServerAdapter({
      target: { baseUrl: serverBaseUrl },
      fetch: deps.fetch,
      initialWorkspace: deps.initialWorkspace,
    });
    return serverAdapter;
  }

  const localAdapter = createLocalAdapter({
    initialWorkspace: deps.initialWorkspace,
  });
  const directory = createHeliaDirectoryAdapter({
    listDirectoryRecords: deps.listDirectoryRecords ?? (async () => []),
  });

  return {
    mode: localAdapter.mode,
      directory,
      connect: () => localAdapter.connect(),
      snapshot: () => localAdapter.snapshot(),
      setWorkspace: (workspaceId: AdminWorkspaceId) => localAdapter.setWorkspace(workspaceId),
  };
}

export function getSharedUiRuntimeAdapter(
  deps: SharedUiRuntimeAdapterDeps = {},
): UiRuntimeAdapter {
  if (!sharedUiRuntimeAdapter) {
    const source = deps.source ?? readHostedUiRuntimeWindow();
    sharedUiRuntimeAdapter = createUiRuntimeAdapter({
      config: source,
      fetch: deps.fetch,
      initialWorkspace: deps.initialWorkspace,
      listDirectoryRecords: createHostedDirectoryRecordLister(source),
    });
  }
  return sharedUiRuntimeAdapter;
}

export function resetSharedUiRuntimeAdapterForTests(): void {
  sharedUiRuntimeAdapter = null;
}

async function readJson(
  fetcher: FetchLike,
  url: string,
  options: { allowUnauthorized?: boolean } = {},
): Promise<Record<string, unknown> | null> {
  const response = await fetcher(url, {
    credentials: 'include',
  });
  if (options.allowUnauthorized && response.status === 401) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`request failed (${response.status}) for ${url}`);
  }
  const payload = await response.json();
  return isRecord(payload) ? payload : {};
}

function buildPermissions(
  authStatus: AuthStatusResponse | null,
  authMe: AuthMeResponse | null,
): AdminPermissions {
  const role = normalizeRole(authMe?.trust_level);
  const authenticated = Boolean(authMe && role !== 'guest');
  return {
    authenticated,
    role,
    canManageUsers: role === 'admin',
    canManageFrontend: role === 'admin',
    canManageStore: role === 'admin' || role === 'trusted',
    canOpenWallet: Boolean(authStatus?.wallet_ui_configured ?? true),
  };
}

function normalizeRole(value?: string): AdminPermissions['role'] {
  switch ((value ?? '').trim().toLowerCase()) {
    case 'admin':
      return 'admin';
    case 'trusted':
      return 'trusted';
    case 'standard':
      return 'standard';
    case 'limited':
      return 'limited';
    default:
      return 'guest';
  }
}

function pickString(
  payload: Record<string, unknown> | null,
  keys: string[],
): string | null {
  if (!payload) {
    return null;
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'string' && value.trim()) {
      return value.trim();
    }
  }
  return null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}

function readHostedUiRuntimeWindow(): (HostedRuntimeConfigWindow & HostedDirectoryWindow) | null {
  return typeof globalThis === 'object'
    ? (globalThis as HostedRuntimeConfigWindow & HostedDirectoryWindow)
    : null;
}

function createHostedDirectoryRecordLister(
  source: (HostedRuntimeConfigWindow & HostedDirectoryWindow) | null | undefined,
): () => Promise<Array<Record<string, unknown>>> {
  return async () => {
    const directorySource = source?.__SDN_DIRECTORY__;
    if (!directorySource) {
      return [];
    }
    if (Array.isArray(directorySource.records)) {
      return directorySource.records;
    }
    if (typeof directorySource.listDirectoryRecords === 'function') {
      const records = await directorySource.listDirectoryRecords();
      return Array.isArray(records) ? records : [];
    }
    return [];
  };
}
