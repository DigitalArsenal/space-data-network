<script lang="ts">
  export let label: string;
  export let value: string | number | null | undefined = null;
  export let tone: 'neutral' | 'online' | 'warning' | 'critical' | 'special' = 'neutral';
</script>

<span class="sdn-chip" data-state={tone}>
  {#if label}
    <span class="sdn-chip-label">{label}</span>
  {/if}
  <strong>{value ?? 'pending'}</strong>
</span>
