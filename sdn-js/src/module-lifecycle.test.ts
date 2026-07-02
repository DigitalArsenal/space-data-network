import { describe, it, expect, vi } from 'vitest';

import {
  createModuleRuntimeManager,
  MemoryModuleBytesStore,
} from './module-lifecycle';
import type { ModuleHarnessLike } from './module-lifecycle';
import {
  InstalledModuleRegistry,
  MemoryRegistryStore,
} from './installed-module-registry';

// A minimal legacy manifest with one timer, encoded through the real SDK
// codec so start() exercises the actual TIMERS decode path.
async function encodeTimerManifest(intervalMs: number): Promise<Uint8Array> {
  const sdk = await import('space-data-module-sdk');
  const { encodePlgManifest, legacyManifestToPlg } = sdk as unknown as {
    encodePlgManifest(input: unknown): Uint8Array;
    legacyManifestToPlg(input: unknown): unknown;
  };
  return encodePlgManifest(
    legacyManifestToPlg({
      pluginId: 'com.example.lifecycle',
      name: 'Lifecycle',
      version: '1.0.0',
      pluginFamily: 'data_source',
      capabilities: ['http'],
      invokeSurfaces: ['command'],
      methods: [{ methodId: 'pull', inputPorts: [], outputPorts: [] }],
      timers: [{ timerId: 'tick', methodId: 'pull', defaultIntervalMs: intervalMs }],
    }),
  );
}

function fakeHarness(invocations: unknown[]): ModuleHarnessLike & { destroyed: boolean } {
  const harness = {
    destroyed: false,
    invoke: vi.fn(async (request: unknown) => {
      invocations.push(request);
      return { ok: true };
    }),
    destroy: vi.fn(() => {
      harness.destroyed = true;
    }),
  };
  return harness;
}

describe('createModuleRuntimeManager', () => {
  it('install dedupes by id+version and updates on version change', async () => {
    const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
    const bytesStore = new MemoryModuleBytesStore();
    const manager = createModuleRuntimeManager({
      registry,
      bytesStore,
      loadModule: async () => fakeHarness([]),
    });

    const bytes = Uint8Array.from([1, 2, 3]);
    expect(await manager.install({ pluginId: 'a', version: '1.0.0', wasmBytes: bytes })).toBe('installed');
    expect(await manager.install({ pluginId: 'a', version: '1.0.0', wasmBytes: bytes })).toBe('unchanged');
    expect(await manager.install({ pluginId: 'a', version: '1.1.0', wasmBytes: bytes })).toBe('updated');
    expect(registry.installedVersion('a')).toBe('1.1.0');
    expect(await bytesStore.list()).toEqual([{ pluginId: 'a', version: '1.1.0' }]);
  });

  it('start loads cached bytes, starts manifest timers, stop tears down', async () => {
    const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
    const bytesStore = new MemoryModuleBytesStore();
    const invocations: unknown[] = [];
    const harness = fakeHarness(invocations);
    const loadedWith: Uint8Array[] = [];
    const manager = createModuleRuntimeManager({
      registry,
      bytesStore,
      loadModule: async (wasmBytes) => {
        loadedWith.push(wasmBytes);
        return harness;
      },
      schedules: { 'com.example.lifecycle': { tick: { intervalMs: 20 } } },
      minTimerIntervalMs: 10,
    });

    await manager.install({
      pluginId: 'com.example.lifecycle',
      version: '1.0.0',
      wasmBytes: Uint8Array.from([9, 9, 9]),
      manifestBytes: await encodeTimerManifest(60_000),
    });

    const handle = await manager.start('com.example.lifecycle');
    expect(handle.timerIds).toEqual(['tick']);
    expect(loadedWith[0]).toEqual(Uint8Array.from([9, 9, 9]));
    expect(manager.isRunning('com.example.lifecycle')).toBe(true);
    // idempotent start returns the same handle
    expect(await manager.start('com.example.lifecycle')).toEqual(handle);

    await new Promise((resolve) => setTimeout(resolve, 90));
    expect(invocations.length).toBeGreaterThanOrEqual(2);
    expect(invocations[0]).toEqual({ methodId: 'pull', inputs: [] });

    expect(await manager.stop('com.example.lifecycle')).toBe(true);
    expect(harness.destroyed).toBe(true);
    const countAfterStop = invocations.length;
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(invocations.length).toBe(countAfterStop);
    expect(manager.isRunning('com.example.lifecycle')).toBe(false);
  });

  it('start without cached bytes throws; uninstall clears everything', async () => {
    const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
    const bytesStore = new MemoryModuleBytesStore();
    const manager = createModuleRuntimeManager({
      registry,
      bytesStore,
      loadModule: async () => fakeHarness([]),
    });
    await expect(manager.start('missing')).rejects.toThrow(/not installed/);

    await manager.install({ pluginId: 'b', version: '1.0.0', wasmBytes: Uint8Array.from([1]) });
    await manager.start('b');
    await manager.uninstall('b');
    expect(manager.isRunning('b')).toBe(false);
    expect(registry.has('b')).toBe(false);
    expect(await bytesStore.get('b')).toBeUndefined();
  });

  it('modules without timers run harness-only', async () => {
    const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
    const bytesStore = new MemoryModuleBytesStore();
    const manager = createModuleRuntimeManager({
      registry,
      bytesStore,
      loadModule: async () => fakeHarness([]),
    });
    await manager.install({ pluginId: 'plain', version: '1.0.0', wasmBytes: Uint8Array.from([1]) });
    const handle = await manager.start('plain');
    expect(handle.timerIds).toEqual([]);
    expect(manager.listRunning().map((m) => m.pluginId)).toEqual(['plain']);
    await manager.stopAll();
    expect(manager.listRunning()).toEqual([]);
  });
});
