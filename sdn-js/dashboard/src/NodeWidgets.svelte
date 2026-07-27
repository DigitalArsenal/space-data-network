<script>
  /**
   * THIS NODE as a PAGE of widgets (owner directive 2026-07-27: "the 'this
   * node' page needs to look less like a modal and more like a page with
   * different element widgets").
   *
   * The same facts NodeDetail shows in one column, broken into independent
   * panels on a responsive grid: status/identity, verification keys, EPM
   * provenance, chain addresses, the contact card, and network addresses.
   * Each widget renders only if it HAS content — an absent fact leaves no
   * empty panel behind, and nothing is ever invented to fill one.
   *
   * The NODES modal deliberately keeps the compact NodeDetail: a dialog wants
   * one scannable column, a page wants widgets.
   *
   * Design system only — Panel/StatusChip/Kicker/GBtn + theme tokens.
   */
  import { onMount } from 'svelte';
  import QRCode from 'qrcode';
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import Kicker from 'spaceaware-student-sdn/src/lib/components/Kicker.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { parseVCard, displayFields, isAliasEmail, extractIdentity, buildCompactVCard } from './vcard.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';

  /** @type {{ node: any, now: number }} */
  let { node, now } = $props();

  let view = $state('parsed'); // 'parsed' | 'raw' | 'qr'
  let epmAvailable = $state(false);
  let qrCanvas = $state(null);

  const props = $derived(parseVCard(node.vcard));
  const identity = $derived(extractIdentity(props));
  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const coords = $derived(formatCoords(node.lat, node.lon));
  const hasVCard = $derived(Boolean(node.vcard?.trim()));

  // Machine props already decoded into their own widgets (or too raw to show,
  // like the embedded binary EPM) stay out of the parsed contact view.
  const HIDDEN_PROPS = new Set([
    'X-SDN-PEER-ID', 'X-SDN-EPM-CID', 'X-SDN-EPM-B64', 'X-SDN-EPM-SIGNATURE',
    'X-SDN-EPM-SIGNATURE-TIMESTAMP', 'X-SIGNING-KEY', 'X-ENCRYPTION-KEY',
    'X-SDN-BITCOIN-ADDRESS', 'PRODID',
  ]);
  const fields = $derived(
    displayFields(
      props.filter(
        (p) => !isAliasEmail(p) && !HIDDEN_PROPS.has(p.name) &&
          !p.name.endsWith('.X-ABLABEL') && !p.name.endsWith('.X-ABRELATEDNAMES')
      )
    )
  );

  const statusRows = $derived(
    [
      ['PEER ID', node.peerId || '—', theme.ice],
      ['ROLE', node.role ? node.role.toUpperCase() : '', theme.textBody],
      ['GEO', node.geoLabel || (coords ? '' : ''), theme.textBody],
      ['AGENT', node.agent || '—', theme.textBody],
      ['SDS · SUITE', node.standardsVersion || node.suiteVersion ? `${node.standardsVersion || '—'} · ${node.suiteVersion || '—'}` : '', theme.textDim],
      ['LATENCY', node.latencyMs ? `${node.latencyMs.toFixed(0)} ms` : '', theme.textBody],
      [node.isSelf ? 'UPTIME' : 'LAST SEEN', node.isSelf ? formatUptime(node.uptimeS) : formatLastSeen(node.lastSeen, now), theme.textBody],
    ].filter(([, v]) => Boolean(v))
  );

  const keyRows = $derived(
    [
      ['XPUB', identity.xpub],
      ['SIGNING PATH', identity.signPaths.join('   ·   ')],
      ['ENCRYPTION PATH', identity.encryptPaths.join('   ·   ')],
      ['SIGNING KEY', identity.signingKeys.join('\n')],
      ['ENCRYPTION KEY', identity.encryptionKeys.join('\n')],
    ].filter(([, v]) => Boolean(v))
  );

  const epmRows = $derived(
    [
      ['SIGNATURE', identity.epmSignature],
      [
        'SIGNED AT',
        identity.epmSignedAt
          ? `${identity.epmSignedAt} · ${new Date(Number(identity.epmSignedAt) * 1000).toISOString()}`
          : '',
      ],
      ['CID', identity.epmCid],
    ].filter(([, v]) => Boolean(v))
  );

  const chainRows = $derived(
    [
      ['BITCOIN', identity.addresses.bitcoin],
      ['ETHEREUM', identity.addresses.ethereum],
      ['SOLANA', identity.addresses.solana],
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

  // The QR content is the node's SERVER-canonical compact card; the
  // client-built one is only the offline fallback.
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
    fetch(`/identity/${node.peerId}.epm`, { method: 'HEAD' })
      .then((res) => (epmAvailable = res.ok))
      .catch(() => {});
  });
</script>

<div class="grid">
  <!-- STATUS & IDENTITY ------------------------------------------------- -->
  <Panel variant="raised" pad="0">
    <div class="w">
      <div class="whead" style="border-color:{theme.divider};">
        <Kicker text="NODE" />
        <div class="wtitle" style="color:{theme.textBright};">STATUS &amp; IDENTITY</div>
        <div class="wchips">
          <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
          <StatusChip label={node.online ? 'ONLINE' : 'OFFLINE'} color={node.online ? theme.green : theme.textMuted} />
        </div>
      </div>
      <div class="wbody">
        <dl>
          {#each statusRows as [label, value, color] (label)}
            <div class="row">
              <dt style="color:{theme.textMuted};">{label}</dt>
              <dd class="mono" style="color:{color};">
                {value}{#if label === 'GEO' && coords}<span style="color:{theme.textFaint};"> · {coords}</span>{/if}
              </dd>
            </div>
          {/each}
          {#if !node.trustLevel?.trim()}
            <div class="row">
              <dt style="color:{theme.textMuted};">TRUST</dt>
              <dd style="color:{theme.textFaint};">no assertion</dd>
            </div>
          {/if}
        </dl>
      </div>
    </div>
  </Panel>

  <!-- VERIFICATION KEYS -------------------------------------------------- -->
  {#if keyRows.length}
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="DERIVED FROM THE NODE SEED" />
          <div class="wtitle" style="color:{theme.textBright};">VERIFICATION KEYS</div>
        </div>
        <div class="wbody">
          <dl>
            {#each keyRows as [label, value] (label)}
              <div class="row">
                <dt style="color:{theme.textMuted};">{label}</dt>
                <dd class="mono wrap" style="color:{label.includes('PATH') ? theme.ice : theme.textBody};">{value}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </div>
    </Panel>
  {/if}

  <!-- EPM PROVENANCE ----------------------------------------------------- -->
  {#if epmRows.length}
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="SIGNED RECORD" />
          <div class="wtitle" style="color:{theme.textBright};">EPM PROVENANCE</div>
        </div>
        <div class="wbody">
          <dl>
            {#each epmRows as [label, value] (label)}
              <div class="row">
                <dt style="color:{theme.textMuted};">{label}</dt>
                <dd class="mono wrap small" style="color:{theme.textBody};">{value}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </div>
    </Panel>
  {/if}

  <!-- CHAIN ADDRESSES ---------------------------------------------------- -->
  {#if chainRows.length}
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="PUBLISHED IN THE CARD" />
          <div class="wtitle" style="color:{theme.textBright};">CHAIN ADDRESSES</div>
        </div>
        <div class="wbody">
          <dl>
            {#each chainRows as [label, value] (label)}
              <div class="row">
                <dt style="color:{theme.textMuted};">{label}</dt>
                <dd class="mono wrap" style="color:{theme.textBody};">{value}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </div>
    </Panel>
  {/if}

  <!-- CONTACT CARD (wide) ------------------------------------------------ -->
  <div class="wide">
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="VCARD" />
          <div class="wtitle" style="color:{theme.textBright};">CONTACT CARD</div>
          {#if hasVCard}
            <div class="wchips actions">
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
        <div class="wbody">
          {#if !hasVCard}
            <div class="none" style="color:{theme.textFaint};">No vCard published by this node.</div>
          {:else if view === 'raw'}
            <pre class="mono" style="color:{theme.textDim};border-color:{theme.hairline};">{node.vcard}</pre>
          {:else if view === 'qr'}
            <div class="qr">
              <canvas bind:this={qrCanvas}></canvas>
              <div class="qr-hint" style="color:{theme.textFaint};">
                Scan to import this node as a contact (vCard 3.0 — xpub, key derivation paths,
                EPM signature + CID ride as email aliases).
              </div>
            </div>
          {:else}
            <dl class="cols">
              {#each fields as field (field.name)}
                <div class="row">
                  <dt style="color:{theme.textMuted};">{field.label}</dt>
                  <dd class="mono wrap" style="color:{theme.textBody};">
                    {#each field.values as v, i}{#if i}<br />{/if}{v}{/each}
                  </dd>
                </div>
              {/each}
            </dl>
          {/if}
        </div>
      </div>
    </Panel>
  </div>

  <!-- NETWORK ADDRESSES -------------------------------------------------- -->
  {#if node.addrs?.length}
    <div class="wide">
      <Panel variant="raised" pad="0">
        <div class="w">
          <div class="whead" style="border-color:{theme.divider};">
            <Kicker text="LIBP2P" />
            <div class="wtitle" style="color:{theme.textBright};">NETWORK ADDRESSES</div>
            <div class="wchips">
              <StatusChip label={`${node.addrs.length}`} color={theme.ice} dot={false} />
            </div>
          </div>
          <div class="wbody">
            <ul class="addr-list" style="border-color:{theme.hairline};">
              {#each node.addrs as addr (addr)}
                <li class="mono" style="color:{theme.textDim};border-color:{theme.divider};">{addr}</li>
              {/each}
            </ul>
          </div>
        </div>
      </Panel>
    </div>
  {/if}
</div>

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 16px;
    align-items: start;
    min-width: 0;
  }
  /* Card + addresses want the full row where there is room to give it. */
  .wide { grid-column: 1 / -1; min-width: 0; }
  .grid :global(> section),
  .wide :global(> section) { height: 100%; }
  .w { display: flex; flex-direction: column; height: 100%; min-width: 0; }
  .whead {
    display: flex;
    align-items: baseline;
    gap: 10px;
    flex-wrap: wrap;
    padding: 13px 16px 11px;
    border-bottom: 1px solid;
  }
  .wtitle {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 16px;
    letter-spacing: 0.1em;
    margin-right: auto;
  }
  .wchips { display: flex; gap: 6px; flex-wrap: wrap; }
  .wchips.actions { gap: 6px; }
  .wbody { padding: 12px 16px 15px; min-width: 0; }
  dl { margin: 0; }
  dl.cols { columns: 2; column-gap: 26px; }
  dl.cols .row { break-inside: avoid; }
  .row { display: flex; gap: 12px; padding: 4px 0; align-items: baseline; min-width: 0; }
  dt { flex: none; width: 132px; font-size: 12.5px; letter-spacing: 0.16em; }
  dd { margin: 0; font-size: 15.5px; min-width: 0; }
  dd.wrap { overflow-wrap: anywhere; }
  dd.small { font-size: 12px; white-space: pre-line; }
  .act {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    letter-spacing: 0.12em;
    padding: 4px 10px;
  }
  .qr { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 8px 0 4px; }
  .qr canvas { background: #eaf6f8; padding: 6px; max-width: 100%; height: auto; }
  .qr-hint { font-size: 12px; letter-spacing: 0.04em; max-width: 460px; text-align: center; line-height: 1.5; }
  .addr-list { list-style: none; margin: 0; padding: 0; border: 1px solid; }
  .addr-list li {
    font-size: 11.5px;
    line-height: 1.5;
    overflow-wrap: anywhere;
    padding: 6px 10px;
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

  @media (max-width: 760px) {
    .grid { grid-template-columns: 1fr; gap: 12px; }
    dl.cols { columns: 1; }
    .row { flex-direction: column; gap: 2px; align-items: stretch; }
    dt { width: auto; }
    dd { font-size: 15px; }
    .whead { padding: 12px 13px 10px; }
    .wbody { padding: 11px 13px 13px; }
  }
</style>
