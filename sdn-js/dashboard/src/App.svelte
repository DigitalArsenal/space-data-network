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
  import NodeDetail from './NodeDetail.svelte';
  import NodeEditForm from './NodeEditForm.svelte';
  import Permissions from './Permissions.svelte';
  import SignInModal from './SignInModal.svelte';
  import Globe from './Globe.svelte';
  import { shortId } from './format.js';
  import { TRUST_TIERS, normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { canEditNodeProfile } from './permissions.js';
  import { apiFetch } from './api.js';
  import { xpubFingerprint, fingerprintMatches, shortFingerprint } from './wallet.js';
  import { applySettings, substringSearch, semanticRank, sortNodes, nodeEmbedText } from './filters.js';
  import { createSemanticEngine } from './semantic.js';

  const SECTIONS = [
    {
      label: 'NETWORK',
      items: [
        { id: 'self', label: 'THIS NODE', glyph: '◉', fkey: 'N1' },
        { id: 'nodes', label: 'NODES', glyph: '◍', fkey: 'N2' },
        { id: 'permissions', label: 'PERMISSIONS', glyph: '◈', fkey: 'N3' },
      ],
    },
  ];

  const ROUTE_TITLE = {
    self: ['THIS NODE', '· NODE IDENTITY & STATUS'],
    nodes: ['NETWORK NODES', '· LIVE NODE STATUS'],
    permissions: ['PERMISSIONS', '· OPERATOR KEYS & PEER TRUST'],
  };

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
  let route = $state('self');
  let query = $state('');
  let trustTier = $state('all');
  let hideUntrustedOffline = $state(readHidePref());
  let sortKey = $state('trust');
  let sortDir = $state(1);
  let selected = $state(null);
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
  /** @type {boolean|null} null until /api/auth/status answers. */
  let adminConfigured = $state(null);
  let signInOpen = $state(false);
  let editing = $state(false);
  /** Post-save profile echo, held until the status feed catches up. */
  let epmOverride = $state(null);

  const PAGE_SIZE = 5;

  const engine = createSemanticEngine({ onStatus: (s) => (semStatus = s) });
  // Diagnostic seam (same spirit as SDN_NODE_STATUS): lets operators probe
  // embeddings/scores from the console; the UI never reads it back.
  globalThis.SDN_SEMANTIC = engine;

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
  const selfTitle = $derived(selfNode ? selfNode.dn?.trim() || selfNode.org?.trim() || shortId(selfNode.peerId) : '');
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
      fingerprint: result.user?.xpub_fingerprint ?? result.localFingerprint ?? '',
      xpub: result.identity.xpub,
      identity: result.identity,
    };
    apiFetch('/api/auth/status')
      .then((s) => (adminConfigured = Boolean(s?.admin_configured)))
      .catch(() => {});
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
  const onlinePeers = $derived(nodes.filter((n) => !n.isSelf && n.online).length);
  const totalPeers = $derived(nodes.filter((n) => !n.isSelf).length);
  const connected = $derived(view != null);
  const updatedAgo = $derived(
    view ? Math.max(0, Math.round((now - view.generatedAt) / 1000)) : null
  );

  const visible = $derived(applySettings(nodes, { trustTier, hideUntrustedOffline }));
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

  const searchModeLabel = $derived(
    semStatus === 'ready' ? 'SEMANTIC' : semStatus === 'loading' ? 'MODEL…' : 'TEXT'
  );
  const searchModeColor = $derived(
    semStatus === 'ready' ? theme.cyan : semStatus === 'loading' ? theme.amber : theme.textMuted
  );

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
    // Restore any live session and learn whether this node has an admin yet
    // (drives the first-admin bootstrap notice in the sign-in dialog).
    refreshSession();
    apiFetch('/api/auth/status')
      .then((s) => (adminConfigured = Boolean(s?.admin_configured)))
      .catch(() => (adminConfigured = null));
    const clock = setInterval(() => (now = Date.now()), 1000);
    // Warm the semantic model shortly after first paint (fail-open).
    const warm = setTimeout(() => engine.init(), 800);
    return () => {
      cancelled = true;
      clock && clearInterval(clock);
      clearTimeout(warm);
      unsub();
    };
  });
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && settingsOpen && (settingsOpen = false)} />

<div class="root" style="background:{theme.pageGlow};color:{theme.textBody};">
  <SdnRail sections={SECTIONS} active={route} onSelect={(id) => (route = id)} />
  <main>
    <ConsoleHeader
      title={(ROUTE_TITLE[route] ?? ROUTE_TITLE.nodes)[0]}
      sub={(ROUTE_TITLE[route] ?? ROUTE_TITLE.nodes)[1]}
      accent={theme.cyan}
    >
      {#snippet right()}
        <span class="hdr-status">
          {#if connected}
            <StatusChip label="FEED LIVE" color={theme.green} />
            <StatusChip label={`${onlinePeers}/${totalPeers} PEERS ONLINE`} color={theme.ice} dot={false} />
          {:else}
            <StatusChip label="CONNECTING" color={theme.amber} />
          {/if}
        </span>
        <span class="hdr-session">
          {#if session}
            <!-- The key's fingerprint is the ONE identifier that survives a
                 reload (§4/§6b); the dot marks a key still unlocked in this
                 page (able to sign an attestation), hollow means the session
                 was restored from the cookie alone. -->
            <StatusChip
              label={`${shortFingerprint(session.fingerprint) || session.name || 'SIGNED IN'} · ${sessionTier.toUpperCase()}`}
              color={sessionTierColor}
              dot={Boolean(session.identity)}
              title={`KEY ${session.fingerprint || 'unknown'}${session.name ? ` · ${session.name}` : ''}${session.identity ? ' · unlocked in this page' : ' · session restored (key locked)'}`}
            />
            <GBtn title="End this session" onclick={signOut}>SIGN OUT</GBtn>
          {:else}
            <GBtn title="Sign in with your wallet key" variant="primary" onclick={() => (signInOpen = true)}>SIGN IN</GBtn>
          {/if}
        </span>
      {/snippet}
    </ConsoleHeader>

    <div class="body">
      <!-- PERMISSIONS reads the node's own APIs, not the status feed, so it
           renders before the feed-connection gate. -->
      {#if route === 'permissions'}
        <Permissions {session} {nodes} onRequestSignIn={() => (signInOpen = true)} />
      {:else if !connected}
        <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
          <span class="glyph" style="color:{theme.cyan};">◍</span>
          Connecting to the node status feed (/ws/status)…
        </div>
      {:else if route === 'self'}
        {#if selfNode}
          <div class="self-page">
            <Panel variant="raised" pad="0" style="max-width:880px;">
              <div class="self-head" style="border-color:{theme.divider};">
                <div class="self-titles">
                  <div class="self-dn" style="color:{theme.textBright};">{selfTitle}</div>
                  {#if selfNode.org?.trim() && selfNode.org.trim() !== selfTitle}
                    <div class="self-org" style="color:{theme.textDim};">{selfNode.org}</div>
                  {/if}
                </div>
                <div class="self-chips">
                  <StatusChip label="SELF" color={theme.cyan} dot={false} />
                  <StatusChip label={selfNode.online ? 'ONLINE' : 'OFFLINE'} color={selfNode.online ? theme.green : theme.textMuted} />
                  {#if canEdit && !editing}
                    <GBtn title="Edit this node's published identity" variant="primary" onclick={() => (editing = true)}>EDIT</GBtn>
                  {/if}
                </div>
              </div>
              <div class="self-body">
                {#if editing}
                  <NodeEditForm
                    onCancel={() => (editing = false)}
                    onSaved={({ json, vcard }) => {
                      epmOverride = { vcard, dn: typeof json?.dn === 'string' ? json.dn : undefined };
                      editing = false;
                    }}
                  />
                {:else}
                  <NodeDetail node={selfNode} {now} />
                {/if}
              </div>
            </Panel>
          </div>
        {:else}
          <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
            <span class="glyph" style="color:{theme.cyan};">◉</span>
            Waiting for this node's status entry…
          </div>
        {/if}
      {:else}
        <div class="toolbar">
          <div class="search" style="border-color:{theme.hairline};">
            <span class="sglyph" style="color:{theme.textMuted};">⌕</span>
            <input
              type="search"
              placeholder="SEARCH NODES — name, org, vCard, place, trust…"
              bind:value={query}
              style="color:{theme.textBright};"
              aria-label="Search nodes"
            />
            <span class="mode" style="color:{searchModeColor};border-color:{searchModeColor};" title="Active search mode">{searchModeLabel}</span>
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
            >⚙ SETTINGS</button>
            {#if settingsOpen}
              <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
              <div class="settings-backdrop" onclick={() => (settingsOpen = false)}></div>
              <div class="settings-menu" style="background:{theme.panelRaised};border-color:{theme.panelBorder};">
                <div class="settings-title" style="color:{theme.textMuted};border-color:{theme.divider};">DISPLAY SETTINGS</div>
                <label class="ctl check" style="color:{theme.textBody};" title="When checked, offline nodes are hidden unless an explicit trust tier has been set for them">
                  <input
                    type="checkbox"
                    checked={hideUntrustedOffline}
                    onchange={(e) => setHidePref(e.currentTarget.checked)}
                  />
                  HIDE UNTRUSTED OFFLINE
                </label>
                <div class="settings-hint" style="color:{theme.textFaint};">
                  Offline nodes stay visible when an explicit trust tier is set (including NEVER).
                </div>
              </div>
            {/if}
          </div>
        </div>

        <div class="meta" style="color:{theme.textMuted};">
          <span>{rows.length}/{nodes.length} NODE{nodes.length === 1 ? '' : 'S'}</span>
          <span class="dot">·</span>
          <span>SOURCE {shortId(view?.sourcePeerId ?? '')}</span>
          <span class="dot">·</span>
          <span>UPDATED {updatedAgo === 0 ? 'now' : `${updatedAgo}s ago`}</span>
        </div>

        <div class="stack">
          <Panel variant="raised" pad="0" style="flex:1 1 50%;min-height:0;display:flex;flex-direction:column;">
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
              <div class="pager" style="border-color:{theme.divider};color:{theme.textMuted};">
                <span>ROWS {rows.length ? safePage * PAGE_SIZE + 1 : 0}–{Math.min((safePage + 1) * PAGE_SIZE, rows.length)} OF {rows.length}</span>
                <span class="pager-ctl">
                  <button style="color:{theme.ice};border-color:{theme.hairline};" disabled={safePage === 0} onclick={() => (page = safePage - 1)}>‹ PREV</button>
                  <span>PAGE {safePage + 1}/{pageCount}</span>
                  <button style="color:{theme.ice};border-color:{theme.hairline};" disabled={safePage >= pageCount - 1} onclick={() => (page = safePage + 1)}>NEXT ›</button>
                </span>
              </div>
            </div>
          </Panel>
          <Panel variant="raised" pad="0" style="flex:1 1 50%;min-height:0;display:flex;flex-direction:column;">
            <div class="globe-panel">
              <div class="k" style="color:{theme.textMuted};border-color:{theme.divider};">NODE LOCATIONS <span style="color:{theme.textFaint};">· GEOLITE2 MMDB</span></div>
              <div class="globe-body">
                <Globe nodes={rows.map((r) => r.node)} selectedId={selected?.peerId ?? ''} onSelect={(n) => (selected = n)} />
              </div>
            </div>
          </Panel>
        </div>
      {/if}
    </div>
  </main>

  {#if selected}
    <NodeModal node={selected} {now} onClose={() => (selected = null)} />
  {/if}

  {#if signInOpen}
    <SignInModal {adminConfigured} onClose={() => (signInOpen = false)} onSignedIn={onSignedIn} />
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
  main {
    position: absolute;
    left: 66px;
    right: 0;
    top: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    padding: 16px 24px 24px;
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
  .sglyph { font-size: 18px; }
  .search input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 15px;
    letter-spacing: 0.06em;
    min-width: 0;
  }
  .search input::placeholder { color: rgba(159, 212, 245, 0.35); }
  .mode {
    font-size: 11px;
    letter-spacing: 0.16em;
    border: 1px solid;
    padding: 2px 7px;
    white-space: nowrap;
  }
  .ctl {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    letter-spacing: 0.16em;
    white-space: nowrap;
  }
  .ctl select {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 13px;
    letter-spacing: 0.1em;
    padding: 5px 8px;
    outline: none;
  }
  .ctl select option { background: #0a141b; }
  .ctl.check { cursor: pointer; user-select: none; }
  .ctl.check input {
    appearance: none;
    width: 13px;
    height: 13px;
    border: 1px solid rgba(110, 170, 190, 0.5);
    background: transparent;
    cursor: pointer;
    display: inline-grid;
    place-content: center;
    margin: 0;
  }
  .ctl.check input::before {
    content: '';
    width: 7px;
    height: 7px;
    transform: scale(0);
    background: #35c9d8;
  }
  .ctl.check input:checked::before { transform: scale(1); }
  .meta {
    display: flex;
    align-items: center;
    gap: 9px;
    font-size: 14.5px;
    letter-spacing: 0.14em;
    margin-bottom: 12px;
  }
  .meta .dot { opacity: 0.5; }
  .stack {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .globe-panel { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .globe-panel .k {
    font-size: 12.5px;
    letter-spacing: 0.18em;
    padding: 11px 14px 9px;
    border-bottom: 1px solid;
  }
  .globe-body { flex: 1; min-height: 0; }
  .table-panel { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .pager {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    border-top: 1px solid;
    padding: 9px 14px;
    font-size: 12.5px;
    letter-spacing: 0.14em;
  }
  .pager-ctl { display: inline-flex; align-items: center; gap: 10px; }
  .pager button {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12.5px;
    letter-spacing: 0.12em;
    padding: 4px 10px;
  }
  .pager button:disabled { opacity: 0.35; cursor: default; }
  .settings-wrap { position: relative; }
  .settings-backdrop { position: fixed; inset: 0; z-index: 29; }
  .settings-btn {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12.5px;
    letter-spacing: 0.16em;
    padding: 7px 12px;
    white-space: nowrap;
  }
  .settings-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 30;
    border: 1px solid;
    min-width: 300px;
    padding: 12px 14px 13px;
    box-shadow: 0 14px 44px rgba(0, 0, 0, 0.5);
  }
  .settings-title {
    font-size: 11.5px;
    letter-spacing: 0.18em;
    border-bottom: 1px solid;
    padding-bottom: 7px;
    margin-bottom: 10px;
  }
  .settings-hint {
    font-size: 12.5px;
    letter-spacing: 0.04em;
    line-height: 1.5;
    margin-top: 8px;
  }
  .self-page {
    flex: 1;
    min-height: 0;
    overflow: auto;
    display: flex;
    justify-content: center;
    padding-bottom: 8px;
  }
  .self-page :global(> section) { width: 100%; height: fit-content; }
  .self-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    padding: 16px 18px 13px;
    border-bottom: 1px solid;
  }
  .self-titles { min-width: 0; }
  .self-dn {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 24px;
    letter-spacing: 0.04em;
    overflow-wrap: anywhere;
  }
  .self-org { font-size: 15px; letter-spacing: 0.04em; margin-top: 3px; }
  .self-chips { display: flex; gap: 6px; flex: none; flex-wrap: wrap; justify-content: flex-end; }
  .self-body { padding: 14px 18px 18px; }
  .empty {
    display: flex;
    align-items: center;
    gap: 12px;
    border: 1px solid;
    padding: 26px 28px;
    font-size: 16.5px;
    letter-spacing: 0.06em;
    max-width: 560px;
  }

  .hdr-status { display: contents; }
  .hdr-session { display: contents; }

  /* Narrow screens (owner report: taps landing on clipped targets): the
     desktop half-and-half no-scroll layout collapses tap targets into
     140px strips. On phones the page scrolls naturally instead: panels
     take their content height, the table shows its 5 rows fully, the
     globe gets a fixed workable height. Desktop is untouched. */
  @media (max-width: 760px) {
    .body {
      overflow-y: auto;
      -webkit-overflow-scrolling: touch;
      padding: 12px 12px 24px;
    }
    .stack {
      flex: none;
      min-height: auto;
    }
    .table-panel {
      flex: none;
      min-height: auto;
      max-height: none;
    }
    .globe-panel {
      flex: none;
      height: 340px;
      min-height: 340px;
    }
    .self-page {
      flex: none;
      min-height: auto;
      overflow: visible;
    }
    .toolbar {
      gap: 10px;
    }
    .search {
      flex-basis: 100%;
      max-width: none;
    }
    /* The header's right side can't fit the status chips AND the session
       control on a phone — the actionable control wins; the same counts
       live in the meta line anyway. */
    .hdr-status { display: none; }
    .hdr-session {
      display: inline-flex;
      gap: 6px;
      align-items: center;
      padding-right: 8px;
    }
  }
  .empty .glyph { font-size: 21px; }
</style>
