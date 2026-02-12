<svelte:options runes={true} />

<script>
  let { buildStamp, orbProPath } = $props();
</script>

<div class="footer">
  Build: {buildStamp}
  <br />
  OrbPro Source: {orbProPath}
</div>
