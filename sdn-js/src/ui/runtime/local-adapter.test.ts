import { describe, expect, it } from 'vitest';

import { createLocalAdapter } from './local-adapter';

describe('createLocalAdapter', () => {
  it('connects a local helia-backed snapshot and preserves workspace changes', async () => {
    const adapter = createLocalAdapter({
      getNodeContext: async () => ({
        displayName: 'Local Helia node',
        peerId: '12D3KooWLocal',
        transport: 'helia',
      }),
      getPermissions: async () => ({
        authenticated: true,
        role: 'local',
        canManageUsers: true,
        canManageFrontend: true,
        canManageStore: true,
        canOpenWallet: true,
      }),
    });

    const connected = await adapter.connect();
    expect(connected.mode).toBe('local');
    expect(connected.nodeContext.peerId).toBe('12D3KooWLocal');
    expect(connected.permissions.canManageUsers).toBe(true);
    expect(connected.workspace.activeId).toBe('network');

    const updated = await adapter.setWorkspace('wallet');
    expect(updated.workspace.activeId).toBe('wallet');
    expect((await adapter.snapshot()).workspace.activeId).toBe('wallet');
  });
});
