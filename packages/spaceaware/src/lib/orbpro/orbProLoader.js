import { ORBPRO_BASE_URL, ORBPRO_ESM_MODULE_URL } from "./embeddedOrbPro.js";

const runtime = {
  cesium: null,
  viewer: null,
};

export function hasEmbeddedOrbPro() {
  return (
    typeof ORBPRO_ESM_MODULE_URL === "string" &&
    ORBPRO_ESM_MODULE_URL.length > 0 &&
    !ORBPRO_ESM_MODULE_URL.startsWith("__")
  );
}

export function hasLoadedOrbPro() {
  return Boolean(runtime.viewer && runtime.cesium);
}

export async function loadOrbPro(containerId) {
  if (hasLoadedOrbPro()) {
    return runtime;
  }

  if (!hasEmbeddedOrbPro()) {
    throw new Error("OrbPro module URL is not configured for this build.");
  }

  const moduleUrl = ORBPRO_ESM_MODULE_URL;
  window.CESIUM_BASE_URL = ORBPRO_BASE_URL;

  const Cesium = await import(moduleUrl);
  runtime.cesium = Cesium;

  const blueMarble = Cesium.EmbeddedImageryProvider.createBlueMarble();
  runtime.viewer = new Cesium.Viewer(containerId, {
    baseLayer: new Cesium.ImageryLayer(blueMarble),
    baseLayerPicker: false,
    geocoder: false,
    animation: false,
    timeline: false,
    homeButton: false,
    sceneModePicker: false,
    navigationHelpButton: false,
    infoBox: false,
    selectionIndicator: false,
  });

  if (typeof Cesium.Viewer.configureEmbeddedImagery === "function") {
    Cesium.Viewer.configureEmbeddedImagery(runtime.viewer, {
      enableLighting: true,
      addNightLayer: true,
    });
  }

  runtime.viewer.scene.globe.depthTestAgainstTerrain = true;
  runtime.viewer.clock.shouldAnimate = true;
  runtime.viewer.clock.multiplier = 60;
  runtime.viewer.camera.setView({
    destination: Cesium.Cartesian3.fromDegrees(-40, 20, 14000000),
    orientation: {
      heading: 0,
      pitch: Cesium.Math.toRadians(-90),
      roll: 0,
    },
  });

  return runtime;
}

export function flyToIssContext() {
  if (!hasLoadedOrbPro()) {
    return false;
  }

  runtime.viewer.camera.flyTo({
    destination: runtime.cesium.Cartesian3.fromDegrees(-70, 10, 9000000),
    duration: 1.6,
  });

  return true;
}
