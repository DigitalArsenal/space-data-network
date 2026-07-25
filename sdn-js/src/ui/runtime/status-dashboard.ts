/**
 * Status dashboard runtime hook.
 *
 * Thin bridge that wires a {@link createNodeStatusClient} instance to a global
 * seam, `globalThis.SDN_NODE_STATUS`, which the design-built status dashboard UI
 * reads. This mirrors the `SDN_MANIFESTS` global registry pattern used by the
 * served apps: the runtime owns the data source, the design owns the rendering.
 *
 * Kept dependency-light on purpose so the `./ui` dist contract consumed by
 * OrbPro stays lean — it only pulls in the status client + view model.
 */

import {
  createNodeStatusClient,
  type NodeStatusClient,
  type NodeStatusClientOptions,
} from '../../status/client';
import type { NodeStatusSetView } from '../../status/view-model';

/** The global seam shape read by the status dashboard UI. */
export interface SDNNodeStatusGlobal {
  subscribe(cb: (view: NodeStatusSetView) => void): () => void;
  current(): NodeStatusSetView | null;
  stop(): void;
}

declare global {
  // eslint-disable-next-line no-var
  var SDN_NODE_STATUS: SDNNodeStatusGlobal | undefined;
}

/** The name of the global property the UI reads. */
export const SDN_NODE_STATUS_GLOBAL = 'SDN_NODE_STATUS' as const;

/** Handle returned by {@link startStatusDashboard}. */
export interface StatusDashboardHandle extends SDNNodeStatusGlobal {
  /** The underlying status client. */
  readonly client: NodeStatusClient;
}

/**
 * Start a status dashboard data source and publish it on
 * `globalThis.SDN_NODE_STATUS`.
 *
 * Calling `stop()` tears down the client and removes the global (only when it
 * still points at this handle, so a subsequent dashboard is never clobbered).
 */
export function startStatusDashboard(options: NodeStatusClientOptions): StatusDashboardHandle {
  const client = createNodeStatusClient(options);

  const handle: StatusDashboardHandle = {
    client,
    subscribe: (cb) => client.subscribe(cb),
    current: () => client.current(),
    stop: () => {
      client.stop();
      if (globalThis.SDN_NODE_STATUS === handle) {
        globalThis.SDN_NODE_STATUS = undefined;
      }
    },
  };

  globalThis.SDN_NODE_STATUS = handle;
  return handle;
}

/** Read the currently published status global, if any. */
export function getStatusDashboardGlobal(): SDNNodeStatusGlobal | undefined {
  return globalThis.SDN_NODE_STATUS;
}
