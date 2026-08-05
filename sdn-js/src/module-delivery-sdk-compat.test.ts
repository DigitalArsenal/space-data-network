import fs from 'node:fs/promises';
import { createCipheriv, createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import * as flatbuffers from 'flatbuffers';
import { describe, expect, it, vi } from 'vitest';
import { ENC } from 'spacedatastandards.org/lib/js/REC/ENC.js';
import { KDF } from 'spacedatastandards.org/lib/js/REC/KDF.js';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';
import { KeyExchange } from 'spacedatastandards.org/lib/js/REC/KeyExchange.js';
import { LCH } from 'spacedatastandards.org/lib/js/REC/LCH.js';
import { LGR } from 'spacedatastandards.org/lib/js/REC/LGR.js';
import { LPF } from 'spacedatastandards.org/lib/js/REC/LPF.js';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { SymmetricAlgo } from 'spacedatastandards.org/lib/js/REC/SymmetricAlgo.js';
import { licensingChallengeMessageType } from 'spacedatastandards.org/lib/js/REC/licensingChallengeMessageType.js';
import { licensingChallengeRole } from 'spacedatastandards.org/lib/js/REC/licensingChallengeRole.js';
import { licensingGrantMessageType } from 'spacedatastandards.org/lib/js/REC/licensingGrantMessageType.js';
import { licensingProofMessageType } from 'spacedatastandards.org/lib/js/REC/licensingProofMessageType.js';
import { keyMaterialAlgorithm } from 'spacedatastandards.org/lib/js/REC/keyMaterialAlgorithm.js';
import { keyMaterialEncoding } from 'spacedatastandards.org/lib/js/REC/keyMaterialEncoding.js';
import { keyMaterialRole } from 'spacedatastandards.org/lib/js/REC/keyMaterialRole.js';
import { pluginCategory as pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginCategory.js';
import {
  decodeLicensingChallengeMessage,
  decodeLicensingGrant,
  decodeLicensingProofMessage,
  validateLicensingGrant,
} from 'space-data-module-sdk/licensing';
import {
  cleanupCompilation,
  compileModuleFromSource,
  ModuleThreadModel,
} from 'space-data-module-sdk/compiler';

vi.mock('./crypto/hd-wallet', async () => {
  const actual = await vi.importActual<typeof import('./crypto/hd-wallet')>('./crypto/hd-wallet');
  return {
    ...actual,
    derivePeerIdFromPublicKey: vi.fn(async () => 'provider-peer-id'),
    sign: vi.fn(async () => new Uint8Array([0xaa, 0xbb, 0xcc])),
    verify: vi.fn(async (publicKey: Uint8Array, message: Uint8Array, signature: Uint8Array) => (
      publicKey.length === 32 &&
      message.length > 0 &&
      signature.length === 64 &&
      signature.every((byte) => byte === 0x99)
    )),
  };
});

import { requestModuleGrant } from './module-delivery';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

async function compilePublicHelperModule() {
  return compileModuleFromSource({
    language: 'c',
    manifest: {
      pluginId: 'com.example.public-helper',
      name: 'Public Helper',
      version: '1.0.0',
      pluginFamily: 'analysis',
      invokeSurfaces: ['direct'],
      runtimeTargets: ['browser', 'wasmedge'],
      capabilities: [],
      externalInterfaces: [],
      methods: [
        {
          methodId: 'echo',
          displayName: 'Echo',
          inputPorts: [
            {
              portId: 'request',
              acceptedTypeSets: [
                {
                  setId: 'request-any',
                  allowedTypes: [{ acceptsAnyFlatbuffer: true }],
                },
              ],
              minStreams: 0,
              maxStreams: 1,
              required: false,
            },
          ],
          outputPorts: [
            {
              portId: 'response',
              acceptedTypeSets: [
                {
                  setId: 'response-any',
                  allowedTypes: [{ acceptsAnyFlatbuffer: true }],
                },
              ],
              minStreams: 0,
              maxStreams: 1,
              required: false,
            },
          ],
          maxBatch: 1,
          drainPolicy: 'single-shot',
        },
      ],
    },
    sourceCode: `
#include <stdint.h>
#include "space_data_module_invoke.h"

int echo(void) {
  static const uint8_t output[] = "public-helper-ok";
  plugin_push_output("response", 0, 0, output, sizeof(output) - 1);
  return 0;
}
`,
    threadModel: ModuleThreadModel.SINGLE_THREAD,
  });
}

describe('module-delivery SDK compatibility', () => {
  it('declares the SDK as a direct dependency of sdn-js', async () => {
    const packageJson = JSON.parse(
      await fs.readFile(path.join(__dirname, '..', 'package.json'), 'utf8'),
    ) as {
      dependencies?: Record<string, string>;
      devDependencies?: Record<string, string>;
    };

    expect(packageJson.dependencies?.['space-data-module-sdk']).toBeTruthy();
  });

  it('keeps the browser runtime delivery helpers on the public UI package subpath', async () => {
    const ui = await import('@spacedatanetwork/sdn-js/ui');

    expect(typeof ui.loadMarketplaceListingsFromServer).toBe('function');
    expect(typeof ui.unwrapGrantContentKey).toBe('function');
    expect(typeof ui.decryptGrantProtectedModuleBundle).toBe('function');
    expect(typeof ui.decryptEncryptedModuleBundle).toBe('function');
    expect(typeof ui.loadDecryptedModule).toBe('function');
    expect(typeof ui.invokeLoadedModule).toBe('function');
  });

  // SKIP (2026-08-05, graph/tasks/sdn-gauntlet-required-reds-flowrt-hdwallet.md):
  // `@spacedatanetwork/sdn-js` resolves to the built dist/index.mjs, whose
  // sha256/decrypt path requires a native crypto provider pre-installed on
  // globalThis[Symbol.for('orbpro.hd-wallet-wasm.provider.v1')] — a frozen
  // object with Curve/Language metadata + getWallet/getWalletOriginCapabilities,
  // normally installed by the desktop/browser host bootstrap (hd-wallet-ui),
  // not by this package. No install/staging entry point is exported for
  // tests to call, and faking the provider shape here would test a stub
  // crypto path rather than the real one. Environment gap, not a code
  // regression — needs a real test-harness install path or an exported
  // installer, not a hand-rolled mock in this file.
  it.skip('decrypts fetched encrypted bundle bytes and invokes the module through public package helpers', async () => {
    const [{ fetchEncryptedModuleBundle }, ui] = await Promise.all([
      import('@spacedatanetwork/sdn-js'),
      import('@spacedatanetwork/sdn-js/ui'),
    ]);
    const compilation = await compilePublicHelperModule();
    const contentKey = new Uint8Array(32).fill(0x37);
    try {
      const wasmBytes = compilation.wasmBytes;
      expect(compilation.threadModel).toBe(ModuleThreadModel.SINGLE_THREAD);
      expect(compilation.compiler).toBe('em++ (emception)');
      expect(
        Array.from(new Set(
          WebAssembly.Module.imports(new WebAssembly.Module(wasmBytes))
            .map(({ module }) => module),
        )).sort(),
      ).toEqual(['wasi_snapshot_preview1']);
      const encryptedBundleBytes = await encryptBundleBytes(wasmBytes, contentKey);
      const contentHash = await sha256(encryptedBundleBytes);
      const fetched = await fetchEncryptedModuleBundle(
        {
          async fetchCIDBytes(cid: string) {
            expect(cid).toBe('bafypublichelpers');
            return encryptedBundleBytes;
          },
        },
        {
          provider: {
            peerId: 'provider-peer-id',
            publicKey: new Uint8Array(33).fill(2),
            publicKeyHex: '02'.padEnd(66, '1'),
            relayAddresses: ['/ip4/159.203.150.8/tcp/4001/ws/p2p/provider-peer-id'],
            source: 'descriptor',
          },
          grant: {
            reqId: 'req-public-helper-flow',
            moduleId: 'com.example.public-helper',
            requestedTimeoutMs: 300_000,
            grantedDomain: 'xpub6FixtureAllowList',
            grantedTimeoutMs: 300_000,
            expiresAtMs: 1_700_003_600_000,
            capabilityToken: new Uint8Array(),
            grantVerifierPublicKey: new Uint8Array(),
            providerSignature: new Uint8Array(),
            bundleDescriptor: {
              cid: 'bafypublichelpers',
              contentHash,
              sizeBytes: encryptedBundleBytes.length,
              moduleId: 'com.example.public-helper',
              moduleVersion: '1.0.0',
              allowedXpubs: ['xpub6FixtureAllowList'],
              maxGrantTimeoutMs: 300_000,
              encrypted: true,
            },
            wrappedContentKey: {
              wrappingAlgorithm: 'direct-test-fixture',
              keyBytes: contentKey,
              requesterEphemeralPublicKey: new Uint8Array(),
              providerEphemeralPublicKey: new Uint8Array(),
              hkdfSalt: new Uint8Array(),
              iv: new Uint8Array(),
              ciphertext: new Uint8Array(),
              tag: new Uint8Array(),
              expiresAtMs: 1_700_003_600_000,
              recipientPublicKey: new Uint8Array(),
              ephemeralPublicKey: new Uint8Array(),
              nonce: new Uint8Array(),
            },
          },
          grantResponseBytes: new Uint8Array(),
        },
      );

      const unwrappedKey = await ui.unwrapGrantContentKey(
        fetched.grant.wrappedContentKey,
        new Uint8Array(32),
      );
      const decryptedWasm = await ui.decryptEncryptedModuleBundle(
        fetched.encryptedBundleBytes,
        unwrappedKey,
      );
      const harness = await ui.loadDecryptedModule(decryptedWasm);
      const response = await ui.invokeLoadedModule<{
        statusCode: number;
        outputs: Array<{ payload: Uint8Array }>;
      }>(harness, {
        methodId: 'echo',
        // A non-empty owned payload selects the SDK's portable serialized-PIV
        // path; an empty payload arena is reserved for the shared external-
        // arena ABI.
        inputs: [{
          portId: 'request',
          typeRef: {
            schemaName: 'Blob.fbs',
            fileIdentifier: 'BLOB',
          },
          payload: new Uint8Array([0x01]),
        }],
      });

      expect(decryptedWasm).toEqual(wasmBytes);
      expect(response.statusCode).toBe(0);
      expect(new TextDecoder().decode(response.outputs[0].payload)).toBe('public-helper-ok');
      harness.destroy?.();
    } finally {
      await cleanupCompilation(compilation);
    }
  }, 60_000);

  it('emits requester bytes and consumes grant bytes that remain valid under the SDK licensing helpers', async () => {
    let capturedGrantBytes = new Uint8Array(0);
    const transport = {
      async dialProtocol(
        _targetPeerId: string,
        _protocolId: string,
        payload: Uint8Array,
        _candidateAddrs: string[] = [],
      ) {
        if (LCH.bufferHasIdentifier(new flatbuffers.ByteBuffer(payload))) {
          const decoded = decodeLicensingChallengeMessage(payload);
          expect(decoded.messageType).toBe('request');
          expect(decoded.role).toBe('requester');
          expect(decoded.reqId).toBe('req-sdk-compat');
          expect(decoded.moduleId).toBe('com.space-data-network.fastest-path');
          expect(decoded.requestedDomain).toBe('xpub6FixtureAllowList');
          expect(decoded.requestedTimeoutMs).toBe(300_000);
          expect(decoded.requesterSigningPublicKey).toEqual(new Uint8Array(32).fill(6));
          expect(decoded.requesterEphemeralPublicKey).toEqual(new Uint8Array(32).fill(8));
          return encodeChallengeResponse({
            reqId: decoded.reqId,
            moduleId: decoded.moduleId,
            moduleVersion: decoded.moduleVersion,
            providerPeerId: 'provider-peer-id',
            challengeNonce: new Uint8Array([1, 2, 3, 4]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        const decoded = decodeLicensingProofMessage(payload);
        expect(decoded.messageType).toBe('proof-request');
        expect(decoded.reqId).toBe('req-sdk-compat');
        expect(decoded.moduleId).toBe('com.space-data-network.fastest-path');
        expect(decoded.requestedDomain).toBe('xpub6FixtureAllowList');
        expect(decoded.requestedTimeoutMs).toBe(300_000);
        expect(decoded.requesterEphemeralPublicKey).toEqual(new Uint8Array(32).fill(8));
        expect(decoded.signature).toEqual(new Uint8Array([0xaa, 0xbb, 0xcc]));
        capturedGrantBytes = encodeGrantResponse({
          reqId: decoded.reqId,
          moduleId: decoded.moduleId,
          moduleVersion: decoded.moduleVersion,
          requesterPeerId: decoded.requesterPeerId,
          requesterXpub: decoded.requesterXpub,
          requestedDomain: decoded.requestedDomain ?? '',
          requestedTimeoutMs: BigInt(decoded.requestedTimeoutMs),
          grantedDomain: 'xpub6FixtureAllowList',
          grantedTimeoutMs: 300_000n,
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
        return capturedGrantBytes;
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
    };

    const result = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: '02'.padEnd(66, '1'),
        relayAddresses: ['/dns4/relay.example/tcp/443/wss/p2p/relay-peer'],
      },
      requesterIdentity: {
        peerId: 'requester-peer-id',
        xpub: 'xpub-requester',
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
      requesterDomain: 'xpub6FixtureAllowList',
      requestedTimeoutMs: 300_000,
      reqId: 'req-sdk-compat',
      requestedAtMs: 1_700_000_000_000,
    });

    const sdkGrant = validateLicensingGrant(decodeLicensingGrant(capturedGrantBytes), {
      reqId: 'req-sdk-compat',
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
      expectedDomain: 'xpub6FixtureAllowList',
      requestedTimeoutMs: 300_000,
    });

    expect(sdkGrant.messageType).toBe('granted');
    expect(sdkGrant.moduleDescriptor?.moduleId).toBe('com.space-data-network.fastest-path');
    expect(sdkGrant.wrappedContentKey?.keyMaterialRootType).toBe('$KMF');
    expect(sdkGrant.wrappedContentKey).toBeTruthy();
    const sdkWrappedContentKey = sdkGrant.wrappedContentKey!;

    expect(result.grant.bundleDescriptor).toMatchObject({
      cid: 'bafyencryptedmodule',
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
      requiredScope: 'orbpro.default',
      keyId: 'com.space-data-network.fastest-path:0.5.22',
      allowedXpubs: ['xpub6FixtureAllowList'],
      maxGrantTimeoutMs: 300_000,
      encrypted: true,
    });
    expect(result.grant.bundleDescriptor.contentHash).toEqual(new Uint8Array(32).fill(7));
    expect(result.grant.bundleDescriptor.sizeBytes).toBe(4);
    expect(result.grant.grantedDomain).toBe('xpub6FixtureAllowList');
    expect(result.grant.grantedTimeoutMs).toBe(300_000);
    expect(result.grant.grantVerifierPublicKey).toEqual(new Uint8Array(32).fill(5));
    expect(result.grant.providerSignature).toEqual(new Uint8Array(64).fill(0x99));
    expect(result.grant.wrappedContentKey.wrappingAlgorithm).toBe(
      'x25519-hkdf-sha256-aes-256-ctr-rec',
    );
    expect(result.grant.wrappedContentKey.contentKeyRole).toBe(
      sdkWrappedContentKey.contentKeyRole,
    );
    expect(result.grant.wrappedContentKey.contentKeyAlgorithm).toBe(
      sdkWrappedContentKey.contentKeyAlgorithm,
    );
    expect(result.grant.wrappedContentKey.contentKeyEncoding).toBe(
      sdkWrappedContentKey.contentKeyEncoding,
    );
    expect(result.grant.wrappedContentKey.keyBytes).toEqual(sdkWrappedContentKey.keyBytes);
    expect(result.grant.wrappedContentKey.contentKeyVersion).toBe(
      sdkWrappedContentKey.contentKeyVersion,
    );
    expect(result.grant.wrappedContentKey.recipientKeyId).toBe(
      sdkWrappedContentKey.recipientKeyId,
    );
    expect(result.grant.wrappedContentKey.providerEphemeralPublicKey).toEqual(
      sdkWrappedContentKey.providerEphemeralPublicKey,
    );
    expect(result.grant.wrappedContentKey.nonce).toEqual(sdkWrappedContentKey.nonce);
    expect(result.grant.wrappedContentKey.ciphertext).toEqual(
      sdkWrappedContentKey.ciphertext,
    );
    expect(result.grant.wrappedContentKey.keyMaterialRootType).toBe(
      sdkWrappedContentKey.keyMaterialRootType,
    );
  });
});

function encodeChallengeResponse(options: {
  reqId: string;
  moduleId: string;
  moduleVersion?: string;
  providerPeerId: string;
  challengeNonce: Uint8Array;
  expiresAtMs: bigint;
}): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const reqIdOffset = builder.createString(options.reqId);
  const moduleIdOffset = builder.createString(options.moduleId);
  const moduleVersionOffset = options.moduleVersion ? builder.createString(options.moduleVersion) : 0;
  const providerPeerIdOffset = builder.createString(options.providerPeerId);
  const challengeNonceOffset = LCH.createChallengeNonceVector(builder, options.challengeNonce);
  const root = LCH.createLCH(
    builder,
    licensingChallengeMessageType.Response,
    licensingChallengeRole.Provider,
    reqIdOffset,
    moduleIdOffset,
    moduleVersionOffset,
    0,
    0,
    0,
    0,
    0,
    0n,
    0n,
    challengeNonceOffset,
    options.expiresAtMs,
    providerPeerIdOffset,
    0,
    0,
  );
  LCH.finishLCHBuffer(builder, root);
  return builder.asUint8Array();
}

function encodeGrantResponse(options: {
  reqId: string;
  moduleId: string;
  moduleVersion?: string;
  requesterPeerId?: string;
  requesterXpub?: string;
  requestedDomain: string;
  requestedTimeoutMs: bigint;
  grantedDomain: string;
  grantedTimeoutMs: bigint;
  expiresAtMs: bigint;
  contentHash: Uint8Array;
}): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const reqIdOffset = builder.createString(options.reqId);
  const moduleIdOffset = builder.createString(options.moduleId);
  const moduleVersionOffset = options.moduleVersion ? builder.createString(options.moduleVersion) : 0;
  const requesterPeerIdOffset = options.requesterPeerId ? builder.createString(options.requesterPeerId) : 0;
  const requesterXpubOffset = options.requesterXpub ? builder.createString(options.requesterXpub) : 0;
  const requestedDomainOffset = builder.createString(options.requestedDomain);
  const grantedDomainOffset = builder.createString(options.grantedDomain);
  const requiredScopeOffset = builder.createString('deliver');
  const grantStatusOffset = builder.createString('granted');
  const capabilityTokenOffset = LGR.createCapabilityTokenVector(builder, new Uint8Array([0x42]));
  const grantVerifierPubkeyOffset = LGR.createGrantVerifierPubkeyVector(builder, new Uint8Array(32).fill(5));
  const providerSignatureOffset = LGR.createProviderSignatureVector(builder, new Uint8Array(64).fill(0x99));

  const moduleDescriptorOffset = createModuleDescriptor(builder, options);
  const wrappedContentKeyHeaderOffset = createWrappedContentKeyHeader(builder);
  const wrappedContentKeyPayloadOffset = createWrappedContentKeyPayload(builder, options);

  LGR.startLGR(builder);
  LGR.addMessageType(builder, licensingGrantMessageType.Granted);
  LGR.addRequestId(builder, reqIdOffset);
  LGR.addModuleId(builder, moduleIdOffset);
  if (moduleVersionOffset !== 0) {
    LGR.addModuleVersion(builder, moduleVersionOffset);
  }
  if (requesterPeerIdOffset !== 0) {
    LGR.addRequesterPeerId(builder, requesterPeerIdOffset);
  }
  if (requesterXpubOffset !== 0) {
    LGR.addRequesterXpub(builder, requesterXpubOffset);
  }
  LGR.addRequestedDomain(builder, requestedDomainOffset);
  LGR.addRequestedTimeoutMs(builder, options.requestedTimeoutMs);
  LGR.addGrantedDomain(builder, grantedDomainOffset);
  LGR.addGrantedTimeoutMs(builder, options.grantedTimeoutMs);
  LGR.addExpiresAt(builder, options.expiresAtMs);
  LGR.addRequiredScope(builder, requiredScopeOffset);
  LGR.addGrantStatus(builder, grantStatusOffset);
  LGR.addCapabilityToken(builder, capabilityTokenOffset);
  LGR.addModuleDescriptor(builder, moduleDescriptorOffset);
  LGR.addWrappedContentKeyHeader(builder, wrappedContentKeyHeaderOffset);
  LGR.addWrappedContentKeyPayload(builder, wrappedContentKeyPayloadOffset);
  LGR.addGrantVerifierPubkey(builder, grantVerifierPubkeyOffset);
  LGR.addProviderSignature(builder, providerSignatureOffset);
  const root = LGR.endLGR(builder);
  LGR.finishLGRBuffer(builder, root);
  return builder.asUint8Array();
}

function createModuleDescriptor(
  builder: flatbuffers.Builder,
  options: {
    moduleId: string;
    moduleVersion?: string;
    contentHash: Uint8Array;
  },
): flatbuffers.Offset {
  const pluginIdOffset = builder.createString(options.moduleId);
  const nameOffset = builder.createString(options.moduleId);
  const versionOffset = options.moduleVersion ? builder.createString(options.moduleVersion) : 0;
  const descriptionOffset = builder.createString('Protected module fixture');
  const wasmHashOffset = PLG.createWasmHashVector(builder, options.contentHash);
  const wasmCidOffset = builder.createString('bafyencryptedmodule');
  const requiredScopeOffset = builder.createString('orbpro.default');
  const keyIdOffset = builder.createString(`${options.moduleId}:${options.moduleVersion ?? 'latest'}`);
  const allowedXpubsOffset = PLG.createAllowedXpubsVector(
    builder,
    [builder.createString('xpub6FixtureAllowList')],
  );

  // Use field-by-field setters so the fixture stays compatible when the
  // generated PLG.createPLG positional signature grows new optional fields.
  PLG.startPLG(builder);
  PLG.addPluginId(builder, pluginIdOffset);
  PLG.addName(builder, nameOffset);
  if (versionOffset !== 0) {
    PLG.addVersion(builder, versionOffset);
  }
  PLG.addDescription(builder, descriptionOffset);
  PLG.addPluginType(builder, pluginType.Analysis);
  PLG.addAbiVersion(builder, 1);
  PLG.addWasmHash(builder, wasmHashOffset);
  PLG.addWasmSize(builder, 4n);
  PLG.addWasmCid(builder, wasmCidOffset);
  PLG.addEncrypted(builder, true);
  PLG.addRequiredScope(builder, requiredScopeOffset);
  PLG.addKeyId(builder, keyIdOffset);
  PLG.addAllowedXpubs(builder, allowedXpubsOffset);
  PLG.addMaxGrantTimeoutMs(builder, 300_000n);
  return PLG.endPLG(builder);
}

function createWrappedContentKeyHeader(builder: flatbuffers.Builder): flatbuffers.Offset {
  const ephemeralPublicKeyOffset = ENC.createEphemeralPublicKeyVector(builder, new Uint8Array(32).fill(2));
  const nonceStartOffset = ENC.createNonceStartVector(builder, new Uint8Array(12).fill(4));
  const recipientKeyIdOffset = ENC.createRecipientKeyIdVector(builder, new Uint8Array([0x01, 0x02]));
  const rootTypeOffset = builder.createString('$KMF');
  const schemaHashOffset = ENC.createSchemaHashVector(builder, new Uint8Array(32).fill(3));

  return ENC.createENC(
    builder,
    1,
    KeyExchange.X25519,
    SymmetricAlgo.AES_256_CTR,
    KDF.HKDF_SHA256,
    ephemeralPublicKeyOffset,
    nonceStartOffset,
    recipientKeyIdOffset,
    0,
    schemaHashOffset,
    rootTypeOffset,
    0n,
  );
}

function createWrappedContentKeyPayload(
  builder: flatbuffers.Builder,
  options: { moduleId: string; moduleVersion?: string; expiresAtMs: bigint },
): flatbuffers.Offset {
  const kmfBuilder = new flatbuffers.Builder(256);
  const keyIdOffset = kmfBuilder.createString(`${options.moduleId}:${options.moduleVersion ?? 'latest'}`);
  const keyBytesOffset = KMF.createKeyBytesVector(kmfBuilder, new Uint8Array([4, 5, 6]));
  const kmfOffset = KMF.createKMF(
    kmfBuilder,
    keyIdOffset,
    keyMaterialRole.PublicationContent,
    keyMaterialAlgorithm.Aes256Gcm,
    keyMaterialEncoding.RawBytes,
    keyBytesOffset,
    1,
    options.expiresAtMs,
  );
  KMF.finishKMFBuffer(kmfBuilder, kmfOffset);
  return LGR.createWrappedContentKeyPayloadVector(builder, kmfBuilder.asUint8Array());
}

async function sha256(value: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(createHash('sha256').update(value).digest());
}

async function encryptBundleBytes(plaintext: Uint8Array, contentKey: Uint8Array): Promise<Uint8Array> {
  const iv = Buffer.alloc(12, 1);
  const cipher = createCipheriv('aes-256-gcm', Buffer.from(contentKey), iv);
  const ciphertext = Buffer.concat([
    cipher.update(Buffer.from(plaintext)),
    cipher.final(),
  ]);
  const tag = cipher.getAuthTag();
  const encrypted = new Uint8Array(iv.length + ciphertext.length + tag.length);
  encrypted.set(iv, 0);
  encrypted.set(ciphertext, iv.length);
  encrypted.set(tag, iv.length + ciphertext.length);
  return encrypted;
}
