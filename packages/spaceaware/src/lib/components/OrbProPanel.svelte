<svelte:options runes={true} />

<script>
  import FreeFeatures from "./FreeFeatures.svelte";
  import { setOrbProState, orbProState } from "../stores/orbProStore.js";
  import { flyToIssContext, hasEmbeddedOrbPro, hasLoadedOrbPro, loadOrbPro } from "../orbpro/orbProLoader.js";

  let containerId = $state("orbproContainer");
  let canFly = $derived($orbProState.loaded && !$orbProState.loading);

  async function handleLoadOrbPro() {
    if (hasLoadedOrbPro()) {
      setOrbProState({
        loaded: true,
        loading: false,
        kind: "good",
        message: "OrbPro already loaded.",
      });
      return;
    }

    if (!hasEmbeddedOrbPro()) {
      setOrbProState({
        loaded: false,
        loading: false,
        kind: "bad",
        message: "OrbPro module URL is not configured for this build.",
      });
      return;
    }

    setOrbProState({
      loading: true,
      kind: "warn",
      message: "Loading OrbPro module...",
    });

    try {
      await loadOrbPro(containerId);
      setOrbProState({
        loaded: true,
        loading: false,
        kind: "good",
        message: "OrbPro loaded. Globe is live.",
      });
    } catch (error) {
      setOrbProState({
        loaded: false,
        loading: false,
        kind: "bad",
        message: `OrbPro load failed: ${String(error)}`,
      });
    }
  }

  function handleFlyIss() {
    if (!flyToIssContext()) {
      setOrbProState({
        kind: "warn",
        message: "Load OrbPro first.",
      });
    }
  }
</script>

<section class="card">
  <h2>OrbPro Viewer</h2>
  <p class="small">
    OrbPro is loaded lazily from the local `/Build` static path. Use this to
    inspect globe context while running free-tier queries.
  </p>

  <div class="actions">
    <button onclick={handleLoadOrbPro} disabled={$orbProState.loading}>
      {$orbProState.loading ? "Loading..." : "Load OrbPro Globe"}
    </button>
    <button class="alt" onclick={handleFlyIss} disabled={!canFly}>
      Fly To ISS Context
    </button>
  </div>
  <div class={`status ${$orbProState.kind || ""}`}>{$orbProState.message}</div>
  <div id={containerId} class="globe"></div>

  <FreeFeatures />
</section>
