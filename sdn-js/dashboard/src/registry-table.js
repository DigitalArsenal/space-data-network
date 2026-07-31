/*
 * THE ACCOUNTS TABLES, MADE SEARCHABLE AND SORTABLE — and TRUST given its own
 * column.
 *
 * Owner, 2026-07-31: "Yeah, the accounts table does not have the trust as a
 * separate column either." It was true of both of them: OPERATOR KEYS and the
 * PEER REGISTRY each carried the tier only inside the subscript metaline, where
 * it cannot be sorted, cannot be filtered, and reads as one of four facts on a
 * dim line rather than as the column the operator is actually scanning for.
 *
 * The vocabulary is trust.js's — the SAME tier grammar the peers table uses
 * (NodeTable.svelte). Two surfaces that render a tier in two grammars is the
 * mess the owner named about the tables themselves; a second trust vocabulary
 * would be the same defect one level down.
 *
 * WHICH TIER THE COLUMN HOLDS, for a peer: `trust_level` — the operator's own
 * assertion, the value the EDIT modal writes. `effective_trust_level` is what
 * the node COMPUTES from the web of trust, and it stays on the subscript line
 * beside it: sorting a table by a value no control on the page can set would
 * make the column look like an input when it is an outcome. They are two facts
 * and the page keeps saying both.
 *
 * Pure: no Svelte, no design imports, no fetch. Every function takes its rows.
 */

import { normalizeTrust, trustRank } from './trust.js';

/**
 * One operator-key row's searchable text. The xpub and the derived peer id are
 * INCLUDED — an operator pasting an identifier into the box is the commonest
 * way this table is used, and a search that only matched display names would
 * fail exactly then.
 *
 * @param {any} user the /api/auth/users row
 * @param {string} [derivedPeerId] peer id derived from the xpub in the page
 */
export function operatorSearchText(user, derivedPeerId = '') {
  return [
    user?.name,
    user?.organization,
    user?.xpub,
    derivedPeerId,
    normalizeTrust(user?.trust_level),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
}

/** One peer-registry row's searchable text: name, org, id, addresses, tiers. */
export function peerSearchText(peer) {
  return [
    peer?.name,
    peer?.organization,
    peer?.id,
    normalizeTrust(peer?.trust_level),
    normalizeTrust(peer?.effective_trust_level),
    ...(Array.isArray(peer?.addrs) ? peer.addrs : []),
    ...(Array.isArray(peer?.groups) ? peer.groups : []),
    peer?.notes,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
}

/**
 * Substring search, every whitespace-separated term required (AND) — the same
 * rule filters.js applies to the peers table, so the box behaves identically on
 * both surfaces.
 */
export function searchRows(rows, query, textOf) {
  const terms = String(query ?? '').trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return [...(rows ?? [])];
  return (rows ?? []).filter((row) => {
    const text = textOf(row);
    return terms.every((t) => text.includes(t));
  });
}

/** Tier filter: 'all', or one canonical tier from trust.js. */
export function filterTier(rows, tier, tierOf = (r) => r?.trust_level) {
  if (!tier || tier === 'all') return [...(rows ?? [])];
  return (rows ?? []).filter((row) => normalizeTrust(tierOf(row)) === tier);
}

/**
 * Column sorters, keyed by the header they sit under. `dir` is 1 for the
 * sorter's natural order and -1 for its reverse — the same contract
 * filters.js:sortNodes uses, so a column header behaves the same way on every
 * table this page has.
 *
 * TRUST sorts MOST-TRUSTED FIRST at dir=1: the operator scanning a registry for
 * "who can do what here" is looking for the top of that list, and admin rows
 * buried under an alphabetical run of unknowns is the sort nobody wants.
 */
export const OPERATOR_SORTERS = {
  xpub: (a, b) => String(a?.xpub ?? '').localeCompare(String(b?.xpub ?? '')),
  peer: (a, b) => String(a?.__peerId ?? '').localeCompare(String(b?.__peerId ?? '')),
  name: (a, b) => nameKey(a).localeCompare(nameKey(b)),
  trust: (a, b) => trustRank(b?.trust_level) - trustRank(a?.trust_level),
};

export const PEER_SORTERS = {
  peer: (a, b) => String(a?.id ?? '').localeCompare(String(b?.id ?? '')),
  name: (a, b) => nameKey(a).localeCompare(nameKey(b)),
  trust: (a, b) => trustRank(b?.trust_level) - trustRank(a?.trust_level),
};

/**
 * An unnamed row sorts LAST in the natural direction, never first: `'￿'` is
 * the same sentinel filters.js uses for an absent org. A blank at the top of an
 * alphabetical sort reads as a broken table.
 */
function nameKey(row) {
  const name = String(row?.name ?? '').trim();
  return (name || '￿').toLowerCase();
}

/** Sort a registry list by column key. Stable-by-construction: it copies first. */
export function sortRows(rows, key, dir = 1, sorters = PEER_SORTERS) {
  const cmp = sorters[key] ?? sorters.name;
  return [...(rows ?? [])].sort((a, b) => cmp(a, b) * dir);
}

/**
 * The whole pipeline, in the order the peers table runs it:
 *   filter by tier → search → sort
 * so the count under a table ("N OF M SHOWN") is always the truth about what the
 * operator is looking at.
 */
export function applyRegistryView(rows, { query = '', tier = 'all', sortKey = 'trust', sortDir = 1 } = {}, {
  textOf = peerSearchText,
  tierOf = (r) => r?.trust_level,
  sorters = PEER_SORTERS,
} = {}) {
  return sortRows(searchRows(filterTier(rows, tier, tierOf), query, textOf), sortKey, sortDir, sorters);
}

/**
 * The tiers actually PRESENT in a list, most-trusted first, so the filter
 * control offers what is there rather than the whole scale with seven empty
 * options. 'all' is prepended by the view.
 */
export function tiersPresent(rows, tierOf = (r) => r?.trust_level) {
  const seen = new Set((rows ?? []).map((r) => normalizeTrust(tierOf(r))));
  return [...seen].sort((a, b) => trustRank(b) - trustRank(a));
}
