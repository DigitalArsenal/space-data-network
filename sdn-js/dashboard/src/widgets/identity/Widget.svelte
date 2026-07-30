<script>
  /**
   * IDENTITY.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :142-166 · registry entry :864
   *
   * The template's literal "CONFIRMED" badge is replaced by the node's ACTUAL
   * trust assertion, rendered through StatusChip. A node that has asserted
   * nothing gets no badge — a green CONFIRMED chip that means nothing is worse
   * than no chip (IRIS §6). CSV is dropped: no single-EPM CSV serializer exists
   * anywhere (IRIS §6).
   *
   * DETAIL replaces the dead THIS NODE route (owner directive 2026-07-30: "the
   * 'this node' menu should go away") — the self node is a node, so it opens the
   * SAME detail modal every other peer gets, which already carries the
   * verification keys, chain addresses and EPM provenance that page used to hold
   * (IRIS R6). QR opens that modal on its QR tab, because the inline canvas
   * rendered wide, squat and unscannable (owner directive 2026-07-30; IRIS R5).
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { parseVCard, extractIdentity } from '../../vcard.js';
  import { normalizeTrust, hasTrustAssertion, TRUST_COLOR_TOKEN } from '../../trust.js';
  import { apiFetch } from '../../api.js';

  let { node, canEdit = false, onEdit = () => {}, onShowQr = () => {}, onShowDetail = () => {} } = $props();

  const props = $derived(parseVCard(node?.vcard));
  const identity = $derived(extractIdentity(props));
  const tier = $derived(normalizeTrust(node?.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
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
</script>

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
<div class="btnrow">
  <GBtn title="Every published field, key and address for this node" style="flex:1;" onclick={onShowDetail}>DETAIL</GBtn>
  <GBtn title="Download this node's EPM as JSON" style="flex:1;" onclick={downloadJson}>JSON</GBtn>
  <GBtn title="Download this node's published vCard" style="flex:1;" onclick={downloadVcard}>vCARD</GBtn>
  <GBtn title="Show the compact contact card as a scannable QR code" style="flex:1;" onclick={onShowQr}>QR</GBtn>
</div>

<style>
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

  /* IRIS §7 — the design's identity name (21.5) re-snapped to the lead rung. */
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
  .cells.fill { flex: 1; justify-content: space-between; }
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
  /* Long machine values (CID) at the denser rung so a 4-column widget holds them
     in a couple of lines instead of a ragged tower. */
  .cval.small { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); }
  .cval.break { overflow-wrap: anywhere; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  /* The export's terminal button row sits on the panel floor. */
  .btnrow { display: flex; gap: var(--sdn-sp-2); margin-top: var(--sdn-sp-6); flex-wrap: wrap; }
</style>
