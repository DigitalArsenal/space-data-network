/*
 * The semantic engine, OFF the main thread.
 *
 * Defect this fixes (measured live on sdn.spaceaware.io): a single 9,214 ms
 * long task starting ~1,085 ms after navigation. The page painted at ~200 ms
 * and then froze solid for over nine seconds — onnxruntime-web's session
 * creation for the int8 MiniLM model plus the whole corpus embedding ran
 * synchronously on the main thread, because ort's WASM backend blocks
 * whichever thread it is on (numThreads = 1, no COOP/COEP on this page).
 *
 * Nothing about the ENGINE changed; only where it runs. The main thread now
 * does postMessage and nothing else, so the freeze becomes background work.
 *
 * Same-origin only: ort's runtime, the model and the vocab all come from the
 * node's own /embedding/* surface, exactly as before. The worker itself is
 * spawned from a blob: URL built from inline-bundled source — that is what
 * `worker-src 'self' blob:` in the dashboard CSP is for, and it keeps the
 * single-file law intact (no second served file).
 *
 * Fail-open is unchanged and absolute: any failure reports 'unavailable' and
 * the dashboard keeps its always-on substring search.
 */

import * as ort from 'onnxruntime-web/wasm';
import {
  MAX_TOKENS,
  assetUrls,
  cosine,
  l2normalize,
  textHash,
  wordpieceTokenize,
} from './semantic-core.js';

let status = 'idle';
let session = null;
let vocab = null;
const nodeCache = new Map(); // id → {hash, vec}
const queryCache = new Map(); // text → vec (bounded)

const post = (msg) => self.postMessage(msg);
const setStatus = (s, reason = '') => {
  status = s;
  post({ type: 'status', status: s, reason });
};

async function init(origin) {
  if (status !== 'idle') return;
  setStatus('loading');
  try {
    // Absolute, because this worker's base URL is a blob: (see semantic-core).
    const urls = assetUrls(origin);
    const [vocabRes, modelHead] = await Promise.all([
      fetch(urls.vocab),
      fetch(urls.model, { method: 'HEAD' }),
    ]);
    if (!vocabRes.ok || !modelHead.ok) throw new Error('embedding assets absent');
    const words = (await vocabRes.text()).split(/\r?\n/);
    vocab = new Map();
    words.forEach((w, i) => {
      if (w) vocab.set(w, i);
    });
    // Same-origin ort artifacts; single-threaded (the page has no COOP/COEP).
    // Blocking here is now harmless — this is not the main thread.
    ort.env.wasm.wasmPaths = urls.base;
    ort.env.wasm.numThreads = 1;
    session = await ort.InferenceSession.create(urls.model, { executionProviders: ['wasm'] });
    setStatus('ready');
  } catch (err) {
    session = null;
    vocab = null;
    // Fail-open is unchanged; the reason rides along purely as a diagnostic
    // (SDN_SEMANTIC.lastError) so an absent-asset node and a broken runtime
    // are distinguishable without reading the worker's own console.
    setStatus('unavailable', String(err?.message ?? err));
  }
}

async function embed(text) {
  if (status !== 'ready') return null;
  const tokens = wordpieceTokenize(text, vocab).slice(0, MAX_TOKENS - 2);
  const cls = vocab.get('[CLS]') ?? 101;
  const sep = vocab.get('[SEP]') ?? 102;
  const ids = [cls, ...tokens, sep];
  const n = ids.length;
  const inputIds = new BigInt64Array(n);
  const mask = new BigInt64Array(n);
  const types = new BigInt64Array(n);
  for (let i = 0; i < n; i += 1) {
    inputIds[i] = BigInt(ids[i]);
    mask[i] = 1n;
    types[i] = 0n;
  }
  const feeds = {
    input_ids: new ort.Tensor('int64', inputIds, [1, n]),
    attention_mask: new ort.Tensor('int64', mask, [1, n]),
  };
  if (session.inputNames.includes('token_type_ids')) {
    feeds.token_type_ids = new ort.Tensor('int64', types, [1, n]);
  }
  const out = await session.run(feeds);
  const hidden = out[session.outputNames[0]];
  const [, seq, dim] = hidden.dims;
  const data = hidden.data;
  const vec = new Float32Array(dim);
  for (let t = 0; t < seq; t += 1) {
    for (let d = 0; d < dim; d += 1) vec[d] += data[t * dim + d];
  }
  for (let d = 0; d < dim; d += 1) vec[d] /= seq;
  return l2normalize(vec);
}

async function embedCorpus(items) {
  if (status !== 'ready') return;
  for (const { id, text } of items) {
    const hash = textHash(text);
    const cached = nodeCache.get(id);
    if (cached && cached.hash === hash) continue;
    const vec = await embed(text);
    if (vec) nodeCache.set(id, { hash, vec });
  }
  post({ type: 'corpus', size: nodeCache.size });
}

async function queryScores(seq, text) {
  const entries = [];
  if (status === 'ready') {
    let qvec = queryCache.get(text);
    if (!qvec) {
      qvec = await embed(text);
      if (qvec) {
        if (queryCache.size > 64) queryCache.clear();
        queryCache.set(text, qvec);
      }
    }
    if (qvec) {
      for (const [id, { vec }] of nodeCache) entries.push([id, cosine(qvec, vec)]);
    }
  }
  post({ type: 'scores', seq, entries });
}

self.onmessage = (e) => {
  const msg = e.data ?? {};
  if (msg.type === 'init') init(msg.origin);
  else if (msg.type === 'corpus') embedCorpus(msg.items ?? []);
  else if (msg.type === 'query') queryScores(msg.seq, msg.text);
};
