# Operational module placement

`deployment/module-placement.json` assigns every canonical plugin/version to one
of the five existing node identities. University names are operator-selected
test roles. The local node is the customer, history store, and execution host.
Placement is deployment configuration. Identity, version, dependencies and
names come from each package's PLG manifest, not a second module schema.

## Inventory

```sh
node scripts/module-placement.mjs --public /path/to/space-data-network-modules \
  --closed /path/to/space-data-network-closed-modules --out /tmp/placement.json
```

The inventory includes missing builds and deduplicates generated and embedded
copies by plugin ID/version without downgrading protection. It checks the WASM
bytes, host import/export surface and embedded PLG identity. Stream modules use
the SDK module harness; composed flows use the SDK flow host; direct libraries
use their declared library loader. `artifact-verified` is initialization and
manifest evidence, not numerical validation, tri-runtime parity, or proof of a
running service. Do not enable schedules as a side effect of assigning modules.
Contextual ingestion modules do not make their datasets space-data products.

## Customer-encrypted test checkout

The test path uses the SDK REC/MBL/PNM/ENC single-file bundle, sealed directly to
the existing local node's X25519 public key. The node's private key stays in its
existing identity store. Providers and IPFS caches receive ciphertext only.
This path takes no payment and does not assert a production license entitlement.

```sh
node scripts/module-placement.mjs --public /path/to/space-data-network-modules \
  --closed /path/to/space-data-network-closed-modules --out /tmp/placement.json \
  --customer-stage /private/new-customer-stage \
  --customer-public-key-file /path/to/node-public-key-hex
```

Staging verifies the original file hash and the SDK canonical plaintext hash.
The latter excludes SDK metadata sections removed during packaging. Using the
whole-file hash for decrypted bytes incorrectly rejects valid bundles.

Pin each staged ciphertext file on its assigned provider's IPFS node. Preserve
its pin receipt. Add `published: true`, `customerCid`, `customerSha256` and
`customerPublicKey` to the corresponding inventory row after confirming the pin
hash and provider identity. `artifact.canonicalSha256` pins the decrypted bytes.
Keep this generated inventory in operator-local storage, not the public repo.

On the customer dev node, set `SDN_STOREFRONT_DEV_PAYMENTS=1` and
`SDN_MODULE_CUSTOMER_CATALOG` to that inventory. The admin-only
`/api/v1/modules/customer` endpoint supplies Store assignments and handles test
checkout. It is absent when test mode is disabled. Requests select only an
approved plugin/version/provider; they cannot supply keys, prices, paths or CIDs.

Checkout fetches ciphertext through IPFS, verifies its pinned hash, decrypts
with the local identity, verifies the canonical WASM hash, and atomically caches
ciphertext plus a receipt. Downloads are bounded to 64 MiB and 90 seconds. The
existing API rate limit applies. Repeated checkout verifies the encrypted cache.
Success means **downloaded**, not installed, executed, or scientifically tested.
Incorrect customer keys and changed ciphertext fail verification. Artifact
encryption does not provide server-blind conjunction assessment of customer data.

## Existing licensing publication path

`--stage /private/new-stage --customer-xpub-file /path/to/public-xpub` retains
the existing provider-wrapped staging format for `plugins publish-orbpro`.
Public modules have an explicit open grant policy; restricted modules use a
customer allowlist and `sdn:test-customer` scope. Generated wrapping keys are
0600 under 0700 directories. Never commit, print, or publish them as HTTP assets.

This is distinct from customer-sealed test checkout. Production licensing still
requires an authenticated LCH/LPF/LGR exchange, requester EPM binding and verified
customer key delivery. The installed licensing WASM currently predates its
source fix and rejects the current EPM's separate xpub/signing-key binding.
The legacy client also needs REC-aware delivery and requester-EPM forwarding.
Do not weaken allowlists, signatures or identity checks to work around this.
Rebuild and validate that shared licensing path before enabling paid checkout.

## Current test placement

| Node role | Assigned | Validated and pinned | Protected assigned / ready |
| --- | ---: | ---: | ---: |
| CelesTrak products | 54 | 44 | 2 / 1 |
| TU Delft | 19 | 17 | 1 / 0 |
| CU Boulder | 11 | 7 | 4 / 2 |
| UT Austin | 17 | 14 | 14 / 13 |
| Local customer | 35 | 28 | 2 / 1 |
| Total | 136 | 110 | 23 / 17 |

These are deployment observations from 2026-09-06, not hardcoded UI totals.
The live catalog supplies counts. Failed builds remain assigned and visible,
with checkout unavailable until verified ciphertext is published.

RF ITU propagation and the ephemeris exporter were rebuilt from closed-modules
commit `5b64328`, passed 8 and 9 focused tests respectively, and passed loading
in real Chrome, native WasmEdge 0.16.4 and its pinned container. Reproduction:
run the RF package's `build.js`, and the ephemeris package's `build.mjs` with
`SPACE_DATA_NETWORK_MODULES_ROOT` pointing at the public modules checkout.
The latter needs its declared `flatc-wasm`, `flatbuffers` and SDS dependencies.

Six protected packages remain blocked: CSV exporter, Starlink parser and
validator declare wildcard port types; poly-coverage references unresolved
standard types; RF terrain solver lacks canonical FlatBuffer counterparts for
its aligned ports; sensor-model currently declares only a browser target.
The build attempts stopped on those contracts. Do not substitute untyped ports,
invent schema identifiers, or advertise a browser-only build as node-ready.

A stricter source-PLG audit also found 33 of the 110 downloadable packages with
stream-manifest findings (including direct libraries whose declared library
interface needs its own validation). These downloads are not a blanket SDK
compliance certification. Reconcile source descriptors with the embedded PLG,
apply the correct interface validator, and prove typed invocation before
enabling installation or services. The customer endpoint intentionally never
reports installation or execution success.

Each provider has an IPFS pin set. CU Boulder required a dedicated Kubo instance
(`sdn-module-ipfs.service`, loopback API 5002); its direct swarm port 4002 is not
reachable from the customer. The dev customer currently peers through an SSH
forward on loopback 14302 to CU Boulder's swarm port. This tunnel is a dev
connectivity dependency and needs supervision or a reachable private swarm
address for durable operation. Other provider swarm connections are direct.
The dev UI runs on 5181, original customer backend on 7173, and the second local
UT Austin node on 7174. None of these deployment observations authorize a fleet
binary update or a change to provider identities.
