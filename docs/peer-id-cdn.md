# Peer-ID URLs and the public cache

Every detected SDN node has the address `<peer-label>.spacedatanetwork.org`
(owner 2026-10-05). The label is the
full IPFS/libp2p peer identity encoded as a lowercase base36 CIDv1 with the
`libp2p-key` codec. It is lossless, not an alias, a shortened ID or a hash of the
display name. This encoding fits supported peer IDs in a DNS label and avoids
DNS case folding changing a base58 identity.

The publisher label identifies **who** publishes. The content CID in
`/ipfs/<cid>` identifies **which immutable bytes** to fetch. Verify the CID and
the signed publication that names it; DNS and HTTPS do not replace those checks.
Updating a dataset produces a new publication and content CID.

## The node's homepage

The address serves the node's homepage at `/`, and the node's public API
answers behind it, for example `/api/v1/id`. Every other address the node
answers on (`127.0.0.1`, the operator's own domain) keeps the dashboard at
`/`, and `/home/` shows the homepage on any address.

The homepage always carries the node's EPM card: its name, contact rows and
scannable code, read from `/api/node/epm/vcard` and `/api/node/epm/qr`. The
operator adds headings, text and links in the dashboard (THIS NODE ›
Homepage), which the node keeps at `/api/v1/homepage`. A document can move
the card but never remove it. Locked nodes serve these reads to anyone;
writes stay an admin's.

## Working public example

This CelesTrak CAT snapshot is replicated on a public origin:

```sh
curl --fail --location \
  'https://kzwfwjn5ji4pupxkysaraxurxv9mo7tb99iu3r2gna6hexozftkooiymx1qly67.spacedatanetwork.org/ipfs/bafybeiaa6fk4gi775ou2l366jgo3jxjl7ib33urp556lownphi4lyblrnu' \
  --output catalog.bin
```

Source SDN peer: `16Uiu2HAmCL9enDzrbxJS8xKjFXVYVogjtbaJw2KQc45EYE6KkzRL`.
The public replica serves only admitted artifacts. Follow the redirect to
`/artifacts/<cid>.bin`, the cacheable binary route. A request to the hostname's
root may return 404: the replica is not the publisher's node.

The example is separate from whichever node or dataset is open in the UI.
New study publisher labels are not public routes until their origins and DNS
records have actually been enabled. Current API-client testing also identified
a Cloudflare Browser Integrity Check rejection for Python's default user agent;
the scoped exception remains a deployment gate. Do not silently treat a 403 as
an empty dataset.

## Routing and discovery

Native SDN discovery still uses libp2p/IPFS and DHT provider advertisements.
A public CDN route is an additional HTTP delivery path. Discovery does not
create a reachable origin, authorize redistribution, or prove that a hostname
has been configured. Use actual advertised multiaddresses to dial libp2p;
an HTTPS artifact hostname does not imply a WebSocket listener.

Cloudflare proxies an explicitly configured public origin or public replica.
It needs ordinary DNS and caching, without Workers or Tunnel. A private LAN
address cannot be used as a directly reachable origin. Serve confidential or
customer-encrypted material through the existing protected delivery contract,
outside this public artifact allowlist.

Paginated queries and FTS use `/api/v1/data/query` on the node, returning binary
FlatBuffer streams. Native dataset synchronization and subscriptions use SDN's
p2p protocols. Do not move POST queries or authenticated APIs to the public
artifact hostname or cache their responses under a shared public policy.

The Cloudflare registrar module registers every detected node that passes
its checks (owner 2026-10-05): no operator opts in, and only an explicit
exclusion removes a route. Until its signed release is installed, routing is
the explicit deployment configuration recorded in the stack's orbital-data
checkpoint.

References: [IPFS addressing](https://docs.ipfs.tech/how-to/address-ipfs-on-web/),
[Cloudflare default caching](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/).
