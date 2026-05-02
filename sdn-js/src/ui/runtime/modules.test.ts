import { describe, expect, it, vi } from 'vitest';

import {
  loadModuleRuntimeSnapshotFromServer,
  resolveSelectedModuleId,
  runModuleRuntimeAction,
  saveModuleRuntimeInputValues,
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
              methods: [
                {
                  methodId: 'server_handle_message',
                  maxBatch: 4,
                  drainPolicy: 'DRAIN_UNTIL_YIELD',
                  inputPorts: [
                    {
                      portId: 'request',
                      displayName: 'Request',
                      minStreams: 1,
                      maxStreams: 1,
                      required: true,
                      acceptedTypeSets: [
                        {
                          setId: 'module-delivery-request',
                          allowedTypes: [
                            {
                              schemaName: 'MODULE.fbs',
                              fileIdentifier: 'MODL',
                              schemaVersion: '1.0.0',
                              rootType: 'ModuleDeliveryRequest',
                            },
                          ],
                          allowedWireFormats: ['FLATBUFFER'],
                        },
                      ],
                    },
                  ],
                  outputPorts: [
                    {
                      portId: 'response',
                      required: true,
                    },
                  ],
                },
              ],
              capabilities: ['protocol_handle'],
              protocols: [
                {
                  protocolId: 'module-delivery',
                  wireId: '/space-data-network/module-delivery/1.0.0',
                },
              ],
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
            inputValues: [
              {
                methodId: 'server_handle_message',
                portId: 'request',
                wireFormat: 'FLATBUFFER_JSON',
                encoding: 'json',
                schemaName: 'MODULE.fbs',
                rootType: 'ModuleDeliveryRequest',
                value: '{"reqId":"abc"}',
                updatedAt: '2026-04-30T12:01:00Z',
              },
            ],
            restartPending: true,
            commandHistory: [
              {
                id: '20260430120100-000001',
                at: '2026-04-30T12:01:00Z',
                command: 'save-inputs',
                status: 'updated',
                methodId: 'server_handle_message',
                portId: 'request',
                summary: 'Saved 1 input value',
              },
            ],
          },
        ],
      });
    });

    const snapshot = await loadModuleRuntimeSnapshotFromServer(
      'https://node.example/',
      fetch,
    );

    expect(snapshot.count).toBe(1);
    expect(snapshot.modules[0]).toMatchObject({
      id: 'licensing',
      version: '1.2.3',
      status: 'running',
      manifest: {
        pluginId: 'licensing',
        methods: [
          {
            methodId: 'server_handle_message',
            maxBatch: 4,
            drainPolicy: 'DRAIN_UNTIL_YIELD',
            inputPorts: [
              {
                portId: 'request',
                required: true,
                acceptedTypeSets: [
                  {
                    allowedTypes: [{ rootType: 'ModuleDeliveryRequest' }],
                    allowedWireFormats: ['FLATBUFFER'],
                  },
                ],
              },
            ],
            outputPorts: [{ portId: 'response', required: true }],
          },
        ],
      },
      stats: {
        memoryBytes: 458752,
        maxMemoryBytes: 67108864,
        hostRssBytes: 1048576,
        invokeCount: 11,
        averageLatencyMs: 14.25,
      },
      restartPending: true,
    });
    expect(snapshot.modules[0]?.inputValues[0]).toMatchObject({
      methodId: 'server_handle_message',
      portId: 'request',
      wireFormat: 'FLATBUFFER_JSON',
      encoding: 'json',
      rootType: 'ModuleDeliveryRequest',
      value: '{"reqId":"abc"}',
    });
    expect(snapshot.modules[0]?.commandHistory[0]).toMatchObject({
      command: 'save-inputs',
      status: 'updated',
      methodId: 'server_handle_message',
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
    expect(snapshot.modules[0]?.links?.logsUrl).toBe(
      '/api/v1/modules/runtime/licensing/logs',
    );
  });

  it('returns an empty snapshot for unavailable module runtime APIs', async () => {
    const fetch = vi.fn(async () => jsonResponse(404, { code: 'not_found' }));

    await expect(
      loadModuleRuntimeSnapshotFromServer('https://node.example', fetch),
    ).resolves.toMatchObject({
      count: 0,
      modules: [],
    });
  });

  it('returns an empty snapshot for dev-server HTML fallbacks', async () => {
    const fetch = vi.fn(async () => htmlResponse(200, '<!doctype html>'));

    await expect(
      loadModuleRuntimeSnapshotFromServer('http://127.0.0.1:5174', fetch),
    ).resolves.toMatchObject({
      count: 0,
      modules: [],
    });
  });
});

describe('resolveSelectedModuleId', () => {
  it('keeps the selected module across refreshes when it still exists', () => {
    expect(
      resolveSelectedModuleId('analysis', [
        { id: 'licensing' },
        { id: 'analysis' },
      ]),
    ).toBe('analysis');
  });

  it('falls back to the first module only when the selected module is missing', () => {
    expect(
      resolveSelectedModuleId('missing', [
        { id: 'licensing' },
        { id: 'analysis' },
      ]),
    ).toBe('licensing');
  });

  it('returns an empty selection for an empty module list', () => {
    expect(resolveSelectedModuleId('analysis', [])).toBe('');
  });
});

describe('module runtime mutations', () => {
  it('updates runtime options through the server mutation API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      expect(input).toBe(
        'https://node.example/api/v1/modules/runtime/licensing/options/timer.refresh-grants.interval',
      );
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

    await expect(
      updateModuleRuntimeOption(
        'https://node.example/',
        'licensing',
        'timer.refresh-grants.interval',
        '45000',
        fetch,
      ),
    ).resolves.toMatchObject({
      key: 'timer.refresh-grants.interval',
      value: '45000',
      units: 'ms',
    });
  });

  it('runs lifecycle actions through the server action API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      expect(input).toBe(
        'https://node.example/api/v1/modules/runtime/licensing/actions/clear-error',
      );
      expect(init?.method).toBe('POST');
      expect(init?.credentials).toBe('include');
      expect(init?.headers).toMatchObject({
        'x-requested-with': 'XMLHttpRequest',
      });
      return jsonResponse(200, { ok: true, actionId: 'clear-error' });
    });

    await expect(
      runModuleRuntimeAction(
        'https://node.example/',
        'licensing',
        'clear-error',
        fetch,
      ),
    ).resolves.toMatchObject({ ok: true, actionId: 'clear-error' });
  });

  it('saves runtime input values through the server input API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      expect(input).toBe('https://node.example/api/v1/modules/runtime/licensing/inputs');
      expect(init?.method).toBe('PATCH');
      expect(init?.credentials).toBe('include');
      expect(init?.headers).toMatchObject({
        'content-type': 'application/json',
        'x-requested-with': 'XMLHttpRequest',
      });
      expect(JSON.parse(String(init?.body))).toEqual({
        values: [
          {
            methodId: 'server_configure_runtime',
            portId: 'request',
            wireFormat: 'FLATBUFFER_JSON',
            encoding: 'json',
            schemaName: 'MODULE.fbs',
            rootType: 'ConfigureRuntimeRequest',
            value: '{"refreshIntervalMs":45000}',
          },
        ],
      });
      return jsonResponse(200, {
        moduleId: 'licensing',
        restartPending: true,
        inputValues: [
          {
            methodId: 'server_configure_runtime',
            portId: 'request',
            wireFormat: 'FLATBUFFER_JSON',
            encoding: 'json',
            schemaName: 'MODULE.fbs',
            rootType: 'ConfigureRuntimeRequest',
            value: '{"refreshIntervalMs":45000}',
            updatedAt: '2026-05-02T12:00:00Z',
          },
        ],
      });
    });

    await expect(
      saveModuleRuntimeInputValues(
        'https://node.example/',
        'licensing',
        [
          {
            methodId: 'server_configure_runtime',
            portId: 'request',
            wireFormat: 'FLATBUFFER_JSON',
            encoding: 'json',
            schemaName: 'MODULE.fbs',
            rootType: 'ConfigureRuntimeRequest',
            value: '{"refreshIntervalMs":45000}',
          },
        ],
        fetch,
      ),
    ).resolves.toMatchObject({
      moduleId: 'licensing',
      restartPending: true,
      inputValues: [
        {
          methodId: 'server_configure_runtime',
          portId: 'request',
          rootType: 'ConfigureRuntimeRequest',
        },
      ],
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
