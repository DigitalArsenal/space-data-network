<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant = 'default',
    title = undefined,
    children,
  }: {
    variant?: 'default' | 'raised' | 'well';
    title?: string;
    children: Snippet;
  } = $props();

  const variantClass = $derived(
    variant === 'raised' ? 'sa-panel sa-panel--raised' : variant === 'well' ? 'sa-panel sa-panel--well' : 'sa-panel',
  );
</script>

<section class={variantClass}>
  {#if title}
    <header class="sa-panel-header">
      <span class="sa-kicker">{title}</span>
    </header>
  {/if}
  <div class="sa-panel-body">
    {@render children()}
  </div>
</section>

<style>
  section {
    display: flex;
    flex-direction: column;
  }
  .sa-panel-header {
    padding: 10px 14px 0 14px;
  }
  .sa-panel-body {
    padding: 12px 14px 14px 14px;
  }
</style>
