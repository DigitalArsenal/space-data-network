<script>
  /**
   * ACCOUNTS: the two admin tables, as TABS, one at a time (owner directive
   * 2026-07-30 — "have the tables selectable by tab (trusted peers vs current
   * node operator keys)"). The globe and the node directory have moved to PEERS.
   */
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import AccountAdmin from '../../AccountAdmin.svelte';
  import { canManagePermissions } from '../../permissions.js';

  let {
    accountNodes = [],
    session = null,
    rootAdminAvailable = null,
    onRequestSignIn = () => {},
  } = $props();

  /**
   * NOT "TRUSTED PEERS". That tab is the libp2p trust REGISTRY, whose entries
   * carry a trust level that may be `never` — calling the whole list "trusted"
   * asserted of every row the one thing the row is there to record. The tab is
   * named after the registry, and its chip already counts honestly ("N IN
   * REGISTRY"). Part of the same correction as the header count: nothing on this
   * page calls a set "trusted" on its behalf.
   */
  const ACCOUNT_TABS = [
    ['peers', 'PEER REGISTRY'],
    ['keys', 'OPERATOR KEYS'],
  ];

  let accountTab = $state('peers');
</script>

{#if session && canManagePermissions(session.trustLevel)}
  <div class="tabbar" style="border-color:{theme.panelBorder};">
    {#each ACCOUNT_TABS as [id, label] (id)}
      <button
        class="tab"
        style="background:{accountTab === id ? 'rgba(53,201,216,0.18)' : 'transparent'};color:{accountTab === id ? theme.cyan : theme.textDim};border-color:{theme.panelBorder};"
        aria-pressed={accountTab === id}
        onclick={() => (accountTab = id)}
      >{label}</button>
    {/each}
  </div>
  <div class="account-admin">
    <AccountAdmin
      {session}
      nodes={accountNodes}
      {rootAdminAvailable}
      onRequestSignIn={onRequestSignIn}
      view={accountTab}
    />
  </div>
{:else}
  <!-- Honest, not empty: the two surfaces on this page are Admin-only reads
       (/api/auth/users, /api/peers), so an anonymous visitor is told what this
       page is and how to reach it rather than shown a table with nothing in it.
       The public peer directory is on PEERS. -->
  <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
    <span class="glyph" style="color:{theme.cyan};">⬡</span>
    Operator keys and the trust registry are administrator surfaces. Sign in to manage them —
    the public peer directory is on PEERS.
  </div>
  {#if !session}
    <div class="signin-row">
      <GBtn title="Sign in with your wallet key" variant="primary" onclick={onRequestSignIn}>SIGN IN</GBtn>
    </div>
  {/if}
{/if}

<style>
  /* ACCOUNTS' tab bar. Same control vocabulary as the PEER MAP's 3D/2D switch, so
     the page has one idea of what a segmented control looks like. */
  .tabbar {
    display: inline-flex;
    border: 1px solid;
    margin-bottom: var(--sdn-sp-7);
    align-self: flex-start;
  }
  .tab {
    border: 0;
    cursor: pointer;
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.12em;
    padding: var(--sdn-sp-3) var(--sdn-sp-8);
  }
  .tab + .tab { border-left: 1px solid; }
  .signin-row { margin-top: var(--sdn-sp-6); }
  .account-admin { margin-top: var(--sdn-sp-7); flex: none; }
  .empty {
    display: flex;
    align-items: center;
    gap: 12px;
    border: 1px solid;
    padding: 26px 28px;
    font-size: var(--sdn-fs-lead); line-height: var(--sdn-lh-lead);
    letter-spacing: 0.06em;
    max-width: 560px;
  }
  .empty .glyph { font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title); }
</style>
