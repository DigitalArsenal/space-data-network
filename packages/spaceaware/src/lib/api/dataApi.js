export function utcDayNow() {
  return new Date().toISOString().slice(0, 10);
}

export function defaultApiBase() {
  if (
    typeof window !== "undefined" &&
    (window.location.protocol === "http:" || window.location.protocol === "https:")
  ) {
    return `${window.location.protocol}//${window.location.host}`;
  }
  return "https://spaceaware.io";
}

export function normalizeApiBase(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return defaultApiBase();
  }
  return trimmed.replace(/\/+$/, "");
}

export function validateQueryParams(params) {
  if (!params.apiBase.startsWith("http://") && !params.apiBase.startsWith("https://")) {
    return "API Base URL must start with http:// or https://";
  }

  if (params.endpoint === "omm" || params.endpoint === "cat") {
    const norad = Number(params.noradCatId);
    if (!params.noradCatId || Number.isNaN(norad) || norad <= 0) {
      return "NORAD CAT ID must be a positive number";
    }
  }

  if (params.endpoint === "mpe" && !params.entityId) {
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

export function buildApiPath(params) {
  if (params.endpoint === "health") {
    return "/api/v1/data/health";
  }

  const q = new URLSearchParams();
  q.set("limit", String(params.limit));
  q.set("format", params.format);

  if (params.endpoint === "omm") {
    q.set("norad_cat_id", String(params.noradCatId));
    q.set("day", params.day);
    return `/api/v1/data/omm?${q.toString()}`;
  }

  if (params.endpoint === "mpe") {
    q.set("entity_id", params.entityId);
    q.set("day", params.day);
    return `/api/v1/data/mpe?${q.toString()}`;
  }

  q.set("norad_cat_id", String(params.noradCatId));
  return `/api/v1/data/cat?${q.toString()}`;
}

export function buildRequestUrl(params) {
  return `${normalizeApiBase(params.apiBase)}${buildApiPath(params)}`;
}

function previewHex(bytes, max = 96) {
  const slice = bytes.slice(0, max);
  return Array.from(slice, (value) => value.toString(16).padStart(2, "0")).join(" ");
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

export async function runDataQuery(params) {
  const requestUrl = buildRequestUrl(params);
  const accept =
    params.endpoint === "health" || params.format === "json"
      ? "application/json"
      : "application/x-flatbuffers";

  const response = await fetch(requestUrl, {
    method: "GET",
    headers: { Accept: accept },
    cache: "no-store",
  });

  const contentType = response.headers.get("content-type") || "unknown";

  if (!response.ok) {
    const errorText = await response.text();
    return {
      ok: false,
      requestUrl,
      payload: errorText || `HTTP ${response.status}`,
      status: response.status,
    };
  }

  if (contentType.includes("application/json")) {
    const json = await response.json();
    return {
      ok: true,
      requestUrl,
      payload: JSON.stringify(json, null, 2),
      status: response.status,
      contentType,
    };
  }

  const bytes = new Uint8Array(await response.arrayBuffer());
  const parsed = parseLengthPrefixedFrames(bytes);

  return {
    ok: true,
    requestUrl,
    status: response.status,
    contentType,
    payload: JSON.stringify(
      {
        endpoint: params.endpoint,
        request_url: requestUrl,
        content_type: contentType,
        bytes: bytes.length,
        length_prefixed_frames: parsed.frames,
        frame_parse_truncated: parsed.truncated,
        head_hex: previewHex(bytes),
      },
      null,
      2,
    ),
  };
}
