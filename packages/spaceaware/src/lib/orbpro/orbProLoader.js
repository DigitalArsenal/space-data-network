import { ORBPRO_BASE_URL, ORBPRO_ESM_MODULE_URL, ORBPRO_KEY_BROKER_URL } from "./embeddedOrbPro.js";

const runtime = {
  cesium: null,
  viewer: null,
};

function isAbsoluteOrRootPath(value) {
  return /^[a-zA-Z][a-zA-Z\d+\-.]*:/.test(value) || value.startsWith("/");
}

function resolveRuntimeBaseUrl() {
  const url = new URL(window.location.href);
  url.search = "";
  url.hash = "";

  if (url.pathname.endsWith("/")) {
    return url.toString();
  }

  const parts = url.pathname.split("/").filter(Boolean);
  if (parts.length === 2 && (parts[0] === "ipfs" || parts[0] === "ipns")) {
    url.pathname = `${url.pathname}/`;
    return url.toString();
  }

  const slash = url.pathname.lastIndexOf("/");
  url.pathname = slash >= 0 ? url.pathname.slice(0, slash + 1) : "/";
  return url.toString();
}

function resolveAssetUrl(pathValue) {
  if (isAbsoluteOrRootPath(pathValue)) {
    return pathValue;
  }
  return new URL(pathValue, resolveRuntimeBaseUrl()).toString();
}

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

  const moduleUrl = resolveAssetUrl(ORBPRO_ESM_MODULE_URL);
  window.CESIUM_BASE_URL = resolveAssetUrl(ORBPRO_BASE_URL);

  // Set key broker URL before OrbPro import so protection runtime can find it.
  if (ORBPRO_KEY_BROKER_URL && !ORBPRO_KEY_BROKER_URL.startsWith("__")) {
    globalThis.__ORBPRO_KEY_BROKER_URL__ = resolveAssetUrl(ORBPRO_KEY_BROKER_URL);
  }

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
