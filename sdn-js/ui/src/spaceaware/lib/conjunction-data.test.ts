import { describe, expect, it } from 'vitest';
import type { ChannelCollectionRow } from './channels-data';
import type { NodeStatsSnapshot } from './node-data';
import type { SharedGroup } from './groups-data';
import {
  CONJUNCTION_DEFAULT_CRITERIA,
  CONJUNCTION_DEFAULT_PROPAGATOR,
  CONJUNCTION_DEFAULT_RESULT_MODE,
  CONJUNCTION_LIVE_DEMO_TAG_TITLE,
  CONJUNCTION_NUMERICAL_PAID_TOOLTIP,
  CONJUNCTION_ONE_OFF_DEFAULT_WINDOW,
  CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE,
  CONJUNCTION_PROPAGATORS,
  CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE,
  CONJUNCTION_RESULTS_DEMO_TAG_TITLE,
  CONJUNCTION_RESULTS_FIXTURE,
  CONJUNCTION_SOURCES_PRECEDENCE_FOOTNOTE,
  CONJUNCTION_STREAM_TICK_INTERVAL_MS,
  LOCAL_CATALOG_SOURCE_ID,
  buildLiveCardView,
  buildOneOffMessage,
  buildPropagatorRows,
  buildProvenanceView,
  buildResultRecords,
  buildResultRows,
  buildResultsCsvOutput,
  buildResultsJsonOutput,
  buildSourceRowViews,
  buildSourceRows,
  buildTargetPills,
  buildTargetStripView,
  bumpHardBodyRadius,
  bumpMissDistance,
  bumpOneOffWindow,
  bumpScreenWindow,
  bumpStepSize,
  computeRowState,
  conjunctionMissValueColor,
  conjunctionStateColor,
  cyclePcThreshold,
  formatLastDeltaLabel,
  formatMissDistanceLabel,
  formatPcThresholdLabel,
  loadConjunctionSources,
  moveSourceOrder,
  nextStreamTick,
  parseConjunctionSourcePeers,
  propagatorName,
  selectPropagator,
  toggleSourceOff,
  type ConjunctionApiClient,
  type ConjunctionCriteria,
  type ConjunctionSourcePeer,
  type ConjunctionSourceRow,
} from './conjunction-data';

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

function channel(overrides: Partial<ChannelCollectionRow> = {}): ChannelCollectionRow {
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

function stats(overrides: Partial<NodeStatsSnapshot> = {}): NodeStatsSnapshot {
  return { connectedPeers: null, totalBytes: null, totalRecords: null, schemas: [], ...overrides };
}

function group(overrides: Partial<SharedGroup> = {}): SharedGroup {
  return {
    id: 'leo-a',
    name: 'LEO Constellation A',
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 42,
    regime: 'LEO',
    scope: '53° shell',
    conj: 'watch',
    conjN: 2,
    maxPc: '7.3e-5',
    nextTca: '19h 40m',
    tcaH: 19.7,
    updated: '2m ago',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// parseConjunctionSourcePeers
// ---------------------------------------------------------------------------

describe('parseConjunctionSourcePeers', () => {
  it('parses peer_id and defaults dn to null (real live shape today)', () => {
    const result = parseConjunctionSourcePeers({ peers: [{ peer_id: '12D3KooW1', addrs: ['/ip4/1.2.3.4/tcp/5001'] }] });
    expect(result).toEqual([{ peerId: '12D3KooW1', dn: null }]);
  });

  it('defensively reads a dn field when present (forward-compatible, no live peer has this today)', () => {
    const result = parseConjunctionSourcePeers({ peers: [{ peer_id: '12D3KooW1', dn: 'SpaceAware.io Analytics' }] });
    expect(result).toEqual([{ peerId: '12D3KooW1', dn: 'SpaceAware.io Analytics' }]);
  });

  it('drops entries with no peer_id', () => {
    const result = parseConjunctionSourcePeers({ peers: [{ dn: 'x' }, { peer_id: 'ok' }] });
    expect(result).toEqual([{ peerId: 'ok', dn: null }]);
  });

  it('degrades to [] for a non-object payload or missing peers array', () => {
    expect(parseConjunctionSourcePeers(null)).toEqual([]);
    expect(parseConjunctionSourcePeers({})).toEqual([]);
    expect(parseConjunctionSourcePeers({ peers: 'nope' })).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// buildSourceRows — source-row synthesis (live-minimal, synthetic provider,
// synthetic sealed channel)
// ---------------------------------------------------------------------------

describe('buildSourceRows', () => {
  it('live-minimal: no peer dn, no sealed MPE channel — renders only the Local SDN Catalog row', () => {
    const rows = buildSourceRows([{ peerId: 'peer-1', dn: null }], [channel({ standardCode: 'OMM' })], stats({ totalRecords: 4213 }));
    expect(rows).toHaveLength(1);
    expect(rows[0]?.id).toBe(LOCAL_CATALOG_SOURCE_ID);
    expect(rows[0]?.name).toBe('Local SDN Catalog');
    expect(rows[0]?.type).toBe('CATALOG');
    expect(rows[0]?.tag).toBe('LOCAL STORE');
  });

  it('live-minimal with no stats at all still renders exactly the local row, with an honest unavailable detail', () => {
    const rows = buildSourceRows([], [], null);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.detail).toMatch(/unavailable/i);
  });

  it('local row detail carries the real record count when stats are available', () => {
    const rows = buildSourceRows([], [], stats({ totalRecords: 8462 }));
    expect(rows[0]?.detail).toContain('8,462');
  });

  it('synthetic provider: a peer WITH an EPM dn yields a real catalog row', () => {
    const rows = buildSourceRows([{ peerId: 'peer-9', dn: 'SpaceAware.io Analytics' }], [], stats());
    expect(rows).toHaveLength(2);
    const peerRow = rows.find((r) => r.id === 'peer:peer-9');
    expect(peerRow).toMatchObject({ name: 'SpaceAware.io Analytics', type: 'CATALOG', tag: 'PEER PROVIDER' });
  });

  it('multiple peers with dn each yield their own row', () => {
    const rows = buildSourceRows(
      [
        { peerId: 'peer-a', dn: 'Alpha Provider' },
        { peerId: 'peer-b', dn: 'Beta Provider' },
      ],
      [],
      stats(),
    );
    expect(rows.map((r) => r.id)).toEqual(['peer:peer-a', 'peer:peer-b', LOCAL_CATALOG_SOURCE_ID]);
  });

  it('synthetic sealed channel: a private/sealed MPE channel yields the padlocked SpaceAware MPE ephemeris row', () => {
    const rows = buildSourceRows([], [channel({ standardCode: 'MPE', encryptionState: 'encrypted', visibility: 'private' })], stats());
    expect(rows).toHaveLength(2);
    const mpeRow = rows.find((r) => r.type === 'EPHEMERIS');
    expect(mpeRow).toMatchObject({ name: 'SpaceAware MPE', tag: 'PRIVATE · SEALED', enc: '🔒' });
  });

  it('a public/unsealed MPE channel does NOT yield an ephemeris row (never fabricated)', () => {
    const rows = buildSourceRows([], [channel({ standardCode: 'MPE', encryptionState: 'none', visibility: 'public' })], stats());
    expect(rows).toHaveLength(1);
    expect(rows[0]?.id).toBe(LOCAL_CATALOG_SOURCE_ID);
  });

  it('a sealed non-MPE channel does NOT yield an ephemeris row', () => {
    const rows = buildSourceRows([], [channel({ standardCode: 'CDM', encryptionState: 'encrypted' })], stats());
    expect(rows).toHaveLength(1);
  });

  it('a channel with visibility starting "private" (not just encryptionState) also qualifies', () => {
    const rows = buildSourceRows([], [channel({ standardCode: 'MPE', encryptionState: 'none', visibility: 'private-sealed' })], stats());
    expect(rows.some((r) => r.type === 'EPHEMERIS')).toBe(true);
  });

  it('default precedence order: ephemeris rows first, then peer catalogs, then local last', () => {
    const rows = buildSourceRows(
      [{ peerId: 'peer-1', dn: 'Provider One' }],
      [channel({ standardCode: 'MPE', encryptionState: 'encrypted' })],
      stats(),
    );
    expect(rows.map((r) => r.type)).toEqual(['EPHEMERIS', 'CATALOG', 'CATALOG']);
    expect(rows[rows.length - 1]?.id).toBe(LOCAL_CATALOG_SOURCE_ID);
  });

  it('combines a real provider row and a real sealed row simultaneously (both surfaces exist)', () => {
    const rows = buildSourceRows(
      [{ peerId: 'peer-1', dn: 'Provider One' }],
      [channel({ standardCode: 'MPE', encryptionState: 'encrypted' })],
      stats({ totalRecords: 10 }),
    );
    expect(rows).toHaveLength(3);
  });
});

// ---------------------------------------------------------------------------
// Precedence reorder / toggle logic
// ---------------------------------------------------------------------------

describe('moveSourceOrder', () => {
  const order = ['a', 'b', 'c'];

  it('raises precedence (direction -1) by swapping with the previous row', () => {
    expect(moveSourceOrder(order, 'b', -1)).toEqual(['b', 'a', 'c']);
  });

  it('lowers precedence (direction +1) by swapping with the next row', () => {
    expect(moveSourceOrder(order, 'b', 1)).toEqual(['a', 'c', 'b']);
  });

  it('no-ops when already at the top and moved up', () => {
    expect(moveSourceOrder(order, 'a', -1)).toEqual(order);
  });

  it('no-ops when already at the bottom and moved down', () => {
    expect(moveSourceOrder(order, 'c', 1)).toEqual(order);
  });

  it('no-ops for an unknown id', () => {
    expect(moveSourceOrder(order, 'nope', -1)).toEqual(order);
  });

  it('never mutates the input array', () => {
    const copy = order.slice();
    moveSourceOrder(order, 'b', -1);
    expect(order).toEqual(copy);
  });
});

describe('toggleSourceOff', () => {
  it('flips an unset id to true (off)', () => {
    expect(toggleSourceOff({}, 'local')).toEqual({ local: true });
  });

  it('flips true back to false', () => {
    expect(toggleSourceOff({ local: true }, 'local')).toEqual({ local: false });
  });

  it('leaves other keys untouched', () => {
    expect(toggleSourceOff({ a: true, b: false }, 'a')).toEqual({ a: false, b: false });
  });
});

describe('buildSourceRowViews', () => {
  const rows: ConjunctionSourceRow[] = [
    { id: 'mpe:MPE', name: 'SpaceAware MPE', type: 'EPHEMERIS', tag: 'PRIVATE · SEALED', tagColor: '#ff9b9b', enc: '🔒', detail: 'sealed' },
    { id: 'local', name: 'Local SDN Catalog', type: 'CATALOG', tag: 'LOCAL STORE', tagColor: '#9fb3bc', enc: '●', detail: '0 records' },
  ];

  it('assigns 1-based precedence numbers in order', () => {
    const views = buildSourceRowViews(rows, ['mpe:MPE', 'local'], {});
    expect(views.map((v) => v.precedence)).toEqual([1, 2]);
  });

  it('applies dimmed styling when a row is toggled off', () => {
    const views = buildSourceRowViews(rows, ['mpe:MPE', 'local'], { local: true });
    const localView = views.find((v) => v.id === 'local')!;
    expect(localView.off).toBe(true);
    expect(localView.rowOpacity).toBe('0.5');
    expect(localView.nameColor).toBe('#6f8693');
    expect(localView.toggleBg).toBe('transparent');
  });

  it('applies bright styling when a row is enabled', () => {
    const views = buildSourceRowViews(rows, ['mpe:MPE', 'local'], {});
    const localView = views.find((v) => v.id === 'local')!;
    expect(localView.off).toBe(false);
    expect(localView.rowOpacity).toBe('1');
    expect(localView.toggleKnob).toBe('11px');
  });

  it('sets canMoveUp/canMoveDown at the boundaries', () => {
    const views = buildSourceRowViews(rows, ['mpe:MPE', 'local'], {});
    expect(views[0]).toMatchObject({ canMoveUp: false, canMoveDown: true });
    expect(views[1]).toMatchObject({ canMoveUp: true, canMoveDown: false });
  });

  it('drops an order id no longer present in rows', () => {
    const views = buildSourceRowViews(rows, ['stale-id', 'mpe:MPE', 'local'], {});
    expect(views.map((v) => v.id)).toEqual(['mpe:MPE', 'local']);
  });

  it('appends a row missing from a stale order at the end', () => {
    const views = buildSourceRowViews(rows, ['local'], {});
    expect(views.map((v) => v.id)).toEqual(['local', 'mpe:MPE']);
  });

  it('precedence footnote text is exported and non-empty', () => {
    expect(CONJUNCTION_SOURCES_PRECEDENCE_FOOTNOTE.length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// Propagator rows
// ---------------------------------------------------------------------------

describe('buildPropagatorRows / selectPropagator', () => {
  it('has exactly SGP4, SDP4, Numerical in order', () => {
    expect(CONJUNCTION_PROPAGATORS.map((p) => p.name)).toEqual(['SGP4', 'SDP4', 'Numerical']);
  });

  it('marks the selected propagator IN USE with the accent styling', () => {
    const rows = buildPropagatorRows('sgp4');
    const sgp4 = rows.find((r) => r.key === 'sgp4')!;
    expect(sgp4.selected).toBe(true);
    expect(sgp4.stateLabel).toBe('IN USE');
    expect(sgp4.radioDot).toBe('#35c9d8');
  });

  it('Numerical is always disabled with the paid tooltip, selected or not', () => {
    const rows = buildPropagatorRows('sgp4');
    const numerical = rows.find((r) => r.key === 'num')!;
    expect(numerical.disabled).toBe(true);
    expect(numerical.tooltip).toBe(CONJUNCTION_NUMERICAL_PAID_TOOLTIP);
  });

  it('selectPropagator switches between unlocked propagators', () => {
    expect(selectPropagator('sgp4', 'sdp4')).toBe('sdp4');
  });

  it('selectPropagator refuses to select the paid Numerical propagator', () => {
    expect(selectPropagator('sgp4', 'num')).toBe('sgp4');
  });

  it('propagatorName resolves a display name', () => {
    expect(propagatorName('sgp4')).toBe('SGP4');
    expect(propagatorName(CONJUNCTION_DEFAULT_PROPAGATOR)).toBe('SGP4');
  });
});

// ---------------------------------------------------------------------------
// Screening criteria steppers
// ---------------------------------------------------------------------------

describe('criteria steppers', () => {
  it('CONJUNCTION_DEFAULT_CRITERIA matches the mock verbatim', () => {
    expect(CONJUNCTION_DEFAULT_CRITERIA).toEqual({ miss: 5, pcExp: 4, window: 72, hbr: 20, step: 60 });
  });

  it('bumpMissDistance increments/decrements by 0.5 and clamps to [0.5, 50]', () => {
    expect(bumpMissDistance(CONJUNCTION_DEFAULT_CRITERIA, 0.5).miss).toBe(5.5);
    expect(bumpMissDistance({ ...CONJUNCTION_DEFAULT_CRITERIA, miss: 0.5 }, -0.5).miss).toBe(0.5);
    expect(bumpMissDistance({ ...CONJUNCTION_DEFAULT_CRITERIA, miss: 50 }, 0.5).miss).toBe(50);
  });

  it('bumpScreenWindow increments by 12h and clamps to [12, 336]', () => {
    expect(bumpScreenWindow(CONJUNCTION_DEFAULT_CRITERIA, 12).window).toBe(84);
    expect(bumpScreenWindow({ ...CONJUNCTION_DEFAULT_CRITERIA, window: 12 }, -12).window).toBe(12);
    expect(bumpScreenWindow({ ...CONJUNCTION_DEFAULT_CRITERIA, window: 336 }, 12).window).toBe(336);
  });

  it('bumpHardBodyRadius increments by 5m and clamps to [5, 200]', () => {
    expect(bumpHardBodyRadius(CONJUNCTION_DEFAULT_CRITERIA, 5).hbr).toBe(25);
    expect(bumpHardBodyRadius({ ...CONJUNCTION_DEFAULT_CRITERIA, hbr: 5 }, -5).hbr).toBe(5);
    expect(bumpHardBodyRadius({ ...CONJUNCTION_DEFAULT_CRITERIA, hbr: 200 }, 5).hbr).toBe(200);
  });

  it('bumpStepSize increments by 30s and clamps to [30, 600]', () => {
    expect(bumpStepSize(CONJUNCTION_DEFAULT_CRITERIA, 30).step).toBe(90);
    expect(bumpStepSize({ ...CONJUNCTION_DEFAULT_CRITERIA, step: 30 }, -30).step).toBe(30);
    expect(bumpStepSize({ ...CONJUNCTION_DEFAULT_CRITERIA, step: 600 }, 30).step).toBe(600);
  });

  it('cyclePcThreshold cycles through [3,4,5,6] and wraps around', () => {
    let c: ConjunctionCriteria = { ...CONJUNCTION_DEFAULT_CRITERIA, pcExp: 3 };
    c = cyclePcThreshold(c);
    expect(c.pcExp).toBe(4);
    c = cyclePcThreshold(c);
    expect(c.pcExp).toBe(5);
    c = cyclePcThreshold(c);
    expect(c.pcExp).toBe(6);
    c = cyclePcThreshold(c);
    expect(c.pcExp).toBe(3);
  });

  it('formatMissDistanceLabel renders one decimal place', () => {
    expect(formatMissDistanceLabel({ ...CONJUNCTION_DEFAULT_CRITERIA, miss: 5 })).toBe('5.0');
    expect(formatMissDistanceLabel({ ...CONJUNCTION_DEFAULT_CRITERIA, miss: 5.5 })).toBe('5.5');
  });

  it('formatPcThresholdLabel renders the 1e-N form', () => {
    expect(formatPcThresholdLabel(CONJUNCTION_DEFAULT_CRITERIA)).toBe('1e-4');
  });
});

// ---------------------------------------------------------------------------
// Results builders (key passthrough) + demo fixture shape
// ---------------------------------------------------------------------------

describe('CONJUNCTION_RESULTS_FIXTURE shape', () => {
  it('has exactly 3 fixture rows with object/tca/miss/pc fields', () => {
    expect(CONJUNCTION_RESULTS_FIXTURE).toHaveLength(3);
    for (const r of CONJUNCTION_RESULTS_FIXTURE) {
      expect(typeof r.object).toBe('string');
      expect(typeof r.tca).toBe('string');
      expect(typeof r.miss).toBe('string');
      expect(typeof r.pc).toBe('string');
    }
  });

  it('CONJUNCTION_DEFAULT_RESULT_MODE is table', () => {
    expect(CONJUNCTION_DEFAULT_RESULT_MODE).toBe('table');
  });
});

describe('computeRowState', () => {
  it('WARN when miss <= threshold and pc >= pcThreshold', () => {
    expect(computeRowState(0.42, 7.3e-4, CONJUNCTION_DEFAULT_CRITERIA)).toBe('WARN');
  });

  it('REVIEW when miss is within 2x threshold but pc is below threshold', () => {
    expect(computeRowState(1.84, 2.1e-5, CONJUNCTION_DEFAULT_CRITERIA)).toBe('REVIEW');
  });

  it('CLEAR when both miss and pc are well outside thresholds', () => {
    expect(computeRowState(100, 1e-9, CONJUNCTION_DEFAULT_CRITERIA)).toBe('CLEAR');
  });

  it('tightening the miss-distance criterion can flip a REVIEW row to CLEAR', () => {
    // At the default 5km threshold, miss=3/pc=1e-9 falls inside the 2x
    // review band (3 <= 10) — REVIEW. Tightening to a 1km threshold (2x
    // band shrinks to 2km) pushes the same event outside it — CLEAR.
    expect(computeRowState(3, 1e-9, CONJUNCTION_DEFAULT_CRITERIA)).toBe('REVIEW');
    const tight = { ...CONJUNCTION_DEFAULT_CRITERIA, miss: 1 };
    expect(computeRowState(3, 1e-9, tight)).toBe('CLEAR');
  });
});

describe('conjunctionStateColor / conjunctionMissValueColor', () => {
  it('state color ramp: WARN red, REVIEW amber, CLEAR green', () => {
    expect(conjunctionStateColor('WARN')).toBe('#ff6b6b');
    expect(conjunctionStateColor('REVIEW')).toBe('#ffb24d');
    expect(conjunctionStateColor('CLEAR')).toBe('#5ad6a0');
  });

  it('miss-value color ramp: WARN red, REVIEW amber, CLEAR neutral ice (distinct from state ramp)', () => {
    expect(conjunctionMissValueColor('WARN')).toBe('#ff6b6b');
    expect(conjunctionMissValueColor('REVIEW')).toBe('#ffb24d');
    expect(conjunctionMissValueColor('CLEAR')).toBe('#cfe3ec');
  });
});

describe('buildResultRows / buildResultRecords / JSON / CSV — key passthrough', () => {
  it('buildResultRows recomputes state per current criteria for all 3 fixture rows', () => {
    // Note: fixture row 3 (SAT-57944, miss=8.92) recomputes to REVIEW at the
    // DEFAULT criteria (8.92 <= miss*2=10 still trips the review band) even
    // though the mock's own hardcoded fixture labels it 'clear' — see the
    // file doc comment's TABLE/JSON/CSV-agreement deviation: this view
    // always derives state dynamically, never trusting a stale fixture
    // label once criteria are live-adjustable.
    const rows = buildResultRows(CONJUNCTION_DEFAULT_CRITERIA);
    expect(rows.map((r) => r.state)).toEqual(['WARN', 'REVIEW', 'REVIEW']);
  });

  it('buildResultRecords uses the mock-verbatim key set: object, tca, missDistanceKm, pc, state', () => {
    const records = buildResultRecords(CONJUNCTION_DEFAULT_CRITERIA);
    expect(Object.keys(records[0]!)).toEqual(['object', 'tca', 'missDistanceKm', 'pc', 'state']);
    expect(records[0]).toMatchObject({ object: 'SAT-39210', missDistanceKm: 0.42, state: 'warn' });
  });

  it('missDistanceKm is a real number, not the original string', () => {
    const records = buildResultRecords(CONJUNCTION_DEFAULT_CRITERIA);
    expect(typeof records[0]!.missDistanceKm).toBe('number');
  });

  it('buildResultsJsonOutput pretty-prints the exact records array (reuses query-data.ts)', () => {
    const json = buildResultsJsonOutput(CONJUNCTION_DEFAULT_CRITERIA);
    expect(JSON.parse(json)).toEqual(buildResultRecords(CONJUNCTION_DEFAULT_CRITERIA));
  });

  it('buildResultsCsvOutput has a header row matching the record keys and one line per row', () => {
    const csv = buildResultsCsvOutput(CONJUNCTION_DEFAULT_CRITERIA);
    const lines = csv.split('\n');
    expect(lines[0]).toBe('object,tca,missDistanceKm,pc,state');
    expect(lines).toHaveLength(1 + CONJUNCTION_RESULTS_FIXTURE.length);
  });

  it('TABLE/JSON/CSV all agree on state for the same criteria (deviation from the mock — see file doc comment)', () => {
    const custom = { ...CONJUNCTION_DEFAULT_CRITERIA, miss: 0.5 };
    const tableStates = buildResultRows(custom).map((r) => r.state.toLowerCase());
    const jsonStates = JSON.parse(buildResultsJsonOutput(custom)).map((r: { state: string }) => r.state);
    expect(jsonStates).toEqual(tableStates);
  });

  it('CONJUNCTION_RESULTS_DEMO_TAG_TITLE is non-empty explanatory text', () => {
    expect(CONJUNCTION_RESULTS_DEMO_TAG_TITLE.length).toBeGreaterThan(20);
  });
});

// ---------------------------------------------------------------------------
// Group-pill selection view-model
// ---------------------------------------------------------------------------

describe('buildTargetPills', () => {
  it('styles the selected pill with the red accent', () => {
    const groups = [group({ id: 'a' }), group({ id: 'b', owner: 'celestrak', ownerName: 'CelesTrak Provider' })];
    const pills = buildTargetPills(groups, 'a');
    expect(pills[0]).toMatchObject({ selected: true, color: '#ffd2d2', border: 'rgba(255,107,107,0.5)' });
    expect(pills[1]).toMatchObject({ selected: false, color: '#9fb3bc' });
  });

  it('uses the same ownership glyph/color as GROUPS (mine vs peer)', () => {
    const groups = [group({ id: 'mine', owner: 'self' }), group({ id: 'peer', owner: 'celestrak' })];
    const pills = buildTargetPills(groups, null);
    expect(pills[0]).toMatchObject({ glyph: '⬢', glyphColor: '#c77dff' });
    expect(pills[1]).toMatchObject({ glyph: '⬡', glyphColor: '#ff9e64' });
  });

  it('maps conj status to the pill dot color, honest gray dash color for no data', () => {
    const groups = [group({ id: 'a', conj: 'critical' }), group({ id: 'b', conj: '' })];
    const pills = buildTargetPills(groups, null);
    expect(pills[0]?.conjColorDot).toBe('#ff6b6b');
    expect(pills[1]?.conjColorDot).toBe('#5a7a8a');
  });

  it('returns an empty array for an empty group list', () => {
    expect(buildTargetPills([], null)).toEqual([]);
  });
});

describe('buildTargetStripView', () => {
  it('renders the real count/ownership fields for a mine group', () => {
    const strip = buildTargetStripView(group({ count: 42, owner: 'self', ownerName: 'THIS NODE' }));
    expect(strip.countLabel).toBe('42 OBJECTS');
    expect(strip.kindLabel).toBe('MY GROUP');
    expect(strip.ownerName).toBe('THIS NODE');
  });

  it('renders PEER GROUP kind + real owner name for a peer group', () => {
    const strip = buildTargetStripView(group({ owner: 'celestrak', ownerName: 'CelesTrak Provider', count: 128 }));
    expect(strip.kindLabel).toBe('PEER GROUP');
    expect(strip.countLabel).toBe('128 OBJECTS');
    expect(strip.ownerName).toBe('CelesTrak Provider');
  });

  it('openIn3dPath matches groups-data.ts groupOrbitalPath', () => {
    const strip = buildTargetStripView(group({ id: 'leo-a' }));
    expect(strip.openIn3dPath).toBe('/orbital?group=leo-a');
  });
});

// ---------------------------------------------------------------------------
// Pause/resume + live card + one-off state
// ---------------------------------------------------------------------------

describe('formatLastDeltaLabel / nextStreamTick', () => {
  it('renders "no deltas while paused" when not live', () => {
    expect(formatLastDeltaLabel(false, 5)).toBe('no deltas while paused');
  });

  it('renders the mock-verbatim delta formula while live', () => {
    expect(formatLastDeltaLabel(true, 0)).toBe('Δ 1s ago · 1240 deltas');
    expect(formatLastDeltaLabel(true, 9)).toBe('Δ 2s ago · 1267 deltas');
  });

  it('nextStreamTick increments by 1', () => {
    expect(nextStreamTick(0)).toBe(1);
    expect(nextStreamTick(41)).toBe(42);
  });

  it('CONJUNCTION_STREAM_TICK_INTERVAL_MS matches the mock (2600ms)', () => {
    expect(CONJUNCTION_STREAM_TICK_INTERVAL_MS).toBe(2600);
  });
});

describe('buildLiveCardView', () => {
  it('renders the LIVE state styling', () => {
    const view = buildLiveCardView(true, 3, 1, 'SGP4');
    expect(view.label).toBe('SCREENING · LIVE');
    expect(view.dotColor).toBe('#5ad6a0');
    expect(view.pulseOn).toBe(true);
    expect(view.buttonLabel).toBe('PAUSE STREAM');
    expect(view.sourceCountLabel).toBe('1 sources');
    expect(view.propagatorLabel).toBe('SGP4');
  });

  it('renders the PAUSED state styling', () => {
    const view = buildLiveCardView(false, 3, 1, 'SGP4');
    expect(view.label).toBe('STREAM PAUSED');
    expect(view.dotColor).toBe('#f0b54a');
    expect(view.pulseOn).toBe(false);
    expect(view.buttonLabel).toBe('RESUME STREAM');
    expect(view.lastDeltaLabel).toBe('no deltas while paused');
  });

  it('CONJUNCTION_LIVE_DEMO_TAG_TITLE is explanatory, non-empty', () => {
    expect(CONJUNCTION_LIVE_DEMO_TAG_TITLE.length).toBeGreaterThan(20);
  });
});

describe('one-off run popover state', () => {
  it('CONJUNCTION_ONE_OFF_DEFAULT_WINDOW is 6h (mock default)', () => {
    expect(CONJUNCTION_ONE_OFF_DEFAULT_WINDOW).toBe(6);
  });

  it('bumpOneOffWindow clamps to [1, 72]', () => {
    expect(bumpOneOffWindow(6, 1)).toBe(7);
    expect(bumpOneOffWindow(1, -1)).toBe(1);
    expect(bumpOneOffWindow(72, 1)).toBe(72);
  });

  it('buildOneOffMessage is empty before a run and shows the window after', () => {
    expect(buildOneOffMessage(false, 6)).toBe('');
    expect(buildOneOffMessage(true, 6)).toBe('last backfill · 6h window · done');
  });

  it('CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE explains no real computation occurs', () => {
    expect(CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE.toLowerCase()).toContain('no computation');
  });
});

// ---------------------------------------------------------------------------
// Provenance view-model
// ---------------------------------------------------------------------------

describe('buildProvenanceView', () => {
  it('matches the mock static fixture fields verbatim', () => {
    const view = buildProvenanceView();
    expect(view).toEqual({
      mode: 'private-maneuver-ephemeris',
      module: 'sdn-ca-screen/1.0.0',
      resultChannel: 'ca-results-private',
      grant: 'grant-mpe-alpha',
      queryHash: 'sha256:designerqueryexample',
      resultHash: 'sha256:designerresultexample',
      enclaveNote:
        'Screening runs in the SpaceAware assessor enclave. Only the signed result returns to your private channel — input MPE is never disclosed.',
    });
  });

  it('returns a fresh object each call (never a shared mutable reference)', () => {
    const a = buildProvenanceView();
    const b = buildProvenanceView();
    expect(a).not.toBe(b);
    expect(a).toEqual(b);
  });

  it('CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE is explanatory, non-empty', () => {
    expect(CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE.length).toBeGreaterThan(20);
  });
});

// ---------------------------------------------------------------------------
// loadConjunctionSources — fetch orchestration (fake apiClient, never
// throws even when every endpoint fails)
// ---------------------------------------------------------------------------

describe('loadConjunctionSources', () => {
  function fakeApiClient(handlers: Record<string, unknown>): ConjunctionApiClient {
    return {
      requestJson: async <T,>(path: string) => {
        if (!(path in handlers)) throw new Error(`unexpected path ${path}`);
        const value = handlers[path];
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as ConjunctionApiClient;
  }

  it('parses a successful set of responses', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [{ peer_id: 'p1', addrs: [] }] },
      '/channels': { count: 0, results: [] },
      '/stats': { total_records: 99, schemas: [], total_bytes: 10, connected_peers: 1 },
    });
    const data = await loadConjunctionSources(apiClient);
    expect(data.peers).toEqual([{ peerId: 'p1', dn: null }]);
    expect(data.channels).toEqual([]);
    expect(data.stats?.totalRecords).toBe(99);
  });

  it('never rejects — every endpoint failing resolves to an honest empty/null snapshot', async () => {
    const apiClient = fakeApiClient({
      '/peers': new Error('offline'),
      '/channels': new Error('offline'),
      '/stats': new Error('offline'),
    });
    const data = await loadConjunctionSources(apiClient);
    expect(data).toEqual({ peers: [], channels: [], stats: null });
  });

  it('degrades each surface independently (peers ok, channels/stats fail)', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [] },
      '/channels': new Error('down'),
      '/stats': new Error('down'),
    });
    const data = await loadConjunctionSources(apiClient);
    expect(data.peers).toEqual([]);
    expect(data.channels).toEqual([]);
    expect(data.stats).toBeNull();
  });

  it('feeds directly into buildSourceRows to produce the live-minimal single row', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [] },
      '/channels': { count: 0, results: [] },
      '/stats': { total_records: 5, schemas: [], total_bytes: 1, connected_peers: 0 },
    });
    const data = await loadConjunctionSources(apiClient);
    const rows = buildSourceRows(data.peers, data.channels, data.stats);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.id).toBe(LOCAL_CATALOG_SOURCE_ID);
  });
});

// ---------------------------------------------------------------------------
// Cross-cutting sanity: no source row is silently duplicated by id
// ---------------------------------------------------------------------------

describe('buildSourceRows id uniqueness', () => {
  it('never produces duplicate row ids for a plausible mixed fixture', () => {
    const peers: ConjunctionSourcePeer[] = [
      { peerId: 'peer-1', dn: 'Provider One' },
      { peerId: 'peer-2', dn: null },
    ];
    const channels = [channel({ standardCode: 'MPE', encryptionState: 'encrypted' }), channel({ standardCode: 'OMM' })];
    const rows = buildSourceRows(peers, channels, stats({ totalRecords: 1 }));
    const ids = rows.map((r) => r.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
});
