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
  import Kick from './Kick.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { apiFetch, apiPostText, describeApiError } from './api.js';
  import { normalizeTrust, trustRank, TRUST_COLOR_TOKEN, TRUST_TIERS } from './trust.js';
  import { shortId } from './format.js';
  import { peerIdFromXpub, shortFingerprint } from './wallet.js';
  import OperatorEditModal from './OperatorEditModal.svelte';
  import PeerEditModal from './PeerEditModal.svelte';
  import { operatorMeta, peerMeta, peerIdCell } from './keystate.js';
  import {
    buildPinBody,
    multiaddrsFromVCard,
    pinDisplayName,
    pinIsLocked,
    pinNoteIsPublishable,
    pinSourceLabel,
    pinnableNodes,
    pinnedAtLabel,
    sortPins,
  } from './peers.js';
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

  /**
   * `view` selects WHICH of this component's sections render. One component, one
   * set of API calls and one piece of state, shown on the surface each part
   * belongs to (owner directive 2026-07-30: ACCOUNTS' two tables become tabs, and
   * the add-peer form moves to the new PEERS menu; IRIS R7 sequenced it):
   *
   *   'keys'     — OPERATOR KEYS only        (ACCOUNTS tab 2)
   *   'peers'    — NETWORK PEERS table only, WITHOUT the add form (ACCOUNTS tab 1)
   *   'peer-add' — the add-peer form only    (the PEERS route)
   *   'all'      — everything, as before
   *
   * Splitting the component instead of the state is deliberate: two mounted
   * copies would each poll /api/auth/users and /api/peers, and an edit made in one
   * would leave the other stale.
   *
   * WHICH MODAL IS OPEN IS NOT THIS COMPONENT'S STATE (IRIS ruling 2026-07-30
   * §5). It is in the URL — `#/accounts/peers/<id>`, `#/accounts/keys/<xpub>` —
   * so App.svelte owns it and hands it down as `openPeerId`/`openUserXpub`,
   * and every open/close here goes back out through `onOpenModal`. Two copies
   * of that fact would be two answers to "what is on screen", one of them in
   * the address bar and wrong.
   *
   * `sessionKnown` is false until /api/auth/me has answered ONCE: an empty
   * assignable-tier list means "below Admin" only after that, and "not yet"
   * before it (§2).
   *
   * @type {{
   *   session: any, nodes: any[], rootAdminAvailable?: boolean|null,
   *   sessionKnown?: boolean, openPeerId?: string, openUserXpub?: string,
   *   onOpenModal?: (kind: string, id: string) => void,
   *   onCloseModal?: () => void,
   *   onModalMissing?: (kind: string, id: string) => void,
   *   onRequestSignIn: () => void, view?: 'all' | 'keys' | 'peers' | 'peer-add',
   * }}
   */
  let {
    session,
    nodes,
    rootAdminAvailable = null,
    sessionKnown = false,
    openPeerId = '',
    openUserXpub = '',
    onOpenModal,
    onCloseModal,
    onModalMissing,
    onRequestSignIn,
    view = 'all',
  } = $props();

  const showKeys = $derived(view === 'all' || view === 'keys');
  const showPeers = $derived(view === 'all' || view === 'peers' || view === 'peer-add');
  /** The registry TABLE — absent on the PEERS route, which shows the form alone. */
  const showPeerTable = $derived(view === 'all' || view === 'peers');
  /** The ADD A PEER form — absent from the ACCOUNTS tab, which is a reading surface. */
  const showPeerForm = $derived(view === 'all' || view === 'peer-add');

  const level = $derived(session?.trustLevel ?? '');
  const admin = $derived(canManagePermissions(level));
  const userTiers = $derived(tiersHighestFirst(assignableUserTiers(level)));
  const peerTiers = $derived(tiersHighestFirst(assignablePeerTiers(level)));
  const tierColor = (t) => theme[TRUST_COLOR_TOKEN[normalizeTrust(t)]] ?? theme.textMuted;

  /**
   * Resolve a subscript item's tone to a theme token. The two trust items name
   * a TIER rather than a colour, because a tier's colour is the trust palette's
   * to decide (trust.js), not this line's.
   */
  function metaColor(item, row) {
    if (item.tone === 'trust') return tierColor(row?.trust_level);
    if (item.tone === 'effective') return tierColor(row?.effective_trust_level);
    return theme[item.tone] ?? theme.textDim;
  }

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

  // `null` means "not answered yet" and renders LOADING; `[]` means the node
  // really has none and renders the empty sentence. Starting at `[]` flashed
  // "No operator keys enrolled." at every admin on every page load (IRIS D2).
  /** @type {any[]|null} */
  let users = $state(null);
  /** @type {any[]|null} */
  let peers = $state(null);
  /**
   * THE PIN REGISTRY (owner ruling 2026-07-30: "The Peers table should not ever
   * show peers that have never been seen, UNLESS they have been added manually
   * and 'pinned' (we need an interface for that)"). This is that interface.
   *
   * `null` means the node did not answer — a build without the pin registry, or
   * a session that lost its cookie — and renders `pinsError`, never an empty
   * list. "No pins" and "could not read the pins" are different facts and the
   * one that matters most here is the second: an operator looking at an empty
   * panel would conclude their pins were gone.
   * @type {any[]|null}
   */
  let pins = $state(null);
  let pinsError = $state('');
  let loaded = $state(false);
  let error = $state('');
  /** Non-fatal outcome of the last add: it worked, but not entirely. */
  let notice = $state('');
  let busy = $state('');
  /** xpub → derived peer id, or '' when a derivation ran and failed. */
  let derivedPeerIds = $state(new Map());
  /** The row whose EDIT modal is open — read from the URL, never held here. */
  const editUserXPub = $derived(openUserXpub);
  const editPeerId = $derived(openPeerId);

  const userList = $derived(users ?? []);
  const peerList = $derived(peers ?? []);
  const pinList = $derived(sortPins(pins ?? []));
  const editUser = $derived(userList.find((u) => u.xpub === editUserXPub) ?? null);
  const editPeer = $derived(peerList.find((p) => p.id === editPeerId) ?? null);
  /**
   * The feed row for the peer whose modal is open, when it has one. The
   * registry knows a peer's trust; only the feed knows whether this node has
   * ever had it on the line, which is the fact the contact card turns on (§3).
   */
  const editPeerNode = $derived(
    editPeer ? (nodes ?? []).find((n) => String(n?.peerId ?? '') === editPeer.id) ?? null : null
  );

  /**
   * A DEEP LINK TO A RECORD THAT IS NOT THERE is answered, not silently
   * redirected (§5): the link may have come from another node, or the record
   * may have been removed since. Only judged once the registry has actually
   * answered — `loaded` with a non-null list — or a slow read would look like
   * a missing row.
   */
  $effect(() => {
    if (!openPeerId || !loaded || peers === null) return;
    if (peerList.some((p) => p.id === openPeerId)) return;
    onModalMissing?.('peer', openPeerId);
  });
  $effect(() => {
    if (!openUserXpub || !loaded || users === null) return;
    if (userList.some((u) => u.xpub === openUserXpub)) return;
    onModalMissing?.('operator', openUserXpub);
  });
  /** The pin record for the peer whose modal is open, or null when unpinned. */
  const editPeerPin = $derived(
    editPeer ? (pins ?? []).find((p) => String(p?.peer_id ?? '') === editPeer.id) ?? null : null
  );
  /** Connected right now and NOT pinned — one click from being kept. */
  const pinnable = $derived(pinnableNodes(nodes, pins ?? []));

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

  /**
   * The pin registry is read SEPARATELY from the other two: it is the newest
   * endpoint on the node, and a node running a build without it must still be
   * able to manage operator keys and trust. Its failure is reported in its own
   * panel, not as the page's error.
   */
  async function readPins() {
    try {
      const res = await apiFetch('/api/peers/pins');
      pins = Array.isArray(res?.pins) ? res.pins : [];
      pinsError = '';
    } catch (err) {
      pins = null;
      pinsError = describeApiError(err);
    }
  }

  async function refresh() {
    error = '';
    try {
      const [u, p] = await Promise.all([
        apiFetch('/api/auth/users'),
        apiFetch('/api/peers'),
        readPins(),
      ]);
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
      users = null;
      peers = null;
      pins = null;
      pinsError = '';
      loaded = false;
    }
  });

  /**
   * DERIVE THE PEER ID (owner directive: "the peerID calculated form teh keys").
   *
   * The node's operator JSON carries NO peer_id — internal/auth/userstore.go
   * defines the User wire shape without one — which is why this column rendered
   * an em-dash for every row that has ever been displayed. An xpub is not a
   * peer id (contract §8), but it CONVERTS to one, and the wallet runtime this
   * page already loads exposes exactly that conversion.
   *
   * Derived once per xpub and memoized: `peerIdFromXpub` pulls in the wallet
   * wasm, so a table of five keys must not be five loads, and a re-render must
   * not be a re-derivation.
   */
  $effect(() => {
    const pending = userList
      .map((u) => String(u.xpub ?? '').trim())
      .filter((x) => x && !derivedPeerIds.has(x));
    if (!pending.length) return;
    let cancelled = false;
    (async () => {
      for (const xpub of pending) {
        let id = '';
        try {
          id = await peerIdFromXpub(xpub);
        } catch {
          // The wallet runtime is unstaged, or this is not a key the node can
          // convert. Recorded as attempted-and-empty so the cell settles on
          // NOT XPUB-KEYED instead of spinning forever.
          id = '';
        }
        if (cancelled) return;
        const next = new Map(derivedPeerIds);
        next.set(xpub, String(id ?? '').trim());
        derivedPeerIds = next;
      }
    })();
    return () => (cancelled = true);
  });

  const cellFor = (user) => {
    const xpub = String(user?.xpub ?? '').trim();
    return peerIdCell(derivedPeerIds.get(xpub) ?? '', xpub, derivedPeerIds.has(xpub));
  };

  async function run(key, fn) {
    if (busy) return;
    busy = key;
    error = '';
    notice = '';
    try {
      await fn();
      await refresh();
    } catch (err) {
      error = describeApiError(err);
    } finally {
      busy = '';
    }
  }

  /**
   * PIN — the peer stays listed even when it is not connected. Idempotent by
   * design: `already_pinned` means the list this page was rendering was one
   * frame stale, which is not an error an operator should ever be shown.
   */
  const pinPeer = (node) =>
    run(`pin:${node.peerId}`, async () => {
      try {
        await apiFetch('/api/peers/pins', {
          method: 'POST',
          body: buildPinBody({
            peerId: node.peerId,
            addrs: node.addrs ?? [],
            name: (node.dn ?? '').trim(),
          }),
        });
      } catch (err) {
        if (err?.code !== 'already_pinned') throw err;
      }
    });

  /**
   * UNPIN. A config-file pin is refused by the node with 409 `config_pin` and
   * this page does not offer the button for one — but the refusal is still
   * handled, because a config change between render and click is exactly the
   * case where a button would otherwise appear to do nothing.
   */
  const unpinPeer = (pin) =>
    run(`pin:${pin.peer_id}`, () =>
      apiFetch(`/api/peers/pins/${encodeURIComponent(pin.peer_id)}`, { method: 'DELETE' })
    );

  const setUserTier = (user, tier) =>
    run(`user:${user.xpub}`, () =>
      apiFetch(`/api/auth/users/${encodeURIComponent(user.xpub)}`, {
        method: 'PUT',
        body: buildAddUserBody({ xpub: user.xpub, name: user.name ?? '', trustLevel: tier }),
      })
    );

  /**
   * Rename keeps the trust level: PUT is the whole record (contract §7), so
   * sending the name alone would silently reset the tier to the body default.
   */
  const renameUser = (user, name) =>
    run(`user:${user.xpub}`, () =>
      apiFetch(`/api/auth/users/${encodeURIComponent(user.xpub)}`, {
        method: 'PUT',
        body: buildAddUserBody({ xpub: user.xpub, name, trustLevel: normalizeTrust(user.trust_level) }),
      })
    );

  const removeUser = (user) =>
    run(`user:${user.xpub}`, async () => {
      await apiFetch(`/api/auth/users/${encodeURIComponent(user.xpub)}`, { method: 'DELETE' });
      // The row this modal is about no longer exists — closing it is the only
      // honest outcome, and closing means the URL, which is where the modal is.
      onCloseModal?.();
    });

  const setPeerTier = (peer, tier) =>
    run(`peer:${peer.id}`, () =>
      apiFetch(`/api/peers/${encodeURIComponent(peer.id)}/trust`, {
        method: 'PUT',
        body: { trust_level: normalizeTrust(tier) },
      })
    );

  const removePeer = (peer) =>
    run(`peer:${peer.id}`, async () => {
      await apiFetch(`/api/peers/${encodeURIComponent(peer.id)}`, { method: 'DELETE' });
      onCloseModal?.();
    });

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

  /**
   * ADDING A PEER PINS IT (owner ruling 2026-07-30: a never-seen peer is listed
   * only if it "has been added manually and 'pinned'"). Adding without pinning
   * would put a row in the trust registry that the peers table refuses to show
   * until the peer happens to dial in — which is precisely the invisible state
   * the owner was complaining about, arrived at from the other direction.
   *
   * The pin is a SECOND call and can fail on its own, so its failure is reported
   * as what it is: the registry entry exists, the listing guarantee does not.
   */
  async function pinAfterAdd(id, { addrs = [], name = '' } = {}) {
    if (!id) {
      notice =
        'Added to the trust registry, but this page could not read a peer id from what you pasted, so it was not pinned. It will be listed only while it is connected.';
      return;
    }
    try {
      await apiFetch('/api/peers/pins', {
        method: 'POST',
        body: buildPinBody({ peerId: id, addrs, name, note: peerNotes }),
      });
    } catch (err) {
      if (err?.code === 'already_pinned') return;
      notice = `Added to the trust registry, but not pinned: ${describeApiError(err)} Until it is pinned it is listed only while it is connected.`;
    }
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
        // The card's own multiaddrs, verbatim — a pin the node can actually
        // dial. Nothing is synthesized when the card carries none.
        await pinAfterAdd(id, {
          addrs: multiaddrsFromVCard(card.vcard),
          name: peerName || card.name,
        });
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
        await pinAfterAdd(id, { name: peerName });
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
    <!-- PARTIAL SUCCESS IS NOT SUCCESS AND IT IS NOT FAILURE. An add that
         reached the trust registry but not the pin registry gets said out loud,
         in amber, naming what the operator now does NOT have. -->
    {#if notice}
      <div class="err" style="color:{theme.amber};border-color:{theme.amber};">{notice}</div>
    {/if}

    <!-- ---------------------------------------------------------------- -->
    {#if showKeys}
    <Panel variant="raised" pad="0">
      <div class="head" style="border-color:{theme.divider};">
        <div>
          <Kick text="XPUB USER STORE · /api/auth/users" />
          <div class="ttl" style="color:{theme.textBright};">OPERATOR KEYS</div>
        </div>
        <div class="chips">
          <StatusChip label={`${userList.length} REGISTERED`} color={theme.ice} dot={false} />
        </div>
      </div>
      <div class="pad">
        <div class="tbl-wrap">
          <!--
            THE ROW GRAMMAR (owner directive 2026-07-29: "The table should just
            have teh XPUB, the peerID calculated form teh keys, and the vcard
            title/name/etc (or unknown), and that's it. The trust, key,
            sign-ins should all be small subscript stuff on the same row, and
            there should be an 'edit' button").

            One RECORD = one <tbody> holding two <tr>s: the primary line, and a
            subscript metadata line that carries the pair's bottom border so the
            two read as ONE row. Column alignment survives; the whole tbody is
            the click target (IRIS ruling §1).
          -->
          <table>
            <thead>
              <tr style="border-color:{theme.divider};color:{theme.textMuted};">
                <th>XPUB</th><th>PEER ID</th><th>NAME</th><th></th>
              </tr>
            </thead>
            {#if users === null}
              <tbody>
                <tr><td colspan="4" class="none" style="color:{theme.textFaint};">Reading the operator keys…</td></tr>
              </tbody>
            {:else if !userList.length}
              <tbody>
                <tr><td colspan="4" class="none" style="color:{theme.textFaint};">
                  No operator keys enrolled.{#if rootAdminAvailable} This node's own root recovery phrase signs in as admin.{/if}
                </td></tr>
              </tbody>
            {:else}
              {#each userList as user (user.xpub)}
                {@const cell = cellFor(user)}
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions a11y_click_events_have_key_events a11y_no_noninteractive_tabindex -->
                <tbody
                  class="rec"
                  onclick={() => onOpenModal?.('operator', user.xpub)}
                  onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onOpenModal?.('operator', user.xpub)}
                >
                  <tr class="primary">
                    <td class="mono" style="color:{theme.ice};" title={user.xpub}>{shortId(user.xpub)}</td>
                    <td class="mono" style="color:{cell.id ? theme.textDim : theme.textFaint};" title={cell.id || 'derived from the xpub in this page — the node stores no peer id'}>
                      {cell.id ? shortId(cell.id) : cell.label}
                    </td>
                    <td>
                      <!-- `unnamed`, not `unknown` (IRIS §4): UNKNOWN is a trust
                           tier, and this row renders one two columns away. -->
                      <span class="nm" class:unnamed={!(user.name || '').trim()} style="color:{(user.name || '').trim() ? theme.textBright : theme.textMuted};">
                        {(user.name || '').trim() || 'unnamed'}
                      </span>
                      {#if (user.organization || '').trim() && user.organization.trim() !== (user.name || '').trim()}
                        <span class="org" style="color:{theme.textDim};">· {user.organization}</span>
                      {/if}
                    </td>
                    <td class="right" rowspan="2" style="border-color:{theme.divider};">
                      <GBtn title="Manage this operator key" onclick={() => onOpenModal?.('operator', user.xpub)}>EDIT</GBtn>
                    </td>
                  </tr>
                  <tr class="meta">
                    <td colspan="3" style="border-color:{theme.divider};">
                      <span class="metaline">
                        {#each operatorMeta(user, normalizeTrust(user.trust_level)) as item, i (item.id)}
                          {#if i > 0}<span class="sep" style="color:{theme.textFaint};">·</span>{/if}
                          {#if item.k}<span class="lbl" style="color:{theme.textFaint};">{item.k}</span>{/if}
                          <span style="color:{metaColor(item, user)};">{item.v}</span>
                        {/each}
                      </span>
                    </td>
                  </tr>
                </tbody>
              {/each}
            {/if}
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
    {/if}

    <!-- ---------------------------------------------------------------- -->
    {#if showPeers}
    <Panel variant="raised" pad="0">
      <div class="head" style="border-color:{theme.divider};">
        <div>
          <Kick text="LIBP2P TRUST REGISTRY · /api/peers" />
          <div class="ttl" style="color:{theme.textBright};">{showPeerTable ? 'NETWORK PEERS' : 'ADD A PEER'}</div>
        </div>
        <div class="chips">
          <StatusChip label={`${peerList.length} IN REGISTRY`} color={theme.ice} dot={false} />
        </div>
      </div>
      <div class="pad">
        {#if showPeerTable}
        <div class="tbl-wrap">
          <!-- Same grammar as OPERATOR KEYS (IRIS ruling §4): two adjacent
               tables that behave differently is the mess the owner named. -->
          <table>
            <thead>
              <tr style="border-color:{theme.divider};color:{theme.textMuted};">
                <th>PEER ID</th><th>NAME</th><th></th>
              </tr>
            </thead>
            {#if peers === null}
              <tbody>
                <tr><td colspan="3" class="none" style="color:{theme.textFaint};">Reading the trust registry…</td></tr>
              </tbody>
            {:else if !peerList.length}
              <tbody>
                <tr><td colspan="3" class="none" style="color:{theme.textFaint};">No peers in the trust registry.</td></tr>
              </tbody>
            {:else}
              {#each peerList as peer (peer.id)}
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions a11y_click_events_have_key_events a11y_no_noninteractive_tabindex -->
                <tbody
                  class="rec"
                  onclick={() => onOpenModal?.('peer', peer.id)}
                  onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onOpenModal?.('peer', peer.id)}
                >
                  <tr class="primary">
                    <td class="mono" style="color:{theme.ice};" title={peer.id}>{shortId(peer.id)}</td>
                    <td>
                      <span class="nm" class:unnamed={!(peer.name || '').trim()} style="color:{(peer.name || '').trim() ? theme.textBright : theme.textMuted};">
                        {(peer.name || '').trim() || 'unnamed'}
                      </span>
                      {#if (peer.organization || '').trim() && peer.organization.trim() !== (peer.name || '').trim()}
                        <span class="org" style="color:{theme.textDim};">· {peer.organization}</span>
                      {/if}
                    </td>
                    <td class="right" rowspan="2" style="border-color:{theme.divider};">
                      <GBtn title="Manage this peer" onclick={() => onOpenModal?.('peer', peer.id)}>EDIT</GBtn>
                    </td>
                  </tr>
                  <tr class="meta">
                    <td colspan="2" style="border-color:{theme.divider};">
                      <span class="metaline">
                        {#each peerMeta(peer, normalizeTrust(peer.trust_level), normalizeTrust(peer.effective_trust_level)) as item, i (item.id)}
                          {#if i > 0}<span class="sep" style="color:{theme.textFaint};">·</span>{/if}
                          {#if item.k}<span class="lbl" style="color:{theme.textFaint};">{item.k}</span>{/if}
                          <span style="color:{metaColor(item, peer)};">{item.v}</span>
                        {/each}
                      </span>
                    </td>
                  </tr>
                </tbody>
              {/each}
            {/if}
          </table>
        </div>
        {/if}

        {#if showPeerForm}
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
            Adding a peer PINS it: it stays in the peers table even while it is offline, which is
            the only way a peer this node has never contacted is listed at all. Everything else in
            that table is there because it is connected right now, and leaves when it disconnects.
            An xpub is a wallet account, not a libp2p host — it is converted to a peer id in
            this page before it is sent. A pasted card is stored verbatim with the EPM record
            the node can derive from it; nothing the card does not say is filled in.
          </div>
        </form>
        {/if}
      </div>
    </Panel>
    {/if}

    <!-- ---------------------------------------------------------------- -->
    <!-- PINNED PEERS — the interface the owner asked for by name (2026-07-30:
         "UNLESS they have been added manually and 'pinned' (we need an interface
         for that)"). It is ONE surface for the pin registry: the pins, why each
         one is there, and the peers that could become one. Same record grammar
         as the two tables above it. -->
    {#if showPeerForm}
    <Panel variant="raised" pad="0">
      <div class="head" style="border-color:{theme.divider};">
        <div>
          <Kick text="PIN REGISTRY · /api/peers/pins" />
          <div class="ttl" style="color:{theme.textBright};">PINNED PEERS</div>
        </div>
        <div class="chips">
          <StatusChip label={`${pinList.length} PINNED`} color={theme.ice} dot={false} />
        </div>
      </div>
      <div class="pad">
        <div class="hint" style="color:{theme.textFaint};">
          A pinned peer is listed whether or not it is connected. Everything else disappears from
          the peers table the moment it drops off the network.
        </div>
        <div class="tbl-wrap">
          <table>
            <thead>
              <tr style="border-color:{theme.divider};color:{theme.textMuted};">
                <th>PEER ID</th><th>NAME</th><th></th>
              </tr>
            </thead>
            {#if pinsError}
              <tbody>
                <tr><td colspan="3" class="none" style="color:{theme.amber};">
                  This node did not answer for its pins: {pinsError}
                </td></tr>
              </tbody>
            {:else if pins === null}
              <tbody>
                <tr><td colspan="3" class="none" style="color:{theme.textFaint};">Reading the pin registry…</td></tr>
              </tbody>
            {:else if !pinList.length}
              <tbody>
                <tr><td colspan="3" class="none" style="color:{theme.textFaint};">
                  No pinned peers. The peers table shows only what is connected right now.
                </td></tr>
              </tbody>
            {:else}
              {#each pinList as pin (pin.peer_id)}
                {@const locked = pinIsLocked(pin)}
                {@const when = pinnedAtLabel(pin)}
                <tbody class="rec static">
                  <tr class="primary">
                    <td class="mono" style="color:{theme.ice};" title={pin.peer_id}>{shortId(pin.peer_id)}</td>
                    <td>
                      <span class="nm" class:unnamed={pinDisplayName(pin) === 'unnamed'} style="color:{pinDisplayName(pin) === 'unnamed' ? theme.textMuted : theme.textBright};">
                        {pinDisplayName(pin)}
                      </span>
                    </td>
                    <td class="right" rowspan="2" style="border-color:{theme.divider};">
                      {#if locked}
                        <!-- NOT a disabled button. "Three greyed buttons advertise
                             a capability the page does not have" (NodeConsole's
                             standing note) — the row states the reason instead,
                             which is also the thing the operator needs to know. -->
                        <span class="locked" style="color:{theme.textFaint};">CONFIG FILE</span>
                      {:else}
                        <GBtn title="Stop listing this peer while it is offline" disabled={busy === `pin:${pin.peer_id}`} onclick={() => unpinPeer(pin)}>
                          {busy === `pin:${pin.peer_id}` ? 'UNPINNING…' : 'UNPIN'}
                        </GBtn>
                      {/if}
                    </td>
                  </tr>
                  <tr class="meta">
                    <td colspan="2" style="border-color:{theme.divider};">
                      <span class="metaline">
                        <span style="color:{locked ? theme.textDim : theme.ice};">{pinSourceLabel(pin)}</span>
                        {#if when}
                          <span class="sep" style="color:{theme.textFaint};">·</span>
                          <span style="color:{theme.textDim};">{when}</span>
                        {/if}
                        {#if (pin.pinned_by ?? '').trim()}
                          <span class="sep" style="color:{theme.textFaint};">·</span>
                          <span class="lbl" style="color:{theme.textFaint};">BY</span>
                          <span style="color:{theme.textDim};">{pin.pinned_by}</span>
                        {/if}
                        <!-- A CONFIG pin's "note" is a file path and a key, and
                             it is never rendered as text (IRIS §6): the peer's
                             own modal copies it to the clipboard instead. What
                             survives here is an operator's own prose note, and
                             only when it does not read like a location. -->
                        {#if pinNoteIsPublishable(pin)}
                          <span class="sep" style="color:{theme.textFaint};">·</span>
                          <span class="lbl" style="color:{theme.textFaint};">NOTE</span>
                          <span style="color:{theme.textDim};">{pin.note}</span>
                        {/if}
                      </span>
                    </td>
                  </tr>
                </tbody>
              {/each}
            {/if}
          </table>
        </div>

        {#if pinnable.length}
          <!-- The owner's list, one click per row (2026-07-30: "Right now we
               should have the peer at sdn.spaceaware.io, the one at
               celestrak.eth, the one at vm-orbit-det-01, and that's it"). These
               are connected NOW and will vanish when they disconnect. -->
          <div class="sub" style="border-color:{theme.divider};">
            <div class="k" style="color:{theme.textMuted};">CONNECTED · NOT PINNED</div>
            <div class="hint" style="color:{theme.textFaint};">
              These are listed only because they are connected. Pin the ones this node should
              always show.
            </div>
            <div class="alist">
              {#each pinnable as node (node.peerId)}
                <div class="arow" style="border-color:{theme.divider};">
                  <span class="nm" class:unnamed={!(node.dn ?? '').trim()} style="color:{(node.dn ?? '').trim() ? theme.textBright : theme.textMuted};">
                    {(node.dn ?? '').trim() || 'unnamed'}
                  </span>
                  <span class="mono aid" style="color:{theme.textDim};" title={node.peerId}>{shortId(node.peerId)}</span>
                  <GBtn title="Keep listing this peer after it disconnects" disabled={busy === `pin:${node.peerId}`} onclick={() => pinPeer(node)}>
                    {busy === `pin:${node.peerId}` ? 'PINNING…' : 'PIN'}
                  </GBtn>
                </div>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    </Panel>
    {/if}

    {#if editUser}
      <OperatorEditModal
        user={editUser}
        peerCell={cellFor(editUser)}
        tiers={userTiers}
        tiersKnown={sessionKnown}
        sessionTier={level}
        hasSession={Boolean(session)}
        {rootAdminAvailable}
        busy={busy === `user:${editUser.xpub}`}
        {error}
        onSetTrust={(tier) => setUserTier(editUser, tier)}
        onRename={(name) => renameUser(editUser, name)}
        onRemove={() => removeUser(editUser)}
        {onRequestSignIn}
        onClose={() => onCloseModal?.()}
      />
    {/if}

    {#if editPeer}
      <PeerEditModal
        peer={editPeer}
        pin={editPeerPin}
        pinsKnown={pins !== null}
        node={editPeerNode}
        tiers={peerTiers}
        tiersKnown={sessionKnown}
        sessionTier={level}
        hasSession={Boolean(session)}
        {rootAdminAvailable}
        busy={busy === `peer:${editPeer.id}`}
        {error}
        onSetTrust={(tier) => setPeerTier(editPeer, tier)}
        onRemove={() => removePeer(editPeer)}
        {onRequestSignIn}
        onClose={() => onCloseModal?.()}
      />
    {/if}
  {/if}
</div>

<style>
  /* GRAMMAR L1 + L2 + L5 (iris-dashboard-grammar-law). This block used to carry
     `overflow:auto; scrollbar-gutter:stable; padding-inline:var(--sdn-sp-9)` and
     it was BOTH defects in the owner's screenshots at once: the overflow made a
     second scroller inside the page's own, and the padding added a SECOND page
     gutter on top of the route body's — which is why these panels ended ~37px
     short of the panels above them ("never have widgets with different widths and
     mismatched edges"). The page scroller and the page gutter each have exactly
     one owner now, and it is not this component. */
  .page {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sdn-sp-7);
    padding-bottom: var(--sdn-sp-3);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .page :global(> section) { height: fit-content; }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sdn-sp-6);
    padding: var(--sdn-sp-8) var(--sdn-sp-9);
    border-bottom: 1px solid;
  }
  .ttl {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-lead); line-height: var(--sdn-lh-lead);
    letter-spacing: 0.06em;
  }
  .chips { display: flex; gap: var(--sdn-sp-3); flex-wrap: wrap; justify-content: flex-end; align-self: center; }
  .pad { padding: var(--sdn-sp-9); }
  .k { font-size: var(--sdn-fs-fine); line-height: var(--sdn-lh-fine); letter-spacing: 0.18em; margin-bottom: var(--sdn-sp-3); }
  /* L1/L4: no horizontal scroller. A table too wide for its panel is a
     RESPONSIVE failure, not a pagination case — the columns that can drop do so
     at the breakpoint below, and the rest wrap. A nested scroller here is what
     put a second scrollbar inside the page in the owner's screenshot. */
  .tbl-wrap { margin-bottom: var(--sdn-sp-7); }
  table { width: 100%; border-collapse: collapse; }
  /* ONE header style for every column. The old table coloured SOURCE per-cell,
     which is the inconsistency the owner spotted; colour is inherited from the
     header <tr> and from nowhere else (IRIS ruling §1). */
  /* DENSITY (owner directive 2026-07-30: "reduce the font size in the tables by
     30%"). IRIS ruled the rungs rather than a multiplier (R7): th 15 -> micro
     (13), td 18 -> label (15). 0.7x of 18 is 12.6px, BELOW the ladder's floor and
     below the size the owner paid to increase twenty-seven hours earlier, so the
     remaining density comes from the ROW BOX — padding-block sp-3 -> sp-2 — and
     from showing ONE table at a time instead of two. Rows per viewport roughly
     doubles, which is what the instruction was actually about. */
  th {
    text-align: left;
    font-weight: 400;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.18em;
    padding: 0 var(--sdn-sp-5) var(--sdn-sp-2) 0;
    border-bottom: 1px solid;
    white-space: nowrap;
    color: inherit;
  }
  td {
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.02em;
    padding: var(--sdn-sp-2) var(--sdn-sp-5) var(--sdn-sp-1) 0;
    vertical-align: middle;
  }
  th:last-child, td.right { text-align: right; padding-right: 0; }
  /* The spanning actions cell owns the record's underline (the meta <td> no
     longer reaches this column) and centres its control by construction. */
  td.right[rowspan] {
    vertical-align: middle;
    border-bottom: 1px solid;
    /* The record's primary <td> is deliberately top-heavy (8px over 4px) so the
       subscript hugs its line. A cell that SPANS both rows must not inherit
       that asymmetry, or the control it centres lands 2px low. */
    padding-block: var(--sdn-sp-2);
  }
  td.none { text-align: center; padding: 18px 0; font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data); }

  /* A RECORD is a <tbody> of two rows that read as one: the primary line
     carries no border, the subscript line carries the pair's. */
  tbody.rec { cursor: pointer; }
  tbody.rec:hover { background: rgba(110, 170, 190, 0.05); }
  /* A pin record is not a click target — its one action is the button in its own
     cell — so it does not pretend to be one. */
  tbody.rec.static { cursor: default; }
  tbody.rec.static:hover { background: transparent; }
  .locked {
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.16em;
    white-space: nowrap;
  }
  /* The pin candidates: a list, not a third table — three facts and one control
     per row, so a table's column machinery would be scaffolding around nothing. */
  .sub { border-top: 1px solid; padding-top: var(--sdn-sp-7); display: flex; flex-direction: column; gap: var(--sdn-sp-3); }
  .alist { display: flex; flex-direction: column; margin-top: var(--sdn-sp-3); }
  .arow {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-5);
    flex-wrap: wrap;
    padding: var(--sdn-sp-3) 0;
    border-bottom: 1px solid;
  }
  .arow :global(button) { margin-left: auto; }
  .aid { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); letter-spacing: 0.04em; }
  tr.primary td { border-bottom: 0; }
  tr.meta td { border-bottom: 1px solid; padding: 0 10px 7px 0; }
  .nm {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
    letter-spacing: 0.03em;
  }
  .nm.unnamed { font-style: italic; font-weight: 400; }
  .org { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); letter-spacing: 0.03em; }
  .metaline {
    display: flex;
    flex-wrap: wrap;
    gap: 0 7px;
    align-items: baseline;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-micro);
    letter-spacing: 0.14em;
    line-height: var(--sdn-lh-micro);
  }
  .metaline .lbl { letter-spacing: 0.16em; }
  .metaline .sep { padding: 0 1px; }
  select,
  input,
  textarea {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data);
    letter-spacing: 0.06em;
    padding: var(--sdn-sp-3) var(--sdn-sp-4);
    min-height: 40px;
    outline: none;
  }
  /* L6 (owner directive 2026-07-30: "the arrow in the drop down should have
     margin"): the trust-tier select draws a chevron INSIDE its box, so the box
     reserves inset space for it — otherwise the tier name sits against the arrow
     and the arrow against the border. Same rule, same floor, as the toolbar's
     TRUST select. */
  select { padding-right: var(--sdn-sp-8); }
  select option { background: #0a141b; }
  textarea {
    width: 100%;
    max-width: 100%;
    font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body);
    resize: vertical;
    letter-spacing: 0.02em;
    padding: var(--sdn-sp-4) var(--sdn-sp-5);
  }
  .add { border-top: 1px solid rgba(110, 170, 190, 0.13); padding: var(--sdn-sp-8) 0 0; display: flex; flex-direction: column; gap: var(--sdn-sp-5); }
  .add-row { display: flex; gap: var(--sdn-sp-5); flex-wrap: wrap; align-items: stretch; }
  .add-row input { flex: 1 1 clamp(220px, 30%, 340px); min-width: 0; }
  .badge {
    border: 1px solid;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
    padding: var(--sdn-sp-1) var(--sdn-sp-2);
    margin-left: var(--sdn-sp-3);
  }
  .hint { font-size: var(--sdn-fs-body); letter-spacing: 0.03em; line-height: var(--sdn-lh-body); }
  /* A panel that OPENS with a hint owes the table under it a gap. */
  .pad > .hint:first-child { margin-bottom: var(--sdn-sp-5); display: block; }
  .err {
    border: 1px solid;
    padding: var(--sdn-sp-5) var(--sdn-sp-6);
    font-size: var(--sdn-fs-body);
    letter-spacing: 0.04em;
    line-height: var(--sdn-lh-data);
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
