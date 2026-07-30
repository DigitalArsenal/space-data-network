<script>
  /**
   * SDN Node Status $APP view — dashboard v2 (graph task nst-dashboard-table).
   *
   * Composes the DESIGN repo's shell (SdnRail + ConsoleHeader) around one
   * "NETWORK NODES" route: an mmdb-geo globe + a searchable, sortable,
   * trust-filterable node table; row/dot click opens the full-detail modal
   * (parsed vCard). Search is semantic when the node serves /embedding/*
   * assets (MiniLM int8 via onnxruntime-web, same-origin, fail-open) and
   * plain substring always. Data comes EXCLUSIVELY from
   * globalThis.SDN_NODE_STATUS; this view never fetches node data itself.
   *
   * PROVISIONAL (Iris ruling): the next design-tool zip supersedes this view.
   * The data seam (SDN_NODE_STATUS) survives. Styled ONLY with theme.js
   * tokens and the design components — no invented palette.
   */
  import { onMount } from 'svelte';
  import SdnRail from 'spaceaware-student-sdn/src/lib/shell/SdnRail.svelte';
  import ConsoleHeader from 'spaceaware-student-sdn/src/lib/shell/ConsoleHeader.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeTable from './NodeTable.svelte';
  import NodeModal from './NodeModal.svelte';
  import NodeConsole from './NodeConsole.svelte';
  import PeerMap from './PeerMap.svelte';
  import Pager from './Pager.svelte';
  import NodeEditForm from './NodeEditForm.svelte';
  import AccountAdmin from './AccountAdmin.svelte';
  import SignInModal from './SignInModal.svelte';
  import AccountModal from './AccountModal.svelte';
  import { shortId } from './format.js';
  import { TRUST_TIERS, normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { canEditNodeProfile, canManagePermissions } from './permissions.js';
  import { accountFromNode, mergeAccounts, sortAccounts, vcardDisplayName, withoutSelf } from './accounts.js';
  import { apiFetch } from './api.js';
  import { xpubFingerprint, fingerprintMatches, shortFingerprint } from './wallet.js';
  import { applySettings, substringSearch, semanticRank, sortNodes, nodeEmbedText } from './filters.js';
  import { presenceSummary } from './peers.js';
  import { createSemanticEngine } from './semantic.js';
  import { createRuntimeFeed, foldRuntime } from './runtime.js';

  /**
   * The rail, from the design source rather than from taste: the template's own
   * ROUTES table is `['node','NODE','◉','N1'], ['peers','PEERS','◍','N2'],
   * ['groups','GROUPS','⬡','N3'], …` (`SDN Console.dc.html:892`), so NODE and
   * PEERS are verbatim and ACCOUNTS inherits ⬡ — the template's third glyph,
   * freed by the death of THIS NODE.
   *
   * THIS NODE IS GONE (owner directive 2026-07-30: "the 'this node' menu should
   * go away"). Nothing was lost with it: every fact its widgets carried — xpub,
   * derivation paths, signing/encryption keys, EPM signature/timestamp/CID, chain
   * addresses, multiaddrs, trust, the vCard and the QR — is in the node detail
   * modal that every OTHER peer already gets, and the self node is a node (IRIS
   * R6). The IDENTITY widget's DETAIL button opens it.
   *
   * PEERS IS NEW (owner directive 2026-07-30: "move the peers table with add peer
   * form, along with the globe, to a whole new menu called Peers").
   */
  /*
   * NO `fkey`. The design source's rail renders `<span class="fkey">{it.fkey}</span>`
   * beside every label, which is where the N1/N2/N3 column came from. Those keys
   * are not even bound to anything — nothing in this dashboard listens for them —
   * so the column was a tag describing a shortcut that does not exist. The data is
   * dropped HERE (this table is the consumer's) and the span is suppressed in the
   * rail-override block at the bottom of this file, because the markup itself
   * lives in the design tree and the design tree is not hand-edited (ZIP-SYNC LAW).
   */
  const SECTIONS = [
    {
      label: 'NETWORK',
      items: [
        { id: 'node', label: 'NODE', glyph: '◉' },
        { id: 'peers', label: 'PEERS', glyph: '◍' },
        { id: 'accounts', label: 'ACCOUNTS', glyph: '⬡' },
      ],
    },
  ];

  /*
   * A ROUTE IS ITS NAME. The second element used to carry an explanatory
   * subtitle ('· DASHBOARD', '· SWARM MAP & PEER DIRECTORY', '· OPERATOR KEYS &
   * TRUST') rendered next to the 29px route title — a description of a page the
   * user is already looking at. Owner directive 2026-07-30 (twice). The shape
   * stays a pair so `ROUTE_TITLE[route][1]` keeps working; the subtitle is empty
   * and ConsoleHeader's `.sub` span collapses (see the override block).
   */
  const ROUTE_TITLE = {
    node: ['NODE', ''],
    peers: ['PEERS', ''],
    accounts: ['ACCOUNTS', ''],
  };

  /**
   * ACCOUNTS' two tables are TABS now — one visible at a time (owner 2026-07-30).
   *
   * NOT "TRUSTED PEERS". That tab is the libp2p trust REGISTRY, whose entries
   * carry a trust level that may be `never` — calling the whole list "trusted"
   * asserted of every row the one thing the row is there to record. The tab is
   * named after the registry, and its chip already counts honestly ("N IN
   * REGISTRY"). Part of the same correction as the header count: nothing on this
   * page calls a set "trusted" on its behalf.
   */
  const ACCOUNT_TABS = [
    ['peers', 'PEER REGISTRY'],
    ['keys', 'OPERATOR KEYS'],
  ];

  const HIDE_KEY = 'sdn.dashboard.hideUntrustedOffline';
  const readHidePref = () => {
    try {
      const raw = globalThis.localStorage?.getItem(HIDE_KEY);
      return raw === null || raw === undefined ? true : raw === '1';
    } catch {
      return true;
    }
  };

  /** @type {import('../../src/status/view-model').NodeStatusSetView | null} */
  let view = $state(null);
  let now = $state(Date.now());
  let route = $state('node');
  /**
   * The node's own runtime facts for the NODE dashboard (runtime.js). Starts as
   * the empty fold so every widget renders from one shape and never has to guard
   * for "not loaded yet" — an absent number is an absent cell, by construction.
   */
  let runtime = $state(foldRuntime());
  /** Admin-only enrichment rows from GET /api/accounts (§16.5). */
  let accountEntries = $state([]);
  let query = $state('');
  let trustTier = $state('all');
  let hideUntrustedOffline = $state(readHidePref());
  let sortKey = $state('trust');
  let sortDir = $state(1);
  let selected = $state(null);
  /**
   * The detail modal for THIS node, opened from the IDENTITY widget: '' closed,
   * 'parsed' on the fields, 'qr' straight on the scannable card (owner directive
   * 2026-07-30 — the QR belongs in a modal). It is a separate piece of state from
   * `selected` so opening this node's own card cannot be confused with selecting
   * a peer.
   */
  let selfModal = $state('');
  let accountTab = $state('peers');
  let settingsOpen = $state(false);
  let page = $state(0);
  let semStatus = $state('idle');
  /** @type {Map<string, number> | null} */
  let semScores = $state(null);

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
  let editing = $state(false);
  /** Post-save profile echo, held until the status feed catches up. */
  let epmOverride = $state(null);

  const PAGE_SIZE = 5;

  const runtimeFeed = createRuntimeFeed({ onUpdate: (r) => (runtime = r) });

  const engine = createSemanticEngine({ onStatus: (s) => (semStatus = s) });
  // Diagnostic seam (same spirit as SDN_NODE_STATUS): lets operators probe
  // embeddings/scores from the console; the UI never reads it back.
  //
  // Renamed off SDN_SEMANTIC deliberately. This global was NEVER rendered — it is
  // a console handle — but it was the last occurrence of the token the owner named
  // twice, and a reviewer grepping the served page for "SEMANTIC" would have found
  // it and concluded the fix had missed again. The acceptance gate is a clean grep,
  // so the token is gone from the bundle entirely.
  globalThis.SDN_SEARCH_RANKER = engine;

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
    editing = false;
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
  const updatedAgo = $derived(
    view ? Math.max(0, Math.round((now - view.generatedAt) / 1000)) : null
  );

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
  const visible = $derived(applySettings(accountNodes, { trustTier, hideUntrustedOffline }));
  const searching = $derived(Boolean(query.trim()));
  const semanticActive = $derived(searching && semStatus === 'ready' && semScores !== null);

  /** @type {{node: any, score?: number}[]} */
  const rows = $derived.by(() => {
    if (!searching) return sortNodes(visible, sortKey, sortDir).map((node) => ({ node }));
    const subs = substringSearch(visible, query);
    if (semanticActive) {
      return semanticRank(visible, semScores, new Set(subs.map((n) => n.peerId)));
    }
    return sortNodes(subs, sortKey, sortDir).map((node) => ({ node }));
  });

  const pageCount = $derived(Math.max(1, Math.ceil(rows.length / PAGE_SIZE)));
  const safePage = $derived(Math.min(page, pageCount - 1));
  const pagedRows = $derived(rows.slice(safePage * PAGE_SIZE, (safePage + 1) * PAGE_SIZE));

  // Any change to the filtered set snaps back to the first page.
  $effect(() => {
    void query;
    void trustTier;
    void hideUntrustedOffline;
    void sortKey;
    void sortDir;
    page = 0;
  });

  /*
   * OWNER DIRECTIVE 2026-07-30, twice: "also remove 'semantic' and ALL
   * superfluous descriptions / tags from all the menus here, you are shitting up
   * the interface with this garbage".
   *
   * The search-mode chip is GONE — label, colour and tooltip together. It was a
   * status word bolted onto a search control, which is the exact class the owner
   * named. `semStatus` is still what selects the ranking function below; the
   * search box simply no longer narrates which one won. A control is its label.
   */

  function toggleSort(key) {
    if (sortKey === key) sortDir = -sortDir;
    else {
      sortKey = key;
      sortDir = 1;
    }
  }

  function setHidePref(checked) {
    hideUntrustedOffline = checked;
    try {
      globalThis.localStorage?.setItem(HIDE_KEY, checked ? '1' : '0');
    } catch {
      /* private mode etc. — non-persistent is fine */
    }
  }

  // Keep node embeddings current whenever the feed updates (no-op until ready).
  $effect(() => {
    if (semStatus === 'ready' && nodes.length) {
      engine.embedNodes(nodes, nodeEmbedText);
    }
  });

  // Debounced query embedding → semScores drives the semantic ranking.
  $effect(() => {
    const q = query.trim();
    if (!q || semStatus !== 'ready') {
      semScores = null;
      return;
    }
    const t = setTimeout(async () => {
      const scores = await engine.queryScores(q);
      if (query.trim() === q) semScores = scores;
    }, 220);
    return () => clearTimeout(t);
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
    // Warm the semantic model shortly after first paint (fail-open).
    const warm = setTimeout(() => engine.init(), 800);
    return () => {
      cancelled = true;
      clock && clearInterval(clock);
      clearTimeout(warm);
      runtimeFeed.stop();
      unsub();
    };
  });
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && settingsOpen && (settingsOpen = false)} />

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
    <!-- The title fallback used to name `ROUTE_TITLE.nodes`, a key that has never
         existed: an unknown route rendered `undefined[0]` and threw. It is the
         landing route's title now, which is what a fallback is for. -->
    <ConsoleHeader
      title={(ROUTE_TITLE[route] ?? ROUTE_TITLE.node)[0]}
      sub={(ROUTE_TITLE[route] ?? ROUTE_TITLE.node)[1]}
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
      {:else if route === 'node'}
        <!-- THE NODE DASHBOARD — the SDN Console template's view, with EDIT
             LAYOUT and the full eight-widget registry (IRIS §2/§3 wave 2). -->
        {#if selfNode}
          {#if editing}
            <!-- The IDENTITY card's EDIT opens the profile form INLINE, in a
                 raised panel on this page — "less like a modal, more like a
                 page" (owner 2026-07-27; IRIS §6). -->
            <Panel variant="raised" pad="0" style="max-width:880px;">
              <div class="self-body">
                <NodeEditForm
                  onCancel={() => (editing = false)}
                  onSaved={({ json, vcard }) => {
                    epmOverride = { vcard, dn: typeof json?.dn === 'string' ? json.dn : undefined };
                    editing = false;
                  }}
                />
              </div>
            </Panel>
          {:else}
            <div class="node-page">
              <NodeConsole
                node={selfNode}
                {nodes}
                {runtime}
                {now}
                {canEdit}
                onSelectNode={(n) => (selected = n)}
                onEdit={() => (editing = true)}
                onShowDetail={() => (selfModal = 'parsed')}
                onShowQr={() => (selfModal = 'qr')}
              />
            </div>
          {/if}
        {:else}
          <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
            <span class="glyph" style="color:{theme.cyan};">◉</span>
            Waiting for this node's status entry…
          </div>
        {/if}
      {:else if route === 'peers'}
        <div class="toolbar">
          <div class="search" style="border-color:{theme.hairline};">
            <span class="sglyph" style="color:{theme.textMuted};">⌕</span>
            <input
              type="search"
              placeholder="SEARCH"
              bind:value={query}
              style="color:{theme.textBright};"
              aria-label="Search nodes"
            />
          </div>

          <label class="ctl" style="color:{theme.textMuted};">
            TRUST
            <select bind:value={trustTier} style="color:{theme.textBright};border-color:{theme.hairline};" aria-label="Filter by trust tier">
              <option value="all">ALL</option>
              {#each TRUST_TIERS as tier (tier)}
                <option value={tier}>{tier.toUpperCase()}</option>
              {/each}
            </select>
          </label>

          <div class="settings-wrap">
            <button
              class="settings-btn"
              style="color:{settingsOpen ? theme.cyan : theme.textMuted};border-color:{settingsOpen ? theme.cyan : theme.hairline};"
              onclick={() => (settingsOpen = !settingsOpen)}
              aria-expanded={settingsOpen}
              aria-haspopup="true"
            ><span class="gear">⚙</span> SETTINGS</button>
            {#if settingsOpen}
              <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
              <div class="settings-backdrop" onclick={() => (settingsOpen = false)}></div>
              <div class="settings-menu" style="background:{theme.panelRaised};border-color:{theme.panelBorder};">
                <div class="settings-title" style="color:{theme.textMuted};border-color:{theme.divider};">DISPLAY SETTINGS</div>
                <!-- This menu said the same thing THREE times: the checkbox
                     label, a 96-character `title` restating it, and a hint
                     paragraph restating it again. The label survives; the other
                     two are the "garbage" the owner named (2026-07-30, twice). -->
                <label class="ctl check" style="color:{theme.textBody};">
                  <input
                    type="checkbox"
                    checked={hideUntrustedOffline}
                    onchange={(e) => setHidePref(e.currentTarget.checked)}
                  />
                  HIDE UNTRUSTED OFFLINE
                </label>
              </div>
            {/if}
          </div>
        </div>

        <!-- The table's own arithmetic, stated as a subset rather than as a
             total: a filter is on this toolbar and "12 PEERS" beside a table of
             three is the same class of defect as the header count was. -->
        <div class="meta" style="color:{theme.textMuted};">
          <span>{rows.length} OF {accountRows.length} SHOWN</span>
          <span class="dot">·</span>
          <span>{presence.connected} CONNECTED</span>
          {#if presence.pinnedOffline}
            <span class="dot">·</span>
            <span>{presence.pinnedOffline} PINNED, OFFLINE</span>
          {/if}
          <span class="dot">·</span>
          <span>SOURCE {shortId(view?.sourcePeerId ?? '')}</span>
          <span class="dot">·</span>
          <span>UPDATED {updatedAgo === 0 ? 'now' : `${updatedAgo}s ago`}</span>
        </div>

        <!-- THE PEERS ROUTE (owner directive 2026-07-30). The swarm map and the
             peer directory it plots, in one column: the SAME PeerMap component the
             dashboard's netmap widget renders, never a second globe surface (IRIS
             R7 + the grammar law's companion trap — two mounted globes are two
             WebGL contexts). L5: no panel here carries a width, a max-width or its
             own inline padding, so every edge is the column's edge. -->
        <div class="stack">
          <Panel variant="raised">
            <PeerMap
              nodes={accountNodes}
              selectedId={selected?.peerId ?? ''}
              onSelectNode={(n) => (selected = n)}
              height="420px"
            />
          </Panel>
          <Panel variant="raised" pad="0">
            <div class="table-panel">
              <NodeTable
                rows={pagedRows}
                {now}
                {sortKey}
                {sortDir}
                onSort={toggleSort}
                onOpen={(n) => (selected = n)}
                {semanticActive}
              />
              <!-- L4: the ONE shared pager. This was an inline copy; the ACTIVITY
                   LOG widget needed the same control, and two implementations is
                   how "widgets paginate" becomes "widgets mostly paginate". -->
              <Pager
                page={safePage}
                pageCount={pageCount}
                total={rows.length}
                pageSize={PAGE_SIZE}
                unit="ROWS"
                onPage={(p) => (page = p)}
              />
            </div>
          </Panel>

          {#if session && canManagePermissions(session.trustLevel)}
            <!-- ADD A PEER lives with the peers (owner directive 2026-07-30). It
                 is the SAME component the ACCOUNTS tab uses, rendering only its
                 form section — one set of API calls, one piece of state. -->
            <AccountAdmin
              {session}
              nodes={accountNodes}
              {rootAdminAvailable}
              onRequestSignIn={() => (signInOpen = true)}
              view="peer-add"
            />
          {/if}
        </div>
      {:else}
        <!-- ACCOUNTS: the two admin tables, as TABS, one at a time (owner
             directive 2026-07-30 — "have the tables selectable by tab (trusted
             peers vs current node operator keys)"). The globe and the node
             directory have moved to PEERS. -->
        {#if session && canManagePermissions(session.trustLevel)}
          <div class="tabbar" style="border-color:{theme.panelBorder};">
            {#each ACCOUNT_TABS as [id, label] (id)}
              <button
                class="tab"
                style="background:{accountTab === id ? 'rgba(53,201,216,0.18)' : 'transparent'};color:{accountTab === id ? theme.cyan : theme.textDim};border-color:{theme.panelBorder};"
                aria-pressed={accountTab === id}
                onclick={() => (accountTab = id)}
              >{label}</button>
            {/each}
          </div>
          <div class="account-admin">
            <AccountAdmin
              {session}
              nodes={accountNodes}
              {rootAdminAvailable}
              onRequestSignIn={() => (signInOpen = true)}
              view={accountTab}
            />
          </div>
        {:else}
          <!-- Honest, not empty: the two surfaces on this page are Admin-only
               reads (/api/auth/users, /api/peers), so an anonymous visitor is told
               what this page is and how to reach it rather than shown a table
               with nothing in it. The public peer directory is on PEERS. -->
          <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
            <span class="glyph" style="color:{theme.cyan};">⬡</span>
            Operator keys and the trust registry are administrator surfaces. Sign in to manage them —
            the public peer directory is on PEERS.
          </div>
          {#if !session}
            <div class="signin-row">
              <GBtn title="Sign in with your wallet key" variant="primary" onclick={() => (signInOpen = true)}>SIGN IN</GBtn>
            </div>
          {/if}
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
     narrower than the panels above them). */
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: var(--sdn-sp-7) var(--sdn-sp-9) var(--sdn-sp-9);
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 18px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 1px solid;
    padding: 7px 10px;
    flex: 1 1 320px;
    min-width: 260px;
    max-width: 560px;
  }
  .sglyph { font-size: var(--sdn-fs-head); line-height: var(--sdn-lh-head); }
  .search input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
    letter-spacing: 0.06em;
    min-width: 0;
  }
  .search input::placeholder { color: rgba(159, 212, 245, 0.35); }
  /* MENU SCALE. Originally the owner's 2026-07-27 directive, applied here by
     hand. The comment that stood here claimed "body text, tables and panels
     are deliberately untouched: they were already scaled by the earlier
     global directive" — that was FALSE, and it is why the owner had to give
     the same instruction twice: measured on the built page, tables rendered
     at 10-14.5px, i.e. unscaled. Sizes now come from the ladder in
     scale.css, which covers every surface; padding/tracking stay nudged only
     where larger glyphs would clip. */
  .ctl {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body);
    letter-spacing: 0.14em; /* eased from 0.16em so TRUST/HIDE… stay on one line */
    white-space: nowrap;
  }
  /* L6 (owner directive 2026-07-30: "the arrow in the drop down should have
     margin"): a <select> reserves INSET space for the chevron the platform draws
     inside its box. Without it "ALL" sat against the arrow and the arrow against
     the border. --sdn-sp-8 (20px) is the law's floor for this. */
  .ctl select {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note);
    letter-spacing: 0.08em;
    padding: 6px 10px;
    padding-right: var(--sdn-sp-8);
    outline: none;
  }
  .ctl select option { background: #0a141b; }
  .ctl.check { cursor: pointer; user-select: none; }
  .ctl.check input {
    appearance: none;
    width: 17px; /* the tick keeps pace with its label */
    height: 17px;
    border: 1px solid rgba(110, 170, 190, 0.5);
    background: transparent;
    cursor: pointer;
    display: inline-grid;
    place-content: center;
    margin: 0;
  }
  .ctl.check input::before {
    content: '';
    width: 9px;
    height: 9px;
    transform: scale(0);
    background: #35c9d8;
  }
  .ctl.check input:checked::before { transform: scale(1); }
  /* THE METALINE WRAPS (defect, owner's 390px screenshot 2026-07-30). This was a
     nowrap flex row inside `main`, which is `overflow-x: hidden` — so at phone
     widths it did not scroll and it did not wrap, it was CUT: measured 592px of
     content in a 285px box, losing the last 307px. The words it lost were the
     ones the page was rewritten to say ("1 PINNED, OFFLINE", "UPDATED …"), and
     it is the surface the header's own count chips DELEGATE to below 1180px
     (see the L3 block near the end of this file) — the one place a phone can
     read them. Two rules do it:
       flex-wrap  the LIST of facts breaks onto as many lines as it needs;
       nowrap     a single fact never splits, so "3 OF 3 SHOWN" cannot render as
                  "3 OF" / "3 SHOWN" the way it did at 390px. */
  .meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 9px;
    row-gap: 2px;
    font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data);
    letter-spacing: 0.14em;
    margin-bottom: 12px;
  }
  .meta > span { white-space: nowrap; }
  .meta .dot { opacity: 0.5; }
  /* L1 + L5: a plain column of panels, each at its NATURAL height, all the same
     width. It used to be a flex race (`flex: 1 1 50%` on each panel against a
     `min-height:480px` parent), which is what squeezed a panel to zero height
     while its pager kept painting — the overlap in the owner's screenshot. The
     page scrolls; nothing here competes for height. */
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--sdn-sp-7);
  }
  /* ACCOUNTS' tab bar. Same control vocabulary as the PEER MAP's 3D/2D switch, so
     the page has one idea of what a segmented control looks like. */
  .tabbar {
    display: inline-flex;
    border: 1px solid;
    margin-bottom: var(--sdn-sp-7);
    align-self: flex-start;
  }
  .tab {
    border: 0;
    cursor: pointer;
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.12em;
    padding: var(--sdn-sp-3) var(--sdn-sp-8);
  }
  .tab + .tab { border-left: 1px solid; }
  .signin-row { margin-top: var(--sdn-sp-6); }
  /* L1: no `flex: 1` height race and no inner scroller — the table is as tall as
     its five rows and the pager sits under it. */
  .table-panel { display: flex; flex-direction: column; }
  .settings-wrap { position: relative; }
  .settings-backdrop { position: fixed; inset: 0; z-index: 29; }
  .settings-btn {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body);
    letter-spacing: 0.14em;
    padding: 8px 14px;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: var(--sdn-sp-3);
  }
  /* L6 (owner directive 2026-07-30: "the settings icon needs to be full size"): a
     glyph beside a text label renders at the label's rung or ONE ABOVE it, never
     below. The gear was inheriting the button's --sdn-fs-body and reading as a
     rendering failure next to its own word; it now sits one rung up at
     --sdn-fs-data with its own line-height so the button box does not grow. */
  .gear {
    font-size: var(--sdn-fs-data);
    line-height: 1;
  }
  .settings-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 30;
    border: 1px solid;
    /* Widened for the scaled type so the hint does not re-wrap — but
       min-width beats max-width in CSS, so the cap has to live INSIDE the
       min() or a 390px phone pushes the popover off the left edge. */
    min-width: min(390px, calc(100vw - 32px));
    max-width: calc(100vw - 32px);
    padding: 14px 16px 15px;
    box-shadow: 0 14px 44px rgba(0, 0, 0, 0.5);
  }
  .settings-title {
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.16em;
    border-bottom: 1px solid;
    padding-bottom: 7px;
    margin-bottom: 10px;
  }
  .account-admin { margin-top: var(--sdn-sp-7); flex: none; }
  .self-body { padding: 14px 18px 18px; }
  /* The NODE dashboard takes its natural height and lets .body scroll — the
     widget grid is the page, so a nested scroller would trap the last row. */
  .node-page { flex: none; min-width: 0; padding-bottom: 8px; }
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
    .toolbar {
      gap: 10px;
    }
    .search {
      flex-basis: 100%;
      max-width: none;
    }
    /* At the +30% menu size the popover is wider than the toolbar row it
       hangs off, and that row has wrapped — anchoring to it pushes the menu
       off the left edge. Pin it to the viewport instead; the existing
       full-screen backdrop already dismisses it. */
    .settings-menu {
      position: fixed;
      inset: auto 12px 12px 12px;
      min-width: 0;
      max-width: none;
    }
    .hdr-status { display: none; }
    .hdr-session {
      display: inline-flex;
      gap: 6px;
      align-items: center;
      padding-right: 8px;
    }
  }

  /* Phones — the same 560px tier NodeTable's column ladder uses, so the table
     and the line above it change shape at one width rather than two.
     scale.css assigns the metaline rung to `micro`; at --sdn-fs-data its
     longest single fact ("SOURCE 16Uiu2…1uF5PP") measures 266px, which still
     does not fit a 320px viewport's 215px content box even after wrapping, and
     wrapping alone spends five lines at 390px. At `micro` it fits every phone
     width and costs four. Desktop keeps `data` — this is the one rung the
     narrow tier lowers. */
  @media (max-width: 560px) {
    .meta {
      font-size: var(--sdn-fs-micro);
      line-height: var(--sdn-lh-micro);
    }
  }
  .empty .glyph { font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title); }
</style>
