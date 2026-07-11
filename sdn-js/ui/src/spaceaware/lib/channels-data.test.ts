import { describe, expect, it } from 'vitest';
import { SdnApiError, type RequestOptions } from '../../lib/auth/sdn-api-client';
import type { AuthSessionState } from '../../lib/auth/auth-store';
import {
  CHANNELS_EMPTY_LABEL,
  CHANNELS_GRANT_TO_REQUIRED_MESSAGE,
  CHANNELS_INVALID_EXPIRY_MESSAGE,
  CHANNELS_ISSUE_GRANT_REQUIRES_ADMIN_TOOLTIP,
  CHANNELS_ISSUE_GRANT_TOOLTIP,
  CHANNELS_LOADING_LABEL,
  CHANNELS_NO_ENVELOPE_TOOLTIP,
  CHANNELS_NO_SEALED_STREAM_TOOLTIP,
  buildChannelRows,
  buildGrantChannelId,
  buildMonitorView,
  canIssueChannelGrant,
  channelDisplayName,
  channelRecipientLabel,
  channelRowKey,
  channelVisibilityColor,
  channelVisibilityGlyph,
  channelsEmptyStateLabel,
  fetchChannelDetailRow,
  issueChannelGrant,
  loadChannelsCollection,
  mapEncryptionLabel,
  mapGrantLabel,
  parseChannelsCollection,
  parseGrantScopesInput,
  validateExpiresAt,
  validateGrantTo,
  type ChannelCollectionRow,
  type ChannelsApiClient,
} from './channels-data';

function row(overrides: Partial<ChannelCollectionRow> = {}): ChannelCollectionRow {
  return {
    standardCode: 'OMM',
    topic: '/spacedatanetwork/channels/OMM',
    visibility: 'public',
    subscribed: false,
    grantState: 'not-required',
    encryptionState: 'none',
    channelId: null,
    sourceId: null,
    pnmVerified: null,
    dpmVerified: null,
    pnmCid: null,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// parseChannelsCollection
// ---------------------------------------------------------------------------

describe('parseChannelsCollection', () => {
  it('parses the real /api/v1/channels topic-level row shape', () => {
    const result = parseChannelsCollection({
      count: 1,
      results: [
        {
          standardCode: 'OMM',
          topic: '/spacedatanetwork/channels/OMM',
          visibility: 'public',
          subscribed: false,
          grantState: 'not-required',
          encryptionState: 'none',
        },
      ],
    });
    expect(result).toEqual([row()]);
  });

  it('parses a verified per-provider row carrying channelId/sourceId/pnmVerified/dpmVerified/pnmCid', () => {
    const result = parseChannelsCollection({
      results: [
        {
          channelId: 'celestrak-OMM',
          sourceId: 'celestrak',
          standardCode: 'OMM',
          topic: '/spacedatanetwork/channels/OMM',
          visibility: 'public',
          subscribed: true,
          pnmVerified: true,
          dpmVerified: true,
          pnmCid: 'bafkpnm-restored',
          grantState: 'not-required',
          encryptionState: 'none',
        },
      ],
    });
    expect(result).toEqual([
      row({
        channelId: 'celestrak-OMM',
        sourceId: 'celestrak',
        subscribed: true,
        pnmVerified: true,
        dpmVerified: true,
        pnmCid: 'bafkpnm-restored',
      }),
    ]);
  });

  it('drops entries with no standardCode', () => {
    const result = parseChannelsCollection({ results: [{ topic: 'x' }, { standardCode: 'ACL' }] });
    expect(result).toHaveLength(1);
    expect(result[0]?.standardCode).toBe('ACL');
  });

  it('defaults missing fields honestly (visibility/grantState blank, encryptionState "none", subscribed false)', () => {
    const result = parseChannelsCollection({ results: [{ standardCode: 'ACL' }] });
    expect(result).toEqual([
      row({ standardCode: 'ACL', topic: '', visibility: '', grantState: '', encryptionState: 'none', subscribed: false }),
    ]);
  });

  it('treats a non-boolean subscribed as false', () => {
    const result = parseChannelsCollection({ results: [{ standardCode: 'ACL', subscribed: 'yes' }] });
    expect(result[0]?.subscribed).toBe(false);
  });

  it('degrades to [] for a non-object payload', () => {
    expect(parseChannelsCollection(null)).toEqual([]);
    expect(parseChannelsCollection('nope')).toEqual([]);
  });

  it('degrades to [] when results is missing or not an array', () => {
    expect(parseChannelsCollection({})).toEqual([]);
    expect(parseChannelsCollection({ results: 'nope' })).toEqual([]);
  });

  it('filters out non-object entries in results', () => {
    const result = parseChannelsCollection({ results: [null, 'x', 42, { standardCode: 'ACL' }] });
    expect(result).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// mapGrantLabel
// ---------------------------------------------------------------------------

describe('mapGrantLabel', () => {
  it('maps not-required to a neutral gray label', () => {
    expect(mapGrantLabel('not-required')).toEqual({ label: 'NOT REQUIRED', color: '#7d929b' });
  });

  it('maps required to amber', () => {
    expect(mapGrantLabel('required')).toEqual({ label: 'REQUIRED', color: '#ffb24d' });
  });

  it('maps verified to green', () => {
    expect(mapGrantLabel('verified')).toEqual({ label: 'VERIFIED', color: '#5ad6a0' });
  });

  it('is case-insensitive and trims whitespace', () => {
    expect(mapGrantLabel('  VeRiFiEd  ')).toEqual({ label: 'VERIFIED', color: '#5ad6a0' });
  });

  it('degrades an unrecognized value to its own uppercased text in neutral gray', () => {
    expect(mapGrantLabel('mystery-state')).toEqual({ label: 'MYSTERY-STATE', color: '#7d929b' });
  });

  it('renders a blank grantState as an honest dash', () => {
    expect(mapGrantLabel('')).toEqual({ label: '—', color: '#7d929b' });
  });
});

// ---------------------------------------------------------------------------
// mapEncryptionLabel
// ---------------------------------------------------------------------------

describe('mapEncryptionLabel', () => {
  it('maps "none" to a dim PLAINTEXT label with no glyph (never claims signed)', () => {
    expect(mapEncryptionLabel('none')).toEqual({ label: 'PLAINTEXT', glyph: '', color: '#7d929b' });
  });

  it('maps "encrypted" (the real server value) to a cyan SEALED padlock', () => {
    expect(mapEncryptionLabel('encrypted')).toEqual({ label: 'SEALED', glyph: '🔒', color: '#35c9d8' });
  });

  it('also accepts "sealed" defensively', () => {
    expect(mapEncryptionLabel('sealed')).toEqual({ label: 'SEALED', glyph: '🔒', color: '#35c9d8' });
  });

  it('is case-insensitive and trims whitespace', () => {
    expect(mapEncryptionLabel('  ENCRYPTED  ')).toEqual({ label: 'SEALED', glyph: '🔒', color: '#35c9d8' });
  });

  it('degrades an unrecognized value honestly', () => {
    expect(mapEncryptionLabel('quantum')).toEqual({ label: 'QUANTUM', glyph: '', color: '#7d929b' });
  });

  it('renders a blank encryptionState as an honest dash', () => {
    expect(mapEncryptionLabel('')).toEqual({ label: '—', glyph: '', color: '#7d929b' });
  });
});

// ---------------------------------------------------------------------------
// channelVisibilityGlyph / channelVisibilityColor
// ---------------------------------------------------------------------------

describe('channelVisibilityGlyph', () => {
  it('renders public as a hollow ring', () => {
    expect(channelVisibilityGlyph('public')).toBe('◯');
  });

  it('renders anything else as a filled dot', () => {
    expect(channelVisibilityGlyph('private')).toBe('●');
    expect(channelVisibilityGlyph('')).toBe('●');
  });
});

describe('channelVisibilityColor', () => {
  it('renders public as neutral gray', () => {
    expect(channelVisibilityColor('public')).toBe('#7d929b');
  });

  it('renders any private* value as cyan', () => {
    expect(channelVisibilityColor('private')).toBe('#35c9d8');
    expect(channelVisibilityColor('private-listed')).toBe('#35c9d8');
    expect(channelVisibilityColor('private-hidden')).toBe('#35c9d8');
  });

  it('renders controlled as amber', () => {
    expect(channelVisibilityColor('controlled')).toBe('#ffb24d');
  });
});

// ---------------------------------------------------------------------------
// channelRowKey / channelDisplayName / channelRecipientLabel
// ---------------------------------------------------------------------------

describe('channelRowKey', () => {
  it('uses the real channelId when present', () => {
    expect(channelRowKey(row({ channelId: 'celestrak-OMM' }))).toBe('celestrak-OMM');
  });

  it('falls back to standardCode for a topic-level row', () => {
    expect(channelRowKey(row())).toBe('OMM');
  });
});

describe('channelDisplayName', () => {
  it('uses the real channelId verbatim when present', () => {
    expect(channelDisplayName(row({ channelId: 'celestrak-OMM' }))).toBe('celestrak-OMM');
  });

  it('renders an honest "{CODE} BROADCAST" label for a topic-level row', () => {
    expect(channelDisplayName(row({ standardCode: 'ACL' }))).toBe('ACL BROADCAST');
  });
});

describe('channelRecipientLabel', () => {
  it('renders Broadcast for a public channel', () => {
    expect(channelRecipientLabel(row({ visibility: 'public' }))).toBe('Broadcast');
  });

  it('renders an honest dash for a non-public channel', () => {
    expect(channelRecipientLabel(row({ visibility: 'private' }))).toBe('—');
  });

  it('renders an honest dash for a blank visibility', () => {
    expect(channelRecipientLabel(row({ visibility: '' }))).toBe('—');
  });
});

// ---------------------------------------------------------------------------
// buildChannelRows
// ---------------------------------------------------------------------------

describe('buildChannelRows', () => {
  it('maps every row and marks the selected one', () => {
    const rows = [row({ standardCode: 'ACL' }), row({ standardCode: 'OMM', channelId: 'celestrak-OMM' })];
    const views = buildChannelRows(rows, 'celestrak-OMM');
    expect(views).toHaveLength(2);
    expect(views[0]).toMatchObject({ key: 'ACL', name: 'ACL BROADCAST', selected: false });
    expect(views[1]).toMatchObject({ key: 'celestrak-OMM', name: 'celestrak-OMM', selected: true });
  });

  it('marks nothing selected when selectedKey matches no row', () => {
    const views = buildChannelRows([row({ standardCode: 'ACL' })], 'nope');
    expect(views[0]?.selected).toBe(false);
  });

  it('carries grant/encryption/visibility colors through from the mapping functions', () => {
    const [view] = buildChannelRows([row({ grantState: 'verified', encryptionState: 'encrypted', visibility: 'private' })], null);
    expect(view).toMatchObject({
      grantLabel: 'VERIFIED',
      grantColor: '#5ad6a0',
      encryptionLabel: 'SEALED',
      encryptionGlyph: '🔒',
      encryptionColor: '#35c9d8',
      visibilityGlyph: '●',
      visibilityColor: '#35c9d8',
    });
  });
});

// ---------------------------------------------------------------------------
// channelsEmptyStateLabel
// ---------------------------------------------------------------------------

describe('channelsEmptyStateLabel', () => {
  it('shows the loading label before the first load resolves', () => {
    expect(channelsEmptyStateLabel(false, 0)).toBe(CHANNELS_LOADING_LABEL);
  });

  it('shows the empty label once loaded with zero rows', () => {
    expect(channelsEmptyStateLabel(true, 0)).toBe(CHANNELS_EMPTY_LABEL);
  });

  it('shows nothing once loaded with rows', () => {
    expect(channelsEmptyStateLabel(true, 5)).toBe('');
  });
});

// ---------------------------------------------------------------------------
// canIssueChannelGrant
// ---------------------------------------------------------------------------

describe('canIssueChannelGrant', () => {
  function authState(overrides: Partial<AuthSessionState>): Pick<AuthSessionState, 'status' | 'user'> {
    return { status: 'anonymous', user: null, ...overrides };
  }

  it('is false for an anonymous session', () => {
    expect(canIssueChannelGrant(authState({ status: 'anonymous', user: null }))).toBe(false);
  });

  it('is false for an authenticated session below admin trust', () => {
    expect(canIssueChannelGrant(authState({ status: 'authenticated', user: { trust_level: 'standard' } }))).toBe(false);
  });

  it('is true for an authenticated admin session', () => {
    expect(canIssueChannelGrant(authState({ status: 'authenticated', user: { trust_level: 'admin' } }))).toBe(true);
  });

  it('is true for an authenticated ultimate (this node\'s own key) session', () => {
    expect(canIssueChannelGrant(authState({ status: 'authenticated', user: { trust_level: 'ultimate' } }))).toBe(true);
  });

  it('is false while a session is still hydrating', () => {
    expect(canIssueChannelGrant(authState({ status: 'unknown', user: null }))).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// buildMonitorView
// ---------------------------------------------------------------------------

describe('buildMonitorView', () => {
  it('returns null when nothing is selected', () => {
    expect(buildMonitorView(null, true)).toBeNull();
  });

  it('renders an honest empty KEY ENVELOPE + disabled stream/envelope buttons for a real public plaintext row', () => {
    const view = buildMonitorView(row(), true);
    expect(view).toMatchObject({
      name: 'OMM BROADCAST',
      recipientLabel: 'Broadcast',
      headerTagLabel: 'PLAINTEXT',
      headerTagGlyph: '',
      visibilityLabel: 'PUBLIC',
      subscriptionLabel: '—',
      subscriptionColor: '#7d929b',
      grantLabel: 'NOT REQUIRED',
      standardCode: 'OMM',
      streamButtonLabel: 'OPEN SEALED STREAM',
      streamButtonEnabled: false,
      streamButtonTooltip: CHANNELS_NO_SEALED_STREAM_TOOLTIP,
      envelopeButtonEnabled: false,
      envelopeButtonTooltip: CHANNELS_NO_ENVELOPE_TOOLTIP,
    });
    expect(view?.keyEnvelope).toEqual({
      glyph: '—',
      color: '#7d929b',
      title: 'NO KEY ENVELOPE',
      meta: 'public channel · payloads are not sealed to a recipient',
    });
  });

  it('renders ACTIVE/green for a subscribed row', () => {
    const view = buildMonitorView(row({ subscribed: true }), true);
    expect(view).toMatchObject({ subscriptionLabel: 'ACTIVE', subscriptionColor: '#5ad6a0' });
  });

  it('renders a sealed-channel KEY ENVELOPE state for a synthetic encrypted+verified fixture (still honestly empty — no envelope surface wired)', () => {
    const view = buildMonitorView(row({ encryptionState: 'encrypted', grantState: 'verified', visibility: 'private' }), true);
    expect(view?.headerTagLabel).toBe('SEALED');
    expect(view?.headerTagGlyph).toBe('🔒');
    expect(view?.grantLabel).toBe('VERIFIED');
    expect(view?.keyEnvelope).toEqual({
      glyph: '🔒',
      color: '#35c9d8',
      title: 'NO KEY ENVELOPE AVAILABLE',
      meta: 'sealed channel · wrapped-key-unwrap is not called from this view',
    });
    // Even a sealed/granted synthetic fixture never enables the stream/envelope buttons.
    expect(view?.streamButtonEnabled).toBe(false);
    expect(view?.envelopeButtonEnabled).toBe(false);
  });

  it('gates ISSUE GRANT on the canIssueGrant param, with an honest tooltip either way', () => {
    const enabled = buildMonitorView(row(), true);
    expect(enabled).toMatchObject({ issueGrantEnabled: true, issueGrantTooltip: CHANNELS_ISSUE_GRANT_TOOLTIP });
    const disabled = buildMonitorView(row(), false);
    expect(disabled).toMatchObject({ issueGrantEnabled: false, issueGrantTooltip: CHANNELS_ISSUE_GRANT_REQUIRES_ADMIN_TOOLTIP });
  });
});

// ---------------------------------------------------------------------------
// buildGrantChannelId
// ---------------------------------------------------------------------------

describe('buildGrantChannelId', () => {
  it('builds {sourceId}-{standardCode}', () => {
    expect(buildGrantChannelId('local', 'OMM')).toBe('local-OMM');
  });

  it('trims a padded sourceId', () => {
    expect(buildGrantChannelId('  celestrak  ', 'OMM')).toBe('celestrak-OMM');
  });

  it('defaults a blank sourceId to "local"', () => {
    expect(buildGrantChannelId('', 'OMM')).toBe('local-OMM');
    expect(buildGrantChannelId('   ', 'OMM')).toBe('local-OMM');
  });
});

// ---------------------------------------------------------------------------
// parseGrantScopesInput
// ---------------------------------------------------------------------------

describe('parseGrantScopesInput', () => {
  it('splits on commas', () => {
    expect(parseGrantScopesInput('subscribe,stream_open')).toEqual(['subscribe', 'stream_open']);
  });

  it('splits on whitespace', () => {
    expect(parseGrantScopesInput('subscribe stream_open')).toEqual(['subscribe', 'stream_open']);
  });

  it('splits on a mix of commas and whitespace, trimming each token', () => {
    expect(parseGrantScopesInput('subscribe, stream_open ,  key_unwrap')).toEqual(['subscribe', 'stream_open', 'key_unwrap']);
  });

  it('lowercases tokens to match the server\'s snake_case boundary vocabulary', () => {
    expect(parseGrantScopesInput('SUBSCRIBE')).toEqual(['subscribe']);
  });

  it('returns [] for a blank input', () => {
    expect(parseGrantScopesInput('')).toEqual([]);
    expect(parseGrantScopesInput('   ')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// validateExpiresAt
// ---------------------------------------------------------------------------

describe('validateExpiresAt', () => {
  it('accepts a blank input as "omit the field"', () => {
    expect(validateExpiresAt('')).toEqual({ ok: true, value: null });
    expect(validateExpiresAt('   ')).toEqual({ ok: true, value: null });
  });

  it('accepts an RFC3339 UTC (Z) timestamp', () => {
    expect(validateExpiresAt('2026-08-01T00:00:00Z')).toEqual({ ok: true, value: '2026-08-01T00:00:00Z' });
  });

  it('accepts an RFC3339 timestamp with a numeric offset', () => {
    expect(validateExpiresAt('2026-08-01T00:00:00-07:00')).toEqual({ ok: true, value: '2026-08-01T00:00:00-07:00' });
  });

  it('accepts fractional seconds', () => {
    expect(validateExpiresAt('2026-08-01T00:00:00.123Z')).toEqual({ ok: true, value: '2026-08-01T00:00:00.123Z' });
  });

  it('rejects a bare date with no time component', () => {
    expect(validateExpiresAt('2026-08-01')).toEqual({ ok: false, error: CHANNELS_INVALID_EXPIRY_MESSAGE });
  });

  it('rejects a timestamp missing seconds', () => {
    expect(validateExpiresAt('2026-08-01T00:00Z')).toEqual({ ok: false, error: CHANNELS_INVALID_EXPIRY_MESSAGE });
  });

  it('rejects a timestamp missing a UTC offset', () => {
    expect(validateExpiresAt('2026-08-01T00:00:00')).toEqual({ ok: false, error: CHANNELS_INVALID_EXPIRY_MESSAGE });
  });

  it('rejects garbage input', () => {
    expect(validateExpiresAt('not-a-date')).toEqual({ ok: false, error: CHANNELS_INVALID_EXPIRY_MESSAGE });
  });
});

// ---------------------------------------------------------------------------
// validateGrantTo
// ---------------------------------------------------------------------------

describe('validateGrantTo', () => {
  it('accepts a non-blank recipient', () => {
    expect(validateGrantTo('peer-alpha')).toBeNull();
  });

  it('rejects a blank recipient', () => {
    expect(validateGrantTo('')).toBe(CHANNELS_GRANT_TO_REQUIRED_MESSAGE);
    expect(validateGrantTo('   ')).toBe(CHANNELS_GRANT_TO_REQUIRED_MESSAGE);
  });
});

// ---------------------------------------------------------------------------
// loadChannelsCollection
// ---------------------------------------------------------------------------

describe('loadChannelsCollection', () => {
  it('parses a successful response', async () => {
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => ({
        status: 200,
        data: { count: 1, results: [{ standardCode: 'OMM' }] } as T,
        etag: null,
        notModified: false,
      }),
    };
    const result = await loadChannelsCollection(client);
    expect(result).toEqual([row({ topic: '', visibility: '', grantState: '' })]);
  });

  it('degrades to [] on a fetch failure rather than throwing', async () => {
    const client: ChannelsApiClient = {
      requestJson: async () => {
        throw new SdnApiError(500, null, '/channels');
      },
    };
    expect(await loadChannelsCollection(client)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// fetchChannelDetailRow
// ---------------------------------------------------------------------------

describe('fetchChannelDetailRow', () => {
  it('prefers a verified per-provider row (real channelId) over the bare topic row', async () => {
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => ({
        status: 200,
        data: {
          results: [
            { standardCode: 'OMM' },
            { channelId: 'celestrak-OMM', sourceId: 'celestrak', standardCode: 'OMM', pnmVerified: true },
          ],
        } as T,
        etag: null,
        notModified: false,
      }),
    };
    const result = await fetchChannelDetailRow(client, 'OMM', row());
    expect(result.channelId).toBe('celestrak-OMM');
    expect(result.pnmVerified).toBe(true);
  });

  it('falls back to the plain topic row when no verified row exists', async () => {
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => ({
        status: 200,
        data: { results: [{ standardCode: 'OMM' }] } as T,
        etag: null,
        notModified: false,
      }),
    };
    const result = await fetchChannelDetailRow(client, 'OMM', row());
    expect(result.channelId).toBeNull();
  });

  it('falls back to the supplied fallback row on a fetch failure', async () => {
    const client: ChannelsApiClient = {
      requestJson: async () => {
        throw new SdnApiError(400, null, '/channels');
      },
    };
    const fallback = row({ standardCode: 'ACL' });
    expect(await fetchChannelDetailRow(client, 'ACL', fallback)).toEqual(fallback);
  });

  it('falls back to the supplied fallback row when the filtered result is empty', async () => {
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => ({ status: 200, data: { results: [] } as T, etag: null, notModified: false }),
    };
    const fallback = row({ standardCode: 'ACL' });
    expect(await fetchChannelDetailRow(client, 'ACL', fallback)).toEqual(fallback);
  });

  it('URL-encodes the standardCode query value', async () => {
    let capturedPath = '';
    const client: ChannelsApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { results: [] } as T, etag: null, notModified: false };
      },
    };
    await fetchChannelDetailRow(client, 'O M', row());
    expect(capturedPath).toBe('/channels?standardCode=O%20M');
  });
});

// ---------------------------------------------------------------------------
// issueChannelGrant
// ---------------------------------------------------------------------------

describe('issueChannelGrant', () => {
  function fakeClient(fn: (path: string, opts?: RequestOptions) => unknown): ChannelsApiClient {
    return {
      requestJson: async <T,>(path: string, opts?: RequestOptions) => {
        const value = fn(path, opts);
        if (value instanceof Error) throw value;
        return { status: 201, data: value as T, etag: null, notModified: false };
      },
    };
  }

  const grantFixture = {
    grantId: 'grant-abc123',
    channelId: 'local-OMM',
    subject: 'peer-alpha',
    scopes: ['subscribe', 'stream_open'],
    grantState: 'verified',
    issuedAt: '2026-07-11T00:00:00Z',
    expiresAt: '2026-07-12T00:00:00Z',
  };

  it('POSTs /channels/{sourceId}-{standardCode}/grants with the exact {to,scopes,expiresAt} body contract', async () => {
    let capturedPath = '';
    let capturedOpts: RequestOptions | undefined;
    const client: ChannelsApiClient = {
      requestJson: async <T,>(path: string, opts?: RequestOptions) => {
        capturedPath = path;
        capturedOpts = opts;
        return { status: 201, data: grantFixture as T, etag: null, notModified: false };
      },
    };
    const result = await issueChannelGrant(client, {
      sourceId: 'local',
      standardCode: 'OMM',
      to: 'peer-alpha',
      scopesRaw: 'subscribe, stream_open',
      expiresAtRaw: '2026-07-12T00:00:00Z',
    });
    expect(capturedPath).toBe('/channels/local-OMM/grants');
    expect(capturedOpts?.method).toBe('POST');
    expect(capturedOpts?.body).toEqual({ to: 'peer-alpha', scopes: ['subscribe', 'stream_open'], expiresAt: '2026-07-12T00:00:00Z' });
    expect(result).toEqual({ ok: true, grant: grantFixture, error: null });
  });

  it('omits expiresAt from the body when left blank (server defaults to +24h)', async () => {
    let capturedOpts: RequestOptions | undefined;
    const client: ChannelsApiClient = {
      requestJson: async <T,>(_path: string, opts?: RequestOptions) => {
        capturedOpts = opts;
        return { status: 201, data: grantFixture as T, etag: null, notModified: false };
      },
    };
    await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(capturedOpts?.body).toEqual({ to: 'peer-alpha', scopes: [] });
    expect(capturedOpts?.body).not.toHaveProperty('expiresAt');
  });

  it('defaults a blank sourceId to "local" in the built channel id', async () => {
    let capturedPath = '';
    const client = fakeClient((path) => {
      capturedPath = path;
      return grantFixture;
    });
    await issueChannelGrant(client, { sourceId: '  ', standardCode: 'ACL', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(capturedPath).toBe('/channels/local-ACL/grants');
  });

  it('rejects a blank recipient before making any network call', async () => {
    let called = false;
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => {
        called = true;
        return { status: 201, data: grantFixture as T, etag: null, notModified: false };
      },
    };
    const result = await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: '  ', scopesRaw: '', expiresAtRaw: '' });
    expect(result).toEqual({ ok: false, grant: null, error: CHANNELS_GRANT_TO_REQUIRED_MESSAGE });
    expect(called).toBe(false);
  });

  it('rejects an invalid expiry before making any network call', async () => {
    let called = false;
    const client: ChannelsApiClient = {
      requestJson: async <T,>() => {
        called = true;
        return { status: 201, data: grantFixture as T, etag: null, notModified: false };
      },
    };
    const result = await issueChannelGrant(client, {
      sourceId: 'local',
      standardCode: 'OMM',
      to: 'peer-alpha',
      scopesRaw: '',
      expiresAtRaw: 'not-a-date',
    });
    expect(result).toEqual({ ok: false, grant: null, error: CHANNELS_INVALID_EXPIRY_MESSAGE });
    expect(called).toBe(false);
  });

  it('surfaces an honest generic message for a server 400 (real message text is not recoverable — see file doc comment)', async () => {
    const client = fakeClient(() => new SdnApiError(400, null, '/channels/local-OMM/grants'));
    const result = await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(result.ok).toBe(false);
    expect(result.grant).toBeNull();
    expect(result.error).toContain('400');
  });

  it('surfaces the error body message when the client happens to have parsed one', async () => {
    const client = fakeClient(() => new SdnApiError(400, { code: 'bad_request', message: 'invalid channel grant scope' }, '/channels/local-OMM/grants'));
    const result = await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(result.error).toBe('invalid channel grant scope');
  });

  it('handles a generic network throw honestly', async () => {
    const client = fakeClient(() => new TypeError('Failed to fetch'));
    const result = await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(result).toEqual({ ok: false, grant: null, error: 'Grant request failed — network error.' });
  });

  it('treats a malformed success payload as a failure rather than fabricating a grant', async () => {
    const client = fakeClient(() => ({ ok: true }));
    const result = await issueChannelGrant(client, { sourceId: 'local', standardCode: 'OMM', to: 'peer-alpha', scopesRaw: '', expiresAtRaw: '' });
    expect(result.ok).toBe(false);
    expect(result.grant).toBeNull();
  });
});
