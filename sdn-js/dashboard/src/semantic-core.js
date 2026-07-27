/*
 * Semantic-search primitives — pure, runtime-agnostic.
 *
 * Split out of semantic.js so the SAME code runs inside the Web Worker that
 * now owns the model (see semantic.worker.js) and inside the unit tests,
 * with no DOM and no onnxruntime import in the pure path.
 */

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

export function l2normalize(vec) {
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
 * Where the node serves the model, vocab and ort runtime (same-origin).
 *
 * These MUST be absolutised against the page origin before use inside the
 * Web Worker: the worker is created from a `blob:` URL, so its base URL is
 * the blob, and a root-relative path like `/embedding/vocab.txt` fails to
 * parse there ("Failed to parse URL"). The main thread posts its origin with
 * the init message and the worker builds absolute URLs from it — including
 * `ort.env.wasm.wasmPaths`, which ort resolves the same way.
 */
export const ASSET_PATH = '/embedding/';
export const MAX_TOKENS = 128;

/** @param {string} origin e.g. "https://sdn.spaceaware.io" */
export function assetUrls(origin) {
  const base = `${String(origin ?? '').replace(/\/$/, '')}${ASSET_PATH}`;
  return { base, model: `${base}model.onnx`, vocab: `${base}vocab.txt` };
}
