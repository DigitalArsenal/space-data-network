/*
 * Minimal vCard 4.0 (RFC 6350) reader for the status dashboard.
 *
 * The feed's VCARD strings come from two producers: peers.TrustedPeerToVCard
 * (FN/ORG/NOTE + X-SDN-* extended props) and the node's own EPM-derived card
 * (which may additionally carry TITLE/EMAIL/TEL/ADR/URL). This parser handles
 * the generic grammar — line unfolding, property parameters, escaped values,
 * repeated properties — rather than either producer's exact shape.
 */

/**
 * @typedef {Object} VCardProp
 * @property {string} name   Uppercased property name (e.g. "FN", "X-SDN-TRUST-LEVEL")
 * @property {Record<string,string>} params Uppercased param name → raw value
 * @property {string} value  Unescaped property value
 */

/** Unescape RFC 6350 text values (\\ \, \; \n). */
function unescapeValue(raw) {
  return raw.replace(/\\([\\,;nN])/g, (_, ch) => (ch === 'n' || ch === 'N' ? '\n' : ch));
}

/**
 * Parse vCard text into an ordered property list. Tolerant: bad lines are
 * skipped, BEGIN/END/VERSION scaffolding is dropped, folding accepts both
 * CRLF and bare LF.
 * @param {string} text
 * @returns {VCardProp[]}
 */
export function parseVCard(text) {
  if (!text || typeof text !== 'string') return [];
  // Unfold: a CRLF (or LF) followed by space/tab continues the previous line.
  const unfolded = text.replace(/\r?\n[ \t]/g, '');
  const props = [];
  for (const line of unfolded.split(/\r?\n/)) {
    const idx = line.indexOf(':');
    if (idx <= 0) continue;
    const head = line.slice(0, idx);
    const value = unescapeValue(line.slice(idx + 1).trim());
    const [rawName, ...rawParams] = head.split(';');
    const name = rawName.trim().toUpperCase();
    if (!name || name === 'BEGIN' || name === 'END' || name === 'VERSION') continue;
    const params = {};
    for (const p of rawParams) {
      const eq = p.indexOf('=');
      if (eq > 0) params[p.slice(0, eq).trim().toUpperCase()] = p.slice(eq + 1).trim();
    }
    props.push({ name, params, value });
  }
  return props;
}

/** Human labels for the props the modal displays in preferred order. */
export const VCARD_FIELD_LABELS = {
  FN: 'NAME',
  N: 'STRUCTURED NAME',
  ORG: 'ORGANIZATION',
  TITLE: 'TITLE',
  ROLE: 'ROLE',
  EMAIL: 'EMAIL',
  TEL: 'TEL',
  ADR: 'ADDRESS',
  URL: 'URL',
  NOTE: 'NOTE',
  'X-SDN-PEER-ID': 'SDN PEER ID',
  'X-SDN-TRUST-LEVEL': 'SDN TRUST',
  'X-SDN-GROUP': 'SDN GROUP',
  'X-SDN-MULTIADDR': 'SDN MULTIADDR',
};

/**
 * Group parsed props for display: known fields first (VCARD_FIELD_LABELS
 * order), then any remaining extended/custom props, each as {label, values[]}.
 * @param {VCardProp[]} props
 */
export function displayFields(props) {
  const byName = new Map();
  for (const p of props) {
    if (!p.value) continue;
    const list = byName.get(p.name) ?? [];
    // ADR keeps its component separators readable; others pass through.
    list.push(p.name === 'ADR' ? p.value.split(';').filter(Boolean).join(', ') : p.value);
    byName.set(p.name, list);
  }
  const fields = [];
  for (const name of Object.keys(VCARD_FIELD_LABELS)) {
    if (byName.has(name)) {
      fields.push({ name, label: VCARD_FIELD_LABELS[name], values: byName.get(name) });
      byName.delete(name);
    }
  }
  for (const [name, values] of byName) {
    fields.push({ name, label: name, values });
  }
  return fields;
}

const MACHINE_PROPS = new Set(['X-SDN-MULTIADDR', 'X-SDN-PEER-ID']);

/*
 * SDN identity email aliases (owner rule): because phones drop X- properties
 * on vCard import, machine identity rides in EMAIL items shaped
 * <value>@<kind>.spacedatanetwork.org. Kinds: xpub (HD extended public key,
 * verbatim local part), sign/encrypt (HD derivation paths, base64url local
 * part — apostrophes/slashes are not email-safe), bitcoin/ethereum/solana
 * (chain addresses, verbatim). The UI decodes these for display and hides
 * the synthetic emails from the contact rows.
 */
const ALIAS_DOMAIN_SUFFIX = '.spacedatanetwork.org';
const PATH_ALIAS_KINDS = new Set(['sign', 'encrypt']);
const ADDRESS_ALIAS_KINDS = new Set(['bitcoin', 'ethereum', 'solana']);

/** base64url (raw, unpadded) → string, null if invalid. */
export function decodeB64Url(value) {
  try {
    const b64 = value.replace(/-/g, '+').replace(/_/g, '/');
    const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
    return globalThis.atob(padded);
  } catch {
    return null;
  }
}

/** Split an alias email into {kind, local}; null when not an SDN alias. */
function aliasParts(value) {
  const at = value.lastIndexOf('@');
  if (at <= 0) return null;
  const domain = value.slice(at + 1).toLowerCase();
  if (!domain.endsWith(ALIAS_DOMAIN_SUFFIX)) return null;
  const kind = domain.slice(0, -ALIAS_DOMAIN_SUFFIX.length);
  if (!kind || kind.includes('.')) return null;
  return { kind, local: value.slice(0, at) };
}

/** True when an EMAIL prop is an SDN machine alias (not a human contact). */
export function isAliasEmail(prop) {
  return prop.name === 'EMAIL' && aliasParts(prop.value) !== null;
}

/**
 * Pull the SDN identity out of parsed props: xpub, decoded sign/encrypt
 * derivation paths, chain addresses, peer id, EPM CID.
 * @param {VCardProp[]} props
 */
export function extractIdentity(props) {
  const id = { xpub: '', signPath: '', encryptPath: '', addresses: {}, peerId: '', epmCid: '' };
  for (const p of props) {
    if (p.name === 'X-SDN-PEER-ID') {
      id.peerId = p.value;
      continue;
    }
    if (p.name === 'X-SDN-EPM-CID') {
      id.epmCid = p.value;
      continue;
    }
    if (p.name !== 'EMAIL') continue;
    const alias = aliasParts(p.value);
    if (!alias) continue;
    if (alias.kind === 'xpub') id.xpub = alias.local;
    else if (PATH_ALIAS_KINDS.has(alias.kind)) {
      const decoded = decodeB64Url(alias.local) ?? alias.local;
      if (alias.kind === 'sign') id.signPath = decoded;
      else id.encryptPath = decoded;
    } else if (ADDRESS_ALIAS_KINDS.has(alias.kind)) id.addresses[alias.kind] = alias.local;
  }
  return id;
}

function escapeV3(value) {
  return value.replace(/\\/g, '\\\\').replace(/\r?\n/g, '\\n').replace(/,/g, '\\,').replace(/;/g, '\\;');
}

function foldLine(line) {
  const MAX = 75;
  if (line.length <= MAX) return line;
  const parts = [];
  while (line.length > MAX) {
    parts.push(line.slice(0, MAX));
    line = ' ' + line.slice(MAX);
  }
  parts.push(line);
  return parts.join('\r\n');
}

/**
 * Compact VERSION:3.0 card for QR display — same shape as the node's
 * GetNodeQRVCard: N/FN + human contact fields + the identity email aliases,
 * built client-side from the feed's vCard so it works for every node.
 * @param {{dn?: string, org?: string, peerId?: string}} node
 * @param {VCardProp[]} props
 */
export function buildCompactVCard(node, props) {
  const first = (name) => props.find((p) => p.name === name)?.value ?? '';
  const fn = first('FN') || node.dn?.trim() || node.org?.trim() || node.peerId || 'SDN Node';
  const lines = ['BEGIN:VCARD', 'VERSION:3.0'];
  const n = first('N');
  lines.push('N:' + (n || ';' + escapeV3(fn) + ';;;'));
  lines.push('FN:' + escapeV3(fn));
  for (const name of ['ORG', 'TITLE', 'TEL']) {
    const v = first(name);
    if (v) lines.push(name + ':' + escapeV3(v));
  }
  const adr = props.find((p) => p.name === 'ADR');
  if (adr) lines.push('ADR;TYPE=WORK:' + adr.value);
  for (const p of props) {
    if (p.name !== 'EMAIL') continue;
    const alias = aliasParts(p.value);
    if (alias) lines.push(`EMAIL;TYPE=INTERNET;TYPE=${alias.kind}:${p.value}`);
    else lines.push('EMAIL;TYPE=INTERNET:' + escapeV3(p.value));
  }
  lines.push('END:VCARD');
  return lines.map(foldLine).join('\r\n') + '\r\n';
}

/**
 * Flatten a card's text content for search indexing (semantic + substring).
 * Multiaddrs/peer-id stay searchable but are appended last so prose fields
 * dominate.
 * @param {VCardProp[]} props
 */
export function vcardSearchText(props) {
  const prose = [];
  const machine = [];
  for (const p of props) {
    if (!p.value) continue;
    (MACHINE_PROPS.has(p.name) ? machine : prose).push(p.value);
  }
  return [...prose, ...machine].join(' ');
}

/**
 * Prose-only card text for the EMBEDDING input. Base58 peer ids and
 * multiaddrs explode into dozens of junk WordPiece tokens that dilute the
 * mean-pooled sentence vector, so they are excluded here — exact-identifier
 * lookups are the substring search's job.
 * @param {VCardProp[]} props
 */
export function vcardProseText(props) {
  return props
    .filter((p) => p.value && !MACHINE_PROPS.has(p.name))
    .map((p) => p.value)
    .join(' ');
}
