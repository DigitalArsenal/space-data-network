import {
  decodeModuleBundleEntryPayload,
  findModuleBundleEntry,
} from '../../node_modules/space-data-module-sdk/src/bundle/codec.js';
import { decodePluginManifest } from '../../node_modules/space-data-module-sdk/src/manifest/browser.js';
import { extractPublicationRecordCollection } from '../../node_modules/space-data-module-sdk/src/transport/records.js';

function cloneBytes(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  return new Uint8Array(bytes);
}

function normalizeBytes(value: unknown): Uint8Array {
  if (value instanceof Uint8Array) {
    return cloneBytes(value);
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  if (Array.isArray(value)) {
    return Uint8Array.from(value);
  }
  return new Uint8Array(0);
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

async function fallbackSha256(bytes: Uint8Array): Promise<Uint8Array> {
  const digest = await crypto.subtle.digest('SHA-256', cloneBytes(bytes));
  return new Uint8Array(digest);
}

export interface ParsedBrowserBundle {
  bundle: unknown;
  bundleBytes: Uint8Array;
  manifest: Record<string, unknown> | null;
  canonicalModuleHash: Uint8Array;
  canonicalModuleHashHex: string;
}

export async function parseBrowserBundle(
  wasmBytes: Uint8Array,
): Promise<ParsedBrowserBundle> {
  const protectedArtifact = extractPublicationRecordCollection(wasmBytes);
  if (!protectedArtifact?.mbl) {
    throw new Error('Missing required REC trailer containing an MBL record.');
  }

  const bundle = protectedArtifact.mbl;
  const bundleBytes = normalizeBytes(
    protectedArtifact.mblBytes ?? protectedArtifact.recordCollectionBytes,
  );
  const manifestEntry = findModuleBundleEntry(bundle, 'manifest');

  let manifest: Record<string, unknown> | null = null;
  if (manifestEntry) {
    try {
      const payload = decodeModuleBundleEntryPayload(manifestEntry);
      if (payload instanceof Uint8Array) {
        manifest = decodePluginManifest(payload) as Record<string, unknown>;
      }
    } catch {
      manifest = null;
    }
  }

  const bundleHash = normalizeBytes(
    (bundle as { canonicalModuleHash?: unknown }).canonicalModuleHash,
  );
  const canonicalModuleHash =
    bundleHash.length > 0
      ? bundleHash
      : await fallbackSha256(normalizeBytes(protectedArtifact.payloadBytes));

  return {
    bundle,
    bundleBytes,
    manifest,
    canonicalModuleHash,
    canonicalModuleHashHex: bytesToHex(canonicalModuleHash),
  };
}

export async function parseFirstBrowserBundle(
  candidates: Iterable<Uint8Array>,
): Promise<ParsedBrowserBundle> {
  let lastError: unknown = null;

  for (const candidate of candidates) {
    try {
      return await parseBrowserBundle(candidate);
    } catch (error) {
      lastError = error;
    }
  }

  if (lastError instanceof Error) {
    throw lastError;
  }
  throw new Error('Missing required REC trailer containing an MBL record.');
}
