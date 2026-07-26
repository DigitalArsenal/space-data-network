/*
 * In-browser semantic search over the node table (spec: graph task
 * nst-dashboard-table deliverable 4).
 *
 * Sentence embeddings from a MiniLM-class int8 ONNX model executed by
 * onnxruntime-web's WASM backend. CSP-clean and same-origin only: the ort
 * runtime JS is bundled into the single-file page; the model, vocab and ort
 * .wasm/.mjs artifacts are fetched from the node's /embedding/* static
 * surface (config embedding.assets_dir — staged like the geoip mmdb,
 * fail-open). If the assets are absent the engine reports 'unavailable' and
 * the dashboard silently keeps its always-on substring search.
 *
 * Tokenizer: BERT WordPiece (uncased) implemented inline against
 * /embedding/vocab.txt — greedy longest-match with ## continuations,
 * [CLS]/[SEP] framing, 128-token cap. Pooling: attention-masked mean over
 * last_hidden_state, L2-normalized → cosine via dot product.
 */

import * as ort from 'onnxruntime-web/wasm';

const ASSET_BASE = '/embedding/';
const MODEL_URL = `${ASSET_BASE}model.onnx`;
const VOCAB_URL = `${ASSET_BASE}vocab.txt`;
const MAX_TOKENS = 128;

/** djb2 — cheap change-detection hash for per-node embedding cache keys. */
export function textHash(s) {
  let h = 5381;
  for (let i = 0; i < s.length; i += 1) h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  return h;
}

/** BERT-uncased basic + WordPiece tokenization. Exported for tests. */
export function wordpieceTokenize(text, vocab) {
  const clean = text
    .toLowerCase()
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '') // strip accents
    .replace(/([\p{P}\p{S}])/gu, ' $1 ') // isolate punctuation/symbols
    .trim();
  const ids = [];
  for (const word of clean.split(/\s+/)) {
    if (!word) continue;
    let start = 0;
    const sub = [];
    let bad = false;
    while (start < word.length) {
      let end = word.length;
      let id = -1;
      while (start < end) {
        const piece = (start > 0 ? '##' : '') + word.slice(start, end);
        const found = vocab.get(piece);
        if (found !== undefined) {
          id = found;
          break;
        }
        end -= 1;
      }
      if (id < 0) {
        bad = true;
        break;
      }
      sub.push(id);
      start = end;
    }
    if (bad) ids.push(vocab.get('[UNK]') ?? 100);
    else ids.push(...sub);
  }
  return ids;
}

function l2normalize(vec) {
  let sum = 0;
  for (let i = 0; i < vec.length; i += 1) sum += vec[i] * vec[i];
  const inv = sum > 0 ? 1 / Math.sqrt(sum) : 0;
  for (let i = 0; i < vec.length; i += 1) vec[i] *= inv;
  return vec;
}

/** Cosine of two L2-normalized vectors = dot product. */
export function cosine(a, b) {
  let dot = 0;
  for (let i = 0; i < a.length; i += 1) dot += a[i] * b[i];
  return dot;
}

/**
 * Create the lazy engine. status: 'idle' → 'loading' → 'ready' | 'unavailable'.
 * All failures (missing assets, wasm init, inference) permanently fail-open
 * to 'unavailable'; callers keep substring search.
 */
export function createSemanticEngine({ onStatus = () => {} } = {}) {
  let status = 'idle';
  let session = null;
  let vocab = null;
  const nodeCache = new Map(); // peerId → {hash, vec}
  const queryCache = new Map(); // query → vec (bounded)

  const setStatus = (s) => {
    status = s;
    onStatus(s);
  };

  async function init() {
    if (status !== 'idle') return status;
    setStatus('loading');
    try {
      const [vocabRes, modelHead] = await Promise.all([
        fetch(VOCAB_URL),
        fetch(MODEL_URL, { method: 'HEAD' }),
      ]);
      if (!vocabRes.ok || !modelHead.ok) throw new Error('embedding assets absent');
      const words = (await vocabRes.text()).split(/\r?\n/);
      vocab = new Map();
      words.forEach((w, i) => {
        if (w) vocab.set(w, i);
      });
      // Same-origin ort artifacts; single-threaded (page has no COOP/COEP).
      ort.env.wasm.wasmPaths = ASSET_BASE;
      ort.env.wasm.numThreads = 1;
      session = await ort.InferenceSession.create(MODEL_URL, {
        executionProviders: ['wasm'],
      });
      setStatus('ready');
    } catch {
      session = null;
      vocab = null;
      setStatus('unavailable');
    }
    return status;
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

  /** Ensure every node's embedding is cached & current; yields between runs. */
  async function embedNodes(nodes, textOf) {
    if (status !== 'ready') return;
    for (const node of nodes) {
      const text = textOf(node);
      const hash = textHash(text);
      const cached = nodeCache.get(node.peerId);
      if (cached && cached.hash === hash) continue;
      const vec = await embed(text);
      if (vec) nodeCache.set(node.peerId, { hash, vec });
    }
  }

  /** peerId → cosine(query, node) for all cached nodes. */
  async function queryScores(query) {
    if (status !== 'ready') return new Map();
    let qvec = queryCache.get(query);
    if (!qvec) {
      qvec = await embed(query);
      if (!qvec) return new Map();
      if (queryCache.size > 64) queryCache.clear();
      queryCache.set(query, qvec);
    }
    const scores = new Map();
    for (const [peerId, { vec }] of nodeCache) scores.set(peerId, cosine(qvec, vec));
    return scores;
  }

  return {
    get status() {
      return status;
    },
    init,
    embed,
    embedNodes,
    queryScores,
  };
}
