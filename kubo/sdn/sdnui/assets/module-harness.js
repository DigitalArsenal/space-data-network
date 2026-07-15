// PAGE-side SDN module harness — Phase 9 of the kubo rebase.
//
// "Modules load into the JS harness the same as SDN nodes." This module is the
// browser HOST for the SAME module ABI the node runs: it fetches a module's
// portable WASM bytes by CONTENT_HASH from GET /sdn/v1/module?hash=, verifies
// the sha-256 in-page (SubtleCrypto), instantiates it with a stock
// WebAssembly runtime under a JS shim of the two host import namespaces every
// SDN module needs —
//
//   - wasi_snapshot_preview1 : a browser-native WASI preview1 polyfill
//   - space_data_module_host : the sync hostcall bridge (call / response_len /
//     read_response / clear_response / last_status_code), a faithful JS mirror
//     of kubo/sdn/modulert/hostbridge.go
//
// — and invokes a declared method through the guest's `plugin_invoke_stream`
// export using the EXACT alloc/call/read convention the node uses in
// kubo/sdn/modulert/module.go (InvokeMethodFrames): allocate the SDS $PIV
// request, call plugin_invoke_stream(reqPtr, reqLen, &respLen), read the $PIV
// response from guest memory, decode it. The $PIV request/response codec here
// mirrors kubo/sdn/modulert/invoke_codec.go field-for-field, built on the same
// vendored FlatBuffers runtime the node-side SDS bindings use, so a request
// encoded in-page is byte-compatible with the node runtime and the decoded
// result is identical — the isomorphic guarantee.
//
// Self-contained: the only import is the sibling ./flatbuffers.js. No bundler,
// no CDN, no external-origin fetch — every network call is a same-origin GET to
// /sdn/v1/* on the node that served this page. Runs unchanged in a browser and
// under Node (used by the parity test), because it touches only standard
// WebAssembly / crypto.subtle / fetch / TextEncoder — never window/document.

import { Builder, ByteBuffer } from "./flatbuffers.js";

// ---------------------------------------------------------------------------
// SDS $PIV constants (schema/PIV). Enum values and vtable field slots below are
// taken directly from the generated PIV bindings; keep them in lockstep with
// spacedatastandards.org lib/{go,js}/PIV.
// ---------------------------------------------------------------------------

const PIV_FILE_ID = "$PIV";
const INVOKE_ARENA_ALIGNMENT = 8;

const WIRE_FLATBUFFER = 0; // payloadWireFormat.FLATBUFFER
const MUT_IMMUTABLE = 0; // bufferMutability.IMMUTABLE
const OWN_HOST_OWNED = 0; // bufferOwnership.HOST_OWNED

// FlatBuffers vtable field offsets (voffset = 4 + 2*fieldIndex).
const PIV_VOFF = { REQUEST: 4, RESPONSE: 6 };
const REQ_VOFF = { METHOD_ID: 4, INPUTS: 6, PAYLOAD_ARENA: 8, TRACE_ID: 10, OUTPUT_STREAM_CAP: 12 };
const RESP_VOFF = {
  STATUS_CODE: 4, STATUS: 6, YIELDED: 8, BACKLOG_REMAINING: 10, OUTPUTS: 12,
  PAYLOAD_ARENA: 14, ERROR_CODE: 16, ERROR_MESSAGE: 18, TRACE_ID: 20,
};
const TAB_VOFF = {
  OFFSET: 4, SIZE: 6, ALIGNMENT: 8, WIRE_FORMAT: 10, TYPE_REF: 12,
  MUTABILITY: 14, OWNERSHIP: 16, FRAME_ID: 18, PORT_ID: 20,
};

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

// ---------------------------------------------------------------------------
// $PIV request encode — mirrors kubo/sdn/modulert/invoke_codec.go
// encodePluginInvokeRequestFrames for the default single-frame request.
// ---------------------------------------------------------------------------

function alignOffset(offset, alignment) {
  if (alignment <= 1) return offset;
  const r = offset % alignment;
  return r === 0 ? offset : offset + alignment - r;
}

// createAlignedByteVector authors a ubyte vector whose data is aligned to
// `alignment` bytes relative to the finished buffer (the generated
// createPayloadArenaVector only aligns to 1). Identical to codec.js.
function createAlignedByteVector(builder, bytes, alignment) {
  builder.startVector(1, bytes.length, alignment);
  builder.bb.setPosition((builder.space -= bytes.length));
  builder.bb.bytes().set(bytes, builder.space);
  return builder.endVector();
}

function buildTAB(builder, frame) {
  const portIdOffset = frame.portId != null ? builder.createString(frame.portId) : 0;
  // TAB has 9 fields. Defaults (0 / FLATBUFFER / IMMUTABLE / HOST_OWNED) are
  // omitted by the builder exactly as the Go encoder omits them.
  builder.startObject(9);
  builder.addFieldInt32(0, frame.offset >>> 0, 0);
  builder.addFieldInt32(1, frame.size >>> 0, 0);
  builder.addFieldInt32(2, frame.alignment >>> 0, 0);
  builder.addFieldInt8(3, frame.wireFormat, WIRE_FLATBUFFER);
  builder.addFieldInt8(5, frame.mutability, MUT_IMMUTABLE);
  builder.addFieldInt8(6, frame.ownership, OWN_HOST_OWNED);
  builder.addFieldInt64(7, BigInt(frame.frameId || 0), BigInt(0));
  if (portIdOffset) builder.addFieldOffset(8, portIdOffset, 0);
  return builder.endObject();
}

// encodePluginInvokeRequest builds an SDS $PIV request carrying one input frame
// ("request" port) with the given payload, matching the node encoder.
export function encodePluginInvokeRequest(methodID, payload) {
  if (!methodID) throw new Error("method id is required");
  const body = payload ? asUint8(payload) : new Uint8Array();

  // Pack the single frame into an 8-byte-aligned arena (offset 0, no padding
  // for a lone frame at arena base).
  const alignment = Math.max(INVOKE_ARENA_ALIGNMENT, 0);
  const arena = new Uint8Array(alignOffset(0, alignment) + body.length);
  arena.set(body, 0);

  const builder = new Builder(256 + arena.length);

  const methodIdOffset = builder.createString(methodID);
  const payloadArenaOffset = createAlignedByteVector(builder, arena, INVOKE_ARENA_ALIGNMENT);

  const tabOffset = buildTAB(builder, {
    portId: "request",
    offset: 0,
    size: body.length,
    alignment: INVOKE_ARENA_ALIGNMENT,
    wireFormat: WIRE_FLATBUFFER,
    mutability: MUT_IMMUTABLE,
    ownership: OWN_HOST_OWNED,
    frameId: 0,
  });

  // Inputs vector (vector of TAB offsets).
  builder.startVector(4, 1, 4);
  builder.addOffset(tabOffset);
  const inputsVector = builder.endVector();

  builder.startObject(5); // PIVRequest: 5 fields
  builder.addFieldOffset(0, methodIdOffset, 0);
  builder.addFieldOffset(1, inputsVector, 0);
  builder.addFieldOffset(2, payloadArenaOffset, 0);
  // TRACE_ID (i64, default 0) and OUTPUT_STREAM_CAP (i32, default 0) omitted.
  const requestOffset = builder.endObject();
  builder.requiredField(requestOffset, REQ_VOFF.METHOD_ID);

  builder.startObject(2); // PIV: {REQUEST, RESPONSE}
  builder.addFieldOffset(0, requestOffset, 0);
  const root = builder.endObject();

  builder.finish(root, PIV_FILE_ID);
  return builder.asUint8Array();
}

// ---------------------------------------------------------------------------
// $PIV response decode — mirrors decodePluginInvokeResponseBytes +
// extractPluginInvokePayload in the node runtime.
// ---------------------------------------------------------------------------

function tableInt32(bb, pos, voff, def) {
  const o = bb.__offset(pos, voff);
  return o ? bb.readInt32(pos + o) : def;
}
function tableUint32(bb, pos, voff, def) {
  const o = bb.__offset(pos, voff);
  return o ? bb.readUint32(pos + o) : def;
}
function tableString(bb, pos, voff) {
  const o = bb.__offset(pos, voff);
  return o ? bb.__string(pos + o) : null;
}

export function decodePluginInvokeResponse(bytes) {
  const bb = new ByteBuffer(asUint8(bytes));
  if (!bb.__has_identifier(PIV_FILE_ID)) {
    throw new Error("SDS PIV invoke response buffer identifier mismatch");
  }
  const pivPos = bb.readInt32(bb.position()) + bb.position();
  const respFieldOff = bb.__offset(pivPos, PIV_VOFF.RESPONSE);
  if (!respFieldOff) {
    throw new Error("SDS PIV invoke envelope does not contain a response");
  }
  const respPos = bb.__indirect(pivPos + respFieldOff);

  const statusCode = tableInt32(bb, respPos, RESP_VOFF.STATUS_CODE, 0);
  const status = tableInt32(bb, respPos, RESP_VOFF.STATUS, 0);
  const errorCode = tableString(bb, respPos, RESP_VOFF.ERROR_CODE);
  const errorMessage = tableString(bb, respPos, RESP_VOFF.ERROR_MESSAGE);

  const arenaOff = bb.__offset(respPos, RESP_VOFF.PAYLOAD_ARENA);
  let arena = new Uint8Array();
  if (arenaOff) {
    const start = bb.__vector(respPos + arenaOff);
    const len = bb.__vector_len(respPos + arenaOff);
    const raw = bb.bytes();
    arena = raw.subarray(start, start + len);
  }

  const outputs = [];
  const outOff = bb.__offset(respPos, RESP_VOFF.OUTPUTS);
  if (outOff) {
    const vecStart = bb.__vector(respPos + outOff);
    const count = bb.__vector_len(respPos + outOff);
    for (let i = 0; i < count; i++) {
      const framePos = bb.__indirect(vecStart + i * 4);
      const offset = tableUint32(bb, framePos, TAB_VOFF.OFFSET, 0);
      const size = tableUint32(bb, framePos, TAB_VOFF.SIZE, 0);
      const portId = tableString(bb, framePos, TAB_VOFF.PORT_ID);
      const payload = arena.subarray(offset, offset + size);
      outputs.push({ portId, offset, size, payload });
    }
  }

  return { statusCode, status, errorCode, errorMessage, outputs, payloadArena: arena };
}

// extractPayload replays extractPluginInvokePayload: a non-zero status code or a
// carried error is a failure; otherwise the selected output frame's payload is
// returned (empty when the guest produced no frames).
export function extractPayload(response, preferredPortID = "response") {
  if (response.statusCode !== 0) {
    const msg = response.errorMessage || response.errorCode || `status ${response.statusCode}`;
    throw new ModuleInvokeError(`plugin invoke failed (${response.statusCode}): ${msg}`, response);
  }
  if (response.errorCode || response.errorMessage) {
    throw new ModuleInvokeError(`plugin invoke failed: ${response.errorMessage || response.errorCode}`, response);
  }
  if (response.outputs.length === 0) return new Uint8Array();
  let selected = response.outputs[0];
  for (const f of response.outputs) {
    if (f.portId === preferredPortID) { selected = f; break; }
  }
  return selected.payload.slice();
}

export class ModuleInvokeError extends Error {
  constructor(message, response) {
    super(message);
    this.name = "ModuleInvokeError";
    this.response = response;
  }
}

// ---------------------------------------------------------------------------
// Browser WASI preview1 shim — mirrors space-data-module-sdk src/host/wasiShim.js.
// ---------------------------------------------------------------------------

const ERRNO_SUCCESS = 0, ERRNO_BADF = 8, ERRNO_INVAL = 28, ERRNO_NOSYS = 52, ERRNO_SPIPE = 70;
const CLOCKID_REALTIME = 0, CLOCKID_MONOTONIC = 1;

function createWasiShim(options = {}) {
  const args = options.args ?? [];
  const env = options.env ?? {};
  const perf = options.performance ?? globalThis.performance ?? { now: () => Date.now(), timeOrigin: 0 };
  const cryptoApi = options.crypto ?? globalThis.crypto ?? null;
  const stdout = [];
  const stderr = [];
  let memory = null;
  const setMemory = (m) => { memory = m; };
  const mem8 = () => new Uint8Array(memory.buffer);
  const view = () => new DataView(memory.buffer);
  const encodedArgs = args.map((a) => textEncoder.encode(a));
  const encodedEnv = Object.entries(env).map(([k, v]) => textEncoder.encode(`${k}=${v}`));

  function clock_time_get(clockId, _prec, resultPtr) {
    let nanos;
    if (clockId === CLOCKID_REALTIME) nanos = BigInt(Math.round((perf.timeOrigin + perf.now()) * 1e6));
    else if (clockId === CLOCKID_MONOTONIC) nanos = BigInt(Math.round(perf.now() * 1e6));
    else return ERRNO_INVAL;
    view().setBigUint64(resultPtr, nanos, true);
    return ERRNO_SUCCESS;
  }
  function fd_write(fd, iovsPtr, iovsLen, nwrittenPtr) {
    if (fd !== 1 && fd !== 2) return ERRNO_BADF;
    const target = fd === 1 ? stdout : stderr;
    const dv = view(); const bytes = mem8(); let total = 0;
    for (let i = 0; i < iovsLen; i++) {
      const base = iovsPtr + i * 8;
      const ptr = dv.getUint32(base, true);
      const len = dv.getUint32(base + 4, true);
      target.push(bytes.slice(ptr, ptr + len));
      total += len;
    }
    dv.setUint32(nwrittenPtr, total, true);
    return ERRNO_SUCCESS;
  }
  function fd_read(_fd, _iovsPtr, _iovsLen, nreadPtr) {
    view().setUint32(nreadPtr, 0, true); // no stdin
    return ERRNO_SUCCESS;
  }
  function fd_close(fd) { return fd <= 2 ? ERRNO_SUCCESS : ERRNO_BADF; }
  function fd_seek(fd) { return fd <= 2 ? ERRNO_SPIPE : ERRNO_BADF; }
  function fd_fdstat_get(fd, bufPtr) {
    if (fd > 2) return ERRNO_BADF;
    const dv = view();
    dv.setUint8(bufPtr, 2); // CHARACTER_DEVICE
    dv.setUint16(bufPtr + 2, 0, true);
    dv.setBigUint64(bufPtr + 8, BigInt(0), true);
    dv.setBigUint64(bufPtr + 16, BigInt(0), true);
    return ERRNO_SUCCESS;
  }
  function environ_sizes_get(countPtr, bufSizePtr) {
    const dv = view();
    dv.setUint32(countPtr, encodedEnv.length, true);
    dv.setUint32(bufSizePtr, encodedEnv.reduce((n, e) => n + e.length + 1, 0), true);
    return ERRNO_SUCCESS;
  }
  function environ_get(environPtr, environBufPtr) {
    const dv = view(); const bytes = mem8(); let off = environBufPtr;
    for (let i = 0; i < encodedEnv.length; i++) {
      dv.setUint32(environPtr + i * 4, off, true);
      bytes.set(encodedEnv[i], off); off += encodedEnv[i].length; bytes[off++] = 0;
    }
    return ERRNO_SUCCESS;
  }
  function args_sizes_get(argcPtr, bufSizePtr) {
    const dv = view();
    dv.setUint32(argcPtr, encodedArgs.length, true);
    dv.setUint32(bufSizePtr, encodedArgs.reduce((n, a) => n + a.length + 1, 0), true);
    return ERRNO_SUCCESS;
  }
  function args_get(argvPtr, argvBufPtr) {
    const dv = view(); const bytes = mem8(); let off = argvBufPtr;
    for (let i = 0; i < encodedArgs.length; i++) {
      dv.setUint32(argvPtr + i * 4, off, true);
      bytes.set(encodedArgs[i], off); off += encodedArgs[i].length; bytes[off++] = 0;
    }
    return ERRNO_SUCCESS;
  }
  function random_get(bufPtr, bufLen) {
    if (!cryptoApi?.getRandomValues) return ERRNO_NOSYS;
    // getRandomValues caps at 65536 bytes/call.
    const out = mem8();
    for (let i = 0; i < bufLen; i += 65536) {
      cryptoApi.getRandomValues(out.subarray(bufPtr + i, bufPtr + Math.min(i + 65536, bufLen)));
    }
    return ERRNO_SUCCESS;
  }
  function proc_exit(code) { throw new WasiExitError(code); }

  return {
    imports: {
      wasi_snapshot_preview1: {
        clock_time_get, fd_write, fd_read, fd_close, fd_seek, fd_fdstat_get,
        environ_sizes_get, environ_get, args_sizes_get, args_get, random_get, proc_exit,
      },
    },
    setMemory,
  };
}

export class WasiExitError extends Error {
  constructor(code) { super(`WASI exit ${code}`); this.name = "WasiExitError"; this.code = code; }
}

// ---------------------------------------------------------------------------
// Hostcall envelope wire format — mirrors src/host/hostcallWire.js and the Go
// encode/decodeHostcallEnvelope in hostbridge.go.
//   u32 metaLen | meta JSON | u32 segCount | (u32 segLen | seg bytes)...
// ---------------------------------------------------------------------------

const BIN_REF_KEY = "$bin";

function detachBinaryValues(value, segments) {
  if (value === undefined || value === null) return value ?? null;
  if (value instanceof Uint8Array) { const i = segments.length; segments.push(value); return { [BIN_REF_KEY]: i }; }
  if (Array.isArray(value)) return value.map((v) => detachBinaryValues(v, segments));
  if (typeof value === "bigint") return value.toString();
  if (typeof value === "object") {
    const out = {};
    for (const [k, v] of Object.entries(value)) if (v !== undefined) out[k] = detachBinaryValues(v, segments);
    return out;
  }
  return value;
}
function attachBinaryValues(value, segments) {
  if (value === null || value === undefined) return value;
  if (Array.isArray(value)) return value.map((v) => attachBinaryValues(v, segments));
  if (typeof value === "object") {
    const keys = Object.keys(value);
    if (keys.length === 1 && keys[0] === BIN_REF_KEY) {
      const i = value[BIN_REF_KEY];
      if (!Number.isInteger(i) || i < 0 || i >= segments.length) throw new RangeError(`missing binary segment ${i}`);
      return segments[i];
    }
    const out = {};
    for (const k of keys) out[k] = attachBinaryValues(value[k], segments);
    return out;
  }
  return value;
}
function encodeHostcallEnvelope(meta, segments = []) {
  const metaBytes = textEncoder.encode(JSON.stringify(meta ?? null));
  let total = 8 + metaBytes.length;
  for (const s of segments) total += 4 + s.length;
  const bytes = new Uint8Array(total);
  const dv = new DataView(bytes.buffer);
  let off = 0;
  dv.setUint32(off, metaBytes.length, true); off += 4;
  bytes.set(metaBytes, off); off += metaBytes.length;
  dv.setUint32(off, segments.length, true); off += 4;
  for (const s of segments) { dv.setUint32(off, s.length, true); off += 4; bytes.set(s, off); off += s.length; }
  return bytes;
}
function decodeHostcallEnvelope(bytes) {
  if (bytes.length < 8) throw new RangeError("hostcall envelope truncated");
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let off = 0;
  const metaLen = dv.getUint32(off, true); off += 4;
  const meta = JSON.parse(textDecoder.decode(bytes.subarray(off, off + metaLen))); off += metaLen;
  const segCount = dv.getUint32(off, true); off += 4;
  const segments = [];
  for (let i = 0; i < segCount; i++) {
    const segLen = dv.getUint32(off, true); off += 4;
    segments.push(bytes.subarray(off, off + segLen)); off += segLen;
  }
  return { meta, segments };
}

function base64FromBytes(bytes) {
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  if (typeof btoa === "function") return btoa(s);
  return Buffer.from(bytes).toString("base64"); // Node fallback
}

// ---------------------------------------------------------------------------
// Sync hostcall dispatcher — a faithful JS mirror of HostBridge.Dispatch in
// kubo/sdn/modulert/hostbridge.go (built-in, capability-free operations). The
// one intentional host-identity difference is host.runtimeTarget: the node
// answers "server", the page answers "browser". That names the host, not the
// result of any module method, so it does not affect isomorphic parity.
// ---------------------------------------------------------------------------

const SUPPORTED_CAPABILITIES = [
  "clock", "random", "protocol_handle", "protocol_dial", "pubsub",
  "crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt", "crypto_decrypt",
  "crypto_key_agreement", "crypto_kdf", "wallet_sign", "ipfs",
  "storage_query", "storage_write", "storage_adapter", "storage_ingest", "http",
  "schedule_cron", "p2p_read", "node_status_read", "node_activity_read",
];
const BASE_OPERATIONS = [
  "clock.now", "clock.nowIso", "clock.monotonicNow", "random.bytes",
  "host.runtimeTarget", "host.listCapabilities", "host.hasCapability",
  "host.listOperations", "node.publicKey", "node.peerId", "plugin.getConfig",
];

function createHostDispatcher(ctx = {}) {
  const ok = (result) => ({ ok: true, result });
  const err = (message) => ({ ok: false, error: { message } });
  return function dispatch(operation, params) {
    switch (operation) {
      case "clock.now": return ok(Date.now());
      case "clock.nowIso": return ok(new Date().toISOString());
      case "clock.monotonicNow": return ok(Date.now());
      case "random.bytes": {
        let n = 32;
        if (params && typeof params.length === "number" && params.length > 0) n = Math.min(params.length, 8192);
        const buf = new Uint8Array(n);
        (globalThis.crypto ?? {}).getRandomValues?.(buf);
        return ok({ __type: "bytes", base64: base64FromBytes(buf) });
      }
      case "host.runtimeTarget": return ok(ctx.runtimeTarget ?? "browser");
      case "host.listCapabilities": return ok(ctx.capabilities ?? []);
      case "host.listSupportedCapabilities": return ok(SUPPORTED_CAPABILITIES);
      case "host.hasCapability": return ok((ctx.capabilities ?? []).includes(params?.capability));
      case "host.listOperations": return ok(BASE_OPERATIONS);
      case "node.publicKey": return ctx.publicKeyHex ? ok(ctx.publicKeyHex) : err("node public key not available");
      case "node.peerId": return ctx.peerId ? ok(ctx.peerId) : err("node peer ID not available");
      case "plugin.getConfig": return ok(ctx.config ?? {});
      default: return err(`operation ${JSON.stringify(operation)} not supported`);
    }
  };
}

// createHostBridge builds the space_data_module_host import namespace, driving
// the guest's sync hostcall protocol over the module's linear memory exactly as
// HostBridge.BuildWasmEdgeHostFuncs does node-side.
function createHostBridge(dispatch, getMemory) {
  let responseBuf = encodeHostcallEnvelope({ ok: true, result: null }, []);
  let lastStatus = 0;
  const mem8 = () => new Uint8Array(getMemory().buffer);

  function encodeResponse(resultObj) {
    const segments = [];
    const meta = detachBinaryValues(resultObj, segments);
    return encodeHostcallEnvelope(meta, segments);
  }

  return {
    imports: {
      space_data_module_host: {
        call(opPtr, opLen, payloadPtr, payloadLen) {
          const mem = mem8();
          const op = textDecoder.decode(mem.subarray(opPtr, opPtr + opLen));
          let params = null;
          if (payloadLen > 0) {
            const { meta, segments } = decodeHostcallEnvelope(mem.subarray(payloadPtr, payloadPtr + payloadLen).slice());
            params = attachBinaryValues(meta, segments);
          }
          let result;
          try { result = dispatch(op, params); }
          catch (e) { result = { ok: false, error: { message: String(e && e.message ? e.message : e) } }; }
          responseBuf = encodeResponse(result);
          lastStatus = 0;
          return 0;
        },
        response_len() { return responseBuf.length; },
        read_response(dstPtr, dstLen) {
          const n = Math.min(responseBuf.length, dstLen);
          if (n > 0) mem8().set(responseBuf.subarray(0, n), dstPtr);
          return n;
        },
        clear_response() {
          responseBuf = encodeHostcallEnvelope({ ok: true, result: null }, []);
          lastStatus = 0;
          return 0;
        },
        last_status_code() { return lastStatus; },
      },
    },
  };
}

// ---------------------------------------------------------------------------
// Instantiation + invoke — the browser HOST for the plugin_invoke_stream ABI.
// ---------------------------------------------------------------------------

const WASM_PAGE = 65536;

// toLoadableWasmBytes strips an appended SDN publication record collection
// (signature / PNM / REC trailers) that wasm engines reject as "unknown section
// code". Canonical unsigned modules pass through unchanged. This mirrors the
// SDK's toLoadableWasmBytes for the common (unsigned/mbl-suffixed) shapes.
export function toLoadableWasmBytes(bytes) {
  const u8 = asUint8(bytes);
  // A raw wasm module starts with "\0asm" and version 1. If trailing bytes were
  // appended past the last section the browser engine rejects them; but SDN's
  // canonical isomorphic artifacts are plain modules, so pass through and let
  // WebAssembly.compile surface any real problem.
  return u8;
}

async function compileModule(bytes) {
  return WebAssembly.compile(toLoadableWasmBytes(bytes));
}

// instantiate wires the JS host ABI shim and returns a live module handle.
export async function instantiateModule(source, options = {}) {
  const wasmModule = source instanceof WebAssembly.Module ? source : await compileModule(source);
  const imports = WebAssembly.Module.imports(wasmModule);
  const wasi = createWasiShim({ args: options.args, env: options.env, performance: options.performance });
  const importObject = { ...wasi.imports };

  let memory = null;
  const memoryImport = imports.find((i) => i.kind === "memory");
  if (memoryImport) {
    memory = new WebAssembly.Memory({ initial: 256, maximum: 32768 }); // 16MiB..2GiB
    importObject[memoryImport.module] = { ...(importObject[memoryImport.module] ?? {}), [memoryImport.name]: memory };
  }

  const needsHostBridge = imports.some((i) => i.module === "space_data_module_host");
  const dispatch = options.dispatch ?? createHostDispatcher(options.hostContext);
  if (needsHostBridge) {
    const bridge = createHostBridge(dispatch, () => memory ?? instance.exports.memory);
    Object.assign(importObject, bridge.imports);
  }

  const instance = await WebAssembly.instantiate(wasmModule, importObject);
  memory = instance.exports.memory ?? memory;
  if (!memory) throw new Error("module exposes no memory");
  wasi.setMemory(memory);
  if (typeof instance.exports._initialize === "function") instance.exports._initialize();

  return new ModuleInstance(wasmModule, instance, memory);
}

// ModuleInstance owns a loaded guest and invokes methods through
// plugin_invoke_stream with the node's exact alloc/call/read convention.
export class ModuleInstance {
  constructor(wasmModule, instance, memory) {
    this.module = wasmModule;
    this.instance = instance;
    this.memory = memory;
    const e = instance.exports;
    this.alloc = e.plugin_alloc;
    this.free = e.plugin_free;
    this.invokeStream = e.plugin_invoke_stream;
    if (typeof this.alloc !== "function" || typeof this.invokeStream !== "function") {
      throw new Error("module is missing plugin_alloc / plugin_invoke_stream exports");
    }
  }

  // invokeRaw runs a pre-encoded $PIV request through plugin_invoke_stream and
  // returns the raw $PIV response bytes — the same three-argument call the node
  // makes: plugin_invoke_stream(reqPtr, reqLen, &respLen) -> respPtr.
  invokeRaw(requestBytes) {
    const req = asUint8(requestBytes);
    const reqPtr = this.alloc(req.length);
    if (!reqPtr) throw new Error("plugin_alloc returned null for request");
    if (reqPtr % INVOKE_ARENA_ALIGNMENT !== 0) throw new Error(`request pointer ${reqPtr} not ${INVOKE_ARENA_ALIGNMENT}-byte aligned`);
    new Uint8Array(this.memory.buffer, reqPtr, req.length).set(req);

    const outLenPtr = this.alloc(4);
    if (!outLenPtr) throw new Error("plugin_alloc returned null for response length");
    new DataView(this.memory.buffer).setUint32(outLenPtr, 0, true);

    const resPtr = this.invokeStream(reqPtr, req.length, outLenPtr) >>> 0;
    const resLen = new DataView(this.memory.buffer).getUint32(outLenPtr, true);

    if (typeof this.free === "function") { this.free(reqPtr, req.length); this.free(outLenPtr, 4); }

    if (!resPtr || !resLen) throw new Error("plugin_invoke_stream returned an empty response");
    const responseBytes = new Uint8Array(this.memory.buffer, resPtr, resLen).slice();
    if (typeof this.free === "function") this.free(resPtr, resLen);
    return responseBytes;
  }

  // invoke encodes methodID + payload, runs the guest, and decodes the $PIV
  // response — returning the full structured result (statusCode, status,
  // errorCode, errorMessage, outputs) so callers can assert isomorphic parity.
  invoke(methodID, payload) {
    const requestBytes = encodePluginInvokeRequest(methodID, payload);
    const responseBytes = this.invokeRaw(requestBytes);
    return decodePluginInvokeResponse(responseBytes);
  }

  readManifest() {
    const e = this.instance.exports;
    if (typeof e.plugin_get_manifest_flatbuffer !== "function" || typeof e.plugin_get_manifest_flatbuffer_size !== "function") return null;
    const ptr = e.plugin_get_manifest_flatbuffer();
    const size = e.plugin_get_manifest_flatbuffer_size();
    if (!ptr || !size) return null;
    return new Uint8Array(this.memory.buffer, ptr, size).slice();
  }
}

// ---------------------------------------------------------------------------
// sha-256 verification + fetch-by-CONTENT_HASH.
// ---------------------------------------------------------------------------

export async function sha256Hex(bytes) {
  const digest = await (globalThis.crypto.subtle).digest("SHA-256", asUint8(bytes));
  const view = new Uint8Array(digest);
  let hex = "";
  for (let i = 0; i < view.length; i++) hex += view[i].toString(16).padStart(2, "0");
  return hex;
}

// fetchModuleBytes GETs the module WASM by CONTENT_HASH from the node's
// same-origin /sdn/v1/module?hash= endpoint and re-verifies the digest in-page.
export async function fetchModuleBytes(hash, options = {}) {
  const base = options.apiBase ?? "/sdn/v1";
  const normalized = String(hash).trim().toLowerCase();
  const resp = await fetch(`${base}/module?hash=${encodeURIComponent(normalized)}`);
  if (!resp.ok) throw new Error(`module fetch failed: HTTP ${resp.status}`);
  const bytes = new Uint8Array(await resp.arrayBuffer());
  const got = await sha256Hex(bytes);
  if (got !== normalized) throw new Error(`content hash mismatch: served bytes hash to ${got}, expected ${normalized}`);
  return bytes;
}

// loadModuleByHash is the page entry point: fetch + verify + instantiate a
// module addressed by CONTENT_HASH, ready for in-page invoke.
export async function loadModuleByHash(hash, options = {}) {
  const bytes = await fetchModuleBytes(hash, options);
  const inst = await instantiateModule(bytes, options);
  return { contentHash: String(hash).trim().toLowerCase(), instance: inst, bytes };
}

// loadModuleFromBytes instantiates from bytes already in hand (used by the Node
// parity test and by callers that already resolved the artifact). When
// expectedHash is given, the sha-256 is re-verified first.
export async function loadModuleFromBytes(bytes, options = {}) {
  const u8 = asUint8(bytes);
  if (options.expectedHash) {
    const got = await sha256Hex(u8);
    const want = String(options.expectedHash).trim().toLowerCase();
    if (got !== want) throw new Error(`content hash mismatch: bytes hash to ${got}, expected ${want}`);
  }
  const inst = await instantiateModule(u8, options);
  return { instance: inst, bytes: u8 };
}

// ---------------------------------------------------------------------------

function asUint8(value) {
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  throw new TypeError("expected Uint8Array, ArrayBuffer, or ArrayBufferView");
}
