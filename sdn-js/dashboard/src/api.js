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
 * One operator-facing sentence per contract failure code. The node answers
 * every verification failure with the SINGLE opaque code
 * `authentication_failed` (contract §4) — never try to say more than it does.
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
    default:
      return err.message || `Request failed (HTTP ${err.status}).`;
  }
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
