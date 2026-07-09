import { describe, expect, it, vi } from "vitest";
import type { CID } from "multiformats/cid";
import type { PeerId, PeerInfo } from "@libp2p/interface";

import {
  CURRENT_ADVERTISEMENT_FLAG,
  SUPPORTED_ADVERTISEMENT_FLAGS,
} from "./version-info.generated";
import {
  SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE,
  advertisementNamespaceToCid,
  findSDNAdvertisementPeers,
  provideSDNAdvertisementFlag,
  sdnAdvertisementDiscoveryTargets,
  sdnAdvertisementNamespace,
  type SDNAdvertisementDiscoveryNode,
} from "./sdn-advertisement-discovery";

// --- Golden CID -------------------------------------------------------
//
// Two independent derivations of the same value, matching go-libp2p's
// nsToCid (p2p/discovery/routing/routing.go:75-82, go-libp2p v0.46.0 — the
// version pinned by sdn-server/go.mod, verified by reading the vendored
// module source, not from memory):
//
//   h := mh.Sum([]byte(ns), mh.SHA2_256, -1)   // sha2-256 multihash, 32-byte digest
//   cid.NewCidV1(cid.Raw, h)                    // CIDv1, raw codec (multicodec 0x55)
//
// 1) `computeGoldenCidManually` below re-implements the CIDv1/raw/sha2-256
//    byte layout (version varint, codec varint, multihash code+len+digest,
//    base32 multibase) by hand, independent of the `multiformats` library
//    that `advertisementNamespaceToCid` itself uses.
// 2) `advertisementNamespaceToCid` (the function under test) uses the
//    `multiformats` library.
// Both must agree with the hardcoded golden string, which was cross-checked
// against a throwaway Node script using `multiformats` directly.
const GOLDEN_BASE_NAMESPACE_CID =
  "bafkreig6whpgcxvdobj3qfcf53jxkjz5g5rhhwx7or7tc6uyel2xycdzce";
// Full per-flag namespace actually used at runtime by both sides:
// `${SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE}/${CURRENT_ADVERTISEMENT_FLAG}`
// (advertisement_discovery.go:57-59; CURRENT_ADVERTISEMENT_FLAG is generated
// from suite.versions.json into both versioninfo.generated.go and
// version-info.generated.ts, so it's already identical on both sides).
const GOLDEN_CURRENT_FLAG_NAMESPACE_CID =
  "bafkreiafjhthax5wlunbjg7uy6v2qzbfaehf5vyxozep46wyemkree5wcu";

describe("sdn-advertisement-discovery: rendezvous CID derivation", () => {
  it("matches the golden CID for the base namespace (multiformats-derived)", async () => {
    const cid = await advertisementNamespaceToCid(
      SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE,
    );
    expect(cid.toString()).toBe(GOLDEN_BASE_NAMESPACE_CID);
    expect(cid.code).toBe(0x55); // raw codec
    expect(cid.version).toBe(1);
    expect(cid.multihash.code).toBe(0x12); // sha2-256
    expect(cid.multihash.digest.length).toBe(32);
  });

  it("matches the golden CID via an independent hand-rolled CIDv1/raw/sha2-256 encoder", async () => {
    const golden = await computeGoldenCidManually(
      SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE,
    );
    expect(golden).toBe(GOLDEN_BASE_NAMESPACE_CID);

    const cid = await advertisementNamespaceToCid(
      SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE,
    );
    expect(cid.toString()).toBe(golden);
  });

  it("matches the golden CID for the full current-flag namespace (real runtime rendezvous point)", async () => {
    const namespace = sdnAdvertisementNamespace(CURRENT_ADVERTISEMENT_FLAG);
    expect(namespace).toBe(
      `${SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE}/${CURRENT_ADVERTISEMENT_FLAG}`,
    );

    const cid = await advertisementNamespaceToCid(namespace);
    expect(cid.toString()).toBe(GOLDEN_CURRENT_FLAG_NAMESPACE_CID);

    const golden = await computeGoldenCidManually(namespace);
    expect(golden).toBe(GOLDEN_CURRENT_FLAG_NAMESPACE_CID);
  });

  it("rejects an empty flag", () => {
    expect(() => sdnAdvertisementNamespace("  ")).toThrow(/flag is required/i);
  });
});

describe("sdn-advertisement-discovery: target ordering", () => {
  it("puts the current flag first and dedups against supported flags", () => {
    const { current, targets } = sdnAdvertisementDiscoveryTargets("a", [
      "b",
      "a",
      "c",
    ]);
    expect(current).toEqual({
      flag: "a",
      namespace: `${SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE}/a`,
    });
    expect(targets.map((t) => t.flag)).toEqual(["a", "b", "c"]);
  });

  it("defaults to the generated suite advertisement flags", () => {
    const { current, targets } = sdnAdvertisementDiscoveryTargets();
    expect(current.flag).toBe(CURRENT_ADVERTISEMENT_FLAG);
    for (const flag of SUPPORTED_ADVERTISEMENT_FLAGS) {
      expect(targets.some((t) => t.flag === flag)).toBe(true);
    }
  });

  it("throws when the current flag is empty", () => {
    expect(() => sdnAdvertisementDiscoveryTargets("  ", [])).toThrow(
      /current advertisement flag is required/i,
    );
  });
});

// --- Discovery unit (mocked contentRouting) ----------------------------
//
// A live two-node js-libp2p + kadDHT loopback swarm was judged too heavy/
// flaky for this unit suite (needs TCP/mplex transports, DHT bootstrap
// convergence, and real timing that isn't installed as a sdn-js browser
// dependency today). Per the task's fallback, this delivers: the golden-CID
// test above, plus this mocked-contentRouting test of the discovery unit
// itself (findSDNAdvertisementPeers / provideSDNAdvertisementFlag). Full
// browser<->Go-daemon interop over the public DHT is listed as a follow-up.

function makePeerId(value: string): PeerId {
  return { toString: () => value } as unknown as PeerId;
}

function makePeerInfo(peerId: string, multiaddrs: string[] = []): PeerInfo {
  return {
    id: makePeerId(peerId),
    multiaddrs: multiaddrs.map((addr) => ({
      toString: () => addr,
    })) as unknown as PeerInfo["multiaddrs"],
  };
}

async function* asyncIterableOf<T>(items: T[]): AsyncIterable<T> {
  for (const item of items) {
    yield item;
  }
}

describe("findSDNAdvertisementPeers", () => {
  it("discovers a peer providing the flag CID, skips self, dials the new peer", async () => {
    const seenCids: string[] = [];
    const dialed: string[] = [];
    const findProviders = vi.fn((cid: CID) => {
      seenCids.push(cid.toString());
      return asyncIterableOf<PeerInfo>([
        makePeerInfo("self-peer"),
        makePeerInfo("go-daemon-peer", ["/ip4/127.0.0.1/tcp/4001"]),
      ]);
    });
    const dial = vi.fn(async (peer: PeerId) => {
      dialed.push(peer.toString());
    });
    const getConnections = vi.fn(() => []);

    const libp2p: SDNAdvertisementDiscoveryNode = {
      peerId: makePeerId("self-peer"),
      contentRouting: { findProviders, provide: vi.fn() },
      dial,
      getConnections,
    };

    const discovered = await findSDNAdvertisementPeers({
      libp2p,
      flags: [CURRENT_ADVERTISEMENT_FLAG],
    });

    expect(discovered).toEqual([
      {
        peerId: "go-daemon-peer",
        multiaddrs: ["/ip4/127.0.0.1/tcp/4001"],
        flag: CURRENT_ADVERTISEMENT_FLAG,
      },
    ]);
    expect(dialed).toEqual(["go-daemon-peer"]);

    // The CID searched must be the exact rendezvous point the Go daemon
    // provides at for this flag.
    const expectedCid = await advertisementNamespaceToCid(
      sdnAdvertisementNamespace(CURRENT_ADVERTISEMENT_FLAG),
    );
    expect(seenCids).toEqual([expectedCid.toString()]);
  });

  it("does not redial peers already connected", async () => {
    const dial = vi.fn();
    const libp2p: SDNAdvertisementDiscoveryNode = {
      peerId: makePeerId("self-peer"),
      contentRouting: {
        findProviders: () =>
          asyncIterableOf<PeerInfo>([makePeerInfo("already-connected-peer")]),
        provide: vi.fn(),
      },
      dial,
      getConnections: vi.fn(() => [{}]), // non-empty => already connected
    };

    const discovered = await findSDNAdvertisementPeers({
      libp2p,
      flags: [CURRENT_ADVERTISEMENT_FLAG],
    });

    expect(discovered).toHaveLength(1);
    expect(dial).not.toHaveBeenCalled();
  });

  it("dedups a peer discovered under multiple flags and never dials twice", async () => {
    const dial = vi.fn();
    const libp2p: SDNAdvertisementDiscoveryNode = {
      contentRouting: {
        findProviders: () =>
          asyncIterableOf<PeerInfo>([makePeerInfo("dual-flag-peer")]),
        provide: vi.fn(),
      },
      dial,
      getConnections: vi.fn(() => []),
    };

    const discovered = await findSDNAdvertisementPeers({
      libp2p,
      flags: ["flag-a", "flag-b"],
    });

    expect(discovered).toHaveLength(1);
    expect(discovered[0].flag).toBe("flag-a");
    expect(dial).toHaveBeenCalledTimes(1);
  });

  it("does not auto-dial when autoDial is false", async () => {
    const dial = vi.fn();
    const libp2p: SDNAdvertisementDiscoveryNode = {
      contentRouting: {
        findProviders: () =>
          asyncIterableOf<PeerInfo>([makePeerInfo("peer-x")]),
        provide: vi.fn(),
      },
      dial,
      getConnections: vi.fn(() => []),
    };

    await findSDNAdvertisementPeers({
      libp2p,
      flags: [CURRENT_ADVERTISEMENT_FLAG],
      autoDial: false,
    });

    expect(dial).not.toHaveBeenCalled();
  });

  it("continues to the next flag when a lookup throws", async () => {
    let findProvidersCallCount = 0;
    const libp2p: SDNAdvertisementDiscoveryNode = {
      contentRouting: {
        findProviders: vi.fn((_cid: CID) => {
          if (findProvidersCallCount++ === 0) {
            throw new Error("dht lookup failed");
          }
          return asyncIterableOf<PeerInfo>([makePeerInfo("recovered-peer")]);
        }),
        provide: vi.fn(),
      },
      dial: vi.fn(),
      getConnections: vi.fn(() => []),
    };

    const discovered = await findSDNAdvertisementPeers({
      libp2p,
      flags: ["flag-a", "flag-b"],
    });

    expect(discovered.map((p) => p.peerId)).toEqual(["recovered-peer"]);
  });
});

describe("provideSDNAdvertisementFlag", () => {
  it("provides the CID for the current flag's full namespace by default", async () => {
    const provide = vi.fn(async (_cid: CID, _options?: { signal?: AbortSignal }) => undefined);
    await provideSDNAdvertisementFlag({
      libp2p: { contentRouting: { findProviders: vi.fn(), provide } },
    });

    const expectedCid = await advertisementNamespaceToCid(
      sdnAdvertisementNamespace(CURRENT_ADVERTISEMENT_FLAG),
    );
    expect(provide).toHaveBeenCalledTimes(1);
    const [calledCid] = provide.mock.calls[0];
    expect(calledCid.toString()).toBe(expectedCid.toString());
  });

  it("provides the CID for an explicitly requested flag", async () => {
    const provide = vi.fn(async (_cid: CID, _options?: { signal?: AbortSignal }) => undefined);
    await provideSDNAdvertisementFlag({
      libp2p: { contentRouting: { findProviders: vi.fn(), provide } },
      flag: "custom-flag",
    });

    const expectedCid = await advertisementNamespaceToCid(
      sdnAdvertisementNamespace("custom-flag"),
    );
    const [calledCid] = provide.mock.calls[0];
    expect(calledCid.toString()).toBe(expectedCid.toString());
  });
});

/**
 * Independent (no `multiformats` library) re-implementation of go-libp2p's
 * `nsToCid` byte layout, used only to cross-check the golden CID values
 * above: CIDv1 = varint(version=1) + varint(codec=raw=0x55) +
 * multihash(varint(code=sha2-256=0x12) + varint(len=32) + digest), base32
 * (RFC4648, lowercase, no padding) multibase-prefixed with 'b'.
 */
async function computeGoldenCidManually(namespace: string): Promise<string> {
  const nsBytes = new TextEncoder().encode(namespace);
  const digestBuffer = await crypto.subtle.digest("SHA-256", nsBytes);
  const digest = new Uint8Array(digestBuffer);

  const cidBytes = new Uint8Array(4 + digest.length);
  cidBytes[0] = 0x01; // CID version 1
  cidBytes[1] = 0x55; // raw codec
  cidBytes[2] = 0x12; // sha2-256 multihash code
  cidBytes[3] = 0x20; // 32-byte digest length
  cidBytes.set(digest, 4);

  return `b${base32Encode(cidBytes)}`;
}

const BASE32_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";

function base32Encode(value: Uint8Array): string {
  let output = "";
  let bits = 0;
  let current = 0;

  for (const byte of value) {
    current = (current << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      output += BASE32_ALPHABET[(current >>> (bits - 5)) & 0x1f];
      bits -= 5;
    }
  }

  if (bits > 0) {
    output += BASE32_ALPHABET[(current << (5 - bits)) & 0x1f];
  }

  return output;
}
