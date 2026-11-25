/**
 * Subscription Compute Offload Client ($PIV -> $TMX / $COA / $DCO / $QRP).
 *
 * Encodes schema-tagged $PIV requests over FlatBuffers streams and consumes
 * progressive size-prefixed FlatBuffer responses from POST /api/v1/compute/invoke.
 * Zero JSON parsing or serialization on the data path.
 */

import * as flatbuffers from 'flatbuffers';
import {
  FlatBufferTypeRefT,
  PIV,
  PIVRequestT,
  PIVT,
  TABT,
  bufferMutability,
  bufferOwnership,
  payloadWireFormat,
} from 'spacedatastandards.org/lib/js/PIV/main.js';
import { QRP, qrpKind, qrpStatus } from 'spacedatastandards.org/lib/js/QRP/main.js';
import { TMX, TMXT } from 'spacedatastandards.org/lib/js/TMX/main.js';
import { COA, COAT } from 'spacedatastandards.org/lib/js/COA/main.js';
import { DCO, DCOT } from 'spacedatastandards.org/lib/js/DCO/main.js';
import {
  FLATBUFFER_STREAM_CONTENT_TYPE,
  iterateSizePrefixedFrameStream,
  iterateSizePrefixedFrames,
} from './transport/http';

export const COMPUTE_INVOKE_PATH = '/api/v1/compute/invoke';
export const COMPUTE_FALLBACK_HEADER = 'X-SDN-Compute-Fallback';
export const COMPUTE_MODULE_ID_HEADER = 'X-SDN-Module-ID';
export const COMPUTE_WASM_SHA256_HEADER = 'X-SDN-Wasm-Sha256';
export const COMPUTE_FALLBACK_BROWSER_LOCAL = 'browser-local';

export interface ComputeInputFrame {
  portId?: string;
  schemaName?: string;
  fileIdentifier?: string;
  rootType?: string;
  payload: Uint8Array;
  frameId?: bigint;
}

export interface ComputePivotRequestOptions {
  methodId: string;
  inputs: readonly ComputeInputFrame[];
  traceId?: bigint;
  outputStreamCap?: number;
}

export interface QrpErrorSummary {
  kind: qrpKind;
  status: qrpStatus;
  errorCode: string;
  message: string;
  retryAfterMs: number;
}

export interface ComputeOutputFrame {
  fileIdentifier: string;
  bytes: Uint8Array;
}

export interface ComputeInvokeOptions {
  baseUrl: string;
  methodId: string;
  moduleId?: string;
  inputs: readonly ComputeInputFrame[];
  traceId?: bigint;
  outputStreamCap?: number;
  headers?: HeadersInit;
  fetchFn?: typeof fetch;
  signal?: AbortSignal;
  onFrame?: (frame: ComputeOutputFrame) => void | Promise<void>;
}

export interface ComputeInvokeResult {
  ok: boolean;
  status: number;
  fallback: typeof COMPUTE_FALLBACK_BROWSER_LOCAL | null;
  errorCode: string | null;
  message: string | null;
  frames: ComputeOutputFrame[];
  rawFrames: Uint8Array[];
  moduleId: string | null;
  wasmSha256: string | null;
}

export class ComputeOffloadError extends Error {
  readonly status: number;
  readonly errorCode: string | null;
  readonly fallback: string | null;

  constructor(
    message: string,
    status: number,
    errorCode: string | null = null,
    fallback: string | null = null,
  ) {
    super(message);
    this.name = 'ComputeOffloadError';
    this.status = status;
    this.errorCode = errorCode;
    this.fallback = fallback;
  }
}

export function extractFlatBufferFileIdentifier(bytes: Uint8Array): string {
  if (bytes.length < 8) {
    return '';
  }
  let end = 8;
  while (end > 4 && bytes[end - 1] === 0) {
    end--;
  }
  return String.fromCharCode(...bytes.subarray(4, end));
}

export function encodeComputePivotRequest(options: ComputePivotRequestOptions): Uint8Array {
  const alignment = 8;
  let totalArenaBytes = 0;
  const offsets: number[] = [];

  for (const input of options.inputs) {
    const rem = totalArenaBytes % alignment;
    if (rem !== 0) {
      totalArenaBytes += alignment - rem;
    }
    offsets.push(totalArenaBytes);
    totalArenaBytes += input.payload.byteLength;
  }

  const arena = new Uint8Array(totalArenaBytes);
  const tabs: TABT[] = [];

  options.inputs.forEach((input, idx) => {
    const offset = offsets[idx];
    arena.set(input.payload, offset);
    const inferredFid = input.fileIdentifier ?? extractFlatBufferFileIdentifier(input.payload);
    const typeRef = new FlatBufferTypeRefT(
      input.schemaName ?? null,
      inferredFid || null,
      null,
      input.rootType ?? null,
      [],
      false,
      payloadWireFormat.FLATBUFFER,
      0,
      0,
      alignment,
    );
    tabs.push(
      new TABT(
        offset >>> 0,
        input.payload.byteLength >>> 0,
        alignment >>> 0,
        payloadWireFormat.FLATBUFFER,
        typeRef,
        bufferMutability.IMMUTABLE,
        bufferOwnership.HOST_OWNED,
        input.frameId ?? BigInt(idx + 1),
        input.portId ?? null,
      ),
    );
  });

  const pivReq = new PIVRequestT(
    options.methodId,
    tabs,
    Array.from(arena),
    options.traceId ?? 0n,
    (options.outputStreamCap ?? 0) >>> 0,
  );
  const piv = new PIVT(pivReq, null);
  const builder = new flatbuffers.Builder(1024 + totalArenaBytes);
  const rootOffset = piv.pack(builder);
  PIV.finishPIVBuffer(builder, rootOffset);
  return builder.asUint8Array().slice();
}

export function decodeQrpErrorFrame(bytes: Uint8Array): QrpErrorSummary | null {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!QRP.bufferHasIdentifier(bb)) {
    return null;
  }
  const qrp = QRP.getRootAsQRP(bb);
  return {
    kind: qrp.KIND(),
    status: qrp.STATUS(),
    errorCode: qrp.ERROR_CODE() ?? '',
    message: qrp.MESSAGE() ?? '',
    retryAfterMs: qrp.RETRY_AFTER_MS(),
  };
}

export function decodeComputeOutputFrame(bytes: Uint8Array): ComputeOutputFrame {
  return {
    fileIdentifier: extractFlatBufferFileIdentifier(bytes),
    bytes,
  };
}

export function decodeTmxFrame(bytes: Uint8Array): TMXT {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!TMX.bufferHasIdentifier(bb)) {
    throw new Error('Expected $TMX FlatBuffer frame');
  }
  return TMX.getRootAsTMX(bb).unpack();
}

export function decodeCoaFrame(bytes: Uint8Array): COAT {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!COA.bufferHasIdentifier(bb)) {
    throw new Error('Expected $COA FlatBuffer frame');
  }
  return COA.getRootAsCOA(bb).unpack();
}

export function decodeDcoFrame(bytes: Uint8Array): DCOT {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!DCO.bufferHasIdentifier(bb)) {
    throw new Error('Expected $DCO FlatBuffer frame');
  }
  return DCO.getRootAsDCO(bb).unpack();
}

function parseQrpFromFrameBytes(raw: Uint8Array): QrpErrorSummary | null {
  if (raw.byteLength === 0) {
    return null;
  }
  // Check raw frame or size-prefixed frame(s).
  const direct = decodeQrpErrorFrame(raw);
  if (direct) {
    return direct;
  }
  try {
    for (const frame of iterateSizePrefixedFrames(raw)) {
      const qrp = decodeQrpErrorFrame(frame);
      if (qrp) {
        return qrp;
      }
    }
  } catch {
    return null;
  }
  return null;
}

export async function invokeRemoteCompute(options: ComputeInvokeOptions): Promise<ComputeInvokeResult> {
  const fetchFn = options.fetchFn ?? globalThis.fetch;
  if (typeof fetchFn !== 'function') {
    throw new Error('fetch is unavailable for remote compute invocation');
  }

  let baseEnd = options.baseUrl.length;
  while (baseEnd > 0 && options.baseUrl.charCodeAt(baseEnd - 1) === 47) {
    baseEnd--;
  }
  const normalizedBase = options.baseUrl.slice(0, baseEnd);
  const url = `${normalizedBase}${COMPUTE_INVOKE_PATH}`;
  const pivBytes = encodeComputePivotRequest({
    methodId: options.methodId,
    inputs: options.inputs,
    traceId: options.traceId,
    outputStreamCap: options.outputStreamCap,
  });

  const headers = new Headers(options.headers);
  if (!headers.has('Content-Type')) {
    headers.set('Content-Type', FLATBUFFER_STREAM_CONTENT_TYPE);
  }
  if (!headers.has('Accept')) {
    headers.set('Accept', FLATBUFFER_STREAM_CONTENT_TYPE);
  }
  if (options.moduleId) {
    headers.set('X-SDN-Module-Id', options.moduleId);
  }

  const body = new Uint8Array(pivBytes.byteLength);
  body.set(pivBytes);

  const response = await fetchFn(url, {
    method: 'POST',
    headers,
    body,
    signal: options.signal,
  });

  const fallbackHeader = response.headers.get(COMPUTE_FALLBACK_HEADER);
  const moduleId = response.headers.get(COMPUTE_MODULE_ID_HEADER);
  const wasmSha256 = response.headers.get(COMPUTE_WASM_SHA256_HEADER);

  if (
    response.status === 402 ||
    fallbackHeader === COMPUTE_FALLBACK_BROWSER_LOCAL
  ) {
    const rawErrorBytes = new Uint8Array(await response.arrayBuffer());
    const qrp = parseQrpFromFrameBytes(rawErrorBytes);
    return {
      ok: false,
      status: response.status,
      fallback: COMPUTE_FALLBACK_BROWSER_LOCAL,
      errorCode: qrp?.errorCode || 'subscription_required',
      message: qrp?.message || 'Active compute offload subscription required; run locally in browser WASM.',
      frames: [],
      rawFrames: [],
      moduleId,
      wasmSha256,
    };
  }

  if (!response.ok) {
    const rawErrorBytes = new Uint8Array(await response.arrayBuffer());
    const qrp = parseQrpFromFrameBytes(rawErrorBytes);
    throw new ComputeOffloadError(
      qrp?.message || `Remote compute failed with HTTP ${response.status}`,
      response.status,
      qrp?.errorCode || null,
      fallbackHeader,
    );
  }

  const frames: ComputeOutputFrame[] = [];
  const rawFrames: Uint8Array[] = [];

  if (response.body) {
    for await (const frameBytes of iterateSizePrefixedFrameStream(response.body)) {
      const qrp = decodeQrpErrorFrame(frameBytes);
      if (qrp && qrp.kind === qrpKind.Error) {
        throw new ComputeOffloadError(
          qrp.message || `Remote compute stream error (${qrp.errorCode || 'error'})`,
          response.status,
          qrp.errorCode || null,
          fallbackHeader,
        );
      }
      const frame = decodeComputeOutputFrame(frameBytes);
      frames.push(frame);
      rawFrames.push(frameBytes);
      if (options.onFrame) {
        await options.onFrame(frame);
      }
    }
  } else {
    const rawBytes = new Uint8Array(await response.arrayBuffer());
    for (const frameBytes of iterateSizePrefixedFrames(rawBytes)) {
      const qrp = decodeQrpErrorFrame(frameBytes);
      if (qrp && qrp.kind === qrpKind.Error) {
        throw new ComputeOffloadError(
          qrp.message || `Remote compute stream error (${qrp.errorCode || 'error'})`,
          response.status,
          qrp.errorCode || null,
          fallbackHeader,
        );
      }
      const frame = decodeComputeOutputFrame(frameBytes);
      frames.push(frame);
      rawFrames.push(frameBytes);
      if (options.onFrame) {
        await options.onFrame(frame);
      }
    }
  }

  return {
    ok: true,
    status: response.status,
    fallback: null,
    errorCode: null,
    message: null,
    frames,
    rawFrames,
    moduleId,
    wasmSha256,
  };
}
