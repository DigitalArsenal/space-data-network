/**
 * Installed-module registry + delivery-backed installer for the browser/Helia
 * node (WS4.5). Tracks which modules are installed, persists across sessions via
 * a pluggable RegistryStore (localStorage/IndexedDB in the browser, in-memory in
 * tests), and provides the per-module fetch -> decrypt -> register step the
 * dependency installer drives (installClosure).
 */

import type { InstalledModules, InstallFn, PlanStep } from './module-dependency-resolver';

export interface InstalledModuleRecord {
  pluginId: string;
  version: string;
  installedAtMs?: number;
}

/** RegistryStore persists the registry as a JSON-serializable snapshot. */
export interface RegistryStore {
  load(): InstalledModuleRecord[];
  save(records: InstalledModuleRecord[]): void;
}

/** In-memory RegistryStore for tests / ephemeral nodes. */
export class MemoryRegistryStore implements RegistryStore {
  private snapshot: InstalledModuleRecord[] = [];
  load(): InstalledModuleRecord[] {
    return this.snapshot.map((r) => ({ ...r }));
  }
  save(records: InstalledModuleRecord[]): void {
    this.snapshot = records.map((r) => ({ ...r }));
  }
}

/** localStorage/Storage-backed RegistryStore (browser). */
export class LocalStorageRegistryStore implements RegistryStore {
  constructor(
    private readonly storage: Pick<Storage, 'getItem' | 'setItem'>,
    private readonly key = 'sdn.installed-modules',
  ) {}

  load(): InstalledModuleRecord[] {
    try {
      const raw = this.storage.getItem(this.key);
      if (!raw) return [];
      const parsed: unknown = JSON.parse(raw);
      return Array.isArray(parsed) ? (parsed as InstalledModuleRecord[]) : [];
    } catch {
      return [];
    }
  }

  save(records: InstalledModuleRecord[]): void {
    this.storage.setItem(this.key, JSON.stringify(records));
  }
}

/**
 * InstalledModuleRegistry is the browser node's catalog of installed modules. It
 * implements InstalledModules so the dependency resolver skips modules already
 * present, and persists through an optional RegistryStore.
 */
export class InstalledModuleRegistry implements InstalledModules {
  private readonly records = new Map<string, InstalledModuleRecord>();

  constructor(private readonly store?: RegistryStore) {
    if (store) {
      for (const r of store.load()) {
        const id = (r.pluginId ?? '').trim();
        if (id) this.records.set(id, { ...r, pluginId: id });
      }
    }
  }

  installedVersion(pluginId: string): string | undefined {
    return this.records.get((pluginId ?? '').trim())?.version;
  }

  has(pluginId: string): boolean {
    return this.records.has((pluginId ?? '').trim());
  }

  list(): InstalledModuleRecord[] {
    return [...this.records.values()].map((r) => ({ ...r }));
  }

  record(rec: InstalledModuleRecord): void {
    const id = (rec.pluginId ?? '').trim();
    if (!id) return;
    this.records.set(id, { ...rec, pluginId: id });
    this.store?.save(this.list());
  }

  remove(pluginId: string): void {
    if (this.records.delete((pluginId ?? '').trim())) {
      this.store?.save(this.list());
    }
  }
}

/**
 * FetchAndDecrypt fetches + decrypts a module's WASM bytes for a plan step. In
 * the browser this wraps requestEncryptedModuleBundle + the client-decrypt
 * module; tests inject a fake.
 */
export type FetchAndDecrypt = (step: PlanStep) => Promise<Uint8Array>;

/** RegisterModule loads/registers decrypted module bytes into the runtime. */
export type RegisterModule = (step: PlanStep, wasmBytes: Uint8Array) => Promise<void>;

export interface ModuleInstallerOptions {
  fetchAndDecrypt: FetchAndDecrypt;
  register: RegisterModule;
  registry: InstalledModuleRegistry;
  nowMs?: () => number;
}

/**
 * createModuleInstaller returns an InstallFn that fetches + decrypts a module,
 * registers it into the runtime, and records it in the installed registry — the
 * per-module fetch -> decrypt -> register step installClosure drives, once per
 * module in dependency-first order. Already-installed modules are skipped.
 */
export function createModuleInstaller(options: ModuleInstallerOptions): InstallFn {
  const { fetchAndDecrypt, register, registry, nowMs } = options;
  return async (step: PlanStep): Promise<void> => {
    if (registry.has(step.pluginId)) return; // idempotent
    const wasmBytes = await fetchAndDecrypt(step);
    if (!wasmBytes || wasmBytes.length === 0) {
      throw new Error(`empty module bundle for ${step.pluginId}@${step.version}`);
    }
    await register(step, wasmBytes);
    registry.record({
      pluginId: step.pluginId,
      version: step.version,
      installedAtMs: nowMs ? nowMs() : undefined,
    });
  };
}
