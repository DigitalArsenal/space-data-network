/** One-shot, wallet-authorized native SDS publication. No session or key custody. */
import { initHDWallet, sha256, verify } from '../crypto/index';
import type { AuthProvider, AuthRequest } from './auth';

export interface SdnPublishRequest {
  readonly protocolVersion: 1;
  readonly providerOrigin: string;
  readonly method: 'POST';
  readonly requestUri: string;
  readonly bodySha256: string;
  readonly bodyBytes: number;
  readonly schema: 'CZM' | 'ETM';
  /** Descriptions supplied by the requesting application, not certified contents. */
  readonly entityId: string;
  readonly entityName: string;
  readonly documentCount: number;
}

export interface SdnPublishSignature {
  readonly schemaVersion: 1;
  readonly keyId: string;
  readonly identityScheme: 'sdn-bip32-slip10-purpose-v1';
  readonly algorithm: 'ed25519';
  readonly encoding: 'raw';
  readonly signatureProfile: 'ed25519-sdn-signed-request-v2';
  readonly publicKeyHex: string;
  readonly signatureHex: string;
  readonly requestDigestSha256: string;
  /** The isolated wallet obtains this challenge from the confirmed provider. */
  readonly challengeId: string;
  readonly challengeBase64url: string;
}

export interface SdnPublishAuthOptions {
  providerOrigin: string;
  /** From the connected modern wallet's sdn-authentication descriptor. */
  publicKeyHex: string;
  keyId: string;
  entityId: string;
  entityName: string;
  documentCount: number;
  sign(request: SdnPublishRequest): Promise<SdnPublishSignature>;
}

const MAX_BODY_BYTES = 1_048_576;
const HEX32 = /^[0-9a-f]{64}$/;
const RESULT_FIELDS = [
  'schemaVersion', 'keyId', 'identityScheme', 'algorithm', 'encoding',
  'signatureProfile', 'publicKeyHex', 'signatureHex', 'requestDigestSha256',
  'challengeId', 'challengeBase64url',
];

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function unhex(value: string): Uint8Array {
  return Uint8Array.from(value.match(/../g) ?? [], (byte) => Number.parseInt(byte, 16));
}

function canonicalProviderOrigin(value: string): string {
  let url: URL;
  try { url = new URL(value); } catch { throw new Error('Publishing requires a canonical HTTPS provider origin.'); }
  const labels = url.hostname.split('.');
  if (url.protocol !== 'https:' || value !== url.origin || url.username || url.password ||
      url.pathname !== '/' || url.search || url.hash || url.port === '0' || url.hostname.length > 253 ||
      labels.some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) {
    throw new Error('Publishing requires a canonical HTTPS DNS or IPv4 provider origin.');
  }
  return url.origin;
}

function label(value: string, field: string): string {
  const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : new Uint8Array();
  if (typeof value !== 'string' || /[\u0000-\u001f\u007f]/u.test(value) ||
      bytes.length < 1 || bytes.length > 256 ||
      new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes) !== value) {
    throw new Error(`${field} must contain 1..256 UTF-8 bytes without control characters.`);
  }
  return value;
}

function resultRecord(value: unknown): SdnPublishSignature {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Wallet returned an invalid publication signature.');
  }
  const keys = Reflect.ownKeys(value);
  if (keys.length !== RESULT_FIELDS.length || RESULT_FIELDS.some((key) => {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    return !descriptor?.enumerable || !('value' in descriptor);
  }) || keys.some((key) => typeof key !== 'string' || !RESULT_FIELDS.includes(key))) {
    throw new Error('Wallet returned an invalid publication signature.');
  }
  return Object.freeze(Object.fromEntries(RESULT_FIELDS.map((key) => [
    key, Object.getOwnPropertyDescriptor(value, key)!.value,
  ]))) as unknown as SdnPublishSignature;
}

function challengeBytes(value: string): Uint8Array {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(value)) {
    throw new Error('Wallet returned an invalid provider challenge.');
  }
  const binary = atob(value.replace(/-/g, '+').replace(/_/g, '/') + '=');
  if (binary.length !== 32 || btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') !== value) {
    throw new Error('Wallet returned a noncanonical provider challenge.');
  }
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

/**
 * Configure with a connected wallet's public descriptor and requestSdnPublish.
 * This provider never establishes a session: server admission remains per-write.
 * Destroy it when its publishing dialog is cancelled or replaced.
 */
export class SdnPublishAuth implements AuthProvider {
  readonly requestCredentials = 'omit' as const;
  readonly providerOrigin: string;
  private readonly options: Readonly<SdnPublishAuthOptions>;
  private destroyed = false;

  constructor(options: SdnPublishAuthOptions) {
    this.providerOrigin = canonicalProviderOrigin(options.providerOrigin);
    if (typeof options.publicKeyHex !== 'string' || !HEX32.test(options.publicKeyHex) ||
        typeof options.keyId !== 'string' || !/^sha256:[0-9a-f]{64}$/.test(options.keyId) ||
        typeof options.sign !== 'function') {
      throw new Error('A connected modern wallet authentication descriptor and signer are required.');
    }
    if (!Number.isInteger(options.documentCount) || options.documentCount < 1 || options.documentCount > 100_000) {
      throw new Error('documentCount must be an integer from 1 through 100000.');
    }
    this.options = Object.freeze({
      providerOrigin: this.providerOrigin,
      publicKeyHex: options.publicKeyHex, keyId: options.keyId, sign: options.sign,
      documentCount: options.documentCount,
      entityId: label(options.entityId, 'entityId'),
      entityName: label(options.entityName, 'entityName'),
    });
  }

  isAuthenticated(): boolean { return false; }
  async authenticate(): Promise<void> {
    throw new Error('Signed publication authorizes each request; it does not establish a session.');
  }
  async getAuthHeaders(): Promise<Record<string, string>> {
    throw new Error('Signed publication requires the exact request context.');
  }
  destroy(): void { this.destroyed = true; }
  private assertActive(): void {
    if (this.destroyed) throw new Error('Publication authorization was cancelled or replaced.');
  }

  async authorizeRequest(context: AuthRequest): Promise<Record<string, string>> {
    this.assertActive();
    const url = new URL(context.url);
    const route = /^\/api\/v1\/data\/publish\/(?:batch\/)?(CZM|ETM)(?:\.fbs)?$/.exec(url.pathname);
    if (context.method !== 'POST' || url.origin !== this.providerOrigin || context.url !== url.href ||
        url.username || url.password || url.search || url.hash || !route) {
      throw new Error('This wallet operation only authorizes exact CZM or ETM publication to the selected provider.');
    }
    const body = context.body?.slice();
    if (!body || body.byteLength < 1 || body.byteLength > MAX_BODY_BYTES) {
      throw new Error('Signed publication requires a nonempty payload no larger than 1 MiB.');
    }
    await initHDWallet();
    const bodySha256 = hex(await sha256(body));
    const canonical = ['SDN-SIGNED-REQUEST/v2', this.providerOrigin, 'POST', url.pathname, bodySha256].join('\n');
    const digest = await sha256(new TextEncoder().encode(canonical));
    const request = Object.freeze({
      protocolVersion: 1 as const, providerOrigin: this.providerOrigin, method: 'POST' as const,
      requestUri: url.pathname, bodySha256, bodyBytes: body.length,
      schema: route[1] as 'CZM' | 'ETM', entityId: this.options.entityId,
      entityName: this.options.entityName, documentCount: this.options.documentCount,
    });
    this.assertActive();
    const result = resultRecord(await this.options.sign(request));
    this.assertActive();
    if (result.schemaVersion !== 1 || result.keyId !== this.options.keyId ||
        result.publicKeyHex !== this.options.publicKeyHex ||
        result.identityScheme !== 'sdn-bip32-slip10-purpose-v1' || result.algorithm !== 'ed25519' ||
        result.encoding !== 'raw' || result.signatureProfile !== 'ed25519-sdn-signed-request-v2' ||
        result.requestDigestSha256 !== hex(digest) || typeof result.signatureHex !== 'string' ||
        !/^[0-9a-f]{128}$/.test(result.signatureHex) || typeof result.challengeId !== 'string' ||
        !/^[0-9a-f]{32}$/.test(result.challengeId)) {
      throw new Error('Wallet publication signature does not match the connected identity and exact request.');
    }
    const message = new Uint8Array(64);
    message.set(challengeBytes(result.challengeBase64url));
    message.set(digest, 32);
    if (!await verify(unhex(result.publicKeyHex), message, unhex(result.signatureHex))) {
      throw new Error('Wallet publication signature verification failed.');
    }
    this.assertActive();
    return { Authorization: `SDN-Signed-v2 challenge="${result.challengeId}", signature="${result.signatureHex}"` };
  }
}
