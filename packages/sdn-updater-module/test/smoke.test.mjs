import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { WASI } from "node:wasi";

import {
  decodePluginInvokeResponse,
  encodePluginInvokeRequest,
} from "space-data-module-sdk/invoke";

const __dirname = dirname(fileURLToPath(import.meta.url));
const WASM_PATH = resolve(__dirname, "..", "dist", "isomorphic", "module.wasm");
const encoder = new TextEncoder();
const decoder = new TextDecoder();

const METHOD_OUTPUT_PORTS = {
  checkForUpdates: "result",
  planUpdate: "result",
  fetchArtifact: "bytes",
  verifyArtifact: "result",
  stageArtifact: "result",
  applyStaged: "result",
  selfUpgrade: "result",
  pollUpstream: "result",
  buildManifest: "result",
  signManifest: "result",
  publishManifest: "result",
};

const REQUIRED_EXPORTS = [
  "plugin_alloc",
  "plugin_free",
  "plugin_invoke_stream",
  "plugin_get_manifest_flatbuffer",
  "plugin_get_manifest_flatbuffer_size",
  ...Object.keys(METHOD_OUTPUT_PORTS),
];

async function instantiateModule() {
  const wasi = new WASI({
    version: "preview1",
    args: [],
    env: {},
  });
  const bytes = await readFile(WASM_PATH);
  const { instance } = await WebAssembly.instantiate(bytes, {
    wasi_snapshot_preview1: wasi.wasiImport,
  });

  if (typeof instance.exports._initialize === "function") {
    wasi.initialize(instance);
  } else if (typeof instance.exports.__wasm_call_ctors === "function") {
    instance.exports.__wasm_call_ctors();
  }

  return instance;
}

function memory(instance) {
  assert.ok(instance.exports.memory, "module does not export memory");
  return instance.exports.memory;
}

function writeGuestBytes(instance, bytes) {
  const ptr = instance.exports.plugin_alloc(bytes.length);
  assert.ok(ptr > 0, "plugin_alloc returned null");
  new Uint8Array(memory(instance).buffer).set(bytes, ptr);
  return ptr;
}

function readGuestBytes(instance, ptr, length) {
  return new Uint8Array(memory(instance).buffer, ptr, length).slice();
}

function readU32(instance, ptr) {
  return new DataView(memory(instance).buffer).getUint32(ptr, true);
}

function requestFor(methodId) {
  const inputs = methodId === "checkForUpdates" || methodId === "selfUpgrade"
    ? []
    : [
        {
          portId: "request",
          payload: encoder.encode("{}"),
          typeRef: {
            schemaName: "raw.json",
            fileIdentifier: "JSON",
          },
        },
      ];
  return encodePluginInvokeRequest({ methodId, inputs });
}

function invoke(instance, methodId) {
  const request = requestFor(methodId);
  const requestPtr = writeGuestBytes(instance, request);
  const responseLenPtr = instance.exports.plugin_alloc(4);
  assert.ok(responseLenPtr > 0, "failed to allocate response length");

  const responsePtr = instance.exports.plugin_invoke_stream(
    requestPtr,
    request.length,
    responseLenPtr,
  );
  const responseLen = readU32(instance, responseLenPtr);
  assert.ok(responsePtr > 0, "plugin_invoke_stream returned null");
  assert.ok(responseLen > 0, "plugin_invoke_stream wrote an empty response");

  const responseBytes = readGuestBytes(instance, responsePtr, responseLen);
  instance.exports.plugin_free(requestPtr, request.length);
  instance.exports.plugin_free(responseLenPtr, 4);
  instance.exports.plugin_free(responsePtr, responseLen);
  return decodePluginInvokeResponse(responseBytes);
}

test("scaffold exposes canonical SDK ABI exports", async () => {
  const instance = await instantiateModule();

  for (const name of REQUIRED_EXPORTS) {
    assert.equal(
      typeof instance.exports[name],
      "function",
      `missing required export: ${name}`,
    );
  }
});

test("embedded PLG manifest is present as a FlatBuffer", async () => {
  const instance = await instantiateModule();
  const size = instance.exports.plugin_get_manifest_flatbuffer_size();
  assert.ok(size > 0, `manifest size should be > 0, got ${size}`);

  const ptr = instance.exports.plugin_get_manifest_flatbuffer();
  const manifestBytes = readGuestBytes(instance, ptr, size);
  const fileId = decoder.decode(manifestBytes.subarray(4, 8));
  assert.equal(fileId, "$PLG");
});

for (const [methodId, outputPort] of Object.entries(METHOD_OUTPUT_PORTS)) {
  test(`${methodId} dispatches to a stub response`, async () => {
    const instance = await instantiateModule();
    const response = invoke(instance, methodId);

    assert.equal(response.statusCode, 0);
    assert.equal(response.outputs.length, 1);
    assert.equal(response.outputs[0].portId, outputPort);
    assert.equal(decoder.decode(response.outputs[0].payload), '{"status":"stub"}');
  });
}
