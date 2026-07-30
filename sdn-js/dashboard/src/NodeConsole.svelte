<script>
  /**
   * The NODE route — the SDN Console template's dashboard, wave 1.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   layout :113 · widgets :122-204 · registry :862-873
   *
   * IRIS ruling 2026-07-30 §1: "No import, no conversion, no hand-edit." The
   * export STAYS in SpaceAware-UI; these widgets are IMPLEMENTED here from the
   * design system's own primitives (Panel / StatusChip / Kicker / GBtn +
   * theme.js) on the ladder in scale.css. Not one hex literal from the export is
   * reproduced — every colour is the nearest theme token, every face is a rung.
   *
   * WAVE 1 IS THE TEMPLATE'S DEFAULT VIEW, READ-ONLY (IRIS §2): the five default
   * widgets on the 12-column dense grid, no EDIT LAYOUT, no drag, no ADD menu,
   * nothing persisted. Wave 2 turns editing on over node-layout.js's registry.
   *
   * EVERY FIELD IS A REAL MEASUREMENT. The export's fixtures
   * (12D3KooWDesignerLocalNode, bafkreidesigner…, v0.47.0, "4.8 GB / 32 GB",
   * "MaxMind GeoLite2") are design placeholders and NONE of them ships. Where the
   * node has no honest answer the cell is ABSENT, never dashed and never guessed:
   *
   *   · AUTOSTART — the host reports `autostart_known:false` (it has no surface
   *     into systemd/desktop autostart), so the cell is omitted and comes back by
   *     itself the day that flag turns true (IRIS §4).
   *   · RESTART / STOP / CHECK — no daemon-lifecycle endpoint exists and creating
   *     one is a new host capability, STOPPED FOR THE OWNER. The buttons are not
   *     rendered disabled: "three greyed buttons advertise a capability the node
   *     lacks and invite the owner to click them" (IRIS §5).
   *   · "Locations · MaxMind GeoLite2" — an attribution claim about a resolver
   *     this node does not use. Absent (IRIS constraint (b)).
   *   · CSV — no single-EPM CSV serializer exists anywhere. Dropped (IRIS §6).
   *   · "current" / "headless-capable ✓" — an update-feed comparison and a
   *     capability flag the node does not publish.
   */
  import QRCode from 'qrcode';
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import Globe from './Globe.svelte';
  import { layoutFor } from './node-layout.js';
  import { formatBytes, formatRateMBs, formatStartedUTC, formatUptimeClock, sparkBars, sparkSpanS, storageFraction } from './runtime.js';
  import { parseVCard, extractIdentity, buildCompactVCard } from './vcard.js';
  import { normalizeTrust, hasTrustAssertion, TRUST_COLOR_TOKEN } from './trust.js';
  import { apiFetch } from './api.js';

  /**
   * @type {{
   *   node: any, nodes?: any[], runtime: any, now?: number, canEdit?: boolean,
   *   onSelectNode?: (node: any) => void, onEdit?: () => void
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
  } = $props();

  const layout = $derived(layoutFor(Boolean(runtime?.privileged)));

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

  let qrOpen = $state(false);
  let qrCanvas = $state(null);

  $effect(() => {
    if (!qrOpen || !qrCanvas) return;
    const canvas = qrCanvas;
    (async () => {
      // The node's SERVER-canonical compact card is authoritative; the
      // client-built one is only the offline fallback.
      let card = '';
      try {
        const res = await fetch(`/identity/${node.peerId}.qr.vcf`);
        if (res.ok) card = await res.text();
      } catch {
        /* fall back below */
      }
      if (!card.trim()) card = buildCompactVCard(node, props);
      await QRCode.toCanvas(canvas, card, {
        errorCorrectionLevel: 'M',
        margin: 2,
        width: 260,
        color: { dark: '#04060a', light: '#eaf6f8' },
      });
    })().catch(() => {});
  });

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

  // ---- PEER MAP ------------------------------------------------------------
  let mapMode = $state('3d');
  const peers = $derived((nodes ?? []).filter((n) => !n.isSelf));
  const links = $derived(peers.filter((n) => n.online).length);
  const placed = $derived((nodes ?? []).filter((n) => n.lat !== 0 || n.lon !== 0));
  const countries = $derived(
    new Set(
      placed
        .map((n) => (n.geoLabel || '').split(',').pop()?.trim())
        .filter((c) => Boolean(c))
    ).size
  );

  // ---- NETWORK THROUGHPUT --------------------------------------------------
  const bars = $derived(sparkBars(runtime?.history));
  const spanS = $derived(sparkSpanS(bars.length));
  const rateIn = $derived(formatRateMBs(runtime?.rateInBps));
  const rateOut = $derived(formatRateMBs(runtime?.rateOutBps));

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
</script>

<div class="dash">
  <!-- The template's page kicker (`:102-104`). EDIT LAYOUT belongs beside it and
       arrives in wave 2; wave 1 deliberately shows no control it cannot honour. -->
  <div class="dashhead">
    <span class="kick" style="color:{theme.textMuted};">DASHBOARD</span>
  </div>

  <div class="grid">
    {#each layout as w (w.id)}
      <Panel variant="raised" style="position:relative;grid-column:span {w.span};min-width:0;">
        <!-- Every widget is a full-height flex column so its terminal block can
             compose to the PANEL FLOOR (IRIS condition C4). Grid rows are as tall
             as their tallest member, and without this the shorter widgets in a
             row were top-packed with a 90-180px void underneath, which the
             export never shows. -->
        <div class="w">
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
            {#if runtime?.privileged && storageUsed}
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
          <!-- QR is an ABSOLUTE takeover of the card body, not a replacement in
               the flow (IRIS condition C4): swapping the fields for a 260px
               canvas grew this widget and with it the whole row, opening a
               ~330px void under SERVICE. The card's height is now the same
               whether the QR is open or shut. -->
          <div class="idbody">
            <div class="cells fill" class:hidden={qrOpen}>
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
            {#if qrOpen}
              <div class="qr">
                <canvas bind:this={qrCanvas}></canvas>
                <div class="qrhint" style="color:{theme.textFaint};">
                  Scan to import this node as a contact — the server-canonical compact card.
                </div>
              </div>
            {/if}
          </div>
          <div class="btnrow">
            <GBtn title="Download this node's EPM as JSON" style="flex:1;" onclick={downloadJson}>JSON</GBtn>
            <GBtn title="Download this node's published vCard" style="flex:1;" onclick={downloadVcard}>vCARD</GBtn>
            <GBtn
              title="Show the compact contact card as a QR code"
              variant={qrOpen ? 'primary' : 'neutral'}
              style="flex:1;"
              onclick={() => (qrOpen = !qrOpen)}
            >QR</GBtn>
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
            {#if runtime?.autostartKnown}
              <!-- Unreachable today by design: the host reports
                   autostart_known:false. It is READ, not assumed, so the cell
                   returns of its own accord on a host that gains the surface. -->
              <div class="cell">
                <div class="clabel" style="color:{theme.textMuted};">AUTOSTART</div>
                <div class="cval" style="color:{theme.green};">ENABLED</div>
              </div>
            {/if}
          </div>

        {:else if w.id === 'netmap'}
          <!-- PEER MAP · GEOIP -------------------------------------------- -->
          <span class="tick tl" style="border-color:{theme.cyan};"></span>
          <span class="tick tr" style="border-color:{theme.cyan};"></span>
          <span class="tick bl" style="border-color:{theme.cyan};"></span>
          <span class="tick br" style="border-color:{theme.cyan};"></span>
          <div class="whead">
            <span class="wkick" style="color:{theme.textMuted};">PEER MAP</span>
            <span class="wsub" style="color:{theme.textFaint};">GEOIP · LIVE SWARM</span>
            <span class="hchips">
              <StatusChip label={`${links} LINK${links === 1 ? '' : 'S'}`} color={theme.green} />
              <span class="tabs" style="border-color:{theme.panelBorder};">
                {#each [['3d', '3D'], ['2d', '2D']] as [id, label] (id)}
                  <button
                    class="tab"
                    style="background:{mapMode === id ? 'rgba(53,201,216,0.18)' : 'transparent'};color:{mapMode === id ? theme.cyan : theme.textDim};border-color:{theme.panelBorder};"
                    aria-pressed={mapMode === id}
                    onclick={() => (mapMode = id)}
                  >{label}</button>
                {/each}
              </span>
            </span>
          </div>
          <div class="map">
            <Globe nodes={placed} mode={mapMode} legend={false} selectedId={''} onSelect={onSelectNode} />
            <div class="mapmeta mono" style="color:{theme.textMuted};">
              <div>{peers.length} PEER{peers.length === 1 ? '' : 'S'}</div>
              <div>{countries} {countries === 1 ? 'COUNTRY' : 'COUNTRIES'}</div>
            </div>
            {#if mapMode === '3d'}
              <div class="maphint mono" style="color:{theme.textFaint};">DRAG TO ROTATE</div>
            {/if}
          </div>
          <div class="mapfoot">
            <span class="legend" style="color:{theme.textDim};">
              <span><i style="background:{theme.cyan};box-shadow:0 0 6px {theme.cyan};"></i>THIS NODE</span>
              <span><i style="background:{theme.green};box-shadow:0 0 6px {theme.green};"></i>ONLINE</span>
              <span><i style="background:{theme.amber};box-shadow:0 0 6px {theme.amber};"></i>OFFLINE · TRUSTED</span>
              <span><i style="background:{theme.textMuted};"></i>OFFLINE</span>
            </span>
            <!-- The design's "Locations · MaxMind GeoLite2" attribution is
                 ABSENT: it is a claim about a resolver, and it is only true of a
                 node whose resolver really is GeoLite2 (IRIS constraint (b)). -->
          </div>

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
        {/if}
        </div>
      </Panel>
    {/each}
  </div>
</div>

<style>
  .dash { min-width: 0; }
  .dashhead {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-5);
    margin-bottom: var(--sdn-sp-6);
  }
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
  .wsub {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
  }
  .hchips { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); margin-left: auto; flex-wrap: wrap; }

  .hero { display: flex; align-items: baseline; gap: var(--sdn-sp-3); }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
  /* IRIS §7 — the design's hero faces re-snapped to the nearest rung, relative
     order preserved: ONLINE 31 -> hero, throughput 26.5 -> title, service state
     24 -> head, identity name 21.5 -> lead. Never re-multiplied. */
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
  .cells.hidden { visibility: hidden; }
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
  .bar .fill { height: 100%; }

  /* IDENTITY's terminal block: the export's export buttons sit on the floor. */
  .btnrow { display: flex; gap: var(--sdn-sp-2); margin-top: var(--sdn-sp-6); }
  /* The card body is the QR's stage — it keeps its own height and the QR is laid
     over it, so opening the QR cannot resize the widget or its grid row. */
  .idbody { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .qr {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--sdn-sp-3);
    overflow: hidden;
  }
  .qr canvas { background: #eaf6f8; padding: 6px; max-width: 100%; max-height: 100%; height: auto; width: auto; }
  .qrhint {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.04em;
    text-align: center;
  }

  .tabs { display: inline-flex; border: 1px solid; }
  .tab {
    border: 0;
    cursor: pointer;
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.08em;
    padding: 4px 11px;
  }
  .tab + .tab { border-left: 1px solid; }

  .map { position: relative; height: 322px; margin: var(--sdn-sp-3) -4px 0; min-width: 0; }
  .mapmeta {
    position: absolute;
    left: 8px;
    top: 6px;
    pointer-events: none;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.12em;
  }
  .maphint {
    position: absolute;
    right: 8px;
    bottom: 6px;
    pointer-events: none;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.1em;
  }
  .mapfoot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sdn-sp-4);
    margin-top: var(--sdn-sp-5);
    flex-wrap: wrap;
  }
  .legend { display: inline-flex; gap: var(--sdn-sp-6); flex-wrap: wrap; }
  .legend span { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.06em; }
  .legend i { width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: none; }

  .thead { display: flex; align-items: baseline; gap: var(--sdn-sp-3); flex-wrap: wrap; }
  .thnum {
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

  /* Below the 12-column grid's usable width every widget takes the full row —
     a 4-of-12 panel at 900px is narrower than the peer id it has to print. */
  @media (max-width: 1180px) {
    .grid > :global(*) { grid-column: span 12 !important; }
    .map { height: 300px; }
  }
</style>
