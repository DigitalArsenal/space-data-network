<script>
  /**
   * NETWORK PEER — the peers variant of the same modal (IRIS ruling §4: the
   * peers panel gets the SAME treatment in the SAME ship, because "two
   * adjacent tables with different grammars is the mess the owner named").
   *
   * Drives the same endpoints the inline select and floating REMOVE drove:
   * PUT /api/peers/<id>/trust and DELETE /api/peers/<id>.
   */
  import { untrack } from 'svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import ModalShell from './ModalShell.svelte';
  import ModalSection from './ModalSection.svelte';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';

  /**
   * @type {{
   *   peer: any, tiers: string[], busy?: boolean, error?: string,
   *   onSetTrust: (tier: string) => void, onRemove: () => void, onClose: () => void
   * }}
   */
  let { peer, tiers, busy = false, error = '', onSetTrust, onRemove, onClose } = $props();

  const tier = $derived(normalizeTrust(peer?.trust_level));
  const effective = $derived(normalizeTrust(peer?.effective_trust_level));
  const tierColor = (t) => theme[TRUST_COLOR_TOKEN[normalizeTrust(t)]] ?? theme.textMuted;
  const displayName = $derived((peer?.name ?? '').trim());
  const unnamed = $derived(!displayName);
  const tierOptions = $derived(tiers.includes(tier) ? tiers : [...tiers, tier]);

  let tierChoice = $state(untrack(() => normalizeTrust(peer?.trust_level)));
  let confirming = $state(false);
</script>

<ModalShell
  title={displayName || 'unknown'}
  sub={(peer?.organization ?? '').trim()}
  label="Network peer"
  {unnamed}
  width="640px"
  {onClose}
>
  {#snippet chips()}
    <StatusChip label={tier.toUpperCase()} color={tierColor(tier)} dot={false} />
    {#if peer?.computed_valid}
      <StatusChip label="WEB OF TRUST" color={theme.ice} dot={false} />
    {/if}
  {/snippet}

  {#if error}
    <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
  {/if}

  <ModalSection title="IDENTITY">
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">PEER ID</span>
      <span class="v mono sel" style="color:{theme.ice};">{peer?.id}</span>
    </div>
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">CONTACT CARD</span>
      <a class="v mono" style="color:{theme.cyan};" href={`/identity/${peer?.id}.vcf`}>/identity/{peer?.id}.vcf</a>
    </div>
    {#if (peer?.notes ?? '').trim()}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">NOTES</span>
        <span class="v" style="color:{theme.textBody};">{peer.notes}</span>
      </div>
    {/if}
  </ModalSection>

  <ModalSection title="TRUST">
    <div class="row">
      <select
        bind:value={tierChoice}
        disabled={busy}
        aria-label="Peer trust level"
        style="color:{tierColor(tierChoice)};border-color:{theme.hairline};"
      >
        {#each tierOptions as t (t)}
          <option value={t}>{t.toUpperCase()}</option>
        {/each}
      </select>
      <GBtn
        title="Apply this trust level"
        variant="primary"
        disabled={busy || tierChoice === tier}
        onclick={() => onSetTrust(tierChoice)}
      >{busy ? 'APPLYING…' : 'APPLY'}</GBtn>
    </div>
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">EFFECTIVE</span>
      <span class="v" style="color:{tierColor(effective)};">{effective.toUpperCase()}</span>
    </div>
    <p class="fine" style="color:{theme.textFaint};">
      EFFECTIVE is what the node actually enforces: the level you set here, raised or
      lowered by what the web of trust can prove about this peer.
    </p>
  </ModalSection>

  <ModalSection title="DANGER ZONE" danger note="Removing a peer drops its trust entry. It can dial this node again as a stranger.">
    <div class="row">
      <GBtn
        title={confirming ? 'Click again to remove this peer' : 'Remove this peer'}
        variant="destructive"
        disabled={busy}
        onclick={() => (confirming ? onRemove() : (confirming = true))}
      >{confirming ? 'CONFIRM REMOVE' : 'REMOVE PEER'}</GBtn>
      {#if confirming}
        <GBtn title="Keep this peer" onclick={() => (confirming = false)}>CANCEL</GBtn>
      {/if}
    </div>
  </ModalSection>
</ModalShell>

<style>
  .kv { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap; }
  .k { font-size: var(--sdn-fs-fine); line-height: var(--sdn-lh-fine); letter-spacing: 0.16em; flex: 0 0 118px; }
  .v { font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data); letter-spacing: 0.02em; min-width: 0; overflow-wrap: anywhere; }
  .sel { user-select: all; }
  .row { display: flex; gap: 9px; align-items: center; flex-wrap: wrap; }
  .fine { font-size: var(--sdn-fs-label); letter-spacing: 0.06em; line-height: var(--sdn-lh-label); margin: 0; }
  select {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data);
    letter-spacing: 0.06em;
    padding: 5px 8px;
    outline: none;
  }
  /* L6 (owner directive 2026-07-30: "the arrow in the drop down should have
     margin"): the tier select draws its chevron inside its own box, so the box
     reserves inset space for it. */
  select { padding-right: var(--sdn-sp-8); }
  select option { background: #0a141b; }
  select:disabled { opacity: 0.5; }
  .err {
    border: 1px solid;
    padding: 9px 11px;
    font-size: var(--sdn-fs-note);
    letter-spacing: 0.04em;
    line-height: var(--sdn-lh-note);
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
  a { text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
