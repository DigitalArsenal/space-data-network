import fs from "node:fs/promises";
import path from "node:path";
import { createHash } from "node:crypto";
import * as flatbuffers from "flatbuffers";
import { describe, expect, it, vi } from "vitest";
import { ENC } from "spacedatastandards.org/lib/js/REC/ENC.js";
import { KDF } from "spacedatastandards.org/lib/js/REC/KDF.js";
import { KMF } from "spacedatastandards.org/lib/js/REC/KMF.js";
import { KeyExchange } from "spacedatastandards.org/lib/js/REC/KeyExchange.js";
import { LCH } from "spacedatastandards.org/lib/js/REC/LCH.js";
import { LGR } from "spacedatastandards.org/lib/js/REC/LGR.js";
import { LPF } from "spacedatastandards.org/lib/js/REC/LPF.js";
import { PLG } from "spacedatastandards.org/lib/js/REC/PLG.js";
import { SymmetricAlgo } from "spacedatastandards.org/lib/js/REC/SymmetricAlgo.js";
import { licensingChallengeMessageType } from "spacedatastandards.org/lib/js/REC/licensingChallengeMessageType.js";
import { licensingChallengeRole } from "spacedatastandards.org/lib/js/REC/licensingChallengeRole.js";
import { licensingGrantMessageType } from "spacedatastandards.org/lib/js/REC/licensingGrantMessageType.js";
import { licensingProofMessageType } from "spacedatastandards.org/lib/js/REC/licensingProofMessageType.js";
import { keyMaterialAlgorithm } from "spacedatastandards.org/lib/js/REC/keyMaterialAlgorithm.js";
import { keyMaterialEncoding } from "spacedatastandards.org/lib/js/REC/keyMaterialEncoding.js";
import { keyMaterialRole } from "spacedatastandards.org/lib/js/REC/keyMaterialRole.js";
import { pluginCategory as pluginType } from "spacedatastandards.org/lib/js/PLG/pluginCategory.js";

vi.mock("./crypto/hd-wallet", () => {
  return {
    derivePeerIdFromPublicKey: vi.fn(async () => "provider-peer-id"),
    sign: vi.fn(async () => new Uint8Array([0xaa, 0xbb, 0xcc])),
    sha256: vi.fn(async (value: Uint8Array) => {
      return new Uint8Array(createHash("sha256").update(value).digest());
    }),
  };
});

import { MODULE_DELIVERY_DISCOVERY_NAMESPACE } from "./discovery";
import {
  MODULE_DELIVERY_PROTOCOL_ID,
  fetchEncryptedModuleBundle,
  requestModuleGrant,
} from "./module-delivery";

describe("module-delivery", () => {
  it("keeps the module-delivery requester API on the public root package export", async () => {
    const sdn = await import("@spacedatanetwork/sdn-js");

    expect(typeof sdn.SDNNode).toBe("function");
    expect(typeof sdn.requestEncryptedModuleBundle).toBe("function");
    expect(typeof sdn.requestModuleGrant).toBe("function");
    expect(typeof sdn.fetchEncryptedModuleBundle).toBe("function");
    expect(sdn.MODULE_DELIVERY_PROTOCOL_ID).toBe(MODULE_DELIVERY_PROTOCOL_ID);
  });

  it("performs the raw SDS challenge and proof exchange over the module delivery protocol", async () => {
    const transport = {
      grantResponseBytes: new Uint8Array(),
      calls: [] as Array<{
        targetPeerId: string;
        protocolId: string;
        payload: Uint8Array;
        candidateAddrs: string[];
      }>,
      async dialProtocol(
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array,
        candidateAddrs: string[] = [],
      ) {
        this.calls.push({ targetPeerId, protocolId, payload, candidateAddrs });

        if (isLCH(payload)) {
          const request = decodeLCH(payload);
          expect(request.MESSAGE_TYPE()).toBe(
            licensingChallengeMessageType.Request,
          );
          expect(request.ROLE()).toBe(licensingChallengeRole.Requester);
          return encodeChallengeResponse({
            reqId: request.REQUEST_ID() ?? "",
            moduleId: request.MODULE_ID() ?? "",
            moduleVersion: request.MODULE_VERSION() ?? undefined,
            providerPeerId: "provider-peer-id",
            challengeNonce: new Uint8Array([1, 2, 3, 4]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        expect(isLPF(payload)).toBe(true);
        const proof = decodeLPF(payload);
        expect(proof.MESSAGE_TYPE()).toBe(
          licensingProofMessageType.ProofRequest,
        );
        const grantResponseBytes = encodeGrantResponse({
          reqId: proof.REQUEST_ID() ?? "",
          moduleId: proof.MODULE_ID() ?? "",
          moduleVersion: proof.MODULE_VERSION() ?? undefined,
          requesterPeerId: proof.REQUESTER_PEER_ID() ?? undefined,
          requesterXpub: proof.REQUESTER_XPUB() ?? undefined,
          requestedDomain: proof.REQUESTED_DOMAIN() ?? "",
          requestedTimeoutMs: proof.REQUESTED_TIMEOUT_MS(),
          grantedDomain: "app.example.com",
          grantedTimeoutMs: 300_000n,
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
        this.grantResponseBytes = grantResponseBytes;
        return grantResponseBytes;
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
    };

    const result = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: "02".padEnd(66, "1"),
        relayAddresses: ["/dns4/relay.example/tcp/443/wss/p2p/relay-peer"],
      },
      requesterIdentity: {
        peerId: "requester-peer-id",
        xpub: "xpub-requester",
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: "com.space-data-network.fastest-path",
      moduleVersion: "0.5.22",
      requesterDomain: "app.example.com",
      requestedTimeoutMs: 300_000,
      reqId: "req-123",
      requestedAtMs: 1_700_000_000_000,
    });

    expect(transport.calls).toHaveLength(2);
    expect(transport.calls[0]).toMatchObject({
      targetPeerId: "provider-peer-id",
      protocolId: MODULE_DELIVERY_PROTOCOL_ID,
      candidateAddrs: ["/dns4/relay.example/tcp/443/wss/p2p/relay-peer"],
    });
    const challengeRequest = decodeLCH(transport.calls[0].payload);
    expect(challengeRequest.REQUEST_ID()).toBe("req-123");
    expect(challengeRequest.REQUESTER_PEER_ID()).toBe("requester-peer-id");
    expect(challengeRequest.REQUESTER_XPUB()).toBe("xpub-requester");
    expect(challengeRequest.REQUESTED_DOMAIN()).toBe("app.example.com");
    expect(challengeRequest.REQUESTED_TIMEOUT_MS()).toBe(300_000n);
    expect(challengeRequest.requesterSigningPubkeyArray()).toEqual(
      new Uint8Array(32).fill(6),
    );
    expect(challengeRequest.requesterEphemeralPubkeyArray()).toEqual(
      new Uint8Array(32).fill(8),
    );

    const proofRequest = decodeLPF(transport.calls[1].payload);
    expect(proofRequest.REQUEST_ID()).toBe("req-123");
    expect(proofRequest.REQUESTED_DOMAIN()).toBe("app.example.com");
    expect(proofRequest.REQUESTED_TIMEOUT_MS()).toBe(300_000n);
    expect(proofRequest.signatureArray()).toEqual(
      new Uint8Array([0xaa, 0xbb, 0xcc]),
    );

    expect(result.grant.bundleDescriptor).toMatchObject({
      cid: "bafyencryptedmodule",
      moduleId: "com.space-data-network.fastest-path",
      moduleVersion: "0.5.22",
    });
    expect(result.grant.grantedDomain).toBe("app.example.com");
    expect(result.grant.grantedTimeoutMs).toBe(300_000);
    expect(result.grant.grantVerifierPublicKey).toEqual(
      new Uint8Array(32).fill(5),
    );
    expect(result.grant.wrappedContentKey.header?.rootType).toBe("$KMF");
    expect(result.grant.wrappedContentKey.nonce).toEqual(
      new Uint8Array(12).fill(4),
    );
    expect(result.grant.wrappedContentKey.ciphertext.length).toBeGreaterThan(0);
    expect(result.provider.peerId).toBe("provider-peer-id");
    expect(result.grantResponseBytes).toEqual(transport.grantResponseBytes);
  });

  it("treats descriptor relay addresses as authoritative and skips discovery plus legacy bootstrap helpers", async () => {
    const legacyHttpBootstrap = vi.fn(async () => {
      throw new Error("legacy bootstrap must not run");
    });
    const discoverProviders = vi.fn(async () => [
      {
        peerId: "provider-peer-id",
        multiaddrs: [
          "/dns4/discovered-relay.example/tcp/443/wss/p2p/discovered-relay-peer",
        ],
      },
    ]);
    const transport = {
      dialCalls: [] as Array<{
        targetPeerId: string;
        protocolId: string;
        candidateAddrs: string[];
        payload: Uint8Array;
      }>,
      discoverProviders,
      async dialProtocol(
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array,
        candidateAddrs: string[] = [],
      ) {
        this.dialCalls.push({
          targetPeerId,
          protocolId,
          candidateAddrs,
          payload,
        });

        if (isLCH(payload)) {
          const request = decodeLCH(payload);
          return encodeChallengeResponse({
            reqId: request.REQUEST_ID() ?? "",
            moduleId: request.MODULE_ID() ?? "",
            moduleVersion: request.MODULE_VERSION() ?? undefined,
            providerPeerId: "provider-peer-id",
            challengeNonce: new Uint8Array([8, 7, 6, 5]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        const proof = decodeLPF(payload);
        return encodeGrantResponse({
          reqId: proof.REQUEST_ID() ?? "",
          moduleId: proof.MODULE_ID() ?? "",
          moduleVersion: proof.MODULE_VERSION() ?? undefined,
          requesterPeerId: proof.REQUESTER_PEER_ID() ?? undefined,
          requesterXpub: proof.REQUESTER_XPUB() ?? undefined,
          requestedDomain: proof.REQUESTED_DOMAIN() ?? "",
          requestedTimeoutMs: proof.REQUESTED_TIMEOUT_MS(),
          grantedDomain: "app.example.com",
          grantedTimeoutMs: proof.REQUESTED_TIMEOUT_MS(),
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
      nodeInfo: legacyHttpBootstrap,
      fetchNodeInfo: legacyHttpBootstrap,
      fetchPublicKey: legacyHttpBootstrap,
    };

    const explicitRelay = "/dns4/relay.example/tcp/443/wss/p2p/relay-peer";
    const result = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: "02".padEnd(66, "1"),
        relayAddresses: [explicitRelay],
      },
      requesterIdentity: {
        peerId: "requester-peer-id",
        xpub: "xpub-requester",
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: "com.space-data-network.fastest-path",
      moduleVersion: "0.5.22",
      requesterDomain: "app.example.com",
      requestedTimeoutMs: 30_000,
      reqId: "req-explicit-relay",
      requestedAtMs: 1_700_000_000_000,
    });

    expect(discoverProviders).not.toHaveBeenCalled();
    expect(transport.dialCalls[0]).toMatchObject({
      targetPeerId: "provider-peer-id",
      protocolId: MODULE_DELIVERY_PROTOCOL_ID,
      candidateAddrs: [explicitRelay],
    });
    expect(legacyHttpBootstrap).not.toHaveBeenCalled();
    expect(result.provider.relayAddresses).toEqual([explicitRelay]);
  });

  it("fetches the encrypted bundle bytes by CID and verifies the declared content hash", async () => {
    const content = new Uint8Array([10, 20, 30, 40]);
    const contentHash = await sha256(content);
    const transport = {
      async fetchCIDBytes(cid: string) {
        expect(cid).toBe("bafyencryptedmodule");
        return content;
      },
    };

    const bundle = await fetchEncryptedModuleBundle(transport, {
      grant: {
        bundleDescriptor: {
          cid: "bafyencryptedmodule",
          contentHash,
          sizeBytes: 4,
          moduleId: "com.space-data-network.fastest-path",
        },
        wrappedContentKey: {
          wrappingAlgorithm: "x25519",
          recipientPublicKey: new Uint8Array(32),
          ephemeralPublicKey: new Uint8Array(32),
          nonce: new Uint8Array(24),
          ciphertext: new Uint8Array(3),
          tag: new Uint8Array(3),
        },
      },
      provider: {
        peerId: "provider-peer-id",
        publicKey: hexToBytes("02".padEnd(66, "1")),
        publicKeyHex: "02".padEnd(66, "1"),
        relayAddresses: [],
        source: "descriptor",
      },
    });

    expect(bundle.encryptedBundleBytes).toEqual(content);
  });

  it("fails when the fetched bundle hash does not match the grant descriptor", async () => {
    await expect(
      fetchEncryptedModuleBundle(
        {
          async fetchCIDBytes() {
            return new Uint8Array([1, 2, 3, 4]);
          },
        },
        {
          grant: {
            bundleDescriptor: {
              cid: "bafyencryptedmodule",
              contentHash: new Uint8Array(32).fill(9),
              sizeBytes: 4,
              moduleId: "com.space-data-network.fastest-path",
            },
            wrappedContentKey: {
              wrappingAlgorithm: "x25519",
              recipientPublicKey: new Uint8Array(32),
              ephemeralPublicKey: new Uint8Array(32),
              nonce: new Uint8Array(24),
              ciphertext: new Uint8Array(3),
              tag: new Uint8Array(3),
            },
          },
          provider: {
            peerId: "provider-peer-id",
            publicKey: hexToBytes("02".padEnd(66, "1")),
            publicKeyHex: "02".padEnd(66, "1"),
            relayAddresses: [],
            source: "descriptor",
          },
        },
      ),
    ).rejects.toThrow(/hash mismatch/i);
  });

  it("discovers provider relay candidates from the normalized public key and never uses legacy bootstrap helpers", async () => {
    const publicIndex = await fs.readFile(
      path.join(__dirname, "index.ts"),
      "utf8",
    );
    expect(publicIndex.includes("/api/node/info")).toBe(false);
    expect(publicIndex.includes("/orbpro/")).toBe(false);

    const legacyHttpBootstrap = vi.fn(async () => {
      throw new Error("legacy bootstrap must not run");
    });

    const transport = {
      discoveryCalls: [] as string[],
      dialCalls: [] as Array<{
        targetPeerId: string;
        protocolId: string;
        candidateAddrs: string[];
        payload: Uint8Array;
      }>,
      async discoverProviders(discoveryCID: string) {
        this.discoveryCalls.push(discoveryCID);
        return [
          {
            peerId: "provider-peer-id",
            multiaddrs: [
              "/dns4/discovered-relay.example/tcp/443/wss/p2p/discovered-relay-peer",
            ],
          },
        ];
      },
      async dialProtocol(
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array,
        candidateAddrs: string[] = [],
      ) {
        this.dialCalls.push({
          targetPeerId,
          protocolId,
          candidateAddrs,
          payload,
        });

        if (isLCH(payload)) {
          const request = decodeLCH(payload);
          return encodeChallengeResponse({
            reqId: request.REQUEST_ID() ?? "",
            moduleId: request.MODULE_ID() ?? "",
            moduleVersion: request.MODULE_VERSION() ?? undefined,
            providerPeerId: "provider-peer-id",
            challengeNonce: new Uint8Array([5, 6, 7]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        const proof = decodeLPF(payload);
        return encodeGrantResponse({
          reqId: proof.REQUEST_ID() ?? "",
          moduleId: proof.MODULE_ID() ?? "",
          moduleVersion: proof.MODULE_VERSION() ?? undefined,
          requesterPeerId: proof.REQUESTER_PEER_ID() ?? undefined,
          requesterXpub: proof.REQUESTER_XPUB() ?? undefined,
          requestedDomain: "example.org",
          requestedTimeoutMs: 30_000n,
          grantedDomain: "example.org",
          grantedTimeoutMs: 30_000n,
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
      nodeInfo: legacyHttpBootstrap,
      fetchNodeInfo: legacyHttpBootstrap,
      fetchPublicKey: legacyHttpBootstrap,
    };

    const result = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: "02".padEnd(66, "1"),
      },
      requesterDomain: "example.org",
      requesterIdentity: {
        peerId: "requester-peer-id",
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: "com.space-data-network.fastest-path",
      reqId: "req-discovery",
      requestedTimeoutMs: 30_000,
      requestedAtMs: 1_700_000_000_000,
    });

    expect(transport.discoveryCalls).toHaveLength(1);
    expect(transport.discoveryCalls[0].startsWith("b")).toBe(true);
    expect(transport.dialCalls[0]).toMatchObject({
      targetPeerId: "provider-peer-id",
      protocolId: MODULE_DELIVERY_PROTOCOL_ID,
      candidateAddrs: [
        "/dns4/discovered-relay.example/tcp/443/wss/p2p/discovered-relay-peer",
      ],
    });
    expect(result.provider.peerId).toBe("provider-peer-id");
    expect(result.provider.relayAddresses).toEqual([]);
    expect(legacyHttpBootstrap).not.toHaveBeenCalled();
    expect(MODULE_DELIVERY_DISCOVERY_NAMESPACE).toBe(
      "space-data-network/module-delivery/provider-pubkey",
    );
  });

  it("prefers an explicit license bootstrap discovery CID over descriptor relay addresses", async () => {
    const licenseBootstrapCID =
      "bafkreiceqr2v4fvjqddussy5wydruetwmupxzvlett4ezl3zywg4ndl2di";
    const descriptorRelay =
      "/dns4/descriptor-relay.example/tcp/443/wss/p2p/descriptor-relay-peer";
    const discoveredRelay =
      "/dns4/discovered-license.example/tcp/443/wss/p2p/discovered-license-peer";
    const transport = {
      discoveryCalls: [] as string[],
      dialCalls: [] as Array<{
        targetPeerId: string;
        protocolId: string;
        candidateAddrs: string[];
        payload: Uint8Array;
      }>,
      async discoverProviders(discoveryCID: string) {
        this.discoveryCalls.push(discoveryCID);
        return [
          {
            peerId: "provider-peer-id",
            multiaddrs: [discoveredRelay],
          },
        ];
      },
      async dialProtocol(
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array,
        candidateAddrs: string[] = [],
      ) {
        this.dialCalls.push({
          targetPeerId,
          protocolId,
          candidateAddrs,
          payload,
        });

        if (isLCH(payload)) {
          const request = decodeLCH(payload);
          return encodeChallengeResponse({
            reqId: request.REQUEST_ID() ?? "",
            moduleId: request.MODULE_ID() ?? "",
            moduleVersion: request.MODULE_VERSION() ?? undefined,
            providerPeerId: "provider-peer-id",
            challengeNonce: new Uint8Array([5, 6, 7]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        const proof = decodeLPF(payload);
        return encodeGrantResponse({
          reqId: proof.REQUEST_ID() ?? "",
          moduleId: proof.MODULE_ID() ?? "",
          moduleVersion: proof.MODULE_VERSION() ?? undefined,
          requesterPeerId: proof.REQUESTER_PEER_ID() ?? undefined,
          requesterXpub: proof.REQUESTER_XPUB() ?? undefined,
          requestedDomain: proof.REQUESTED_DOMAIN() ?? "",
          requestedTimeoutMs: proof.REQUESTED_TIMEOUT_MS(),
          grantedDomain: "app.example.com",
          grantedTimeoutMs: 300_000n,
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
    };

    await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: "02".padEnd(66, "1"),
        discoveryCID: licenseBootstrapCID,
        relayAddresses: [descriptorRelay],
      },
      requesterIdentity: {
        peerId: "requester-peer-id",
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: "com.orbpro.license",
      requesterDomain: "app.example.com",
      requestedTimeoutMs: 300_000,
    });

    expect(transport.discoveryCalls).toEqual([licenseBootstrapCID]);
    expect(transport.dialCalls).toHaveLength(2);
    expect(transport.dialCalls[0]?.candidateAddrs).toEqual([discoveredRelay]);
    expect(transport.dialCalls[1]?.candidateAddrs).toEqual([discoveredRelay]);
  });

  it("rejects a grant that exceeds the requested timeout", async () => {
    const transport = {
      async dialProtocol(
        _targetPeerId: string,
        _protocolId: string,
        payload: Uint8Array,
      ) {
        if (isLCH(payload)) {
          const request = decodeLCH(payload);
          return encodeChallengeResponse({
            reqId: request.REQUEST_ID() ?? "",
            moduleId: request.MODULE_ID() ?? "",
            moduleVersion: request.MODULE_VERSION() ?? undefined,
            providerPeerId: "provider-peer-id",
            challengeNonce: new Uint8Array([1, 2, 3, 4]),
            expiresAtMs: 1_700_000_900_000n,
          });
        }

        const proof = decodeLPF(payload);
        return encodeGrantResponse({
          reqId: proof.REQUEST_ID() ?? "",
          moduleId: proof.MODULE_ID() ?? "",
          moduleVersion: proof.MODULE_VERSION() ?? undefined,
          requesterPeerId: proof.REQUESTER_PEER_ID() ?? undefined,
          requesterXpub: proof.REQUESTER_XPUB() ?? undefined,
          requestedDomain: proof.REQUESTED_DOMAIN() ?? "",
          requestedTimeoutMs: proof.REQUESTED_TIMEOUT_MS(),
          grantedDomain: "app.example.com",
          grantedTimeoutMs: proof.REQUESTED_TIMEOUT_MS() + 1n,
          expiresAtMs: 1_700_003_600_000n,
          contentHash: new Uint8Array(32).fill(7),
        });
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
    };

    await expect(
      requestModuleGrant(transport, {
        serverDescriptor: {
          publicKey: "02".padEnd(66, "1"),
          relayAddresses: ["/dns4/relay.example/tcp/443/wss/p2p/relay-peer"],
        },
        requesterIdentity: {
          peerId: "requester-peer-id",
          xpub: "xpub-requester",
          signingKey: {
            privateKey: new Uint8Array(32).fill(5),
            publicKey: new Uint8Array(32).fill(6),
          },
          encryptionKey: {
            privateKey: new Uint8Array(32).fill(7),
            publicKey: new Uint8Array(32).fill(8),
          },
        },
        moduleId: "com.space-data-network.fastest-path",
        moduleVersion: "0.5.22",
        requesterDomain: "app.example.com",
        requestedTimeoutMs: 30_000,
        reqId: "req-timeout-overgrant",
        requestedAtMs: 1_700_000_000_000,
      }),
    ).rejects.toMatchObject({
      code: "grant_policy_mismatch",
    });
  });

  it("keeps the relay probe example on the real module-delivery exchange path", async () => {
    const exampleSource = await fs.readFile(
      path.join(__dirname, "..", "examples", "ipfs-relay-id-exchange.ts"),
      "utf8",
    );

    expect(exampleSource.includes("requestModuleGrant(")).toBe(true);
    expect(exampleSource.includes("ModuleDeliveryProtocolError")).toBe(true);
    expect(exampleSource.includes("SDN_MODULE_ID")).toBe(true);
  });
});

function isLCH(payload: Uint8Array): boolean {
  return LCH.bufferHasIdentifier(new flatbuffers.ByteBuffer(payload));
}

function isLPF(payload: Uint8Array): boolean {
  return LPF.bufferHasIdentifier(new flatbuffers.ByteBuffer(payload));
}

function decodeLCH(payload: Uint8Array): LCH {
  return LCH.getRootAsLCH(new flatbuffers.ByteBuffer(payload));
}

function decodeLPF(payload: Uint8Array): LPF {
  return LPF.getRootAsLPF(new flatbuffers.ByteBuffer(payload));
}

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
  const moduleVersionOffset = options.moduleVersion
    ? builder.createString(options.moduleVersion)
    : 0;
  const providerPeerIdOffset = builder.createString(options.providerPeerId);
  const challengeNonceOffset = LCH.createChallengeNonceVector(
    builder,
    options.challengeNonce,
  );
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
  const moduleVersionOffset = options.moduleVersion
    ? builder.createString(options.moduleVersion)
    : 0;
  const requesterPeerIdOffset = options.requesterPeerId
    ? builder.createString(options.requesterPeerId)
    : 0;
  const requesterXpubOffset = options.requesterXpub
    ? builder.createString(options.requesterXpub)
    : 0;
  const requestedDomainOffset = builder.createString(options.requestedDomain);
  const grantedDomainOffset = builder.createString(options.grantedDomain);
  const requiredScopeOffset = builder.createString("orbpro.default");
  const grantStatusOffset = builder.createString("active");
  const capabilityTokenOffset = LGR.createCapabilityTokenVector(
    builder,
    new Uint8Array([1, 2, 3]),
  );
  const moduleDescriptorOffset = createModuleDescriptorOffset(
    builder,
    options.contentHash,
  );
  const wrappedContentKeyHeaderOffset = createWrappedContentKeyHeaderOffset(
    builder,
    options,
  );
  const wrappedContentKeyPayloadOffset = createWrappedContentKeyPayloadOffset(
    builder,
    options,
  );
  const verifierPubkeyOffset = LGR.createGrantVerifierPubkeyVector(
    builder,
    new Uint8Array(32).fill(5),
  );
  const providerSignatureOffset = LGR.createProviderSignatureVector(
    builder,
    new Uint8Array([9, 9, 9]),
  );

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
  LGR.addGrantVerifierPubkey(builder, verifierPubkeyOffset);
  LGR.addProviderSignature(builder, providerSignatureOffset);
  const root = LGR.endLGR(builder);
  LGR.finishLGRBuffer(builder, root);
  return builder.asUint8Array();
}

function createModuleDescriptorOffset(
  builder: flatbuffers.Builder,
  contentHash: Uint8Array,
): flatbuffers.Offset {
  const moduleId = "com.space-data-network.fastest-path";
  const moduleVersion = "0.5.22";
  const pluginIdOffset = builder.createString(moduleId);
  const nameOffset = builder.createString(moduleId);
  const versionOffset = builder.createString(moduleVersion);
  const descriptionOffset = builder.createString("Protected module fixture");
  const wasmHashOffset = PLG.createWasmHashVector(builder, contentHash);
  const wasmCidOffset = builder.createString("bafyencryptedmodule");
  const requiredScopeOffset = builder.createString("orbpro.default");
  const keyIdOffset = builder.createString(`${moduleId}:${moduleVersion}`);
  const allowedDomainsOffset = PLG.createAllowedDomainsVector(builder, [
    builder.createString("app.example.com"),
  ]);

  PLG.startPLG(builder);
  PLG.addPluginId(builder, pluginIdOffset);
  PLG.addName(builder, nameOffset);
  PLG.addVersion(builder, versionOffset);
  PLG.addDescription(builder, descriptionOffset);
  PLG.addPluginType(builder, pluginType.Analysis);
  PLG.addAbiVersion(builder, 1);
  PLG.addWasmHash(builder, wasmHashOffset);
  PLG.addWasmSize(builder, 4n);
  PLG.addWasmCid(builder, wasmCidOffset);
  PLG.addEncrypted(builder, true);
  PLG.addRequiredScope(builder, requiredScopeOffset);
  PLG.addKeyId(builder, keyIdOffset);
  PLG.addAllowedDomains(builder, allowedDomainsOffset);
  PLG.addMaxGrantTimeoutMs(builder, 300_000n);
  return PLG.endPLG(builder);
}

function createWrappedContentKeyHeaderOffset(
  builder: flatbuffers.Builder,
  options: {
    reqId: string;
    moduleId: string;
    moduleVersion?: string;
    expiresAtMs: bigint;
  },
): flatbuffers.Offset {
  const ephemeralPublicKeyOffset = ENC.createEphemeralPublicKeyVector(
    builder,
    new Uint8Array(32).fill(9),
  );
  const nonceStartOffset = ENC.createNonceStartVector(
    builder,
    new Uint8Array(12).fill(4),
  );
  const recipientKeyIdOffset = ENC.createRecipientKeyIdVector(
    builder,
    new TextEncoder().encode("requester-encryption-key"),
  );
  const contextOffset = builder.createString(
    `license-grant:${options.moduleId}:${options.moduleVersion ?? "latest"}`,
  );
  const rootTypeOffset = builder.createString("$KMF");

  return ENC.createENC(
    builder,
    1,
    KeyExchange.X25519,
    SymmetricAlgo.AES_256_CTR,
    KDF.HKDF_SHA256,
    ephemeralPublicKeyOffset,
    nonceStartOffset,
    recipientKeyIdOffset,
    contextOffset,
    0,
    rootTypeOffset,
    options.expiresAtMs,
  );
}

function createWrappedContentKeyPayloadOffset(
  builder: flatbuffers.Builder,
  options: { moduleId: string; moduleVersion?: string; expiresAtMs: bigint },
): flatbuffers.Offset {
  const kmfBuilder = new flatbuffers.Builder(256);
  const keyIdOffset = kmfBuilder.createString(
    `${options.moduleId}:${options.moduleVersion ?? "latest"}`,
  );
  const keyBytesOffset = KMF.createKeyBytesVector(
    kmfBuilder,
    new Uint8Array([4, 5, 6]),
  );
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
  return LGR.createWrappedContentKeyPayloadVector(
    builder,
    kmfBuilder.asUint8Array(),
  );
}

function hexToBytes(hex: string): Uint8Array {
  const normalized = hex.trim().toLowerCase();
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(
      normalized.slice(index * 2, index * 2 + 2),
      16,
    );
  }
  return bytes;
}

async function sha256(value: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(createHash("sha256").update(value).digest());
}
