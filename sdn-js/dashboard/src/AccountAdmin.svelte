<script>
  /**
   * ACCOUNT MANAGEMENT — folded into the ACCOUNTS page (contract §16.3/§16.4).
   *
   * The PERMISSIONS page is gone; these affordances moved here unchanged and
   * still call the EXISTING APIs, which are gated by PATH and so need nothing
   * from the UI beyond an Admin session. Rendered only for an Admin session —
   * the anonymous tier of the ACCOUNTS page is the public table itself (§16.5).
   *
   * At Admin+ it manages both registries the node actually has:
   *   · OPERATOR KEYS  — the xpub user store (/api/auth/users). Adding an
   *     xpub here IS "approved by an already approved key"; the holder's
   *     first successful sign-in binds the signing key (TOFU). There is no
   *     attest affordance: §13.2 rules /api/auth/attest CLI/API-only, since
   *     hd-wallet-ui cannot sign a variable-length §7 preimage and keeps no
   *     signer alive after a login operation.
   *   · NETWORK PEERS  — the libp2p trust registry (/api/peers). NOTE this
   *     is a different endpoint from the anonymous /api/v1/peers swarm read.
   *
   * `ultimate` is never offered: it means "this identity IS this node".
   * Styled only with theme.js tokens + design components.
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import Kicker from 'spaceaware-student-sdn/src/lib/components/Kicker.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { apiFetch, apiPostText, describeApiError } from './api.js';
  import { normalizeTrust, trustRank, TRUST_COLOR_TOKEN, TRUST_TIERS } from './trust.js';
  import { shortId } from './format.js';
  import { peerIdFromXpub, shortFingerprint } from './wallet.js';
  import {
    canManagePermissions,
    assignableUserTiers,
    assignablePeerTiers,
    tiersHighestFirst,
    classifyPeerInput,
    peerFromVCardText,
    buildAddPeerBody,
    buildAddUserBody,
    userNeedsKeyProof,
  } from './permissions.js';

  /** @type {{ session: any, nodes: any[], rootAdminAvailable?: boolean|null, onRequestSignIn: () => void }} */
  let { session, nodes, rootAdminAvailable = null, onRequestSignIn } = $props();

  const level = $derived(session?.trustLevel ?? '');
  const admin = $derived(canManagePermissions(level));
  const userTiers = $derived(tiersHighestFirst(assignableUserTiers(level)));
  const peerTiers = $derived(tiersHighestFirst(assignablePeerTiers(level)));
  const tierColor = (t) => theme[TRUST_COLOR_TOKEN[normalizeTrust(t)]] ?? theme.textMuted;

  /** Read-only summary from the public feed: how many peers at each tier. */
  const tierCounts = $derived.by(() => {
    const counts = new Map(TRUST_TIERS.map((t) => [t, 0]));
    for (const n of nodes ?? []) {
      if (n.isSelf) continue;
      const t = normalizeTrust(n.trustLevel);
      counts.set(t, (counts.get(t) ?? 0) + 1);
    }
    return counts;
  });

  let users = $state([]);
  let peers = $state([]);
  let loaded = $state(false);
  let error = $state('');
  let busy = $state('');

  // --- add forms (start empty; nothing is ever prefilled for the operator) ---
  let userInput = $state('');
  let userName = $state('');
  let userTier = $state('standard');
  let peerInput = $state('');
  let peerName = $state('');
  let peerOrg = $state('');
  let peerNotes = $state('');
  let peerTier = $state('standard');

  const peerKind = $derived(classifyPeerInput(peerInput).kind);
  const userKind = $derived(classifyPeerInput(userInput).kind);

  const PEER_KIND_LABEL = {
    empty: '',
    vcard: 'VCARD',
    xpub: 'XPUB → PEER ID',
    peer_id: 'PEER ID',
    public_key: 'PUBLIC KEY',
    unknown: 'UNRECOGNIZED',
  };

  async function refresh() {
    error = '';
    try {
      const [u, p] = await Promise.all([apiFetch('/api/auth/users'), apiFetch('/api/peers')]);
      users = Array.isArray(u) ? u : [];
      peers = Array.isArray(p) ? p : [];
    } catch (err) {
      error = describeApiError(err);
    } finally {
      loaded = true;
    }
  }

  // Load once per admin session. `requested` is a plain flag, NOT $state:
  // an effect that both reads and writes its own reactive guard re-runs
  // forever in Svelte 5.
  let requested = false;
  $effect(() => {
    if (admin) {
      if (!requested) {
        requested = true;
        refresh();
      }
    } else {
      requested = false;
      users = [];
      peers = [];
      loaded = false;
    }
  });

  async function run(key, fn) {
    if (busy) return;
    busy = key;
    error = '';
    try {
      await fn();
      await refresh();
    } catch (err) {
      error = describeApiError(err);
    } finally {
      busy = '';
    }
  }

  const setUserTier = (user, tier) =>
    run(`user:${user.xpub}`, () =>
      apiFetch(`/api/auth/users/${encodeURIComponent(user.xpub)}`, {
        method: 'PUT',
        body: buildAddUserBody({ xpub: user.xpub, name: user.name ?? '', trustLevel: tier }),
      })
    );

  const removeUser = (user) =>
    run(`user:${user.xpub}`, () =>
      apiFetch(`/api/auth/users/${encodeURIComponent(user.xpub)}`, { method: 'DELETE' })
    );

  const setPeerTier = (peer, tier) =>
    run(`peer:${peer.id}`, () =>
      apiFetch(`/api/peers/${encodeURIComponent(peer.id)}/trust`, {
        method: 'PUT',
        body: { trust_level: normalizeTrust(tier) },
      })
    );

  const removePeer = (peer) =>
    run(`peer:${peer.id}`, () => apiFetch(`/api/peers/${encodeURIComponent(peer.id)}`, { method: 'DELETE' }));

  function addUser(e) {
    e?.preventDefault?.();
    return run('add-user', async () => {
      const cls = classifyPeerInput(userInput);
      let xpub = '';
      let name = userName;
      if (cls.kind === 'vcard') {
        const card = peerFromVCardText(cls.value);
        xpub = card.xpub;
        if (!xpub) throw new Error('That card carries no xpub alias — paste the operator xpub instead.');
        if (!name.trim()) name = card.name;
      } else if (cls.kind === 'xpub') {
        xpub = cls.value;
      } else {
        throw new Error('An operator is identified by an xpub (or a card carrying one).');
      }
      await apiFetch('/api/auth/users', {
        method: 'POST',
        body: buildAddUserBody({ xpub, name, trustLevel: userTier }),
      });
      userInput = '';
      userName = '';
    });
  }

  function addPeer(e) {
    e?.preventDefault?.();
    return run('add-peer', async () => {
      const cls = classifyPeerInput(peerInput);
      if (cls.kind === 'empty' || cls.kind === 'unknown') {
        throw new Error('Paste a peer id, a public key, an xpub, or a vCard.');
      }
      if (cls.kind === 'vcard') {
        const card = peerFromVCardText(cls.value);
        let id = card.peerId;
        // An xpub is a wallet account, not a libp2p host: convert it here.
        if (!id && card.xpub) id = await peerIdFromXpub(card.xpub);
        if (!id) {
          // No identity in the card the browser can resolve — let the node
          // parse it (it assigns Standard, or the card's own trust level).
          await apiPostText('/api/peers/import/vcard', card.vcard);
        } else {
          await apiFetch('/api/peers', {
            method: 'POST',
            body: buildAddPeerBody({
              id,
              trustLevel: peerTier,
              name: peerName || card.name,
              organization: peerOrg || card.organization,
              notes: peerNotes,
              vcard: card.vcard,
            }),
          });
        }
      } else {
        const id = cls.kind === 'xpub' ? await peerIdFromXpub(cls.value) : cls.kind === 'peer_id' ? cls.value : '';
        await apiFetch('/api/peers', {
          method: 'POST',
          body: buildAddPeerBody({
            id,
            publicKey: cls.kind === 'public_key' ? cls.value : '',
            trustLevel: peerTier,
            name: peerName,
            organization: peerOrg,
            notes: peerNotes,
          }),
        });
      }
      peerInput = '';
      peerName = '';
      peerOrg = '';
      peerNotes = '';
    });
  }

</script>

<div class="page">
  {#if admin}
    {#if error}
      <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
    {/if}

    <!-- ---------------------------------------------------------------- -->
    <Panel variant="raised" pad="0">
      <div class="head" style="border-color:{theme.divider};">
        <div>
          <Kicker text="XPUB USER STORE · /api/auth/users" />
          <div class="ttl" style="color:{theme.textBright};">OPERATOR KEYS</div>
        </div>
        <div class="chips">
          <StatusChip label={`${users.length} REGISTERED`} color={theme.ice} dot={false} />
        </div>
      </div>
      <div class="pad">
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr style="border-color:{theme.divider};color:{theme.textMuted};">
                <th>XPUB</th><th>PEER ID</th><th>NAME</th><th>ORG</th><th>TRUST</th>
                <th>KEY</th><th>SIGN-INS</th><th>SOURCE</th><th></th>
              </tr>
            </thead>
            <tbody>
              {#each users as user (user.xpub)}
                <tr style="border-color:{theme.divider};">
                  <td class="mono" style="color:{theme.ice};" title={user.xpub}>{shortId(user.xpub)}</td>
                  <td class="mono" style="color:{theme.textDim};" title={user.peer_id || 'no peer id — this row is not xpub-keyed'}>
                    {user.peer_id ? shortId(user.peer_id) : '—'}
                  </td>
                  <td style="color:{theme.textBody};">{user.name || '—'}</td>
                  <td style="color:{theme.textDim};">{user.organization || '—'}</td>
                  <td>
                    <select
                      value={normalizeTrust(user.trust_level)}
                      disabled={busy === `user:${user.xpub}` || user.source === 'config'}
                      onchange={(e) => setUserTier(user, e.currentTarget.value)}
                      style="color:{tierColor(user.trust_level)};border-color:{theme.hairline};"
                      aria-label="Trust level"
                    >
                      {#each userTiers as tier (tier)}
                        <option value={tier}>{tier.toUpperCase()}</option>
                      {/each}
                      {#if !userTiers.includes(normalizeTrust(user.trust_level))}
                        <option value={normalizeTrust(user.trust_level)}>{normalizeTrust(user.trust_level).toUpperCase()}</option>
                      {/if}
                    </select>
                  </td>
                  <td>
                    {#if userNeedsKeyProof(user)}
                      <span style="color:{theme.amber};">AWAITING PROOF</span>
                    {:else}
                      <span class="mono" style="color:{theme.green};">BOUND</span>
                    {/if}
                  </td>
                  <td class="mono" style="color:{theme.textDim};" title={user.last_connected ? `last sign-in ${user.last_connected}` : 'never signed in'}>
                    {user.connection_count ?? 0}
                  </td>
                  <td style="color:{theme.textFaint};">{(user.source || '').toUpperCase()}</td>
                  <td class="right">
                    {#if user.source !== 'config'}
                      <GBtn
                        title="Remove this operator key"
                        variant="destructive"
                        disabled={busy === `user:${user.xpub}`}
                        onclick={() => removeUser(user)}
                      >REMOVE</GBtn>
                    {/if}
                  </td>
                </tr>
              {:else}
                <tr><td colspan="9" class="none" style="color:{theme.textFaint};">
                  No operator keys enrolled.{#if rootAdminAvailable} This node's own root recovery phrase signs in as admin.{/if}
                </td></tr>
              {/each}
            </tbody>
          </table>
        </div>

        <form class="add" onsubmit={addUser}>
          <div class="k" style="color:{theme.textMuted};">
            APPROVE A KEY
            {#if userKind !== 'empty'}
              <span class="badge" style="color:{theme.cyan};border-color:{theme.cyan};">{PEER_KIND_LABEL[userKind]}</span>
            {/if}
          </div>
          <textarea
            bind:value={userInput}
            rows="2"
            spellcheck="false"
            placeholder="operator xpub, or a pasted vCard carrying one"
            style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
          ></textarea>
          <div class="add-row">
            <input
              type="text"
              bind:value={userName}
              placeholder="name (optional)"
              style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
            />
            <select bind:value={userTier} style="color:{tierColor(userTier)};border-color:{theme.hairline};" aria-label="Grant trust level">
              {#each userTiers as tier (tier)}
                <option value={tier}>{tier.toUpperCase()}</option>
              {/each}
            </select>
            <GBtn title="Register this key" variant="primary" disabled={busy === 'add-user' || !userInput.trim()}>
              {busy === 'add-user' ? 'ADDING…' : 'APPROVE'}
            </GBtn>
          </div>
          <div class="hint" style="color:{theme.textFaint};">
            You can grant up to your own tier, capped at ADMIN — ULTIMATE is this node's own
            identity and NEVER is not an operator lockout the node accepts. The holder proves
            possession below (or on their first sign-in, which binds the key permanently).
          </div>
        </form>
      </div>
    </Panel>

    <!-- ---------------------------------------------------------------- -->
    <Panel variant="raised" pad="0">
      <div class="head" style="border-color:{theme.divider};">
        <div>
          <Kicker text="LIBP2P TRUST REGISTRY · /api/peers" />
          <div class="ttl" style="color:{theme.textBright};">NETWORK PEERS</div>
        </div>
        <div class="chips">
          <StatusChip label={`${peers.length} IN REGISTRY`} color={theme.ice} dot={false} />
        </div>
      </div>
      <div class="pad">
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr style="border-color:{theme.divider};color:{theme.textMuted};">
                <th>PEER ID</th><th>NAME</th><th>ORG</th><th>TRUST</th><th>EFFECTIVE</th><th></th>
              </tr>
            </thead>
            <tbody>
              {#each peers as peer (peer.id)}
                <tr style="border-color:{theme.divider};">
                  <td class="mono" style="color:{theme.ice};" title={peer.id}>{shortId(peer.id)}</td>
                  <td style="color:{theme.textBody};">{peer.name || '—'}</td>
                  <td style="color:{theme.textDim};">{peer.organization || '—'}</td>
                  <td>
                    <select
                      value={normalizeTrust(peer.trust_level)}
                      disabled={busy === `peer:${peer.id}`}
                      onchange={(e) => setPeerTier(peer, e.currentTarget.value)}
                      style="color:{tierColor(peer.trust_level)};border-color:{theme.hairline};"
                      aria-label="Peer trust level"
                    >
                      {#each peerTiers as tier (tier)}
                        <option value={tier}>{tier.toUpperCase()}</option>
                      {/each}
                      {#if !peerTiers.includes(normalizeTrust(peer.trust_level))}
                        <option value={normalizeTrust(peer.trust_level)}>{normalizeTrust(peer.trust_level).toUpperCase()}</option>
                      {/if}
                    </select>
                  </td>
                  <td>
                    <span style="color:{tierColor(peer.effective_trust_level)};">
                      {normalizeTrust(peer.effective_trust_level).toUpperCase()}
                    </span>
                    {#if peer.computed_valid}
                      <span class="wot" style="color:{theme.textFaint};" title="Rooted web-of-trust validity">· WOT</span>
                    {/if}
                  </td>
                  <td class="right">
                    <GBtn
                      title="Remove this peer"
                      variant="destructive"
                      disabled={busy === `peer:${peer.id}`}
                      onclick={() => removePeer(peer)}
                    >REMOVE</GBtn>
                  </td>
                </tr>
              {:else}
                <tr><td colspan="6" class="none" style="color:{theme.textFaint};">No peers in the trust registry.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>

        <form class="add" onsubmit={addPeer}>
          <div class="k" style="color:{theme.textMuted};">
            ADD A PEER
            {#if peerKind !== 'empty'}
              <span class="badge" style="color:{theme.cyan};border-color:{theme.cyan};">{PEER_KIND_LABEL[peerKind]}</span>
            {/if}
          </div>
          <textarea
            bind:value={peerInput}
            rows="3"
            spellcheck="false"
            placeholder="peer id, public key (hex/base64), xpub, or a pasted vCard"
            style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
          ></textarea>
          <div class="add-row">
            <input type="text" bind:value={peerName} placeholder="name (optional)" style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};" />
            <input type="text" bind:value={peerOrg} placeholder="organization (optional)" style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};" />
            <select bind:value={peerTier} style="color:{tierColor(peerTier)};border-color:{theme.hairline};" aria-label="Peer trust level">
              {#each peerTiers as tier (tier)}
                <option value={tier}>{tier.toUpperCase()}</option>
              {/each}
            </select>
            <GBtn title="Add this peer" variant="primary" disabled={busy === 'add-peer' || !peerInput.trim()}>
              {busy === 'add-peer' ? 'ADDING…' : 'ADD'}
            </GBtn>
          </div>
          <input type="text" bind:value={peerNotes} placeholder="notes (optional)" style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};width:100%;" />
          <div class="hint" style="color:{theme.textFaint};">
            An xpub is a wallet account, not a libp2p host — it is converted to a peer id in
            this page before it is sent. A pasted card is stored verbatim with the EPM record
            the node can derive from it; nothing the card does not say is filled in.
          </div>
        </form>
      </div>
    </Panel>

  {/if}
</div>

<style>
  .page {
    flex: 1;
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 16px;
    padding-bottom: 8px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .page :global(> section) { height: fit-content; }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    padding: 14px 18px 12px;
    border-bottom: 1px solid;
  }
  .ttl {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 21px;
    letter-spacing: 0.06em;
    margin-top: 4px;
  }
  .chips { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
  .pad { padding: 14px 18px 18px; }
  .k { font-size: 12.5px; letter-spacing: 0.18em; margin-bottom: 8px; }
  .prose { font-size: 15px; line-height: 1.6; letter-spacing: 0.02em; margin: 0 0 16px; }
  .legend { list-style: none; margin: 0 0 18px; padding: 0; }
  .legend li {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    border-bottom: 1px solid;
    padding: 6px 0;
  }
  .legend li:last-child { border-bottom: 0; }
  .tier { font-size: 13.5px; letter-spacing: 0.16em; }
  .count { font-size: 12.5px; letter-spacing: 0.06em; }
  .signin-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .tbl-wrap { overflow-x: auto; margin-bottom: 16px; }
  table { width: 100%; border-collapse: collapse; }
  th {
    text-align: left;
    font-weight: 400;
    font-size: 12.5px;
    letter-spacing: 0.18em;
    padding: 0 10px 8px 0;
    border-bottom: 1px solid;
    white-space: nowrap;
  }
  td {
    font-size: 14.5px;
    letter-spacing: 0.02em;
    padding: 7px 10px 7px 0;
    border-bottom: 1px solid;
    vertical-align: middle;
  }
  td.right { text-align: right; padding-right: 0; }
  td.none { text-align: center; padding: 18px 0; font-size: 14px; }
  .wot { font-size: 11px; letter-spacing: 0.1em; }
  select,
  input,
  textarea {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 14px;
    letter-spacing: 0.06em;
    padding: 5px 8px;
    outline: none;
  }
  select option { background: #0a141b; }
  textarea {
    width: 100%;
    font-size: 14px;
    resize: vertical;
    letter-spacing: 0.02em;
    padding: 8px 10px;
  }
  .add { border-top: 1px solid rgba(110, 170, 190, 0.13); padding-top: 13px; display: flex; flex-direction: column; gap: 9px; }
  .add-row { display: flex; gap: 9px; flex-wrap: wrap; align-items: center; }
  .add-row input { flex: 1 1 180px; min-width: 0; }
  .badge {
    border: 1px solid;
    font-size: 10.5px;
    letter-spacing: 0.14em;
    padding: 1px 6px;
    margin-left: 8px;
  }
  .hint { font-size: 12.5px; letter-spacing: 0.03em; line-height: 1.55; }
  .err {
    border: 1px solid;
    padding: 10px 12px;
    font-size: 13.5px;
    letter-spacing: 0.04em;
    line-height: 1.5;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
