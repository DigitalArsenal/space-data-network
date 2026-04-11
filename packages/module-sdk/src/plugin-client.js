const DEFAULT_TIMEOUT_MS = 15_000;
const DEFAULT_IPFS_API_BASE = "/api/v0";
const DEFAULT_SDN_API_BASE = "/api";
const DEFAULT_DATA_API_BASE = "/api/v1/data";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export const DEFAULT_SDN_DISCOVERY_VERSION = "spacedatanetwork/1.0.0";
export const SDS_EXCHANGE_PROTOCOL_ID = "/spacedatanetwork/sds-exchange/1.0.0";

export const SDS_MESSAGE_TYPES = Object.freeze({
  REQUEST_DATA: 0x01,
  PUSH_DATA: 0x02,
  QUERY: 0x03,
  RESPONSE: 0x04,
  ACK: 0x05,
  NACK: 0x06,
});

export const SDS_RESPONSE_CODES = Object.freeze({
  REJECT: 0x00,
  ACCEPT: 0x01,
  RATE_LIMITED: 0x02,
});

const BASE32_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";

export class SDKHTTPError extends Error {
  constructor(message, status, url, body) {
    super(message);
    this.name = "SDKHTTPError";
    this.status = status;
    this.url = url;
    this.body = body;
  }
}

export class WebClient {
  constructor(options = {}) {
    this.baseUrl = options.baseUrl || "";
    this.defaultHeaders = options.defaultHeaders || {};
    this.timeoutMs = asPositiveInt(options.timeoutMs, DEFAULT_TIMEOUT_MS);
    this.fetchImpl = resolveFetch(options.fetchImpl);
  }

  async request(pathOrUrl, init = {}) {
    const url = resolveURL(pathOrUrl, this.baseUrl);
    const timeoutMs = asPositiveInt(init.timeoutMs, this.timeoutMs);
    const headers = mergeHeaders(this.defaultHeaders, init.headers);
    const { signal, cleanup } = withTimeoutSignal(init.signal, timeoutMs);

    try {
      return await this.fetchImpl(url, {
        ...init,
        headers,
        signal,
      });
    } finally {
      cleanup();
    }
  }

  async json(pathOrUrl, init = {}) {
    const response = await this.request(pathOrUrl, init);
    await assertOK(response);

    if (response.status === 204) {
      return null;
    }

    return response.json();
  }

  async text(pathOrUrl, init = {}) {
    const response = await this.request(pathOrUrl, init);
    await assertOK(response);
    return response.text();
  }

  async bytes(pathOrUrl, init = {}) {
    const response = await this.request(pathOrUrl, init);
    await assertOK(response);
    return new Uint8Array(await response.arrayBuffer());
  }
}

export class IPFSClient {
  constructor(webClient, options = {}) {
    this.web = webClient;
    this.ipfsApiBase = trimRightSlash(options.ipfsApiBase || DEFAULT_IPFS_API_BASE);
  }

  async id(init = {}) {
    return this.rpcJSON("id", {}, init);
  }

  async swarmPeers(options = {}, init = {}) {
    const verbose = "verbose" in options ? Boolean(options.verbose) : true;
    return this.rpcJSON("swarm/peers", { verbose }, init);
  }

  async add(data, options = {}, init = {}) {
    const content = asUint8Array(data);
    const filename = String(options.filename || "payload.bin");
    const form = new FormData();
    form.append("file", new Blob([content]), filename);

    const params = {
      pin: "pin" in options ? Boolean(options.pin) : true,
      "cid-version":
        "cidVersion" in options ? Number(options.cidVersion || 0) : undefined,
      hash: options.hash || undefined,
      "raw-leaves":
        "rawLeaves" in options ? Boolean(options.rawLeaves) : undefined,
      "wrap-with-directory":
        "wrapWithDirectory" in options
          ? Boolean(options.wrapWithDirectory)
          : undefined,
    };

    const raw = await this.rpcText("add", params, {
      ...init,
      body: form,
    });

    const events = parseNDJSON(raw);
    if (events.length === 0) {
      throw new Error("ipfs add returned no data");
    }
    return events[events.length - 1];
  }

  async cat(cid, init = {}) {
    const value = String(cid || "").trim();
    if (!value) {
      throw new Error("cid is required");
    }
    return this.rpcBytes("cat", { arg: value }, init);
  }

  async resolveIPNS(nameOrPath, options = {}, init = {}) {
    const path = normalizeIPNSPath(nameOrPath);
    const payload = await this.rpcJSON(
      "name/resolve",
      {
        arg: path,
        recursive: "recursive" in options ? Boolean(options.recursive) : true,
        dhtRecordCount: options.dhtRecordCount,
        dhtTimeout: options.dhtTimeout,
      },
      init,
    );

    return {
      path: payload.Path || payload.path || "",
      raw: payload,
    };
  }

  async publishIPNS(pathOrCID, options = {}, init = {}) {
    const target = normalizeIPFSPath(pathOrCID);
    return this.rpcJSON(
      "name/publish",
      {
        arg: target,
        key: options.key || "self",
        lifetime: options.lifetime || "24h",
        ttl: options.ttl,
        resolve: "resolve" in options ? Boolean(options.resolve) : true,
        allowOffline:
          "allowOffline" in options ? Boolean(options.allowOffline) : true,
      },
      init,
    );
  }

  async findProviders(cid, options = {}, init = {}) {
    const value = String(cid || "").trim();
    if (!value) {
      throw new Error("cid is required");
    }

    const numProviders = asPositiveInt(options.numProviders, 20);
    const raw = await this.rpcText(
      "dht/findprovs",
      { arg: value, "num-providers": numProviders },
      init,
    );
    const events = parseNDJSON(raw);
    const providers = extractProviderResults(events);

    return {
      cid: value,
      providers,
      events,
    };
  }

  async findPeer(peerID, init = {}) {
    const target = String(peerID || "").trim();
    if (!target) {
      throw new Error("peerID is required");
    }

    const raw = await this.rpcText("dht/findpeer", { arg: target }, init);
    const events = parseNDJSON(raw);
    const providers = extractProviderResults(events);
    return {
      peerID: target,
      providers,
      events,
    };
  }

  async findSDNPeers(options = {}, init = {}) {
    const version = String(
      options.version || DEFAULT_SDN_DISCOVERY_VERSION,
    ).trim();
    if (!version) {
      throw new Error("version is required");
    }

    const discoveryCID = await computeSDNDiscoveryCID(version);
    const found = await this.findProviders(discoveryCID, options, init);

    return {
      version,
      discoveryCID,
      providers: found.providers,
      events: found.events,
    };
  }

  async rpcJSON(command, params = {}, init = {}) {
    const path = this.buildRPCPath(command, params);
    return this.web.json(path, {
      method: "POST",
      ...init,
    });
  }

  async rpcText(command, params = {}, init = {}) {
    const path = this.buildRPCPath(command, params);
    return this.web.text(path, {
      method: "POST",
      ...init,
    });
  }

  async rpcBytes(command, params = {}, init = {}) {
    const path = this.buildRPCPath(command, params);
    return this.web.bytes(path, {
      method: "POST",
      ...init,
    });
  }

  buildRPCPath(command, params = {}) {
    const endpoint = String(command || "").replace(/^\/+/, "");
    if (!endpoint) {
      throw new Error("command is required");
    }
    const base = this.ipfsApiBase || DEFAULT_IPFS_API_BASE;
    const query = buildQueryString(params);
    return query ? `${base}/${endpoint}?${query}` : `${base}/${endpoint}`;
  }
}

export class SDNClient {
  constructor(webClient, options = {}) {
    this.web = webClient;
    this.sdnApiBase = trimRightSlash(options.sdnApiBase || DEFAULT_SDN_API_BASE);
  }

  async nodeInfo(init = {}) {
    return this.web.json(`${this.sdnApiBase}/node/info`, {
      method: "GET",
      ...init,
    });
  }

  async peerGraph(init = {}) {
    return this.web.json(`${this.sdnApiBase}/peers/graph`, {
      method: "GET",
      ...init,
    });
  }

  async pluginManifest(init = {}) {
    return this.web.json("/api/v1/plugins/manifest", {
      method: "GET",
      ...init,
    });
  }
}

export class FlatSQLClient {
  constructor(webClient, options = {}) {
    this.web = webClient;
    this.dataApiBase = trimRightSlash(options.dataApiBase || DEFAULT_DATA_API_BASE);
  }

  async health(init = {}) {
    return this.web.json(`${this.dataApiBase}/health`, {
      method: "GET",
      ...init,
    });
  }

  async queryOMM(options = {}, init = {}) {
    const day = String(options.day || "").trim();
    if (!day) {
      throw new Error("day is required");
    }

    const noradCatID = requiredPositiveInt(
      options.noradCatID ?? options.noradCatId,
    );
    if (!noradCatID) {
      throw new Error("noradCatID must be a positive integer");
    }

    const secure = Boolean(options.secure);
    const query = {
      day,
      norad_cat_id: noradCatID,
      limit: asPositiveInt(options.limit, 100),
      include_data:
        "includeData" in options ? Boolean(options.includeData) : undefined,
      format: normalizeDataFormat(options.format),
    };

    const headers = buildSecureHeaders(options);
    const path = secure ? `${this.dataApiBase}/secure/omm` : `${this.dataApiBase}/omm`;
    return this.fetchData(path, query, headers, init);
  }

  async queryMPE(options = {}, init = {}) {
    const day = String(options.day || "").trim();
    if (!day) {
      throw new Error("day is required");
    }

    const entityID = String(options.entityID || options.entityId || "").trim();
    if (!entityID) {
      throw new Error("entityID is required");
    }

    const query = {
      day,
      entity_id: entityID,
      limit: asPositiveInt(options.limit, 100),
      include_data:
        "includeData" in options ? Boolean(options.includeData) : undefined,
      format: normalizeDataFormat(options.format),
    };

    return this.fetchData(`${this.dataApiBase}/mpe`, query, {}, init);
  }

  async queryCAT(options = {}, init = {}) {
    const noradCatID = requiredPositiveInt(
      options.noradCatID ?? options.noradCatId,
    );
    if (!noradCatID) {
      throw new Error("noradCatID must be a positive integer");
    }

    const query = {
      norad_cat_id: noradCatID,
      limit: asPositiveInt(options.limit, 5),
      include_data:
        "includeData" in options ? Boolean(options.includeData) : undefined,
      format: normalizeDataFormat(options.format),
    };

    return this.fetchData(`${this.dataApiBase}/cat`, query, {}, init);
  }

  async fetchData(path, query, headers, init) {
    const queryString = buildQueryString(query);
    const url = queryString ? `${path}?${queryString}` : path;
    const format = normalizeDataFormat(query.format);
    const request = {
      ...init,
      method: "GET",
      headers: mergeHeaders(headers, init?.headers),
    };

    if (format === "flatbuffers") {
      return this.web.bytes(url, request);
    }
    return this.web.json(url, request);
  }
}

export class SDSExchangeClient {
  constructor(options = {}) {
    this.protocolID = options.sdsProtocolID || SDS_EXCHANGE_PROTOCOL_ID;
    this.timeoutMs = asPositiveInt(options.streamTimeoutMs, DEFAULT_TIMEOUT_MS);
  }

  async pushData(options = {}) {
    if (options.signature !== undefined) {
      throw new Error("options.signature is not supported in SDS v1");
    }

    const schemaName = toSchemaName(options.schemaName);
    const schemaBytes = encoder.encode(schemaName);
    const data = asUint8Array(options.data);

    const payload = concatBytes([
      Uint8Array.of(SDS_MESSAGE_TYPES.PUSH_DATA),
      u16be(schemaBytes.length),
      schemaBytes,
      u32be(data.length),
      data,
    ]);

    const response = await this.exchange(options, payload);
    if (response.length < 1) {
      throw new Error("empty SDS push response");
    }

    const code = response[0];
    if (code !== SDS_RESPONSE_CODES.ACCEPT) {
      throw new Error(`SDS push rejected (${describeResponseCode(code)})`);
    }

    const cid = decoder.decode(response.slice(1)).trim();
    return {
      responseCode: code,
      cid,
    };
  }

  async requestData(options = {}) {
    const schemaName = toSchemaName(options.schemaName);
    const schemaBytes = encoder.encode(schemaName);
    const cid = String(options.cid || "").trim();
    if (!cid) {
      throw new Error("cid is required");
    }
    const cidBytes = encoder.encode(cid);

    const payload = concatBytes([
      Uint8Array.of(SDS_MESSAGE_TYPES.REQUEST_DATA),
      u16be(schemaBytes.length),
      schemaBytes,
      u16be(cidBytes.length),
      cidBytes,
    ]);

    const response = await this.exchange(options, payload);
    if (response.length < 5) {
      throw new Error("invalid SDS request_data response");
    }

    const code = response[0];
    if (code !== SDS_RESPONSE_CODES.ACCEPT) {
      throw new Error(`SDS request_data rejected (${describeResponseCode(code)})`);
    }

    const dataLength = readU32BE(response, 1);
    const start = 5;
    const end = start + dataLength;
    if (response.length < end) {
      throw new Error("truncated SDS request_data payload");
    }

    return response.slice(start, end);
  }

  async query(options = {}) {
    const schemaName = toSchemaName(options.schemaName);
    const schemaBytes = encoder.encode(schemaName);
    const queryString = String(options.query || "");
    const queryBytes = encoder.encode(queryString);

    const payload = concatBytes([
      Uint8Array.of(SDS_MESSAGE_TYPES.QUERY),
      u16be(schemaBytes.length),
      schemaBytes,
      u32be(queryBytes.length),
      queryBytes,
    ]);

    const response = await this.exchange(options, payload);
    if (response.length < 5) {
      throw new Error("invalid SDS query response");
    }

    const code = response[0];
    if (code !== SDS_RESPONSE_CODES.ACCEPT) {
      throw new Error(`SDS query rejected (${describeResponseCode(code)})`);
    }

    const count = readU32BE(response, 1);
    const out = [];
    let offset = 5;
    for (let i = 0; i < count; i++) {
      if (offset + 4 > response.length) {
        throw new Error("truncated SDS query record header");
      }
      const itemLength = readU32BE(response, offset);
      offset += 4;
      const end = offset + itemLength;
      if (end > response.length) {
        throw new Error("truncated SDS query record payload");
      }
      out.push(response.slice(offset, end));
      offset = end;
    }

    return out;
  }

  async exchange(options, payload) {
    const stream = await dialSDSStream(
      options.dialProtocol,
      options.target,
      options.protocolID || this.protocolID,
      asPositiveInt(options.timeoutMs, this.timeoutMs),
    );

    try {
      const timeoutMs = asPositiveInt(options.timeoutMs, this.timeoutMs);
      await withTimeout(stream.sink([payload]), timeoutMs, "SDS write");
      return withTimeout(readAll(stream.source), timeoutMs, "SDS read");
    } finally {
      if (stream && typeof stream.close === "function") {
        try {
          await stream.close();
        } catch {
          // best effort
        }
      }
    }
  }
}

export function createPluginSDK(options = {}) {
  const web = new WebClient(options);
  return {
    web,
    ipfs: new IPFSClient(web, options),
    sdn: new SDNClient(web, options),
    flatsql: new FlatSQLClient(web, options),
    sdsExchange: new SDSExchangeClient(options),
  };
}

// computeSDNDiscoveryCID matches sdn-server/internal/node discovery hashing:
// CIDv1(raw, sha2-256("spacedatanetwork/<version>")) encoded in base32.
export async function computeSDNDiscoveryCID(
  version = DEFAULT_SDN_DISCOVERY_VERSION,
) {
  const normalized = String(version || "").trim();
  if (!normalized) {
    throw new Error("version is required");
  }

  const hash = await sha256(encoder.encode(normalized));
  if (hash.length !== 32) {
    throw new Error("unexpected sha256 length");
  }

  // CIDv1 raw codec + multihash(sha2-256)
  const bytes = new Uint8Array(2 + 2 + hash.length);
  bytes[0] = 0x01; // CIDv1
  bytes[1] = 0x55; // raw codec
  bytes[2] = 0x12; // sha2-256
  bytes[3] = 0x20; // 32-byte digest length
  bytes.set(hash, 4);

  return `b${base32Encode(bytes)}`;
}

export const computeSdnDiscoveryCID = computeSDNDiscoveryCID;

async function sha256(data) {
  if (globalThis.crypto && globalThis.crypto.subtle) {
    const digest = await globalThis.crypto.subtle.digest("SHA-256", data);
    return new Uint8Array(digest);
  }

  // Node fallback when WebCrypto is unavailable.
  try {
    const mod = await import("node:crypto");
    return new Uint8Array(
      mod.createHash("sha256").update(Buffer.from(data)).digest(),
    );
  } catch (error) {
    throw new Error(`SHA-256 unavailable: ${error.message}`);
  }
}

function base32Encode(bytes) {
  let out = "";
  let bits = 0;
  let value = 0;
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += BASE32_ALPHABET[(value >>> (bits - 5)) & 0x1f];
      bits -= 5;
    }
  }
  if (bits > 0) {
    out += BASE32_ALPHABET[(value << (5 - bits)) & 0x1f];
  }
  return out;
}

async function assertOK(response) {
  if (response.ok) {
    return;
  }
  const body = await response.text();
  throw new SDKHTTPError(
    `HTTP ${response.status} for ${response.url}`,
    response.status,
    response.url,
    body,
  );
}

function resolveFetch(fetchImpl) {
  if (typeof fetchImpl === "function") {
    return fetchImpl;
  }
  if (typeof fetch === "function") {
    return fetch;
  }
  throw new Error("fetch implementation is required");
}

function resolveURL(pathOrURL, baseURL) {
  const value = String(pathOrURL || "").trim();
  if (!value) {
    throw new Error("request url is required");
  }
  if (/^[a-zA-Z][a-zA-Z\d+\-.]*:/.test(value)) {
    return value;
  }
  if (!baseURL) {
    return value;
  }
  return new URL(value, ensureTrailingSlash(baseURL)).toString();
}

function mergeHeaders(defaultHeaders, overrideHeaders) {
  const headers = new Headers(defaultHeaders || {});
  if (!overrideHeaders) {
    return headers;
  }

  const incoming = new Headers(overrideHeaders);
  for (const [key, value] of incoming.entries()) {
    headers.set(key, value);
  }
  return headers;
}

function withTimeoutSignal(parentSignal, timeoutMs) {
  const controller = new AbortController();
  const onAbort = () => controller.abort(parentSignal.reason);

  if (parentSignal) {
    if (parentSignal.aborted) {
      controller.abort(parentSignal.reason);
    } else {
      parentSignal.addEventListener("abort", onAbort, { once: true });
    }
  }

  const timer = setTimeout(() => {
    controller.abort(new Error(`request timed out after ${timeoutMs}ms`));
  }, timeoutMs);

  return {
    signal: controller.signal,
    cleanup: () => {
      clearTimeout(timer);
      if (parentSignal) {
        parentSignal.removeEventListener("abort", onAbort);
      }
    },
  };
}

function buildQueryString(params = {}) {
  const query = new URLSearchParams();
  for (const [key, raw] of Object.entries(params)) {
    if (raw === undefined || raw === null || raw === "") {
      continue;
    }

    if (Array.isArray(raw)) {
      for (const item of raw) {
        if (item === undefined || item === null || item === "") {
          continue;
        }
        query.append(key, String(item));
      }
      continue;
    }

    if (typeof raw === "boolean") {
      query.set(key, raw ? "true" : "false");
      continue;
    }

    query.set(key, String(raw));
  }
  return query.toString();
}

function trimRightSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function ensureTrailingSlash(value) {
  return /\/$/.test(value) ? value : `${value}/`;
}

function normalizeIPNSPath(nameOrPath) {
  const value = String(nameOrPath || "").trim();
  if (!value) {
    throw new Error("ipns name/path is required");
  }
  if (value.startsWith("/ipns/")) {
    return value;
  }
  return `/ipns/${value}`;
}

function normalizeIPFSPath(pathOrCID) {
  const value = String(pathOrCID || "").trim();
  if (!value) {
    throw new Error("pathOrCID is required");
  }
  if (value.startsWith("/ipfs/") || value.startsWith("/ipns/")) {
    return value;
  }
  return `/ipfs/${value}`;
}

function parseNDJSON(rawText) {
  const lines = String(rawText || "")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);

  const events = [];
  for (const line of lines) {
    try {
      events.push(JSON.parse(line));
    } catch {
      // Ignore non-JSON progress lines.
    }
  }
  return events;
}

function extractProviderResults(events) {
  const byPeerID = new Map();
  for (const event of events) {
    const responses = Array.isArray(event?.Responses)
      ? event.Responses
      : event?.ID
        ? [event]
        : [];
    for (const item of responses) {
      const id = String(item?.ID || item?.Id || item?.id || "").trim();
      if (!id) {
        continue;
      }

      const addrs = normalizeStringArray(
        item?.Addrs || item?.Addresses || item?.Multiaddrs || [],
      );
      if (!byPeerID.has(id)) {
        byPeerID.set(id, { id, addrs: [] });
      }

      const existing = byPeerID.get(id);
      const merged = new Set([...existing.addrs, ...addrs]);
      existing.addrs = [...merged];
    }
  }
  return [...byPeerID.values()];
}

function normalizeStringArray(input) {
  if (Array.isArray(input)) {
    return input
      .map((item) => String(item || "").trim())
      .filter(Boolean);
  }
  const value = String(input || "").trim();
  return value ? [value] : [];
}

function buildSecureHeaders(options) {
  const headers = {};
  const authHeader = String(
    options.authorization || options.authHeader || "",
  ).trim();
  const peerID = String(options.peerID || options.peerId || "").trim();

  if (authHeader) {
    headers.Authorization = authHeader;
  }
  if (peerID) {
    headers["X-SDN-Peer-ID"] = peerID;
  }
  return headers;
}

function normalizeDataFormat(format) {
  if (format === undefined || format === null || format === "") {
    return "json";
  }
  const value = String(format).toLowerCase();
  if (value === "flatbuffers" || value === "fb" || value === "binary") {
    return "flatbuffers";
  }
  return "json";
}

function asPositiveInt(value, fallback) {
  if (value === undefined || value === null || value === "") {
    return fallback;
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return fallback;
  }
  return Math.floor(parsed);
}

function requiredPositiveInt(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return Math.floor(parsed);
}

function asUint8Array(value) {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (typeof value === "string") {
    return encoder.encode(value);
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  throw new Error("expected Uint8Array, ArrayBuffer, DataView, or string");
}

function concatBytes(chunks) {
  let total = 0;
  for (const chunk of chunks) {
    total += chunk.length;
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

function u16be(value) {
  if (!Number.isInteger(value) || value < 0 || value > 0xffff) {
    throw new Error("u16 value out of range");
  }
  const out = new Uint8Array(2);
  out[0] = (value >> 8) & 0xff;
  out[1] = value & 0xff;
  return out;
}

function u32be(value) {
  if (!Number.isInteger(value) || value < 0 || value > 0xffffffff) {
    throw new Error("u32 value out of range");
  }
  const out = new Uint8Array(4);
  out[0] = (value >>> 24) & 0xff;
  out[1] = (value >>> 16) & 0xff;
  out[2] = (value >>> 8) & 0xff;
  out[3] = value & 0xff;
  return out;
}

function readU32BE(buffer, offset) {
  return (
    ((buffer[offset] << 24) >>> 0) |
    (buffer[offset + 1] << 16) |
    (buffer[offset + 2] << 8) |
    buffer[offset + 3]
  );
}

function toSchemaName(value) {
  const schema = String(value || "").trim();
  if (!schema) {
    throw new Error("schemaName is required");
  }
  if (encoder.encode(schema).length > 0xffff) {
    throw new Error("schemaName exceeds protocol limit");
  }
  return schema;
}

async function dialSDSStream(dialProtocol, target, protocolID, timeoutMs) {
  if (!dialProtocol) {
    throw new Error("dialProtocol is required");
  }

  if (typeof dialProtocol === "function") {
    return withTimeout(
      Promise.resolve(dialProtocol(target, protocolID)),
      timeoutMs,
      "SDS dial",
    );
  }

  if (typeof dialProtocol.dialProtocol === "function") {
    if (!target) {
      throw new Error("target is required when passing a libp2p node");
    }
    return withTimeout(
      Promise.resolve(dialProtocol.dialProtocol(target, protocolID)),
      timeoutMs,
      "SDS dial",
    );
  }

  throw new Error("dialProtocol must be a function or an object with dialProtocol()");
}

async function readAll(source) {
  const chunks = [];
  for await (const value of source) {
    chunks.push(asUint8Array(value));
  }
  return concatBytes(chunks);
}

function describeResponseCode(code) {
  switch (code) {
    case SDS_RESPONSE_CODES.ACCEPT:
      return "accept";
    case SDS_RESPONSE_CODES.REJECT:
      return "reject";
    case SDS_RESPONSE_CODES.RATE_LIMITED:
      return "rate_limited";
    default:
      return `unknown_${code}`;
  }
}

function withTimeout(promise, timeoutMs, label) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => {
      reject(new Error(`${label} timed out after ${timeoutMs}ms`));
    }, timeoutMs);
  });

  return Promise.race([promise, timeout]).finally(() => {
    clearTimeout(timer);
  });
}
