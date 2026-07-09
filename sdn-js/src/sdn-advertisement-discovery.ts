/**
 * SDN advertisement-flag DHT rendezvous discovery (alignment loop A3).
 *
 * sdn-server now joins the PUBLIC IPFS/Amino DHT with the stock
 * `/ipfs/kad/1.0.0` protocol (no `protocolPrefix` override — see node.ts,
 * which likewise runs `kadDHT({ clientMode: true })` with no prefix) and
 * advertises SDN membership as a rendezvous "flag" using go-libp2p's
 * routing-discovery content-routing convention: a namespace string is
 * hashed into a CID, and peers `Provide`/`FindProviders` that CID.
 *
 * This module derives the IDENTICAL CID from the IDENTICAL namespace string
 * so a browser node and a Go daemon rendezvous at the same DHT record.
 *
 * --- Go derivation, verified against the source actually vendored for this
 * repo (not from memory) ---
 *
 * Namespace (sdn-server/internal/node/advertisement_discovery.go:22,55-59):
 *   const sdnAdvertisementDiscoveryNamespace =
 *     "space-data-network/discovery/advertisement-flag"
 *   target.Namespace = fmt.Sprintf("%s/%s", sdnAdvertisementDiscoveryNamespace, flag)
 *   // flag is versioninfo.CurrentAdvertisementFlag ("spacedatanetwork/1.0.0" as of
 *   // suite.versions.json today) or one of versioninfo.SupportedAdvertisementFlags —
 *   // the same generated constants sdn-js already ships in version-info.generated.ts.
 *
 * CID derivation (go.mod pins github.com/libp2p/go-libp2p v0.46.0; verified
 * at $GOPATH/pkg/mod/github.com/libp2p/go-libp2p@v0.46.0/p2p/discovery/routing/routing.go:75-82):
 *   func nsToCid(ns string) (cid.Cid, error) {
 *     h, err := mh.Sum([]byte(ns), mh.SHA2_256, -1)   // sha2-256 multihash, default (32-byte) digest
 *     return cid.NewCidV1(cid.Raw, h), nil             // CIDv1, raw codec (multicodec 0x55)
 *   }
 * Both `drouting.NewRoutingDiscovery(dht).FindPeers(ctx, ns)` (peer discovery,
 * advertisement_discovery.go:477-478) and `dutil.Advertise(ctx, routingDiscovery,
 * ns)` (self-announce, node.go:2661-2662) route through this same `nsToCid`.
 *
 * So: CIDv1 { codec: raw (0x55), multihash: sha2-256(utf8(namespace)) }.
 */

import { CID } from "multiformats/cid";
import { sha256 } from "multiformats/hashes/sha2";
import * as raw from "multiformats/codecs/raw";
import type { PeerInfo, PeerId } from "@libp2p/interface";

import {
  CURRENT_ADVERTISEMENT_FLAG,
  SUPPORTED_ADVERTISEMENT_FLAGS,
} from "./version-info.generated";

/**
 * Base rendezvous namespace — mirrors Go's unexported
 * `sdnAdvertisementDiscoveryNamespace` constant exactly.
 */
export const SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE =
  "space-data-network/discovery/advertisement-flag";

export interface SDNAdvertisementDiscoveryTarget {
  flag: string;
  namespace: string;
}

/**
 * Build the full per-flag rendezvous namespace:
 * `${SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE}/${flag}` — mirrors Go's
 * `target.Namespace = fmt.Sprintf("%s/%s", sdnAdvertisementDiscoveryNamespace, flag)`.
 */
export function sdnAdvertisementNamespace(flag: string): string {
  const trimmed = flag.trim();
  if (!trimmed) {
    throw new Error("advertisement flag is required");
  }
  return `${SDN_ADVERTISEMENT_DISCOVERY_NAMESPACE}/${trimmed}`;
}

/**
 * Ordered, deduplicated discovery targets: current flag first, then any
 * additional supported flags — mirrors Go's `sdnAdvertisementDiscoveryTargets`
 * (advertisement_discovery.go:29-63).
 */
export function sdnAdvertisementDiscoveryTargets(
  currentFlag: string = CURRENT_ADVERTISEMENT_FLAG,
  supportedFlags: readonly string[] = SUPPORTED_ADVERTISEMENT_FLAGS,
): {
  current: SDNAdvertisementDiscoveryTarget;
  targets: SDNAdvertisementDiscoveryTarget[];
} {
  const trimmedCurrent = currentFlag.trim();
  if (!trimmedCurrent) {
    throw new Error("current advertisement flag is required");
  }

  const seen = new Set<string>();
  const orderedFlags: string[] = [];
  const appendFlag = (flag: string) => {
    const value = flag.trim();
    if (!value || seen.has(value)) {
      return;
    }
    seen.add(value);
    orderedFlags.push(value);
  };

  appendFlag(trimmedCurrent);
  for (const flag of supportedFlags) {
    appendFlag(flag);
  }

  const targets = orderedFlags.map((flag) => ({
    flag,
    namespace: sdnAdvertisementNamespace(flag),
  }));

  return { current: targets[0], targets };
}

/**
 * Derive the DHT rendezvous CID for a raw namespace string exactly as
 * go-libp2p's routing-discovery `nsToCid` does: CIDv1, raw codec, sha2-256
 * multihash of the UTF-8 namespace bytes. See module doc comment above for
 * the exact go-libp2p source verified against.
 */
export async function advertisementNamespaceToCid(
  namespace: string,
): Promise<CID> {
  const bytes = new TextEncoder().encode(namespace);
  const digest = await sha256.digest(bytes);
  return CID.createV1(raw.code, digest);
}

/** Minimal content-routing surface this module needs (matches libp2p's `ContentRouting`). */
export interface SDNAdvertisementContentRouting {
  findProviders(
    cid: CID,
    options?: { signal?: AbortSignal },
  ): AsyncIterable<PeerInfo>;
  provide(cid: CID, options?: { signal?: AbortSignal }): Promise<void>;
}

/**
 * Minimal libp2p node surface this module needs. The real `Libp2p` instance
 * (src/node.ts) satisfies this directly — `contentRouting` is required;
 * `dial`/`getConnections`/`peerId` are used opportunistically when present.
 */
export interface SDNAdvertisementDiscoveryNode {
  peerId?: { toString(): string };
  contentRouting: SDNAdvertisementContentRouting;
  dial?(peer: PeerId): Promise<unknown>;
  getConnections?(peerId?: PeerId): unknown[];
}

export interface DiscoveredSDNAdvertisementPeer {
  peerId: string;
  multiaddrs: string[];
  flag: string;
}

export interface FindSDNAdvertisementPeersOptions {
  /** libp2p node with a content-routing capable service (e.g. kadDHT). */
  libp2p: SDNAdvertisementDiscoveryNode;
  /** Flags to search for; defaults to [current, ...supported] suite flags. */
  flags?: readonly string[];
  /** Forwarded to contentRouting.findProviders per flag lookup. */
  signal?: AbortSignal;
  /**
   * Auto-dial newly discovered peers not already connected (default true) —
   * the browser-side counterpart of Go's connect-on-discovery behaviour in
   * `discoverSDNAdvertisementPeersNow` (advertisement_discovery.go:476-506).
   * Dial failures are non-fatal (peer may be unreachable/NATed); discovery
   * continues.
   */
  autoDial?: boolean;
}

/**
 * Search the public DHT for peers advertising the SDN membership flag(s),
 * and (by default) dial any that aren't already connected. Wires discovered
 * peers into the same libp2p connection path used elsewhere in sdn-js
 * (`SDNNode.dial` / `libp2p.dial`), rather than a bespoke transport.
 */
export async function findSDNAdvertisementPeers(
  options: FindSDNAdvertisementPeersOptions,
): Promise<DiscoveredSDNAdvertisementPeer[]> {
  const { libp2p, signal, autoDial = true } = options;
  const flags =
    options.flags && options.flags.length > 0
      ? options.flags
      : [CURRENT_ADVERTISEMENT_FLAG, ...SUPPORTED_ADVERTISEMENT_FLAGS];
  const { targets } = sdnAdvertisementDiscoveryTargets(
    flags[0],
    flags.slice(1),
  );

  const selfPeerId = libp2p.peerId?.toString();
  const seen = new Set<string>();
  const discovered: DiscoveredSDNAdvertisementPeer[] = [];

  for (const target of targets) {
    const cid = await advertisementNamespaceToCid(target.namespace);

    let providers: AsyncIterable<PeerInfo>;
    try {
      providers = libp2p.contentRouting.findProviders(cid, { signal });
    } catch {
      continue;
    }

    try {
      for await (const provider of providers) {
        const peerId = provider.id.toString();
        if (!peerId || peerId === selfPeerId || seen.has(peerId)) {
          continue;
        }
        seen.add(peerId);
        const multiaddrs = provider.multiaddrs.map((addr) => addr.toString());
        discovered.push({ peerId, multiaddrs, flag: target.flag });

        if (autoDial && typeof libp2p.dial === "function") {
          const alreadyConnected =
            typeof libp2p.getConnections === "function" &&
            libp2p.getConnections(provider.id).length > 0;
          if (!alreadyConnected) {
            try {
              await libp2p.dial(provider.id);
            } catch {
              // Non-fatal: unreachable/NATed peers are expected; keep discovering.
            }
          }
        }
      }
    } catch {
      // Non-fatal: continue with the remaining flags on lookup failure/timeout.
    }
  }

  return discovered;
}

export interface ProvideSDNAdvertisementFlagOptions {
  /** libp2p node with a content-routing capable service (e.g. kadDHT). */
  libp2p: Pick<SDNAdvertisementDiscoveryNode, "contentRouting">;
  /** Flag to announce; defaults to the current suite advertisement flag. */
  flag?: string;
  signal?: AbortSignal;
}

/**
 * Announce this browser node as an SDN member on the public DHT — the
 * browser-side counterpart of Go's `announceSDNAdvertisement` (node.go:2653-
 * 2664, which wraps `dutil.Advertise`, itself routed through the same
 * `nsToCid`/`Provide` path as `RoutingDiscovery.Advertise`).
 *
 * Optional by design: most browser nodes are content *consumers* and should
 * not call this. Call it only when the browser node should itself be
 * discoverable as an SDN peer. This performs a single provide(); callers
 * that want the Go daemon's periodic re-announce behaviour (node.go:2605-
 * 2619, every 30s) should re-invoke this on their own interval — DHT
 * provider records expire (~24h upstream, republished at ~3h intervals by
 * convention) so a long-lived browser node should re-provide periodically.
 */
export async function provideSDNAdvertisementFlag(
  options: ProvideSDNAdvertisementFlagOptions,
): Promise<void> {
  const flag = options.flag ?? CURRENT_ADVERTISEMENT_FLAG;
  const namespace = sdnAdvertisementNamespace(flag);
  const cid = await advertisementNamespaceToCid(namespace);
  await options.libp2p.contentRouting.provide(cid, { signal: options.signal });
}
