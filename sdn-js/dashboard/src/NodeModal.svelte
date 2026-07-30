<script>
  /**
   * Node detail modal — thin wrapper around the shared NodeDetail view (also
   * used by the THIS NODE sub-page), in the ONE modal shell every dialog on
   * this page uses (IRIS ruling 2026-07-29 §3). Esc or overlay click closes.
   * Styled only with theme.js tokens.
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import ModalShell from './ModalShell.svelte';
  import NodeDetail from './NodeDetail.svelte';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId } from './format.js';
  import { accountDisplayName, accountFromNode, isUnnamed } from './accounts.js';

  /** @type {{ node: any, now: number, onClose: () => void }} */
  let { node, now, onClose } = $props();

  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  // Same NAME rule as the ACCOUNTS table (§16.4.3): "unknown", never an id
  // dressed up as a name.
  const acct = $derived(node.account ?? accountFromNode(node));
  const title = $derived(accountDisplayName(acct));
  const unnamed = $derived(isUnnamed(acct));

</script>

<ModalShell
  {title}
  sub={node.org?.trim() && node.org.trim() !== title ? node.org : ''}
  label="Node details"
  {unnamed}
  width="720px"
  {onClose}
>
  {#snippet chips()}
    {#if node.isSelf}<StatusChip label="SELF" color={theme.cyan} dot={false} />{/if}
    <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
    <StatusChip label={node.online ? 'ONLINE' : 'OFFLINE'} color={node.online ? theme.green : theme.textMuted} />
  {/snippet}
  <NodeDetail {node} {now} />
</ModalShell>
