/**
 * Provider-credential console data layer.
 *
 * Pure, presentation-free logic for the PROVIDER CREDENTIALS widget, mirroring
 * the node-data.ts convention: this module builds the strings and states the
 * view renders, and it never fabricates a value it does not have.
 *
 * WRITE-ONLY. The daemon exposes credential STATUS only — configured or not, a
 * masked username, timestamps. There is no endpoint that returns a stored
 * secret, so there is nothing here that could display one. The password field
 * in the view is never pre-filled.
 */

import type { SdnApiClient } from '../../lib/auth/sdn-api-client';
import { SdnApiError } from '../../lib/auth/sdn-api-client';

/** Shown when the daemon refuses the credential surface (403). */
export const CREDENTIAL_FORBIDDEN_MESSAGE = 'ADMIN SIGN-IN REQUIRED TO MANAGE CREDENTIALS';

/**
 * Shown on 503 — the daemon refuses to serve the credential routes because node
 * authentication is disabled. It fails closed rather than exposing a
 * credential-entry endpoint anonymously.
 */
export const CREDENTIAL_UNAVAILABLE_MESSAGE =
  'CREDENTIAL MANAGEMENT IS DISABLED WHILE NODE AUTHENTICATION IS OFF';

/** Generic write failure. */
export const CREDENTIAL_SAVE_FAILED_MESSAGE = 'COULD NOT SAVE CREDENTIAL';

export interface CredentialLane {
  id: string;
  label: string;
  /** What the provider calls the account field, shown as the username hint. */
  usernameLabel: string;
  /** Why the node needs it — one short line, operator-facing. */
  purpose: string;
}

/**
 * The credential lanes the node knows about. Space-Track is the live consumer;
 * the others are declared lanes with no wired consumer yet.
 */
export const CREDENTIAL_LANES: readonly CredentialLane[] = [
  {
    id: 'spacetrack',
    label: 'SPACE-TRACK',
    usernameLabel: 'IDENTITY',
    purpose: 'Ephemeris and catalog ingest',
  },
  {
    id: 'edc_cpf',
    label: 'EDC / DGFI CPF',
    usernameLabel: 'USERNAME',
    purpose: 'Consolidated prediction files',
  },
  {
    id: 'myintelsat',
    label: 'MYINTELSAT',
    usernameLabel: 'USERNAME',
    purpose: 'Operator ephemeris exchange',
  },
];

/** Mirrors the daemon's secrets.Status. Carries no secret. */
export interface CredentialStatus {
  id: string;
  configured: boolean;
  usernameMasked: string | null;
  updatedAt: string | null;
  verifiedAt: string | null;
}

export type CredentialState = 'not-configured' | 'verified' | 'unverified';

export interface CredentialRow {
  lane: CredentialLane;
  status: CredentialStatus;
  state: CredentialState;
  /** SCREAMING-CASE status line for the widget. */
  stateLabel: string;
  /**
   * Dot color. Per the console's honesty rule (node-data.ts
   * serviceStatusDotColor): green ONLY on a confirmed successful probe; neutral
   * gray otherwise. A stored-but-never-probed credential is UNKNOWN, not bad —
   * it must never render as a fabricated red/alert.
   */
  dotColor: string;
}

const DOT_VERIFIED = '#3ddc84';
const DOT_NEUTRAL = '#6b7280';

export function parseCredentialStatus(payload: unknown): CredentialStatus | null {
  if (!payload || typeof payload !== 'object') return null;
  const raw = payload as Record<string, unknown>;
  const id = typeof raw.id === 'string' ? raw.id : '';
  if (!id) return null;
  return {
    id,
    configured: raw.configured === true,
    usernameMasked: typeof raw.username_masked === 'string' ? raw.username_masked : null,
    updatedAt: typeof raw.updated_at === 'string' ? raw.updated_at : null,
    verifiedAt: typeof raw.verified_at === 'string' ? raw.verified_at : null,
  };
}

export function parseCredentialList(payload: unknown): CredentialStatus[] {
  if (!payload || typeof payload !== 'object') return [];
  const list = (payload as Record<string, unknown>).credentials;
  if (!Array.isArray(list)) return [];
  return list.map(parseCredentialStatus).filter((s): s is CredentialStatus => s !== null);
}

function emptyStatus(id: string): CredentialStatus {
  return { id, configured: false, usernameMasked: null, updatedAt: null, verifiedAt: null };
}

export function deriveCredentialState(status: CredentialStatus): CredentialState {
  if (!status.configured) return 'not-configured';
  return status.verifiedAt ? 'verified' : 'unverified';
}

export function credentialStateLabel(state: CredentialState): string {
  switch (state) {
    case 'verified':
      return 'CONFIGURED · VERIFIED';
    case 'unverified':
      // Deliberately not "INVALID": we have not probed it, so we do not know.
      return 'CONFIGURED · NOT VERIFIED';
    default:
      return 'NOT CONFIGURED';
  }
}

export function credentialDotColor(state: CredentialState): string {
  return state === 'verified' ? DOT_VERIFIED : DOT_NEUTRAL;
}

/** Joins the lane catalog with whatever statuses the daemon reported. */
export function buildCredentialRows(statuses: readonly CredentialStatus[]): CredentialRow[] {
  const byId = new Map(statuses.map((s) => [s.id, s]));
  return CREDENTIAL_LANES.map((lane) => {
    const status = byId.get(lane.id) ?? emptyStatus(lane.id);
    const state = deriveCredentialState(status);
    return {
      lane,
      status,
      state,
      stateLabel: credentialStateLabel(state),
      dotColor: credentialDotColor(state),
    };
  });
}

export interface CredentialLoadResult {
  rows: CredentialRow[];
  /** Non-null when the surface is unavailable (auth off, or not an admin). */
  notice: string | null;
}

/**
 * Loads credential status. Never throws — the widget degrades to a notice, in
 * line with loadNodeDashboardData's "resolves even with the daemon offline".
 */
export async function loadCredentialStatuses(api: SdnApiClient): Promise<CredentialLoadResult> {
  try {
    const result = await api.requestJson<unknown>('/admin/credentials');
    return { rows: buildCredentialRows(parseCredentialList(result.data)), notice: null };
  } catch (err) {
    return { rows: buildCredentialRows([]), notice: noticeForError(err) };
  }
}

export function noticeForError(err: unknown): string {
  if (err instanceof SdnApiError) {
    if (err.status === 503) return CREDENTIAL_UNAVAILABLE_MESSAGE;
    if (err.status === 401 || err.status === 403) return CREDENTIAL_FORBIDDEN_MESSAGE;
  }
  return CREDENTIAL_SAVE_FAILED_MESSAGE;
}

export type VerificationOutcome = 'verified' | 'unverified' | 'failed';

export interface CredentialSaveResult {
  ok: boolean;
  status: CredentialStatus | null;
  verification: VerificationOutcome | null;
  /** Operator-facing message; never contains the submitted secret. */
  message: string;
}

/**
 * Saves (or replaces) a credential. The secret travels once, in the request
 * body, and is never read back.
 */
export async function saveCredential(
  api: SdnApiClient,
  id: string,
  username: string,
  secret: string,
  verify: boolean,
): Promise<CredentialSaveResult> {
  try {
    const result = await api.requestJson<Record<string, unknown>>(`/admin/credentials/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: { username, secret, verify },
    });
    const data = result.data ?? {};
    const status = parseCredentialStatus(data.status);
    const verification = (data.verification as VerificationOutcome | undefined) ?? null;
    const verificationError = typeof data.verification_error === 'string' ? data.verification_error : '';

    let message = 'SAVED';
    if (verification === 'verified') message = 'SAVED · VERIFIED';
    else if (verification === 'failed') message = `SAVED · NOT VERIFIED — ${verificationError || 'PROBE FAILED'}`;
    else if (verify) message = 'SAVED · NOT VERIFIED';

    return { ok: true, status, verification, message };
  } catch (err) {
    return { ok: false, status: null, verification: null, message: noticeForError(err) };
  }
}

export async function clearCredential(api: SdnApiClient, id: string): Promise<CredentialSaveResult> {
  try {
    const result = await api.requestJson<unknown>(`/admin/credentials/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
    return {
      ok: true,
      status: parseCredentialStatus(result.data),
      verification: null,
      message: 'CLEARED',
    };
  } catch (err) {
    return { ok: false, status: null, verification: null, message: noticeForError(err) };
  }
}
