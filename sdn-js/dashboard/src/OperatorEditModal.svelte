<script>
  /**
   * OPERATOR KEY — the one place management happens (owner directive
   * 2026-07-29: "there should be an 'edit' button, which should pop a modal do
   * to management tasks").
   *
   * Every affordance the table used to scatter across its row lives here, in
   * IRIS's ruled order: identity → name → trust → key → provenance → danger.
   * Nothing here is new wire: it drives PUT/DELETE /api/auth/users/<xpub>,
   * exactly what the inline select and the floating REMOVE drove before.
   *
   * The key-state and provenance SENTENCES are visible text, not tooltips
   * (IRIS D5) — they are the answer to "I have no idea what awaiting proof is".
   */
  import { untrack } from 'svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import ModalShell from './ModalShell.svelte';
  import ModalSection from './ModalSection.svelte';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { keyState, provenance, shortKey, signInsLabel, peerIdCell } from './keystate.js';
  import { trustControlState } from './permissions.js';

  /**
   * `tiersKnown`, `sessionTier`, `hasSession` and `rootAdminAvailable` are the
   * same four the peer modal takes, for the same reason (IRIS ruling
   * 2026-07-30 §2): `assignableUserTiers` returns `[]` both while
   * /api/auth/me is in flight and when the answer was "below Admin", and the
   * old markup padded that empty list with the row's CURRENT tier — a
   * one-option select beside a primary APPLY that could not fire.
   *
   * @type {{
   *   user: any, peerCell: any, tiers: string[], tiersKnown?: boolean,
   *   sessionTier?: string, hasSession?: boolean, rootAdminAvailable?: boolean|null,
   *   busy?: boolean, error?: string,
   *   onSetTrust: (tier: string) => void, onRename: (name: string) => void,
   *   onRemove: () => void, onRequestSignIn?: () => void, onClose: () => void
   * }}
   */
  let {
    user,
    peerCell,
    tiers,
    tiersKnown = false,
    sessionTier = '',
    hasSession = false,
    rootAdminAvailable = null,
    busy = false,
    error = '',
    onSetTrust,
    onRename,
    onRemove,
    onRequestSignIn,
    onClose,
  } = $props();

  const tier = $derived(normalizeTrust(user?.trust_level));
  const tierColor = (t) => theme[TRUST_COLOR_TOKEN[normalizeTrust(t)]] ?? theme.textMuted;
  const key = $derived(keyState(user));
  const prov = $derived(provenance(user));
  // The table already ran (or failed) the derivation; re-deriving here would
  // reopen the em-dash bug from the other side, so the resolved cell is passed
  // in rather than recomputed.
  const cell = $derived(peerCell ?? peerIdCell('', user?.xpub));
  const displayName = $derived((user?.name ?? '').trim());
  const unnamed = $derived(!displayName);

  // A config row is not editable HERE and the reason is stated, never a bare
  // disabled control (IRIS D4).
  const locked = $derived(prov.locked);

  let tierChoice = $state(untrack(() => normalizeTrust(user?.trust_level)));
  let nameDraft = $state(untrack(() => (user?.name ?? '').trim()));
  let confirming = $state(false);

  /**
   * The trust control, on the same law as the peer modal's. `locked` is a
   * SEPARATE reason and keeps its own sentence: a config key is not editable
   * here however privileged the session is.
   */
  const trustControl = $derived(
    trustControlState({ hasSession, tiersKnown, tierCount: (tiers ?? []).length })
  );
  const canSetTrust = $derived(trustControl === 'armed' && !locked);
  const sessionTierLabel = $derived(normalizeTrust(sessionTier).toUpperCase());
  const trustNote = $derived.by(() => {
    if (locked) return 'Trust for a config key is set in the config file too.';
    if (trustControl === 'armed') return '';
    if (trustControl === 'loading') return 'Checking what this session may change…';
    if (trustControl === 'needs-signin') {
      return 'Not signed in. Operator trust is set by an Admin operator on this node.';
    }
    // §14.1/§14.4.2: name the root recovery phrase ONLY when the node said it
    // is a sign-in path. `null` is "not asked yet", never a claim.
    return rootAdminAvailable === true
      ? `Your session is ${sessionTierLabel}. Setting operator trust is an Admin action. Sign in as an Admin operator, or with this node's root recovery phrase.`
      : `Your session is ${sessionTierLabel}. Setting operator trust is an Admin action. An Admin operator must be enrolled on this node before it can be changed.`;
  });

  // Only reachable while the control is armed; the union keeps a tier this
  // session may not grant visible rather than silently re-labelling the row.
  const tierOptions = $derived(
    tiers.includes(tier) ? tiers : [...tiers, tier]
  );
  const lastSignIn = $derived(user?.last_login ? new Date(user.last_login).toISOString().replace('T', ' ').slice(0, 19) + 'Z' : '');
  const added = $derived(user?.created_at ? new Date(user.created_at).toISOString().slice(0, 10) : '');
</script>

<ModalShell
  title={displayName || 'unnamed'}
  sub={(user?.organization ?? '').trim()}
  label="Operator key"
  {unnamed}
  width="640px"
  {onClose}
>
  {#snippet chips()}
    <StatusChip label={tier.toUpperCase()} color={tierColor(tier)} dot={false} />
    <StatusChip label={key.label} color={theme[key.tone] ?? theme.textMuted} dot={false} />
  {/snippet}

  {#if error}
    <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
  {/if}

  <ModalSection title="IDENTITY">
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">XPUB</span>
      <span class="v mono sel" style="color:{theme.ice};">{user?.xpub}</span>
    </div>
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">PEER ID</span>
      <span class="v mono sel" style="color:{cell.id ? theme.textBody : theme.textFaint};">{cell.label}</span>
    </div>
    {#if cell.id}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">CONTACT CARD</span>
        <a class="v mono" style="color:{theme.cyan};" href={`/identity/${cell.id}.vcf`}>/identity/{cell.id}.vcf</a>
      </div>
    {:else if cell.pending}
      <p class="fine" style="color:{theme.textFaint};">
        The peer id is derived from the xpub in this page — the node does not store one.
      </p>
    {/if}
  </ModalSection>

  <ModalSection title="NAME &amp; ORGANIZATION" note={locked ? prov.sentence : ''}>
    <div class="row">
      <input
        type="text"
        bind:value={nameDraft}
        placeholder="name"
        disabled={busy || locked}
        aria-label="Operator name"
        style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
      />
      <GBtn
        title={locked ? 'This key is defined in the config file' : 'Save this name'}
        variant="primary"
        disabled={busy || locked || nameDraft.trim() === displayName}
        onclick={() => onRename(nameDraft.trim())}
      >{busy ? 'SAVING…' : 'SAVE'}</GBtn>
    </div>
    {#if (user?.organization ?? '').trim()}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">ORGANIZATION</span>
        <span class="v" style="color:{theme.textBody};">{user.organization}</span>
      </div>
    {/if}
  </ModalSection>

  <ModalSection title="TRUST" note={trustNote}>
    {#if canSetTrust}
      <div class="row">
        <select
          bind:value={tierChoice}
          disabled={busy}
          aria-label="Trust level"
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
      <p class="fine" style="color:{theme.textFaint};">
        You can grant up to your own tier, capped at ADMIN — ULTIMATE means "this identity IS
        this node" and is never granted here.
      </p>
    {:else}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">ASSIGNED</span>
        <span class="v" style="color:{tierColor(tier)};">{tier.toUpperCase()}</span>
      </div>
      {#if !locked && trustControl === 'needs-signin'}
        <div class="row">
          <GBtn title="Sign in with your wallet key" onclick={() => onRequestSignIn?.()}>SIGN IN</GBtn>
        </div>
      {:else if !locked && trustControl === 'needs-admin' && rootAdminAvailable === true}
        <div class="row">
          <GBtn title="Sign in with an Admin key" onclick={() => onRequestSignIn?.()}>SIGN IN AS ADMIN</GBtn>
        </div>
      {/if}
    {/if}
  </ModalSection>

  <ModalSection title="SIGNING KEY">
    <div class="state">
      <StatusChip label={key.label} color={theme[key.tone] ?? theme.textMuted} dot={false} />
      <span class="fine" style="color:{theme.textFaint};" title={key.contractTerm}>{signInsLabel(user)}</span>
    </div>
    <p class="say" style="color:{theme.textBody};">{key.sentence}</p>
    {#if user?.signing_pubkey_hex}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">PINNED KEY</span>
        <span class="v mono" style="color:{theme.textDim};" title={user.signing_pubkey_hex}>{shortKey(user.signing_pubkey_hex)}</span>
      </div>
    {/if}
    {#if lastSignIn}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">LAST SIGN-IN</span>
        <span class="v mono" style="color:{theme.textDim};">{lastSignIn}</span>
      </div>
    {/if}
  </ModalSection>

  <ModalSection title="WHERE THIS KEY CAME FROM">
    <div class="state">
      <StatusChip label={prov.label} color={theme.textDim} dot={false} />
      {#if added}<span class="fine" style="color:{theme.textFaint};">ADDED {added}</span>{/if}
    </div>
    <p class="say" style="color:{theme.textBody};">{prov.sentence}</p>
  </ModalSection>

  <ModalSection title="DANGER ZONE" danger note={locked
    ? 'A config key cannot be removed from this page — delete its entry from the node config and restart.'
    : 'Removing a key ends its access immediately. Its sign-in history goes with it.'}>
    <div class="row">
      <GBtn
        title={locked ? 'This key is defined in the config file' : confirming ? 'Click again to remove this key' : 'Remove this operator key'}
        variant="destructive"
        disabled={busy || locked}
        onclick={() => (confirming ? onRemove() : (confirming = true))}
      >{confirming ? 'CONFIRM REMOVE' : 'REMOVE KEY'}</GBtn>
      {#if confirming}
        <GBtn title="Keep this key" onclick={() => (confirming = false)}>CANCEL</GBtn>
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
  .row input { flex: 1 1 200px; min-width: 0; }
  .state { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  .say { margin: 0; font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data); letter-spacing: 0.02em; }
  .fine { font-size: var(--sdn-fs-label); letter-spacing: 0.1em; line-height: var(--sdn-lh-label); }
  select,
  input {
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
  select:disabled,
  input:disabled { opacity: 0.5; }
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
