import { describe, expect, it } from 'vitest';

import { createBackendFromLocation } from '../../../ui/src/lib/backend-context';

describe('SDN UI backend context', () => {
  it('uses remote-sdn mode for hosted SDN pages with injected server config', () => {
    const backend = createBackendFromLocation(
      {
        origin: 'https://sdn.spaceaware.io',
        search: '',
      } as Location,
      {
        __SDN_CONFIG__: {
          serverBaseUrl: 'https://sdn.spaceaware.io',
        },
      },
    );

    expect(backend.mode).toBe('remote-sdn');
  });

  it('keeps desktop-local mode for desktop pages without hosted server config', () => {
    const backend = createBackendFromLocation(
      {
        origin: 'http://127.0.0.1:17890',
        search: '',
      } as Location,
      {},
    );

    expect(backend.mode).toBe('desktop-local');
  });
});
