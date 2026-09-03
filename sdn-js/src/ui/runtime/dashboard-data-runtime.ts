/**
 * Dashboard data runtime hook.
 *
 * Boots the node-shaped data plane of the embedded dashboard: ONE FlatSQL
 * engine (the same WASM core the node runs) in a Web Worker as an EPHEMERAL
 * store — no persistence key, because a window is never a mirror — driven by
 * the window runtime ({@link createDashboardWindow}), and published on the
 * global seam `globalThis.SDN_DATA_WINDOW`, the same pattern as
 * `SDN_NODE_STATUS` (status-dashboard.ts): the runtime owns the data source,
 * the design owns the rendering.
 *
 * The handle is published synchronously so the UI can hold it from the first
 * frame; every asynchronous method waits for the engine to come up, and the
 * synchronous ones (`status`, `currentLoad`) answer `idle` until it does.
 */

import {
  DASHBOARD_WINDOW_SYNC_PROTOCOL,
  WINDOW_BATCH_ROWS,
  WINDOW_MAX_BYTES,
  WINDOW_MAX_ROWS,
  createDashboardWindow,
  dashboardWindowStateKey,
  encodeDashboardWindowStatus,
  type DashboardWindowLoad,
  type DashboardWindowLoadOptions,
  type DashboardWindowPageOptions,
  type DashboardWindowRuntime,
  type DashboardWindowStandard,
  type DashboardWindowState,
  type WindowStore,
} from './dashboard-window';
import type {
  LocalFlatSqlEngineOptions,
  LocalFlatSqlQueryOptions,
  LocalFlatSqlQueryResult,
  LocalFlatSqlStoreOptions,
} from './local-flatsql';
import { createWorkerLocalFlatSqlStore, type WorkerLocalFlatSqlStoreOptions } from './local-flatsql-worker-client';
import type { FetchLike } from './sdn-backend-adapter-utils';
import { decodeWorkerSchemaSyncProgressFlatBuffer } from './worker-sync-status-flatbuffer';

export {
  DASHBOARD_WINDOW_SYNC_PROTOCOL,
  WINDOW_BATCH_ROWS,
  WINDOW_MAX_BYTES,
  WINDOW_MAX_ROWS,
  type DashboardWindowLoad,
  type DashboardWindowLoadOptions,
  type DashboardWindowPageOptions,
  type DashboardWindowRuntime,
  type DashboardWindowStandard,
  type DashboardWindowState,
};

/** Decode a `$DSS` frame produced by `SDN_DATA_WINDOW.status(code)`. */
export const decodeWindowStatus = decodeWorkerSchemaSyncProgressFlatBuffer;

/** SHA-384 provider shape flatsql's `initFlatSQL` accepts for integrity checks. */
export type DashboardEngineDigest = NonNullable<LocalFlatSqlEngineOptions['computeSHA384']>;

/**
 * Engine boot options forwarded to the worker's `initFlatSQL`: the absolute
 * URL of the directory serving `flatsql.wasm` + `integrity.json` (same-origin
 * `/sdn-js`) and the digest used for the integrity check.
 */
export type DashboardEngineOptions = LocalFlatSqlEngineOptions;

export type DashboardDataStoreOptions = LocalFlatSqlStoreOptions;

export type DashboardDataStoreDeps = WorkerLocalFlatSqlStoreOptions;

/** Store factory seam — defaults to the worker-hosted engine store. */
export type DashboardDataStoreFactory = (
  options: DashboardDataStoreOptions,
  deps?: DashboardDataStoreDeps,
) => Promise<WindowStore>;

export interface DashboardDataRuntimeOptions {
  /** Node origin; '' = same origin. */
  baseUrl?: string | null;
  /** Absolute URL of the engine artifacts directory (relative URLs cannot resolve inside a blob worker). */
  wasmPath: string;
  /** Worker constructor (bundler-inlined `local-flatsql-store.worker.ts`). */
  createWorker?: () => Worker;
  fetch?: FetchLike | null;
  /** Test seam: supply the store instead of booting the worker engine. */
  createStore?: DashboardDataStoreFactory;
}

/** The global seam shape read by the dashboard's data screens. */
export interface SDNDataWindowGlobal extends DashboardWindowRuntime {
  /** Resolves once the engine store is up; rejects when it cannot boot. */
  readonly ready: Promise<void>;
}

declare global {
  // eslint-disable-next-line no-var
  var SDN_DATA_WINDOW: SDNDataWindowGlobal | undefined;
}

/** The name of the global property the UI reads. */
export const SDN_DATA_WINDOW_GLOBAL = 'SDN_DATA_WINDOW' as const;

/** Handle returned by {@link startDashboardDataRuntime}. */
export interface DashboardDataRuntimeHandle extends SDNDataWindowGlobal {
  /** The engine store, once booted. */
  readonly store: Promise<WindowStore>;
}

/**
 * Start the dashboard data runtime and publish it on
 * `globalThis.SDN_DATA_WINDOW`.
 *
 * `destroy()` drops every window, tears down the engine store (and its
 * worker) and removes the global — only when it still points at this handle,
 * so a subsequent runtime is never clobbered.
 */
export function startDashboardDataRuntime(options: DashboardDataRuntimeOptions): DashboardDataRuntimeHandle {
  const createStore = options.createStore ?? defaultStoreFactory;
  const storeOptions: DashboardDataStoreOptions = {
    schemas: [],
    engine: {
      wasmPath: options.wasmPath,
      computeSHA384: webCryptoSha384,
    },
  };
  const store = createStore(storeOptions, { createWorker: options.createWorker });
  const runtimePromise = store.then((engine) => createDashboardWindow({
    store: engine,
    fetch: options.fetch ?? undefined,
    baseUrl: options.baseUrl ?? '',
  }));
  let runtime: DashboardWindowRuntime | null = null;
  runtimePromise.then((value) => { runtime = value; }, () => undefined);
  const ready = runtimePromise.then(() => undefined);

  const handle: DashboardDataRuntimeHandle = {
    ready,
    store,
    ensureStandard: (code: string) => runtimePromise.then((value) => value.ensureStandard(code)),
    stateKey: (state: DashboardWindowState) => dashboardWindowStateKey(state),
    loadPage: (state: DashboardWindowState, pageOptions: DashboardWindowPageOptions) =>
      runtimePromise.then((value) => value.loadPage(state, pageOptions)),
    loadWindow: (state: DashboardWindowState, loadOptions?: DashboardWindowLoadOptions) =>
      runtimePromise.then((value) => value.loadWindow(state, loadOptions)),
    query: (sql: string, code: string, queryOptions?: LocalFlatSqlQueryOptions): Promise<LocalFlatSqlQueryResult> =>
      runtimePromise.then((value) => value.query(sql, code, queryOptions)),
    listSources: (code: string) => runtimePromise.then((value) => value.listSources(code)),
    clear: (code: string) => runtimePromise.then((value) => value.clear(code)),
    currentLoad: (code: string) => runtime?.currentLoad(code) ?? null,
    status: (code: string) => (runtime ? runtime.status(code) : encodeDashboardWindowStatus(null)),
    destroy: () => {
      runtimePromise.then((value) => value.destroy(), () => undefined);
      store.then((engine) => engine.destroy?.(), () => undefined);
      if (globalThis.SDN_DATA_WINDOW === handle) {
        globalThis.SDN_DATA_WINDOW = undefined;
      }
    },
  };

  globalThis.SDN_DATA_WINDOW = handle;
  return handle;
}

/** Read the currently published data-window global, if any. */
export function getDashboardDataRuntimeGlobal(): SDNDataWindowGlobal | undefined {
  return globalThis.SDN_DATA_WINDOW;
}

/**
 * The worker-hosted engine store (`local-flatsql-store.worker.ts` through
 * `createWorker`); without a Worker constructor and no hook — the Node/vitest
 * lane — the same engine runs in-thread.
 */
const defaultStoreFactory: DashboardDataStoreFactory = (storeOptions, deps) =>
  createWorkerLocalFlatSqlStore(storeOptions, deps);

async function webCryptoSha384(data: ArrayBuffer): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest('SHA-384', data));
}
