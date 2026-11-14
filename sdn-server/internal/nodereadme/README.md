# Space Data Network

The Space Data Network (SDN) is a peer-to-peer network for sharing space data:
orbits, catalogs, conjunctions, space weather, sensor and link data, and the
identities of the people and machines that publish them. There is no central
server. Every participant runs a node, every record travels in one standard
binary format, and every publication is signed by the node that made it.

Every SDN node pins this document. Its bytes are the same on every node, so it
has the same content address (CID) everywhere, and any node can serve it.

## Nodes

A node is the `spacedatanetwork` program running on a computer. It stores
records, serves them to other nodes and to browsers, and runs modules (small
WebAssembly programs) that fetch, convert, and analyze data.

- **Full nodes** run on servers with public addresses. They keep data, answer
  queries, pin content, and relay traffic for peers that cannot be reached
  directly.
- **Light peers** run behind home or office networks, and in a web browser. A
  browser running the SDN library is a peer, not just a client: it fetches
  records, checks their signatures, and keeps its own copy.

Each node has a dashboard at `/` on its own address: what it holds, which nodes
it knows, what it offers, and its settings.

## Peers

Nodes find and talk to each other with [libp2p](https://libp2p.io), the
networking layer of [IPFS](https://ipfs.tech).

- A new node starts from a few known bootstrap nodes, then discovers more
  through the distributed hash table (DHT).
- Nodes behind a router reach each other through relays and hole punching.
- Publish/subscribe topics carry live data: one topic per data standard, so a
  node subscribes only to the kinds of records it wants.
- Larger datasets move over a direct sync protocol between two nodes, at close
  to the speed of the link.

## Space Data Standards, as FlatBuffers

Every record follows one of the [Space Data Standards](https://spacedatastandards.org):
open schemas for space data, each named by a short code such as `OMM` (orbit
mean elements), `CAT` (catalog entry), `CDM` (conjunction data message) or
`EPM` (entity profile, the identity card of a person or organization).

Records are [FlatBuffers](https://flatbuffers.dev): compact binary messages
that are read in place, without parsing into another form. The same bytes
travel the network, sit on disk, answer queries, and arrive in the browser.
A node checks every record against its standard before storing it.

Records are stored in FlatSQL, a SQL engine that runs as WebAssembly and reads
FlatBuffer records directly. The same engine runs on the node and in the
browser, so a query means the same thing in both places.

## Publishing and pulling data

A provider, such as an organization that tracks satellites, publishes like
this:

1. Encode the data as Space Data Standards records.
2. Store them on its node, which validates and indexes them.
3. Export the dataset into content-addressed files (data shards and a query
   index), and sign a manifest that lists them by CID.
4. Announce the new publication on the network with a signed notification.

Another node pulls it like this:

1. Hear the announcement, or ask a node what it offers.
2. Check the signature against the publisher's identity.
3. Fetch the manifest and files by CID from any node that has them, and check
   that the bytes match their addresses.
4. Import the records into its own store, where they answer local queries.

Because a CID is computed from the bytes themselves, a node can fetch data
from anyone and still know it has exactly what the publisher signed.

## Signing and trust

Each node has one key tree, derived from a single recovery phrase.

- Its signing key is also its network identity (its peer ID), so "who sent
  this" and "who signed this" are the same question.
- Records, publications, modules and software updates are signed. Each kind
  of signature is bound to its purpose, so a signature made for one thing can
  never be passed off as another.
- A node publishes an identity card (an `EPM`) with its name, organization,
  and the public keys that verify it.
- Each node decides how much it trusts every other node and every account,
  from "never" to "full". Trust decides who may connect, who may read
  restricted data, and who may administer the node. Nodes also weigh the trust
  their trusted peers express (a web of trust).

## The catalog

Every node describes what it offers, and the network gathers those
descriptions into a catalog:

- **Datasets**, by standard, provider and source, with how fresh they are.
- **Modules and services**: WebAssembly programs and data products other nodes
  can request, with their versions, prices and terms. Paid modules travel
  encrypted and open only for a buyer that holds a grant.
- **Space objects**: the public catalog of satellites and debris (`CAT` and
  `OMM` records) is open for anyone to read, without an account.

The dashboard's Store shows this catalog: what this node and the nodes it knows
publish, and what it can fetch, buy, or subscribe to.

## Pinning

Content on a node is stored and served by CID. Pinning keeps it from being
cleared when the node frees space. By default a node pins its own identity
card, its accounts' identity cards, what it publishes, the files its modules
add, and this document. The operator turns other kinds of content on or off on
the dashboard's Pinning page.

## Learn more

- Space Data Standards: https://spacedatastandards.org
- Source code and releases: https://github.com/DigitalArsenal/space-data-network
- Each node's own guides: `/docs` on its dashboard address.
