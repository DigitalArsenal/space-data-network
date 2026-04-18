import { describe, expect, it } from 'vitest';

import {
  createAdminState,
  type AdminState,
} from './admin-state';
import type {
  AdminAdapter,
  AdminMode,
  AdminServerTarget,
  AdminSnapshot,
  AdminWorkspaceId,
} from './admin-adapter';

describe('createAdminState', () => {
  it('switches from local mode to server mode and replaces the effective node context', async () => {
    const localAdapter = new FakeAdminAdapter({
      mode: 'local',
      nodeContext: {
        displayName: 'Local Helia node',
        peerId: '12D3KooWLocal',
        transport: 'helia',
      },
      permissions: {
        authenticated: true,
        role: 'local',
        canManageUsers: true,
        canManageFrontend: true,
        canManageStore: true,
        canOpenWallet: true,
      },
    });

    const state = createAdminState({
      localAdapter: () => localAdapter,
      serverAdapter: (target) => new FakeAdminAdapter({
        mode: 'server',
        serverTarget: target,
        nodeContext: {
          displayName: 'Remote server',
          peerId: '12D3KooWRemote',
          transport: 'https',
        },
        permissions: {
          authenticated: true,
          role: 'admin',
          canManageUsers: true,
          canManageFrontend: true,
          canManageStore: true,
          canOpenWallet: true,
        },
      }),
    });

    await state.connectLocal();
    expect(state.snapshot().mode).toBe('local');
    expect(state.snapshot().nodeContext.peerId).toBe('12D3KooWLocal');

    await state.connectServer({ baseUrl: 'https://node.example', label: 'Remote Node' });

    expect(state.snapshot().mode).toBe('server');
    expect(state.snapshot().serverTarget?.baseUrl).toBe('https://node.example');
    expect(state.snapshot().nodeContext.peerId).toBe('12D3KooWRemote');
    expect(state.snapshot().permissions.role).toBe('admin');
  });

  it('routes workspace changes through the active adapter and keeps older snapshots immutable', async () => {
    const localAdapter = new FakeAdminAdapter({
      mode: 'local',
      workspace: {
        activeId: 'network',
        available: ['network', 'directory', 'store', 'frontend', 'wallet'],
      },
    });
    const state = createStateWithLocal(localAdapter);

    await state.connectLocal();
    const firstSnapshot = state.snapshot();

    await state.setWorkspace('wallet');
    const secondSnapshot = state.snapshot();

    expect(firstSnapshot.workspace.activeId).toBe('network');
    expect(secondSnapshot.workspace.activeId).toBe('wallet');
    expect(localAdapter.workspaceChanges).toEqual(['wallet']);
  });

  it('can reconnect to the last server target when switching modes', async () => {
    const localAdapter = new FakeAdminAdapter({ mode: 'local' });
    const serverTargets: string[] = [];
    const state = createAdminState({
      localAdapter: () => localAdapter,
      serverAdapter: (target) => {
        serverTargets.push(target.baseUrl);
        return new FakeAdminAdapter({
          mode: 'server',
          serverTarget: target,
          permissions: {
            authenticated: true,
            role: 'admin',
            canManageUsers: true,
            canManageFrontend: true,
            canManageStore: true,
            canOpenWallet: true,
          },
        });
      },
    });

    await state.connectServer({ baseUrl: 'https://node.example' });
    await state.setMode('local');
    await state.setMode('server');

    expect(serverTargets).toEqual(['https://node.example', 'https://node.example']);
    expect(state.snapshot().mode).toBe('server');
  });
});

function createStateWithLocal(localAdapter: FakeAdminAdapter): AdminState {
  return createAdminState({
    localAdapter: () => localAdapter,
    serverAdapter: (target) => new FakeAdminAdapter({
      mode: 'server',
      serverTarget: target,
    }),
  });
}

class FakeAdminAdapter implements AdminAdapter {
  readonly mode: AdminMode;
  readonly workspaceChanges: AdminWorkspaceId[] = [];
  #snapshot: AdminSnapshot;

  constructor(seed: Partial<AdminSnapshot> & { mode: AdminMode }) {
    this.mode = seed.mode;
    this.#snapshot = makeSnapshot(seed);
  }

  async connect(): Promise<AdminSnapshot> {
    return cloneSnapshot(this.#snapshot);
  }

  async snapshot(): Promise<AdminSnapshot> {
    return cloneSnapshot(this.#snapshot);
  }

  async setWorkspace(workspaceId: AdminWorkspaceId): Promise<AdminSnapshot> {
    this.workspaceChanges.push(workspaceId);
    this.#snapshot = makeSnapshot({
      ...this.#snapshot,
      workspace: {
        ...this.#snapshot.workspace,
        activeId: workspaceId,
      },
    });
    return cloneSnapshot(this.#snapshot);
  }
}

function makeSnapshot(seed: Partial<AdminSnapshot> & { mode: AdminMode }): AdminSnapshot {
  return {
    mode: seed.mode,
    serverTarget: seed.serverTarget ? { ...seed.serverTarget } : null,
    nodeContext: {
      displayName: seed.nodeContext?.displayName ?? (seed.mode === 'local' ? 'Local backend' : 'Server backend'),
      peerId: seed.nodeContext?.peerId ?? null,
      xpub: seed.nodeContext?.xpub ?? null,
      transport: seed.nodeContext?.transport ?? (seed.mode === 'local' ? 'helia' : 'https'),
      descriptorUrl: seed.nodeContext?.descriptorUrl ?? null,
    },
    permissions: {
      authenticated: seed.permissions?.authenticated ?? false,
      role: seed.permissions?.role ?? 'guest',
      canManageUsers: seed.permissions?.canManageUsers ?? false,
      canManageFrontend: seed.permissions?.canManageFrontend ?? false,
      canManageStore: seed.permissions?.canManageStore ?? false,
      canOpenWallet: seed.permissions?.canOpenWallet ?? true,
    },
    workspace: {
      activeId: seed.workspace?.activeId ?? 'network',
      available: [...(seed.workspace?.available ?? ['network', 'directory', 'store', 'frontend', 'wallet'])],
    },
  };
}

function cloneSnapshot(snapshot: AdminSnapshot): AdminSnapshot {
  return {
    ...snapshot,
    serverTarget: snapshot.serverTarget ? { ...snapshot.serverTarget } : null,
    nodeContext: { ...snapshot.nodeContext },
    permissions: { ...snapshot.permissions },
    workspace: {
      activeId: snapshot.workspace.activeId,
      available: [...snapshot.workspace.available],
    },
  };
}
