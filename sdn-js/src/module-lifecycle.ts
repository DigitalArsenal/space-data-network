/**
 * Browser installed-module lifecycle (WS6.4).
 *
 * Builds on the WS4.5 InstalledModuleRegistry (id/version metadata +
 * persistence) with the two missing pieces:
 *
 *  - a ModuleBytesStore that caches each installed module's DECRYPTED wasm
 *    bytes (+ optional manifest bytes) so a page reload can restart modules
 *    without re-running the delivery grant flow (IndexedDB in the browser,
 *    in-memory in tests), and
 *  - a runtime manager with start/stop lifecycle: start loads the cached
 *    bytes into a module harness (pluggable loader — use a worker harness for
 *    modules that make host capability calls) and starts the module's
 *    manifest TIMERS via the SDK timer driver; stop tears both down.
 *
 * Install dedupes by pluginId: re-installing the same id+version is a no-op;
 * a different version replaces the cached bytes and registry record.
 */

import { openDB, type IDBPDatabase } from 'idb';
// Browser-safe leaf subpath — the SDK root entry pulls node-flavored modules
// (worker harness dynamic imports) that break browser bundling.
import { createModuleTimerDriver } from 'space-data-module-sdk/host/timer-driver';
import { InstalledModuleRegistry } from './installed-module-registry';

export interface CachedModuleBytes {
  pluginId: string;
  version: string;
  wasmBytes: Uint8Array;
  manifestBytes?: Uint8Array;
  installedAtMs?: number;
}

export interface ModuleBytesStore {
  put(entry: CachedModuleBytes): Promise<void>;
  get(pluginId: string): Promise<CachedModuleBytes | undefined>;
  delete(pluginId: string): Promise<void>;
  list(): Promise<Array<{ pluginId: string; version: string }>>;
}

/** In-memory bytes store for tests / ephemeral nodes. */
export class MemoryModuleBytesStore implements ModuleBytesStore {
  private readonly entries = new Map<string, CachedModuleBytes>();
  async put(entry: CachedModuleBytes): Promise<void> {
    this.entries.set(entry.pluginId, {
      ...entry,
      wasmBytes: new Uint8Array(entry.wasmBytes),
      manifestBytes: entry.manifestBytes
        ? new Uint8Array(entry.manifestBytes)
        : undefined,
    });
  }
  async get(pluginId: string): Promise<CachedModuleBytes | undefined> {
    const entry = this.entries.get(pluginId);
    return entry
      ? { ...entry, wasmBytes: new Uint8Array(entry.wasmBytes) }
      : undefined;
  }
  async delete(pluginId: string): Promise<void> {
    this.entries.delete(pluginId);
  }
  async list(): Promise<Array<{ pluginId: string; version: string }>> {
    return [...this.entries.values()].map(({ pluginId, version }) => ({
      pluginId,
      version,
    }));
  }
}

/** IndexedDB-backed bytes store (browser). */
export class IndexedDbModuleBytesStore implements ModuleBytesStore {
  private db: IDBPDatabase | null = null;

  constructor(private readonly dbName = 'sdn-installed-modules') {}

  private async open(): Promise<IDBPDatabase> {
    if (!this.db) {
      this.db = await openDB(this.dbName, 1, {
        upgrade(db) {
          if (!db.objectStoreNames.contains('modules')) {
            db.createObjectStore('modules', { keyPath: 'pluginId' });
          }
        },
      });
    }
    return this.db;
  }

  async put(entry: CachedModuleBytes): Promise<void> {
    const db = await this.open();
    await db.put('modules', entry);
  }

  async get(pluginId: string): Promise<CachedModuleBytes | undefined> {
    const db = await this.open();
    return (await db.get('modules', pluginId)) as CachedModuleBytes | undefined;
  }

  async delete(pluginId: string): Promise<void> {
    const db = await this.open();
    await db.delete('modules', pluginId);
  }

  async list(): Promise<Array<{ pluginId: string; version: string }>> {
    const db = await this.open();
    const entries = (await db.getAll('modules')) as CachedModuleBytes[];
    return entries.map(({ pluginId, version }) => ({ pluginId, version }));
  }
}

/** Minimal harness contract the lifecycle needs (browser or worker harness). */
export interface ModuleHarnessLike {
  invoke(request: unknown): Promise<unknown>;
  invokeRaw?(requestBytes: Uint8Array): Promise<unknown>;
  readManifest?():
    | Uint8Array
    | null
    | Promise<Uint8Array | ArrayBuffer | null>;
  destroy?(): void | Promise<void>;
}

export type ModuleLoader = (
  wasmBytes: Uint8Array,
  pluginId: string,
) => Promise<ModuleHarnessLike>;

export interface TimerScheduleOverride {
  enabled?: boolean;
  intervalMs?: number;
}

export interface RunningModule {
  pluginId: string;
  version: string;
  startedAtMs: number;
  timerIds: string[];
}

export interface ModuleRuntimeManagerOptions {
  registry: InstalledModuleRegistry;
  bytesStore: ModuleBytesStore;
  /** Loads decrypted bytes into a harness (e.g. worker harness for cap-calling modules). */
  loadModule: ModuleLoader;
  /** Per-module timer schedule overrides: pluginId -> timerId -> override. */
  schedules?: Record<string, Record<string, TimerScheduleOverride>>;
  minTimerIntervalMs?: number;
  nowMs?: () => number;
  onTimerRun?: (pluginId: string, run: unknown) => void;
}

interface RunningEntry {
  handle: RunningModule;
  harness: ModuleHarnessLike;
  driver: { stop(): void; listTimers(): Array<{ timerId: string }> } | null;
}

async function resolveManifestBytes(
  cached: CachedModuleBytes,
  harness: ModuleHarnessLike,
): Promise<Uint8Array | null> {
  if (cached.manifestBytes && cached.manifestBytes.length > 0) {
    return new Uint8Array(cached.manifestBytes);
  }
  if (typeof harness.readManifest !== 'function') return null;
  const bytes = await harness.readManifest();
  if (!bytes) return null;
  return bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
}

/**
 * createModuleRuntimeManager — install/start/stop/uninstall lifecycle over the
 * installed registry + bytes cache.
 */
export function createModuleRuntimeManager(options: ModuleRuntimeManagerOptions) {
  const { registry, bytesStore, loadModule } = options;
  const nowMs = options.nowMs ?? (() => Date.now());
  const running = new Map<string, RunningEntry>();

  return {
    /**
     * Cache decrypted bytes + record the install. Same id+version is a no-op;
     * a different version replaces the cache and the registry record.
     */
    async install(entry: CachedModuleBytes): Promise<'installed' | 'unchanged' | 'updated'> {
      const pluginId = entry.pluginId.trim();
      if (!pluginId) throw new Error('install requires a pluginId');
      const existing = registry.installedVersion(pluginId);
      if (existing === entry.version && (await bytesStore.get(pluginId))) {
        return 'unchanged';
      }
      await bytesStore.put({ ...entry, pluginId, installedAtMs: entry.installedAtMs ?? nowMs() });
      registry.record({ pluginId, version: entry.version, installedAtMs: nowMs() });
      return existing === undefined ? 'installed' : 'updated';
    },

    async start(pluginId: string): Promise<RunningModule> {
      const id = pluginId.trim();
      const already = running.get(id);
      if (already) return already.handle;
      const cached = await bytesStore.get(id);
      if (!cached) {
        throw new Error(`module ${id} is not installed (no cached bytes)`);
      }
      const harness = await loadModule(new Uint8Array(cached.wasmBytes), id);
      const manifestBytes = await resolveManifestBytes(cached, harness);
      let driver: RunningEntry['driver'] = null;
      let timerIds: string[] = [];
      if (manifestBytes) {
        const candidate = createModuleTimerDriver({
          harness,
          manifestBytes,
          schedules: options.schedules?.[id] ?? {},
          minIntervalMs: options.minTimerIntervalMs,
          onRun: options.onTimerRun
            ? (run: unknown) => options.onTimerRun?.(id, run)
            : undefined,
        });
        const timers = candidate.start();
        timerIds = timers.filter((t: { scheduled: boolean }) => t.scheduled).map(
          (t: { timerId: string }) => t.timerId,
        );
        driver = timerIds.length > 0 ? candidate : (candidate.stop(), null);
      }
      const handle: RunningModule = {
        pluginId: id,
        version: cached.version,
        startedAtMs: nowMs(),
        timerIds,
      };
      running.set(id, { handle, harness, driver });
      return handle;
    },

    async stop(pluginId: string): Promise<boolean> {
      const id = pluginId.trim();
      const entry = running.get(id);
      if (!entry) return false;
      running.delete(id);
      entry.driver?.stop();
      await entry.harness.destroy?.();
      return true;
    },

    async uninstall(pluginId: string): Promise<void> {
      await this.stop(pluginId);
      await bytesStore.delete(pluginId.trim());
      registry.remove(pluginId);
    },

    isRunning(pluginId: string): boolean {
      return running.has(pluginId.trim());
    },

    listRunning(): RunningModule[] {
      return [...running.values()].map((entry) => ({ ...entry.handle }));
    },

    async stopAll(): Promise<void> {
      for (const id of [...running.keys()]) {
        await this.stop(id);
      }
    },
  };
}
