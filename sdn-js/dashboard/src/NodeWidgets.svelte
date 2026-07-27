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
  import { parseVCard, contactCard, extractIdentity, buildCompactVCard } from './vcard.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';
  import { apiFetch, apiPutExpectBinary } from './api.js';
  import { profileFormFromJson, profileFormToBody } from './profile.js';

  /** @type {{ node: any, now: number, canEdit?: boolean }} */
  let { node, now, canEdit = false } = $props();

  // ---- key slots: paths in effect, GEN KEY proposals, save ----------------
  /** @type {{slot:string,label:string,rotatable:boolean}[]} */
  let keySlots = $state([]);
  let paths = $state({});
  let baseline = $state({});
  let proposed = $state({});
  let busySlot = $state('');
  let savingPaths = $state(false);
  let keyError = $state('');
  let keyNotice = $state('');

  const pathsDirty = $derived(keySlots.some((s) => (paths[s.slot] ?? '') !== (baseline[s.slot] ?? '')));

  const SLOT_LABEL = { signing: 'SIGNING PATH', encryption: 'ENCRYPTION PATH' };

  async function loadSlots() {
    // §16.5's rule applies here too: /api/node/epm/keys is Admin-only, so an
    // anonymous or below-Admin visitor must never call it. A 403 in the
    // console on every public page load is exactly the noise that trains
    // operators to ignore real errors.
    if (!node?.isSelf || !canEdit) {
      keySlots = [];
      return;
    }
    try {
      const res = await apiFetch('/api/node/epm/keys');
      const slots = (res?.slots ?? [])
        // §18.7.2: only the xpub-derivable secp256k1 slots are offered. The
        // ed25519 record-signing key is not rotatable from a button.
        .filter((s) => s.xpub_derivable)
        .map((s) => ({ slot: s.slot, label: SLOT_LABEL[s.slot] ?? String(s.slot).toUpperCase(), rotatable: Boolean(s.rotatable) }));
      const truth = {};
      for (const s of res?.slots ?? []) if (s.xpub_derivable) truth[s.slot] = s.path ?? '';

      // A refresh must NEVER discard an operator's unsaved work. The status
      // feed re-renders this widget every few seconds, and re-reading server
      // truth into the field wiped any pending GEN KEY proposal while the
      // operator was still reading it.
      //
      // Field content is replaced only when:
      //   · it is not dirty (nothing to lose), or
      //   · the SERVER itself moved — someone published a different path, and
      //     showing a stale proposal against it would be a lie.
      // Otherwise the edit stands and the dirty flag with it.
      const merged = {};
      const keptProposals = {};
      for (const slot of Object.keys(truth)) {
        const dirty = (paths[slot] ?? '') !== (baseline[slot] ?? '');
        const serverMoved = baseline[slot] !== undefined && baseline[slot] !== truth[slot];
        const keep = dirty && !serverMoved;
        merged[slot] = keep ? paths[slot] : truth[slot];
        if (keep && proposed[slot]) keptProposals[slot] = true;
      }
      keySlots = slots;
      paths = merged;
      // The baseline is always server truth — that is what "dirty" is measured
      // against, and what SAVE must diff from.
      baseline = { ...truth };
      proposed = keptProposals;
    } catch {
      // Anonymous or non-admin: the widget simply shows what the card says.
      keySlots = [];
    }
  }

  async function genKey(slot) {
    if (busySlot) return;
    busySlot = slot;
    keyError = '';
    keyNotice = '';
    try {
      const res = await apiFetch('/api/node/epm/keys', { method: 'POST', body: { slot } });
      // A PROPOSAL: it fills the field and nothing else. Never auto-saved.
      if (res?.next_path) {
        paths = { ...paths, [slot]: res.next_path };
        proposed = { ...proposed, [slot]: true };
      }
    } catch (err) {
      keyError = err?.message || 'The node refused to propose a path.';
    } finally {
      busySlot = '';
    }
  }

  async function savePaths() {
    if (savingPaths) return;
    savingPaths = true;
    keyError = '';
    keyNotice = '';
    try {
      // §6 round-trip rule: PUT replaces the WHOLE profile, so the current one
      // is read back and re-sent with the edited paths carried alongside it.
      const current = await apiFetch('/api/node/epm/json').catch(() => ({}));
      const body = profileFormToBody(profileFormFromJson(current));
      for (const s of keySlots) body[`${s.slot}_key_path`] = paths[s.slot] ?? '';
      await apiPutExpectBinary('/api/node/epm', body);

      // VERIFY-AFTER-WRITE. The node is the authority on what it published; a
      // 200 is not proof the path was applied. Re-read and compare, so this
      // can never report a success the record does not show.
      const after = await apiFetch('/api/node/epm/keys').catch(() => null);
      const applied = {};
      for (const s of after?.slots ?? []) if (s.xpub_derivable) applied[s.slot] = s.path ?? '';
      const missed = keySlots.filter((s) => (applied[s.slot] ?? '') !== (paths[s.slot] ?? ''));
      if (missed.length) {
        keyError =
          `The node accepted the request but still publishes ${missed
            .map((s) => `${s.label} ${applied[s.slot] || '(unset)'}`)
            .join(', ')}. The edited path was not applied.`;
        paths = { ...applied };
        baseline = { ...applied };
      } else {
        baseline = { ...paths };
        keyNotice = 'Paths published — the EPM was re-signed and the sign/encrypt aliases regenerated.';
      }
      proposed = {};
    } catch (err) {
      keyError = err?.message || 'Save failed.';
    } finally {
      savingPaths = false;
    }
  }

  // Section controls, in the owner's order: MAIN CARD -> CANONICAL RAW -> QR.
  // QR is a full-section takeover, not an inline panel.
  let view = $state('card'); // 'card' | 'raw' | 'qr'
  let epmAvailable = $state(false);
  let qrCanvas = $state(null);

  const props = $derived(parseVCard(node.vcard));
  const identity = $derived(extractIdentity(props));
  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const coords = $derived(formatCoords(node.lat, node.lon));
  const hasVCard = $derived(Boolean(node.vcard?.trim()));

  // The HUMAN card: a fixed set of labelled rows that render even when empty,
  // so the shape of the card is visible. Values are never invented.
  const contactRows = $derived(contactCard(props));

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

  // §18.7.6: the public-key rows are gone. A verifier derives those from
  // xpub + path, so publishing the bytes was redundant — the PATHS are the
  // identity surface, and they are editable.
  const xpubRow = $derived(identity.xpub);

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

  // Load when the session's authority changes — NOT on every feed tick. The
  // widget re-renders whenever the status feed produces a new node object, and
  // re-fetching on each of those was what reset the field. `loadedFor` is a
  // plain flag, not $state: an effect that reads and writes its own reactive
  // guard re-runs forever.
  let loadedFor = null;
  $effect(() => {
    const authority = canEdit;
    if (loadedFor === authority) return;
    loadedFor = authority;
    loadSlots();
  });

  onMount(() => {
    fetch(`/identity/${node.peerId}.epm`, { method: 'HEAD' })
      .then((res) => (epmAvailable = res.ok))
      .catch(() => {});
  });
</script>

<div class="grid">
  <!-- CONTACT CARD — upper-left, the human card ------------------------- -->
  <div class="contact">
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="VCARD" />
          <div class="wtitle" style="color:{theme.textBright};">CONTACT CARD</div>
          <div class="wchips actions">
            {#each [['card', 'MAIN CARD'], ['raw', 'CANONICAL RAW'], ['qr', 'QR']] as [id, label] (id)}
              <button
                class="act"
                style="color:{view === id ? theme.cyan : theme.ice};border-color:{view === id ? theme.cyan : theme.hairline};"
                onclick={() => (view = id)}
              >{label}</button>
            {/each}
          </div>
        </div>
        <div class="wbody">
          {#if view === 'qr'}
            <!-- QR REPLACES ALL CONTENT of the section when selected. -->
            <div class="qr">
              <canvas bind:this={qrCanvas}></canvas>
              <div class="qr-hint" style="color:{theme.textFaint};">
                Scan to import this node as a contact — the server-canonical compact card.
              </div>
            </div>
          {:else if view === 'raw'}
            <pre class="mono" style="color:{theme.textDim};border-color:{theme.hairline};">{node.vcard || 'No vCard published by this node.'}</pre>
          {:else}
            <dl class="contact-fields">
              {#each contactRows as f (f.key)}
                <div class="row">
                  <dt style="color:{theme.textMuted};">{f.label}</dt>
                  <dd class="mono wrap" style="color:{f.value ? theme.textBody : theme.textFaint};">{f.value}</dd>
                </div>
              {/each}
            </dl>
          {/if}
        </div>
        {#if hasVCard && view !== 'qr'}
          <!-- Downloads live at the BOTTOM of the vCard section. -->
          <div class="wfoot" style="border-color:{theme.divider};">
            <span class="k" style="color:{theme.textMuted};">DOWNLOADS</span>
            <span class="dl">
              <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadVcf}>↓ VCF</button>
              {#if epmAvailable}
                <button class="act" style="color:{theme.ice};border-color:{theme.hairline};" onclick={downloadEpm}>↓ EPM</button>
              {/if}
            </span>
          </div>
        {/if}
      </div>
    </Panel>
  </div>

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
              <dd class="mono wrap" class:machine={label === 'PEER ID'} style="color:{color};">
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

  <!-- VERIFICATION KEYS — xpub + the EDITABLE derivation paths ---------- -->
  <div>
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kicker text="DERIVED FROM THE NODE SEED" />
          <div class="wtitle" style="color:{theme.textBright};">VERIFICATION KEYS</div>
        </div>
        <div class="wbody">
          <dl>
            <div class="row">
              <dt style="color:{theme.textMuted};">XPUB</dt>
              <dd class="mono wrap machine" style="color:{theme.textBody};">{xpubRow}</dd>
            </div>
          </dl>

          {#each keySlots as slot (slot.slot)}
            <div class="slot" style="border-color:{theme.divider};">
              <div class="slot-head">
                <span class="k" style="color:{theme.textMuted};">{slot.label}</span>
                {#if canEdit && slot.rotatable}
                  <GBtn
                    title="Propose the next derivable path for this slot — fills the field, does not save"
                    disabled={busySlot === slot.slot}
                    onclick={() => genKey(slot.slot)}
                  >{busySlot === slot.slot ? 'PROPOSING…' : 'GEN KEY'}</GBtn>
                {/if}
              </div>
              {#if canEdit}
                <input
                  class="mono path"
                  bind:value={paths[slot.slot]}
                  spellcheck="false"
                  autocomplete="off"
                  style="color:{theme.textBright};border-color:{proposed[slot.slot] ? theme.cyan : theme.hairline};background:{theme.inputWell};"
                />
                {#if proposed[slot.slot]}
                  <div class="hint" style="color:{theme.cyan};">
                    Proposal only — press SAVE PATHS to publish it.
                  </div>
                {/if}
              {:else}
                <div class="mono path ro" style="color:{theme.ice};">{paths[slot.slot] || '—'}</div>
              {/if}
            </div>
          {/each}

          {#if keyError}
            <!-- Verbatim from the server: the hardening rules are not
                 guessable from the UI, so the node's own words are shown. -->
            <div class="err mono" style="color:{theme.red};border-color:{theme.red};">{keyError}</div>
          {/if}
          {#if keyNotice}
            <div class="ok" style="color:{theme.green};border-color:{theme.green};">{keyNotice}</div>
          {/if}
        </div>
        {#if canEdit && keySlots.length}
          <div class="wfoot" style="border-color:{theme.divider};">
            <span class="k" style="color:{theme.textFaint};">
              {pathsDirty ? 'UNSAVED PATH CHANGES' : 'PATHS PUBLISHED'}
            </span>
            <GBtn
              title="Re-sign and republish this node's EPM with these paths"
              variant="primary"
              disabled={!pathsDirty || savingPaths}
              onclick={savePaths}
            >{savingPaths ? 'SAVING…' : 'SAVE PATHS'}</GBtn>
          </div>
        {/if}
      </div>
    </Panel>
  </div>

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
                <dd class="mono wrap machine" style="color:{theme.textBody};">{value}</dd>
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
                <dd class="mono wrap machine" style="color:{theme.textBody};">{value}</dd>
              </div>
            {/each}
          </dl>
        </div>
      </div>
    </Panel>
  {/if}

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
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    /* dense: a short widget backfills the hole a tall one leaves, so the page
       fills upper-left -> lower-right instead of stranding one column. */
    grid-auto-flow: row dense;
    gap: 16px;
    /* Owner: every widget in a row is the SAME HEIGHT — the grid reads as
       uniform cards instead of leaving dead space under the short ones. */
    align-items: stretch;
    min-width: 0;
  }
  .grid > * { min-width: 0; display: grid; }
  .grid :global(> * > section) { height: 100%; }
  /* The contact card is the first and widest widget: two columns where there
     is room, so its labelled rows are readable rather than squeezed. */
  .contact { grid-column: span 2; min-width: 0; }
  .contact :global(> section) { height: 100%; }
  .contact .contact-fields { columns: 2; column-gap: 24px; }
  .contact .contact-fields .row { break-inside: avoid; }
  /* Addresses are long strings; give them the full row. */
  .wide { grid-column: 1 / -1; min-width: 0; }
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
  .wbody { padding: 12px 16px 15px; min-width: 0; overflow-wrap: anywhere; }
  .wfoot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    flex-wrap: wrap;
    border-top: 1px solid;
    padding: 11px 16px;
    margin-top: auto;
  }
  .wfoot .k { font-size: 12.5px; letter-spacing: 0.16em; }
  .slot { border-top: 1px solid; margin-top: 10px; padding-top: 10px; }
  .slot-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 6px; }
  .slot .k { font-size: 12.5px; letter-spacing: 0.16em; }
  .path {
    width: 100%;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 14px;
    letter-spacing: 0.02em;
    padding: 7px 9px;
    outline: none;
    overflow-wrap: anywhere;
  }
  .path.ro { border: 0; padding: 0; background: transparent; }
  .hint { font-size: 12.5px; letter-spacing: 0.03em; margin-top: 5px; line-height: 1.5; }
  .err, .ok {
    border: 1px solid;
    padding: 8px 10px;
    margin-top: 10px;
    font-size: 12.5px;
    line-height: 1.5;
    overflow-wrap: anywhere;
  }
  .dl { display: inline-flex; gap: 6px; flex-wrap: wrap; }
  dl { margin: 0; }
  dl.cols { columns: 2; column-gap: 26px; }
  dl.cols .row { break-inside: avoid; }
  .row { display: flex; gap: 12px; padding: 4px 0; align-items: baseline; min-width: 0; }
  dt { flex: none; width: 132px; font-size: 12.5px; letter-spacing: 0.16em; }
  dd {
    margin: 0;
    font-size: 15.5px;
    min-width: 0;
    /* Nothing is ever clipped: peer ids, xpubs and signature hex are single
       unbreakable tokens, so they MUST be allowed to break anywhere. */
    overflow-wrap: anywhere;
    word-break: break-word;
  }
  dd.wrap { overflow-wrap: anywhere; }
  /* Long machine values (64-128 hex, xpub, CID) at a denser face so a column
     holds them in a few lines instead of a ragged tower. */
  dd.machine { font-size: 12.5px; line-height: 1.45; }
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
    .contact { grid-column: 1 / -1; }
    .contact .contact-fields { columns: 1; }
    dl.cols { columns: 1; }
    .row { flex-direction: column; gap: 2px; align-items: stretch; }
    dt { width: auto; }
    dd { font-size: 15px; }
    .whead { padding: 12px 13px 10px; }
    .wbody { padding: 11px 13px 13px; }
  }
</style>
