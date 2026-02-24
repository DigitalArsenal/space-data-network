import assert from "node:assert/strict";
import {
  SDS_MESSAGE_TYPES,
  createPluginSDK,
  computeSDNDiscoveryCID,
} from "../../src/index.js";

function jsonResponse(payload, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function textResponse(payload, status = 200) {
  return new Response(String(payload), {
    status,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}

function bytesResponse(payload, status = 200) {
  return new Response(payload, {
    status,
    headers: { "content-type": "application/octet-stream" },
  });
}

function captureHeaders(init) {
  return new Headers(init?.headers || {});
}

async function runWebAndIPFSConformance() {
  const calls = [];
  const mockFetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });

    if (String(url).includes("/api/node/info")) {
      return jsonResponse({ peer_id: "12D3KooWNodeInfoPeer" });
    }
    if (String(url).includes("/api/v0/name/resolve")) {
      return jsonResponse({ Path: "/ipfs/bafyresolved" });
    }
    if (String(url).includes("/api/v0/dht/findprovs")) {
      return textResponse(
        '{"Responses":[{"ID":"12D3KooWProvider","Addrs":["/ip4/127.0.0.1/tcp/4001"]}]}\n',
      );
    }
    throw new Error(`unexpected URL: ${url}`);
  };

  const sdk = createPluginSDK({
    baseUrl: "http://127.0.0.1:5010",
    fetchImpl: mockFetch,
  });

  const info = await sdk.sdn.nodeInfo();
  assert.equal(info.peer_id, "12D3KooWNodeInfoPeer");

  const resolved = await sdk.ipfs.resolveIPNS("k51sdnexample");
  assert.equal(resolved.path, "/ipfs/bafyresolved");

  const providers = await sdk.ipfs.findProviders("bafkexamplecid");
  assert.equal(providers.providers.length, 1);
  assert.equal(providers.providers[0].id, "12D3KooWProvider");

  const expectedDiscoveryCID =
    "bafkreicx65tchhjt5qxodpf57vts4mnasxntqp5swrj6dbrukzi7yt5kye";
  const computedDiscoveryCID = await computeSDNDiscoveryCID(
    "spacedatanetwork/1.0.0",
  );
  assert.equal(computedDiscoveryCID, expectedDiscoveryCID);

  const sdnPeers = await sdk.ipfs.findSDNPeers();
  assert.equal(sdnPeers.discoveryCID, expectedDiscoveryCID);
  assert.equal(sdnPeers.providers.length, 1);

  assert.ok(
    calls.some((entry) =>
      entry.url.includes(
        `/api/v0/dht/findprovs?arg=${expectedDiscoveryCID}&num-providers=20`,
      ),
    ),
    "expected SDN discovery CID lookup call",
  );
}

async function runFlatSQLConformance() {
  const calls = [];
  const mockFetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });

    if (
      String(url).includes("/api/v1/data/omm") ||
      String(url).includes("/api/v1/data/secure/omm")
    ) {
      if (String(url).includes("format=flatbuffers")) {
        return bytesResponse(new Uint8Array([0x01, 0x02, 0x03]));
      }
      return jsonResponse({ schema: "OMM.fbs", count: 1 });
    }
    if (String(url).includes("/api/v1/data/cat")) {
      return bytesResponse(new Uint8Array([0x09, 0x08]));
    }
    if (String(url).includes("/api/v1/data/health")) {
      return jsonResponse({ status: "ok" });
    }
    throw new Error(`unexpected URL: ${url}`);
  };

  const sdk = createPluginSDK({
    baseUrl: "http://127.0.0.1:5010",
    fetchImpl: mockFetch,
  });

  const health = await sdk.flatsql.health();
  assert.equal(health.status, "ok");

  const ommJSON = await sdk.flatsql.queryOMM({
    day: "2026-02-11",
    noradCatId: 25544,
    limit: 5,
    secure: true,
    authorization: "Bearer token-123",
    peerId: "12D3KooWPeer",
  });
  assert.equal(ommJSON.schema, "OMM.fbs");

  const ommBinary = await sdk.flatsql.queryOMM({
    day: "2026-02-11",
    noradCatId: 25544,
    format: "flatbuffers",
  });
  assert.deepEqual([...ommBinary], [0x01, 0x02, 0x03]);

  const catBinary = await sdk.flatsql.queryCAT({
    noradCatID: 25544,
    format: "flatbuffers",
  });
  assert.deepEqual([...catBinary], [0x09, 0x08]);

  const secureCall = calls.find((entry) =>
    entry.url.includes("/api/v1/data/secure/omm"),
  );
  assert.ok(secureCall, "secure OMM call expected");
  const secureHeaders = captureHeaders(secureCall.init);
  assert.equal(secureHeaders.get("authorization"), "Bearer token-123");
  assert.equal(secureHeaders.get("x-sdn-peer-id"), "12D3KooWPeer");
}

async function readAll(source) {
  const chunks = [];
  for await (const value of source) {
    if (value instanceof Uint8Array) {
      chunks.push(value);
    } else if (value instanceof ArrayBuffer) {
      chunks.push(new Uint8Array(value));
    } else {
      chunks.push(new Uint8Array(value.buffer, value.byteOffset, value.byteLength));
    }
  }
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

function u32be(value) {
  return new Uint8Array([
    (value >>> 24) & 0xff,
    (value >>> 16) & 0xff,
    (value >>> 8) & 0xff,
    value & 0xff,
  ]);
}

function withCapturedDialer(responseFactory, captures) {
  return async () => {
    let requestPayload = new Uint8Array();
    let resolveReady;
    const ready = new Promise((resolve) => {
      resolveReady = resolve;
    });

    return {
      sink: async (source) => {
        requestPayload = await readAll(source);
        captures.push(requestPayload);
        resolveReady();
      },
      source: (async function* () {
        await ready;
        yield responseFactory(requestPayload);
      })(),
      close: async () => {},
    };
  };
}

async function runSDSExchangeConformance() {
  const sdk = createPluginSDK();
  const captures = [];

  const pushDialer = withCapturedDialer(
    () =>
      new Uint8Array([
        0x01,
        ...new TextEncoder().encode(
          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        ),
      ]),
    captures,
  );
  const pushResult = await sdk.sdsExchange.pushData({
    dialProtocol: pushDialer,
    schemaName: "OMM.fbs",
    data: new Uint8Array([0xaa, 0xbb]),
    timeoutMs: 1_000,
  });
  assert.equal(
    pushResult.cid,
    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  );

  const pushPayload = captures[0];
  assert.equal(pushPayload[0], SDS_MESSAGE_TYPES.PUSH_DATA);
  assert.equal(pushPayload[1], 0x00);
  assert.equal(pushPayload[2], 0x07); // len("OMM.fbs")
  assert.equal(pushPayload.length, 16); // msgType + schemaLen + schema + dataLen + data

  await assert.rejects(
    () =>
      sdk.sdsExchange.pushData({
        dialProtocol: pushDialer,
        schemaName: "OMM.fbs",
        data: new Uint8Array([0xaa, 0xbb]),
        signature: new Uint8Array(64),
        timeoutMs: 1_000,
      }),
    /not supported in SDS v1/,
  );

  const requestDialer = withCapturedDialer(
    () => new Uint8Array([0x01, ...u32be(3), 0x01, 0x02, 0x03]),
    captures,
  );
  const requested = await sdk.sdsExchange.requestData({
    dialProtocol: requestDialer,
    schemaName: "OMM.fbs",
    cid: "abc123",
    timeoutMs: 1_000,
  });
  assert.deepEqual([...requested], [0x01, 0x02, 0x03]);

  const queryDialer = withCapturedDialer(
    () =>
      new Uint8Array([
        0x01,
        ...u32be(2),
        ...u32be(1),
        0x99,
        ...u32be(2),
        0x55,
        0x66,
      ]),
    captures,
  );
  const queryResults = await sdk.sdsExchange.query({
    dialProtocol: queryDialer,
    schemaName: "OMM.fbs",
    query: "ignored",
    timeoutMs: 1_000,
  });
  assert.equal(queryResults.length, 2);
  assert.deepEqual([...queryResults[0]], [0x99]);
  assert.deepEqual([...queryResults[1]], [0x55, 0x66]);
}

export async function runSDKClientConformance() {
  await runWebAndIPFSConformance();
  await runFlatSQLConformance();
  await runSDSExchangeConformance();
  return {
    ok: true,
    suite: "sdk-client-conformance",
  };
}
