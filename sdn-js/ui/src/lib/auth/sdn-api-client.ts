/**
 * Typed SDN gateway/auth API client (loop task U0.3 — D1 groundwork).
 *
 * Thin `fetch` wrapper honoring the wire conventions documented in
 * SDN_SPACEAWARE_UI_LOOP.md GROUND TRUTH:
 *   - gateway JSON is a BARE top-level array (no `{"records":[...]}`
 *     envelope) — record counts travel in the `X-SDN-Record-Count` header,
 *     not the body (loop protocol rule 7);
 *   - conditional GETs use `ETag` / `If-None-Match`; a 304 body is never
 *     parsed as JSON (empty by definition);
 *   - 401s are a typed JSON body `{"code":"...","message":"..."}`
 *     (`sdn-server/internal/auth/handler.go` `errorResponse`), NEVER a
 *     redirect — `/api/**` never redirects (loop GROUND TRUTH); any
 *     unauthenticated → `/login` navigation is a client-side route decision
 *     made by the CALLER (see auth-store.ts's session guard), not this
 *     client;
 *   - auth endpoints live at the server root (`/api/auth/...`), while
 *     gateway v1 endpoints live under the configured `apiBase`
 *     (`/api/v1/...`, injected as `window.__SDN_CONFIG__.apiBase` by
 *     `injectFrontendConfig` in `cmd/spacedatanetwork/main.go`).
 *
 * Session auth is the `sdn_wallet_session` httpOnly cookie set by
 * `POST /api/auth/verify`; every request defaults to `credentials:'include'`
 * so the browser attaches it automatically — this client never reads or
 * writes the cookie itself.
 */

export interface SdnConfigWindow {
  __SDN_CONFIG__?: {
    apiBase?: string;
    serverBaseUrl?: string;
    ipfsDashboardUrl?: string;
  };
}

export interface SdnApiClientOptions {
  /** Origin the API lives on. Default: `window.location.origin`. */
  serverBaseUrl?: string;
  /** Path prefix for gateway v1 endpoints. Default: `/api/v1`. */
  apiBase?: string;
  /** Injectable for tests; default `globalThis.fetch`. */
  fetchImpl?: typeof fetch;
  /** Default `'include'` — session-cookie auth against the same-origin node. */
  credentials?: RequestCredentials;
}

/** `{"code":"...","message":"..."}` — the shape of every 4xx/5xx JSON error body. */
export interface SdnApiErrorBody {
  code: string;
  message: string;
}

/** Thrown for any non-2xx, non-304 response. Never triggers navigation. */
export class SdnApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly url: string;
  readonly body: SdnApiErrorBody | null;

  constructor(status: number, body: SdnApiErrorBody | null, url: string) {
    super(body?.message || `HTTP ${status}`);
    this.name = 'SdnApiError';
    this.status = status;
    this.code = body?.code ?? 'http_error';
    this.url = url;
    this.body = body;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  /**
   * `'api'` (default) = `apiBase`-prefixed gateway path
   * (`/api/v1/...`); `'root'` = server-root path (`/api/auth/...`,
   * `/api/node/...`).
   */
  base?: 'api' | 'root';
  /** Sent as `If-None-Match` for a conditional request. */
  ifNoneMatch?: string | null;
  headers?: Record<string, string>;
}

export interface JsonResult<T> {
  status: number;
  /** `null` only when `notModified` is true (304 — nothing to parse). */
  data: T | null;
  etag: string | null;
  notModified: boolean;
}

export interface BareArrayResult<T> {
  status: number;
  records: T[];
  /** From `X-SDN-Record-Count`; falls back to `records.length` when the header is absent. */
  recordCount: number;
  etag: string | null;
  notModified: boolean;
}

function readConfig(source: SdnConfigWindow | null | undefined): { apiBase: string; serverBaseUrl: string } {
  const cfg = source?.__SDN_CONFIG__;
  const apiBase = (cfg?.apiBase ?? '').trim();
  const serverBaseUrl = (cfg?.serverBaseUrl ?? '').trim();
  return { apiBase: apiBase || '/api/v1', serverBaseUrl };
}

export class SdnApiClient {
  private readonly serverBaseUrl: string;
  private readonly apiBase: string;
  private readonly fetchImpl: typeof fetch;
  private readonly credentials: RequestCredentials;

  constructor(options: SdnApiClientOptions = {}) {
    const globalWindow = typeof window !== 'undefined' ? (window as unknown as SdnConfigWindow) : undefined;
    const configured = readConfig(globalWindow);
    const fallbackOrigin = typeof window !== 'undefined' ? window.location.origin : '';
    this.serverBaseUrl = (options.serverBaseUrl ?? configured.serverBaseUrl ?? fallbackOrigin ?? '').replace(/\/+$/, '');
    this.apiBase = options.apiBase ?? configured.apiBase;
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
    this.credentials = options.credentials ?? 'include';
  }

  /** Generic JSON request. Never parses a body on 304. Throws `SdnApiError` for any other non-2xx status. */
  async requestJson<T>(path: string, opts: RequestOptions = {}): Promise<JsonResult<T>> {
    const resp = await this.rawRequest(path, opts);
    const etag = resp.headers.get('etag');
    const notModified = resp.status === 304;
    if (!resp.ok && !notModified) {
      throw await this.toApiError(resp);
    }
    const data = notModified ? null : ((await resp.json()) as T);
    return { status: resp.status, data, etag, notModified };
  }

  /**
   * GET expecting the gateway's BARE top-level JSON array convention
   * (loop protocol rule 7: "JSON = bare arrays"). Record count comes from
   * `X-SDN-Record-Count`, never a `{"records":[...]}` body envelope.
   */
  async requestBareArray<T>(path: string, opts: RequestOptions = {}): Promise<BareArrayResult<T>> {
    const resp = await this.rawRequest(path, opts);
    const etag = resp.headers.get('etag');
    const notModified = resp.status === 304;
    if (!resp.ok && !notModified) {
      throw await this.toApiError(resp);
    }
    const payload: unknown = notModified ? [] : await resp.json();
    const records = Array.isArray(payload) ? (payload as T[]) : [];
    const countHeader = resp.headers.get('x-sdn-record-count');
    const headerCount = countHeader ? Number.parseInt(countHeader, 10) : Number.NaN;
    return {
      status: resp.status,
      records,
      recordCount: Number.isFinite(headerCount) ? headerCount : records.length,
      etag,
      notModified,
    };
  }

  // ---- Auth surface (server root, sdn-server/internal/auth/handler.go) ----

  async authChallenge(req: AuthChallengeRequest): Promise<AuthChallengeResponse> {
    const result = await this.requestJson<AuthChallengeResponse>('/api/auth/challenge', {
      method: 'POST',
      base: 'root',
      body: req,
    });
    return requireData(result, '/api/auth/challenge');
  }

  async authVerify(req: AuthVerifyRequest): Promise<AuthVerifyResponse> {
    const result = await this.requestJson<AuthVerifyResponse>('/api/auth/verify', {
      method: 'POST',
      base: 'root',
      body: req,
    });
    return requireData(result, '/api/auth/verify');
  }

  /** Throws `SdnApiError` (status 401) when anonymous — callers treat that as "no session", never as a redirect. */
  async authMe(): Promise<AuthSessionUser> {
    const result = await this.requestJson<AuthSessionUser>('/api/auth/me', { base: 'root' });
    return requireData(result, '/api/auth/me');
  }

  async authLogout(): Promise<void> {
    await this.requestJson('/api/auth/logout', { method: 'POST', base: 'root' });
  }

  async authStatus(): Promise<AuthStatusResponse> {
    const result = await this.requestJson<AuthStatusResponse>('/api/auth/status', { base: 'root' });
    return requireData(result, '/api/auth/status');
  }

  // ---- internals ----------------------------------------------------------

  private async rawRequest(path: string, opts: RequestOptions): Promise<Response> {
    const base = opts.base === 'root' ? this.serverBaseUrl : this.serverBaseUrl + this.apiBase;
    const url = base + path;
    const headers: Record<string, string> = {
      Accept: 'application/json',
      ...(opts.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...opts.headers,
    };
    if (opts.ifNoneMatch) headers['If-None-Match'] = opts.ifNoneMatch;

    return this.fetchImpl(url, {
      method: opts.method ?? 'GET',
      headers,
      credentials: this.credentials,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    });
  }

  private async toApiError(resp: Response): Promise<SdnApiError> {
    let body: SdnApiErrorBody | null = null;
    try {
      const parsed: unknown = await resp.json();
      if (parsed && typeof parsed === 'object' && 'code' in parsed && 'message' in parsed) {
        body = parsed as SdnApiErrorBody;
      }
    } catch {
      // Non-JSON error body — leave body null; status/url still convey enough.
    }
    return new SdnApiError(resp.status, body, resp.url || '');
  }
}

function requireData<T>(result: JsonResult<T>, path: string): T {
  if (result.data === null) {
    throw new SdnApiError(result.status, null, path);
  }
  return result.data;
}

// ---- Auth wire types (sdn-server/internal/auth/handler.go) ----------------

export interface AuthChallengeRequest {
  /** Omit to look up the caller by `client_pubkey_hex` (attested/TOFU paths). */
  xpub?: string;
  client_pubkey_hex: string;
  ts: number;
}

export interface AuthChallengeResponse {
  challenge_id: string;
  /** base64 (`RawStdEncoding` — no padding), 32 random bytes to sign. */
  challenge: string;
  expires_at: number;
}

export interface AuthVerifyRequest {
  challenge_id: string;
  xpub: string;
  client_pubkey_hex: string;
  challenge: string;
  signature_hex: string;
}

/**
 * PGP-scale trust vocabulary (`sdn-server/internal/peers.TrustLevel.String()`
 * — 2026-07 web-of-trust rewrite): `never` (explicit distrust) < `unknown`
 * (no assertion) < `marginal` < `standard` (no PGP alias) < `full` < `admin`
 * (operational super-user, not a trust assertion) < `ultimate` (this node's
 * own key). Supersedes the legacy `untrusted`/`limited`/`trusted` names,
 * which the server still ACCEPTS as request input (back-compat) but never
 * emits in a response body.
 */
export type SdnTrustLevel = 'never' | 'unknown' | 'marginal' | 'standard' | 'full' | 'admin' | 'ultimate';

export interface AuthSessionUser {
  name?: string;
  trust_level: SdnTrustLevel;
}

export interface AuthVerifyResponse {
  user: AuthSessionUser;
  expires_at: number;
}

export interface AuthStatusResponse {
  admin_configured: boolean;
  users_configured: boolean;
  config_path: string;
  wallet_ui_configured: boolean;
  wallet_js_file: string;
  wallet_css_file: string;
}
