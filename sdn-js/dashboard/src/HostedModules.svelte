<script>
  /**
   * WHAT THIS NODE HOSTS — the peer's own signed $PMM, read from the peer.
   *
   * Owner directive 2026-07-30: "clicking on a node shows what's being hosted on
   * that node in terms of modules, both data and functionality."
   *
   * Every rule this view obeys lives in pmm.js, which is pure and tested: where
   * to ask, what may be believed, and how the offering is grouped. This file is
   * only the rendering — and its one job beyond that is to render NOTHING when
   * the manifest does not verify (Iris 2026-07-31, /beta's precedent): a greyed
   * list of module names still reads as "what this provider offers".
   *
   * Styled with theme.js tokens only.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { shortId } from './format.js';
  // The node dashboard's byte formatter — the same words for the same
  // magnitudes, so an artifact size here reads like a store size there.
  import { formatBytes } from './runtime.js';
  import { multiaddrsFromVCard } from './peers.js';
  import {
    fetchManifest,
    hostedState,
    manifestOrigin,
    manifestURL,
    offeringSections,
    verifyManifest,
    accessLabel,
    entryStateLabel,
    GROUPING_UNSIGNED_NOTE,
  } from './pmm.js';

  /**
   * @type {{
   *   node: any,
   *   selfOrigin?: string,
   *   fetchImpl?: typeof fetch,
   *   subtle?: SubtleCrypto|null,
   * }}
   */
  let { node, selfOrigin = '', fetchImpl = undefined, subtle = undefined } = $props();

  /**
   * WHERE WE ASK. This node's own manifest comes from this node's own origin —
   * the page is already served by it, and asking it for its own multiaddrs to
   * rediscover an address we are literally connected to would be a worse answer
   * than the one in the address bar.
   */
  const origin = $derived(
    node?.isSelf
      ? selfOrigin || globalThis.location?.origin || ''
      : manifestOrigin({ addrs: node?.addrs ?? [], vcardAddrs: multiaddrsFromVCard(node?.vcard ?? '') })
  );
  const url = $derived(
    node?.isSelf
      ? `${origin}/.well-known/sdn/modules.pmm`
      : manifestURL({ addrs: node?.addrs ?? [], vcardAddrs: multiaddrsFromVCard(node?.vcard ?? '') })
  );

  let phase = $state('idle');
  let fetchReason = $state('');
  let status = $state(0);
  let verifyReason = $state('');
  let manifest = $state(null);
  let codeOpen = $state(false);

  /**
   * ONE READ PER PEER, and a late answer for a peer the operator has already
   * navigated away from is DROPPED rather than rendered under the new title.
   * `token` is the guard; an AbortController would leave the same race in the
   * verify step, which is async too.
   */
  let token = 0;
  $effect(() => {
    const peerId = String(node?.peerId ?? '');
    const at = origin;
    const mine = ++token;
    phase = 'idle';
    fetchReason = '';
    status = 0;
    verifyReason = '';
    manifest = null;
    codeOpen = false;
    if (!at || !peerId) return;
    phase = 'reading';
    (async () => {
      const read = await fetchManifest(at, fetchImpl ? { fetchImpl } : {});
      if (mine !== token) return;
      if (!read.ok) {
        fetchReason = read.reason;
        status = read.status ?? 0;
        phase = 'done';
        return;
      }
      const verdict = await verifyManifest(read.manifest, {
        servedFrom: read.servedFrom,
        ...(subtle === undefined ? {} : { subtle }),
      });
      if (mine !== token) return;
      if (!verdict.ok) {
        verifyReason = verdict.reason;
        phase = 'done';
        return;
      }
      manifest = read.manifest;
      phase = 'done';
    })().catch(() => {
      if (mine !== token) return;
      // fetchManifest and verifyManifest both answer with results rather than
      // throwing; this is the belt for anything neither anticipated, and it is
      // still a STATE rather than a stack trace on the operator's screen.
      fetchReason = 'unreachable';
      phase = 'done';
    });
  });

  const sections = $derived(offeringSections(manifest?.MODULES ?? []));
  const count = $derived(manifest?.MODULES?.length ?? 0);
  const state = $derived(hostedState({ phase, origin, fetchReason, status, verifyReason, count }));
</script>

<div class="hosted">
  <div class="head">
    <div class="k" style="color:{theme.textMuted};">HOSTED MODULES</div>
    {#if state.id === 'ok'}
      <div class="chips">
        <StatusChip label="MANIFEST VERIFIED" color={theme.green} dot={false} />
        <StatusChip label={`${count} MODULES`} color={theme.ice} dot={false} />
      </div>
    {/if}
  </div>

  <!-- WHERE THIS CAME FROM, always visible when we have an address to name: the
       operator can paste it, and it is the one fact that makes "verified" mean
       something — the signature is checked against the domain that served it. -->
  {#if url}
    <div class="src mono" style="color:{theme.textFaint};">{url}</div>
  {/if}

  {#if state.id !== 'ok'}
    <div class="state" style="border-color:{state.id === 'unverified' ? theme.amber : theme.hairline};">
      {#if state.title}
        <div class="stitle" style="color:{state.id === 'unverified' ? theme.amber : theme.textDim};">{state.title}</div>
      {/if}
      {#if state.detail}
        <div class="sdetail" style="color:{theme.textDim};">{state.detail}</div>
      {/if}
      {#if state.code}
        <!-- The raw code is EVIDENCE, not the answer (the `HTTP 521` correction,
             owner 2026-07-30). It lives behind an explicit toggle. -->
        <button
          class="act"
          style="color:{theme.ice};border-color:{theme.hairline};"
          onclick={() => (codeOpen = !codeOpen)}
          aria-expanded={codeOpen}
        >{codeOpen ? '▾ HIDE DETAIL' : '▸ DETAIL'}</button>
        {#if codeOpen}
          <div class="code mono" style="color:{theme.textFaint};">{state.code}</div>
        {/if}
      {/if}
    </div>
  {:else}
    {#if manifest?.PROVIDER_NAME || manifest?.PROVIDER_DOMAIN}
      <div class="prov" style="color:{theme.textBody};">
        {manifest.PROVIDER_NAME || manifest.PROVIDER_DOMAIN}
        {#if manifest.PROVIDER_NAME && manifest.PROVIDER_DOMAIN}
          <span style="color:{theme.textFaint};"> · {manifest.PROVIDER_DOMAIN}</span>
        {/if}
      </div>
    {/if}

    {#each sections as section (section.id)}
      <div class="section" style="border-color:{theme.divider};">
        <div class="shead">
          <span class="slabel" style="color:{theme.cyan};">{section.label}</span>
          <span class="scount" style="color:{theme.textFaint};">{section.count}</span>
        </div>
        {#each section.families as family (family.label)}
          <div class="family">
            <div class="flabel" style="color:{theme.textMuted};">{family.label}</div>
            <ul class="mods" style="border-color:{theme.hairline};">
              {#each family.entries as entry (entry.MODULE_ID)}
                <li style="border-color:{theme.divider};">
                  <div class="mline">
                    <span class="mname" style="color:{theme.textBright};">{entry.NAME?.trim() || entry.MODULE_ID}</span>
                    <span class="mver mono" style="color:{theme.ice};">{entry.VERSION || '—'}</span>
                    {#if entryStateLabel(entry.ENTRY_STATE)}
                      <span class="badge" style="color:{theme.amber};border-color:{theme.amber};">{entryStateLabel(entry.ENTRY_STATE)}</span>
                    {/if}
                    {#if accessLabel(entry.ACCESS_POLICY)}
                      <span class="badge" style="color:{theme.textDim};border-color:{theme.hairline};">{accessLabel(entry.ACCESS_POLICY)}</span>
                    {/if}
                  </div>
                  <div class="mmeta mono" style="color:{theme.textFaint};">
                    {entry.MODULE_ID}
                    {#if entry.CONTENT_HASH}<span class="sep"> · </span>{shortId(entry.CONTENT_HASH)}{/if}
                    {#if Number(entry.ARTIFACT_SIZE_BYTES) > 0}<span class="sep"> · </span>{formatBytes(Number(entry.ARTIFACT_SIZE_BYTES))}{/if}
                  </div>
                  {#if entry.DESCRIPTION?.trim()}
                    <div class="mdesc" style="color:{theme.textDim};">{entry.DESCRIPTION}</div>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/each}
      </div>
    {/each}

    <!-- THE ONE THING THE SIGNATURE DOES NOT COVER, said where the headings it
         does not cover are (Themis 2026-07-31). -->
    <div class="note" style="color:{theme.textFaint};border-color:{theme.divider};">{GROUPING_UNSIGNED_NOTE}</div>
  {/if}
</div>

<style>
  .hosted { min-width: 0; }
  .head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
  .chips { display: flex; gap: 6px; flex-wrap: wrap; }
  .k { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); letter-spacing: 0.18em; }
  .src { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); overflow-wrap: anywhere; margin-top: 4px; }
  .state { border: 1px solid; padding: 11px 12px; margin-top: 9px; }
  .stitle { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); letter-spacing: 0.14em; }
  .sdetail { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); margin-top: 5px; }
  .code { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); margin-top: 6px; }
  .act {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.12em;
    padding: 3px 9px;
    margin-top: 8px;
  }
  .prov { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); margin-top: 8px; }
  .section { border-top: 1px solid; margin-top: 11px; padding-top: 9px; }
  .shead { display: flex; align-items: baseline; gap: 8px; }
  .slabel { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); letter-spacing: 0.18em; }
  .scount { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); }
  .family { margin-top: 8px; }
  .flabel { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.16em; }
  .mods { list-style: none; margin: 5px 0 0; padding: 0; border: 1px solid; }
  .mods li { padding: 7px 9px; border-bottom: 1px solid; }
  .mods li:last-child { border-bottom: 0; }
  .mline { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .mname { font-family: 'Chakra Petch', sans-serif; font-weight: 600; font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note); letter-spacing: 0.04em; }
  .mver { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); }
  .badge {
    border: 1px solid;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
    padding: 1px 5px;
  }
  .mmeta { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); margin-top: 3px; overflow-wrap: anywhere; }
  .mdesc { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); margin-top: 3px; }
  .note { border-top: 1px solid; margin-top: 12px; padding-top: 8px; font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
