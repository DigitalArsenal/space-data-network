import { derived, writable } from "svelte/store";
import { defaultApiBase, utcDayNow } from "../api/dataApi.js";
import { readShareState } from "../share/shareLinks.js";

export const endpointFieldVisibility = {
  health: [],
  omm: ["day", "norad", "limit", "format"],
  mpe: ["day", "entity", "limit", "format"],
  cat: ["norad", "limit", "format"],
};

function initialQueryParams() {
  return {
    apiBase: defaultApiBase(),
    endpoint: "omm",
    day: utcDayNow(),
    noradCatId: "25544",
    entityId: "1998-067A",
    limit: "5",
    format: "flatbuffers",
  };
}

export const queryParams = writable(initialQueryParams());
export const queryStatus = writable({ kind: "", message: "" });
export const queryResult = writable("No query yet.");
export const queryBusy = writable(false);

export const queryVisibleFields = derived(queryParams, ($params) => {
  return new Set(endpointFieldVisibility[$params.endpoint] || []);
});

export function patchQueryParams(patch) {
  queryParams.update((current) => ({ ...current, ...patch }));
}

export function setQueryStatus(message, kind = "") {
  queryStatus.set({ message, kind });
}

export function hydrateQueryFromUrl(href) {
  const shared = readShareState(href);
  if (!shared) {
    return;
  }

  const patch = {};
  if (shared.apiBase) {
    patch.apiBase = shared.apiBase;
  }
  if (shared.endpoint) {
    patch.endpoint = shared.endpoint;
  }
  if (shared.day) {
    patch.day = shared.day;
  }
  if (shared.noradCatId) {
    patch.noradCatId = shared.noradCatId;
  }
  if (shared.entityId) {
    patch.entityId = shared.entityId;
  }
  if (shared.limit) {
    patch.limit = shared.limit;
  }
  if (shared.format) {
    patch.format = shared.format;
  }

  patchQueryParams(patch);
}
