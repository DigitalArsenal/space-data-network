/**
 * Standalone CONJUNCTION-only ship (loop SDN_SPACEAWARE_UI_LOOP.md Phase C,
 * task C1) — pure logic for the minimal chrome that wraps the reused
 * `ConjunctionView` when it mounts at `/` as its own single-file artifact.
 *
 * The conjunction-only artifact ships NO console shell, NO rail, NO login
 * screen: its three data sources (`/api/v1/peers`, `/api/v1/channels`,
 * `/api/v1/stats`) are all anonymous-safe, and `ConjunctionView` carries no
 * admin-gated affordances, so there is nothing to session-guard. This module
 * holds the two standalone-specific decisions worth unit-testing (the rest of
 * the header reuses the already-tested helpers in `./console`):
 *
 *   1. Which navigations the standalone ship actually owns. Everything in the
 *      full app besides the conjunction experience (login, `/console/*`,
 *      `/orbital`, `/gantt`, `/bmc2`) is DESCOPED per the 2026-07-11 owner
 *      directive and is not bundled here. `ConjunctionView`'s reused "OPEN IN
 *      3D" affordance targets `/orbital?group=`; in this ship that route is
 *      descoped, so the app's `navigate()` is a documented no-op for it
 *      (see `classifyConjunctionAppNav`) rather than pushing history to a
 *      dead route.
 *   2. The honest "no session" chip the header shows in place of the console's
 *      IDENTITY chip — this ship never authenticates (`conjunctionAppSessionChip`).
 */

export type ConjunctionAppNavKind = 'internal' | 'descoped';

/**
 * Classifies a `navigate()` target for the standalone conjunction ship. The
 * only route this artifact owns is the conjunction experience itself (served
 * at `/`; `/console/conjunction` is accepted as an alias so a deep link from
 * the full app's route scheme still resolves in-app). Every other path — the
 * descoped full-app screens — resolves to `'descoped'`, and the caller must
 * NOT push browser history for it (there is nothing bundled to render).
 */
export function classifyConjunctionAppNav(path: string): ConjunctionAppNavKind {
  const pathname = (path.split(/[?#]/)[0] ?? '').trim();
  if (pathname === '' || pathname === '/' || pathname === '/console/conjunction') {
    return 'internal';
  }
  return 'descoped';
}

export interface ConjunctionAppChip {
  label: string;
  color: string;
}

/**
 * The header's session chip for the conjunction-only ship. There is no login
 * flow bundled, so this is a fixed, honest "public / anonymous" indicator
 * rather than the console's dynamic IDENTITY chip — matching the fact that
 * every data source this artifact reads is anonymous-safe.
 */
export function conjunctionAppSessionChip(): ConjunctionAppChip {
  return { label: 'PUBLIC · ANONYMOUS', color: '#7d929b' };
}
