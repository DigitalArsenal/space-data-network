import { describe, expect, it } from 'vitest';
import {
  AUTH_STEP_TIMINGS_MS,
  GRANTED_DWELL_MS,
  MIN_SEQUENCE_DWELL_MS,
  MIN_STEP_DWELL_MS,
  NODE_KEY_FORMAT_ERROR,
  NODE_KEY_NO_PEER_ID_ERROR,
  NODE_KEY_REQUIRED_ERROR,
  STAR_COUNT,
  STAR_SEED,
  buildAuthSteps,
  createSeededRandom,
  describeAuthFailure,
  extractPeerIdFromKey,
  formatAgentVersionLabel,
  formatFeedsSynced,
  formatFooterNodeLabel,
  formatPeerIdentitySummary,
  formatPeersConnected,
  formatTelemetryCount,
  generateOrbitArcs,
  generateStarfield,
  isRecognizedNodeKey,
  networkStatusFromHealth,
  nodeStepLabels,
  parseHealthResponse,
  parseNodeInfoResponse,
  parseStatsResponse,
  remainingDwellMs,
  resolvePeerIdentity,
  shortenPeerId,
  validateNodeKeyForm,
} from './login';
import { SdnApiError } from '../../lib/auth/sdn-api-client';

// Not currently wired into a vitest `include` glob for `ui/src/**` (no such
// harness exists yet in this package — root `vitest.config.mts` only covers
// `src/**/*.test.ts`). Colocated here per U1.1 loop-task instructions so the
// pure logic is testable now and picked up automatically whenever a
// `ui/src` test project is added. Verified manually in the meantime via
// `npx vitest run --config <scratch config> ui/src/spaceaware/lib/login.test.ts`.

describe('starfield PRNG (seed-42 Park-Miller LCG)', () => {
  it('matches golden values for the first 10 draws', () => {
    const rnd = createSeededRandom(STAR_SEED);
    const got = Array.from({ length: 10 }, () => rnd());
    const golden = [
      0.00032870750889587566, 0.5245871020129822, 0.7354235321913956, 0.26330554078487006,
      0.37622397131110724, 0.19628582577979464, 0.9758738810084173, 0.512318108469396,
      0.5304490451377114, 0.2571016295147602,
    ];
    expect(got).toEqual(golden);
  });

  it('is deterministic across independent generators with the same seed', () => {
    const a = createSeededRandom(STAR_SEED);
    const b = createSeededRandom(STAR_SEED);
    for (let i = 0; i < 50; i++) {
      expect(a()).toBe(b());
    }
  });
});

describe('generateStarfield', () => {
  const w = 1200;
  const h = 800;

  it('produces exactly STAR_COUNT stars', () => {
    expect(generateStarfield(w, h)).toHaveLength(STAR_COUNT);
  });

  it('matches golden star values (first, second, last) for a 1200x800 canvas', () => {
    const stars = generateStarfield(w, h);
    expect(stars[0]).toEqual({
      x: 0.3944490106750508,
      y: 419.66968161038574,
      r: 1.0618811789722562,
      color: '207,227,236',
      alpha: 0.35,
    });
    expect(stars[1]).toEqual({
      x: 235.54299093575358,
      y: 780.6991048067339,
      r: 0.8610862976224565,
      color: '207,227,236',
      alpha: 0.27,
    });
    expect(stars[169]).toEqual({
      x: 1196.1744938027925,
      y: 336.4782290237389,
      r: 1.2882946022266033,
      color: '207,227,236',
      alpha: 0.49,
    });
  });

  it('is deterministic: identical inputs produce identical output (redraw-on-resize safety)', () => {
    expect(generateStarfield(w, h)).toEqual(generateStarfield(w, h));
  });

  it('only ever emits the 3 documented star colors', () => {
    const allowed = new Set(['207,227,236', '127,215,226', '255,217,160']);
    for (const star of generateStarfield(w, h)) {
      expect(allowed.has(star.color)).toBe(true);
    }
  });

  it('keeps radius in [0.4, 1.3) and alpha in [0.12, 0.72]', () => {
    for (const star of generateStarfield(w, h)) {
      expect(star.r).toBeGreaterThanOrEqual(0.4);
      expect(star.r).toBeLessThan(1.3);
      expect(star.alpha).toBeGreaterThanOrEqual(0.12);
      expect(star.alpha).toBeLessThanOrEqual(0.72);
    }
  });
});

describe('generateOrbitArcs', () => {
  it('produces 3 arcs with the documented center, radii, style and dash', () => {
    const arcs = generateOrbitArcs(1200, 800);
    expect(arcs).toHaveLength(3);
    for (const arc of arcs) {
      expect(arc.cx).toBe(600);
      expect(arc.cy).toBe(800 * 1.85);
    }
    expect(arcs[0].strokeStyle).toBe('rgba(90,150,180,0.06)');
    expect(arcs[0].dash).toEqual([]);
    expect(arcs[1].strokeStyle).toBe('rgba(53,201,216,0.09)');
    expect(arcs[1].dash).toEqual([]);
    expect(arcs[2].strokeStyle).toBe('rgba(90,150,180,0.06)');
    expect(arcs[2].dash).toEqual([2, 7]);
  });
});

describe('node-key validation', () => {
  it('flags empty input as required', () => {
    expect(validateNodeKeyForm('')).toBe(NODE_KEY_REQUIRED_ERROR);
    expect(validateNodeKeyForm('   ')).toBe(NODE_KEY_REQUIRED_ERROR);
  });

  it('flags unrecognized prefixes', () => {
    expect(validateNodeKeyForm('not-a-key')).toBe(NODE_KEY_FORMAT_ERROR);
    expect(isRecognizedNodeKey('not-a-key')).toBe(false);
  });

  it('accepts the documented prefixes', () => {
    for (const key of [
      '16Uiu2HAm1Lbvwj...',
      '12D3KooWAbc123',
      '/ip4/127.0.0.1/tcp/4001',
      '/dns/node.example.com/tcp/4001',
    ]) {
      expect(validateNodeKeyForm(key)).toBeNull();
      expect(isRecognizedNodeKey(key)).toBe(true);
    }
  });

  it('trims before validating', () => {
    expect(validateNodeKeyForm('  16Uiu2HAm  ')).toBeNull();
  });
});

describe('mock auth timing + step view models', () => {
  it('matches the documented timing constants', () => {
    expect(AUTH_STEP_TIMINGS_MS).toEqual({ step1: 700, step2: 1450, complete: 2150, redirect: 2900 });
  });

  it('has the documented read-only node step labels', () => {
    expect(nodeStepLabels()).toEqual(['PEER KEY VERIFY', 'P2P HANDSHAKE', 'SESSION SEALED']);
  });

  it('renders pending/active/done row states correctly', () => {
    const labels = nodeStepLabels();
    const steps = buildAuthSteps(labels, 1);
    expect(steps[0]).toMatchObject({ glyph: '✓', color: '#5ad6a0', status: 'OK' });
    expect(steps[1]).toMatchObject({
      glyph: '◌',
      color: '#35c9d8',
      status: 'RUNNING',
      anim: 'sa-spin 0.9s linear infinite',
    });
    expect(steps[2]).toMatchObject({ glyph: '·', color: '#44586a', status: 'QUEUED', anim: 'none' });
  });

  it('marks every step done once step advances past all of them', () => {
    const steps = buildAuthSteps(nodeStepLabels(), 3);
    expect(steps.every((s) => s.glyph === '✓' && s.status === 'OK')).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// U1.2 — real-auth dwell timing + operator-tab stage mapping
// ---------------------------------------------------------------------------

describe('remainingDwellMs', () => {
  it('returns the full floor when nothing has elapsed', () => {
    expect(remainingDwellMs(0, 220)).toBe(220);
  });

  it('returns the remainder once partway through the floor', () => {
    expect(remainingDwellMs(150, 220)).toBe(70);
  });

  it('never goes negative once the floor has already elapsed', () => {
    expect(remainingDwellMs(500, 220)).toBe(0);
    expect(remainingDwellMs(220, 220)).toBe(0);
  });

  it('treats negative/non-finite elapsed as "start of window"', () => {
    expect(remainingDwellMs(-5, 220)).toBe(220);
    expect(remainingDwellMs(Number.NaN, 220)).toBe(220);
  });

  it('defaults to MIN_STEP_DWELL_MS', () => {
    expect(remainingDwellMs(0)).toBe(MIN_STEP_DWELL_MS);
  });
});

describe('dwell constants', () => {
  it('keeps the per-step floor at or under the 300ms anti-flash ceiling', () => {
    expect(MIN_STEP_DWELL_MS).toBeGreaterThan(0);
    expect(MIN_STEP_DWELL_MS).toBeLessThanOrEqual(300);
  });

  it('sizes the whole-sequence floor off the per-step floor (3 rows)', () => {
    expect(MIN_SEQUENCE_DWELL_MS).toBe(MIN_STEP_DWELL_MS * 3);
  });

  it('keeps a positive granted-banner dwell', () => {
    expect(GRANTED_DWELL_MS).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// U1.2 — read-only peer-resolution error text
// ---------------------------------------------------------------------------

describe('describeAuthFailure', () => {
  it('prefers the SdnApiError JSON body message, uppercased', () => {
    const err = new SdnApiError(401, { code: 'unauthorized', message: 'challenge expired' }, '/api/auth/verify');
    expect(describeAuthFailure(err)).toBe('CHALLENGE EXPIRED');
  });

  it('falls back to the HTTP-status message for a bodyless SdnApiError', () => {
    // SdnApiError's own constructor already synthesizes `HTTP <status>` as
    // its `.message` when there is no JSON body (see sdn-api-client.ts) —
    // describeAuthFailure just uppercases whatever it finds.
    const err = new SdnApiError(503, null, '/api/v1/peers/16Uiu2HAmX');
    expect(describeAuthFailure(err)).toBe('HTTP 503');
  });

  it('uppercases a plain Error message', () => {
    expect(describeAuthFailure(new Error('network request failed'))).toBe('NETWORK REQUEST FAILED');
  });

  it('falls back to a generic message for a non-Error throw', () => {
    expect(describeAuthFailure('nope')).toBe('AUTHENTICATION FAILED');
    expect(describeAuthFailure(undefined)).toBe('AUTHENTICATION FAILED');
  });
});

// ---------------------------------------------------------------------------
// U1.2 — node-key tab: peer-ID extraction + EPM identity resolution
// ---------------------------------------------------------------------------

describe('extractPeerIdFromKey', () => {
  it('returns a bare 16Uiu…/12D3Koo… peer ID as-is', () => {
    expect(extractPeerIdFromKey('16Uiu2HAm1Lbvwj')).toBe('16Uiu2HAm1Lbvwj');
    expect(extractPeerIdFromKey('12D3KooWAbc123')).toBe('12D3KooWAbc123');
  });

  it('extracts the trailing /p2p/<id> component from a multiaddr', () => {
    expect(extractPeerIdFromKey('/ip4/127.0.0.1/tcp/4001/p2p/16Uiu2HAmX')).toBe('16Uiu2HAmX');
  });

  it('extracts /p2p/<id> even when more path follows it', () => {
    expect(extractPeerIdFromKey('/dns/node.example.com/tcp/4001/p2p/12D3KooWY/extra')).toBe('12D3KooWY');
  });

  it('trims surrounding whitespace before extracting', () => {
    expect(extractPeerIdFromKey('  16Uiu2HAmZ  ')).toBe('16Uiu2HAmZ');
  });

  it('returns null for a well-formed multiaddr with no embedded peer ID', () => {
    expect(extractPeerIdFromKey('/dns/node.example.com/tcp/4001')).toBeNull();
    expect(extractPeerIdFromKey('/ip4/127.0.0.1/tcp/4001')).toBeNull();
  });

  it('documents the error the component throws when extraction returns null', () => {
    // screens/LoginScreen.svelte's runNodeResolve() throws
    // `new Error(NODE_KEY_NO_PEER_ID_ERROR)` when this returns null — pinned
    // here so the two stay in sync.
    expect(extractPeerIdFromKey('/dns/node.example.com/tcp/4001')).toBeNull();
    expect(NODE_KEY_NO_PEER_ID_ERROR).toBe('MULTIADDR HAS NO EMBEDDED PEER ID (/p2p/<id>) TO RESOLVE');
  });
});

describe('resolvePeerIdentity', () => {
  it('reads dn/signature/signing-key fields from a directory-record-shaped payload', () => {
    const view = resolvePeerIdentity('16Uiu2HAmX', {
      dn: 'Node Alice',
      signature: 'abcd1234',
      keys: [
        { key_type: 'encryption', address_type: 'x25519' },
        { key_type: 'signing', address_type: 'ed25519' },
      ],
    });
    expect(view).toEqual({
      peerId: '16Uiu2HAmX',
      dn: 'Node Alice',
      signed: true,
      keyAlgorithm: 'ED25519',
    });
  });

  it('prefers the ed25519 signing key over a legacy secp256k1 signing key', () => {
    const view = resolvePeerIdentity('16Uiu2HAmX', {
      keys: [
        { key_type: 'signing', address_type: 'secp256k1' },
        { key_type: 'signing', address_type: 'ed25519' },
      ],
    });
    expect(view.keyAlgorithm).toBe('ED25519');
  });

  it('falls back to the only signing key when no ed25519 key is present', () => {
    const view = resolvePeerIdentity('16Uiu2HAmX', {
      keys: [{ key_type: 'signing', address_type: 'secp256k1' }],
    });
    expect(view.keyAlgorithm).toBe('SECP256K1');
  });

  it('degrades gracefully for the legacy native getPeer shape (no dn/signature/keys)', () => {
    const view = resolvePeerIdentity('16Uiu2HAmX', {
      peer_id: '16Uiu2HAmX',
      addrs: ['/ip4/127.0.0.1/tcp/4001'],
      connection_count: 1,
    });
    expect(view).toEqual({ peerId: '16Uiu2HAmX', dn: null, signed: false, keyAlgorithm: null });
  });

  it('degrades gracefully for a non-object/null payload', () => {
    expect(resolvePeerIdentity('16Uiu2HAmX', null)).toEqual({
      peerId: '16Uiu2HAmX',
      dn: null,
      signed: false,
      keyAlgorithm: null,
    });
    expect(resolvePeerIdentity('16Uiu2HAmX', undefined)).toEqual({
      peerId: '16Uiu2HAmX',
      dn: null,
      signed: false,
      keyAlgorithm: null,
    });
  });
});

describe('formatPeerIdentitySummary', () => {
  it('joins DN, signed state, and algorithm', () => {
    expect(
      formatPeerIdentitySummary({ peerId: '16Uiu2HAmX', dn: 'Node Alice', signed: true, keyAlgorithm: 'ED25519' }),
    ).toBe('Node Alice · SIGNED · ED25519');
  });

  it('falls back to the peer ID, UNSIGNED, and KEY UNKNOWN when the record is bare', () => {
    expect(
      formatPeerIdentitySummary({ peerId: '16Uiu2HAmX', dn: null, signed: false, keyAlgorithm: null }),
    ).toBe('16Uiu2HAmX · UNSIGNED · KEY UNKNOWN');
  });
});

// ---------------------------------------------------------------------------
// U1.2 — node telemetry parsing/formatting + footer identity
// ---------------------------------------------------------------------------

describe('networkStatusFromHealth', () => {
  it('maps ok/healthy/nominal to NOMINAL', () => {
    expect(networkStatusFromHealth('ok')).toBe('NOMINAL');
    expect(networkStatusFromHealth('healthy')).toBe('NOMINAL');
    expect(networkStatusFromHealth('NOMINAL')).toBe('NOMINAL');
  });

  it('maps degraded/warn/warning to DEGRADED', () => {
    expect(networkStatusFromHealth('degraded')).toBe('DEGRADED');
    expect(networkStatusFromHealth('warn')).toBe('DEGRADED');
  });

  it('maps anything else (including absent) to ALERT', () => {
    expect(networkStatusFromHealth('down')).toBe('ALERT');
    expect(networkStatusFromHealth(null)).toBe('ALERT');
    expect(networkStatusFromHealth(undefined)).toBe('ALERT');
    expect(networkStatusFromHealth('')).toBe('ALERT');
  });
});

describe('parseStatsResponse', () => {
  it('reads total_records/connected_peers/schemas.length from GET /api/v1/stats', () => {
    expect(
      parseStatsResponse({
        connected_peers: 3,
        total_records: 31000,
        total_bytes: 999,
        schemas: [{ schema: 'OMM' }, { schema: 'MPE' }],
      }),
    ).toEqual({ totalRecords: 31000, connectedPeers: 3, schemaCount: 2 });
  });

  it('degrades to nulls for a bare/malformed payload', () => {
    expect(parseStatsResponse({})).toEqual({ totalRecords: null, connectedPeers: null, schemaCount: null });
    expect(parseStatsResponse(null)).toEqual({ totalRecords: null, connectedPeers: null, schemaCount: null });
  });
});

describe('parseHealthResponse', () => {
  it('reads status from GET /api/v1/data/health', () => {
    expect(parseHealthResponse({ status: 'ok', component: 'spaceaware-data-api' })).toBe('NOMINAL');
  });

  it('degrades to ALERT for a bare/malformed payload', () => {
    expect(parseHealthResponse({})).toBe('ALERT');
    expect(parseHealthResponse(null)).toBe('ALERT');
  });
});

describe('parseNodeInfoResponse', () => {
  it('reads peer_id/agent_version from GET /api/node/info', () => {
    expect(parseNodeInfoResponse({ peer_id: '16Uiu2HAmX', agent_version: 'spacedatanetwork/1.4.2' })).toEqual({
      peerId: '16Uiu2HAmX',
      agentVersion: 'spacedatanetwork/1.4.2',
    });
  });

  it('degrades to nulls for a bare/malformed payload', () => {
    expect(parseNodeInfoResponse({})).toEqual({ peerId: null, agentVersion: null });
    expect(parseNodeInfoResponse(undefined)).toEqual({ peerId: null, agentVersion: null });
  });
});

describe('formatTelemetryCount', () => {
  it('groups with commas', () => {
    expect(formatTelemetryCount(31000)).toBe('31,000');
    expect(formatTelemetryCount(1234567)).toBe('1,234,567');
  });

  it('falls back to an em dash for null/undefined/non-finite', () => {
    expect(formatTelemetryCount(null)).toBe('—');
    expect(formatTelemetryCount(undefined)).toBe('—');
    expect(formatTelemetryCount(Number.NaN)).toBe('—');
  });
});

describe('formatPeersConnected / formatFeedsSynced', () => {
  it('appends the documented unit label', () => {
    expect(formatPeersConnected(3)).toBe('3 CONNECTED');
    expect(formatFeedsSynced(9)).toBe('9 SYNCED');
  });

  it('falls back to an em dash when unavailable', () => {
    expect(formatPeersConnected(null)).toBe('—');
    expect(formatFeedsSynced(undefined)).toBe('—');
  });
});

describe('shortenPeerId', () => {
  it('shortens a long peer ID to prefix…suffix (15 chars · ellipsis · 6 chars)', () => {
    expect(shortenPeerId('16Uiu2HAm1Lbvwjabcdefghijklmnop')).toBe('16Uiu2HAm1Lbvwj…klmnop');
  });

  it('leaves a short peer ID untouched', () => {
    expect(shortenPeerId('16Uiu2HAmX')).toBe('16Uiu2HAmX');
  });

  it('falls back to UNKNOWN for empty/absent input', () => {
    expect(shortenPeerId(null)).toBe('UNKNOWN');
    expect(shortenPeerId(undefined)).toBe('UNKNOWN');
    expect(shortenPeerId('   ')).toBe('UNKNOWN');
  });
});

describe('formatFooterNodeLabel', () => {
  it('prefixes THIS NODE · without a city (no geo data source on a stock node)', () => {
    expect(formatFooterNodeLabel('16Uiu2HAmX')).toBe('THIS NODE · 16Uiu2HAmX');
  });

  it('falls back to UNKNOWN for absent input', () => {
    expect(formatFooterNodeLabel(null)).toBe('THIS NODE · UNKNOWN');
  });
});

describe('formatAgentVersionLabel', () => {
  it('takes the part after the last / (AgentName/SuiteVersion)', () => {
    expect(formatAgentVersionLabel('spacedatanetwork/1.4.2')).toBe('NODE v1.4.2');
  });

  it('uses the whole string when there is no slash', () => {
    expect(formatAgentVersionLabel('1.4.2')).toBe('NODE v1.4.2');
  });

  it('falls back to a placeholder for absent input', () => {
    expect(formatAgentVersionLabel(null)).toBe('NODE VERSION UNKNOWN');
    expect(formatAgentVersionLabel('')).toBe('NODE VERSION UNKNOWN');
  });
});
