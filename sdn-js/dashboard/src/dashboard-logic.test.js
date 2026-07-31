/*
 * Unit coverage for the dashboard v2 pure logic (graph task
 * nst-dashboard-table): vCard parsing, trust tiers, the visibility settings
 * (trust filter + hide-untrusted-offline checkbox), substring search,
 * semantic rank merge, sorting, and the WordPiece tokenizer.
 */
import { describe, expect, it } from 'vitest';

import { parseVCard, displayFields, vcardSearchText } from './vcard.js';
import { normalizeTrust, trustRank, hasTrustAssertion, TRUST_TIERS } from './trust.js';
import {
  nodeSearchText,
  nodeEmbedText,
  applySettings,
  substringSearch,
  semanticRank,
  sortNodes,
} from './filters.js';
import { wordpieceTokenize, cosine, textHash } from './semantic.js';
import { formatLastSeen } from './format.js';
import {
  peerSource,
  isPeerRow,
  lastSeenLabel,
  presenceSummary,
  mapCoverage,
  hasFix,
} from './peers.js';

const CARD = [
  'BEGIN:VCARD',
  'VERSION:4.0',
  'FN:Celestrak Ops Node',
  'ORG:CelesTrak',
  'NOTE:Primary supplemental OMM publisher\\, Europe region',
  'EMAIL;TYPE=work:ops@example.org',
  'X-SDN-PEER-ID:12D3KooWExample',
  'X-SDN-TRUST-LEVEL:full',
  'X-SDN-MULTIADDR:/ip4/1.2.3.4/tcp/4001',
  'X-SDN-MULTIADDR:/ip4/1.2.3.4/udp/4001/quic-v1',
  'END:VCARD',
].join('\r\n');

function makeNode(overrides = {}) {
  return {
    peerId: '12D3KooWExample',
    dn: 'Celestrak Ops Node',
    org: 'CelesTrak',
    vcard: CARD,
    lat: 40.0,
    lon: -105.0,
    geoLabel: 'Boulder, United States',
    online: true,
    isSelf: false,
    agent: 'sdn/1.0.3',
    uptimeS: 0,
    lastSeen: 1_700_000_000,
    addrs: ['/ip4/1.2.3.4/tcp/4001'],
    trustLevel: 'full',
    role: '',
    latencyMs: 12,
    suiteVersion: '1.0.3',
    standardsVersion: '1.155.0',
    ...overrides,
  };
}

describe('vcard', () => {
  it('parses folded, escaped vCard 4.0 with repeated and extended props', () => {
    const props = parseVCard(CARD);
    const byName = (n) => props.filter((p) => p.name === n);
    expect(byName('FN')[0].value).toBe('Celestrak Ops Node');
    expect(byName('NOTE')[0].value).toBe('Primary supplemental OMM publisher, Europe region');
    expect(byName('EMAIL')[0].params.TYPE).toBe('work');
    expect(byName('X-SDN-MULTIADDR')).toHaveLength(2);
    // scaffolding dropped
    expect(props.some((p) => p.name === 'BEGIN' || p.name === 'VERSION')).toBe(false);
  });

  it('unfolds continuation lines', () => {
    const folded = 'BEGIN:VCARD\r\nNOTE:one two\r\n  three\r\nEND:VCARD';
    expect(parseVCard(folded)[0].value).toBe('one two three');
  });

  it('groups display fields with known labels first', () => {
    const fields = displayFields(parseVCard(CARD));
    expect(fields[0].label).toBe('NAME');
    expect(fields.find((f) => f.name === 'X-SDN-MULTIADDR').values).toHaveLength(2);
  });

  it('search text puts prose before machine ids', () => {
    const text = vcardSearchText(parseVCard(CARD));
    expect(text.indexOf('Europe')).toBeGreaterThan(-1);
    expect(text.indexOf('Europe')).toBeLessThan(text.indexOf('12D3KooW'));
  });

  it('tolerates empty/garbage input', () => {
    expect(parseVCard('')).toEqual([]);
    expect(parseVCard(null)).toEqual([]);
    expect(parseVCard('no colon line\n:leading colon')).toEqual([]);
  });
});

describe('trust', () => {
  it('normalizes feed strings and ranks tiers', () => {
    expect(normalizeTrust('FULL')).toBe('full');
    expect(normalizeTrust('')).toBe('unknown');
    expect(normalizeTrust('bogus')).toBe('unknown');
    expect(trustRank('ultimate')).toBeGreaterThan(trustRank('admin'));
    expect(trustRank('never')).toBeLessThan(trustRank('unknown'));
    expect(TRUST_TIERS).toContain('marginal');
  });

  it('treats every explicit tier as an assertion, including never', () => {
    expect(hasTrustAssertion('full')).toBe(true);
    expect(hasTrustAssertion('never')).toBe(true);
    expect(hasTrustAssertion('unknown')).toBe(false);
    expect(hasTrustAssertion('')).toBe(false);
  });
});

describe('applySettings (trust filter + hide-untrusted-offline checkbox)', () => {
  const self = makeNode({ peerId: 'self', isSelf: true, online: true, trustLevel: '' });
  const onlineUnknown = makeNode({ peerId: 'a', online: true, trustLevel: '' });
  const offlineUnknown = makeNode({ peerId: 'b', online: false, trustLevel: '' });
  const offlineFull = makeNode({ peerId: 'c', online: false, trustLevel: 'full' });
  const offlineNever = makeNode({ peerId: 'd', online: false, trustLevel: 'never' });
  const all = [self, onlineUnknown, offlineUnknown, offlineFull, offlineNever];

  it('default (checked): hides offline nodes without a trust assertion', () => {
    const ids = applySettings(all, { hideUntrustedOffline: true }).map((n) => n.peerId);
    expect(ids).toEqual(['self', 'a', 'c', 'd']); // offline-unknown hidden; never/full kept
  });

  it('unchecked: shows everything', () => {
    expect(applySettings(all, { hideUntrustedOffline: false })).toHaveLength(5);
  });

  it('trust tier filter applies on top', () => {
    const ids = applySettings(all, { trustTier: 'full', hideUntrustedOffline: false }).map((n) => n.peerId);
    expect(ids).toEqual(['c']);
  });

  it('self is exempt from the offline rule', () => {
    const offSelf = makeNode({ peerId: 'self', isSelf: true, online: false, trustLevel: '' });
    expect(applySettings([offSelf], { hideUntrustedOffline: true })).toHaveLength(1);
  });
});

describe('search', () => {
  const nodes = [
    makeNode({ peerId: 'a' }),
    makeNode({
      peerId: 'b',
      dn: 'Backup Node',
      org: 'OtherOrg',
      geoLabel: 'Berlin, Germany',
      vcard: '',
    }),
  ];

  it('substring search matches vCard content (AND across terms)', () => {
    expect(substringSearch(nodes, 'europe publisher').map((n) => n.peerId)).toEqual(['a']);
    expect(substringSearch(nodes, 'berlin').map((n) => n.peerId)).toEqual(['b']);
    expect(substringSearch(nodes, '')).toHaveLength(2);
    expect(substringSearch(nodes, 'nomatchxyz')).toHaveLength(0);
  });

  it('nodeEmbedText keeps prose but excludes machine identifiers', () => {
    const text = nodeEmbedText(nodes[0]);
    for (const frag of ['Celestrak Ops Node', 'Boulder', 'trust full', 'Europe region']) {
      expect(text).toContain(frag);
    }
    expect(text).not.toContain('/ip4/');
    expect(text).not.toContain('12D3KooW');
  });

  it('nodeSearchText includes trust tier, geo and addrs', () => {
    const text = nodeSearchText(nodes[0]);
    for (const frag of ['full', 'boulder', 'celestrak', '/ip4/1.2.3.4/tcp/4001', 'online']) {
      expect(text).toContain(frag);
    }
  });

  it('semanticRank orders by score and never drops substring hits', () => {
    const scores = new Map([
      ['a', 0.9],
      ['b', 0.05],
    ]);
    const ranked = semanticRank(nodes, scores, new Set(['b']), 0.25);
    expect(ranked.map((r) => r.node.peerId)).toEqual(['a', 'b']);
    // substring-only hit is floored, not dropped
    expect(ranked[1].score).toBe(0.25);
    // neither semantic nor substring → dropped
    const rankedNone = semanticRank(nodes, new Map([['a', 0.9]]), new Set(), 0.25);
    expect(rankedNone.map((r) => r.node.peerId)).toEqual(['a']);
  });
});

describe('sortNodes', () => {
  const nodes = [
    makeNode({ peerId: 'low', trustLevel: 'marginal', online: false }),
    makeNode({ peerId: 'high', trustLevel: 'ultimate' }),
    makeNode({ peerId: 'me', isSelf: true, trustLevel: '' }),
  ];

  it('self pins first; trust sorts most-trusted first; dir flips', () => {
    expect(sortNodes(nodes, 'trust', 1).map((n) => n.peerId)).toEqual(['me', 'high', 'low']);
    expect(sortNodes(nodes, 'trust', -1).map((n) => n.peerId)).toEqual(['me', 'low', 'high']);
    expect(sortNodes(nodes, 'status', 1).map((n) => n.peerId)).toEqual(['me', 'high', 'low']);
  });

  it('SOURCE sorts the rows that need explaining to the top', () => {
    const bySource = [
      makeNode({ peerId: 'live', source: 'connected' }),
      makeNode({ peerId: 'cfg', source: 'config' }),
      makeNode({ peerId: 'mute', source: '' }),
      makeNode({ peerId: 'pin', source: 'pinned' }),
    ];
    expect(sortNodes(bySource, 'source', 1).map((n) => n.peerId)).toEqual([
      'mute', // SOURCE NOT STATED is a defect and is never buried
      'cfg',
      'pin',
      'live',
    ]);
  });
});

/*
 * PEER PROVENANCE — the owner asked four questions on 2026-07-30 and every one
 * of them is a case below. The feed he was looking at carried 35 peers, 34 of
 * them DHT advertisements this node had never dialled, one of them named by a
 * hardcoded placeholder string.
 */
describe('peers — how did this row get here?', () => {
  it('names each admitted source in words an operator can read', () => {
    expect(peerSource({ source: 'config' }).label).toBe('FROM CONFIG FILE');
    expect(peerSource({ source: 'pinned' }).label).toBe('PINNED BY OPERATOR');
    expect(peerSource({ source: 'connected' }).label).toBe('CONNECTED NOW');
  });

  /*
   * AMENDED 2026-07-30 (IRIS ruling, sdn-peer-modal-trust-apply-honesty §6).
   * This test used to REQUIRE the config location in the rendered sentence —
   * the earlier "park it in the note" ruling. That ruling is revoked: the owner
   * flagged the rendered path twice, and `sentence` is displayed text (the
   * peers table renders it, and so did the modal). The note is still carried as
   * DATA, because the copy affordance sends it to the clipboard verbatim; what
   * changed is that no rendered string may contain it.
   */
  it('a config pin is pinned AND locked, and never says where', () => {
    const src = peerSource({
      source: 'config',
      pinNote: '/etc/space-data-network/config.yaml  peers.trusted_peers',
    });
    expect(src.pinned).toBe(true);
    expect(src.locked).toBe(true);
    // The note is the REAL file and key, and it stays in the data.
    expect(src.note).toContain('peers.trusted_peers');
    // The SENTENCE is what a peer's row and its modal print.
    expect(src.sentence).not.toContain('peers.trusted_peers');
    expect(src.sentence).not.toContain('/etc');
    expect(src.sentence).toBe("Pinned by this node's config file — edit it there, not here.");
    // …and it does not vary with the note, so there is no path through this
    // function that puts a location on screen.
    expect(peerSource({ source: 'config' }).sentence).toBe(src.sentence);
  });

  it('an unstated source is admitted as unstated, never guessed into a good answer', () => {
    for (const node of [{}, { source: '' }, { source: 'bogus' }, null]) {
      const src = peerSource(node);
      expect(src.id).toBe('unknown');
      expect(src.label).toBe('SOURCE NOT STATED');
      expect(src.tone).toBe('amber');
    }
  });

  it('a connected peer that is ALSO pinned keeps both facts', () => {
    const src = peerSource({ source: 'connected', pinned: true });
    expect(src.id).toBe('connected');
    expect(src.pinned).toBe(true);
    expect(src.sentence).toContain('pinned');
  });

  it('an operator account is not a peer, so it is never counted as one', () => {
    expect(isPeerRow({ source: 'account' })).toBe(false);
    expect(isPeerRow({ source: 'connected' })).toBe(true);
    expect(peerSource({ source: 'account' }).label).toBe('OPERATOR ACCOUNT');
  });
});

describe('LAST SEEN — never the bare word "never" again', () => {
  const NOW = 1_700_000_000_000;

  it('a pinned peer this node has not dialled says exactly that', () => {
    expect(lastSeenLabel({ source: 'pinned', lastSeen: 0 }, NOW)).toBe('PINNED · NOT YET SEEN');
    expect(lastSeenLabel({ source: 'config', lastSeen: 0 }, NOW)).toBe('PINNED · NOT YET SEEN');
  });

  it('a real observation is still a real relative time', () => {
    expect(lastSeenLabel({ source: 'connected', lastSeen: 1_699_999_970 }, NOW)).toBe('30s ago');
  });

  it('self reports uptime, not a last sighting', () => {
    expect(lastSeenLabel({ isSelf: true, uptimeS: 3700 }, NOW)).toBe('UP 1h 1m');
  });

  it('the word "never" is gone from the formatter itself', () => {
    expect(formatLastSeen(0, NOW)).toBe('not yet seen');
    expect(lastSeenLabel({ lastSeen: 0 }, NOW)).toBe('NOT YET SEEN');
  });
});

describe('the count and the map must agree on screen', () => {
  const rows = [
    makeNode({ peerId: 'self', isSelf: true, lat: 1, lon: 1 }),
    makeNode({ peerId: 'live', source: 'connected', online: true, lat: 40, lon: -105 }),
    makeNode({ peerId: 'pin', source: 'pinned', online: false, lat: 0, lon: 0, lastSeen: 0 }),
    makeNode({ peerId: 'cfg', source: 'config', online: false, lat: 0, lon: 0, lastSeen: 0 }),
    // An Admin-overlay account with no peer presence: not a peer, not a dot.
    makeNode({ peerId: 'op', source: 'account', online: false, lat: 0, lon: 0 }),
  ];

  it('decomposes the header count into the two reasons a row is admitted', () => {
    const p = presenceSummary(rows);
    expect(p).toEqual({ connected: 1, pinnedOffline: 2, unexplained: 0, total: 3 });
  });

  it('counts an unadmitted row as UNEXPLAINED rather than absorbing it', () => {
    // This is the 2026-07-30 feed: offline, never seen, nobody pinned it.
    const ghost = makeNode({ peerId: 'ghost', source: '', online: false, lastSeen: 0 });
    expect(presenceSummary([...rows, ghost]).unexplained).toBe(1);
  });

  it('states map coverage, because 0,0 is no location and cannot be drawn', () => {
    expect(hasFix({ lat: 0, lon: 0 })).toBe(false);
    expect(hasFix({ lat: 0, lon: -105 })).toBe(true);
    // 3 peers, 1 plotted — the exact shape of "it says 35 peers but shows one".
    expect(mapCoverage(rows)).toEqual({ peers: 3, plotted: 1, unplaced: 2 });
  });
});

/*
 * THE PEER MODAL'S FOUR HONESTY RULES (IRIS ruling 2026-07-30,
 * sdn-peer-modal-trust-apply-honesty). Every case here is something the page
 * told the owner that it could not back up.
 */
describe('a config location is data, never a rendered string', async () => {
  const { pinNoteIsPublishable } = await import('./peers.js');

  it('a config pin never publishes its note', () => {
    expect(
      pinNoteIsPublishable({
        source: 'config',
        note: '/etc/space-data-network/config.module-delivery-sidecar.yaml · peers.trusted_peers',
      })
    ).toBe(false);
  });

  it("an operator's own prose note is publishable", () => {
    expect(pinNoteIsPublishable({ source: 'pinned', note: 'the celestrak box' })).toBe(true);
  });

  it('…but not when what they typed IS a location', () => {
    // The source says an operator pinned it; the note says otherwise. The note
    // is what reaches the screen, so the note is what decides.
    expect(pinNoteIsPublishable({ source: 'pinned', note: '/etc/foo.yaml' })).toBe(false);
    expect(pinNoteIsPublishable({ source: 'pinned', note: 'peers.trusted_peers' })).toBe(false);
    expect(pinNoteIsPublishable({ source: 'pinned', note: 'C:\\sdn\\config.toml' })).toBe(false);
  });

  it('an absent note is nothing to publish either way', () => {
    expect(pinNoteIsPublishable({ source: 'pinned' })).toBe(true);
    expect(pinNoteIsPublishable({ source: 'config' })).toBe(false);
  });
});

describe('the contact card states what this node measured, not a status code', async () => {
  const { cardState, everConnected } = await import('./peers.js');

  it('never seen + failed read = there is no card to serve', () => {
    expect(cardState({ read: 'done', ok: false, everConnected: false })).toBe('not-served-here');
  });

  it('seen before + failed read = it did not come back this time', () => {
    expect(cardState({ read: 'done', ok: false, everConnected: true })).toBe('did-not-load');
  });

  it('distinguishes in-flight from never-attempted', () => {
    expect(cardState({ read: 'reading' })).toBe('reading');
    expect(cardState({ read: 'idle' })).toBe('not-read');
    expect(cardState({})).toBe('not-read');
  });

  it('a card that came back is just the card', () => {
    expect(cardState({ read: 'done', ok: true, everConnected: false })).toBe('ok');
  });

  it('"ever connected" is the two facts the node HAS: a sighting, or a live link', () => {
    expect(everConnected({ lastSeen: 1_700_000_000, online: false })).toBe(true);
    expect(everConnected({ lastSeen: 0, online: true })).toBe(true);
    expect(everConnected({ lastSeen: 0, online: false })).toBe(false);
    // No row in the feed at all — this node has never had it on the line.
    expect(everConnected(null)).toBe(false);
  });
});

describe('the name ladder — and who said the name', async () => {
  const { peerDisplayName } = await import('./peers.js');

  it("prefers what the peer publishes about itself", () => {
    expect(
      peerDisplayName({ peer: { name: 'Celestrak Ops' }, cardFN: 'Card Name', pin: { name: 'the eth box' } })
    ).toEqual({ name: 'Celestrak Ops', origin: 'peer' });
  });

  it('then the FN of a card this node actually read', () => {
    expect(peerDisplayName({ peer: {}, cardFN: 'Card Name', pin: { name: 'label' } })).toEqual({
      name: 'Card Name',
      origin: 'peer',
    });
  });

  it("then an operator's own label, MARKED as one", () => {
    expect(peerDisplayName({ peer: {}, pin: { name: 'the eth box' } })).toEqual({
      name: 'the eth box',
      origin: 'operator',
    });
  });

  it('and otherwise "unnamed" — never unknown, never an id promoted', () => {
    const bare = peerDisplayName({ peer: { id: '16Uiu2HAmQMSobG4' }, pin: { note: 'a note' } });
    expect(bare).toEqual({ name: 'unnamed', origin: 'none' });
    // Not the id, not the note, not the organization, not a provenance label.
    expect(
      peerDisplayName({ peer: { id: '16Uiu2HAm', organization: 'CelesTrak' } }).name
    ).toBe('unnamed');
    expect(peerDisplayName({}).name).toBe('unnamed');
  });
});

/*
 * THE URL GRAMMAR (§5). Hash, and forced: the Go handler answers 404 for every
 * path but `/`, so a path route survives exactly until reload.
 */
describe('route-url — the address bar is the state', async () => {
  const { parseHash, formatHash, ROUTES } = await import('./route-url.js');
  const PEER = '16Uiu2HAmQMSobG4rFZHS9wTQbYTzAKg5tNReY7sRUMah9CcuvDMv';
  const XPUB = 'xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKrhko4egpiMZbpiaQL2jkwSB1icqYh2cfDfVxdx4df189oLKnC5fSwqPfgyP3hooxujYzAu3fDVmz';

  it('round-trips every shape the ruling names', () => {
    for (const hash of [
      '#/node',
      '#/peers',
      '#/accounts',
      '#/accounts/keys',
      '#/accounts/peers',
      `#/accounts/peers/${PEER}`,
      `#/accounts/keys/${XPUB}`,
      `#/peers/${PEER}`,
      '#/peers/self',
      '#/peers/self/qr',
    ]) {
      expect(formatHash(parseHash(hash)), hash).toBe(hash);
    }
  });

  it('names the modal each shape opens', () => {
    expect(parseHash(`#/accounts/peers/${PEER}`)).toEqual({
      route: 'accounts', sub: 'peers', modal: 'peer', modalId: PEER, view: '',
    });
    expect(parseHash(`#/accounts/keys/${XPUB}`)).toEqual({
      route: 'accounts', sub: 'keys', modal: 'operator', modalId: XPUB, view: '',
    });
    expect(parseHash(`#/peers/${PEER}`)).toEqual({
      route: 'peers', sub: '', modal: 'node', modalId: PEER, view: '',
    });
    expect(parseHash('#/peers/self/qr')).toEqual({
      route: 'peers', sub: '', modal: 'self', modalId: 'self', view: 'qr',
    });
  });

  it('an empty or unparseable hash is the landing route, never a blank page', () => {
    const landing = { route: 'node', sub: '', modal: '', modalId: '', view: '' };
    for (const hash of ['', '#', '#/', '#/nope', '#/nope/deeper', 'garbage']) {
      expect(parseHash(hash), hash).toEqual(landing);
    }
    // A subsection this route does not have is dropped, not invented.
    expect(parseHash('#/accounts/wat')).toEqual(landing2('accounts'));
    expect(parseHash('#/node/anything')).toEqual(landing2('node'));
    function landing2(route) {
      return { route, sub: '', modal: '', modalId: '', view: '' };
    }
  });

  it('a modal is addressed under its OWN route, however it was opened', () => {
    // The self card opened from the NODE dashboard is still the peers card:
    // one dialog, one address, or Back is ambiguous.
    expect(formatHash({ route: 'node', modal: 'self', modalId: 'self' })).toBe('#/peers/self');
    expect(formatHash({ route: 'node', modal: 'node', modalId: PEER })).toBe(`#/peers/${PEER}`);
    expect(formatHash({ route: 'peers', sub: 'keys', modal: 'peer', modalId: PEER })).toBe(
      `#/accounts/peers/${PEER}`
    );
  });

  it('closing a modal is its parent route, which is what CLOSE replaces to', () => {
    const open = parseHash(`#/accounts/peers/${PEER}`);
    expect(formatHash({ ...open, modal: '', modalId: '', view: '' })).toBe('#/accounts/peers');
  });

  it('nothing but the route, the subsection and the modal is addressable', () => {
    // Filters, sort, page and the sign-in dialog are NOT places (§5), so no
    // shape here can carry them. ROUTES is the whole vocabulary.
    expect(Object.keys(ROUTES)).toEqual(['node', 'peers', 'accounts']);
    expect(ROUTES.node).toEqual([]);
    expect(ROUTES.peers).toEqual([]);
    expect(ROUTES.accounts).toEqual(['peers', 'keys']);
  });

  it('a half-escaped fragment is not an id', () => {
    expect(parseHash('#/peers/%E0%A4%A')).toEqual({
      route: 'peers', sub: '', modal: '', modalId: '', view: '',
    });
  });
});

describe('semantic primitives', () => {
  it('wordpiece tokenizes with continuations and [UNK]', () => {
    const vocab = new Map(
      Object.entries({ '[UNK]': 100, play: 1, '##ing': 2, ',': 3, ground: 4 })
    );
    expect(wordpieceTokenize('Playing, ground', vocab)).toEqual([1, 2, 3, 4]);
    expect(wordpieceTokenize('zzzz', vocab)).toEqual([100]);
  });

  it('cosine of identical normalized vectors is 1', () => {
    const v = new Float32Array([0.6, 0.8]);
    expect(cosine(v, v)).toBeCloseTo(1);
    expect(cosine(v, new Float32Array([0.8, -0.6]))).toBeCloseTo(0);
  });

  it('textHash is stable and change-sensitive', () => {
    expect(textHash('abc')).toBe(textHash('abc'));
    expect(textHash('abc')).not.toBe(textHash('abd'));
  });
});

describe('identity aliases', async () => {
  const { extractIdentity, isAliasEmail, decodeB64Url, buildCompactVCard } = await import('./vcard.js');
  const b64url = (s) => Buffer.from(s).toString('base64url');
  const CARD_ID = [
    'BEGIN:VCARD',
    'VERSION:3.0',
    'N:Koury;TJ;;;',
    'FN:TJ Koury',
    'TITLE:Director',
    'ORG:DigitalArsenal',
    'TEL:+1-555-0100',
    'EMAIL;TYPE=INTERNET:tj@example.org',
    'EMAIL;type=INTERNET;type=xpub:xpub661MyMwAqRbcF@xpub.spacedatanetwork.org',
    `EMAIL;type=INTERNET;type=sign:${b64url("m/44'/0'/7'/0'/0'")}@sign.spacedatanetwork.org`,
    `EMAIL;type=INTERNET;type=encrypt:${b64url("m/44'/0'/7'/1'/0'")}@encrypt.spacedatanetwork.org`,
    'EMAIL;type=INTERNET;type=bitcoin:bc1qexample@bitcoin.spacedatanetwork.org',
    'X-SDN-PEER-ID:16UiuPeer',
    'X-SDN-EPM-CID:bafyExample',
    'END:VCARD',
  ].join('\r\n');

  it('decodes xpub, derivation paths, chain addresses, peer id, EPM CID', () => {
    const id = extractIdentity(parseVCard(CARD_ID));
    expect(id.xpub).toBe('xpub661MyMwAqRbcF');
    expect(id.signPaths).toEqual(["m/44'/0'/7'/0'/0'"]);
    expect(id.encryptPaths).toEqual(["m/44'/0'/7'/1'/0'"]);
    expect(id.addresses.bitcoin).toBe('bc1qexample');
    expect(id.peerId).toBe('16UiuPeer');
    expect(id.epmCid).toBe('bafyExample');
  });

  it('decodes signature-chain and pubkey aliases (owner verification rule)', () => {
    const sigB64 = Buffer.from('a1b2c3d4', 'hex').toString('base64url');
    const card = [
      'BEGIN:VCARD',
      'VERSION:3.0',
      'FN:Chain Node',
      `EMAIL;type=INTERNET;type=epmsig:${sigB64}@epmsig.spacedatanetwork.org`,
      'EMAIL;type=INTERNET;type=epmts:1785069259@epmts.spacedatanetwork.org',
      'EMAIL;type=INTERNET;type=epmcid:bafkreichain@epmcid.spacedatanetwork.org',
      'EMAIL;type=INTERNET;type=signing:0d80e1fd@signing.spacedatanetwork.org',
      'EMAIL;type=INTERNET;type=encryption:0213dc85@encryption.spacedatanetwork.org',
      'EMAIL;type=INTERNET;type=peer:16UiuAliasPeer@peer.spacedatanetwork.org',
      'END:VCARD',
    ].join('\r\n');
    const id = extractIdentity(parseVCard(card));
    expect(id.epmSignature).toBe('a1b2c3d4');
    expect(id.epmSignedAt).toBe('1785069259');
    expect(id.epmCid).toBe('bafkreichain');
    expect(id.signingKeys).toEqual(['0d80e1fd']);
    expect(id.encryptionKeys).toEqual(['0213dc85']);
    expect(id.peerId).toBe('16UiuAliasPeer');
  });

  it('classifies alias vs contact emails', () => {
    const props = parseVCard(CARD_ID);
    const emails = props.filter((p) => p.name === 'EMAIL');
    expect(emails.filter(isAliasEmail)).toHaveLength(4);
    expect(emails.filter((p) => !isAliasEmail(p)).map((p) => p.value)).toEqual(['tj@example.org']);
  });

  it('decodeB64Url round-trips and rejects garbage', () => {
    expect(decodeB64Url(b64url("m/44'/0'/0'"))).toBe("m/44'/0'/0'");
    expect(decodeB64Url('!!!not-base64!!!')).toBeNull();
  });

  it('buildCompactVCard keeps contact fields and identity aliases (v3)', () => {
    const props = parseVCard(CARD_ID);
    const compact = buildCompactVCard({ dn: 'TJ Koury', peerId: '16UiuPeer' }, props);
    expect(compact).toContain('VERSION:3.0');
    expect(compact).toContain('FN:TJ Koury');
    expect(compact).toContain('TEL:+1-555-0100');
    const unfolded = compact.replace(/\r\n /g, '');
    expect(unfolded).toContain('@xpub.spacedatanetwork.org');
    expect(unfolded).toContain('@sign.spacedatanetwork.org');
    expect(unfolded).toContain('EMAIL;TYPE=INTERNET:tj@example.org');
    // every physical line folded to <= 75 octets
    for (const line of compact.split('\r\n')) expect(line.length).toBeLessThanOrEqual(75);
  });
});
