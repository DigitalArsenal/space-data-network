/*
 * THE URL IS THE STATE — and it is a HASH, which is not a preference (IRIS
 * ruling 2026-07-30 §5).
 *
 * The dashboard is ONE embedded $APP served at `/` by
 * `sdn-server/cmd/spacedatanetwork/conjunction_ui.go`, whose handler answers
 * 404 for every path but `/` and `/index.html`. There is no SPA fallback, so a
 * path route survives exactly until the operator presses reload. A fragment is
 * never sent to the origin at all: neither the Go 404 nor an edge in front of
 * the node can touch it, and the address bar becomes something an operator can
 * paste to a colleague — "the peer modal for 16Uiu…" — which is what the owner
 * asked the routing to be for.
 *
 * WHAT IS IN THE URL: the route, the ACCOUNTS subsection (the owner named the
 * submenu, so Back must return to it), and the open modal. WHAT IS NOT: search
 * text, the trust filter, the hide-offline checkbox, sort, page, the globe's
 * 3D/2D switch, and the sign-in dialog — restoring a live wallet transaction
 * from a URL is a defect, not a feature. A filter change is not a place, so it
 * leaves no history entry.
 *
 * Pure: no DOM, no history, no design imports. App.svelte reads on mount and on
 * `hashchange`, and writes through ONE effect that compares before it pushes.
 */

/**
 * The rail's routes, and the subsections each one admits.
 *
 * STOREFRONT joined them on 2026-07-31 (owner: "We also need the storefront
 * submenu"). It nests exactly the way ACCOUNTS already does — a tab bar whose
 * selection is in the address — because a second nesting idiom on one rail is
 * two answers to "where am I".
 */
export const ROUTES = {
  node: [],
  peers: [],
  accounts: ['peers', 'keys'],
  storefront: ['listings', 'modules'],
};

/**
 * The views a NODE card can open on. `qr` was the first (the IDENTITY widget's
 * scannable card); `modules` is the peer's signed $PMM offering, which is a
 * PLACE — an operator sends "look at what this node hosts" to a colleague as a
 * link, or refreshes on it, and both must land back on the same tab.
 */
export const NODE_VIEWS = ['qr', 'modules'];

/**
 * A modal's HOME — the route (and subsection) it is always addressed under, so
 * one dialog has one address however it was opened. `node` and `self` are the
 * same NodeModal over feed rows and live under PEERS; the two registry dialogs
 * live under the ACCOUNTS tab that lists them.
 */
export const MODAL_HOME = {
  node: ['peers', ''],
  self: ['peers', ''],
  peer: ['accounts', 'peers'],
  operator: ['accounts', 'keys'],
};

/**
 * `self` is unambiguous next to a peer id: libp2p ids are base58 and start
 * `12D3KooW…` / `16Uiu2HA…` / `Qm…`, and an xpub is base58 too. Nothing this
 * page addresses is four lowercase letters.
 */
const SELF = 'self';

const decode = (seg) => {
  try {
    return decodeURIComponent(seg);
  } catch {
    // A half-escaped fragment is not an id. Treated as absent, which routes to
    // the parent and raises the "no peer with that ID" notice rather than
    // hunting for a record named `%E0`.
    return '';
  }
};

/**
 * @param {string} hash `location.hash`, with or without its leading '#'.
 * @returns {{route: string, sub: string, modal: string, modalId: string, view: string}}
 */
export function parseHash(hash) {
  const parts = String(hash ?? '')
    .replace(/^#/, '')
    .split('/')
    .map((s) => s.trim())
    .filter(Boolean);

  const empty = { route: 'node', sub: '', modal: '', modalId: '', view: '' };
  const route = parts[0] ?? '';
  // An empty or unparseable hash resolves to the landing route rather than to a
  // blank page — and the caller corrects the address with replaceState, so a
  // typo never becomes a history entry.
  if (!route || !(route in ROUTES)) return empty;

  if (route === 'storefront') {
    const sub = parts[1] ?? '';
    // An unknown or absent subsection lands on the route itself, which picks its
    // own default tab — never a blank page, and never a modal this route has no
    // dialog for.
    return { ...empty, route, sub: ROUTES.storefront.includes(sub) ? sub : '' };
  }

  if (route === 'accounts') {
    const sub = parts[1] ?? '';
    if (!ROUTES.accounts.includes(sub)) return { ...empty, route };
    const id = decode(parts[2] ?? '');
    if (!id) return { route, sub, modal: '', modalId: '', view: '' };
    return { route, sub, modal: sub === 'keys' ? 'operator' : 'peer', modalId: id, view: '' };
  }

  if (route === 'peers') {
    const second = parts[1] ?? '';
    // The third segment is the card's TAB, from one list for both cards: the
    // self node is a node (IRIS R6), so `#/peers/self/modules` and
    // `#/peers/<id>/modules` are the same dialog on the same tab.
    const view = NODE_VIEWS.includes(parts[2] ?? '') ? parts[2] : '';
    if (second === SELF) {
      return { route, sub: '', modal: 'self', modalId: SELF, view };
    }
    const id = decode(second);
    if (!id) return { ...empty, route };
    return { route, sub: '', modal: 'node', modalId: id, view };
  }

  return { ...empty, route };
}

/**
 * The inverse. A modal's route/subsection come from MODAL_HOME rather than from
 * the caller: the self card opened from the NODE dashboard is still the peers
 * card, and one dialog with two addresses would make Back ambiguous.
 */
export function formatHash({ route = 'node', sub = '', modal = '', modalId = '', view = '' } = {}) {
  const home = MODAL_HOME[modal];
  const r = home ? home[0] : route in ROUTES ? route : 'node';
  const s = home ? home[1] : (ROUTES[r] ?? []).includes(sub) ? sub : '';
  const tab = NODE_VIEWS.includes(view) ? `/${view}` : '';
  let out = `#/${r}`;
  if (s) out += `/${s}`;
  if (modal === 'self') return `${out}/${SELF}${tab}`;
  if (home && modalId) {
    out += `/${encodeURIComponent(modalId)}`;
    // Only the node card has tabs; the two registry dialogs do not, and a `view`
    // handed in for one of them is dropped rather than minting an address that
    // parses back to something else.
    if (modal === 'node') out += tab;
  }
  return out;
}
