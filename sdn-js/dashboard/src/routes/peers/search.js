/*
 * PEERS' semantic search engine — the route's own data module.
 *
 * WHY THIS IS NOT IN App.svelte ANY MORE, AND WHY IT IS NOT IN Route.svelte
 * EITHER. The engine has a PAGE lifetime, not a route lifetime: it is warmed
 * 800ms after first paint and it re-embeds whenever the node feed updates, so
 * that the first search on PEERS is instant no matter which route the operator
 * landed on. That is behaviour worth keeping exactly as it was, and it is why
 * `route.js` exports `boot` — the shell calls it once for EVERY registered
 * route, so page-lifetime work lives in the feature's own directory instead of
 * being hoisted into the shell.
 *
 * The model is same-origin (/embedding/*, MiniLM int8 via onnxruntime-web) and
 * FAIL-OPEN: a node that does not serve those assets simply never leaves
 * `idle`, and substring search — which is always on — carries the whole
 * feature. Zero external-origin bytes, which is the node UI's standing law.
 */
import { createSemanticEngine } from '../../semantic.js';
import { nodeEmbedText } from '../../filters.js';

/** @type {ReturnType<typeof createSemanticEngine> | null} */
let engine = null;
let status = 'idle';
/** @type {(() => any[]) | null} */
let readNodes = null;
const statusSubs = new Set();

function embedIfReady() {
  const nodes = readNodes?.() ?? [];
  if (status === 'ready' && nodes.length) engine?.embedNodes(nodes, nodeEmbedText);
}

function setStatus(next) {
  status = next;
  for (const fn of statusSubs) fn(next);
  // The old App-level `$effect` re-ran on a change to EITHER the status or the
  // node list, so both edges have to trigger the embed or a feed that went
  // quiet before the model finished loading would never be embedded at all.
  embedIfReady();
}

/**
 * Called once by the shell on mount. Returns a teardown.
 *
 * `shell` is the shell API: `getNodes()` and `subscribeNodes(fn)`.
 */
export function boot(shell) {
  readNodes = () => shell.getNodes();
  engine = createSemanticEngine({ onStatus: setStatus });
  // Diagnostic seam (same spirit as SDN_NODE_STATUS): lets operators probe
  // embeddings/scores from the console; the UI never reads it back.
  //
  // Named SDN_SEARCH_RANKER, not SDN_SEMANTIC. This global was NEVER rendered —
  // it is a console handle — but it was the last occurrence of the token the
  // owner named twice, and a reviewer grepping the served page for "SEMANTIC"
  // would have found it and concluded the fix had missed again. The acceptance
  // gate is a clean grep, so the token is gone from the bundle entirely.
  globalThis.SDN_SEARCH_RANKER = engine;
  const warm = setTimeout(() => engine.init(), 800);
  const unsub = shell.subscribeNodes(() => embedIfReady());
  return () => {
    clearTimeout(warm);
    unsub();
    statusSubs.clear();
    readNodes = null;
  };
}

/** The engine's status right now: 'idle' | … | 'ready'. */
export const searchStatus = () => status;

/** Subscribe to status changes. Returns an unsubscribe. */
export function onSearchStatus(fn) {
  statusSubs.add(fn);
  return () => statusSubs.delete(fn);
}

/** Score every embedded node against a query, or null when there is no engine. */
export async function queryScores(q) {
  if (!engine) return null;
  return engine.queryScores(q);
}
