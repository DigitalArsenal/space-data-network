<script lang="ts">
  let {
    tabs,
    selected,
    onselect,
  }: {
    tabs: readonly { id: string; label: string; title: string }[];
    selected: string;
    onselect: (id: string) => void;
  } = $props();
</script>

<div class="sa-tabs" role="tablist">
  {#each tabs as tab (tab.id)}
    <button
      type="button"
      class="sa-tab"
      role="tab"
      aria-selected={tab.id === selected}
      title={tab.title}
      onclick={() => onselect(tab.id)}
    >
      {tab.label}
    </button>
  {/each}
</div>
