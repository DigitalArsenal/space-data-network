<script>
  /**
   * THE ACCOUNT MODAL (owner directive 2026-07-29, verbatim: "clicking on the
   * account button in the upper right needs to pop a modal with all the account
   * information for that account, even if it's the same as the node, since some
   * times someone who is NOT the node key will be in, and they need to edit
   * their shit").
   *
   * THE RULE THIS MODAL EXISTS TO KEEP (IRIS ruling §5): the SESSION and the
   * NODE are never conflated. Everything above the divider is the identity that
   * signed in. Node fields appear ONLY when the signed-in key is provably the
   * node's own root account — proved here by comparing the session's
   * fingerprint against the fingerprint of the xpub the node PUBLISHES in its
   * own contact card. Two empty values never "match": unverifiable is not
   * verified, so an unproven session simply renders no node fields at all.
   *
   * WHAT "EDIT THEIR SHIT" CAN REACH TODAY, HONESTLY:
   *   · An Admin session owns the operator registry, so it can find its OWN row
   *     (by fingerprint — the node never returns the session's xpub, §4/§13.1)
   *     and rename it through the same endpoint the OPERATOR KEYS modal uses.
   *   · Below Admin the node exposes NO self-service write at all — there is no
   *     PATCH /api/auth/me. That is stated in plain words instead of being
   *     mocked up with disabled controls (IRIS D4), and it is filed as a
   *     missing node surface (task sdn-operator-self-service-profile).
   * Nothing here invents a host endpoint: the connectors-only boundary holds.
   */
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import ModalShell from './ModalShell.svelte';
  import ModalSection from './ModalSection.svelte';
  import StoredWallets from './StoredWallets.svelte';
  import { apiFetch, describeApiError } from './api.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId } from './format.js';
  import { xpubFingerprint, shortFingerprint } from './wallet.js';
  import { WALLET_UI_VERSION, walletUIAvailable } from './walletui.js';
  import { canManagePermissions, buildAddUserBody } from './permissions.js';
  import { keyState, provenance, shortKey, signInsLabel } from './keystate.js';
  import { extractIdentity, parseVCard } from './vcard.js';
  import { vcardDisplayName } from './accounts.js';

  /**
   * @type {{
   *   session: any, selfNode?: any, rootAdminAvailable?: boolean|null,
   *   onSignOut: () => void, onClose: () => void
   * }}
   */
  let { session, selfNode = null, rootAdminAvailable = null, onSignOut, onClose } = $props();

  const tier = $derived(normalizeTrust(session?.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const admin = $derived(canManagePermissions(session?.trustLevel));
  const inPage = $derived(Boolean(session?.identity));
  const sessionName = $derived((session?.name ?? '').trim());

  /** The xpub the node publishes in its own card — never a key we invented. */
  const nodeXPub = $derived.by(() => {
    if (!selfNode?.vcard) return '';
    try {
      return extractIdentity(parseVCard(selfNode.vcard))?.xpub ?? '';
    } catch {
      return '';
    }
  });
  const nodeName = $derived(
    (selfNode?.dn ?? '').trim() || vcardDisplayName(selfNode?.vcard, selfNode?.peerId) || ''
  );

  let nodeFingerprint = $state('');
  let isNodeSession = $state(false);
  let myRow = $state(null);
  let lookupDone = $state(false);
  let walletStaged = $state(null);
  let error = $state('');
  let busy = $state('');
  let nameDraft = $state('');
  let saved = $state(false);

  const myKey = $derived(myRow ? keyState(myRow) : null);
  const myProv = $derived(myRow ? provenance(myRow) : null);
  const canRename = $derived(Boolean(myRow) && !myProv?.locked);

  // Prove (or fail to prove) that this session is the node's own root account.
  $effect(() => {
    const xpub = nodeXPub;
    const fp = String(session?.fingerprint ?? '').trim().toLowerCase();
    let cancelled = false;
    if (!xpub || !fp) {
      nodeFingerprint = '';
      isNodeSession = false;
      return;
    }
    xpubFingerprint(xpub).then((computed) => {
      if (cancelled) return;
      const c = String(computed ?? '').trim().toLowerCase();
      nodeFingerprint = c;
      // Both sides must exist AND agree. An empty computed value proves nothing.
      isNodeSession = Boolean(c) && c === fp;
    });
    return () => (cancelled = true);
  });

  /**
   * Find MY row in the operator registry. The node never hands the session its
   * own xpub (§13.1), so the join key is the fingerprint — the same
   * sha256(xpub)[:8] the node computes for /api/auth/me.
   */
  $effect(() => {
    if (!admin) {
      myRow = null;
      lookupDone = true;
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const rows = await apiFetch('/api/auth/users');
        const want = String(session?.fingerprint ?? '').trim().toLowerCase();
        for (const row of Array.isArray(rows) ? rows : []) {
          const fp = (await xpubFingerprint(row.xpub)).toLowerCase();
          if (fp && fp === want) {
            if (cancelled) return;
            myRow = row;
            nameDraft = (row.name ?? '').trim();
            break;
          }
        }
      } catch (err) {
        if (!cancelled) error = describeApiError(err);
      } finally {
        if (!cancelled) lookupDone = true;
      }
    })();
    return () => (cancelled = true);
  });

  $effect(() => {
    let cancelled = false;
    walletUIAvailable().then((ok) => !cancelled && (walletStaged = ok));
    return () => (cancelled = true);
  });

  async function saveName() {
    if (!canRename || busy) return;
    busy = 'name';
    error = '';
    saved = false;
    try {
      await apiFetch(`/api/auth/users/${encodeURIComponent(myRow.xpub)}`, {
        method: 'PUT',
        body: buildAddUserBody({
          xpub: myRow.xpub,
          name: nameDraft.trim(),
          trustLevel: normalizeTrust(myRow.trust_level),
        }),
      });
      myRow = { ...myRow, name: nameDraft.trim() };
      saved = true;
    } catch (err) {
      error = describeApiError(err);
    } finally {
      busy = '';
    }
  }
</script>

<ModalShell
  title={sessionName || 'unknown'}
  sub={`SIGNED IN · ${tier.toUpperCase()}`}
  label="Your account"
  unnamed={!sessionName}
  width="680px"
  {onClose}
>
  {#snippet chips()}
    <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
    {#if isNodeSession}<StatusChip label="THIS NODE'S ROOT KEY" color={theme.cyan} dot={false} />{/if}
  {/snippet}

  {#if error}
    <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
  {/if}

  <ModalSection title="THIS SESSION">
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">KEY FINGERPRINT</span>
      <span class="v mono sel" style="color:{theme.ice};">{session?.fingerprint || 'unknown'}</span>
    </div>
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">TRUST</span>
      <span class="v" style="color:{tierColor};">{tier.toUpperCase()}</span>
    </div>
    <div class="kv">
      <span class="k" style="color:{theme.textMuted};">HOW</span>
      <span class="v" style="color:{theme.textBody};">
        {inPage
          ? 'Signed in from this page — your key is still unlocked here and can sign.'
          : 'Restored from the session cookie — your key is not in this page, so it cannot sign until you sign in again.'}
      </span>
    </div>
    <div class="row">
      <GBtn title="End this session" variant="destructive" onclick={onSignOut}>SIGN OUT</GBtn>
    </div>
  </ModalSection>

  {#if isNodeSession}
    <ModalSection
      title="THIS NODE"
      note="You are signed in with this node's own root account — the key the node itself is derived from."
    >
      {#if nodeName}
        <div class="kv">
          <span class="k" style="color:{theme.textMuted};">NODE</span>
          <span class="v" style="color:{theme.textBright};">{nodeName}</span>
        </div>
      {/if}
      {#if selfNode?.peerId}
        <div class="kv">
          <span class="k" style="color:{theme.textMuted};">PEER ID</span>
          <span class="v mono sel" style="color:{theme.textDim};">{selfNode.peerId}</span>
        </div>
        <div class="kv">
          <span class="k" style="color:{theme.textMuted};">CONTACT CARD</span>
          <a class="v mono" style="color:{theme.cyan};" href={`/identity/${selfNode.peerId}.vcf`}>/identity/{shortId(selfNode.peerId)}.vcf</a>
        </div>
      {/if}
      <p class="fine" style="color:{theme.textFaint};">
        Editing what this node publishes about itself lives on the THIS NODE page, not here —
        that is the node's identity, not your account.
      </p>
    </ModalSection>
  {/if}

  <ModalSection
    title="MY ACCOUNT"
    note={admin
      ? ''
      : 'This node has no self-service profile endpoint for your tier yet, so there is nothing here you can change. An Admin can rename your entry from the ACCOUNTS page.'}
  >
    {#if !lookupDone}
      <p class="fine" style="color:{theme.textFaint};">Looking up your entry…</p>
    {:else if myRow}
      <div class="kv">
        <span class="k" style="color:{theme.textMuted};">XPUB</span>
        <span class="v mono sel" style="color:{theme.ice};" title={myRow.xpub}>{shortId(myRow.xpub)}</span>
      </div>
      <div class="row">
        <input
          type="text"
          bind:value={nameDraft}
          placeholder="your name"
          disabled={busy === 'name' || !canRename}
          aria-label="Your name"
          style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
        />
        <GBtn
          title={canRename ? 'Save your name' : 'Your entry comes from the node config file'}
          variant="primary"
          disabled={busy === 'name' || !canRename || nameDraft.trim() === (myRow.name ?? '').trim()}
          onclick={saveName}
        >{busy === 'name' ? 'SAVING…' : 'SAVE'}</GBtn>
        {#if saved}<span class="fine" style="color:{theme.green};">SAVED</span>{/if}
      </div>
      {#if !canRename}
        <p class="fine" style="color:{theme.textFaint};">{myProv.sentence}</p>
      {/if}
      <div class="state">
        <StatusChip label={myKey.label} color={theme[myKey.tone] ?? theme.textMuted} dot={false} />
        <span class="fine" style="color:{theme.textFaint};" title={myKey.contractTerm}>{signInsLabel(myRow)}</span>
        <StatusChip label={myProv.label} color={theme.textDim} dot={false} />
      </div>
      <p class="say" style="color:{theme.textBody};">{myKey.sentence}</p>
      {#if myRow.signing_pubkey_hex}
        <div class="kv">
          <span class="k" style="color:{theme.textMuted};">PINNED KEY</span>
          <span class="v mono" style="color:{theme.textDim};" title={myRow.signing_pubkey_hex}>{shortKey(myRow.signing_pubkey_hex)}</span>
        </div>
      {/if}
    {:else}
      <p class="fine" style="color:{theme.textFaint};">
        {admin
          ? 'No operator entry on this node matches your key — you are signed in through the node\'s own root account, which is derived, not enrolled.'
          : `Your key is known to this node as ${shortFingerprint(session?.fingerprint) || 'an approved operator'} at ${tier.toUpperCase()}.`}
      </p>
    {/if}
  </ModalSection>

  <ModalSection
    title="WALLET"
    note="Your wallet runs in this page and comes only from this node — never from another site."
  >
    <div class="state">
      {#if walletStaged === null}
        <StatusChip label="CHECKING WALLET" color={theme.amber} />
      {:else if walletStaged}
        <StatusChip label="WALLET SERVED BY THIS NODE" color={theme.green} dot={false} />
        <span class="fine" style="color:{theme.textFaint};">hd-wallet-ui {WALLET_UI_VERSION}</span>
      {:else}
        <StatusChip label="WALLET NOT STAGED" color={theme.red} dot={false} />
      {/if}
    </div>
    <p class="fine" style="color:{theme.textFaint};">
      Your recovery phrase or password is read by the wallet in this page, used to derive your
      key, and wiped. It is never sent anywhere, and this node never stores it.
      Remembering a wallet on this device arrives with the modern sign-in.
    </p>
    <StoredWallets />
  </ModalSection>
</ModalShell>

<style>
  .kv { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap; }
  .k { font-size: 11px; letter-spacing: 0.16em; flex: 0 0 132px; }
  .v { font-size: 13.5px; letter-spacing: 0.02em; min-width: 0; overflow-wrap: anywhere; flex: 1 1 240px; }
  .sel { user-select: all; }
  .row { display: flex; gap: 9px; align-items: center; flex-wrap: wrap; }
  .row input { flex: 1 1 200px; min-width: 0; }
  .state { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  .say { margin: 0; font-size: 13.5px; line-height: 1.6; letter-spacing: 0.02em; }
  .fine { font-size: 11.5px; letter-spacing: 0.06em; line-height: 1.55; margin: 0; }
  input {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 14px;
    letter-spacing: 0.06em;
    padding: 5px 8px;
    outline: none;
  }
  input:disabled { opacity: 0.5; }
  .err {
    border: 1px solid;
    padding: 9px 11px;
    font-size: 13px;
    letter-spacing: 0.04em;
    line-height: 1.5;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
  a { text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
