const ORBPRO_ESM_SOURCE = __ORBPRO_ESM_SOURCE__;

const DEFAULT_API_BASE = (() => {
  if (window.location.protocol === "http:" || window.location.protocol === "https:") {
    return `${window.location.protocol}//${window.location.host}`;
  }
  return "https://spaceaware.io";
})();

const endpointFieldVisibility = {
  health: ["none"],
  omm: ["day", "norad", "limit", "format"],
  mpe: ["day", "entity", "limit", "format"],
  cat: ["norad", "limit", "format"],
};

const state = {
  cesium: null,
  viewer: null,
  orbProLoaded: false,
};

const els = {
  apiBase: document.getElementById("apiBase"),
  endpoint: document.getElementById("endpoint"),
  day: document.getElementById("day"),
  noradCatId: document.getElementById("noradCatId"),
  entityId: document.getElementById("entityId"),
  limit: document.getElementById("limit"),
  format: document.getElementById("format"),
  runQuery: document.getElementById("runQuery"),
  copyShare: document.getElementById("copyShare"),
  copyRequest: document.getElementById("copyRequest"),
  queryStatus: document.getElementById("queryStatus"),
  result: document.getElementById("result"),
  loadOrbPro: document.getElementById("loadOrbPro"),
  flyIss: document.getElementById("flyIss"),
  orbStatus: document.getElementById("orbStatus"),
  orbproContainer: document.getElementById("orbproContainer"),
};

function setStatus(el, message, kind) {
  el.textContent = message;
  el.className = `status${kind ? ` ${kind}` : ""}`;
}

function utcDayNow() {
  return new Date().toISOString().slice(0, 10);
}

function normalizeApiBase(value) {
  const trimmed = (value || "").trim();
  if (!trimmed) {
    return DEFAULT_API_BASE;
  }
  return trimmed.replace(/\/+$/, "");
}

function buildParams() {
  return {
    api: normalizeApiBase(els.apiBase.value),
    endpoint: els.endpoint.value,
    day: (els.day.value || "").trim(),
    norad: (els.noradCatId.value || "").trim(),
    entity: (els.entityId.value || "").trim(),
    limit: (els.limit.value || "").trim(),
    format: els.format.value,
  };
}

function applyFieldVisibility() {
  const visible = endpointFieldVisibility[els.endpoint.value] || [];
  const rows = document.querySelectorAll("[data-field]");
  for (const row of rows) {
    const field = row.getAttribute("data-field");
    row.style.display = visible.includes(field) ? "grid" : "none";
  }
}

function validateParams(params) {
  if (!params.api.startsWith("http://") && !params.api.startsWith("https://")) {
    return "API Base URL must start with http:// or https://";
  }

  if (params.endpoint === "omm" || params.endpoint === "cat") {
    if (!params.norad || Number.isNaN(Number(params.norad)) || Number(params.norad) <= 0) {
      return "NORAD CAT ID must be a positive number";
    }
  }

  if (params.endpoint === "mpe" && !params.entity) {
    return "Entity ID is required for MPE queries";
  }

  if ((params.endpoint === "omm" || params.endpoint === "mpe") && !params.day) {
    return "Day is required for OMM/MPE queries";
  }

  if (params.endpoint !== "health") {
    const limit = Number(params.limit || "0");
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) {
      return "Limit must be an integer from 1 to 1000";
    }
  }

  return "";
}

function buildApiPath(params) {
  const q = new URLSearchParams();

  if (params.endpoint === "health") {
    return "/api/v1/data/health";
  }

  q.set("limit", params.limit);
  q.set("format", params.format);

  if (params.endpoint === "omm") {
    q.set("norad_cat_id", params.norad);
    q.set("day", params.day);
    return `/api/v1/data/omm?${q.toString()}`;
  }

  if (params.endpoint === "mpe") {
    q.set("entity_id", params.entity);
    q.set("day", params.day);
    return `/api/v1/data/mpe?${q.toString()}`;
  }

  q.set("norad_cat_id", params.norad);
  return `/api/v1/data/cat?${q.toString()}`;
}

function buildRequestUrl(params) {
  return `${params.api}${buildApiPath(params)}`;
}

function toShareQuery(params) {
  const q = new URLSearchParams();
  q.set("api", params.api);
  q.set("e", params.endpoint);
  if (params.day) {
    q.set("d", params.day);
  }
  if (params.norad) {
    q.set("n", params.norad);
  }
  if (params.entity) {
    q.set("x", params.entity);
  }
  if (params.limit) {
    q.set("l", params.limit);
  }
  q.set("f", params.format);
  return q;
}

function applyShareQuery() {
  const q = new URLSearchParams(window.location.search);
  if (!q.size) {
    return;
  }

  const api = q.get("api");
  const endpoint = q.get("e");

  if (api) {
    els.apiBase.value = api;
  }
  if (endpoint && ["health", "omm", "mpe", "cat"].includes(endpoint)) {
    els.endpoint.value = endpoint;
  }
  if (q.get("d")) {
    els.day.value = q.get("d");
  }
  if (q.get("n")) {
    els.noradCatId.value = q.get("n");
  }
  if (q.get("x")) {
    els.entityId.value = q.get("x");
  }
  if (q.get("l")) {
    els.limit.value = q.get("l");
  }
  if (q.get("f") && ["json", "flatbuffers"].includes(q.get("f"))) {
    els.format.value = q.get("f");
  }
}

async function copyText(value, successMsg) {
  try {
    await navigator.clipboard.writeText(value);
    setStatus(els.queryStatus, successMsg, "good");
  } catch (err) {
    setStatus(els.queryStatus, `Clipboard copy failed: ${String(err)}`, "bad");
  }
}

function hexPreview(bytes, max = 96) {
  const slice = bytes.slice(0, max);
  return Array.from(slice, (b) => b.toString(16).padStart(2, "0")).join(" ");
}

function parseLengthPrefixedFrames(bytes) {
  let offset = 0;
  let frames = 0;
  while (offset + 4 <= bytes.length) {
    const len =
      (bytes[offset] << 24) |
      (bytes[offset + 1] << 16) |
      (bytes[offset + 2] << 8) |
      bytes[offset + 3];
    offset += 4;
    if (len < 0 || offset + len > bytes.length) {
      return { frames, truncated: true };
    }
    offset += len;
    frames++;
  }
  return { frames, truncated: offset !== bytes.length };
}

async function runQuery() {
  const params = buildParams();
  const validationError = validateParams(params);
  if (validationError) {
    setStatus(els.queryStatus, validationError, "bad");
    return;
  }

  const url = buildRequestUrl(params);
  setStatus(els.queryStatus, "Running query...", "warn");
  els.result.textContent = "Loading...";
  els.runQuery.disabled = true;

  try {
    const accept =
      params.endpoint === "health" || params.format === "json"
        ? "application/json"
        : "application/x-flatbuffers";

    const response = await fetch(url, {
      method: "GET",
      headers: { Accept: accept },
      cache: "no-store",
    });

    const contentType = response.headers.get("content-type") || "unknown";

    if (!response.ok) {
      const text = await response.text();
      setStatus(els.queryStatus, `Request failed (${response.status})`, "bad");
      els.result.textContent = text || `HTTP ${response.status}`;
      return;
    }

    if (contentType.includes("application/json")) {
      const json = await response.json();
      setStatus(
        els.queryStatus,
        `OK: JSON response (${response.status})`,
        "good",
      );
      els.result.textContent = JSON.stringify(json, null, 2);
      return;
    }

    const bytes = new Uint8Array(await response.arrayBuffer());
    const parsed = parseLengthPrefixedFrames(bytes);
    setStatus(
      els.queryStatus,
      `OK: FlatBuffers response (${bytes.length} bytes)`,
      "good",
    );
    els.result.textContent = JSON.stringify(
      {
        endpoint: params.endpoint,
        request_url: url,
        content_type: contentType,
        bytes: bytes.length,
        length_prefixed_frames: parsed.frames,
        frame_parse_truncated: parsed.truncated,
        head_hex: hexPreview(bytes),
      },
      null,
      2,
    );
  } catch (err) {
    setStatus(els.queryStatus, `Request error: ${String(err)}`, "bad");
    els.result.textContent = String(err);
  } finally {
    els.runQuery.disabled = false;
  }
}

async function loadOrbPro() {
  if (state.orbProLoaded) {
    setStatus(els.orbStatus, "OrbPro already loaded.", "good");
    return;
  }
  if (!ORBPRO_ESM_SOURCE || ORBPRO_ESM_SOURCE.startsWith("__")) {
    setStatus(els.orbStatus, "OrbPro bundle not embedded in this build.", "bad");
    return;
  }

  els.loadOrbPro.disabled = true;
  setStatus(els.orbStatus, "Loading OrbPro module...", "warn");

  try {
    const moduleURL = URL.createObjectURL(
      new Blob([ORBPRO_ESM_SOURCE], { type: "text/javascript" }),
    );
    const Cesium = await import(moduleURL);
    URL.revokeObjectURL(moduleURL);
    state.cesium = Cesium;

    const blueMarble = Cesium.EmbeddedImageryProvider.createBlueMarble();
    state.viewer = new Cesium.Viewer("orbproContainer", {
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
      Cesium.Viewer.configureEmbeddedImagery(state.viewer, {
        enableLighting: true,
        addNightLayer: true,
      });
    }

    state.viewer.scene.globe.depthTestAgainstTerrain = true;
    state.viewer.clock.shouldAnimate = true;
    state.viewer.clock.multiplier = 60;
    state.viewer.camera.setView({
      destination: Cesium.Cartesian3.fromDegrees(-40, 20, 14000000),
      orientation: { heading: 0, pitch: Cesium.Math.toRadians(-90), roll: 0 },
    });

    state.orbProLoaded = true;
    els.flyIss.disabled = false;
    setStatus(els.orbStatus, "OrbPro loaded. Globe is live.", "good");
  } catch (err) {
    setStatus(els.orbStatus, `OrbPro load failed: ${String(err)}`, "bad");
  } finally {
    els.loadOrbPro.disabled = false;
  }
}

function flyIssContext() {
  if (!state.orbProLoaded || !state.viewer || !state.cesium) {
    setStatus(els.orbStatus, "Load OrbPro first.", "warn");
    return;
  }
  state.viewer.camera.flyTo({
    destination: state.cesium.Cartesian3.fromDegrees(-70, 10, 9000000),
    duration: 1.6,
  });
}

function wire() {
  els.apiBase.value = DEFAULT_API_BASE;
  els.day.value = utcDayNow();
  applyShareQuery();
  applyFieldVisibility();

  els.endpoint.addEventListener("change", () => {
    applyFieldVisibility();
  });

  els.runQuery.addEventListener("click", () => {
    runQuery();
  });

  els.copyRequest.addEventListener("click", () => {
    const params = buildParams();
    const validationError = validateParams(params);
    if (validationError) {
      setStatus(els.queryStatus, validationError, "bad");
      return;
    }
    copyText(buildRequestUrl(params), "Request URL copied.");
  });

  els.copyShare.addEventListener("click", () => {
    const params = buildParams();
    const validationError = validateParams(params);
    if (validationError) {
      setStatus(els.queryStatus, validationError, "bad");
      return;
    }
    const url = new URL(window.location.href);
    url.search = toShareQuery(params).toString();
    copyText(url.toString(), "Beta share link copied.");
  });

  els.loadOrbPro.addEventListener("click", () => {
    loadOrbPro();
  });

  els.flyIss.addEventListener("click", () => {
    flyIssContext();
  });
}

wire();
