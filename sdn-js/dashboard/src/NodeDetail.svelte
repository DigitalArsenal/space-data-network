<script>
  /**
   * Full node information view — shared by the row/dot modal AND the
   * "THIS NODE" sub-page. Sections:
   *   · core info (peer id, trust, geo, agent, versions, uptime/last-seen)
   *   · IDENTITY — the verification chain decoded from the vCard's email
   *     aliases (owner rule; phones drop X-* props): xpub, HD sign/encrypt
   *     derivation paths, signing/encryption keys, EPM signature +
   *     timestamp + CID, chain addresses
   *   · vCard — parsed contact fields (alias emails hidden; empty fields
   *     stay empty — contact data is NEVER invented), RAW text, or the
   *     scannable QR (server-canonical /identity/<peerId>.qr.vcf);
   *     downloads as .vcf / signed .epm
   *   · addresses — collapsed by default, small separated rows
   * Styled only with theme.js tokens.
   */
  import { onMount, untrack } from 'svelte';
  import QRCode from 'qrcode';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import HostedModules from './HostedModules.svelte';
  import {
    parseVCard,
    displayFields,
    isAliasEmail,
    extractIdentity,
    buildCompactVCard,
    cardCarriesCryptoIdentity,
  } from './vcard.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';

  /**
   * `initialView` opens this view directly on one of its tabs. The IDENTITY
   * widget's QR button uses it to land on the scannable card in one click
   * (IRIS ruling 2026-07-30 R5: the QR modal already existed here — "do not
   * build a new modal"), instead of the inline canvas that rendered squashed
   * and unscannable inside the dashboard card.
   *
   * MODULES is the fourth tab (owner directive 2026-07-30 — "clicking on a node
   * shows what's being hosted on that node"; IRIS ruling 2026-07-31: a tab, not
   * a section). `onView` reports a tab change UP so the address bar can carry it
   * — which tab is open is a place, and a refresh returns to it.
   *
   * @type {{ node: any, now: number, initialView?: 'parsed'|'raw'|'qr'|'modules',
   *          onView?: (view: string) => void }}
   */
  let { node, now, initialView = 'parsed', onView = undefined } = $props();

  let view = $state(initialView); // 'parsed' | 'raw' | 'qr' | 'modules'

  /**
   * The tab follows the ADDRESS, not just the click: arriving on
   * `#/peers/<id>/modules`, then pressing Back to `#/peers/<id>`, has to move
   * the tab too. `initialView` is the prop the parent recomputes from the hash.
   *
   * `lastInitial` is deliberately NOT `$state`: reading `view` inside this
   * effect would make the effect depend on the value it writes, and every click
   * on a tab would be undone in the next flush by the prop that had not caught
   * up yet. The effect reacts to the PROP changing and to nothing else.
   */
  // `untrack` because this is a one-time snapshot of a prop at creation, which
  // is exactly what svelte's state_referenced_locally warning exists to make
  // deliberate rather than accidental.
  let lastInitial = untrack(() => initialView) || 'parsed';
  $effect(() => {
    const want = initialView || 'parsed';
    if (want === lastInitial) return;
    lastInitial = want;
    view = want;
  });

  /** One place where a tab is chosen, so the URL can never miss one. */
  function selectView(id) {
    view = id;
    onView?.(id);
  }
  let addrsOpen = $state(false);
  let epmAvailable = $state(false);
  let qrCanvas = $state(null);

  const props = $derived(parseVCard(node.vcard));
  const identity = $derived(extractIdentity(props));
  // Machine props already decoded into the IDENTITY section (or too raw to
  // display, like the embedded binary EPM) stay out of the parsed view —
  // the RAW view still shows the complete card.
  // §18: the emitted card no longer carries key bytes or the serialized blob,
  // so hiding them is dead weight — X-SDN-EPM-B64, X-SIGNING-KEY,
  // X-ENCRYPTION-KEY and X-PUBLIC-KEY are dropped from this list. The
  // verification chain IS still emitted and is still decoded into the
  // IDENTITY section, so those three stay hidden here.
  const HIDDEN_PROPS = new Set([
    'X-SDN-PEER-ID',
    'X-SDN-EPM-CID',
    'X-SDN-EPM-SIGNATURE',
    'X-SDN-EPM-SIGNATURE-TIMESTAMP',
    'X-SDN-BITCOIN-ADDRESS',
    'PRODID',
  ]);
  const fields = $derived(
    displayFields(props.filter((p) => !isAliasEmail(p) && !HIDDEN_PROPS.has(p.name) && !p.name.endsWith('.X-ABLABEL') && !p.name.endsWith('.X-ABRELATEDNAMES')))
  );
  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const coords = $derived(formatCoords(node.lat, node.lon));
  const hasVCard = $derived(Boolean(node.vcard?.trim()));

  /*
   * The PARSED view is an OUTLOOK-STYLE CONTACT CARD (owner 2026-07-31):
   * image upper-left, the basic contact fields as plain text beside it, the
   * scannable QR on the card itself. Basic = what a phone contact shows;
   * everything else stays in the detail list under the card. No photo is
   * published in these cards today, so the image is the tier-colored
   * initials block — never an invented picture.
   */
  const CARD_BASIC = new Set(['FN', 'N', 'ORG', 'TITLE', 'ROLE', 'EMAIL', 'TEL', 'ADR', 'URL']);
  /* Old node binaries publish EPMs whose DN is libp2p's "<peer.ID 16*abc123>"
   * short form; a vCard FN carrying it is machine noise, not a name. */
  const cardFN = $derived.by(() => {
    const fn = (fields.find((f) => f.name === 'FN')?.values?.[0] || '').trim();
    return fn.includes('<peer.ID') ? '' : fn;
  });
  const cardName = $derived.by(() => {
    const name = (node.name || '').trim();
    return cardFN || (name && name !== 'unknown' ? name : 'unnamed');
  });
  const cardInitials = $derived(
    cardName === 'unnamed'
      ? '?'
      : cardName
          .split(/[\s.-]+/)
          .filter(Boolean)
          .slice(0, 2)
          .map((w) => w[0].toUpperCase())
          .join('')
  );
  const basicFields = $derived(fields.filter((f) => CARD_BASIC.has(f.name) && f.name !== 'FN' && f.name !== 'N'));
  const extraFields = $derived(fields.filter((f) => !CARD_BASIC.has(f.name) && f.name !== 'FN' && f.name !== 'N'));
  /** Server-rendered QR (integer module scale, inline disposition). The server
   * 404s when it holds no signed EPM for the peer (owner law 2026-07-31) —
   * that state is SHOWN, never silently blank. */
  let cardQrOk = $state(true);
  /** QR tab render state. 'loading' while the server card fetch is pending
   * (a hung origin previously left a bare blank canvas on screen — owner
   * report 2026-07-31), 'ready' once the code is drawn, 'missing' when
   * neither the server card nor the offline fallback carries the full
   * crypto identity — the refusal renders, never a name-only QR. */
  let qrTabState = $state('loading');

  const identityRows = $derived(
    [
      ['XPUB', identity.xpub, true],
      ['SIGNING PATHS', identity.signPaths.join('   ·   '), false],
      ['ENCRYPTION PATHS', identity.encryptPaths.join('   ·   '), false],
      ['SIGNING KEYS', identity.signingKeys.join('\n'), true],
      ['ENCRYPTION KEYS', identity.encryptionKeys.join('\n'), true],
      ['EPM SIGNATURE', identity.epmSignature, true],
      [
        'EPM SIGNED AT',
        identity.epmSignedAt
          ? `${identity.epmSignedAt} · ${new Date(Number(identity.epmSignedAt) * 1000).toISOString()}`
          : '',
        true,
      ],
      ['EPM CID', identity.epmCid, true],
      ['BITCOIN', identity.addresses.bitcoin, true],
      ['ETHEREUM', identity.addresses.ethereum, true],
      ['SOLANA', identity.addresses.solana, true],
    ].filter(([, v]) => Boolean(v))
  );

  function download(name, blob) {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  }

  const downloadVcf = () =>
    download(`${shortId(node.peerId).replace('…', '-')}.vcf`, new Blob([node.vcard], { type: 'text/vcard' }));

  async function downloadEpm() {
    const res = await fetch(`/identity/${node.peerId}.epm`);
    if (!res.ok) return;
    download(`${shortId(node.peerId).replace('…', '-')}.epm`, await res.blob());
  }

  // Render the QR whenever the QR view opens. The content is the node's
  // SERVER-canonical compact card (/identity/<peerId>.qr.vcf — contact
  // fields + the complete verification-chain aliases); the client-built
  // compact card is only the offline fallback.
  $effect(() => {
    if (view !== 'qr' || !qrCanvas) return;
    const canvas = qrCanvas;
    qrTabState = 'loading';
    (async () => {
      let card = '';
      try {
        // A hung origin must degrade to the fallback, not pin the tab in
        // 'loading' for the CF timeout window (~100s).
        const res = await fetch(`/identity/${node.peerId}.qr.vcf`, {
          signal: AbortSignal.timeout(15000),
        });
        if (res.ok) card = await res.text();
      } catch {
        /* fall back below */
      }
      if (!card.trim()) card = buildCompactVCard(node, props);
      // OWNER LAW 2026-07-31: never render a scannable card without the
      // full crypto identity (xpub + sign/encrypt paths + epmsig) — the
      // server refuses such cards and the offline fallback must too.
      if (!cardCarriesCryptoIdentity(card)) {
        qrTabState = 'missing';
        return;
      }
      await QRCode.toCanvas(canvas, card, {
        errorCorrectionLevel: 'M',
        margin: 2,
        // 480, not 320 (IRIS ruling 2026-07-30 R5). At the vCard contract's
        // 1400B density lock the code is ~version 33 = 149 modules, plus the
        // 2-module quiet zone = 153 across. 320px gave 2.09 px/module — a
        // fractional scale, which is resampling, which is the owner's
        // "unreadable". 480/153 = 3 px/module exactly: an integer scale and a
        // crisp 459px canvas.
        width: 480,
        color: { dark: '#04060a', light: '#eaf6f8' },
      });
      qrTabState = 'ready';
    })().catch(() => {
      // A draw failure must surface as the refusal state, never a blank
      // canvas pretending to be a code.
      qrTabState = 'missing';
    });
  });

  onMount(() => {
    // Signed serialized EPM record availability (fail quiet: button hides).
    fetch(`/identity/${node.peerId}.epm`, { method: 'HEAD' })
      .then((res) => (epmAvailable = res.ok))
      .catch(() => {});
  });
</script>

<div class="detail">
  <dl>
    <div class="row"><dt style="color:{theme.textMuted};">PEER ID</dt>
      <dd class="mono" style="color:{theme.ice};">{node.peerId || '—'}</dd></div>
    <div class="row"><dt style="color:{theme.textMuted};">TRUST</dt>
      <dd><span style="color:{tierColor};">{tier.toUpperCase()}</span>
        {#if !node.trustLevel?.trim()}<span style="color:{theme.textFaint};"> · no assertion</span>{/if}</dd></div>
    {#if node.role}<div class="row"><dt style="color:{theme.textMuted};">ROLE</dt>
      <dd style="color:{theme.textBody};">{node.role.toUpperCase()}</dd></div>{/if}
    {#if node.geoLabel || coords}
      <div class="row"><dt style="color:{theme.textMuted};">GEO</dt>
        <dd style="color:{theme.textBody};">{node.geoLabel || '—'}{#if coords}<span style="color:{theme.textFaint};"> · {coords}</span>{/if}</dd></div>
    {/if}
    <div class="row"><dt style="color:{theme.textMuted};">AGENT</dt>
      <dd class="mono" style="color:{theme.textBody};">{node.agent || '—'}</dd></div>
    {#if node.suiteVersion || node.standardsVersion}
      <div class="row"><dt style="color:{theme.textMuted};">SDS · SUITE</dt>
        <dd class="mono" style="color:{theme.textDim};">{node.standardsVersion || '—'} · {node.suiteVersion || '—'}</dd></div>
    {/if}
    {#if node.latencyMs}
      <div class="row"><dt style="color:{theme.textMuted};">LATENCY</dt>
        <dd class="mono" style="color:{theme.textBody};">{node.latencyMs.toFixed(0)} ms</dd></div>
    {/if}
    <div class="row"><dt style="color:{theme.textMuted};">{node.isSelf ? 'UPTIME' : 'LAST SEEN'}</dt>
      <dd class="mono" style="color:{theme.textBody};">{node.isSelf ? formatUptime(node.uptimeS) : formatLastSeen(node.lastSeen, now)}</dd></div>
  </dl>

  {#if identityRows.length}
    <div class="section" style="border-color:{theme.divider};">
      <div class="k" style="color:{theme.textMuted};">IDENTITY</div>
      <dl>
        {#each identityRows as [label, value, mono] (label)}
          <div class="row"><dt style="color:{theme.textMuted};">{label}</dt>
            <dd class={mono ? 'mono small' : 'mono'} style="color:{label.includes('PATH') ? theme.ice : theme.textBody};">{value}</dd></div>
        {/each}
      </dl>
    </div>
  {/if}

  <!-- ONE TAB BAR, FOUR TABS (IRIS 2026-07-31). MODULES is not a vCard view, so
       the section's own label follows the tab rather than always saying VCARD —
       and the bar renders even for a node that publishes no card, because "what
       does this node host" is a question about the node, not about its card. -->
  <div class="section" style="border-color:{theme.divider};">
    <div class="vhead">
      <!-- ONE LABEL PER SECTION. On the MODULES tab the panel below prints its
           own head ("HOSTED MODULES" + the verified chip), so a label here would
           stack the same word twice — visible in the first local build, and the
           kind of repeat the owner has already cut three times. -->
      {#if view === 'modules'}<div></div>{:else}<div class="k" style="color:{theme.textMuted};">VCARD</div>{/if}
      <div class="actions">
        {#each hasVCard ? [['parsed', 'PARSED'], ['raw', 'RAW'], ['qr', 'QR'], ['modules', 'MODULES']] : [['modules', 'MODULES']] as [id, label] (id)}
          <button
            class="act"
            style="color:{view === id ? theme.cyan : theme.ice};border-color:{view === id ? theme.cyan : theme.hairline};"
            onclick={() => selectView(id)}
          >{label}</button>
        {/each}
        {#if hasVCard}
          <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadVcf}>↓ VCF</button>
          {#if epmAvailable}
            <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadEpm}>↓ EPM</button>
          {/if}
        {/if}
      </div>
    </div>
    {#if view === 'modules'}
      <HostedModules {node} />
    {:else if !hasVCard}
      <div class="none" style="color:{theme.textFaint};">No vCard published by this node.</div>
    {:else if view === 'raw'}
      <pre class="mono" style="color:{theme.textDim};border-color:{theme.hairline};">{node.vcard}</pre>
    {:else if view === 'qr'}
      <div class="qr">
        {#if qrTabState === 'missing'}
          <div class="none" style="color:{theme.textFaint};">
            No scannable card: this node has not exchanged its signed EPM yet, so the full crypto
            identity (xpub, key derivation paths, EPM signature) is not held here. A QR without it
            is never served.
          </div>
        {:else}
          <canvas bind:this={qrCanvas} style={qrTabState === 'ready' ? '' : 'display:none;'}></canvas>
          {#if qrTabState === 'loading'}
            <div class="none" style="color:{theme.textFaint};">FETCHING SIGNED CONTACT CARD…</div>
          {:else}
            <div class="qr-hint" style="color:{theme.textFaint};">Scan to import this node as a contact (vCard 3.0 — xpub, key derivation paths, EPM signature + CID ride as email aliases).</div>
          {/if}
        {/if}
      </div>
    {:else}
      <div class="card" style="border-color:{theme.hairline};">
        <div class="card-head">
          <div class="avatar" style="border-color:{tierColor};color:{tierColor};" aria-hidden="true">{cardInitials}</div>
          <div class="card-id">
            <div class="card-name" style="color:{theme.textBright};">{cardName}</div>
            {#each basicFields as field (field.name)}
              <div class="card-line">
                <span class="card-k" style="color:{theme.textMuted};">{field.label}</span>
                <span class="card-v" style="color:{theme.textBody};">{field.values.join(' · ')}</span>
              </div>
            {/each}
            <div class="card-line">
              <span class="card-k" style="color:{theme.textMuted};">PEER ID</span>
              <span class="card-v mono" style="color:{theme.ice};">{shortId(node.peerId)}</span>
            </div>
          </div>
          {#if cardQrOk}
            <img
              class="card-qr"
              src={`/identity/${node.peerId}.qr.png`}
              alt="Scannable contact QR for this node"
              width="160"
              height="160"
              onerror={() => (cardQrOk = false)}
            />
          {:else}
            <div class="card-qr-missing mono" style="color:{theme.textFaint};border-color:{theme.hairline};">
              NO SIGNED EPM YET — QR REQUIRES THE FULL CRYPTO IDENTITY
            </div>
          {/if}
        </div>
        {#if extraFields.length}
          <dl class="card-extra" style="border-color:{theme.divider};">
            {#each extraFields as field (field.name)}
              <div class="row"><dt style="color:{theme.textMuted};">{field.label}</dt>
                <dd class="mono" style="color:{theme.textBody};">
                  {#each field.values as v, i}{#if i}<br />{/if}{v}{/each}
                </dd></div>
            {/each}
          </dl>
        {/if}
      </div>
    {/if}
  </div>

  {#if node.addrs?.length}
    <div class="section" style="border-color:{theme.divider};">
      <button class="addr-toggle" style="color:{theme.textMuted};" onclick={() => (addrsOpen = !addrsOpen)} aria-expanded={addrsOpen}>
        <span class="k" style="margin-bottom:0;">ADDRESSES ({node.addrs.length})</span>
        <span class="chev" style="color:{theme.ice};">{addrsOpen ? '▾ HIDE' : '▸ SHOW'}</span>
      </button>
      {#if addrsOpen}
        <ul class="addr-list" style="border-color:{theme.hairline};">
          {#each node.addrs as addr (addr)}
            <li class="mono" style="color:{theme.textDim};border-color:{theme.divider};">{addr}</li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>

<style>
  .detail { min-width: 0; }
  dl { margin: 0; }
  .row { display: flex; gap: 14px; padding: 4px 0; align-items: baseline; }
  dt { flex: none; width: 150px; font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); letter-spacing: 0.16em; }
  dd { margin: 0; font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value); min-width: 0; overflow-wrap: anywhere; }
  dd.small { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); white-space: pre-line; }
  .section { border-top: 1px solid; margin-top: 12px; padding-top: 11px; }
  .k { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); letter-spacing: 0.18em; margin-bottom: 7px; display: inline-block; }
  .vhead { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
  .actions { display: flex; gap: 6px; flex-wrap: wrap; }
  .act {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.12em;
    padding: 3px 9px;
  }
  .qr { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 12px 0 4px; }
  .card { border: 1px solid; padding: 14px; margin-top: 8px; }
  .card-head { display: flex; gap: 14px; align-items: flex-start; }
  .avatar {
    flex: none;
    width: 72px; height: 72px;
    border: 1px solid;
    display: flex; align-items: center; justify-content: center;
    font-size: 24px; letter-spacing: 0.06em;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .card-id { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
  .card-name { font-size: 15px; letter-spacing: 0.08em; }
  .card-line { display: flex; gap: 8px; align-items: baseline; min-width: 0; }
  .card-k { flex: none; width: 68px; font-size: var(--sdn-fs-label); letter-spacing: 0.14em; }
  .card-v { font-size: var(--sdn-fs-value); overflow-wrap: anywhere; min-width: 0; }
  .card-qr { flex: none; width: 160px; height: 160px; image-rendering: pixelated; }
  .card-qr-missing {
    flex: none;
    width: 160px;
    border: 1px dashed;
    padding: 10px;
    font-size: 9px;
    letter-spacing: 0.08em;
    text-align: center;
    align-self: flex-start;
  }
  .card-extra { border-top: 1px solid; margin-top: 12px; padding-top: 10px; }
  @media (max-width: 560px) {
    .card-head { flex-wrap: wrap; }
    .card-qr { order: 3; margin: 8px auto 0; }
  }
  /* GRAMMAR L6 (iris-dashboard-grammar-law): aspect-locked media is clamped on
     AT MOST ONE axis and states its ratio, with flex:none so the column's
     flex-shrink cannot squash it. Two percentage clamps plus flex-shrink is
     exactly what made the dashboard's inline QR an unscannable rectangle. */
  .qr canvas {
    background: #eaf6f8;
    padding: 6px;
    max-width: 100%;
    height: auto;
    aspect-ratio: 1;
    flex: none;
  }
  .qr-hint { font-size: var(--sdn-fs-label); letter-spacing: 0.04em; max-width: 460px; text-align: center; line-height: var(--sdn-lh-label); }
  .addr-toggle {
    background: transparent;
    border: 0;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: 0;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .chev { font-size: var(--sdn-fs-fine); line-height: var(--sdn-lh-fine); letter-spacing: 0.12em; }
  .addr-list {
    list-style: none;
    margin: 9px 0 0;
    padding: 0;
    border: 1px solid;
  }
  .addr-list li {
    font-size: var(--sdn-fs-fine);
    line-height: var(--sdn-lh-fine);
    overflow-wrap: anywhere;
    padding: 5px 9px;
    border-bottom: 1px solid;
  }
  .addr-list li:last-child { border-bottom: 0; }
  pre {
    margin: 0;
    border: 1px solid;
    padding: 10px 12px;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .none { font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value); }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
