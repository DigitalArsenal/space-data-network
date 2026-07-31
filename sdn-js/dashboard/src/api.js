/*
 * Same-origin JSON transport for the dashboard's authenticated surfaces
 * (graph task nst-node-edit-permissions-ui; contract:
 * graph/tasks/nst-node-admin-contract.md "## Contract (final)").
 *
 * Every call is same-origin with the HttpOnly session cookie
 * (credentials: 'same-origin' — the token is never readable, never cached,
 * and is silently rotated by the node). Every MUTATING call carries
 * `X-Requested-With: XMLHttpRequest` so adminSecurityMiddleware's CSRF check
 * can never trip (contract §6/§9.3).
 *
 * Two error shapes exist on the node and both are normalized here:
 *   · internal/auth + gates      → JSON  {"code":"forbidden","message":"…"}
 *   · internal/peers (http.Error) → text/plain body, status only
 * Callers see one ApiError {status, code, message}.
 */

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code || '';
  }
}

/** True when the failure means "no session" (vs. "session, wrong tier"). */
export const isUnauthorized = (err) => err instanceof ApiError && err.status === 401;
/** True when the failure means "authenticated but below the required tier". */
export const isForbidden = (err) => err instanceof ApiError && err.status === 403;

/**
 * Statuses that mean "nothing on the other end answered": the node is down or
 * restarting, or an edge in front of it could not reach it. 52x are
 * Cloudflare's origin-side codes and 521 is the one the owner was shown
 * verbatim in a modal error banner on 2026-07-30.
 */
const NO_ANSWER = new Set([502, 503, 504, 521, 522, 523, 524, 525, 526, 530]);

/**
 * `ApiError` synthesizes `HTTP <status>` as its message when the node sent
 * neither a code nor a body (constructor above), so "the message the node
 * actually said" has to exclude that synthetic one — otherwise the fallback
 * below renders the transport code as the sentence, which is the defect.
 */
const BARE_STATUS = /^HTTP\s*\d{3}\.?$/;
const nodeSaid = (err) => {
  const said = String(err?.message ?? '').trim();
  return BARE_STATUS.test(said) ? '' : said;
};

/**
 * One operator-facing sentence per contract failure code. The node answers
 * every verification failure with the SINGLE opaque code
 * `authentication_failed` (contract §4) — never try to say more than it does.
 *
 * NOTHING HERE MAY RETURN A BARE STATUS (IRIS ruling 2026-07-30 §2). The owner
 * was shown `HTTP 521` as the whole explanation of a failed peer-trust write.
 * A status code is a fact about the transport; the operator needs the fact
 * about their action, and above all whether anything changed.
 */
export function describeApiError(err) {
  if (!(err instanceof ApiError)) return String(err?.message ?? err ?? 'request failed');
  switch (err.code) {
    case 'authentication_failed':
      return 'Sign-in failed. Check the recovery phrase, the account index, and that this key is registered on this node.';
    case 'attestation_failed':
      return 'Attestation could not be verified against the key on file for that xpub.';
    case 'too_many_requests':
      return 'Rate limited by the node. Wait a minute and try again.';
    case 'invalid_timestamp':
      return 'This browser clock is more than 2 minutes off the node clock.';
    case 'unauthorized':
      return 'Not signed in.';
    case 'forbidden':
      return 'This session does not have the required trust level.';
    case 'user_exists':
      return 'That xpub is already registered on this node.';
    case 'invalid_trust_level':
      return 'That trust level cannot be assigned through this API.';
    // --- the pin registry (GET/POST /api/peers/pins, DELETE …/pins/{id}) ---
    // A pin is what keeps a never-yet-seen peer in the table at all (owner
    // ruling 2026-07-30), so every refusal has to be a sentence an operator can
    // act on — a silent failure here reads as "the button does nothing".
    case 'invalid_peer':
      return 'That is not a peer id or a multiaddr this node can parse.';
    case 'already_pinned':
      return 'That peer is already pinned on this node.';
    case 'not_pinned':
      return 'That peer is not pinned, so there was nothing to remove.';
    case 'config_pin':
      return "This pin comes from the node's config file. It can only be removed there — this page cannot unpin it.";
    default:
      break;
  }
  // No code, or one this page does not name: fall back to the STATUS, said in
  // words. `internal/peers` answers with http.Error (a text body and a status,
  // no code at all), and an edge in front of the node answers with neither.
  if (err.status === 0) return 'Could not reach the node.';
  if (NO_ANSWER.has(err.status)) {
    return 'The node did not answer. It may be restarting, or the connection to it is down. Nothing was changed.';
  }
  if (err.status >= 500) return 'The node answered with an error. Nothing was changed.';
  return nodeSaid(err) || 'The node refused that request.';
}

/**
 * Core fetch. Returns parsed JSON (or null for 204 / empty bodies).
 * @param {string} path same-origin path, always starting with '/'
 */
export async function apiFetch(path, { method = 'GET', body, accept = 'application/json' } = {}) {
  const headers = { Accept: accept };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (MUTATING.has(method)) headers['X-Requested-With'] = 'XMLHttpRequest';

  let res;
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (cause) {
    throw new ApiError(0, 'network_error', 'Could not reach the node.');
  }

  if (res.status === 204) return null;
  const text = await res.text();
  let parsed = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, parsed?.code ?? '', parsed?.message ?? text.trim());
  }
  return parsed;
}

/** GET a text surface (vCard). Returns '' when absent — never throws on 404. */
export async function apiText(path) {
  try {
    const res = await fetch(path, { credentials: 'same-origin' });
    if (!res.ok) return '';
    return await res.text();
  } catch {
    return '';
  }
}

/**
 * GET a text surface AND SAY HOW THE READ WENT. `apiText` collapses every
 * failure to '', which is why the peer modal could not tell "this node never
 * asked" from "the peer did not answer" — two facts with opposite meanings for
 * a contact card (IRIS ruling 2026-07-30 §3). Never throws.
 *
 * @returns {Promise<{ok: boolean, status: number, text: string, threw: boolean}>}
 */
export async function apiTextResult(path) {
  try {
    const res = await fetch(path, { credentials: 'same-origin' });
    return { ok: res.ok, status: res.status, text: res.ok ? await res.text() : '', threw: false };
  } catch {
    return { ok: false, status: 0, text: '', threw: true };
  }
}

/**
 * POST a raw text body (POST /api/peers/import/vcard takes text/vcard, not
 * JSON — contract §8). Same cookie + CSRF discipline as apiFetch.
 */
export async function apiPostText(path, text, contentType = 'text/vcard') {
  let res;
  try {
    res = await fetch(path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': contentType, 'X-Requested-With': 'XMLHttpRequest' },
      body: text,
    });
  } catch {
    throw new ApiError(0, 'network_error', 'Could not reach the node.');
  }
  const body = await res.text();
  let parsed = null;
  try {
    parsed = JSON.parse(body);
  } catch {
    parsed = null;
  }
  if (!res.ok) throw new ApiError(res.status, parsed?.code ?? '', parsed?.message ?? body.trim());
  return parsed;
}

/**
 * PUT a JSON body to an endpoint whose success response is BINARY
 * (PUT /api/node/epm answers application/x-flatbuffers — contract §6).
 * The bytes are discarded; the caller re-reads JSON from /api/node/epm/json.
 */
export async function apiPutExpectBinary(path, body) {
  const res = await fetch(path, {
    method: 'PUT',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify(body),
  }).catch(() => {
    throw new ApiError(0, 'network_error', 'Could not reach the node.');
  });
  if (res.ok) return true;
  const text = await res.text();
  let parsed = null;
  try {
    parsed = JSON.parse(text);
  } catch {
    /* http.Error bodies are plain text */
  }
  throw new ApiError(res.status, parsed?.code ?? '', parsed?.message ?? text.trim());
}
