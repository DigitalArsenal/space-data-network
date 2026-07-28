/*
 * ACCOUNTS — one list for nodes and logins (contract §16).
 *
 * Owner: "we are not differentiating between a node running somewhere and an
 * account that will login to the site as they are both the same thing."
 *
 * TWO TIERS, and the anonymous one is the existing one (§16.5):
 *   · signed out → the public /ws/status peer feed, exactly as the NODES page
 *     rendered it. `/api/accounts` is Admin-only and MUST NOT be called; a 401
 *     on every public page load is noise that trains operators to ignore real
 *     ones.
 *   · Admin session → `/api/accounts` is fetched and MERGED IN, which is what
 *     surfaces operator-only facets (xpub, can_sign_in, source, sign-in counts)
 *     and `kind: operator` rows that have no peer presence at all.
 *
 * The merge itself is the server's (§16.1) — this module only overlays the
 * server's rows onto the feed rows by peer id. It never invents a merge key:
 * an operator with no parseable xpub carries an empty peer id and is listed on
 * its own rather than folded into someone else's row.
 */

import { normalizeTrust, trustRank } from './trust.js';

/** Feed row → account row. The anonymous tier, and always the base. */
export function accountFromNode(node) {
  return {
    peerId: node.peerId ?? '',
    xpub: '',
    kind: 'peer',
    name: (node.dn ?? '').trim(),
    organization: (node.org ?? '').trim(),
    trustLevel: normalizeTrust(node.trustLevel),
    canSignIn: false,
    source: '',
    connectionCount: 0,
    lastConnected: 0,
    // Everything the table and the modal still read straight off the feed.
    node,
    online: Boolean(node.online),
    isSelf: Boolean(node.isSelf),
  };
}

/** `GET /api/accounts` row → the same shape, for operator-only entries. */
export function accountFromEntry(entry) {
  return {
    peerId: entry.peer_id ?? '',
    xpub: entry.xpub ?? '',
    kind: entry.kind ?? 'operator',
    name: (entry.name ?? '').trim(),
    organization: (entry.organization ?? '').trim(),
    trustLevel: normalizeTrust(entry.trust_level),
    canSignIn: Boolean(entry.can_sign_in),
    source: entry.source ?? '',
    connectionCount: Number(entry.connection_count ?? 0),
    lastConnected: Number(entry.last_connected ?? 0),
    node: null,
    online: false,
    isSelf: false,
  };
}

/**
 * Overlay the Admin `/api/accounts` rows onto the anonymous feed rows.
 *
 * Feed rows stay authoritative for live presence (online, isSelf, geo, vCard);
 * server rows contribute identity and login facets. A server row whose peer id
 * matches no feed row is appended — that is exactly the operator who has never
 * connected as a peer. An EMPTY peer id never matches anything (§16.1).
 */
export function mergeAccounts(feedRows, entries) {
  const rows = feedRows.map((r) => ({ ...r }));
  const byPeer = new Map();
  for (const row of rows) if (row.peerId) byPeer.set(row.peerId, row);

  for (const entry of entries ?? []) {
    const server = accountFromEntry(entry);
    const existing = server.peerId ? byPeer.get(server.peerId) : null;
    if (!existing) {
      rows.push(server);
      if (server.peerId) byPeer.set(server.peerId, server);
      continue;
    }
    existing.kind = server.kind;
    existing.xpub = server.xpub;
    existing.canSignIn = server.canSignIn;
    existing.source = server.source;
    existing.connectionCount = server.connectionCount;
    existing.lastConnected = server.lastConnected;
    if (server.name) existing.name = server.name;
    if (server.organization) existing.organization = server.organization;
    // §16.2: the server already reconciled the two facets to the HIGHER tier.
    // Take it rather than re-deriving policy in the UI.
    existing.trustLevel = server.trustLevel;
  }
  return rows;
}

/**
 * The NAME column's primary line. An account with no name reads "unknown" —
 * never a blank cell, and never an identifier promoted to look like a name.
 */
export function accountDisplayName(row) {
  const name = (row?.name ?? '').trim();
  if (name) return name;
  const org = (row?.organization ?? '').trim();
  if (org) return org;
  return 'unknown';
}

/** True when the primary line is the placeholder, so the view can dim it. */
export const isUnnamed = (row) => accountDisplayName(row) === 'unknown';

/** The identifier shown under the name: peer id preferred, else xpub. */
export function accountIdentifier(row) {
  return (row?.peerId ?? '').trim() || (row?.xpub ?? '').trim();
}

/** Badge text for the kind facet. */
export const KIND_LABEL = { peer: 'NODE', operator: 'LOGIN', both: 'NODE + LOGIN' };
export const kindLabel = (kind) => KIND_LABEL[kind] ?? 'NODE';

/**
 * Which underlying record an edit targets (§16.4.5). A `both` row has TWO,
 * and guessing between them is exactly what the ruling forbids.
 */
export function editTargets(row) {
  const targets = [];
  if (row?.peerId && (row.kind === 'peer' || row.kind === 'both')) {
    targets.push({ facet: 'peer', label: 'NODE TRUST', id: row.peerId, path: `/api/peers/${encodeURIComponent(row.peerId)}/trust` });
  }
  if (row?.xpub && (row.kind === 'operator' || row.kind === 'both')) {
    targets.push({ facet: 'operator', label: 'LOGIN TRUST', id: row.xpub, path: `/api/auth/users/${encodeURIComponent(row.xpub)}` });
  }
  return targets;
}

/**
 * STANDING RULE (owner, 2026-07-28): the ACCOUNTS listing NEVER contains this
 * node itself. SELF has its own page — THIS NODE — and a duplicate row there
 * is noise, not information. Applied at the source so no caller can
 * reintroduce it, and so counts/pagination derive from the same filtered set.
 */
export function withoutSelf(rows) {
  return (rows ?? []).filter((r) => !r?.isSelf);
}

/** Sort: most-trusted first, then by display name. */
export function sortAccounts(rows) {
  return [...rows].sort((a, b) => {
    const t = trustRank(b.trustLevel) - trustRank(a.trustLevel);
    if (t) return t;
    return accountDisplayName(a).localeCompare(accountDisplayName(b));
  });
}
