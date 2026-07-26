<script>
  /**
   * Node detail modal — full node information for a clicked table row / globe
   * dot. Sections:
   *   · core info (peer id, trust, geo, agent, versions, uptime/last-seen)
   *   · IDENTITY — the SDN identity decoded from the vCard's email aliases
   *     (owner rule): xpub, HD signing/encryption derivation paths (shown as
   *     literal m/44'/… paths, serialized as base64url emails), chain
   *     addresses, EPM CID
   *   · vCard — parsed contact fields (alias emails hidden), RAW text view,
   *     or a scannable QR of the compact v3 card; downloadable as .vcf or as
   *     the signed serialized .epm record (/identity/<peerId>.epm)
   *   · addresses — collapsed by default, small mono rows with separators
   * Esc or overlay click closes. Styled only with theme.js tokens.
   */
  import { onMount } from 'svelte';
  import QRCode from 'qrcode';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { parseVCard, displayFields, isAliasEmail, extractIdentity, buildCompactVCard } from './vcard.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';

  /** @type {{ node: any, now: number, onClose: () => void }} */
  let { node, now, onClose } = $props();

  let view = $state('parsed'); // 'parsed' | 'raw' | 'qr'
  let addrsOpen = $state(false);
  let epmAvailable = $state(false);
  let qrCanvas = $state(null);

  const props = $derived(parseVCard(node.vcard));
  const identity = $derived(extractIdentity(props));
  const fields = $derived(
    displayFields(props.filter((p) => !isAliasEmail(p) && p.name !== 'X-SDN-PEER-ID' && p.name !== 'X-SDN-EPM-CID'))
  );
  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const coords = $derived(formatCoords(node.lat, node.lon));
  const title = $derived(node.dn?.trim() || node.org?.trim() || shortId(node.peerId));
  const hasVCard = $derived(Boolean(node.vcard?.trim()));

  const identityRows = $derived(
    [
      ['XPUB', identity.xpub, true],
      ['SIGNING PATH', identity.signPath, false],
      ['ENCRYPTION PATH', identity.encryptPath, false],
      ['BITCOIN', identity.addresses.bitcoin, true],
      ['ETHEREUM', identity.addresses.ethereum, true],
      ['SOLANA', identity.addresses.solana, true],
      ['EPM CID', identity.epmCid, true],
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

  // Render the QR whenever the QR view opens (canvas mounts with the view).
  $effect(() => {
    if (view !== 'qr' || !qrCanvas) return;
    QRCode.toCanvas(qrCanvas, buildCompactVCard(node, props), {
      errorCorrectionLevel: 'M',
      margin: 2,
      width: 280,
      color: { dark: '#04060a', light: '#eaf6f8' },
    }).catch(() => {});
  });

  onMount(() => {
    // Signed serialized EPM record availability (fail quiet: button hides).
    fetch(`/identity/${node.peerId}.epm`, { method: 'HEAD' })
      .then((res) => (epmAvailable = res.ok))
      .catch(() => {});
  });

  function onKeydown(e) {
    if (e.key === 'Escape') onClose();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="overlay" onclick={(e) => e.target === e.currentTarget && onClose()}>
  <div class="modal" role="dialog" aria-modal="true" aria-label="Node details"
    style="background:{theme.panelRaised};border-color:{theme.panelBorder};color:{theme.textBody};">
    <div class="head" style="border-color:{theme.divider};">
      <div class="titles">
        <div class="dn" style="color:{theme.textBright};">{title}</div>
        {#if node.org?.trim() && node.org.trim() !== title}
          <div class="org" style="color:{theme.textDim};">{node.org}</div>
        {/if}
      </div>
      <div class="chips">
        {#if node.isSelf}<StatusChip label="SELF" color={theme.cyan} dot={false} />{/if}
        <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
        <StatusChip label={node.online ? 'ONLINE' : 'OFFLINE'} color={node.online ? theme.green : theme.textMuted} />
        <button class="close" style="color:{theme.textMuted};border-color:{theme.hairline};" onclick={onClose} aria-label="Close">✕</button>
      </div>
    </div>

    <div class="body">
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
            <div class="qr-hint" style="color:{theme.textFaint};">Scan to import this node as a contact (vCard 3.0 — includes xpub + key derivation paths as email aliases).</div>
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
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(2, 5, 8, 0.72);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 40;
    padding: 28px;
  }
  .modal {
    width: min(720px, 100%);
    max-height: min(84vh, 820px);
    border: 1px solid;
    display: flex;
    flex-direction: column;
    box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    padding: 16px 18px 13px;
    border-bottom: 1px solid;
  }
  .titles { min-width: 0; }
  .dn {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 22px;
    letter-spacing: 0.04em;
    overflow-wrap: anywhere;
  }
  .org { font-size: 15px; letter-spacing: 0.04em; margin-top: 3px; }
  .chips { display: flex; gap: 6px; flex: none; flex-wrap: wrap; justify-content: flex-end; align-items: center; }
  .close {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-size: 14.5px;
    line-height: 1;
    padding: 4px 7px;
    margin-left: 4px;
  }
  .body { overflow: auto; padding: 14px 18px 18px; }
  dl { margin: 0; }
  .row { display: flex; gap: 14px; padding: 4px 0; align-items: baseline; }
  dt { flex: none; width: 150px; font-size: 12.5px; letter-spacing: 0.16em; }
  dd { margin: 0; font-size: 15.5px; min-width: 0; overflow-wrap: anywhere; }
  dd.small { font-size: 12px; }
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
  .qr-hint { font-size: 12px; letter-spacing: 0.04em; max-width: 420px; text-align: center; line-height: 1.5; }
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
