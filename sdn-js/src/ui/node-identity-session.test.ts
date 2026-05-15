import { describe, expect, it, vi } from 'vitest';
import {
  createNodeIdentitySessionController,
  type NodeIdentitySessionState,
} from '../../ui/src/lib/node-identity-session';
import type { BackendResult, NodeIdentitySettings, SdnBackend } from './runtime/sdn-backend';

describe('node identity session controller', () => {
  it('restores an unexpired persisted desktop unlock session from identity settings', async () => {
    const states: NodeIdentitySessionState[] = [];
    const backend = fakeBackend({
      ttlMs: 900_000,
      session: {
        unlocked: true,
        expiresAt: '1970-01-01T00:00:10.000Z',
        profile: {
          peer_id: '12D3KooWPersisted',
          signing_public_key: 'aa',
        },
      },
    });
    const mountWallet = vi.fn();
    const controller = createNodeIdentitySessionController({
      backend,
      mountWallet,
      now: () => 1_000,
      onStateChange: (state) => states.push(state),
    });

    await controller.loadSettings();

    expect(controller.state.locked).toBe(false);
    expect(controller.state.status).toBe('Unlocked');
    expect(controller.state.sessionExpiresAt).toBe(10_000);
    expect(controller.state.profile).toMatchObject({ peer_id: '12D3KooWPersisted' });
    expect(mountWallet).not.toHaveBeenCalled();
    expect(states.at(-1)).toMatchObject({ locked: false, status: 'Unlocked' });
    controller.destroy();
  });
});

function fakeBackend(settings: NodeIdentitySettings): SdnBackend {
  return {
    mode: 'desktop-local',
    getNodeIdentitySettings: async () => ok(settings),
    saveNodeIdentitySettings: async (nextSettings) => ok(nextSettings),
    logoutNodeIdentity: async () => ok({ ok: true }),
  } as Partial<SdnBackend> as SdnBackend;
}

function ok<T>(data: T): BackendResult<T> {
  return {
    ok: true,
    capability: { id: 'test', state: 'available' },
    data,
  };
}
