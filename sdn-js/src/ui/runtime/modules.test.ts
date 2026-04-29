import { describe, expect, it, vi } from 'vitest';

import { loadModuleRuntimeSnapshotFromServer } from './modules';

describe('loadModuleRuntimeSnapshotFromServer', () => {
  it('normalizes module runtime data from the server API', async () => {
    const fetch = vi.fn(async (input: string) => {
      expect(input).toBe('https://node.example/api/v1/modules/runtime');
      return jsonResponse(200, {
        generatedAt: '2026-04-29T16:00:00Z',
        count: 1,
        modules: [
          {
            id: 'licensing',
            version: '1.2.3',
            status: 'running',
            manifest: {
              pluginId: 'licensing',
              name: 'Licensing',
              pluginFamily: 'INFRASTRUCTURE',
              methods: [{ methodId: 'server_handle_message' }],
              capabilities: ['protocol_handle'],
              protocols: [{ protocolId: 'module-delivery', wireId: '/space-data-network/module-delivery/1.0.0' }],
              timers: [{ timerId: 'refresh-grants', defaultIntervalMs: 30000 }],
            },
            stats: {
              memoryPages: 7,
              memoryBytes: 458752,
              maxMemoryPages: 1024,
              maxMemoryBytes: 67108864,
            },
            options: [
              {
                key: 'timer.refresh-grants.interval',
                label: 'Timer refresh grants interval',
                type: 'duration-ms',
                value: '30000',
              },
            ],
          },
        ],
      });
    });

    const snapshot = await loadModuleRuntimeSnapshotFromServer('https://node.example/', fetch);

    expect(snapshot.count).toBe(1);
    expect(snapshot.modules[0]).toMatchObject({
      id: 'licensing',
      version: '1.2.3',
      status: 'running',
      manifest: {
        pluginId: 'licensing',
        methods: [{ methodId: 'server_handle_message' }],
      },
      stats: {
        memoryBytes: 458752,
        maxMemoryBytes: 67108864,
      },
    });
    expect(snapshot.modules[0]?.options[0]?.key).toBe('timer.refresh-grants.interval');
  });

  it('returns an empty snapshot for unavailable module runtime APIs', async () => {
    const fetch = vi.fn(async () => jsonResponse(404, { code: 'not_found' }));

    await expect(loadModuleRuntimeSnapshotFromServer('https://node.example', fetch)).resolves.toMatchObject({
      count: 0,
      modules: [],
    });
  });

  it('returns an empty snapshot for dev-server HTML fallbacks', async () => {
    const fetch = vi.fn(async () => htmlResponse(200, '<!doctype html>'));

    await expect(loadModuleRuntimeSnapshotFromServer('http://127.0.0.1:5174', fetch)).resolves.toMatchObject({
      count: 0,
      modules: [],
    });
  });
});

function jsonResponse(status: number, payload: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ 'content-type': 'application/json' }),
    async json() {
      return payload;
    },
  };
}

function htmlResponse(status: number, payload: string) {
  return {
    ok: status >= 200 && status < 300,
    status,
    redirected: false,
    headers: new Headers({ 'content-type': 'text/html' }),
    async json() {
      return JSON.parse(payload);
    },
  };
}
