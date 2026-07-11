/**
 * Real daemon-surface wiring for the CHANNELS console view (loop task
 * U3.7). Ground truth: the `<!-- ============ CHANNELS ============ -->`
 * block in `design_handoff/sdn_console/SDN Console.dc.html` — an
 * "ENCRYPTED DATA CHANNELS" table (left, span 7: status dot / CHANNEL ·
 * RECIPIENT / STD / GRANT / ENCRYPTION) and a "CHANNEL MONITOR" card
 * (right, span 5: header + SEALED/OPEN tag, channel name, `→ recipient`
 * line, a 2×2 VISIBILITY/SUBSCRIPTION/GRANT STATE/STANDARD field grid, a
 * KEY ENVELOPE box, and an OPEN SEALED STREAM / ISSUE GRANT / ENVELOPE
 * button row). See `ChannelsView.svelte`'s doc comment for the
 * pixel-level styling port.
 *
 * Endpoints probed live on this build (NOT the mock's fabricated
 * `CHANNELS` fixture, which invents per-provider ids like
 * `mpe-screening-alpha` and a `recipient` field the server never sends):
 *
 *   1. `GET /api/v1/channels[?standardCode=CODE]` (PUBLIC, 200) —
 *      `internal/api/channels.go` `handleCollection` (line ~187).
 *      Unfiltered, this returns ONE topic-level row per supported
 *      standard code (`channelListRow`, channels.go:1617-1626):
 *      `{"standardCode","topic","visibility":"public","subscribed":false,
 *      "grantState":"not-required","encryptionState":"none"}` — no
 *      `channelId` field, because a bare standard code alone isn't a
 *      parseable `ChannelID` (`internal/channels/channels.go`
 *      `ParseChannelID`, line 33, requires a `{sourceId}-{CODE}` shape;
 *      see `TestChannelHandlerListsStandardCodesOnly`,
 *      channels_test.go:29-60). If this build ALSO has a verified dataset
 *      publication for that code (none exist on today's live node), the
 *      collection additionally appends a richer row from
 *      `channelDetail()` (channels.go:1628-1656) carrying a real
 *      `channelId`/`sourceId`/`pnmVerified`/`dpmVerified`/`pnmCid` — see
 *      `TestChannelHandlerMonitorRestoresVerifiedDatasetPublicationFromDurableLedger`,
 *      channels_test.go:255-467. `?standardCode=CODE` scopes BOTH the
 *      topic-level row and that verified-row search to just that code
 *      (`restoreVerifiedDatasetPublicationMetadataList`, channels.go:1737).
 *      `encryptionState` is a genuine two-value enum server-side
 *      (`internal/channels/metadata.go`): `"none"` or `"encrypted"` —
 *      matches `standards-data.ts`'s own documented finding for this same
 *      endpoint.
 *   2. `POST /api/v1/channels/{channelId}/grants` — `issueGrant`
 *      (channels.go:529-563), body `{"to","subject","scopes","expiresAt"}`
 *      (`channelGrantIssuePayload`, channels.go:522-527; `subject` falls
 *      back to `to` when blank). `channelId` MUST parse as `{sourceId}-
 *      {STANDARD}` (`ParseChannelID`) — a bare code like `"ACL"` 400s, so
 *      this view always builds one from a user-editable `sourceId` field
 *      (default `"local"`) + the selected row's `standardCode` (see
 *      `buildGrantChannelId`). `expiresAt`, when present, must be
 *      `time.RFC3339` (`time.Parse(time.RFC3339, ...)`, channels.go:546)
 *      or the request 400s; omitted, the grant defaults to now+24h
 *      (`ChannelGrantRegistry.Issue`, `internal/channels/grants.go:47-51`).
 *      Success is `201` with `channelGrantResponse` (channels.go:1350-1364):
 *      `{"grantId","channelId","subject","scopes":[...],
 *      "grantState":"verified","issuedAt","expiresAt"}` — confirmed by
 *      `TestChannelHandlerIssuesPrivateGrantAndAuthorizesBoundaries`,
 *      channels_test.go:724-782 (POSTing `{"to":"peer-alpha","scopes":
 *      ["subscribe","stream_open"]}` against `spaceaware-OMM/grants` with
 *      NO prior verified metadata — i.e. exactly this view's topic-row
 *      case — succeeds with `201` and `grantState:"verified"`). The route
 *      itself carries no `requireAuth` wrapper in
 *      `cmd/spacedatanetwork/main.go` (`channelAPI.RegisterRoutes(adminMux)`,
 *      unlike `/peers/connect`'s explicit `peers.Admin` gate) — so
 *      "admin session required" below is a CLIENT-SIDE policy choice
 *      (`canIssueChannelGrant`, mirroring `peers-data.ts`'s
 *      `canConnectPeers`), not something the server itself enforces on
 *      this build. Grant errors use `writeError`'s
 *      `{"error":{"message":...}}` shape (`internal/api/data.go:412-418`),
 *      which does NOT match `SdnApiError`'s `{"code","message"}` parse
 *      contract (`sdn-api-client.ts` `toApiError`) — so `err.body` is
 *      always `null` for a channels error and the real server message
 *      text (e.g. "invalid channel grant scope") is not recoverable
 *      client-side; `err.message` degrades to a generic `HTTP ${status}`
 *      string, which `issueChannelGrant` surfaces honestly rather than
 *      fabricating specifics it cannot actually read.
 *
 * SUBSCRIBE is deliberately NOT wired here (residual, per the loop task's
 * own spec): the mock's CHANNELS view has no subscribe control, and
 * `subscribed` is rendered read-only (ACTIVE / `—`).
 *
 * KEY ENVELOPE / OPEN SEALED STREAM / ENVELOPE have no real backing
 * surface for ANY channel on this build (every real channel today is
 * `visibility:"public"`, `encryptionState:"none"` — there is no private/
 * sealed channel to open a stream for or unwrap a key from, and this view
 * never calls `POST /channels/{id}/key-unwrap` or
 * `GET /channels/{id}/stream`), so both buttons stay permanently disabled
 * with an honest tooltip — never the mock's fabricated `env-mpe-alpha ·
 * sealed to assessor` envelope text.
 */

import type { SdnApiClient, SdnTrustLevel } from '../../lib/auth/sdn-api-client';
import { SdnApiError } from '../../lib/auth/sdn-api-client';
import type { AuthSessionState } from '../../lib/auth/auth-store';

// ---------------------------------------------------------------------------
// Small JSON helpers (mirrors node-data.ts/peers-data.ts/standards-data.ts's
// private helpers — not exported from there, so duplicated narrowly here,
// same rationale as those files' own doc comments).
// ---------------------------------------------------------------------------

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function pickString(record: Record<string, unknown>, key: string): string | null {
  const value = record[key];
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

function pickBoolean(record: Record<string, unknown>, key: string): boolean | null {
  const value = record[key];
  return typeof value === 'boolean' ? value : null;
}

function pickStringArray(record: Record<string, unknown>, key: string): string[] {
  const value = record[key];
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : [];
}

// ---------------------------------------------------------------------------
// Raw endpoint parser: GET /api/v1/channels[?standardCode=CODE]
// ---------------------------------------------------------------------------

export interface ChannelCollectionRow {
  standardCode: string;
  topic: string;
  /** `'public'` on every real row today; a private-visibility value has no live example yet. */
  visibility: string;
  subscribed: boolean;
  /** `'not-required' | 'required' | 'verified'` on this server (see file doc comment) — kept as a string so an unrecognized future value degrades instead of failing to typecheck. */
  grantState: string;
  /** `'none' | 'encrypted'` on this server (see file doc comment). */
  encryptionState: string;
  /** Only present on a verified per-provider row (`channelDetail()`'s shape) — `null` for the topic-level default row every standard code always has. */
  channelId: string | null;
  sourceId: string | null;
  pnmVerified: boolean | null;
  dpmVerified: boolean | null;
  pnmCid: string | null;
}

/** Parses `GET /api/v1/channels`'s `{"count":N,"results":[...]}` envelope. Entries with no `standardCode` are dropped — there is nothing to key them by. */
export function parseChannelsCollection(payload: unknown): ChannelCollectionRow[] {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.results) ? rec.results : [];
  return list
    .filter(isPlainRecord)
    .map((c) => ({
      standardCode: pickString(c, 'standardCode') ?? '',
      topic: pickString(c, 'topic') ?? '',
      visibility: pickString(c, 'visibility') ?? '',
      subscribed: c.subscribed === true,
      grantState: pickString(c, 'grantState') ?? '',
      encryptionState: pickString(c, 'encryptionState') ?? 'none',
      channelId: pickString(c, 'channelId'),
      sourceId: pickString(c, 'sourceId'),
      pnmVerified: pickBoolean(c, 'pnmVerified'),
      dpmVerified: pickBoolean(c, 'dpmVerified'),
      pnmCid: pickString(c, 'pnmCid'),
    }))
    .filter((c) => c.standardCode);
}

// ---------------------------------------------------------------------------
// Row identity / display text (honest name + recipient mapping — see file
// doc comment for why there is no real `recipient` field to read)
// ---------------------------------------------------------------------------

/** Stable row key: the real `channelId` when present, else the `standardCode` (unique among topic-level rows in an unfiltered listing). */
export function channelRowKey(row: Pick<ChannelCollectionRow, 'channelId' | 'standardCode'>): string {
  return row.channelId ?? row.standardCode;
}

/**
 * Channel display name. A verified per-provider row's real `channelId`
 * (e.g. `"celestrak-OMM"`) is shown verbatim — that IS the channel's real
 * identity. The topic-level row (no `channelId` — every code has exactly
 * one on this build) has no per-instance name at all, so it renders an
 * honest, descriptive `"{CODE} BROADCAST"` label instead of fabricating
 * a provider id the server never sent (deviation from the mock's
 * `celestrak-omm-feed`-style fabricated slug).
 */
export function channelDisplayName(row: Pick<ChannelCollectionRow, 'channelId' | 'standardCode'>): string {
  return row.channelId ?? `${row.standardCode} BROADCAST`;
}

/**
 * RECIPIENT sub-line text (without the mock's `'→ '` prefix — the view
 * adds that at render time so `ChannelMonitorView` can reuse the same
 * plain label). `visibility:'public'` is the only honest signal this
 * server exposes for "this is a broadcast, not a sealed 1:1 channel" —
 * matches the mock's own `celestrak-omm-feed` row semantics
 * (`recipient:'Broadcast'` for its one public row). Anything else has no
 * real recipient field to read, so it renders the honest unknown dash.
 */
export function channelRecipientLabel(row: Pick<ChannelCollectionRow, 'visibility'>): string {
  return row.visibility.trim().toLowerCase() === 'public' ? 'Broadcast' : '—';
}

// ---------------------------------------------------------------------------
// GRANT / ENCRYPTION / VISIBILITY label + color mapping (pure, unit-tested
// against both the real vocabulary and synthetic sealed/granted fixtures —
// see file doc comment for why "signed" is never claimed from
// encryptionState:'none')
// ---------------------------------------------------------------------------

const COLOR_GREEN = '#5ad6a0';
const COLOR_AMBER = '#ffb24d';
const COLOR_GRAY = '#7d929b';
const COLOR_CYAN = '#35c9d8';

export interface GrantLabelView {
  label: string;
  color: string;
}

/** Real `grantState` vocabulary (`not-required`/`required`/`verified` — see file doc comment) uppercased with the mock's semantic color ramp; an unrecognized value degrades to its own uppercased text in neutral gray rather than guessing. */
export function mapGrantLabel(grantState: string): GrantLabelView {
  const normalized = grantState.trim().toLowerCase();
  if (normalized === 'verified') return { label: 'VERIFIED', color: COLOR_GREEN };
  if (normalized === 'required') return { label: 'REQUIRED', color: COLOR_AMBER };
  if (normalized === 'not-required') return { label: 'NOT REQUIRED', color: COLOR_GRAY };
  return { label: normalized ? normalized.toUpperCase() : '—', color: COLOR_GRAY };
}

export interface EncryptionLabelView {
  label: string;
  glyph: string;
  color: string;
}

/**
 * Honest `encryptionState` mapping (see file doc comment): `'encrypted'`
 * (and `'sealed'`, accepted defensively even though the server only ever
 * emits `'encrypted'`) renders the mock's cyan padlock; `'none'` renders a
 * dim `'PLAINTEXT'` with NO glyph — deliberately NOT the mock's `'✓
 * SIGNED'`, since `encryptionState:'none'` only tells us the payload isn't
 * sealed to a recipient, not that it carries a verified signature.
 */
export function mapEncryptionLabel(encryptionState: string): EncryptionLabelView {
  const normalized = encryptionState.trim().toLowerCase();
  if (normalized === 'encrypted' || normalized === 'sealed') return { label: 'SEALED', glyph: '🔒', color: COLOR_CYAN };
  if (normalized === 'none') return { label: 'PLAINTEXT', glyph: '', color: COLOR_GRAY };
  return { label: normalized ? normalized.toUpperCase() : '—', glyph: '', color: COLOR_GRAY };
}

/** Port of the mock's `visGlyph()`. */
export function channelVisibilityGlyph(visibility: string): string {
  return visibility.trim().toLowerCase() === 'public' ? '◯' : '●';
}

/** Port of the mock's `visColor()`, generalized to any `private*` value (the mock only ever emitted the bare `'private'`). */
export function channelVisibilityColor(visibility: string): string {
  const v = visibility.trim().toLowerCase();
  if (v.startsWith('private')) return COLOR_CYAN;
  if (v === 'controlled') return COLOR_AMBER;
  return COLOR_GRAY;
}

// ---------------------------------------------------------------------------
// ENCRYPTED DATA CHANNELS table rows (left panel)
// ---------------------------------------------------------------------------

export interface ChannelRowView {
  key: string;
  name: string;
  recipientLabel: string;
  standardCode: string;
  grantLabel: string;
  grantColor: string;
  encryptionLabel: string;
  encryptionGlyph: string;
  encryptionColor: string;
  visibilityGlyph: string;
  visibilityColor: string;
  selected: boolean;
}

export function buildChannelRows(rows: readonly ChannelCollectionRow[], selectedKey: string | null): ChannelRowView[] {
  return rows.map((row) => {
    const grant = mapGrantLabel(row.grantState);
    const encryption = mapEncryptionLabel(row.encryptionState);
    const key = channelRowKey(row);
    return {
      key,
      name: channelDisplayName(row),
      recipientLabel: channelRecipientLabel(row),
      standardCode: row.standardCode,
      grantLabel: grant.label,
      grantColor: grant.color,
      encryptionLabel: encryption.label,
      encryptionGlyph: encryption.glyph,
      encryptionColor: encryption.color,
      visibilityGlyph: channelVisibilityGlyph(row.visibility),
      visibilityColor: channelVisibilityColor(row.visibility),
      selected: key === selectedKey,
    };
  });
}

// ---------------------------------------------------------------------------
// Empty-state label (ENCRYPTED DATA CHANNELS table body)
// ---------------------------------------------------------------------------

export const CHANNELS_LOADING_LABEL = 'LOADING CHANNELS…';
export const CHANNELS_EMPTY_LABEL = 'NO CHANNELS AVAILABLE';

export function channelsEmptyStateLabel(loaded: boolean, count: number): string {
  if (!loaded) return CHANNELS_LOADING_LABEL;
  if (count === 0) return CHANNELS_EMPTY_LABEL;
  return '';
}

// ---------------------------------------------------------------------------
// ISSUE GRANT session gate (mirrors peers-data.ts's canConnectPeers — a
// CLIENT-SIDE policy choice, not a server-enforced one; see file doc
// comment)
// ---------------------------------------------------------------------------

const ISSUE_GRANT_ALLOWED_TRUST_LEVELS: ReadonlySet<SdnTrustLevel> = new Set(['admin', 'ultimate']);

export function canIssueChannelGrant(authState: Pick<AuthSessionState, 'status' | 'user'>): boolean {
  if (authState.status !== 'authenticated' || !authState.user) return false;
  return ISSUE_GRANT_ALLOWED_TRUST_LEVELS.has(authState.user.trust_level);
}

// ---------------------------------------------------------------------------
// CHANNEL MONITOR view-model (right panel)
// ---------------------------------------------------------------------------

export const CHANNELS_NO_SEALED_STREAM_TOOLTIP =
  'No sealed native stream exists for this channel — every real channel on this build is a public, unsealed broadcast.';
export const CHANNELS_NO_ENVELOPE_TOOLTIP =
  'No recipient-sealed key envelope exists for this channel — this view does not call the key-unwrap surface.';
export const CHANNELS_ISSUE_GRANT_REQUIRES_ADMIN_TOOLTIP = 'Sign in with an admin session to issue a channel grant.';
export const CHANNELS_ISSUE_GRANT_TOOLTIP = 'Issue a scoped, time-limited grant for this channel to a recipient identity.';

export interface KeyEnvelopeView {
  glyph: string;
  color: string;
  title: string;
  meta: string;
}

export interface ChannelMonitorView {
  name: string;
  recipientLabel: string;
  headerTagLabel: string;
  headerTagGlyph: string;
  headerTagColor: string;
  visibilityLabel: string;
  visibilityColor: string;
  subscriptionLabel: string;
  subscriptionColor: string;
  grantLabel: string;
  grantColor: string;
  standardCode: string;
  keyEnvelope: KeyEnvelopeView;
  streamButtonLabel: string;
  streamButtonEnabled: boolean;
  streamButtonTooltip: string;
  envelopeButtonEnabled: boolean;
  envelopeButtonTooltip: string;
  issueGrantEnabled: boolean;
  issueGrantTooltip: string;
}

/**
 * CHANNEL MONITOR card. `row` is the selected channel's richest known row
 * (prefer the verified per-provider row from `fetchChannelDetailRow` when
 * one exists, else the base topic-level listing row — both are the same
 * `ChannelCollectionRow` shape). `canIssueGrant` comes from
 * `canIssueChannelGrant`. OPEN SEALED STREAM and ENVELOPE are always
 * disabled (see file doc comment) — never enabled by `row`'s own state,
 * since neither underlying surface is wired from this view regardless of
 * a channel's encryption/visibility.
 */
export function buildMonitorView(row: ChannelCollectionRow | null, canIssueGrant: boolean): ChannelMonitorView | null {
  if (!row) return null;
  const encryption = mapEncryptionLabel(row.encryptionState);
  const grant = mapGrantLabel(row.grantState);
  const sealed = encryption.label === 'SEALED';
  return {
    name: channelDisplayName(row),
    recipientLabel: channelRecipientLabel(row),
    headerTagLabel: encryption.label,
    headerTagGlyph: encryption.glyph,
    headerTagColor: encryption.color,
    visibilityLabel: row.visibility ? row.visibility.toUpperCase() : '—',
    visibilityColor: channelVisibilityColor(row.visibility),
    subscriptionLabel: row.subscribed ? 'ACTIVE' : '—',
    subscriptionColor: row.subscribed ? COLOR_GREEN : COLOR_GRAY,
    grantLabel: grant.label,
    grantColor: grant.color,
    standardCode: row.standardCode,
    keyEnvelope: sealed
      ? {
          glyph: '🔒',
          color: COLOR_CYAN,
          title: 'NO KEY ENVELOPE AVAILABLE',
          meta: 'sealed channel · wrapped-key-unwrap is not called from this view',
        }
      : {
          glyph: '—',
          color: COLOR_GRAY,
          title: 'NO KEY ENVELOPE',
          meta: 'public channel · payloads are not sealed to a recipient',
        },
    streamButtonLabel: 'OPEN SEALED STREAM',
    streamButtonEnabled: false,
    streamButtonTooltip: CHANNELS_NO_SEALED_STREAM_TOOLTIP,
    envelopeButtonEnabled: false,
    envelopeButtonTooltip: CHANNELS_NO_ENVELOPE_TOOLTIP,
    issueGrantEnabled: canIssueGrant,
    issueGrantTooltip: canIssueGrant ? CHANNELS_ISSUE_GRANT_TOOLTIP : CHANNELS_ISSUE_GRANT_REQUIRES_ADMIN_TOOLTIP,
  };
}

// ---------------------------------------------------------------------------
// ISSUE GRANT form — request building (channel id, scopes, RFC3339 expiry)
// ---------------------------------------------------------------------------

/** `{sourceId}-{standardCode}` — the real `ParseChannelID`-shaped id every grant is issued against (see file doc comment). Trims and defaults a blank `sourceId` to `'local'`. */
export function buildGrantChannelId(sourceId: string, standardCode: string): string {
  const trimmed = sourceId.trim();
  return `${trimmed || 'local'}-${standardCode}`;
}

/** Splits free-text scopes on commas and/or whitespace, trims, drops blanks, and lowercases (the server's `AccessBoundary` vocabulary — `internal/channels/access.go`'s `Boundary*` constants — is all lowercase snake_case). Never throws; `''` yields `[]`, which the server (`parseGrantScopes(nil)`) treats as "use the default private-grant scope set" — see file doc comment. */
export function parseGrantScopesInput(raw: string): string[] {
  return raw
    .split(/[,\s]+/)
    .map((s) => s.trim().toLowerCase())
    .filter((s) => s.length > 0);
}

const RFC3339_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;

export type ExpiresAtValidation = { ok: true; value: string | null } | { ok: false; error: string };

export const CHANNELS_INVALID_EXPIRY_MESSAGE = 'Expiry must be RFC3339 (e.g. 2026-08-01T00:00:00Z) — leave blank to default to 24 hours.';

/**
 * Validates the optional expiry field against `time.RFC3339`
 * (`internal/api/channels.go:546` — `time.Parse(time.RFC3339,
 * expiresAt)`, a strict profile that rejects bare dates, missing
 * seconds, and missing UTC offsets). An empty/blank input is valid and
 * means "omit the field" (server defaults to now+24h — see file doc
 * comment), NOT "invalid".
 */
export function validateExpiresAt(raw: string): ExpiresAtValidation {
  const trimmed = raw.trim();
  if (!trimmed) return { ok: true, value: null };
  if (!RFC3339_PATTERN.test(trimmed) || Number.isNaN(Date.parse(trimmed))) {
    return { ok: false, error: CHANNELS_INVALID_EXPIRY_MESSAGE };
  }
  return { ok: true, value: trimmed };
}

export const CHANNELS_GRANT_TO_REQUIRED_MESSAGE = 'A recipient (subject) is required to issue a grant.';

/** Client-side mirror of the server's `subject is required` rejection (`internal/channels/grants.go:40-42`) — checked before any network call so a blank recipient never round-trips for nothing. */
export function validateGrantTo(to: string): string | null {
  return to.trim() ? null : CHANNELS_GRANT_TO_REQUIRED_MESSAGE;
}

// ---------------------------------------------------------------------------
// Fetch orchestration — takes the shared SdnApiClient (see
// `../../lib/auth/sdn-api-client.ts`). Every function here swallows its own
// fetch failure (never throws) except `issueChannelGrant`, which instead
// resolves an honest `{ok:false,error}` — matching `connectToPeer`'s
// contract in `peers-data.ts`.
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type ChannelsApiClient = Pick<SdnApiClient, 'requestJson'>;

/** Fetches the full (unfiltered) `/api/v1/channels` collection. Never rejects — an offline/unreachable node resolves to `[]`, which `channelsEmptyStateLabel` renders as an honest empty state. */
export async function loadChannelsCollection(apiClient: ChannelsApiClient): Promise<ChannelCollectionRow[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/channels');
    return parseChannelsCollection(result.data);
  } catch {
    return [];
  }
}

/**
 * Refetches `/api/v1/channels?standardCode=CODE` for the selected row and
 * returns the RICHEST row for that code: a verified per-provider row
 * (real `channelId`) when one exists, else the plain topic-level row.
 * Falls back to `fallback` (the row already on hand from the unfiltered
 * listing) on any fetch failure or an empty result — this always resolves
 * to a usable row, never `null`, so `ChannelsView.svelte` never loses its
 * selection because of a transient refetch failure.
 */
export async function fetchChannelDetailRow(
  apiClient: ChannelsApiClient,
  standardCode: string,
  fallback: ChannelCollectionRow,
): Promise<ChannelCollectionRow> {
  try {
    const result = await apiClient.requestJson<unknown>(`/channels?standardCode=${encodeURIComponent(standardCode)}`);
    const rows = parseChannelsCollection(result.data);
    const verified = rows.find((r) => r.channelId !== null);
    return verified ?? rows[0] ?? fallback;
  } catch {
    return fallback;
  }
}

export interface ChannelGrantResponse {
  grantId: string;
  channelId: string;
  subject: string;
  scopes: string[];
  grantState: string;
  issuedAt: string;
  expiresAt: string;
}

/** Parses `channelGrantResponse` (channels.go:1350-1364). `null` for a malformed/incomplete payload — a `grantId`+`channelId` pair is the minimum needed to consider the grant real. */
function parseGrantResponse(payload: unknown): ChannelGrantResponse | null {
  const rec = isPlainRecord(payload) ? payload : null;
  if (!rec) return null;
  const grantId = pickString(rec, 'grantId');
  const channelId = pickString(rec, 'channelId');
  if (!grantId || !channelId) return null;
  return {
    grantId,
    channelId,
    subject: pickString(rec, 'subject') ?? '',
    scopes: pickStringArray(rec, 'scopes'),
    grantState: pickString(rec, 'grantState') ?? '',
    issuedAt: pickString(rec, 'issuedAt') ?? '',
    expiresAt: pickString(rec, 'expiresAt') ?? '',
  };
}

export interface IssueChannelGrantParams {
  sourceId: string;
  standardCode: string;
  to: string;
  scopesRaw: string;
  expiresAtRaw: string;
}

export interface IssueChannelGrantResult {
  ok: boolean;
  grant: ChannelGrantResponse | null;
  /** Honest, human-readable error text for an inline (no-toast-library) failure message. `null` on success. */
  error: string | null;
}

/**
 * Validates + POSTs `/api/v1/channels/{sourceId}-{standardCode}/grants`.
 * Client-side validation (`validateGrantTo`/`validateExpiresAt`) runs
 * BEFORE any network call, mirroring the server's own two rejection
 * reasons so a doomed request never round-trips. Never throws — every
 * failure (validation, `SdnApiError`, or a generic network throw)
 * resolves `{ok:false,error}` (see file doc comment for why the error
 * text degrades to a generic `HTTP ${status}` string rather than the
 * server's real message).
 */
export async function issueChannelGrant(
  apiClient: ChannelsApiClient,
  params: IssueChannelGrantParams,
): Promise<IssueChannelGrantResult> {
  const toError = validateGrantTo(params.to);
  if (toError) return { ok: false, grant: null, error: toError };
  const expiry = validateExpiresAt(params.expiresAtRaw);
  if (!expiry.ok) return { ok: false, grant: null, error: expiry.error };

  const channelId = buildGrantChannelId(params.sourceId, params.standardCode);
  const scopes = parseGrantScopesInput(params.scopesRaw);
  const body: { to: string; scopes: string[]; expiresAt?: string } = { to: params.to.trim(), scopes };
  if (expiry.value) body.expiresAt = expiry.value;

  try {
    const result = await apiClient.requestJson<unknown>(`/channels/${encodeURIComponent(channelId)}/grants`, {
      method: 'POST',
      body,
    });
    const grant = parseGrantResponse(result.data);
    if (!grant) return { ok: false, grant: null, error: 'Grant request succeeded but the response was malformed.' };
    return { ok: true, grant, error: null };
  } catch (err) {
    if (err instanceof SdnApiError) {
      return { ok: false, grant: null, error: err.body?.message || err.message || `Grant request failed (HTTP ${err.status}).` };
    }
    return { ok: false, grant: null, error: 'Grant request failed — network error.' };
  }
}
