<script>
  /**
   * Wallet sign-in dialog (graph task nst-node-edit-permissions-ui
   * deliverable 1; wire contract nst-node-admin-contract §1–§5).
   *
   * The recovery phrase is typed here, converted to keys by hd-wallet-wasm
   * IN THIS PAGE, and dropped. It is never stored and never sent: the node
   * receives only the xpub, the Ed25519 public key and a signature over the
   * challenge it issued. Styled only with theme.js tokens + design
   * components.
   */
  import { onMount } from 'svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { apiFetch, describeApiError } from './api.js';
  import { loadWallet, signIn } from './wallet.js';

  /** @type {{ adminConfigured: boolean|null, onClose: () => void, onSignedIn: (s: any) => void }} */
  let { adminConfigured, onClose, onSignedIn } = $props();

  let phrase = $state('');
  let account = $state(0);
  let busy = $state(false);
  let error = $state('');
  let walletState = $state('checking'); // checking | ready | unavailable

  const bootstrap = $derived(adminConfigured === false);

  onMount(() => {
    let cancelled = false;
    loadWallet()
      .then(() => !cancelled && (walletState = 'ready'))
      .catch(() => !cancelled && (walletState = 'unavailable'));
    return () => (cancelled = true);
  });

  async function submit(e) {
    e?.preventDefault?.();
    if (busy) return;
    error = '';
    busy = true;
    try {
      const result = await signIn({
        mnemonic: phrase,
        account: Number(account) || 0,
        post: (path, body) => apiFetch(path, { method: 'POST', body }),
      });
      // Drop the phrase from this component the moment it is no longer needed.
      phrase = '';
      onSignedIn(result);
    } catch (err) {
      error = describeApiError(err);
    } finally {
      busy = false;
    }
  }

  function onKeydown(e) {
    if (e.key === 'Escape' && !busy) onClose();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="overlay" onclick={(e) => e.target === e.currentTarget && !busy && onClose()}>
  <form
    class="modal"
    onsubmit={submit}
    style="background:{theme.panelRaised};border-color:{theme.panelBorder};color:{theme.textBody};"
  >
    <div class="head" style="border-color:{theme.divider};">
      <div class="ttl" style="color:{theme.textBright};">SIGN IN</div>
      <div class="chips">
        {#if walletState === 'ready'}
          <StatusChip label="WALLET READY" color={theme.green} />
        {:else if walletState === 'checking'}
          <StatusChip label="LOADING WALLET" color={theme.amber} />
        {:else}
          <StatusChip label="SIGN-IN UNAVAILABLE" color={theme.red} />
        {/if}
        <button type="button" class="close" style="color:{theme.textMuted};border-color:{theme.hairline};" onclick={onClose} aria-label="Close">✕</button>
      </div>
    </div>

    <div class="body">
      {#if walletState === 'unavailable'}
        <div class="note" style="color:{theme.red};border-color:{theme.hairline};">
          This node does not serve the wallet runtime at <span class="mono">/wallet-wasm/</span>.
          Stage it with <span class="mono">deployment/wallet-wasm/stage-wallet-wasm.sh</span> and reload.
          The dashboard will not load a wallet from anywhere but this node.
        </div>
      {:else}
        {#if bootstrap}
          <div class="note" style="color:{theme.amber};border-color:{theme.hairline};">
            No administrator is registered on this node yet. The first key that signs in
            becomes <span class="mono">Initial Admin</span> at trust <span class="mono">admin</span>,
            and is bound to this node permanently.
          </div>
        {/if}

        <label class="field">
          <span class="k" style="color:{theme.textMuted};">RECOVERY PHRASE</span>
          <textarea
            bind:value={phrase}
            rows="3"
            spellcheck="false"
            autocomplete="off"
            autocapitalize="off"
            autocorrect="off"
            disabled={busy}
            placeholder="your BIP-39 words, separated by spaces"
            style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
          ></textarea>
        </label>

        <label class="field narrow">
          <span class="k" style="color:{theme.textMuted};">ACCOUNT INDEX</span>
          <input
            type="number"
            min="0"
            step="1"
            bind:value={account}
            disabled={busy}
            style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
          />
        </label>
        <div class="hint" style="color:{theme.textFaint};">
          BIP-44 account N — identity <span class="mono">m/44'/0'/N'</span>,
          signing key <span class="mono">m/44'/0'/N'/0'/0'</span>. Use the same N the node was keyed with.
        </div>

        <div class="hint privacy" style="color:{theme.textDim};border-color:{theme.divider};">
          Your phrase stays in this page. The node receives only your xpub, your Ed25519
          public key and a signature over the challenge it issues — never the phrase, never a key.
        </div>
      {/if}

      {#if error}
        <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
      {/if}
    </div>

    <div class="foot" style="border-color:{theme.divider};">
      <!-- GBtn renders a bare <button>, which inside a <form> defaults to
           type=submit; cancelling the click's default action is what keeps
           CANCEL from also submitting. -->
      <GBtn title="Cancel" onclick={(e) => { e.preventDefault(); onClose(); }} disabled={busy}>CANCEL</GBtn>
      <GBtn title="Sign in with this key" variant="primary" disabled={busy || walletState !== 'ready' || !phrase.trim()}>
        {busy ? 'SIGNING IN…' : 'SIGN IN'}
      </GBtn>
    </div>
  </form>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(2, 5, 8, 0.72);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    padding: 28px;
  }
  .modal {
    width: min(560px, 100%);
    max-height: min(88vh, 760px);
    border: 1px solid;
    display: flex;
    flex-direction: column;
    box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    padding: 15px 18px 12px;
    border-bottom: 1px solid;
  }
  .ttl {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 22px;
    letter-spacing: 0.1em;
  }
  .chips { display: flex; gap: 6px; align-items: center; }
  .close {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-size: 14.5px;
    line-height: 1;
    padding: 4px 7px;
  }
  .body { overflow: auto; padding: 15px 18px 4px; display: flex; flex-direction: column; gap: 12px; }
  .field { display: flex; flex-direction: column; gap: 6px; }
  .field.narrow { max-width: 180px; }
  .k { font-size: 12.5px; letter-spacing: 0.16em; }
  textarea,
  input {
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 15px;
    letter-spacing: 0.04em;
    padding: 8px 10px;
    outline: none;
    resize: vertical;
  }
  textarea::placeholder { color: rgba(159, 212, 245, 0.3); }
  .hint { font-size: 12.5px; letter-spacing: 0.04em; line-height: 1.55; }
  .hint.privacy { border-left: 2px solid; padding-left: 10px; }
  .note {
    border: 1px solid;
    padding: 10px 12px;
    font-size: 13px;
    letter-spacing: 0.04em;
    line-height: 1.55;
  }
  .err {
    border: 1px solid;
    padding: 9px 11px;
    font-size: 13px;
    letter-spacing: 0.04em;
    line-height: 1.5;
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    gap: 9px;
    border-top: 1px solid;
    padding: 13px 18px;
    margin-top: 12px;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>
