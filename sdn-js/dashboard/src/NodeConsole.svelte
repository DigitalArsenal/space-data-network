<script>
  /**
   * The NODE route — the SDN Console template's dashboard, waves 1 + 2.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   layout :113 · widgets :122-241 · registry :862-873 · edit chrome :105-117
   *   · ADD WIDGET :250-258 · mutators :883-889 · setInteractive :1020,1049
   *
   * IRIS ruling 2026-07-30 §1: "No import, no conversion, no hand-edit." The
   * export STAYS in SpaceAware-UI; these widgets are IMPLEMENTED here from the
   * design system's own primitives (Panel / StatusChip / GBtn + theme.js) on the
   * ladder in scale.css. Not one hex literal from the export is reproduced —
   * every colour is the nearest theme token, every face is a rung.
   *
   * WAVE 2 IS EDIT LAYOUT + THE THREE ADD-ONLY WIDGETS (IRIS §2/§3), shipped
   * together because "the ADD menu is empty without them". The eight-widget
   * registry, the span vocabulary and the localStorage key are the template's
   * own (node-layout.js); the mutators are pure functions there, so the rules are
   * tested rather than demonstrated. EDIT LAYOUT is ADMIN-ONLY — not on
   * permissions grounds but on coherence: renderLayout() computes the public
   * layout for an anonymous viewer and ignores what is stored, so an anonymous
   * edit would persist into a view the renderer discards (IRIS R3).
   *
   * EVERY FIELD IS A REAL MEASUREMENT. The export's fixtures
   * (12D3KooWDesignerLocalNode, bafkreidesigner…, v0.47.0, "4.8 GB / 32 GB",
   * "MaxMind GeoLite2", the five ACTIVITY rows, "6 STANDARDS SYNCED") are design
   * placeholders and NONE of them ships. Where the node has no honest answer the
   * cell is ABSENT, never dashed and never guessed:
   *
   *   · AUTOSTART — now a MEASUREMENT, from the supervisor probe
   *     (GET /api/node/service, systemd's own UnitFileState). Wave 1 rendered a
   *     hardcoded "ENABLED" behind a flag that was structurally always false;
   *     that literal is GONE (IRIS R8, hermes-dash4's blocking condition). With
   *     no supervisor proven, the cell is absent.
   *   · RESTART / STOP — the owner authorized them 2026-07-30 and the Seal
   *     Council ruled the auth; they ship in the NEXT cutover with a fresh
   *     wallet signature over a single-use nonce. They are rendered ONLY from
   *     `runtime.canRestart` / `canStop`, which are fail-closed on the host, and
   *     never rendered disabled: "three greyed buttons advertise a capability
   *     the node lacks and invite the owner to click them" (IRIS §5).
   *   · CSV — no single-EPM CSV serializer exists anywhere. Dropped (IRIS §6).
   *
   * THE QR IS A MODAL, NOT A CARD TAKEOVER (owner directive 2026-07-30: "the qr
   * code should be in a modal when clicked as it's unreadable this way"). The
   * inline canvas is deleted: it lived in a flex column that shrank it on one
   * axis while a percentage clamp held the other, so it rendered wide, squat and
   * unscannable. IRIS ruled the fix is not a new modal — NodeDetail's QR view
   * already exists, is already square and is already fed by the server-canonical
   * /identity/<peerId>.qr.vcf — so the button opens THAT (R5).
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import PeerMap from './PeerMap.svelte';
  import Pager from './Pager.svelte';
  import {
    WIDGETS,
    addWidget,
    availableWidgets,
    cycleSpan,
    moveWidget,
    readLayout,
    removeWidget,
    renderLayout,
    resetLayout,
    writeLayout,
  } from './node-layout.js';
  import {
    ACTIVITY_ROWS,
    activityRow,
    formatBytes,
    formatRateMBs,
    formatStartedUTC,
    formatUptimeClock,
    sparkBars,
    sparkSpanS,
    storageFraction,
  } from './runtime.js';
  import { parseVCard, extractIdentity } from './vcard.js';
  import { normalizeTrust, hasTrustAssertion, trustRank, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatLastSeen } from './format.js';
  import { apiFetch } from './api.js';

  /**
   * @type {{
   *   node: any, nodes?: any[], runtime: any, now?: number, canEdit?: boolean,
   *   onSelectNode?: (node: any) => void, onEdit?: () => void,
   *   onShowQr?: () => void, onShowDetail?: () => void,
   * }}
   */
  let {
    node,
    nodes = [],
    runtime,
    now = 0,
    canEdit = false,
    onSelectNode = () => {},
    onEdit = () => {},
    onShowQr = () => {},
    onShowDetail = () => {},
  } = $props();

  // ---- LAYOUT + EDIT LAYOUT ------------------------------------------------
  const privileged = $derived(Boolean(runtime?.privileged));
  /** The operator's saved arrangement. Read once — localStorage is not reactive. */
  let stored = $state(readLayout(globalThis.localStorage));
  let editMode = $state(false);
  const layout = $derived(renderLayout(privileged, stored));
  const available = $derived(availableWidgets(layout));
  /** Signing out ends the edit session with the view it was editing. */
  $effect(() => {
    if (!privileged && editMode) editMode = false;
  });

  /** Persist and re-render in one step, exactly as the design's setLayout does. */
  function commit(next) {
    stored = next;
    writeLayout(globalThis.localStorage, next);
  }

  /** The widget currently being dragged (`_dragId` in the export). */
  let dragId = $state('');
  const onDragEnter = (id) => {
    if (!editMode || !dragId) return;
    commit(moveWidget(layout, dragId, id));
  };

  // ---- IDENTITY -----------------------------------------------------------
  const props = $derived(parseVCard(node?.vcard));
  const identity = $derived(extractIdentity(props));
  const tier = $derived(normalizeTrust(node?.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  // The template's literal "CONFIRMED" badge is replaced by the node's ACTUAL
  // trust assertion, rendered through StatusChip. A node that has asserted
  // nothing gets no badge — a green CONFIRMED chip that means nothing is worse
  // than no chip (IRIS §6).
  const trustAsserted = $derived(hasTrustAssertion(node?.trustLevel));
  const displayName = $derived(
    (node?.dn?.trim() || props.find((p) => p.name === 'FN')?.value?.trim() || node?.org?.trim() || '')
  );
  /**
   * The design's vCARD row shows the ORGANIZATION the card publishes. It is
   * suppressed when it merely repeats the headline name above it — a row whose
   * value is the line before it carries no information (IRIS note).
   */
  const vcardOrg = $derived.by(() => {
    const org =
      props.find((p) => p.name === 'ORG')?.value?.split(';')[0]?.trim() ||
      props.find((p) => p.name === 'FN')?.value?.trim() ||
      '';
    return org && org !== displayName ? org : '';
  });
  /**
   * The template's sub-line is "Entity Profile Metadata · self-issued". The first
   * half is what the record IS. "self-issued" is a PROVENANCE claim, so it is
   * appended only when the card actually carries this node's own EPM signature
   * chain (epmsig/epmts aliases) — for THIS node, subject and signer are the same
   * key, which is exactly what self-issued means. An unsigned card says only
   * "Entity Profile Metadata" (IRIS §6).
   */
  const epmSelfIssued = $derived(Boolean(node?.isSelf && identity.epmSignature && identity.epmSignedAt));
  const epmLine = $derived(`Entity Profile Metadata${epmSelfIssued ? ' · self-issued' : ''}`);

  function download(name, blob) {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  }
  const fileStem = $derived((node?.peerId || 'node').slice(0, 12));
  const downloadVcard = () =>
    download(`${fileStem}.vcf`, new Blob([node?.vcard ?? ''], { type: 'text/vcard' }));
  async function downloadJson() {
    // /api/node/epm/json is on the anonymous read surface, so this export works
    // without a session — the same bytes any verifier would fetch.
    const json = await apiFetch('/api/node/epm/json').catch(() => null);
    if (!json) return;
    download(`${fileStem}.epm.json`, new Blob([JSON.stringify(json, null, 2)], { type: 'application/json' }));
  }

  // ---- NODE HEALTH --------------------------------------------------------
  const online = $derived(Boolean(node?.online));
  const storageUsed = $derived(formatBytes(runtime?.storeBytes));
  const storageCapacity = $derived(formatBytes(runtime?.diskCapacityBytes));
  const storageFrac = $derived(storageFraction(runtime?.storeBytes, runtime?.diskCapacityBytes));

  /**
   * API is the origin this console is talking to — a fact about this page, not a
   * guess about the host's config. GATEWAY is only claimed once the node's own
   * /ipfs/ mount answers for the empty-block CID (bafkqaaa is identity-inlined,
   * so the probe costs no network fetch). A node without that mount shows no
   * GATEWAY row rather than a plausible-looking URL.
   */
  let apiOrigin = $state('');
  let gatewayBase = $state('');
  $effect(() => {
    apiOrigin = globalThis.location?.origin ?? '';
    if (gatewayBase) return;
    fetch('/ipfs/bafkqaaa', { method: 'HEAD' })
      .then((res) => {
        if (res.ok) gatewayBase = `${globalThis.location?.origin ?? ''}/ipfs`;
      })
      .catch(() => {});
  });

  // ---- PEER SUMMARY --------------------------------------------------------
  const peers = $derived((nodes ?? []).filter((n) => !n.isSelf));
  const onlinePeers = $derived(peers.filter((n) => n.online).length);
  /**
   * The design's PEER SUMMARY lists three peers with a name, a right-aligned
   * detail and a trust label (`:207-219`). Its detail column is a "feeds" count —
   * a provider-catalogue concept this node does not publish — so the honest
   * equivalent is the peer's own liveness: ONLINE, or when it was last seen.
   * Ordered most-trusted first, then online, so the rows that matter are the ones
   * on screen.
   */
  const peerRows = $derived(
    [...peers]
      .sort(
        (a, b) =>
          trustRank(b.trustLevel) - trustRank(a.trustLevel) ||
          Number(b.online) - Number(a.online) ||
          (b.lastSeen ?? 0) - (a.lastSeen ?? 0)
      )
      .slice(0, 4)
      .map((n) => ({
        peerId: n.peerId,
        name: n.dn?.trim() || n.org?.trim() || shortId(n.peerId),
        tier: normalizeTrust(n.trustLevel),
        color: theme[TRUST_COLOR_TOKEN[normalizeTrust(n.trustLevel)]] ?? theme.textMuted,
        detail: n.online ? 'ONLINE' : formatLastSeen(n.lastSeen, now || Date.now()),
        node: n,
      }))
  );

  // ---- NETWORK THROUGHPUT --------------------------------------------------
  const bars = $derived(sparkBars(runtime?.history));
  const spanS = $derived(sparkSpanS(bars.length));
  const rateIn = $derived(formatRateMBs(runtime?.rateInBps));
  const rateOut = $derived(formatRateMBs(runtime?.rateOutBps));

  // ---- STORAGE · FLATSQL ---------------------------------------------------
  /**
   * IRIS §3: renderable from `store.total_bytes` + `disk.capacity_bytes`, `disk`
   * nullable and NEVER a fabricated 0. The design's hero splits the number from
   * its unit ("4.8" / "/ 32 GB"); `formatBytes` returns them joined, so the split
   * happens here rather than by re-implementing the formatter.
   *
   * The export's two footer lines ("6 STANDARDS SYNCED · FRESH", "SCHEMA v1.0.3 ·
   * synced") are fixtures about a standards-sync surface this node does not
   * publish, so they are replaced by the two facts the same snapshot really
   * carries: how many records are in the store, and how much room is left.
   */
  const storageNumber = $derived(storageUsed.split(' ')[0] ?? '');
  const storageUnit = $derived(storageUsed.split(' ')[1] ?? '');
  const storageAvailable = $derived(formatBytes(runtime?.diskAvailableBytes));
  const storageRecords = $derived(
    typeof runtime?.storeRecords === 'number' ? runtime.storeRecords.toLocaleString('en-US') : ''
  );

  // ---- ACTIVITY LOG --------------------------------------------------------
  /**
   * The ring, NEWEST FIRST, paginated — never scrolled (grammar law L4,
   * iris-dashboard-grammar-law). `activityRead` distinguishes "not read yet" from
   * "read and genuinely empty": an in-memory ring starts empty at boot, and
   * saying so is different from saying nothing (IRIS R4).
   */
  let activityPage = $state(0);
  const activityRows = $derived((runtime?.activity ?? []).map((ev) => activityRow(ev, shortId)));
  const activityPages = $derived(Math.max(1, Math.ceil(activityRows.length / ACTIVITY_ROWS)));
  const activitySafePage = $derived(Math.min(activityPage, activityPages - 1));
  const activityVisible = $derived(
    activityRows.slice(activitySafePage * ACTIVITY_ROWS, (activitySafePage + 1) * ACTIVITY_ROWS)
  );

  // ---- SERVICE -------------------------------------------------------------
  const serviceState = $derived((runtime?.serviceState || '').toUpperCase());
  const versionLine = $derived(
    [runtime?.suiteVersion ? `v${runtime.suiteVersion}` : '', runtime?.standardsVersion ? `SDS ${runtime.standardsVersion}` : '']
      .filter(Boolean)
      .join(' · ')
  );
  const uptimeClock = $derived(
    typeof runtime?.uptimeS === 'number' ? formatUptimeClock(runtime.uptimeS) : ''
  );
  /** started_at on the UTC clock, carrying the date when it is not today (C3). */
  const startedClock = $derived(formatStartedUTC(runtime?.startedAt, now || Date.now()));
  /**
   * systemd's own UnitFileState word, upper-cased for the cell. Colour states
   * what it MEANS: enabled is good, disabled/masked is the operator's choice to
   * see, and static/indirect are neither (so they take the neutral tone rather
   * than being coloured as if they were).
   */
  const autostart = $derived((runtime?.autostart || '').trim());
  const autostartColor = $derived(
    autostart === 'enabled' || autostart === 'enabled-runtime'
      ? theme.green
      : autostart === 'disabled' || autostart === 'masked'
        ? theme.amber
        : theme.textBody
  );
</script>

<div class="dash">
  <!-- The template's page kicker and its EDIT LAYOUT control (`:102-110`). The
       control exists only for an Admin (IRIS R3); an anonymous viewer sees the
       kicker alone, exactly as wave 1 shipped. -->
  <div class="dashhead">
    <span class="kick" style="color:{theme.textMuted};">DASHBOARD</span>
    <span class="spacer"></span>
    <!-- No instruction caption. Every affordance it narrated is VISIBLE in edit
         mode: the ⤢ and ✕ handles are drawn on each widget and the widgets drag.
         Owner directive 2026-07-30 (twice) — a control strip is its controls. -->
    {#if privileged && editMode}
      <GBtn title="Restore the default arrangement" onclick={() => commit(resetLayout())}>RESET</GBtn>
    {/if}
    {#if privileged}
      <GBtn
        title={editMode ? 'Finish editing the layout' : 'Rearrange, resize, add or remove widgets'}
        variant={editMode ? 'primary' : 'neutral'}
        onclick={() => (editMode = !editMode)}
      >{editMode ? 'DONE' : 'EDIT LAYOUT'}</GBtn>
    {/if}
  </div>

  <div class="grid">
    {#each layout as w (w.id)}
      <Panel
        variant="raised"
        style="position:relative;grid-column:span {w.span};min-width:0;{editMode
          ? `border:1px dashed ${theme.ice};cursor:move;`
          : ''}"
      >
        <!-- Every widget is a full-height flex column so its terminal block can
             compose to the PANEL FLOOR (IRIS condition C4). Grid rows are as tall
             as their tallest member, and without this the shorter widgets in a
             row were top-packed with a 90-180px void underneath, which the
             export never shows. -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="w"
          draggable={editMode}
          ondragstart={() => (dragId = w.id)}
          ondragenter={() => onDragEnter(w.id)}
          ondragover={(e) => editMode && e.preventDefault()}
          ondragend={() => (dragId = '')}
        >
        {#if editMode}
          <!-- The export's per-widget edit chrome (`:117-122`): a drag grip, a
               span cycle showing the span it will take, and a remove. -->
          <div class="editbar" style="background:{theme.panelRaised};border-color:{theme.panelBorder};">
            <span class="grip" style="color:{theme.textMuted};" title="Drag to reorder">⠿</span>
            <button
              class="ebtn"
              style="color:{theme.ice};border-color:{theme.ice};"
              title="Resize"
              onclick={() => commit(cycleSpan(layout, w.id))}
            >⤢ W{w.span}</button>
            <button
              class="ebtn del"
              style="color:{theme.red};border-color:{theme.red};"
              title="Remove"
              onclick={() => commit(removeWidget(layout, w.id))}
            >✕</button>
          </div>
        {/if}

        {#if w.id === 'health'}
          <!-- NODE HEALTH ------------------------------------------------- -->
          <span class="tick tl" style="border-color:{theme.ice};"></span>
          <span class="tick tr" style="border-color:{theme.ice};"></span>
          <span class="tick bl" style="border-color:{theme.ice};"></span>
          <span class="tick br" style="border-color:{theme.ice};"></span>
          <div class="wkick" style="color:{theme.textMuted};">NODE HEALTH</div>
          <div class="hero">
            <span class="dot" style="background:{online ? theme.green : theme.textMuted};box-shadow:0 0 9px {online ? theme.green : theme.textMuted};"></span>
            <span class="heroval" style="color:{theme.textBright};">{online ? 'ONLINE' : 'OFFLINE'}</span>
          </div>
          {#if runtime?.mode}
            <div class="sub" style="color:{theme.textDim};">MODE · {runtime.mode.toUpperCase()}</div>
          {/if}
          <div class="cells fill">
            <div class="cell">
              <div class="clabel" style="color:{theme.textMuted};">PEER ID</div>
              <div class="cval mono break" style="color:{theme.textBody};">{node?.peerId || ''}</div>
            </div>
            <div class="crow">
              {#if apiOrigin}
                <div class="cell">
                  <div class="clabel" style="color:{theme.textMuted};">API</div>
                  <div class="cval mono break" style="color:{theme.textBody};">{apiOrigin}</div>
                </div>
              {/if}
              {#if gatewayBase}
                <div class="cell">
                  <div class="clabel" style="color:{theme.textMuted};">GATEWAY</div>
                  <div class="cval mono break" style="color:{theme.textBody};">{gatewayBase}</div>
                </div>
              {/if}
            </div>
            {#if privileged && storageUsed}
              <!-- STORAGE renders ONLY from the admin snapshot (IRIS condition
                   C1). /api/v1/stats looks like the anonymous source for this,
                   but it seeds `total_bytes: 0` and serves that on a store-read
                   budget miss — so it can report an EMPTY store for a busy one.
                   A measurement or nothing. -->
              <div class="cell">
                <div class="sline">
                  <span class="clabel" style="color:{theme.textMuted};">STORAGE</span>
                  <span class="cval num" style="color:{theme.textBright};">
                    <!-- &nbsp; because Svelte trims the leading space out of the
                         span and the design reads "4.8 GB / 32 GB", not "4.8 GB/ 32 GB". -->
                    {storageUsed}{#if storageCapacity}<span style="color:{theme.textMuted};">&nbsp;/ {storageCapacity}</span>{/if}
                  </span>
                </div>
                {#if storageFrac !== null}
                  <!-- The bar renders ONLY with a real capacity. A null `disk`
                       (statfs unavailable) must never become a zero-capacity
                       bar — the node reports null so this does not happen. -->
                  <div class="bar" style="background:{theme.divider};">
                    <div class="fill" style="width:{(storageFrac * 100).toFixed(1)}%;background:linear-gradient(90deg,{theme.cyan},{theme.ice});"></div>
                  </div>
                {/if}
              </div>
            {/if}
          </div>

        {:else if w.id === 'identity'}
          <!-- IDENTITY ---------------------------------------------------- -->
          <div class="whead">
            <span class="wkick" style="color:{theme.textMuted};">IDENTITY</span>
            <span class="hchips">
              {#if trustAsserted}
                <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
              {/if}
              {#if canEdit}
                <GBtn title="Edit this node's published identity" variant="primary" onclick={onEdit}>EDIT</GBtn>
              {/if}
            </span>
          </div>
          {#if displayName}
            <div class="idname" style="color:{theme.textBright};">{displayName}</div>
          {/if}
          <div class="sub" style="color:{theme.textDim};">{epmLine}</div>
          <div class="cells fill">
            {#if identity.epmCid}
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">EPM CID</div>
                <div class="cval mono break small" style="color:{theme.textBody};">{identity.epmCid}</div>
              </div>
            {/if}
            {#if vcardOrg}
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">vCARD</div>
                <div class="cval" style="color:{theme.textBody};">{vcardOrg}</div>
              </div>
            {/if}
          </div>
          <!-- The export's terminal button row. DETAIL replaces the dead THIS
               NODE route (owner directive 2026-07-30: "the 'this node' menu
               should go away") — the self node is a node, so it opens the SAME
               detail modal every other peer gets, which already carries the
               verification keys, chain addresses and EPM provenance that page
               used to hold (IRIS R6). QR opens that modal on its QR tab. -->
          <div class="btnrow">
            <GBtn title="Every published field, key and address for this node" style="flex:1;" onclick={onShowDetail}>DETAIL</GBtn>
            <GBtn title="Download this node's EPM as JSON" style="flex:1;" onclick={downloadJson}>JSON</GBtn>
            <GBtn title="Download this node's published vCard" style="flex:1;" onclick={downloadVcard}>vCARD</GBtn>
            <GBtn title="Show the compact contact card as a scannable QR code" style="flex:1;" onclick={onShowQr}>QR</GBtn>
          </div>

        {:else if w.id === 'service'}
          <!-- SERVICE ----------------------------------------------------- -->
          <div class="wkick" style="color:{theme.textMuted};">SERVICE</div>
          <div class="hero">
            <span class="dot" style="background:{theme.green};box-shadow:0 0 9px {theme.green};"></span>
            <!-- The daemon can only honestly claim "running" while it is up and
                 answering this request — which is the only way this renders. -->
            <span class="svcval" style="color:{theme.textBright};">{serviceState || 'RUNNING'}</span>
          </div>
          {#if versionLine}
            <div class="sub" style="color:{theme.textDim};">{versionLine}</div>
          {/if}
          <div class="crow foot">
            {#if uptimeClock}
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">UPTIME</div>
                <div class="cval num" style="color:{theme.textBody};">{uptimeClock}</div>
              </div>
            {/if}
            {#if startedClock}
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">STARTED</div>
                <div class="cval num" style="color:{theme.textBody};">{startedClock}</div>
              </div>
            {/if}
            {#if autostart}
              <!-- A MEASUREMENT now: systemd's UnitFileState, read through
                   GET /api/node/service. Absent when no supervisor is proven —
                   which is every host that is not systemd, and is the honest
                   answer there. -->
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">AUTOSTART</div>
                <div class="cval" style="color:{autostartColor};">{autostart.toUpperCase()}</div>
              </div>
            {/if}
          </div>

        {:else if w.id === 'netmap'}
          <!-- PEER MAP · GEOIP — the extracted component, shared with the PEERS
               route so only ONE globe is ever mounted (IRIS R7 + the law's
               companion trap). `interactive` is the export's
               setInteractive(!editMode) (`:1020,1049`): without it, dragging this
               widget to reorder it just spins the globe. -->
          <span class="tick tl" style="border-color:{theme.cyan};"></span>
          <span class="tick tr" style="border-color:{theme.cyan};"></span>
          <span class="tick bl" style="border-color:{theme.cyan};"></span>
          <span class="tick br" style="border-color:{theme.cyan};"></span>
          <PeerMap {nodes} onSelectNode={onSelectNode} interactive={!editMode} />

        {:else if w.id === 'throughput'}
          <!-- NETWORK THROUGHPUT ------------------------------------------ -->
          <div class="wkick" style="color:{theme.textMuted};">NETWORK THROUGHPUT</div>
          {#if rateIn !== '' || rateOut !== ''}
            <div class="thead">
              <span class="thnum mono" style="color:{theme.textBright};">{rateIn || '0.00'}</span>
              <span class="thunit" style="color:{theme.textDim};">MB/s ↓</span>
              <span class="thnum2 mono" style="color:{theme.ice};">{rateOut || '0.00'}</span>
              <span class="thunit" style="color:{theme.textDim};">↑</span>
            </div>
          {/if}
          <div class="spark">
            {#each bars as bar, i (i)}
              <!-- The newest bar is the highlighted one. The export highlights
                   index 8, which is fixture styling and means nothing on a live
                   ring (IRIS constraint (d)). -->
              <span
                class="sbar"
                style="height:{bar.pct}%;background:linear-gradient(180deg,{bar.newest ? theme.ice : theme.cyan},transparent);"
              ></span>
            {/each}
          </div>
          <div class="saxis" style="color:{theme.textMuted};">
            <!-- The axis states the span the bars ACTUALLY cover: a node up for
                 twenty seconds has four samples, and labelling that −60s would
                 be a lie about the window. -->
            <span>{spanS ? `−${spanS}s` : ''}</span>
            <span>NOW</span>
          </div>

        {:else if w.id === 'peersum'}
          <!-- PEER SUMMARY (`:207-219`) ----------------------------------- -->
          <div class="whead">
            <span class="wkick" style="color:{theme.textMuted};">PEER SUMMARY</span>
            <span class="hchips">
              <StatusChip label={`${onlinePeers}/${peers.length} ONLINE`} color={theme.green} dot={false} />
            </span>
          </div>
          {#if peerRows.length}
            <div class="cells fill">
              {#each peerRows as row (row.peerId)}
                <button class="prow" onclick={() => onSelectNode(row.node)} title="Open this peer's details">
                  <span class="pdot" style="background:{row.color};box-shadow:0 0 6px {row.color};"></span>
                  <span class="pname" style="color:{theme.textBright};">{row.name}</span>
                  <span class="pdetail" style="color:{theme.textDim};">{row.detail}</span>
                  <span class="ptier" style="color:{row.color};">{row.tier.toUpperCase()}</span>
                </button>
              {/each}
            </div>
          {:else}
            <div class="empty" style="color:{theme.textFaint};">No peers known to this node yet.</div>
          {/if}

        {:else if w.id === 'storage'}
          <!-- STORAGE · FLATSQL (`:221-228`) ------------------------------ -->
          <!-- "· FLATSQL" removed with the rest of the tag clutter (owner
               2026-07-30, twice): which engine backs the bytes is not a fact the
               reader of a STORAGE panel needs. Kept in step with
               node-layout.js's WIDGETS title, which the ADD WIDGET menu uses. -->
          <div class="wkick" style="color:{theme.textMuted};">STORAGE</div>
          {#if storageUsed}
            <div class="thead">
              <span class="storenum mono" style="color:{theme.textBright};">{storageNumber}</span>
              <span class="thunit" style="color:{theme.textDim};">
                {storageUnit}{#if storageCapacity} / {storageCapacity}{/if}
              </span>
            </div>
            {#if storageFrac !== null}
              <div class="bar tall" style="background:{theme.divider};">
                <div class="fill" style="width:{(storageFrac * 100).toFixed(1)}%;background:linear-gradient(90deg,{theme.cyan},{theme.ice});"></div>
              </div>
            {/if}
            <div class="cells fill foot">
              {#if storageRecords}
                <div class="sline">
                  <span class="clabel" style="color:{theme.textMuted};">RECORDS</span>
                  <span class="cval num" style="color:{theme.textBody};">{storageRecords}</span>
                </div>
              {/if}
              {#if storageAvailable}
                <!-- Free space on the store's own filesystem. Absent whenever the
                     statfs probe is (the node reports `disk: null` rather than a
                     fabricated zero), which is the same rule the bar follows. -->
                <div class="sline">
                  <span class="clabel" style="color:{theme.textMuted};">AVAILABLE</span>
                  <span class="cval num" style="color:{theme.textBody};">{storageAvailable}</span>
                </div>
              {/if}
            </div>
          {:else}
            <div class="empty" style="color:{theme.textFaint};">Store totals are not readable on this node.</div>
          {/if}

        {:else if w.id === 'activity'}
          <!-- ACTIVITY LOG (`:231-241`) ----------------------------------- -->
          <div class="whead">
            <span class="wkick" style="color:{theme.textMuted};">ACTIVITY LOG</span>
            {#if activityRows.length}
              <span class="hchips">
                <StatusChip label={`${activityRows.length} EVENTS`} color={theme.ice} dot={false} />
              </span>
            {/if}
          </div>
          {#if activityVisible.length}
            <div class="alist">
              {#each activityVisible as row, i (`${row.clock}:${i}`)}
                <div class="arow">
                  <span class="aclock mono" style="color:{theme.textMuted};">{row.clock}</span>
                  <span class="adot" style="background:{theme[row.token] ?? theme.textDim};"></span>
                  <span class="atext" style="color:{theme.textBody};">{row.text}</span>
                </div>
              {/each}
            </div>
            {#if activityPages > 1}
              <!-- L4: overflow PAGINATES through the one shared pager. -->
              <Pager
                page={activitySafePage}
                pageCount={activityPages}
                total={activityRows.length}
                pageSize={ACTIVITY_ROWS}
                unit="EVENTS"
                compact
                onPage={(p) => (activityPage = p)}
              />
            {/if}
          {:else}
            <div class="empty" style="color:{theme.textFaint};">
              {runtime?.activityRead
                ? 'No activity recorded since this node started.'
                : 'Reading the activity ring…'}
            </div>
          {/if}
        {/if}
        </div>
      </Panel>
    {/each}
  </div>

  {#if privileged && editMode && available.length}
    <!-- ADD WIDGET (`:250-258`): only what is not already placed. -->
    <div class="addtray" style="border-color:{theme.panelBorder};">
      <div class="wkick" style="color:{theme.textMuted};">ADD WIDGET</div>
      <div class="addrow">
        {#each available as w (w.id)}
          <GBtn title={`Add the ${w.title} widget`} onclick={() => commit(addWidget(layout, w.id))}>
            + {w.title}
          </GBtn>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .dash { min-width: 0; }
  .dashhead {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-5);
    margin-bottom: var(--sdn-sp-6);
  }
  .dashhead .spacer { flex: 1; }
  .kick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
  }

  /* The template's grid (`:113`): 12 columns, dense row flow, min-content rows.
     The gap is the ladder's rung nearest the design's 14px under the
     sub-proportional spacing rule (scale.css). */
  .grid {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-auto-rows: min-content;
    grid-auto-flow: row dense;
    gap: var(--sdn-sp-7);
    align-content: start;
    min-width: 0;
  }

  /* The full-height widget column (C4). `height:100%` resolves because a grid
     item is stretched to its row, which is the same arrangement NodeWidgets
     already relies on inside Panel. */
  .w { display: flex; flex-direction: column; height: 100%; min-width: 0; }

  /* EDIT LAYOUT chrome (`:117-122`). Absolutely positioned so turning edit mode
     on cannot change a widget's height — a layout that reflows the moment you
     start editing it is a layout you cannot aim at. */
  .editbar {
    position: absolute;
    top: 6px;
    right: 6px;
    z-index: 6;
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-1);
    border: 1px solid;
    padding: 2px 3px;
  }
  .grip { cursor: move; font-size: var(--sdn-fs-body); line-height: 1; }
  .ebtn {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    /* L6: control chrome never renders below its own label's rung. */
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.04em;
    padding: 1px var(--sdn-sp-2);
  }

  .addtray {
    margin-top: var(--sdn-sp-7);
    border: 1px dashed;
    padding: var(--sdn-sp-5) var(--sdn-sp-7);
  }
  .addrow { display: flex; flex-wrap: wrap; gap: var(--sdn-sp-3); margin-top: var(--sdn-sp-4); }

  /* Corner ticks — the design's accent brackets on the two framed widgets. */
  .tick { position: absolute; width: 9px; height: 9px; }
  .tick.tl { top: -1px; left: -1px; border-top: 1px solid; border-left: 1px solid; }
  .tick.tr { top: -1px; right: -1px; border-top: 1px solid; border-right: 1px solid; }
  .tick.bl { bottom: -1px; left: -1px; border-bottom: 1px solid; border-left: 1px solid; }
  .tick.br { bottom: -1px; right: -1px; border-bottom: 1px solid; border-right: 1px solid; }

  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }
  .whead {
    display: flex;
    align-items: baseline;
    gap: var(--sdn-sp-4);
    flex-wrap: wrap;
    margin-bottom: var(--sdn-sp-5);
  }
  .whead .wkick { margin-bottom: 0; }
  .hchips { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); margin-left: auto; flex-wrap: wrap; }

  .hero { display: flex; align-items: baseline; gap: var(--sdn-sp-3); }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
  /* IRIS §7 — the design's hero faces re-snapped to the nearest rung, relative
     order preserved: ONLINE 31 -> hero, throughput 26.5 -> title, service state
     24 -> head, identity name 21.5 -> lead, storage 29 -> title. Never
     re-multiplied. */
  .heroval {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 700;
    font-size: var(--sdn-fs-hero);
    line-height: var(--sdn-lh-hero);
    letter-spacing: 0.06em;
  }
  .svcval {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 700;
    font-size: var(--sdn-fs-head);
    line-height: var(--sdn-lh-head);
    letter-spacing: 0.05em;
  }
  .idname {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 700;
    font-size: var(--sdn-fs-lead);
    line-height: var(--sdn-lh-lead);
    letter-spacing: 0.04em;
    overflow-wrap: break-word;
  }
  .sub {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.04em;
    margin: var(--sdn-sp-1) 0 var(--sdn-sp-6);
  }

  .cells { display: flex; flex-direction: column; gap: var(--sdn-sp-4); min-width: 0; }
  /* Take the leftover height and SPREAD the cells through it, so the last one
     lands on the panel floor as the export composes it — rather than leaving one
     void beneath a top-packed stack (C4). */
  .cells.fill { flex: 1; justify-content: space-between; }
  .cells.foot { justify-content: flex-end; }
  .crow { display: flex; gap: var(--sdn-sp-8); flex-wrap: wrap; }
  /* SERVICE's terminal row: the export puts its last block at the floor. */
  .crow.foot { margin-top: auto; }
  .crow .cell { flex: 1 1 40%; min-width: 0; }
  .cell { min-width: 0; }
  .clabel {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
  }
  .cval {
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    margin-top: 2px;
  }
  /* Long machine values (peer id, CID) at the denser rung so a 4-column widget
     holds them in a couple of lines instead of a ragged tower. */
  .cval.small { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); }
  .cval.break { overflow-wrap: anywhere; }
  .cval.num { font-variant-numeric: tabular-nums; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  .sline { display: flex; justify-content: space-between; align-items: baseline; gap: var(--sdn-sp-4); }
  .bar { height: 6px; margin-top: var(--sdn-sp-2); }
  .bar.tall { height: 7px; margin-top: var(--sdn-sp-5); }
  .bar .fill { height: 100%; }

  /* IDENTITY's terminal block: the export's export buttons sit on the floor. */
  .btnrow { display: flex; gap: var(--sdn-sp-2); margin-top: var(--sdn-sp-6); flex-wrap: wrap; }

  .empty {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.02em;
    margin-top: var(--sdn-sp-4);
  }

  /* PEER SUMMARY rows: a button, because clicking one opens that peer. */
  .prow {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-3);
    background: transparent;
    border: 0;
    padding: 0;
    text-align: left;
    cursor: pointer;
    font: inherit;
    min-width: 0;
  }
  .prow:hover .pname { filter: brightness(1.2); }
  .prow:focus-visible { outline: 1px solid currentColor; outline-offset: 2px; }
  .pdot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
  /* The design's PEER SUMMARY row is ONE line: name, right-aligned detail, trust
     (`SDN Console.dc.html:210-217`). This max-width is TEXT truncation inside that
     row, not a panel dimension — it caps the name so a long operator name cannot
     push the trust label out of the widget (L5 asks for the reason to be stated
     here, and this is it). */
  .pname {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    flex: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    /* TEXT truncation inside the row, not a panel dimension (L5). */
    max-width: 55%;
  }
  .pdetail {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    flex: 1;
    text-align: right;
    white-space: nowrap;
  }
  .ptier {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.04em;
    flex: none;
    text-align: right;
    min-width: 68px;
  }

  .thead { display: flex; align-items: baseline; gap: var(--sdn-sp-3); flex-wrap: wrap; }
  .thnum {
    font-weight: 600;
    font-size: var(--sdn-fs-title);
    line-height: var(--sdn-lh-title);
    font-variant-numeric: tabular-nums;
  }
  /* The design's storage hero is 29 — the same rung as the page title (27), per
     IRIS §3: "no new rung — re-snap STORAGE 29 -> --sdn-fs-title". */
  .storenum {
    font-weight: 600;
    font-size: var(--sdn-fs-title);
    line-height: var(--sdn-lh-title);
    font-variant-numeric: tabular-nums;
  }
  .thnum2 {
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    font-variant-numeric: tabular-nums;
    margin-left: var(--sdn-sp-1);
  }
  .thunit { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); }
  .spark { display: flex; align-items: flex-end; gap: 2px; margin-top: var(--sdn-sp-6); height: 64px; }
  .sbar { flex: 1; min-width: 0; }
  .saxis {
    display: flex;
    justify-content: space-between;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.08em;
    margin-top: var(--sdn-sp-3);
  }

  /* ACTIVITY LOG rows (`:233-239`). No max-height and no scroller: the pager
     above is how this widget handles more than it can show (L4). */
  .alist { display: flex; flex-direction: column; gap: var(--sdn-sp-3); min-width: 0; }
  .arow { display: flex; align-items: baseline; gap: var(--sdn-sp-4); min-width: 0; }
  .aclock {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    font-variant-numeric: tabular-nums;
    flex: none;
  }
  .adot { width: 6px; height: 6px; border-radius: 50%; flex: none; align-self: center; }
  .atext {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  /* Below the 12-column grid's usable width every widget takes the full row —
     a 4-of-12 panel at 900px is narrower than the peer id it has to print. */
  @media (max-width: 1180px) {
    .grid > :global(*) { grid-column: span 12 !important; }
  }
</style>
