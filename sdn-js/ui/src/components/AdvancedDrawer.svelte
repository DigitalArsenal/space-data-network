<script lang="ts">
  export let open = false;
  export let title = 'Advanced';
</script>

{#if open}
  <aside class="sdn-drawer" aria-label={title}>
    <h2>{title}</h2>
    <slot />
  </aside>
{/if}
