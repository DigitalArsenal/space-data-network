import { describe, expect, it, vi } from 'vitest';

import { SdnApiError } from '../../lib/auth/sdn-api-client';
import type { SdnApiClient } from '../../lib/auth/sdn-api-client';
import {
  CREDENTIAL_FORBIDDEN_MESSAGE,
  CREDENTIAL_UNAVAILABLE_MESSAGE,
  buildCredentialRows,
  clearCredential,
  credentialDotColor,
  credentialStateLabel,
  deriveCredentialState,
  loadCredentialStatuses,
  parseCredentialList,
  parseCredentialStatus,
  saveCredential,
} from './credentials-data';

const SECRET = 'PLAINTEXT-CANARY-ui-9931';

function fakeClient(impl: Partial<SdnApiClient> & { requestJson: unknown }): SdnApiClient {
  return impl as unknown as SdnApiClient;
}

describe('parseCredentialStatus', () => {
  it('parses the daemon status shape', () => {
    const s = parseCredentialStatus({
      id: 'spacetrack',
      configured: true,
      username_masked: 'o***@example.com',
      updated_at: '2026-07-14T00:00:00Z',
      verified_at: '2026-07-14T00:01:00Z',
    });
    expect(s).toEqual({
      id: 'spacetrack',
      configured: true,
      usernameMasked: 'o***@example.com',
      updatedAt: '2026-07-14T00:00:00Z',
      verifiedAt: '2026-07-14T00:01:00Z',
    });
  });

  it('rejects junk without throwing', () => {
    expect(parseCredentialStatus(null)).toBeNull();
    expect(parseCredentialStatus({})).toBeNull();
    expect(parseCredentialStatus('nope')).toBeNull();
    expect(parseCredentialList(null)).toEqual([]);
    expect(parseCredentialList({ credentials: 'no' })).toEqual([]);
  });
});

describe('credential state (console honesty rule)', () => {
  const base = { id: 'spacetrack', usernameMasked: null, updatedAt: null, verifiedAt: null };

  it('an unconfigured lane is NOT CONFIGURED and neutral', () => {
    const state = deriveCredentialState({ ...base, configured: false });
    expect(state).toBe('not-configured');
    expect(credentialStateLabel(state)).toBe('NOT CONFIGURED');
    expect(credentialDotColor(state)).toBe('#6b7280');
  });

  it('a stored-but-never-probed credential is UNKNOWN, never a fabricated failure', () => {
    const state = deriveCredentialState({ ...base, configured: true });
    expect(state).toBe('unverified');
    // Must not claim the credential is bad — we simply have not probed it.
    expect(credentialStateLabel(state)).toBe('CONFIGURED · NOT VERIFIED');
    expect(credentialStateLabel(state)).not.toMatch(/INVALID|FAILED|BAD/);
    // Neutral gray, not red.
    expect(credentialDotColor(state)).toBe('#6b7280');
  });

  it('green only after a confirmed probe', () => {
    const state = deriveCredentialState({ ...base, configured: true, verifiedAt: '2026-07-14T00:00:00Z' });
    expect(state).toBe('verified');
    expect(credentialDotColor(state)).toBe('#3ddc84');
  });
});

describe('buildCredentialRows', () => {
  it('always lists every known lane, defaulting to not-configured', () => {
    const rows = buildCredentialRows([]);
    expect(rows.map((r) => r.lane.id)).toEqual(['spacetrack', 'edc_cpf', 'myintelsat']);
    expect(rows.every((r) => r.state === 'not-configured')).toBe(true);
  });

  it('joins reported status onto the lane catalog', () => {
    const rows = buildCredentialRows([
      {
        id: 'spacetrack',
        configured: true,
        usernameMasked: 'o***@example.com',
        updatedAt: null,
        verifiedAt: '2026-07-14T00:00:00Z',
      },
    ]);
    const st = rows.find((r) => r.lane.id === 'spacetrack');
    expect(st?.state).toBe('verified');
    expect(st?.status.usernameMasked).toBe('o***@example.com');
    // Other lanes stay unconfigured.
    expect(rows.find((r) => r.lane.id === 'edc_cpf')?.state).toBe('not-configured');
  });
});

describe('loadCredentialStatuses', () => {
  it('maps 503 to the fail-closed notice (auth disabled)', async () => {
    const api = fakeClient({
      requestJson: vi.fn().mockRejectedValue(new SdnApiError(503, null, '/admin/credentials')),
    });
    const result = await loadCredentialStatuses(api);
    expect(result.notice).toBe(CREDENTIAL_UNAVAILABLE_MESSAGE);
    // Still renders the lane list rather than crashing.
    expect(result.rows).toHaveLength(3);
  });

  it('maps 401/403 to the sign-in notice', async () => {
    for (const status of [401, 403]) {
      const api = fakeClient({
        requestJson: vi.fn().mockRejectedValue(new SdnApiError(status, null, '/admin/credentials')),
      });
      const result = await loadCredentialStatuses(api);
      expect(result.notice).toBe(CREDENTIAL_FORBIDDEN_MESSAGE);
    }
  });

  it('never throws when the daemon is offline', async () => {
    const api = fakeClient({ requestJson: vi.fn().mockRejectedValue(new Error('network down')) });
    await expect(loadCredentialStatuses(api)).resolves.toBeDefined();
  });
});

describe('saveCredential', () => {
  it('sends the secret exactly once and surfaces the verification outcome', async () => {
    const requestJson = vi.fn().mockResolvedValue({
      status: 200,
      data: {
        status: { id: 'spacetrack', configured: true, username_masked: 'o***@example.com' },
        verification: 'verified',
      },
      etag: null,
      notModified: false,
    });
    const api = fakeClient({ requestJson });

    const result = await saveCredential(api, 'spacetrack', 'operator@example.com', SECRET, true);

    expect(result.ok).toBe(true);
    expect(result.verification).toBe('verified');
    expect(result.message).toBe('SAVED · VERIFIED');

    expect(requestJson).toHaveBeenCalledTimes(1);
    const [path, opts] = requestJson.mock.calls[0];
    expect(path).toBe('/admin/credentials/spacetrack');
    expect(opts.method).toBe('PUT');
    expect(opts.body).toEqual({ username: 'operator@example.com', secret: SECRET, verify: true });
  });

  it('reports saved-but-unverified on a failed probe without leaking the secret', async () => {
    const api = fakeClient({
      requestJson: vi.fn().mockResolvedValue({
        status: 200,
        data: {
          status: { id: 'spacetrack', configured: true },
          verification: 'failed',
          verification_error: 'Space-Track rejected the credential',
        },
        etag: null,
        notModified: false,
      }),
    });

    const result = await saveCredential(api, 'spacetrack', 'operator@example.com', SECRET, true);
    expect(result.ok).toBe(true);
    expect(result.verification).toBe('failed');
    expect(result.message).toContain('NOT VERIFIED');
    // The operator-facing message must never echo the submitted secret.
    expect(result.message).not.toContain(SECRET);
  });

  it('surfaces the fail-closed notice when the daemon refuses (auth off)', async () => {
    const api = fakeClient({
      requestJson: vi.fn().mockRejectedValue(new SdnApiError(503, null, '/admin/credentials')),
    });
    const result = await saveCredential(api, 'spacetrack', 'u@e.com', SECRET, false);
    expect(result.ok).toBe(false);
    expect(result.message).toBe(CREDENTIAL_UNAVAILABLE_MESSAGE);
    expect(result.message).not.toContain(SECRET);
  });
});

describe('clearCredential', () => {
  it('DELETEs the lane', async () => {
    const requestJson = vi.fn().mockResolvedValue({
      status: 200,
      data: { id: 'spacetrack', configured: false },
      etag: null,
      notModified: false,
    });
    const api = fakeClient({ requestJson });

    const result = await clearCredential(api, 'spacetrack');
    expect(result.ok).toBe(true);
    expect(result.message).toBe('CLEARED');
    const [path, opts] = requestJson.mock.calls[0];
    expect(path).toBe('/admin/credentials/spacetrack');
    expect(opts.method).toBe('DELETE');
  });
});

// The UI layer has no code path that could render a stored secret, because the
// daemon has no route that returns one. This pins that: a (hypothetical) status
// payload carrying a secret field must never survive parsing into the view model.
describe('write-only invariant', () => {
  it('drops any secret-looking field the daemon might ever send', () => {
    const parsed = parseCredentialStatus({
      id: 'spacetrack',
      configured: true,
      username_masked: 'o***@example.com',
      secret: SECRET,
      password: SECRET,
    });
    expect(JSON.stringify(parsed)).not.toContain(SECRET);
    const rows = buildCredentialRows([parsed!]);
    expect(JSON.stringify(rows)).not.toContain(SECRET);
  });
});
