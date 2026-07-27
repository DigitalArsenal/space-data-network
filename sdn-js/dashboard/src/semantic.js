/*
 * In-browser semantic search over the node table — MAIN-THREAD FACADE.
 *
 * The model, the tokenizer and every embedding now live in a Web Worker
 * (semantic.worker.js). This module only posts messages, so the dashboard
 * paints and stays interactive while the engine warms up. Before this split
 * the ONNX session creation plus the corpus pass produced ONE 9.2-second
 * long task ~1s after navigation — the page painted at ~200 ms and then
 * froze solid (measured live on sdn.spaceaware.io).
 *
 * The public contract is unchanged, including fail-open: status goes
 * 'idle' → 'loading' → 'ready' | 'unavailable', and anything that fails
 * (missing /embedding/* assets, no worker support, wasm init, inference)
 * lands permanently on 'unavailable' while the dashboard keeps its always-on
 * substring search. "Warming up" is simply the pre-'ready' state — there is
 * no separate spinner contract to honour.
 *
 * Vite inlines the worker as a blob: URL (`?worker&inline`), which is why the
 * dashboard CSP carries `worker-src 'self' blob:`; the single-file law holds
 * because no second file is served.
 */

import SemanticWorker from './semantic.worker.js?worker&inline';

// Re-exported so tests (and anything else) keep importing the primitives
// from here even though they now live in the runtime-agnostic core.
export { textHash, wordpieceTokenize, cosine, l2normalize } from './semantic-core.js';

/**
 * Create the lazy engine. status: 'idle' → 'loading' → 'ready' | 'unavailable'.
 */
export function createSemanticEngine({ onStatus = () => {} } = {}) {
  let status = 'idle';
  let lastError = '';
  let worker = null;
  let seq = 0;
  const pending = new Map(); // seq → resolve

  const setStatus = (s) => {
    status = s;
    onStatus(s);
  };

  const fail = (e) => {
    if (e?.message && !lastError) lastError = e.message;
    if (status !== 'unavailable') setStatus('unavailable');
    for (const resolve of pending.values()) resolve(new Map());
    pending.clear();
    try {
      worker?.terminate();
    } catch {
      /* already gone */
    }
    worker = null;
  };

  function spawn() {
    if (worker) return worker;
    try {
      worker = new SemanticWorker();
    } catch {
      fail();
      return null;
    }
    worker.onerror = fail;
    worker.onmessageerror = fail;
    worker.onmessage = (e) => {
      const msg = e.data ?? {};
      if (msg.type === 'status') {
        if (msg.reason) lastError = msg.reason;
        setStatus(msg.status);
      } else if (msg.type === 'scores') {
        const resolve = pending.get(msg.seq);
        if (resolve) {
          pending.delete(msg.seq);
          resolve(new Map(msg.entries ?? []));
        }
      }
    };
    return worker;
  }

  async function init() {
    if (status !== 'idle') return status;
    const w = spawn();
    if (!w) return status;
    setStatus('loading');
    // The worker runs from a blob:, so it cannot resolve root-relative asset
    // paths on its own — hand it this page's origin.
    w.postMessage({ type: 'init', origin: globalThis.location?.origin ?? '' });
    return status;
  }

  /**
   * Ensure every node's embedding is current in the worker's cache.
   * `textOf` runs HERE (a function cannot cross postMessage) — it is pure
   * string building and costs nothing; the model work stays in the worker.
   */
  function embedNodes(nodes, textOf) {
    if (status !== 'ready' || !worker) return;
    const items = [];
    for (const node of nodes) items.push({ id: node.peerId, text: textOf(node) });
    worker.postMessage({ type: 'corpus', items });
  }

  /** peerId → cosine(query, node) for all cached nodes. */
  function queryScores(query) {
    if (status !== 'ready' || !worker) return Promise.resolve(new Map());
    seq += 1;
    const id = seq;
    return new Promise((resolve) => {
      pending.set(id, resolve);
      worker.postMessage({ type: 'query', seq: id, text: query });
    });
  }

  return {
    get status() {
      return status;
    },
    /** Diagnostic only — why the engine gave up. Never drives UI. */
    get lastError() {
      return lastError;
    },
    init,
    embedNodes,
    queryScores,
    stop: fail,
  };
}
