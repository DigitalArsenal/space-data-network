<script>
  /**
   * SDN Node Status $APP — THE SHELL.
   *
   * Composes the DESIGN repo's chrome (SdnRail + ConsoleHeader) around whichever
   * route the registry discovered, and owns the four things that outlive a
   * route: the status feed, the operator session, the runtime poller and the
   * modals. Data comes EXCLUSIVELY from globalThis.SDN_NODE_STATUS; this view
   * never fetches node data itself.
   *
   * PROVISIONAL (Iris ruling): the next design-tool zip supersedes this view.
   * The data seam (SDN_NODE_STATUS) survives. Styled ONLY with theme.js tokens
   * and the design components — no invented palette.
   *
   * WHAT THIS FILE USED TO BE (sdn-dashboard-modularize-for-parallelism): 1,255
   * lines with 25 hand-wired imports, a SECTIONS table, a ROUTE_TITLE table, a
   * three-branch route `{#if}`, and every page's search/filter/sort/tab state in
   * one scope. Adding PEERS on 2026-07-30 took four separate edits to this file.
   * Routes are DISCOVERED now (`routes/registry.js`) and own their own state, so
   * a page is a directory.
   *
   * THE SHELL CONTEXT — the one seam a route reads, spread as props:
   *
   *   view                 the raw NodeStatusSetView (its generatedAt/sourcePeerId)
   *   nodes                every node in the feed
   *   selfNode             this node's entry, with any unflushed profile edit applied
   *   accountNodes         the feed merged with the Admin /api/accounts overlay
   *   accountRows          the same set as account rows
   *   runtime, now         the folded runtime snapshot and the page clock
   *   session, canEdit     the operator session and whether it may edit
   *   rootAdminAvailable   §14.1 — whether this node's root phrase signs in
   *   presence             the honest pinned-or-connected summary (peers.js)
   *   selected             the peer whose modal is open, if any
   *   onSelectNode(node)   open a peer's detail modal
   *   onRequestSignIn()    open the sign-in dialog
   *   onShowSelf(view)     open THIS node's modal on 'parsed' | 'qr'
   *   onProfileSaved(p)    a profile was just written — echo it until the feed agrees
   *
   * A route may also export `boot(shell)` from its `route.js`; the shell calls it
   * once on mount for EVERY registered route, so page-lifetime work (PEERS warms
   * the search model) lives in the feature's directory, not here.
   */
  import { onMount } from 'svelte';
  import SdnRail from 'spaceaware-student-sdn/src/lib/shell/SdnRail.svelte';
  import ConsoleHeader from 'spaceaware-student-sdn/src/lib/shell/ConsoleHeader.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeModal from './NodeModal.svelte';
  import SignInModal from './SignInModal.svelte';
  import AccountModal from './AccountModal.svelte';
  import { shortId } from './format.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { canEditNodeProfile, canManagePermissions } from './permissions.js';
  import { accountFromNode, mergeAccounts, sortAccounts, vcardDisplayName, withoutSelf } from './accounts.js';
  import { apiFetch } from './api.js';
  import { xpubFingerprint, fingerprintMatches, shortFingerprint } from './wallet.js';
  import { presenceSummary } from './peers.js';
  import { foldRuntime, createRuntimeFeed } from './runtime.js';
  import { ROUTES, SECTIONS, ROUTE_TITLE, LANDING } from './routes/registry.js';
  import { ROUTE_COMPONENTS } from './routes/components.js';

  /** @type {import('../../src/status/view-model').NodeStatusSetView | null} */
  let view = $state(null);
  let now = $state(Date.now());
  let route = $state(LANDING);
  /**
   * The node's own runtime facts for the NODE dashboard (runtime.js). Starts as
   * the empty fold so every widget renders from one shape and never has to guard
   * for "not loaded yet" — an absent number is an absent cell, by construction.
   */
  let runtime = $state(foldRuntime());
  /** Admin-only enrichment rows from GET /api/accounts (§16.5). */
  let accountEntries = $state([]);
  let selected = $state(null);
  /**
   * The detail modal for THIS node, opened from the IDENTITY widget: '' closed,
   * 'parsed' on the fields, 'qr' straight on the scannable card (owner directive
   * 2026-07-30 — the QR belongs in a modal). It is a separate piece of state from
   * `selected` so opening this node's own card cannot be confused with selecting
   * a peer.
   */
  let selfModal = $state('');

  // --- operator session (nst-node-edit-permissions-ui) ---------------------
  // `identity` (the in-page signer) exists ONLY for a session established in
  // this tab: /api/auth/me restores the identity's FINGERPRINT, name and
  // trust level across a reload, but never key material, and there is nothing
  // stored anywhere to restore it from. A restored session can therefore
  // administer, but must re-enter its recovery phrase to sign an attestation.
  // The node never returns the raw xpub (contract §4) — `fingerprint` is the
  // authoritative name of the signed-in key, and while we hold the key we
  // check the node's fingerprint against our own hash of it.
  /** @type {{name?: string, trustLevel: string, fingerprint: string, xpub: string, identity: any} | null} */
  let session = $state(null);
  // §14.1: the node derives its accepted admin keys from its own seed, so its
  // root recovery phrase always signs in as admin. `admin_configured` now
  // means "an admin can sign in at all" and is true on a fresh node, so it
  // drives nothing here; `root_admin_available` is what the dialog says out
  // loud, and `users_configured` is the only "has anyone enrolled" signal.
  /** @type {boolean|null} null until /api/auth/status answers. */
  let rootAdminAvailable = $state(null);
  let signInOpen = $state(false);
  /** The account modal (owner directive 2026-07-29 — the upper-right button). */
  let accountOpen = $state(false);
  /** Post-save profile echo, held until the status feed catches up. */
  let epmOverride = $state(null);

  const runtimeFeed = createRuntimeFeed({ onUpdate: (r) => (runtime = r) });

  const nodes = $derived(view?.nodes ?? []);
  const feedSelfNode = $derived(nodes.find((n) => n.isSelf) ?? null);
  // A saved profile is republished immediately but only reaches the status
  // feed on its next frame; until then THIS NODE renders the node's own
  // freshly-read vCard/DN so an operator never sees a stale card they just
  // changed. The override clears itself the moment the feed agrees.
  const selfNode = $derived.by(() => {
    if (!feedSelfNode) return null;
    if (!epmOverride) return feedSelfNode;
    return {
      ...feedSelfNode,
      vcard: epmOverride.vcard || feedSelfNode.vcard,
      dn: typeof epmOverride.dn === 'string' ? epmOverride.dn : feedSelfNode.dn,
    };
  });
  $effect(() => {
    if (epmOverride && feedSelfNode && feedSelfNode.vcard === epmOverride.vcard) epmOverride = null;
  });
  // Same name resolution as the ACCOUNTS row for this node, so the page and
  // its row cannot disagree: DN, else the published vCard's FN, else ORG.
  const selfTitle = $derived(
    selfNode
      ? selfNode.dn?.trim() ||
        vcardDisplayName(selfNode.vcard, selfNode.peerId) ||
        selfNode.org?.trim() ||
        shortId(selfNode.peerId)
      : ''
  );
  const sessionTier = $derived(normalizeTrust(session?.trustLevel));
  const sessionTierColor = $derived(theme[TRUST_COLOR_TOKEN[sessionTier]] ?? theme.textMuted);
  const canEdit = $derived(Boolean(session) && canEditNodeProfile(session.trustLevel));

  function clearSession() {
    session?.identity?.destroy?.();
    session = null;
  }

  /**
   * Restore the session from the node. Reachable at ANY authenticated tier
   * (contract §6b), so a `marginal` operator renders as SIGNED IN with a
   * read-only permissions view rather than as a stranger — only `401` means
   * "no session". Any other failure (network, node restart mid-request)
   * leaves the current state alone rather than inventing a sign-out.
   */
  async function refreshSession() {
    let me;
    try {
      me = await apiFetch('/api/auth/me');
    } catch (err) {
      if (err?.status === 401) clearSession();
      return;
    }
    const held = session?.identity ?? null;
    const local = held ? await xpubFingerprint(held.xpub) : '';
    if (!fingerprintMatches(local, me?.xpub_fingerprint)) {
      // The cookie names a DIFFERENT identity than the key in this page.
      clearSession();
      return;
    }
    session = {
      name: me?.name ?? '',
      trustLevel: me?.trust_level ?? '',
      fingerprint: me?.xpub_fingerprint ?? session?.fingerprint ?? '',
      xpub: session?.xpub ?? '',
      identity: held,
    };
  }

  function onSignedIn(result) {
    signInOpen = false;
    session = {
      name: result.user?.name ?? '',
      trustLevel: result.user?.trust_level ?? '',
      // The node's fingerprint is authoritative and is all we have: the
      // wallet's own xpub is a MASTER key that never leaves the wallet (§13.1).
      fingerprint: result.user?.xpub_fingerprint ?? '',
      xpub: '',
      identity: result.identity,
    };
    readAuthStatus();
  }

  /**
   * §16.5: the ACCOUNTS page renders from the PUBLIC feed for everyone. The
   * Admin overlay is fetched ONLY once /api/auth/me has already succeeded, so
   * a signed-out visitor never triggers a 401 and never sees an empty table.
   */
  async function readAccounts() {
    if (!session || !canManagePermissions(session.trustLevel)) {
      accountEntries = [];
      return;
    }
    try {
      const rows = await apiFetch('/api/accounts');
      accountEntries = Array.isArray(rows) ? rows : [];
    } catch {
      // Enrichment only — the public tier stands on its own.
      accountEntries = [];
    }
  }

  $effect(() => {
    void session;
    readAccounts();
  });

  /**
   * GET /api/node/runtime is Admin-only, so the poller is told about the session
   * rather than discovering it from a 403. §16.5's rule, applied to the dashboard
   * widgets: an anonymous or below-Admin visitor NEVER calls it, and signing out
   * drops the snapshot in the same tick — privileged numbers cannot linger on a
   * page whose session has ended.
   */
  $effect(() => {
    runtimeFeed.setAdmin(canEdit);
  });

  function readAuthStatus() {
    return apiFetch('/api/auth/status')
      .then((s) => {
        rootAdminAvailable = Boolean(s?.root_admin_available);
      })
      .catch(() => {
        rootAdminAvailable = null;
      });
  }

  async function signOut() {
    try {
      await apiFetch('/api/auth/logout', { method: 'POST' });
    } catch {
      // 401 here means "already not signed in" — contract §6b says treat it
      // as success. Local state is cleared either way.
    }
    clearSession();
  }
  const connected = $derived(view != null);

  // Anonymous tier first: every row the public feed knows about. The Admin
  // overlay merges operator facets in on top when there is a session.
  // The self row is removed BEFORE the merge, so it can never come back via
  // an /api/accounts overlay either (standing rule, accounts.js).
  const accountRows = $derived.by(() => {
    const selfId = feedSelfNode?.peerId ?? '';
    return sortAccounts(
      withoutSelf(mergeAccounts(withoutSelf(nodes.map(accountFromNode), selfId), accountEntries), selfId)
    );
  });
  // The synthetic row for an Admin-overlay account with NO peer presence names
  // itself `account`: it is not a peer, so it is not counted as one, and it can
  // never be mistaken for a row whose provenance the node failed to state
  // (peers.js `peerSource`).
  const accountNodes = $derived(accountRows.map((r) => r.node ?? {
    peerId: r.peerId, dn: r.name, org: r.organization, vcard: '', lat: 0, lon: 0,
    geoLabel: '', online: false, isSelf: false, agent: '', uptimeS: 0, lastSeen: r.lastConnected,
    addrs: [], trustLevel: r.trustLevel, role: '', latencyMs: 0, suiteVersion: '',
    standardsVersion: '', source: 'account', pinned: false, pinNote: '', account: r,
  }));
  /**
   * THE HEADER COUNT (owner, 2026-07-30: "it says 35 peers, but only shows one
   * on the globe", "I have no idea what these peers are that are in the table").
   *
   * It used to read `N/M PEERS ONLINE` over a set of 35 that was 34 DHT
   * rendezvous advertisements this node had never dialled. The number was
   * arithmetically true and told the owner nothing he could check. Admission is
   * server-side now — a row is PINNED or CONNECTED — so the header states the
   * set in exactly those terms, and every part of it is verifiable in the SOURCE
   * column below it. `unexplained` should always be 0; if the node ever admits a
   * row that is neither, the header says so in red rather than absorbing it into
   * a total.
   */
  const presence = $derived(presenceSummary(accountNodes));

  /**
   * THE SHELL API a route's `boot(shell)` is handed. Deliberately tiny: a route
   * that needs page-lifetime work needs the node feed and nothing else. Anything
   * wider belongs in the render context below, where it is reactive.
   */
  const nodeSubs = new Set();
  $effect(() => {
    const current = nodes;
    for (const fn of nodeSubs) fn(current);
  });
  const shell = {
    getNodes: () => view?.nodes ?? [],
    subscribeNodes(fn) {
      nodeSubs.add(fn);
      return () => nodeSubs.delete(fn);
    },
  };

  /** The one seam every route reads. See THE SHELL CONTEXT above. */
  const routeCtx = $derived({
    view,
    nodes,
    selfNode,
    accountNodes,
    accountRows,
    runtime,
    now,
    session,
    canEdit,
    rootAdminAvailable,
    presence,
    selected,
    onSelectNode: (n) => (selected = n),
    onRequestSignIn: () => (signInOpen = true),
    onShowSelf: (v) => (selfModal = v),
    onProfileSaved: ({ json, vcard }) => {
      epmOverride = { vcard, dn: typeof json?.dn === 'string' ? json.dn : undefined };
    },
  });

  onMount(() => {
    let unsub = () => {};
    let cancelled = false;
    // main.js starts the runtime before mount; poll briefly in case of races.
    const attach = () => {
      const g = globalThis.SDN_NODE_STATUS;
      if (!g) {
        if (!cancelled) setTimeout(attach, 100);
        return;
      }
      const seed = g.current?.();
      if (seed) view = seed;
      unsub = g.subscribe((v) => (view = v));
    };
    attach();
    // Restore any live session, and learn whether this node's own root phrase
    // is an admin sign-in path (§14.1) so the dialog can say so.
    refreshSession();
    readAuthStatus();
    const clock = setInterval(() => (now = Date.now()), 1000);
    // The NODE dashboard's runtime facts. Anonymous sources only until the
    // session effect above says otherwise.
    runtimeFeed.start();
    // Page-lifetime work each route declares for itself — called for EVERY
    // registered route, not just the active one, because that is what "warm the
    // search model 800ms after first paint" has always meant.
    const teardowns = ROUTES.map((r) => r.boot?.(shell)).filter(Boolean);
    return () => {
      cancelled = true;
      clock && clearInterval(clock);
      runtimeFeed.stop();
      for (const done of teardowns) done();
      unsub();
    };
  });
</script>

<div class="root" style="background:{theme.pageGlow};color:{theme.textBody};">
  <SdnRail sections={SECTIONS} active={route} onSelect={(id) => (route = id)} />
  <main>
    <!-- GRAMMAR L3 (iris-dashboard-grammar-law): the header and the panel column
         are siblings inside the SAME scroll container (`main`), so the reserved
         scrollbar gutter is subtracted from BOTH or from neither and their right
         edges are the same edge. The owner's screenshot showed the header chips
         sitting ~14px right of every panel below them, because `.body` reserved a
         scrollbar track the header did not — an alignment that cannot be fixed by
         tuning a padding, since scrollbar width is not a CSS value. Sticky keeps
         the header in place while the column scrolls; the background is opaque
         because the design's is 0.92 alpha and content would show through it. -->
    <div class="headwrap" style="background:{theme.pageGlow};">
    <ConsoleHeader
      title={(ROUTE_TITLE[route] ?? ROUTE_TITLE[LANDING])[0]}
      sub={(ROUTE_TITLE[route] ?? ROUTE_TITLE[LANDING])[1]}
      accent={theme.cyan}
    >
      {#snippet right()}
        <span class="hdr-status">
          {#if connected}
            <StatusChip label="FEED LIVE" color={theme.green} />
            <StatusChip
              label={presence.pinnedOffline
                ? `${presence.connected} CONNECTED · ${presence.pinnedOffline} PINNED`
                : `${presence.connected} CONNECTED`}
              color={theme.green}
              dot={false}
            />
            {#if presence.unexplained}
              <StatusChip label={`${presence.unexplained} UNEXPLAINED`} color={theme.red} dot={false} />
            {/if}
          {:else}
            <StatusChip label="CONNECTING" color={theme.amber} />
          {/if}
        </span>
        <span class="hdr-session">
          {#if session}
            <!-- The key's fingerprint is the ONE identifier that survives a
                 reload (§4/§6b); the dot marks a key still unlocked in this
                 page (able to sign an attestation), hollow means the session
                 was restored from the cookie alone.

                 Owner directive 2026-07-29: this is the ACCOUNT BUTTON — it
                 opens the account modal. SIGN OUT moved inside it, because a
                 header that offers "leave" but not "look" is backwards. -->
            <!-- The label is WHO YOU ARE, nothing else. It used to append
                 `· <TIER>` — a tag on a menu control, and the tier's third
                 appearance in the UI: it is already a chip and a TRUST row inside
                 the account modal this button opens. The tier still reaches the
                 eye through `color`, which says it without spending a word. The
                 `title` lost its recital of the fingerprint and the session's
                 provenance for the same reason — both are IN the modal.
                 Owner directive 2026-07-30, issued twice. -->
            <button
              type="button"
              class="acct"
              onclick={() => (accountOpen = true)}
              title="Your account"
            >
              <StatusChip
                label={session.name?.trim() || shortFingerprint(session.fingerprint) || 'ACCOUNT'}
                color={sessionTierColor}
                dot={Boolean(session.identity)}
              />
            </button>
          {:else}
            <GBtn title="Sign in with your wallet key" variant="primary" onclick={() => (signInOpen = true)}>SIGN IN</GBtn>
          {/if}
        </span>
      {/snippet}
    </ConsoleHeader>
    </div>

    <div class="body">
      {#if !connected}
        <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
          <span class="glyph" style="color:{theme.cyan};">◍</span>
          Connecting to the node status feed (/ws/status)…
        </div>
      {:else}
        <!-- The registry lookup that replaced the hand-written route branch. An
             unknown route id renders nothing rather than throwing — the rail can
             only offer ids the registry produced, so this is unreachable from the
             UI and is here for a stale deep link. -->
        {@const Route = ROUTE_COMPONENTS[route]}
        {#if Route}
          <Route {...routeCtx} />
        {/if}
      {/if}
    </div>
  </main>

  {#if selected}
    <NodeModal node={selected} {now} onClose={() => (selected = null)} />
  {/if}

  {#if selfModal && selfNode}
    <!-- This node's own card, in the modal every other peer gets — opened on the
         fields by DETAIL and on the scannable QR by QR (owner directive
         2026-07-30; IRIS R6/R5). It replaces the THIS NODE page, whose every fact
         this view already carries. -->
    <NodeModal node={selfNode} {now} initialView={selfModal} onClose={() => (selfModal = '')} />
  {/if}

  {#if signInOpen}
    <SignInModal
      {rootAdminAvailable}
      nodeName={selfTitle}
      onClose={() => (signInOpen = false)}
      onSignedIn={onSignedIn}
    />
  {/if}

  {#if accountOpen && session}
    <AccountModal
      {session}
      {selfNode}
      {rootAdminAvailable}
      onSignOut={() => {
        accountOpen = false;
        signOut();
      }}
      onClose={() => (accountOpen = false)}
    />
  {/if}
</div>
<style>
  .root {
    position: fixed;
    inset: 0;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }
  /* ------------------------------------------------------------------
     DESIGN-LIB SCALE. The owner asked for "+30% font on the dashboard UI"
     on 2026-07-27; that pass reached the RAIL ONLY, by hand, which is why
     he asked again on 2026-07-30. The factor is now the named ladder in
     scale.css and this block carries it to every design-repo primitive.
     Those primitives live in spaceaware-student-sdn and the ZIP-SYNC LAW
     forbids editing that tree — the design tool is its only writer — so
     the scale is applied HERE, in the consumer, exactly as the law
     prescribes (IRIS ruling 2026-07-30 §4: override where the design
     writes SCOPED CSS; use a prop only where it writes an INLINE
     attribute, which is Panel's `pad` alone).
     `.root` prefixes the selectors purely for specificity — the design's
     own rules carry Svelte's scope class, so an unprefixed selector would
     lose — and the element qualifiers matter: a bare `.primary` would
     restyle <tr class="primary"> in the operator-keys tables.
     Never `!important`. Drop this block if a design zip ships these sizes.
     ------------------------------------------------------------------ */
  .root :global(.sdn-rail .brand-name) {
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
  }
  .root :global(.sdn-rail .sec) {
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
  }
  /* ------------------------------------------------------------------
     THE CLUTTER STRIP (owner directive 2026-07-30, issued TWICE:
     "remove 'semantic' and ALL superfluous descriptions / tags from all
     the menus here, you are shitting up the interface with this garbage").

     These three strings are written as MARKUP inside the design tree
     (SpaceAware-Student-UI: SdnRail.svelte's `.brand-sub` and `.fkey`,
     ConsoleHeader.svelte's `.sub`), and that tree is written ONLY by the
     owner's Claude Design tool — no agent, no human hand-edits it
     (ZIP-SYNC LAW). So the strip happens HERE, in the consumer, which is
     the same seam the type ladder above already uses. A design-tool
     correction is filed separately so the next export stops emitting them;
     until that zip lands, this block is what the owner actually sees.

       .brand-sub  "LOCAL NODE · DESKTOP" — false on a served node anyway:
                   sdn.spaceaware.io is neither local nor a desktop.
       .fkey       the N1/N2/N3 column. Nothing in this dashboard binds
                   those keys, so it labelled shortcuts that do not exist.
                   `display:none` (not just empty data) because the span is
                   a flex child and `gap:8px` would still reserve space.
       .sub:empty  ConsoleHeader's route subtitle. ROUTE_TITLE now passes
                   '', and :empty collapses the span so the 13px baseline
                   gap does not trail the title.
     Drop each rule if a design zip ships the string already gone.
     ------------------------------------------------------------------ */
  .root :global(.sdn-rail .brand-sub),
  .root :global(.sdn-rail .fkey),
  .root :global(header .sub:empty) {
    display: none;
  }
  .root :global(.sdn-rail .nav-lbl) {
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
  }
  .root :global(.sdn-rail .nav-ico) {
    font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title);
  }
  .root :global(.sdn-rail .nav-i) {
    height: 56px; /* 46 -> 56 so the taller glyph + label are not cramped */
  }
  .root :global(.sdn-rail .brand) {
    height: 52px; /* 44 -> 52, matching the larger wordmark */
  }
  /* The expanded flyout has to grow or "PERMISSIONS N3" clips: the label
     column is (width - 64px icon gutter). 218px left ~154px, and the label
     needs ~191px at the value rung. */
  .root :global(aside.sdn-rail:hover),
  .root :global(aside.sdn-rail.pinned) {
    width: 286px;
  }
  /* GBtn (design GBtn.svelte): the owner's "edit buttons need to be bigger
     and vertically centered". 22px line + 2x8px padding + 2x1px border = a
     deterministic 40px box. Element-qualified so <tr class="primary"> is
     never caught. */
  .root :global(button.neutral),
  .root :global(button.primary),
  .root :global(button.destructive),
  .root :global(a.neutral),
  .root :global(a.primary),
  .root :global(a.destructive) {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    padding: var(--sdn-sp-3) var(--sdn-sp-9);
    min-height: 40px;
    min-width: 80px;
  }
  /* StatusChip (design StatusChip.svelte) — the control tier, so a chip never
     out-shouts the button beside it. */
  .root :global(.chip) {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    padding: var(--sdn-sp-2) var(--sdn-sp-6);
  }
  .root :global(.chip .dot) { width: 9px; height: 9px; }
  /* ConsoleHeader (design shell/ConsoleHeader.svelte) — the page title block.
     Qualified by the `header` element on purpose: the design's `.ttl` class is
     also an SDN class in AccountAdmin, and a bare :global(.ttl) would tie on
     specificity and then win or lose on source order. No SDN component renders
     a <header>, so this reaches ConsoleHeader and nothing else.
     Chrome sits deliberately below content: kicker/sub on the control tier,
     and the page title at the ladder's top rung. */
  .root :global(header .kick),
  .root :global(header .sub) {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
  }
  .root :global(header .ttl) {
    font-size: var(--sdn-fs-hero);
    line-height: var(--sdn-lh-hero);
  }
  .root :global(header .peers) {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    padding: var(--sdn-sp-2) var(--sdn-sp-6);
  }

  /* GRAMMAR L1 + L3 (iris-dashboard-grammar-law): `main` is THE scroll context —
     the only one on the page — and the header and the route body are its two
     children. The scroll happens OUTSIDE both, so the reserved scrollbar track is
     subtracted from the same box for both and their right edges coincide by
     construction rather than by arithmetic. `scrollbar-gutter` appears exactly
     once in this stylesheet, here. */
  main {
    position: absolute;
    left: 66px;
    right: 0;
    top: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    overflow-x: hidden;
    overflow-y: auto;
    scrollbar-gutter: stable;
  }
  /* The header travels with the scroll. It must be opaque: the design's own
     background is 0.92 alpha, and panels sliding under it would show through. */
  .headwrap {
    position: sticky;
    top: 0;
    z-index: 20;
    flex: none;
  }
  /* L2: ONE gutter, applied by ONE element — this one. Nothing inside a route may
     add `padding-inline` "to match" (that is how the ACCOUNTS panels ended up 37px
     narrower than the panels above them). This is also the flex column every
     route's markup becomes the children of. */
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: var(--sdn-sp-7) var(--sdn-sp-9) var(--sdn-sp-9);
  }
  /* The shell's own "no feed yet" block. Routes that need this shape carry their
     own copy, in their own directory — the rule is nine lines and duplicating it
     is what keeps two routes off one file. */
  .empty {
    display: flex;
    align-items: center;
    gap: 12px;
    border: 1px solid;
    padding: 26px 28px;
    font-size: var(--sdn-fs-lead); line-height: var(--sdn-lh-lead);
    letter-spacing: 0.06em;
    max-width: 560px;
  }
  .empty .glyph { font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title); }

  .hdr-status { display: contents; }
  .hdr-session { display: contents; }
  /* The account button is the session chip itself: same pixels, now clickable
     (owner directive 2026-07-29). No new chrome, no second control. */
  .acct {
    background: transparent;
    border: 0;
    padding: 0;
    margin: 0;
    font: inherit;
    color: inherit;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
  }
  .acct:hover { filter: brightness(1.15); }
  .acct:focus-visible { outline: 1px solid currentColor; outline-offset: 2px; }

  /* L3, MEASURED RATHER THAN ASSUMED. With header and panels finally sharing one
     content box, their right edges agree exactly at 1280 / 1440 / 1920 — measured
     0.0px — but at 900 the header's right cluster was still 25px PAST the panel
     edge, because the title plus three controls do not fit 771px and the cluster
     overflowed its own padding box. An overflowing control cannot be aligned by
     any padding.
     The rule that already existed for phones is simply applied at the width where
     the header actually stops fitting: the two STATUS chips go, the actionable
     account control stays. Nothing they said is lost — the swarm counts are in
     PEER MAP's own chips on NODE (`N LINKS`, `N PEERS`) and in the meta line on
     PEERS, and this node's liveness is NODE HEALTH's whole hero. Threshold is the
     grid's own 1180px, not a new number. */
  @media (max-width: 1180px) {
    .hdr-status { display: none; }
  }

  /* Narrow screens (owner report: taps landing on clipped targets): the
     desktop half-and-half no-scroll layout collapses tap targets into
     140px strips. On phones the page scrolls naturally instead: panels
     take their content height, the table shows its 5 rows fully, the
     globe gets a fixed workable height. Desktop is untouched. */
  @media (max-width: 760px) {
    /* The page scroller is `main` at every width (L1) — this block used to add
       `overflow-y:auto` here, which was the phone's second scroller. Only the
       gutter narrows. */
    .body {
      padding: 12px 12px 24px;
    }
    .hdr-status { display: none; }
    .hdr-session {
      display: inline-flex;
      gap: 6px;
      align-items: center;
      padding-right: 8px;
    }
  }
</style>
