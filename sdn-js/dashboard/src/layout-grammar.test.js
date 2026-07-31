/*
 * THE GRAMMAR TEST — the enforcement half of IRIS's standing law (graph task
 * `iris-dashboard-grammar-law`, restated atop scale.css).
 *
 * WHY A TEST AND NOT A REVIEW NOTE. The owner has given the same class of
 * instruction three times in four days ("+30% font", "ALL of it well margined and
 * padded", "menus should NEVER have a double scrollbar … never have widgets with
 * different widths and mismatched edges"). Each time it was fixed on the surfaces
 * in that screenshot, and the next surface reproduced it, because nothing in the
 * repo said what correct is. This file says it in a way that fails a build.
 *
 * It reads the SOURCE of every dashboard component, which is the only place these
 * rules are decidable: a rendered page can only show you the violation you
 * happened to screenshot.
 *
 * Adding a surface that needs an exception means AMENDING THE LAW first, in the
 * graph task, with the owner's or Iris's reason — and then adding it here, by
 * name, with that citation.
 */

import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));

/**
 * Strip comments before matching.
 *
 * This is not tidiness: the first version of this file flagged three "violations"
 * that were the PROSE of the fix — comments quoting the `overflow:auto;
 * scrollbar-gutter:stable; padding-inline:…` they had just deleted, so that the
 * next reader knows what the defect was. A source-text law that cannot tell a
 * declaration from a description of a declaration would make documenting a fix
 * impossible, which is the opposite of the point.
 */
function stripComments(src) {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|\s)\/\/[^\n]*/g, '$1');
}

/** Every component in the dashboard, with its source — declarations only. */
const COMPONENTS = readdirSync(HERE)
  .filter((f) => f.endsWith('.svelte'))
  .map((file) => ({
    file,
    src: stripComments(readFileSync(join(HERE, file), 'utf8')),
    raw: readFileSync(join(HERE, file), 'utf8'),
  }));

/**
 * L1's one permitted exception: a modal body is a separate scroll ROOT — it is
 * not nested inside the page scroller's flow, and a dialog taller than the
 * viewport has to scroll somewhere. ModalShell is the ONE modal shell every
 * dialog on this page uses, so the exception is one file, not a pattern.
 */
const MODAL_SCROLL_ROOT = 'ModalShell.svelte';

/** The single element allowed to own the page scroller (L1) and the gutter (L2). */
const PAGE_SCROLLER = 'App.svelte';

describe('L1 — one scroll context', () => {
  it('no component but the modal shell declares an inner scroller', () => {
    const offenders = [];
    for (const { file, src } of COMPONENTS) {
      if (file === MODAL_SCROLL_ROOT) continue;
      // `overflow-wrap` is not overflow; match the property, not the substring.
      // Not anchored to the start of a line: this codebase writes plenty of
      // single-line rules (`.alist { display:flex; overflow:auto; }`), and an
      // anchored pattern would have let exactly that through. The lookbehind is
      // what keeps `overflow-wrap` — which is legitimate everywhere — out of it.
      const matches = src.match(/(?<![-\w])overflow(-x|-y)?\s*:\s*(auto|scroll)/g) ?? [];
      if (file === PAGE_SCROLLER) {
        // App owns the page scroller: overflow-y:auto on `main` is the point.
        // Anything MORE than that pair (x-hidden + y-auto) is a second scroller.
        const extra = matches.filter((m) => !/overflow-y\s*:\s*auto/.test(m));
        if (extra.length) offenders.push(`${file}: ${extra.join(' ')}`);
        continue;
      }
      if (matches.length) offenders.push(`${file}: ${matches.join(' ')}`);
    }
    expect(offenders, 'L1: the page column is the only scroller').toEqual([]);
  });

  it('scrollbar-gutter is declared exactly once, on the page scroller', () => {
    const sites = COMPONENTS.flatMap(({ file, src }) =>
      (src.match(/(?<![-\w])scrollbar-gutter\s*:/g) ?? []).map(() => file)
    );
    expect(sites, 'L1: one reserved scrollbar track, on the page scroller').toEqual([PAGE_SCROLLER]);
  });
});

describe('L2 / L5 — one gutter, one width', () => {
  it('no component but the page scroller applies a page gutter with padding-inline', () => {
    const offenders = COMPONENTS.filter(
      ({ file, src }) => file !== PAGE_SCROLLER && /(?<![-\w])padding-inline\s*:/.test(src)
    ).map(({ file }) => file);
    expect(offenders, 'L2: a second page gutter is how column edges start disagreeing').toEqual([]);
  });

  it('no widget or panel column pins a PANEL-SCALE width', () => {
    // The distinction that matters: `width: 9px` on a corner tick or a legend dot
    // is decoration, and clamping a hint's line length is typography. What breaks
    // L5 is a PANEL-scale dimension — a percentage, a viewport unit, or anything
    // wide enough to be a column — because that is what makes one panel's edge
    // disagree with the panel above it.
    //
    // L5's own wording is the enforcement rule: such a declaration is a defect
    // "unless the ruling that introduced it is cited at the declaration". So the
    // test does not forbid them — it requires the citation, in the comment block
    // immediately above, which is what makes the next reader able to tell a
    // deliberate constraint from drift.
    // A DECLARATION, not a media query: `@media (max-width: …)` is how L4's
    // responsive half is written and is never a panel dimension.
    const PANEL_SCALE = /(?<![-\w])(max-|min-)?width\s*:\s*(?!100%|0)((\d{3,})px|\d+%|\d+v[wh]|calc\()/;
    const CITED = /L4|L5|L6|IRIS|owner directive|the design/i;
    const offenders = [];
    for (const { file, raw } of COMPONENTS) {
      if (!['NodeConsole.svelte', 'PeerMap.svelte', 'Pager.svelte'].includes(file)) continue;
      const lines = raw.split('\n');
      lines.forEach((line, i) => {
        if (line.includes('@media') || !PANEL_SCALE.test(line)) return;
        // The citation lives in the comment block immediately above the rule.
        const lookback = lines.slice(Math.max(0, i - 10), i).join('\n');
        if (!CITED.test(lookback)) offenders.push(`${file}:${i + 1} ${line.trim()}`);
      });
    }
    expect(offenders, 'L5: a panel-scale dimension needs its ruling cited at the declaration').toEqual([]);
  });
});

describe('L4 — widgets paginate, they do not scroll', () => {
  it('no widget body carries a max-height', () => {
    const offenders = COMPONENTS.filter(
      ({ file, src }) => file !== MODAL_SCROLL_ROOT && /(?<![-\w])max-height\s*:/.test(src)
    ).map(({ file }) => file);
    expect(offenders, 'L4: overflow paginates; a max-height hides rows instead').toEqual([]);
  });

  /*
   * L4b — NOTHING IS CUT, AND NOTHING IS CUT UP.
   *
   * Added 2026-07-30 from two defects in one build of the PEERS route, both of
   * them the same mistake in different elements: a box was allowed to be
   * narrower than the shortest thing inside it, and the browser obliged.
   *   · the NAME column rendered the fallback identity as `unkno` / `wn`,
   *     because `overflow-wrap: anywhere` let every cell's minimum collapse to
   *     one character and the SOURCE column's config path outbid it;
   *   · the metaline lost its last 307px at 390px, because it was a nowrap flex
   *     row inside a container that is `overflow-x: hidden` — so it could
   *     neither wrap nor scroll, only disappear.
   * Neither is visible in a unit test or a Go test; both are visible in a
   * screenshot, which is why the rule is written against the source.
   */
  it('no table cell may collapse below one word', () => {
    const table = COMPONENTS.find((c) => c.file === 'NodeTable.svelte').src;
    // The bare `td` rule — anchored, or this matches `tr.row:hover td {` and
    // passes against a block that never mentioned overflow-wrap at all.
    const tdBlock = /^\s*td\s*\{[^}]*\}/m.exec(table)?.[0] ?? '';
    expect(tdBlock, 'NodeTable must style td').not.toBe('');
    expect(/padding\s*:/.test(tdBlock), 'the td default rule, not a hover rule').toBe(true);
    expect(
      /overflow-wrap\s*:\s*anywhere/.test(tdBlock),
      'L4b: `anywhere` on the td default lets any column squeeze to one character — use break-word'
    ).toBe(false);
    // Exactly two values here have no bound on their length: the config path and
    // an operator-typed display name. Each may collapse, and each is capped, so
    // neither can bid for a column at the other columns' expense.
    for (const sel of ['.note', '.dn']) {
      const block = new RegExp(`\\${sel}\\s*\\{[^}]*\\}`).exec(table)?.[0] ?? '';
      expect(block, `NodeTable must style ${sel}`).not.toBe('');
      expect(/overflow-wrap\s*:\s*anywhere/.test(block), `L4b: ${sel} may collapse`).toBe(true);
      expect(/max-width\s*:/.test(block), `L4b: …only while ${sel} is capped`).toBe(true);
    }
    // A peer id split across two lines is two fragments, not an identifier —
    // and its width is the NAME column's floor, which is what keeps the two
    // collapsible values above from squeezing the name to nothing.
    const pid = /\.pid\s*\{[^}]*\}/.exec(table)?.[0] ?? '';
    expect(/white-space\s*:\s*nowrap/.test(pid), 'L4b: a peer id is one token').toBe(true);
  });

  it('the metaline wraps instead of being clipped', () => {
    const app = COMPONENTS.find((c) => c.file === PAGE_SCROLLER).src;
    const meta = /(?<![-\w])\.meta\s*\{[^}]*\}/.exec(app)?.[0] ?? '';
    expect(meta, 'App must style .meta').not.toBe('');
    expect(/flex-wrap\s*:\s*wrap/.test(meta), 'L4b: it lives inside overflow-x:hidden').toBe(true);
    expect(
      /\.meta\s*>\s*span\s*\{[^}]*white-space\s*:\s*nowrap/.test(app),
      'L4b: the LIST wraps, a single fact does not split'
    ).toBe(true);
  });

  it('there is exactly ONE pager implementation and it is the shared component', () => {
    // The tell-tale of a hand-rolled copy is the pager's own vocabulary.
    const offenders = COMPONENTS.filter(
      ({ file, src }) => file !== 'Pager.svelte' && /‹\s*PREV/.test(src)
    ).map(({ file }) => file);
    expect(offenders, 'L4: two pagers is how "widgets paginate" becomes "widgets mostly paginate"').toEqual([]);
  });
});

describe('L6 — control chrome is inset and full-size', () => {
  it('every <select> reserves inset space for its chevron', () => {
    for (const { file, src } of COMPONENTS) {
      if (!/<select/.test(src)) continue;
      const hasInset = /padding-right\s*:\s*var\(--sdn-sp-(8|9|10)\)/.test(src) || /padding-inline-end/.test(src);
      expect(hasInset, `${file}: a <select> needs padding-right >= --sdn-sp-8 (L6)`).toBe(true);
    }
  });

  it('an aspect-locked canvas is clamped on one axis and states its ratio', () => {
    for (const { file, src } of COMPONENTS) {
      if (!/<canvas/.test(src)) continue;
      // Only components that STYLE a canvas box are in scope; the globe sizes its
      // canvas from its own wrapper in script, and has no ratio to preserve.
      const styled = /canvas\s*\{[^}]*\}/s.exec(src);
      if (!styled) continue;
      const block = styled[0];
      if (!/max-width|max-height/.test(block)) continue;
      const clamps = (block.match(/max-(width|height)\s*:\s*\d+%/g) ?? []).length;
      expect(clamps, `${file}: clamp a QR canvas on ONE axis only (L6)`).toBeLessThanOrEqual(1);
      expect(/aspect-ratio\s*:/.test(block), `${file}: state the aspect ratio (L6)`).toBe(true);
      expect(/flex\s*:\s*none/.test(block), `${file}: flex:none, or flex-shrink squashes it (L6)`).toBe(true);
    }
  });
});

/*
 * L7 — A COUNT IS CHECKABLE, A ROW SAYS WHERE IT CAME FROM.
 *
 * Added 2026-07-30 after the owner raised the same page three times in one day:
 * "it says 35 peers, but only shows one on the globe … rows that say 'last seen
 * - never', so how did they get there? Also what does the first row 'config
 * trusted peer' mean?" — and, separately, "I have no idea what these peers are
 * that are in the table".
 *
 * Every one of those was a surface asserting something the operator could not
 * verify from the same screen: a total that the map contradicted, a verdict
 * ("never") where the fact was "not yet", a hardcoded placeholder standing in
 * for a name, and a set called "trusted" on the strength of nothing. The rules
 * below are what "true on screen" means here, written so a build fails rather
 * than a fourth screenshot arriving.
 */
describe('L7 — the page cannot assert what the operator cannot check', () => {
  it('no surface calls a peer set "trusted" on its behalf', () => {
    // The trust REGISTRY is a fine name for a registry: it records a level,
    // which may be `never`. "TRUSTED PEERS" claims of every row the one thing
    // the row exists to record.
    const offenders = COMPONENTS.filter(({ file, src }) => /TRUSTED PEERS?['"`\s<]/.test(src)).map(
      ({ file }) => file
    );
    expect(offenders, 'L7: name the registry, do not vouch for its contents').toEqual([]);
  });

  it('LAST SEEN never renders the bare word "never"', () => {
    // "never" is a verdict about the future; the fact is "not observed yet",
    // which for a pinned peer is ordinary. peers.js `lastSeenLabel` says which.
    const src = readFileSync(join(HERE, 'format.js'), 'utf8');
    expect(/return\s+'never'/.test(stripComments(src)), 'L7: format.js must not mint "never"').toBe(
      false
    );
  });

  it('the peers table carries a SOURCE column, and it is not droppable', () => {
    const table = COMPONENTS.find((c) => c.file === 'NodeTable.svelte').src;
    expect(/label:\s*'SOURCE'/.test(table), 'L7: the row must say how it got here').toBe(true);
    // `drop:` is the narrow-screen priority ladder. NAME and SOURCE — who it is
    // and why it is listed — carry no `drop`, so they survive every breakpoint.
    // The column that answers the owner's question is not the first one to go.
    expect(/key:\s*'source',\s*label:\s*'SOURCE'\s*}/.test(table)).toBe(true);
    expect(/key:\s*'node',\s*label:\s*'NAME'\s*}/.test(table)).toBe(true);
  });

  it('the globe states its own coverage, so a count cannot contradict the dots', () => {
    const map = COMPONENTS.find((c) => c.file === 'PeerMap.svelte').src;
    expect(/mapCoverage\(/.test(map), 'L7: PeerMap must publish plotted vs total').toBe(true);
    expect(/NO LOCATION/.test(map), 'L7: name the peers that cannot be drawn').toBe(true);
  });

  it('a provenance label is never a NAME — the vocabulary lives in peers.js', () => {
    // The live feed's one named row read 'Config Trusted Peer', a placeholder
    // the node manufactured because provenance had nowhere to go. Nothing in
    // this tree may re-mint a name out of a row's origin.
    const offenders = COMPONENTS.filter(({ src }) => /Config Trusted Peer/i.test(src)).map(
      ({ file }) => file
    );
    expect(offenders, 'L7: provenance is a column, not a name').toEqual([]);
  });
});

/*
 * L8 — AN ADDRESS THE OPERATOR CAN REACH FROM THIS PAGE IS VOCABULARY; A
 * LOCATION ON A BOX THEY MAY NOT EVEN HAVE A SHELL ON IS A LEAK.
 *
 * Added 2026-07-30 (IRIS ruling for `sdn-peer-modal-trust-apply-honesty`) from
 * one session of the owner's. He opened the edit modal for a registry peer this
 * node has never connected to and the page told him four untrue-ish things at
 * once: a `<select>` with a single option beside a primary APPLY that could not
 * fire; `HTTP 521` as the whole explanation of a contact card that was never
 * going to load; `unknown` as a name six inches from `UNKNOWN` the trust tier;
 * and, under a SOURCE heading, the literal string
 * `<a config path> · <a config key>` — a location he had already flagged once.
 *
 * Every rule below is source-text law, for the same reason L1–L7 are: a
 * rendered page only shows you the violation you happened to screenshot.
 *
 * What counts as a violation (the ruling's own list): absolute filesystem
 * paths, config FILE names, dotted config KEY paths rendered as a value,
 * systemd unit/socket names, env-var names presented as an answer. What does
 * NOT: HTTP routes on this same origin (`/api/peers`, `/identity/<id>.vcf`,
 * `/ws/status`) — the operator can open them in this very browser — plus URLs,
 * the hash routes, peer ids, multiaddrs, comments, and clipboard payloads.
 */
describe('L8 — a reachable address is vocabulary, a box location is a leak', () => {
  /** Markup only: the <script> half holds data plumbing, not rendered text. */
  const markupOf = (src) => src.split('</script>').slice(1).join('</script>');

  it('no component renders a pin note without the publishable guard', () => {
    // A config pin's `note` IS the file and the key. peers.js decides whether a
    // note is prose an operator typed; markup may only render it through that
    // decision, and the guard lives in the block immediately above the render
    // (the same lookback idiom L5 uses for a cited dimension).
    const offenders = [];
    for (const { file, src } of COMPONENTS) {
      const lines = markupOf(src).split('\n');
      lines.forEach((line, i) => {
        if (!/pin\.note|pinNote/.test(line)) return;
        const lookback = lines.slice(Math.max(0, i - 6), i + 1).join('\n');
        if (!/pinNoteIsPublishable\(/.test(lookback)) offenders.push(`${file}:${i + 1} ${line.trim()}`);
      });
    }
    expect(offenders, 'L8: a config location is data, never rendered text').toEqual([]);
  });

  it('no component carries a path-shaped literal', () => {
    const PATH = /(^|["'>\s])(~?\/(etc|var|usr|opt|home|srv|root)\/|[A-Za-z]:\\)/;
    const offenders = COMPONENTS.filter(({ src }) => PATH.test(src)).map(({ file }) => file);
    expect(offenders, 'L8: an operator may not even have a shell on that box').toEqual([]);
  });

  it('no component names a config file, a unit or a socket', () => {
    const UNIT = /\.(ya?ml|toml|sqlite|service|socket)\b/;
    const offenders = COMPONENTS.filter(({ src }) => UNIT.test(src)).map(({ file }) => file);
    expect(offenders, 'L8: name what the operator does, not the file it lands in').toEqual([]);
  });

  it('the peer modal renders no trust control it cannot fire', () => {
    const modal = COMPONENTS.find((c) => c.file === 'PeerEditModal.svelte').src;
    expect(/canSetTrust/.test(modal), 'L8: the armed state has a name').toBe(true);
    // Every <select> in this file must sit inside the armed branch. Anything
    // before `{#if canSetTrust}` is a control rendered in a state where APPLY
    // does nothing — which is the defect the owner reported.
    const armed = modal.indexOf('{#if canSetTrust}');
    expect(armed, 'L8: PeerEditModal must gate its trust control').toBeGreaterThan(-1);
    expect(modal.indexOf('<select'), 'L8: no <select> outside {#if canSetTrust}').toBeGreaterThan(armed);
  });

  it('no component falls back to the word "unknown" for a NAME', () => {
    // `unknown` is a TRUST TIER, rendered by these same dialogs. A name that is
    // absent reads `unnamed` — one word, one meaning.
    //
    // AccountModal.svelte is EXEMPT BY NAME: it is outside the write scope of
    // sdn-peer-modal-trust-apply-honesty (its two fallbacks name the signed-in
    // session and its fingerprint, not a peer or an operator row). Amending
    // that exemption away is a separate pass — the law is amended first, in the
    // graph task, then here, which is what this comment is.
    const EXEMPT = ['AccountModal.svelte'];
    const offenders = COMPONENTS.filter(
      ({ file, src }) => !EXEMPT.includes(file) && /\|\|\s*'unknown'/.test(src)
    ).map(({ file }) => file);
    expect(offenders, 'L8: an absent name is `unnamed`, not the UNKNOWN tier').toEqual([]);
  });
});

describe('the wave-1 defects these rules were written from', () => {
  it('NodeConsole holds no hardcoded ENABLED literal', () => {
    const src = COMPONENTS.find((c) => c.file === 'NodeConsole.svelte').src;
    // The AUTOSTART cell rendered the WORD "ENABLED" as markup behind a flag that
    // was structurally always false (IRIS §4 / R8). The cell is a measurement now,
    // read from the supervisor probe. `.toUpperCase()` of a real value is fine;
    // the literal in markup is not.
    expect(src).not.toMatch(/>ENABLED</);
  });

  it('the dashboard renders no second Globe of its own', () => {
    // Two mounted globes are two WebGL contexts on one page (the law's companion
    // trap). PeerMap is the one mount point; NodeConsole and App consume it.
    const direct = COMPONENTS.filter(
      ({ file, src }) => file !== 'PeerMap.svelte' && file !== 'Globe.svelte' && /<Globe\b/.test(src)
    ).map(({ file }) => file);
    expect(direct, 'PeerMap is the only component that mounts a Globe').toEqual([]);
  });

  it('the THIS NODE route and its widget file are gone', () => {
    const app = COMPONENTS.find((c) => c.file === 'App.svelte').src;
    expect(app).not.toMatch(/route === 'self'/);
    expect(COMPONENTS.some((c) => c.file === 'NodeWidgets.svelte')).toBe(false);
  });
});
