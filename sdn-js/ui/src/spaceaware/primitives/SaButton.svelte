<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant = 'neutral',
    title,
    onclick = undefined,
    disabled = false,
    children,
  }: {
    variant?: 'primary' | 'neutral' | 'destructive';
    /** Required: every actionable control carries a `title` tooltip. */
    title: string;
    onclick?: (event: MouseEvent) => void;
    disabled?: boolean;
    children: Snippet;
  } = $props();

  const variantClass = $derived(
    variant === 'primary'
      ? 'sa-btn sa-btn--primary'
      : variant === 'destructive'
        ? 'sa-btn sa-btn--destructive'
        : 'sa-btn',
  );
</script>

<button type="button" class={variantClass} {title} {onclick} {disabled}>
  {@render children()}
</button>

<style>
  button:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
</style>
