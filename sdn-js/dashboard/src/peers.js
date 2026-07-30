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
 * ⛔ OWNER, 2026-07-30, reading a real row: "unknown / 16Uiu2…PpYr2U / — / FROM
 * CONFIG FILE / /etc/space-data-network/config.module-delivery-sidecar.yaml ·
 * peers.trusted_peers / FULL / OFFLINE / … — I have no idea what the fuck this
 * means. Remove the path and make this very clear."
 *
 * Two things were wrong and both are fixed here rather than restyled.
 *
 * 1. THE BADGE NAMED A MECHANISM, NOT A REASON. "FROM CONFIG FILE" answers
 *    "which file?" when the question on the page is "why is this here?". IRIS
 *    ruling 2026-07-30: `config` and `pinned` SHARE the badge ADDED BY OPERATOR,
 *    because to the person reading the table they are the same fact — a human
 *    put it there. WHERE that human has to go to change it is a different
 *    question, carried by the `LOCKED` chip and answered in full in the admin
 *    EDIT modal, which is the only surface that has an operator in front of it.
 *
 * 2. THE PATH WAS ROW DATA. It is not. A filesystem path is admin detail, and
 *    it was also the widest unbounded value in the table (a 59-char prod path
 *    bid 342px of an 1311px table). `note` SURVIVES ON THIS OBJECT — the modal
 *    reads it — and NO TABLE MAY RENDER IT. That is the whole of the owner's
 *    "remove the path": removed from the row, kept where it is useful.
 *
 *   config     an operator declared this peer in the node's CONFIG FILE. `note`
 *              is the real FILE + KEY they edit — modal-only.
 *   pinned     an operator pinned it on this dashboard. `note` is their own.
 *   connected  it has a live connection right now. It is listed BECAUSE of that,
 *              and it disappears when it disconnects (owner ruling 1).
 *   account    not a peer at all: an operator account from the Admin overlay
 *              that has never connected. Kept distinct so it can be excluded
 *              from every peer count instead of inflating one.
 *   unknown    the feed said nothing. A defect, rendered as one.
 *
 * `sentence` is prose an operator can read aloud — it is the row's `title` and
 * the modal's LISTING line, and it never contains a path either.
 */
export function peerSource(node) {
  const raw = String(node?.source ?? '').trim().toLowerCase();
  const note = String(node?.pinNote ?? '').trim();
  const flagged = Boolean(node?.pinned);

  if (raw === 'config') {
    return {
      id: 'config',
      label: 'ADDED BY OPERATOR',
      tone: 'ice',
      pinned: true,
      locked: true,
      note,
      sentence:
        "An operator added this peer in this node's configuration file; it stays listed while offline.",
    };
  }
  if (raw === 'pinned') {
    return {
      id: 'pinned',
      label: 'ADDED BY OPERATOR',
      tone: 'ice',
      pinned: true,
      locked: false,
      note,
      sentence:
        'An operator added this peer on this dashboard; it stays listed while offline.',
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
        ? 'Connected right now, and added by an operator: it stays listed after it disconnects.'
        : 'Connected right now — that is the only reason it is listed.',
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
      sentence: 'An operator account on this node. It has never connected as a peer.',
    };
  }
  return {
    id: 'unknown',
    label: 'SOURCE NOT STATED',
    tone: 'amber',
    pinned: flagged,
    locked: false,
    note,
    sentence: 'This node did not say how this row got here.',
  };
}

/** True for rows that are peers — an operator account is not one. */
export const isPeerRow = (node) => peerSource(node).id !== 'account';

/**
 * ⛔ NO SELF ROW — STRUCTURAL, not a call-site habit.
 *
 * Owner, 2026-07-30, on the row he could not read: "Is this the root node? If
 * so it should not be in the peers list". It was not (that row is host-02;
 * measured), but the rule he is invoking has now been RULED THREE TIMES
 * (accounts-table-rules: "⛔ NO self row (ruled twice)"), and a rule ruled
 * three times must be made IMPOSSIBLE rather than merely currently-true.
 *
 * Before this, self-exclusion lived only in App.svelte's `accountRows` — a
 * `withoutSelf()` call at ONE call site. Any second consumer of NodeTable, any
 * future route, any refactor that reordered that derivation, silently
 * reintroduces the defect; nothing in the renderer forbids it. So the RENDERER
 * enforces it (NodeTable calls this on its own props), which makes the property
 * hold for every caller that will ever exist rather than for the one that
 * exists today.
 *
 * TWO tests of self, because they fail independently: the feed's IS_SELF flag
 * (authoritative when set) and a peer-id match against the emitting node
 * (`NodeStatusSet.SOURCE_PEER_ID`), which still catches a row whose IS_SELF was
 * never written. Comparison is exact — a peer id is a hash, never fuzzy-matched.
 *
 * @param {{node?: any}[]} rows  table rows, `{ node }`-shaped
 * @param {string} selfPeerId    the emitting node's peer id (may be '')
 */
export function withoutSelfRows(rows, selfPeerId = '') {
  const self = String(selfPeerId ?? '').trim();
  return (rows ?? []).filter((row) => {
    const node = row?.node ?? row;
    if (!node) return false;
    if (node.isSelf) return false;
    return !(self && String(node.peerId ?? '').trim() === self);
  });
}

/**
 * THE SET DIFFERENCE, stated instead of left to be noticed.
 *
 * ⛔ OWNER, 2026-07-30, two messages: "then why is there four fucking peers when
 * only three exist? make this make sense", and — seeing the trust registry say
 * 4 while the peers table said ROWS 1-3 OF 3 — "what the fuck".
 *
 * Both numbers were CORRECT and neither said what it was counting. The trust
 * registry (/api/peers) holds every peer this node has a record for; the peers
 * board admits only PINNED-OR-CONNECTED (the admission rule landed in
 * sdn-peers-provenance-and-count-honesty). A peer in the registry that has never
 * connected is therefore invisible by design — and the invisibility is exactly
 * what made the two numbers look like a bug.
 *
 * IRIS ruling: the registry row STAYS hidden (owner ruling 1 stands), and the
 * delta is NAMED on the peers board. Returns 0 when the two agree, so the line
 * only appears when there is something to explain.
 */
export function registryDelta(registryCount, boardCount) {
  const registry = Number(registryCount ?? 0);
  const board = Number(boardCount ?? 0);
  if (!Number.isFinite(registry) || !Number.isFinite(board)) return 0;
  return Math.max(0, Math.floor(registry) - Math.floor(board));
}

/** The delta line's copy, or '' when the two counts agree (IRIS 2026-07-30). */
export function registryDeltaLabel(registryCount, boardCount) {
  const n = registryDelta(registryCount, boardCount);
  if (!n) return '';
  return `${n} IN REGISTRY, NEVER CONNECTED · NOT SHOWN`;
}

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
  return 'ADDED BY OPERATOR';
}

/**
 * What the note IS. A config pin's "note" is a FILE PATH AND A KEY, and it is
 * admin-only detail: `DEFINED IN` is the label it carries in PeerEditModal, the
 * one surface allowed to show it (IRIS 2026-07-30, discharging the owner's
 * "Remove the path"). An operator's own pin note is just a NOTE and may appear
 * wherever notes appear.
 */
export const pinNoteLabel = (pin) => (pinIsLocked(pin) ? 'DEFINED IN' : 'NOTE');

/**
 * MAY THIS NOTE BE SHOWN OUTSIDE THE EDIT MODAL? A config pin's note is a path
 * and the answer is NO — anywhere, on any table, forever. An operator's own
 * note is prose they typed and may be shown. Callers ask this instead of
 * re-deriving the rule, so "remove the path" cannot be half-applied to one of
 * the three surfaces that render pin notes.
 */
export const pinNoteIsPublishable = (pin) => !pinIsLocked(pin);

/** NAME rule, unchanged (§16.4.3): "unknown", never an identifier promoted. */
export const pinDisplayName = (pin) => String(pin?.name ?? '').trim() || 'unknown';

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
