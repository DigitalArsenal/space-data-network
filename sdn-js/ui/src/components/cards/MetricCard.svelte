<script lang="ts">
  export let title: string;
  export let value: string | number | null | undefined = null;
  export let detail = '';
  export let tone: 'neutral' | 'online' | 'warning' | 'critical' | 'special' = 'neutral';
</script>

<article class="sdn-card sdn-metric" data-tone={tone}>
  <span>{title}</span>
  <strong>{value ?? 'pending'}</strong>
  {#if detail}
    <p>{detail}</p>
  {/if}
</article>
