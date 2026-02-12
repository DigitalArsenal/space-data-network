<svelte:options runes={true} />

<script>
  let { buildStamp, orbProModuleUrl, orbProBaseUrl } = $props();
</script>

<div class="footer">
  Build: {buildStamp}
  <br />
  OrbPro Module URL: {orbProModuleUrl}
  <br />
  OrbPro Base URL: {orbProBaseUrl}
</div>
