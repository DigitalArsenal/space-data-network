/*
 * Pure node-list pipeline for the status dashboard:
 *
 *   visible = trustFilter(offlineFilter(nodes)) → searched → sorted
 *
 * Kept free of Svelte/design imports so vitest covers the exact logic the
 * view runs. Semantic search plugs in as a peerId→score map produced by
 * semantic.js; substring search is computed here and always available.
 */

import { parseVCard, vcardSearchText, vcardProseText } from './vcard.js';
import { normalizeTrust, trustRank, hasTrustAssertion } from './trust.js';
import { peerSource } from './peers.js';

/**
 * One node's flattened searchable text (lowercased): DN, org, trust tier,
 * role, geo, agent, versions, vCard content, peer id, addrs.
 * @param {import('../../src/status/view-model').NodeStatusView} node
 */
export function nodeSearchText(node) {
  return [
    node.dn,
    node.org,
    normalizeTrust(node.trustLevel),
    node.role,
    node.geoLabel,
    node.agent,
    node.suiteVersion,
    node.standardsVersion,
    node.online ? 'online' : 'offline',
    vcardSearchText(parseVCard(node.vcard)),
    node.peerId,
    ...(node.addrs ?? []),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
}

/**
 * Prose-only text the semantic engine embeds for one node. Machine
 * identifiers (peer id, multiaddrs, version strings) are deliberately
 * excluded — their WordPiece explosion drowns the sentence vector (measured
 * locally: exact-phrase queries scored below the 0.25 floor with them
 * included). Substring search still covers them via nodeSearchText.
 * @param {import('../../src/status/view-model').NodeStatusView} node
 */
export function nodeEmbedText(node) {
  return [
    node.dn,
    node.org,
    hasTrustAssertion(node.trustLevel) ? `trust ${normalizeTrust(node.trustLevel)}` : '',
    node.role,
    node.geoLabel,
    node.online ? 'online' : 'offline',
    vcardProseText(parseVCard(node.vcard)),
  ]
    .filter(Boolean)
    .join('. ');
}

/**
 * Visibility settings filter.
 * @param {import('../../src/status/view-model').NodeStatusView[]} nodes
 * @param {{trustTier: string, hideUntrustedOffline: boolean}} settings
 *   trustTier: 'all' or a canonical tier from trust.js TRUST_TIERS.
 *   hideUntrustedOffline: when true (the default), offline nodes WITHOUT an
 *   explicit trust assertion are hidden. Self is always shown.
 */
export function applySettings(nodes, { trustTier = 'all', hideUntrustedOffline = true } = {}) {
  return nodes.filter((n) => {
    if (n.isSelf) return trustTier === 'all' || normalizeTrust(n.trustLevel) === trustTier;
    if (hideUntrustedOffline && !n.online && !hasTrustAssertion(n.trustLevel)) return false;
    if (trustTier !== 'all' && normalizeTrust(n.trustLevel) !== trustTier) return false;
    return true;
  });
}

/**
 * Substring search over nodeSearchText. Empty query → all nodes.
 * Every whitespace-separated term must match (AND).
 */
export function substringSearch(nodes, query, textOf = nodeSearchText) {
  const terms = (query ?? '').trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return nodes;
  return nodes.filter((n) => {
    const text = textOf(n);
    return terms.every((t) => text.includes(t));
  });
}

/**
 * Merge semantic scores with the substring result for one query:
 * nodes scoring >= floor OR matching by substring survive; ordered by score
 * desc (substring-only hits keep a floor score so they never vanish).
 * @param {import('../../src/status/view-model').NodeStatusView[]} nodes visible set
 * @param {Map<string, number>} scores peerId → cosine similarity
 * @param {Set<string>} substringIds peerIds matched by substring
 * @param {number} floor minimum cosine to count as a semantic hit (0.18:
 *   measured locally against the int8 MiniLM — multi-clause node docs score
 *   ~0.28-0.5 on on-topic queries, <0.1 off-topic)
 * @returns {{node: any, score: number}[]}
 */
export function semanticRank(nodes, scores, substringIds, floor = 0.18) {
  const out = [];
  for (const n of nodes) {
    const score = scores.get(n.peerId) ?? -1;
    const textHit = substringIds.has(n.peerId);
    if (score >= floor || textHit) out.push({ node: n, score: textHit ? Math.max(score, floor) : score });
  }
  out.sort((a, b) => b.score - a.score);
  return out;
}

/**
 * Provenance order for the SOURCE column: the rows that need explaining come
 * first. A config pin is the one an operator cannot change from this page, an
 * operator pin is the one they chose, a connection is self-explanatory, and
 * "not stated" is a defect that belongs where it will be seen — at the top of
 * the descending sort, never buried.
 */
const SOURCE_ORDER = { unknown: 0, config: 1, pinned: 2, connected: 3, account: 4 };

/** Column sorters for the table. Key → comparator over NodeStatusView. */
export const SORTERS = {
  node: (a, b) => (a.dn || a.org || a.peerId).localeCompare(b.dn || b.org || b.peerId),
  org: (a, b) => (a.org || '￿').localeCompare(b.org || '￿'),
  source: (a, b) =>
    (SOURCE_ORDER[peerSource(a).id] ?? 0) - (SOURCE_ORDER[peerSource(b).id] ?? 0),
  trust: (a, b) => trustRank(b.trustLevel) - trustRank(a.trustLevel),
  status: (a, b) => Number(b.online) - Number(a.online),
  geo: (a, b) => (a.geoLabel || '￿').localeCompare(b.geoLabel || '￿'),
  agent: (a, b) => (a.agent || '￿').localeCompare(b.agent || '￿'),
  seen: (a, b) => (b.isSelf ? Infinity : b.lastSeen) - (a.isSelf ? Infinity : a.lastSeen),
};

/**
 * Sort nodes by column key; self always first on equal footing (keeps the
 * emitting node discoverable), dir=1 asc / -1 desc relative to the sorter's
 * natural order.
 */
export function sortNodes(nodes, key, dir = 1) {
  const cmp = SORTERS[key] ?? SORTERS.node;
  return [...nodes].sort((a, b) => {
    if (a.isSelf !== b.isSelf) return a.isSelf ? -1 : 1;
    return cmp(a, b) * dir;
  });
}
