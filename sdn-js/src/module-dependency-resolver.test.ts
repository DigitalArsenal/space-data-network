import { describe, it, expect } from 'vitest';

import {
  DependencyError,
  MapModuleCatalog,
  type ModuleManifest,
  type InstalledModules,
  type PlanStep,
  compareSemver,
  installClosure,
  resolveClosure,
  satisfies,
} from './module-dependency-resolver';
import {
  InstalledModuleRegistry,
  MemoryRegistryStore,
  createModuleInstaller,
} from './installed-module-registry';

function installed(map: Record<string, string>): InstalledModules {
  return { installedVersion: (id) => map[id.trim()] };
}

function ids(steps: PlanStep[]): string[] {
  return steps.map((s) => s.pluginId);
}

describe('resolveClosure', () => {
  it('orders a linear chain dependency-first and excludes the root', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'c', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.2.0' },
    ]);
    const root: ModuleManifest = {
      pluginId: 'a',
      version: '1.0.0',
      dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }],
    };
    const plan = await resolveClosure(root, null, catalog);
    expect(ids(plan)).toEqual(['c', 'b']);
    expect(plan[0].version).toBe('1.2.0'); // highest satisfying
  });

  it('dedups a diamond, installing the shared dep once before both dependents', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'd', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.0.0', dependencies: [{ pluginId: 'd', minVersion: '1.0.0' }] },
      { pluginId: 'd', version: '1.5.0' },
    ]);
    const plan = await resolveClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }, { pluginId: 'c', minVersion: '1.0.0' }] },
      null,
      catalog,
    );
    const order = ids(plan);
    expect(order.filter((x) => x === 'd')).toHaveLength(1);
    expect(order.indexOf('d')).toBeLessThan(order.indexOf('b'));
    expect(order.indexOf('d')).toBeLessThan(order.indexOf('c'));
  });

  it('skips a satisfied installed dependency and its subtree', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'c', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.0.0' },
    ]);
    const plan = await resolveClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
      installed({ b: '1.4.0' }),
      catalog,
    );
    expect(plan).toHaveLength(0);
  });

  it('throws version_conflict when an installed dep cannot satisfy', async () => {
    const catalog = new MapModuleCatalog([{ pluginId: 'b', version: '2.0.0' }]);
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '2.0.0' }] },
        installed({ b: '1.0.0' }),
        catalog,
      ),
    ).rejects.toMatchObject({ code: 'version_conflict' });
  });

  it('throws not_found for a missing dependency', async () => {
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'ghost', minVersion: '1.0.0' }] },
        null,
        new MapModuleCatalog(),
      ),
    ).rejects.toMatchObject({ code: 'not_found' });
  });

  it('throws no_satisfying_version when no published version fits the range', async () => {
    const catalog = new MapModuleCatalog([{ pluginId: 'b', version: '1.0.0' }]);
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '2.0.0' }] },
        null,
        catalog,
      ),
    ).rejects.toMatchObject({ code: 'no_satisfying_version' });
  });

  it('detects a cycle', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'a', minVersion: '1.0.0' }] },
    ]);
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
        null,
        catalog,
      ),
    ).rejects.toMatchObject({ code: 'cycle' });
  });

  it('detects a self-cycle', async () => {
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'a', minVersion: '1.0.0' }] },
        null,
        new MapModuleCatalog(),
      ),
    ).rejects.toMatchObject({ code: 'cycle' });
  });

  it('rejects an invalid range', async () => {
    await expect(
      resolveClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: 'nope' }] },
        null,
        new MapModuleCatalog(),
      ),
    ).rejects.toBeInstanceOf(DependencyError);
  });

  it('selects the highest version within a bounded range', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0' },
      { pluginId: 'b', version: '1.5.0' },
      { pluginId: 'b', version: '2.0.0' },
    ]);
    const plan = await resolveClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0', maxVersion: '1.9.0' }] },
      null,
      catalog,
    );
    expect(plan).toEqual([{ pluginId: 'b', version: '1.5.0' }]);
  });
});

describe('installClosure', () => {
  it('installs dependencies first, then the root, recording order', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'c', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.0.0' },
    ]);
    const order: string[] = [];
    const done = await installClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
      null,
      catalog,
      async (step) => {
        order.push(step.pluginId);
      },
    );
    expect(order).toEqual(['c', 'b', 'a']);
    expect(ids(done)).toEqual(['c', 'b', 'a']);
  });

  it('installs nothing when root and deps are already installed', async () => {
    const catalog = new MapModuleCatalog([{ pluginId: 'b', version: '1.0.0' }]);
    const order: string[] = [];
    await installClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
      installed({ a: '1.0.0', b: '1.0.0' }),
      catalog,
      async (step) => {
        order.push(step.pluginId);
      },
    );
    expect(order).toHaveLength(0);
  });

  it('aborts on the first install failure', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'c', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.0.0' },
    ]);
    const order: string[] = [];
    await expect(
      installClosure(
        { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
        null,
        catalog,
        async (step) => {
          if (step.pluginId === 'b') throw new Error('boom');
          order.push(step.pluginId);
        },
      ),
    ).rejects.toThrow('boom');
    expect(order).toEqual(['c']); // c installed before the failure; a never reached
  });
});

describe('semver helpers', () => {
  it('compares and checks satisfaction', () => {
    expect(compareSemver('1.2.0', '1.3.0')).toBe(-1);
    expect(compareSemver('2.0.0', '2.0.0')).toBe(0);
    expect(compareSemver('v1.0.1', '1.0.0')).toBe(1);
    expect(satisfies('1.5.0', { pluginId: 'x', minVersion: '1.0.0', maxVersion: '2.0.0' })).toBe(true);
    expect(satisfies('2.0.1', { pluginId: 'x', minVersion: '1.0.0', maxVersion: '2.0.0' })).toBe(false);
    expect(satisfies('garbage', { pluginId: 'x' })).toBe(false);
  });
});

describe('InstalledModuleRegistry', () => {
  it('records, reports, and persists installs', () => {
    const store = new MemoryRegistryStore();
    const reg = new InstalledModuleRegistry(store);
    reg.record({ pluginId: 'a', version: '1.0.0' });
    expect(reg.has('a')).toBe(true);
    expect(reg.installedVersion('a')).toBe('1.0.0');
    expect(reg.list()).toHaveLength(1);

    // A fresh registry over the same store recovers the install (persistence).
    const reloaded = new InstalledModuleRegistry(store);
    expect(reloaded.installedVersion('a')).toBe('1.0.0');

    reg.remove('a');
    expect(new InstalledModuleRegistry(store).has('a')).toBe(false);
  });
});

describe('createModuleInstaller + installClosure (end to end)', () => {
  it('fetches, decrypts, registers, and records each module dependency-first', async () => {
    const catalog = new MapModuleCatalog([
      { pluginId: 'b', version: '1.0.0', dependencies: [{ pluginId: 'c', minVersion: '1.0.0' }] },
      { pluginId: 'c', version: '1.0.0' },
    ]);
    const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
    const registered: string[] = [];

    const installFn = createModuleInstaller({
      registry,
      fetchAndDecrypt: async (step) => new TextEncoder().encode(`wasm:${step.pluginId}`),
      register: async (step) => {
        registered.push(step.pluginId);
      },
    });

    await installClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
      registry,
      catalog,
      installFn,
    );

    expect(registered).toEqual(['c', 'b', 'a']);
    expect(registry.installedVersion('c')).toBe('1.0.0');
    expect(registry.installedVersion('a')).toBe('1.0.0');

    // Re-installing is idempotent: nothing new is fetched/registered.
    registered.length = 0;
    await installClosure(
      { pluginId: 'a', version: '1.0.0', dependencies: [{ pluginId: 'b', minVersion: '1.0.0' }] },
      registry,
      catalog,
      installFn,
    );
    expect(registered).toHaveLength(0);
  });

  it('rejects an empty decrypted bundle', async () => {
    const registry = new InstalledModuleRegistry();
    const installFn = createModuleInstaller({
      registry,
      fetchAndDecrypt: async () => new Uint8Array(),
      register: async () => {},
    });
    await expect(installFn({ pluginId: 'x', version: '1.0.0' })).rejects.toThrow(/empty module bundle/);
  });
});
