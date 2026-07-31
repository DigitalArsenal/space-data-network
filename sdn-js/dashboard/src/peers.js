/*
 * PEER PROVENANCE — "how did this row get here?", answered without devtools.
 *
 * Owner, 2026-07-30, four ways in one sitting:
 *   · "The Peers table should not ever show peers that have never been seen,
 *      UNLESS they have been added manually and 'pinned' (we need an interface
 *      for that). When a peer drops off the network it should just disappear."
 *   · "I have no idea what these peers are that are in the table"
 *   · "it says 35 peers, but only shows one on the globe … rows that say
 *      'last seen - never', so how did they get there?"
 *   · "what does the first row 'config trusted peer' mean?"
 *
 * WHAT THE FEED ACTUALLY CARRIED THAT DAY (Hermes measured wss://sdn.spaceaware.io
 * /ws/status): 36 rows = 1 self + 35 peers, 33 offline, ALL 35 with LAST_SEEN=0.
 * 34 had an empty DN and one identical AGENT_VERSION — DHT rendezvous
 * ADVERTISEMENTS this node had never dialled. Exactly one had a NAME, and the
 * name was the literal string 'Config Trusted Peer', hardcoded in the node. The
 * name was carrying PROVENANCE because provenance had nowhere else to go.
 *
 * It has somewhere now. Admission is enforced SERVER-SIDE (Hermes): a row
 * reaches this page only if it is PINNED or CURRENTLY CONNECTED, and a peer that
 * drops off vanishes on the next frame. Every row states which of the two it is
 * in `source`, and this module is the vocabulary for that field.
 *
 * NOTHING HERE INVENTS A FACT THE FEED DID NOT SEND. An unstated source reads
 * SOURCE NOT STATED — visible, amber, and wrong-looking — rather than being
 * guessed into one of the good answers. That is the whole lesson of the fake
 * name: a manufactured label is worse than an admitted blank.
 *
 * Pure: no fetch, no design imports, no DOM. `tone` values are theme TOKEN NAMES
 * (keystate.js's rule) so this module can be unit-tested and can never fork the
 * palette.
 */

import { formatLastSeen, formatUptime } from './format.js';
import { parseVCard } from './vcard.js';

/**
 * WHERE A ROW CAME FROM, in the owner's words rather than the wire's.
 *
 *   config     a pin written into the node's config file. `pinNote` is the real
 *              FILE + KEY an operator edits — it is not a human note, and it is
 *              never rendered as text (§6): it is data, and the only way it
 *              leaves this page is the clipboard, on an explicit click.
 *   pinned     an operator pinned it on this node. `pinNote` is their own note.
 *   connected  it has a live connection right now. It is listed BECAUSE of that,
 *              and it disappears when it disconnects (owner ruling 1).
 *   account    not a peer at all: an operator account from the Admin overlay
 *              that has never connected. Kept distinct so it can be excluded
 *              from every peer count instead of inflating one.
 *   unknown    the feed said nothing. A defect, rendered as one.
 */
export function peerSource(node) {
  const raw = String(node?.source ?? '').trim().toLowerCase();
  const note = String(node?.pinNote ?? '').trim();
  const flagged = Boolean(node?.pinned);

  if (raw === 'config') {
    return {
      id: 'config',
      label: 'FROM CONFIG FILE',
      tone: 'textDim',
      pinned: true,
      locked: true,
      note,
      // THE PATH IS NOT IN THE SENTENCE (IRIS ruling 2026-07-30 §6, revoking the
      // earlier parking ruling). This string used to interpolate `note` — the
      // real file and key — and the owner named the result twice on screen:
      // `/etc/space-data-network/config.module-delivery-sidecar.yaml ·
      // peers.trusted_peers`. An address the operator can reach from this page
      // is vocabulary; a location on a box they may not even have a shell on is
      // a leak. `note` stays in the DATA (the copy affordance sends it to the
      // clipboard verbatim) and never reaches a rendered string.
      sentence: "Pinned by this node's config file — edit it there, not here.",
    };
  }
  if (raw === 'pinned') {
    return {
      id: 'pinned',
      label: 'PINNED BY OPERATOR',
      tone: 'ice',
      pinned: true,
      locked: false,
      note,
      sentence:
        'An operator pinned this peer on this node, so it stays listed even while it is not connected.',
    };
  }
  if (raw === 'connected') {
    return {
      id: 'connected',
      label: 'CONNECTED NOW',
      tone: 'green',
      pinned: flagged,
      locked: false,
      note,
      sentence: flagged
        ? 'Connected right now, and pinned: it stays listed after it disconnects.'
        : 'Connected right now. That is the only reason it is listed — it disappears when it disconnects.',
    };
  }
  if (raw === 'account') {
    return {
      id: 'account',
      label: 'OPERATOR ACCOUNT',
      tone: 'textDim',
      pinned: false,
      locked: false,
      note,
      sentence: 'An operator account registered on this node. It has never connected as a peer.',
    };
  }
  return {
    id: 'unknown',
    label: 'SOURCE NOT STATED',
    tone: 'amber',
    pinned: flagged,
    locked: false,
    note,
    sentence:
      'This node did not say how this row got here. Every row should be pinned or connected.',
  };
}

/** True for rows that are peers — an operator account is not one. */
export const isPeerRow = (node) => peerSource(node).id !== 'account';

/**
 * LAST SEEN, which must never again be the bare word "never" (owner: "rows that
 * say 'last seen - never', so how did they get there?"). A pinned peer the node
 * has not yet dialled is a legitimate state and now SAYS SO — the cell answers
 * the question the bare word provoked.
 */
export function lastSeenLabel(node, nowMs = Date.now()) {
  if (node?.isSelf) return `UP ${formatUptime(node?.uptimeS)}`;
  const ts = Math.floor(Number(node?.lastSeen ?? 0));
  if (ts > 0) return formatLastSeen(ts, nowMs);
  const src = peerSource(node);
  if (src.pinned) return 'PINNED · NOT YET SEEN';
  if (node?.online) return 'CONNECTED NOW';
  return 'NOT YET SEEN';
}

/**
 * THE COUNT, decomposed so it cannot disagree with the table under it.
 * `unexplained` is the invariant check: under server-side admission it is
 * always 0, and a page that shows it non-zero is showing a real regression.
 */
export function presenceSummary(nodes) {
  let connected = 0;
  let pinnedOffline = 0;
  let unexplained = 0;
  for (const n of nodes ?? []) {
    if (n?.isSelf || !isPeerRow(n)) continue;
    if (n?.online) {
      connected += 1;
      continue;
    }
    if (peerSource(n).pinned) pinnedOffline += 1;
    else unexplained += 1;
  }
  return { connected, pinnedOffline, unexplained, total: connected + pinnedOffline + unexplained };
}

/** 0,0 is "no location", not the Gulf of Guinea (PeerMap's standing rule). */
export const hasFix = (node) => Number(node?.lat ?? 0) !== 0 || Number(node?.lon ?? 0) !== 0;

/**
 * WHY THE GLOBE SHOWS FEWER DOTS THAN THE TABLE HAS ROWS. A pinned peer with no
 * GeoIP fix is invisible on a map by construction, so the map states its own
 * coverage instead of letting the two numbers disagree in silence.
 */
export function mapCoverage(nodes) {
  const peers = (nodes ?? []).filter((n) => !n?.isSelf && isPeerRow(n));
  const plotted = peers.filter(hasFix).length;
  return { peers: peers.length, plotted, unplaced: peers.length - plotted };
}

/** Multiaddrs a pasted card declares — used verbatim, never synthesized. */
export function multiaddrsFromVCard(text) {
  const out = [];
  for (const prop of parseVCard(text)) {
    if (prop.name !== 'X-SDN-MULTIADDR') continue;
    const value = (prop.value ?? '').trim();
    if (value && !out.includes(value)) out.push(value);
  }
  return out;
}

/**
 * Body for POST /api/peers/pins. Lowercase synthesized keys (standing
 * capitalization rule), and only what was actually supplied — the pin registry
 * must never be handed contact data nobody typed.
 */
export function buildPinBody({ peerId = '', addrs = [], name = '', note = '' } = {}) {
  const body = { peer_id: String(peerId ?? '').trim() };
  const list = (addrs ?? []).map((a) => String(a ?? '').trim()).filter(Boolean);
  if (list.length) body.addrs = list;
  const n = String(name ?? '').trim();
  if (n) body.name = n;
  const t = String(note ?? '').trim();
  if (t) body.note = t;
  return body;
}

/** A config-file pin cannot be removed from this page (the API answers 409). */
export const pinIsLocked = (pin) => String(pin?.source ?? '').trim().toLowerCase() === 'config';

/** The pin's own provenance badge, in the same vocabulary as a row's. */
export function pinSourceLabel(pin) {
  return pinIsLocked(pin) ? 'FROM CONFIG FILE' : 'PINNED BY OPERATOR';
}

/**
 * A LOCATION ON A BOX, recognised so it can be refused.
 *
 * Absolute unix/windows paths, config FILE names, and dotted config KEY paths
 * standing alone at the end of a value. It is deliberately conservative in one
 * direction only: a false positive costs an operator's own note its cell, a
 * false negative puts a path back on the owner's screen for the third time.
 */
const RAW_LOCATION =
  /(^|\s)(~?\/(etc|var|usr|opt|home|srv|root)\/|[A-Za-z]:\\)|\.(ya?ml|toml|db|sqlite|conf|ini|service|socket)\b|\b[a-z_]+(\.[a-z_]+){1,}\b\s*$/;

/**
 * A config pin's "note" is a filesystem path and a key, not prose — so it is
 * PUBLISHABLE only when an operator wrote it themselves and it does not read
 * like a location (IRIS ruling 2026-07-30 §6a). The only place a config
 * location may go is the clipboard, on an explicit click.
 */
export const pinNoteIsPublishable = (pin) =>
  !pinIsLocked(pin) && !RAW_LOCATION.test(String(pin?.note ?? '').trim());

/**
 * NAME rule (§16.4.3): never an identifier promoted — and "unnamed", never
 * "unknown" (IRIS ruling 2026-07-30 §4). `unknown` is a TRUST TIER rendered in
 * the same modal head: one word, two meanings, six inches apart.
 */
export const pinDisplayName = (pin) => String(pin?.name ?? '').trim() || 'unnamed';

/**
 * HAS THIS NODE EVER HAD THIS PEER ON THE LINE? The two facts the node actually
 * measured — a recorded sighting, or a live connection right now. Nothing here
 * infers NAT, a firewall, or reachability: this node cannot measure any of
 * them, and the contact card's copy turns on this and only this.
 */
export const everConnected = (node) =>
  Number(node?.lastSeen ?? 0) > 0 || node?.online === true;

/**
 * THE CONTACT CARD READ, as a state rather than as an HTTP code.
 *
 * A contact card is served by the PEER THAT OWNS IT. A failed read against a
 * peer this node has never connected to is not an error — there is no card to
 * serve — and rendering `HTTP 521` as the answer (which is what the owner saw)
 * states the transport where the fact belongs.
 *
 * `read` is 'idle' (never attempted), 'reading' (in flight) or 'done'.
 * @returns {'ok'|'not-read'|'reading'|'not-served-here'|'did-not-load'}
 */
export function cardState({ read = 'idle', ok = false, everConnected: seen = false } = {}) {
  if (read === 'reading') return 'reading';
  if (read !== 'done') return 'not-read';
  if (ok) return 'ok';
  return seen ? 'did-not-load' : 'not-served-here';
}

/**
 * WHOSE NAME IS ON THE TITLE, and who said it.
 *
 * First non-empty wins: what the peer publishes about itself, then the FN of a
 * card this node actually read, then the label an operator typed into the pin
 * registry, then nothing — and "nothing" says `unnamed`, it does not promote an
 * id, an organization, a pin note, or a provenance label to the NAME line. That
 * last refusal is the `Config Trusted Peer` lesson: a manufactured name is worse
 * than an admitted blank.
 *
 * Pure, and meant to be `$derived` in the view rather than copied into state:
 * a name that arrives with a later card read supersedes without a refresh.
 *
 * @returns {{ name: string, origin: 'peer'|'operator'|'none' }}
 */
export function peerDisplayName({ peer, cardFN = '', pin = null } = {}) {
  const published = String(peer?.name ?? '').trim();
  if (published) return { name: published, origin: 'peer' };
  const fn = String(cardFN ?? '').trim();
  if (fn) return { name: fn, origin: 'peer' };
  const label = String(pin?.name ?? '').trim();
  if (label) return { name: label, origin: 'operator' };
  return { name: 'unnamed', origin: 'none' };
}

/**
 * "PINNED 3h ago" — tolerant of unix seconds or RFC3339, and ABSENT when the
 * node did not say, because an invented timestamp is worse than no cell.
 */
export function pinnedAtLabel(pin, nowMs = Date.now()) {
  const raw = pin?.pinned_at;
  let secs = 0;
  if (typeof raw === 'number' && Number.isFinite(raw)) secs = Math.floor(raw);
  else if (typeof raw === 'string' && raw.trim()) {
    const asNumber = Number(raw);
    if (Number.isFinite(asNumber) && asNumber > 0) secs = Math.floor(asNumber);
    else {
      const parsed = Date.parse(raw);
      secs = Number.isFinite(parsed) ? Math.floor(parsed / 1000) : 0;
    }
  }
  if (!secs || secs <= 0) return '';
  return `PINNED ${formatLastSeen(secs, nowMs)}`;
}

/** Config pins first (they are the ones that need explaining), then by name. */
export function sortPins(pins) {
  return [...(pins ?? [])].sort((a, b) => {
    const locked = Number(pinIsLocked(b)) - Number(pinIsLocked(a));
    if (locked) return locked;
    return pinDisplayName(a).localeCompare(pinDisplayName(b));
  });
}

/**
 * The peers an operator can still pin: connected right now, not pinned, and not
 * already in the pin registry. This is the list that answers owner ruling 3
 * ("we should have the peer at sdn.spaceaware.io, the one at celestrak.eth, the
 * one at vm-orbit-det-01, and that's it") with one click per row.
 */
export function pinnableNodes(nodes, pins) {
  const already = new Set(
    (pins ?? []).map((p) => String(p?.peer_id ?? '').trim()).filter(Boolean)
  );
  return (nodes ?? []).filter((n) => {
    if (n?.isSelf || !n?.online || !isPeerRow(n)) return false;
    if (peerSource(n).pinned) return false;
    return !already.has(String(n?.peerId ?? '').trim());
  });
}
