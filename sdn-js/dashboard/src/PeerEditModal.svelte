<script>
  /**
   * NETWORK PEER — the peers variant of the same modal (IRIS ruling §4: the
   * peers panel gets the SAME treatment in the SAME ship, because "two
   * adjacent tables with different grammars is the mess the owner named").
   *
   * Drives the same endpoints the inline select and floating REMOVE drove:
   * PUT /api/peers/<id>/trust and DELETE /api/peers/<id>.
   *
   * EVERYTHING IN HERE SAYS WHAT STATE IT IS IN (IRIS ruling 2026-07-30, from
   * the owner's own session: he opened a NAT-hidden registry peer, chose FULL,
   * pressed APPLY, and EFFECTIVE stayed STANDARD — over a select with one
   * option, beside a primary button that could not fire, under a contact-card
   * row reading `HTTP 521`). Three rules come out of it and they are the shape
   * of this file: an armed control is only rendered when it can fire; a fact
   * this node did not measure is never asserted; and a location on a box the
   * operator may not have a shell on is never rendered as text.
   */
  import { untrack } from 'svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import ModalShell from './ModalShell.svelte';
  import ModalSection from './ModalSection.svelte';
  import { apiTextResult } from './api.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { cardState, everConnected, peerDisplayName, pinIsLocked } from './peers.js';
  import { trustControlState } from './permissions.js';
  import { vcardDisplayName } from './accounts.js';

  /**
   * `pin` is this peer's record in the pin registry, or null when it is not
   * pinned; `pinsKnown` is false while the node has not answered for its pins.
   * The distinction is the point — "not pinned" and "we do not know" have
   * opposite consequences for whether this peer stays in the table, and a modal
   * that guessed would be the third surface today to state a peer fact it had
   * not read.
   *
   * `tiersKnown` is that same idiom for the SESSION: `tiers` is `[]` both while
   * /api/auth/me is in flight and when the answer was "below Admin", and those
   * two are different sentences. `node` is this peer's row in the status feed
   * when it has one — the only place the two facts the contact card turns on
   * (ever seen, online now) exist.
   *
   * @type {{
   *   peer: any, pin?: any, pinsKnown?: boolean, node?: any, tiers: string[],
   *   tiersKnown?: boolean, sessionTier?: string, hasSession?: boolean,
   *   rootAdminAvailable?: boolean|null, busy?: boolean, error?: string,
   *   onSetTrust: (tier: string) => void, onRemove: () => void,
   *   onRequestSignIn?: () => void, onClose: () => void
   * }}
   */
  let {
    peer,
    pin = null,
    pinsKnown = false,
    node = null,
    tiers,
    tiersKnown = false,
    sessionTier = '',
    hasSession = false,
    rootAdminAvailable = null,
    busy = false,
    error = '',
    onSetTrust,
    onRemove,
    onRequestSignIn,
    onClose,
  } = $props();

  const pinned = $derived(Boolean(pin));
  const pinLocked = $derived(pinned && pinIsLocked(pin));
  /**
   * LISTING — the answer to "why is this peer in the table, or why is it not".
   * Pins are added and removed in the PINNED PEERS panel; this modal states the
   * fact so the two surfaces can never disagree, and does not grow a second
   * control for one action.
   */
  const listing = $derived.by(() => {
    if (!pinsKnown) return { label: 'NOT READ', sentence: '', tone: 'textMuted' };
    if (pinLocked) {
      return {
        label: 'PINNED BY CONFIG FILE',
        sentence: "Listed whether or not it is connected. The pin lives in this node's config file and can only be removed there.",
        tone: 'textDim',
      };
    }
    if (pinned) {
      return {
        label: 'PINNED',
        sentence: 'Listed whether or not it is connected.',
        tone: 'ice',
      };
    }
    return {
      label: 'NOT PINNED',
      sentence: 'Listed only while it is connected. It disappears from the peers table when it drops off the network.',
      tone: 'textMuted',
    };
  });

  const tier = $derived(normalizeTrust(peer?.trust_level));
  const effective = $derived(normalizeTrust(peer?.effective_trust_level));
  const tierColor = (t) => theme[TRUST_COLOR_TOKEN[normalizeTrust(t)]] ?? theme.textMuted;

  /* ---------------------------------------------------------------------
   * THE CONTACT CARD, READ RATHER THAN LINKED (IRIS §3).
   *
   * `/identity/<id>.vcf` is served by THE PEER THAT OWNS IT. For a peer this
   * node has never connected to there is no card to serve, and the previous
   * markup offered a link into that hole, then printed the transport's answer
   * (`HTTP 521`) as if it were the explanation. This reads it once and states
   * the two facts the node DID measure. It never asserts NAT, a firewall, or
   * unreachability — this node cannot measure any of them.
   * --------------------------------------------------------------------- */
  let card = $state({ read: 'idle', ok: false, status: 0, threw: false, fn: '' });
  let cardDetail = $state(false);
  $effect(() => {
    const id = String(peer?.id ?? '').trim();
    if (!id) return;
    let cancelled = false;
    card = { read: 'reading', ok: false, status: 0, threw: false, fn: '' };
    cardDetail = false;
    (async () => {
      const res = await apiTextResult(`/identity/${encodeURIComponent(id)}.vcf`);
      if (cancelled) return;
      card = {
        read: 'done',
        ok: res.ok && Boolean(res.text.trim()),
        status: res.status,
        threw: res.threw,
        // The card's own FN, through the ACCOUNTS name rule so a placeholder
        // card ("<peer.ID 16*abcd>") or an FN that is the id verbatim is
        // refused rather than promoted to a name (§16.4.3).
        fn: res.ok ? vcardDisplayName(res.text, id) : '',
      };
    })();
    return () => (cancelled = true);
  });

  const cardHref = $derived(`/identity/${peer?.id}.vcf`);
  const cardStatus = $derived(
    cardState({ read: card.read, ok: card.ok, everConnected: everConnected(node) })
  );
  const cardFailed = $derived(cardStatus === 'not-served-here' || cardStatus === 'did-not-load');
  const cardSentence = $derived(
    cardStatus === 'not-served-here'
      ? 'A contact card is served by the peer that owns it. This node has never connected to this peer, so it has no card to serve.'
      : 'This node has connected to this peer before. The card did not come back this time.'
  );
  // The code has exactly ONE place it may appear, and this is it: behind an
  // explicit DETAIL toggle, never as the row's value and never in a `title=`
  // (a tooltip is a hidden place, not an affordance).
  const cardCode = $derived(card.threw ? 'network error' : `HTTP ${card.status}`);

  /* ---------------------------------------------------------------------
   * THE NAME LADDER (IRIS §4). `$derived`, never `$state`: a name that arrives
   * with the card read supersedes without a refresh, which IS the
   * refresh-when-reachable behaviour.
   * --------------------------------------------------------------------- */
  const naming = $derived(peerDisplayName({ peer, cardFN: card.fn, pin }));
  const operatorLabel = $derived(String(pin?.name ?? '').trim());
  /** The peer published a name AND an operator typed a different one: both, never merged. */
  const showOperatorLabelRow = $derived(
    naming.origin === 'peer' && Boolean(operatorLabel) && operatorLabel !== naming.name
  );

  /* ---------------------------------------------------------------------
   * TRUST. `canSetTrust` is the whole of item (2): the select and APPLY exist
   * only in the state where pressing APPLY changes something.
   * --------------------------------------------------------------------- */
  const trustControl = $derived(
    trustControlState({ hasSession, tiersKnown, tierCount: (tiers ?? []).length })
  );
  const canSetTrust = $derived(trustControl === 'armed');
  const sessionTierLabel = $derived(normalizeTrust(sessionTier).toUpperCase());
  const trustNote = $derived.by(() => {
    if (trustControl === 'armed') return '';
    if (trustControl === 'loading') return 'Checking what this session may change…';
    if (trustControl === 'needs-signin') {
      return 'Not signed in. Peer trust is set by an Admin operator on this node.';
    }
    // The root-recovery-phrase clause ONLY when the node said so (contract
    // §14.1/§14.4.2): `null` is "not asked yet", and claiming a sign-in path
    // this node may not have is the same class of defect as the button was.
    return rootAdminAvailable === true
      ? `Your session is ${sessionTierLabel}. Setting peer trust is an Admin action. Sign in as an Admin operator, or with this node's root recovery phrase.`
      : `Your session is ${sessionTierLabel}. Setting peer trust is an Admin action. An Admin operator must be enrolled on this node before it can be changed.`;
  });

  // Only reachable while the control is armed, i.e. with two or more tiers to
  // choose from; the union keeps a tier the peer already holds but this session
  // may not grant visible in the list rather than silently re-labelling it.
  const tierOptions = $derived(tiers.includes(tier) ? tiers : [...tiers, tier]);

  let tierChoice = $state(untrack(() => normalizeTrust(peer?.trust_level)));
  let confirming = $state(false);

  /* ---------------------------------------------------------------------
   * THE CONFIG LOCATION GOES TO THE CLIPBOARD, NEVER TO THE SCREEN (IRIS §6,
   * revoking the earlier parking ruling). The owner named this exact string
   * twice. `navigator.clipboard` is same-origin and costs zero external bytes.
   * --------------------------------------------------------------------- */
  const COPY_IDLE = 'COPY CONFIG LOCATION';
  let copyLabel = $state(COPY_IDLE);
  let copyTimer;
  async function copyConfigLocation() {
    const declaredAt = String(pin?.note ?? '').trim();
    if (!declaredAt) return;
    try {
      await navigator.clipboard.writeText(declaredAt);
      copyLabel = 'COPIED';
    } catch {
      copyLabel = 'COPY FAILED';
    }
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => (copyLabel = COPY_IDLE), 1500);
  }
  const canCopyConfigLocation = $derived(pinLocked && Boolean(String(pin?.note ?? '').trim()));
</script>

<ModalShell
  title={naming.name}
  sub={(peer?.organization ?? '').trim()}
  label="Network peer"
  unnamed={naming.origin === 'none'}
  width="640px"
  {onClose}
>
  {#snippet chips()}
    <StatusChip label={tier.toUpperCase()} color={tierColor(tier)} dot={false} />
    {#if naming.origin === 'operator'}
      <StatusChip label="OPERATOR LABEL" color={theme.textMuted} dot={false} />
    {/if}
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
    {#if naming.origin === 'operator'}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">NAME</span>
        <span class="v" style="color:{theme.textBody};">{naming.name}</span>
      </div>
      <p class="fine" style="color:{theme.textFaint};">
        Typed by an operator on this node. This peer has not published a name.
      </p>
    {:else if showOperatorLabelRow}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">OPERATOR LABEL</span>
        <span class="v" style="color:{theme.textBody};">{operatorLabel}</span>
      </div>
    {/if}
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">CONTACT CARD</span>
      {#if cardStatus === 'ok'}
        <a class="v mono" style="color:{theme.cyan};" href={cardHref}>{cardHref}</a>
      {:else if cardStatus === 'reading'}
        <span class="v" style="color:{theme.textFaint};">READING…</span>
      {:else if cardStatus === 'not-read'}
        <span class="v" style="color:{theme.textMuted};">NOT READ</span>
      {:else}
        <span class="v" style="color:{cardStatus === 'did-not-load' ? theme.amber : theme.textMuted};">
          {cardStatus === 'did-not-load' ? 'DID NOT LOAD' : 'NOT SERVED HERE'}
        </span>
        <GBtn
          title={cardDetail ? 'Hide what the read returned' : 'Show what the read returned'}
          onclick={() => (cardDetail = !cardDetail)}
        >DETAIL</GBtn>
      {/if}
    </div>
    {#if cardFailed}
      <p class="fine" style="color:{theme.textFaint};">{cardSentence}</p>
      {#if cardDetail}
        <p class="fine mono" style="color:{theme.textMuted};">{cardCode}</p>
      {/if}
    {/if}
    {#if (peer?.notes ?? '').trim()}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">NOTES</span>
        <span class="v" style="color:{theme.textBody};">{peer.notes}</span>
      </div>
    {/if}
    {#if pinsKnown}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">LISTING</span>
        <span class="v" style="color:{theme[listing.tone] ?? theme.textDim};">{listing.label}</span>
      </div>
      <p class="fine" style="color:{theme.textFaint};">{listing.sentence}</p>
      {#if canCopyConfigLocation}
        <div class="row">
          <GBtn
            title="Copy the config file and key this pin is declared in"
            onclick={copyConfigLocation}
          >{copyLabel}</GBtn>
        </div>
      {/if}
    {/if}
  </ModalSection>

  <ModalSection title="TRUST" note={trustNote}>
    {#if canSetTrust}
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
    {:else}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">ASSIGNED</span>
        <span class="v" style="color:{tierColor(tier)};">{tier.toUpperCase()}</span>
      </div>
      {#if trustControl === 'needs-signin'}
        <div class="row">
          <GBtn title="Sign in with your wallet key" onclick={() => onRequestSignIn?.()}>SIGN IN</GBtn>
        </div>
      {:else if trustControl === 'needs-admin' && rootAdminAvailable === true}
        <div class="row">
          <GBtn title="Sign in with an Admin key" onclick={() => onRequestSignIn?.()}>SIGN IN AS ADMIN</GBtn>
        </div>
      {/if}
    {/if}
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">EFFECTIVE</span>
      <span class="v" style="color:{tierColor(effective)};">{effective.toUpperCase()}</span>
    </div>
    <p class="fine" style="color:{theme.textFaint};">
      EFFECTIVE is what the node actually enforces: the level you set here, raised or
      lowered by what the web of trust can prove about this peer.
    </p>
  </ModalSection>

  <ModalSection
    title="DANGER ZONE"
    danger
    note={pinned
      ? 'Removing a peer drops its trust entry. It can dial this node again as a stranger. It stays PINNED, so it stays in the peers table — unpin it separately.'
      : 'Removing a peer drops its trust entry. It can dial this node again as a stranger.'}
  >
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
