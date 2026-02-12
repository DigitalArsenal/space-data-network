<svelte:options runes={true} />

<script>
  import { onMount } from "svelte";
  import Hero from "./lib/components/Hero.svelte";
  import FreeQueryConsole from "./lib/components/FreeQueryConsole.svelte";
  import OrbProPanel from "./lib/components/OrbProPanel.svelte";
  import AppFooter from "./lib/components/AppFooter.svelte";
  import { hydrateQueryFromUrl } from "./lib/stores/queryStore.js";

  let buildStamp = $state(__SPACEAWARE_BUILD_STAMP__);
  let orbProModuleUrl = $state(__SPACEAWARE_ORBPRO_MODULE_URL__);
  let orbProBaseUrl = $state(__SPACEAWARE_ORBPRO_BASE_URL__);

  onMount(() => {
    hydrateQueryFromUrl(window.location.href);
  });
</script>

<div class="page">
  <Hero />

  <div class="grid">
    <FreeQueryConsole />
    <OrbProPanel />
  </div>

  <AppFooter {buildStamp} {orbProModuleUrl} {orbProBaseUrl} />
</div>
