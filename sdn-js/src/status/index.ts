/**
 * `@spacedatanetwork/sdn-js/status`
 *
 * Public entry for the node status client + view model. The generated
 * FlatBuffers decoder under `./generated` is re-exported for advanced callers
 * that want to build/decode `$NST` buffers directly.
 */

export {
  createNodeStatusClient,
  computeBackoffDelay,
  deriveStatusWsUrl,
  fetchNodeStatusOnce,
  type BackoffOptions,
  type NodeStatusClient,
  type NodeStatusClientOptions,
  type NodeStatusListener,
  type WebSocketCtor,
  type WebSocketLike,
} from './client';

export {
  decodeNodeStatusSet,
  decodeNodeStatusSetRoot,
  nodeStatusSetToView,
  nodeStatusToView,
  type NodeStatusSetView,
  type NodeStatusView,
} from './view-model';

export {
  decodeDashboardStats,
  isDashboardStatsFrame,
  type DashboardSchemaStatView,
  type DashboardSourceStatView,
  type DashboardStatsView,
} from './dashboard-stats';

export { NodeStatus, NodeStatusSet } from './generated/nst.js';
export { DashboardSchemaStat, DashboardSourceStat, DashboardStatsSet } from './generated/nst.js';
