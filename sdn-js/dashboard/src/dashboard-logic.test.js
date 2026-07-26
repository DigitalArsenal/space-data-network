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
    expect(id.signPath).toBe("m/44'/0'/7'/0'/0'");
    expect(id.encryptPath).toBe("m/44'/0'/7'/1'/0'");
    expect(id.addresses.bitcoin).toBe('bc1qexample');
    expect(id.peerId).toBe('16UiuPeer');
    expect(id.epmCid).toBe('bafyExample');
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
