export function buildShareUrl(params, currentHref) {
  const url = new URL(currentHref);
  const q = new URLSearchParams();

  q.set("api", params.apiBase);
  q.set("e", params.endpoint);
  q.set("f", params.format);

  if (params.day) {
    q.set("d", params.day);
  }
  if (params.noradCatId) {
    q.set("n", String(params.noradCatId));
  }
  if (params.entityId) {
    q.set("x", params.entityId);
  }
  if (params.limit) {
    q.set("l", String(params.limit));
  }

  url.search = q.toString();
  return url.toString();
}

export function readShareState(href) {
  const url = new URL(href);
  const q = url.searchParams;
  if (!q.size) {
    return null;
  }

  const endpoint = q.get("e");
  if (endpoint && !["health", "omm", "mpe", "cat"].includes(endpoint)) {
    return null;
  }

  const format = q.get("f");
  if (format && !["json", "flatbuffers"].includes(format)) {
    return null;
  }

  return {
    apiBase: q.get("api") || "",
    endpoint: endpoint || "",
    day: q.get("d") || "",
    noradCatId: q.get("n") || "",
    entityId: q.get("x") || "",
    limit: q.get("l") || "",
    format: format || "",
  };
}
