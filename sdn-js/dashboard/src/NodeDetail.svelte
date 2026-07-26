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
  import { onMount } from 'svelte';
  import QRCode from 'qrcode';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { parseVCard, displayFields, isAliasEmail, extractIdentity, buildCompactVCard } from './vcard.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';

  /** @type {{ node: any, now: number }} */
  let { node, now } = $props();

  let view = $state('parsed'); // 'parsed' | 'raw' | 'qr'
  let addrsOpen = $state(false);
  let epmAvailable = $state(false);
  let qrCanvas = $state(null);

  const props = $derived(parseVCard(node.vcard));
  const identity = $derived(extractIdentity(props));
  // Machine props already decoded into the IDENTITY section (or too raw to
  // display, like the embedded binary EPM) stay out of the parsed view —
  // the RAW view still shows the complete card.
  const HIDDEN_PROPS = new Set([
    'X-SDN-PEER-ID',
    'X-SDN-EPM-CID',
    'X-SDN-EPM-B64',
    'X-SDN-EPM-SIGNATURE',
    'X-SDN-EPM-SIGNATURE-TIMESTAMP',
    'X-SIGNING-KEY',
    'X-ENCRYPTION-KEY',
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
    (async () => {
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
        width: 320,
        color: { dark: '#04060a', light: '#eaf6f8' },
      });
    })().catch(() => {});
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

  <div class="section" style="border-color:{theme.divider};">
    <div class="vhead">
      <div class="k" style="color:{theme.textMuted};">VCARD</div>
      {#if hasVCard}
        <div class="actions">
          {#each [['parsed', 'PARSED'], ['raw', 'RAW'], ['qr', 'QR']] as [id, label] (id)}
            <button
              class="act"
              style="color:{view === id ? theme.cyan : theme.ice};border-color:{view === id ? theme.cyan : theme.hairline};"
              onclick={() => (view = id)}
            >{label}</button>
          {/each}
          <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadVcf}>↓ VCF</button>
          {#if epmAvailable}
            <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadEpm}>↓ EPM</button>
          {/if}
        </div>
      {/if}
    </div>
    {#if !hasVCard}
      <div class="none" style="color:{theme.textFaint};">No vCard published by this node.</div>
    {:else if view === 'raw'}
      <pre class="mono" style="color:{theme.textDim};border-color:{theme.hairline};">{node.vcard}</pre>
    {:else if view === 'qr'}
      <div class="qr">
        <canvas bind:this={qrCanvas}></canvas>
        <div class="qr-hint" style="color:{theme.textFaint};">Scan to import this node as a contact (vCard 3.0 — xpub, key derivation paths, EPM signature + CID ride as email aliases).</div>
      </div>
    {:else}
      <dl>
        {#each fields as field (field.name)}
          <div class="row"><dt style="color:{theme.textMuted};">{field.label}</dt>
            <dd class="mono" style="color:{theme.textBody};">
              {#each field.values as v, i}{#if i}<br />{/if}{v}{/each}
            </dd></div>
        {/each}
      </dl>
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
  dt { flex: none; width: 150px; font-size: 12.5px; letter-spacing: 0.16em; }
  dd { margin: 0; font-size: 15.5px; min-width: 0; overflow-wrap: anywhere; }
  dd.small { font-size: 12px; white-space: pre-line; }
  .section { border-top: 1px solid; margin-top: 12px; padding-top: 11px; }
  .k { font-size: 12.5px; letter-spacing: 0.18em; margin-bottom: 7px; display: inline-block; }
  .vhead { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
  .actions { display: flex; gap: 6px; flex-wrap: wrap; }
  .act {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    letter-spacing: 0.12em;
    padding: 3px 9px;
  }
  .qr { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 12px 0 4px; }
  .qr canvas { background: #eaf6f8; padding: 6px; }
  .qr-hint { font-size: 12px; letter-spacing: 0.04em; max-width: 460px; text-align: center; line-height: 1.5; }
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
  .chev { font-size: 11px; letter-spacing: 0.12em; }
  .addr-list {
    list-style: none;
    margin: 9px 0 0;
    padding: 0;
    border: 1px solid;
  }
  .addr-list li {
    font-size: 11px;
    line-height: 1.5;
    overflow-wrap: anywhere;
    padding: 5px 9px;
    border-bottom: 1px solid;
  }
  .addr-list li:last-child { border-bottom: 0; }
  pre {
    margin: 0;
    border: 1px solid;
    padding: 10px 12px;
    font-size: 12px;
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .none { font-size: 15px; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
