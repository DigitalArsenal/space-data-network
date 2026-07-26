/*
 * Permission gating + peer/operator add-form logic (graph task
 * nst-node-edit-permissions-ui deliverable 3; wire contract
 * nst-node-admin-contract §7, §8, §9.4).
 *
 * Pure: no fetch, no design imports. The view renders what these functions
 * allow, and the node re-checks every one of them — this module makes the
 * UI honest, it is not the security boundary.
 */

import { normalizeTrust, trustRank, TRUST_TIERS } from './trust.js';
import { parseVCard, extractIdentity } from './vcard.js';

const ADMIN = trustRank('admin');
const STANDARD = trustRank('standard');

/** Admin+ may edit the node's published identity (contract §6). */
export const canEditNodeProfile = (level) => trustRank(level) >= ADMIN;
/** Admin+ may read/modify the operator + peer registries (§7, §8). */
export const canManagePermissions = (level) => trustRank(level) >= ADMIN;
/** Standard+ may prove key possession (§7 — the attest endpoint's own wall). */
export const canAttest = (level) => trustRank(level) >= STANDARD;

/**
 * Trust levels assignable to an OPERATOR (the xpub user store).
 * Contract §7 ceiling `assignableTrustLevel`: only unknown…admin. `ultimate`
 * is reserved for the node's own identity; `never` as an operator lockout is
 * not supported by the node and is refused 400. A session can additionally
 * never grant above its own tier.
 */
export const USER_ASSIGNABLE_TIERS = ['unknown', 'marginal', 'standard', 'full', 'admin'];

/**
 * Trust levels assignable to a PEER (the libp2p registry).
 * `ultimate` is NOT offered: it means "this identity IS this node"
 * (contract §9.4 — the API currently accepts it, so the refusal is ours).
 */
export const PEER_ASSIGNABLE_TIERS = ['never', 'unknown', 'marginal', 'standard', 'full', 'admin'];

/** @returns {string[]} operator tiers this session may grant, high→low order preserved. */
export function assignableUserTiers(sessionLevel) {
  const ceiling = Math.min(trustRank(sessionLevel), ADMIN);
  if (trustRank(sessionLevel) < ADMIN) return [];
  return USER_ASSIGNABLE_TIERS.filter((t) => trustRank(t) <= ceiling);
}

/** @returns {string[]} peer tiers this session may set (empty below Admin). */
export function assignablePeerTiers(sessionLevel) {
  if (trustRank(sessionLevel) < ADMIN) return [];
  return PEER_ASSIGNABLE_TIERS.filter((t) => trustRank(t) <= Math.min(trustRank(sessionLevel), ADMIN));
}

/** Highest-first ordering for a tier list rendered in a <select>. */
export function tiersHighestFirst(tiers) {
  return [...tiers].sort((a, b) => trustRank(b) - trustRank(a));
}

/**
 * Classify what an operator pasted into the "add" box.
 *   'vcard'      — a contact card (may carry its own peer id / xpub)
 *   'xpub'       — a wallet account key: an OPERATOR identity, and for a
 *                  PEER it must be converted with peerIdFromXpub first (§8)
 *   'peer_id'    — a libp2p peer id
 *   'public_key' — hex or base64 key material the node can parse (§8)
 */
export function classifyPeerInput(raw) {
  const value = String(raw ?? '').trim();
  if (!value) return { kind: 'empty', value: '' };
  if (/BEGIN:VCARD/i.test(value)) return { kind: 'vcard', value };
  if (/^(xpub|tpub|ypub|zpub)[1-9A-HJ-NP-Za-km-z]+$/.test(value)) return { kind: 'xpub', value };
  if (/^(12D3KooW|16Uiu2HA|Qm|1[1-9A-HJ-NP-Za-km-z]{20,})/.test(value) && !/\s/.test(value)) {
    return { kind: 'peer_id', value };
  }
  const hex = value.replace(/^0x/i, '');
  if (/^[0-9a-fA-F]+$/.test(hex) && hex.length % 2 === 0 && hex.length >= 64) {
    return { kind: 'public_key', value };
  }
  if (/^[A-Za-z0-9+/_-]{40,}={0,2}$/.test(value)) return { kind: 'public_key', value };
  return { kind: 'unknown', value };
}

/**
 * Read a pasted contact card WITHOUT inventing anything: name/org come from
 * the card's own FN/ORG, identity from its X-SDN-PEER-ID or its
 * verification-chain email aliases. Absent ⇒ empty.
 */
export function peerFromVCardText(text) {
  const props = parseVCard(text);
  const identity = extractIdentity(props);
  const first = (name) => props.find((p) => p.name === name)?.value ?? '';
  return {
    peerId: identity.peerId ?? '',
    xpub: identity.xpub ?? '',
    name: first('FN'),
    organization: first('ORG'),
    vcard: String(text ?? '').trim(),
  };
}

/**
 * Body for POST/PUT /api/peers (contract §8). `id` and `public_key` are the
 * only identity inputs the node accepts — an xpub must already have been
 * converted by the caller (peerIdFromXpub) and passed as `id`.
 * Only non-empty optional fields are included: the peer registry must never
 * be handed contact data that was not supplied.
 */
export function buildAddPeerBody({ id = '', publicKey = '', trustLevel, name = '', organization = '', notes = '', vcard = '' }) {
  const body = { trust_level: normalizeTrust(trustLevel) };
  const peerId = String(id ?? '').trim();
  const key = String(publicKey ?? '').trim();
  if (peerId) body.id = peerId;
  if (key) body.public_key = key;
  const opt = { name, organization, notes, vcard };
  for (const [k, v] of Object.entries(opt)) {
    const trimmed = String(v ?? '').trim();
    if (trimmed) body[k] = trimmed;
  }
  return body;
}

/** Body for POST /api/auth/users (contract §7). */
export function buildAddUserBody({ xpub, name = '', trustLevel, signingPubKeyHex = '' }) {
  const body = {
    xpub: String(xpub ?? '').trim(),
    name: String(name ?? '').trim(),
    trust_level: normalizeTrust(trustLevel),
  };
  const key = String(signingPubKeyHex ?? '').trim().toLowerCase().replace(/^0x/, '');
  if (key) body.signing_pubkey_hex = key;
  return body;
}

/**
 * An operator entry is "pending proof" until a signing key is bound to it:
 * the admin approved the xpub, but nobody has yet demonstrated possession
 * (first successful verify binds it via TOFU — contract §5.3).
 */
export function userNeedsKeyProof(user) {
  return !String(user?.signing_pubkey_hex ?? '').trim();
}

/** Tier list for a read-only legend, most-trusted first. */
export const ALL_TIERS = TRUST_TIERS;
