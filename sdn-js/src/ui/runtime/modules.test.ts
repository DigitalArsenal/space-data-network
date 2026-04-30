import { describe, expect, it, vi } from 'vitest';

import {
  loadModuleRuntimeSnapshotFromServer,
  runModuleRuntimeAction,
  updateModuleRuntimeOption,
} from './modules';

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
              hostRssBytes: 1048576,
              invokeCount: 11,
              errorCount: 2,
              lastInvokeAt: '2026-04-30T12:00:00Z',
              averageLatencyMs: 14.25,
              timerRunCount: 3,
              lastTimerStatus: 'ok',
            },
            options: [
              {
                key: 'timer.refresh-grants.interval',
                label: 'Timer refresh grants interval',
                type: 'duration-ms',
                value: '30000',
                units: 'ms',
                min: 1000,
                max: 86400000,
                defaultValue: '30000',
                restartRequired: false,
                persistence: 'live-only',
              },
            ],
            actions: [
              {
                actionId: 'clear-error',
                label: 'Clear error',
                enabled: true,
              },
            ],
            statusHistory: [
              {
                status: 'registered',
                at: '2026-04-30T11:59:00Z',
              },
            ],
            links: {
              logsUrl: '/api/v1/modules/runtime/licensing/logs',
              eventsUrl: '/api/v1/modules/runtime/licensing/events',
            },
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
        hostRssBytes: 1048576,
        invokeCount: 11,
        averageLatencyMs: 14.25,
      },
    });
    expect(snapshot.modules[0]?.options[0]).toMatchObject({
      key: 'timer.refresh-grants.interval',
      min: 1000,
      max: 86400000,
      units: 'ms',
      defaultValue: '30000',
      persistence: 'live-only',
    });
    expect(snapshot.modules[0]?.actions[0]?.actionId).toBe('clear-error');
    expect(snapshot.modules[0]?.statusHistory[0]?.status).toBe('registered');
    expect(snapshot.modules[0]?.links?.logsUrl).toBe('/api/v1/modules/runtime/licensing/logs');
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

describe('module runtime mutations', () => {
  it('updates runtime options through the server mutation API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      expect(input).toBe('https://node.example/api/v1/modules/runtime/licensing/options/timer.refresh-grants.interval');
      expect(init?.method).toBe('PATCH');
      expect(init?.credentials).toBe('include');
      expect(init?.headers).toMatchObject({
        'content-type': 'application/json',
        'x-requested-with': 'XMLHttpRequest',
      });
      expect(JSON.parse(String(init?.body))).toEqual({ value: '45000' });
      return jsonResponse(200, {
        key: 'timer.refresh-grants.interval',
        label: 'Timer refresh grants interval',
        type: 'duration-ms',
        value: '45000',
        units: 'ms',
        readOnly: false,
      });
    });

    await expect(updateModuleRuntimeOption(
      'https://node.example/',
      'licensing',
      'timer.refresh-grants.interval',
      '45000',
      fetch,
    )).resolves.toMatchObject({
      key: 'timer.refresh-grants.interval',
      value: '45000',
      units: 'ms',
    });
  });

  it('runs lifecycle actions through the server action API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      expect(input).toBe('https://node.example/api/v1/modules/runtime/licensing/actions/clear-error');
      expect(init?.method).toBe('POST');
      expect(init?.credentials).toBe('include');
      expect(init?.headers).toMatchObject({
        'x-requested-with': 'XMLHttpRequest',
      });
      return jsonResponse(200, { ok: true, actionId: 'clear-error' });
    });

    await expect(runModuleRuntimeAction(
      'https://node.example/',
      'licensing',
      'clear-error',
      fetch,
    )).resolves.toMatchObject({ ok: true, actionId: 'clear-error' });
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
