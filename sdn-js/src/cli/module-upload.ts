import {
  decryptLocalContentKeyEnvelope,
  packageModule,
  readModulePackage,
  type PackageModuleOptions,
  type PackagedModule,
} from './module-package';
import { generateKey, x25519ECDH, x25519PublicKey } from '../crypto/hd-wallet';
import { SDNNode } from '../node';
import type { LoadedWallet } from './wallet';

export { packageModule };
export type { PackageModuleOptions, PackagedModule };

const MODULE_UPLOAD_PROTOCOL_ID = '/space-data-network/plugin-module-upload/1.0.0';
const PROVIDER_CONTENT_KEY_CONTEXT = 'space-data-network/plugin-module/content-key/v1';

export interface UploadModuleOptions {
  nodeUrl: string;
  packagePath: string;
  sessionCookie: string;
  wallet: LoadedWallet;
  fetchImpl?: typeof fetch;
  nodeFactory?: (options: {
    wallet: LoadedWallet;
    nodeOrigin: string;
    relayAddresses: string[];
  }) => Promise<ModuleUploadTransport>;
}

export interface ListModulesOptions {
  nodeUrl: string;
  sessionCookie: string;
  fetchImpl?: typeof fetch;
}

export interface ModuleUploadTransport {
  dialProtocol(
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array,
    candidateAddrs?: string[],
  ): Promise<Uint8Array>;
  stop(): Promise<void>;
}

interface ProviderDescriptor {
  peerId: string;
  moduleUpload?: {
    protocolId?: string;
    providerX25519PubKey?: string;
    relayAddresses?: string[];
  };
}

export async function uploadModule(options: UploadModuleOptions): Promise<unknown> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const { packageFile, encryptedBundleBytes } = await readModulePackage(options.packagePath);
  const provider = await fetchProviderDescriptor(nodeOrigin, fetchImpl);
  const moduleUpload = provider.moduleUpload;
  if (!moduleUpload?.protocolId || moduleUpload.protocolId !== MODULE_UPLOAD_PROTOCOL_ID) {
    throw new Error('provider descriptor does not advertise plugin module upload protocol');
  }
  if (!moduleUpload.providerX25519PubKey) {
    throw new Error('provider descriptor missing module upload encryption key');
  }
  const relayAddresses = moduleUpload.relayAddresses ?? [];
  const contentKey = await decryptLocalContentKeyEnvelope(
    packageFile.local_content_key_envelope,
    options.wallet,
  );
  let contentKeyEnvelope: Awaited<ReturnType<typeof wrapProviderContentKey>>;
  try {
    contentKeyEnvelope = await wrapProviderContentKey(
      contentKey,
      moduleUpload.providerX25519PubKey,
      {
        module_id: packageFile.metadata.id,
        version: packageFile.metadata.version,
        bundle_sha256: packageFile.bundle_sha256,
        signer_public_key_hex: packageFile.signer_public_key_hex,
        provider_peer_id: provider.peerId,
      },
    );
  } finally {
    contentKey.fill(0);
  }
  const payload = new TextEncoder().encode(JSON.stringify({
    version: 1,
    metadata: packageFile.metadata,
    uploader_xpub: options.wallet.xpub,
    signer_public_key_hex: packageFile.signer_public_key_hex,
    signature_hex: packageFile.signature_hex,
    content_key_envelope: contentKeyEnvelope,
    encrypted_bundle_b64: bytesToBase64URL(encryptedBundleBytes),
  }));

  const node = await createUploadNode(options, nodeOrigin, relayAddresses);
  try {
    const responseBytes = await node.dialProtocol(
      provider.peerId,
      MODULE_UPLOAD_PROTOCOL_ID,
      payload,
      relayAddresses,
    );
    const responseText = new TextDecoder().decode(responseBytes);
    const response = JSON.parse(responseText);
    if (!response?.ok) {
      throw new Error(`module upload failed: ${response?.error ?? responseText}`);
    }
    return response;
  } finally {
    await node.stop();
  }
}

async function fetchProviderDescriptor(nodeOrigin: string, fetchImpl: typeof fetch): Promise<ProviderDescriptor> {
  const response = await fetchImpl(`${nodeOrigin}/api/module-delivery/provider`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) {
    throw new Error(`provider descriptor fetch failed: ${response.status} ${await response.text()}`);
  }
  const provider = await response.json() as ProviderDescriptor;
  if (!provider.peerId) {
    throw new Error('provider descriptor missing peerId');
  }
  return provider;
}

async function createUploadNode(
  options: UploadModuleOptions,
  nodeOrigin: string,
  relayAddresses: string[],
): Promise<ModuleUploadTransport> {
  if (options.nodeFactory) {
    return options.nodeFactory({ wallet: options.wallet, nodeOrigin, relayAddresses });
  }
  return SDNNode.create({
    identity: options.wallet.identity,
    edgeRelays: relayAddresses,
    enableStorage: false,
  });
}

export async function listModules(options: ListModulesOptions): Promise<unknown> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const response = await fetchImpl(`${nodeOrigin}/api/v1/plugin-modules`, {
    method: 'GET',
    headers: authHeaders(options.sessionCookie),
  });
  if (!response.ok) {
    throw new Error(`module list failed: ${response.status} ${await response.text()}`);
  }
  return response.json();
}

function authHeaders(sessionCookie: string): Record<string, string> {
  return {
    Accept: 'application/json',
    Cookie: sessionCookie,
    'X-Requested-With': 'sdn-cli',
  };
}

function normalizeNodeOrigin(nodeUrl: string): string {
  return new URL(nodeUrl).origin;
}

async function wrapProviderContentKey(
  contentKey: Uint8Array,
  providerPublicKeyBase64URL: string,
  aad: {
    module_id: string;
    version: string;
    bundle_sha256: string;
    signer_public_key_hex: string;
    provider_peer_id: string;
  },
): Promise<{
  version: 1;
  alg: 'X25519-HKDF-SHA256-AES-256-GCM';
  context: string;
  provider_x25519_pubkey: string;
  ephemeral_x25519_pubkey: string;
  nonce: string;
  aad: string;
  ciphertext: string;
}> {
  if (contentKey.length !== 32) {
    throw new Error(`content key must be 32 bytes, got ${contentKey.length}`);
  }
  const providerPublicKey = base64URLToBytes(providerPublicKeyBase64URL);
  if (providerPublicKey.length !== 32) {
    throw new Error(`provider upload public key must be 32 bytes, got ${providerPublicKey.length}`);
  }
  const ephemeralPrivateKey = generateKey();
  let sharedSecret: Uint8Array | null = null;
  let wrapKey: Uint8Array | null = null;
  try {
    const ephemeralPublicKey = await x25519PublicKey(ephemeralPrivateKey);
    sharedSecret = await x25519ECDH(ephemeralPrivateKey, providerPublicKey);
    wrapKey = await deriveProviderContentKeyWrapKey(sharedSecret);
    const nonce = crypto.getRandomValues(new Uint8Array(12));
    const aadBytes = new TextEncoder().encode(JSON.stringify(aad));
    const cryptoKey = await crypto.subtle.importKey(
      'raw',
      toArrayBuffer(wrapKey),
      { name: 'AES-GCM' },
      false,
      ['encrypt'],
    );
    const ciphertext = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv: toArrayBuffer(nonce), additionalData: toArrayBuffer(aadBytes) },
      cryptoKey,
      toArrayBuffer(contentKey),
    );
    return {
      version: 1,
      alg: 'X25519-HKDF-SHA256-AES-256-GCM',
      context: PROVIDER_CONTENT_KEY_CONTEXT,
      provider_x25519_pubkey: bytesToBase64URL(providerPublicKey),
      ephemeral_x25519_pubkey: bytesToBase64URL(ephemeralPublicKey),
      nonce: bytesToBase64URL(nonce),
      aad: bytesToBase64URL(aadBytes),
      ciphertext: bytesToBase64URL(new Uint8Array(ciphertext)),
    };
  } finally {
    ephemeralPrivateKey.fill(0);
    sharedSecret?.fill(0);
    wrapKey?.fill(0);
  }
}

async function deriveProviderContentKeyWrapKey(sharedSecret: Uint8Array): Promise<Uint8Array> {
  const baseKey = await crypto.subtle.importKey(
    'raw',
    toArrayBuffer(sharedSecret),
    'HKDF',
    false,
    ['deriveBits'],
  );
  const bits = await crypto.subtle.deriveBits(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new ArrayBuffer(0),
      info: toArrayBuffer(new TextEncoder().encode(PROVIDER_CONTENT_KEY_CONTEXT)),
    },
    baseKey,
    256,
  );
  return new Uint8Array(bits);
}

function bytesToBase64URL(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64url');
}

function base64URLToBytes(value: string): Uint8Array {
  return Uint8Array.from(Buffer.from(value, 'base64url'));
}

function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  return copy.buffer;
}
