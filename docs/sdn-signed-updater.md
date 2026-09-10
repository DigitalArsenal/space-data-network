# SDN Signed Updater Design

This document defines the SDN-owned updater target architecture. It replaces
inherited IPFS Desktop application update behavior with signed SDN payloads
while keeping Kubo/IPFS runtime refreshes explicit and separable.

## Current CLI execution path

The portable `bin/spacedatanetwork` launcher resolves symlinks, selects the
bundled WasmEdge runtime unless `WASMEDGE_DIR` explicitly overrides it, and
executes the bundled SDN daemon. The daemon supervises its separate Kubo child
with loopback RPC and gateway listeners. Kubo readiness requires a successful
version response, not merely an HTTP listener.

For automatic updates, the running daemon verifies the small signed pubsub
pointer, fetches and verifies its manifest, and checks their channel, target,
version, sequence and hashes agree before downloading the carrier. Downloads
remain HTTPS across redirects and use bounded sizes; a signed carrier size
tightens the payload limit. Staging verifies the actual carrier and bundle
hashes again. The copied helper then performs the shutdown, swap, restart and
health/rollback sequence outside the replaceable bundle and service cgroup.

This path is implemented in Go's generic artifact/update machinery. The
`packages/sdn-updater-module` WASM package still contains stub methods and is
not the executing update coordinator. Shipping that file does not establish
module-driven updating. Its scheduling, upstream checks and orchestration
still need an implemented SDK module and an approved host lifecycle capability.
The current receiver listens for live signals; it does not reconcile updates
missed while offline. A daemon launched from a scratch binary outside a portable
bundle cannot use this in-place update path.

## Payload Envelope

Every update is distributed as an SDN update payload with a signed manifest and
an inert WASM carrier. The manifest is canonical JSON until a canonical SDS
record exists.

Required manifest fields:

- `schema`: fixed value `org.spacedatanetwork.update.v1`.
- `update_id`: globally unique update identifier.
- `version`: SemVer update version.
- `sequence`: monotonic integer used for rollback rejection.
- `channel`: release channel, such as `stable`, `beta`, or `dev`.
- `created_at` and `expires_at`: RFC 3339 timestamps.
- `target.platform`: `darwin`, `linux`, or `win32`.
- `target.arch`: `arm64`, `x64`, or another supported Electron/Kubo arch.
- `target.kind`: `desktop-app`, `kubo-runtime`, `module-delivery`, or `suite`.
- `compatibility.min_app_version` and `compatibility.max_app_version`.
- `compatibility.min_kubo_version` when the update touches runtime behavior.
- `bundle.hash`: SHA-256 digest of the compressed update bundle.
- `bundle.size`: byte length of the compressed bundle.
- `bundle.format`: `tar.zst` for app/runtime bundles, `zip` only where the
  platform installer requires it.
- `wasm.hash`: SHA-256 digest of the inert WASM carrier bytes.
- `signing.key_id`: trusted root or delegated key identifier.
- `signing.algorithm`: `Ed25519`.
- `signing.signature`: Ed25519 signature over the canonical manifest bytes with
  `signing.signature` omitted.
- `rollback.previous_sequence`: last sequence allowed as a rollback target.
- `rollback.reason`: required for rollback payloads.

The manifest must bind the exact target platform, bundle hash, WASM hash,
signer identity, expiration, and rollback policy. Clients must reject manifests
with missing, duplicated, or unknown critical fields.

## WASM Carrier And Bundle Extraction

The WASM carrier is a storage envelope, not executable authority. The updater
never instantiates or executes the carrier, including after verification; it
extracts its embedded bundle as data.

Bundle layout:

```text
update.wasm
manifest.json
bundle.tar.zst
```

The release index advertises the manifest hash and the carrier hash. The client
downloads the manifest first, verifies the manifest signature against trusted
roots, then downloads the carrier and verifies `wasm.hash`. The updater extracts
the embedded bundle bytes as data, computes `bundle.hash`, and only then stages
the bundle for install. Any decompression, installer metadata parsing, or file
replacement happens after signature, expiration, target, sequence, and hash
validation.

## Compatibility Rules

Desktop application updates may replace Electron application assets and desktop
helper code. They must not replace Kubo through an inherited IPFS Desktop app
release feed.

Kubo runtime updates are separate payloads with `target.kind:
kubo-runtime`. They may update the bundled `kubo` binary/package path only after
the app confirms the target Kubo version is compatible with the existing repo
version and migration path.

Module-delivery updates are separate payloads with `target.kind:
module-delivery`. They may update licensing or delivery module artifacts, but
they must not modify the Electron app bundle or Kubo binary.

Suite updates use `target.kind: suite` and are an ordered set of
desktop-app/runtime/module-delivery manifests. Each child manifest is verified
independently before any staged swap begins.

Rollback is allowed only to a manifest whose sequence is lower than the current
sequence and equal to or newer than the current `rollback.previous_sequence`.
Rollback payloads must be signed and unexpired. The updater stores the last
known-good manifest and staged bundle hash outside mutable payload contents.

## Signature And Key Rotation

The signing scheme is Ed25519 over canonical manifest bytes. SDN already uses
Ed25519 for portable identity records and node attestations, so this keeps the
update trust primitive consistent with the rest of the stack.

### Who signs, and what the signature actually covers (2026-07-31)

OWNER RULING 2026-07-30: **the publisher key IS the node key.** There is no
separate update root and no delegation ceremony. Trust is priced by the
Adversarial-Security bond on the chain addresses derived from that key, not by
where the private half is stored. The delegated-release-key design described
below this section is the ORIGINAL design and is not what ships; it is retained
because delegation is still the right shape if the fleet ever outgrows one
publisher.

The node key cannot leave host-01 (machine-bound key-at-rest, 2026-07-28) and
release artifacts are never built on a host (build-locally-ship-binaries). The
resolution is a content-bound signing endpoint, the sibling of the module one:

```text
POST /api/v1/admin/updates/sign-manifest      (Admin session required)
```

The build box POSTs the manifest DOCUMENT with `signing.statement_domain` set
and `signing.signature` absent. The node canonicalizes and hashes the document
ITSELF and signs a domain-separated statement:

```text
statement = "SDN-UPDATE-MANIFEST-V1" || 0x00 || sha256(canonicalManifestBytes)
signature = ed25519(nodeSigningKey, statement)
```

Three properties are non-optional and enforced in code: the domain separation
above (so an update signature can never be replayed as a module publication or
a dataset-publication signature and vice versa), the node never signing a
caller-supplied digest (a digest is not a well-formed manifest, so it is
structurally unsignable), and an append-only audit line per signature that is a
GATE — an unauditable signature is discarded, not logged and returned.
Audit: `~/.spacedatanetwork/logs/update-signing.audit.jsonl`, override
`SDN_UPDATE_SIGNING_AUDIT_LOG`.

**Two accepted verification forms.** `signing.statement_domain` lives inside the
canonical bytes (canonicalization removes only `signing.signature`), so it is
covered by the signature it describes. Absent means the legacy raw-canonical
form, which verifies exactly as before; present means it must be EXACTLY
`SDN-UPDATE-MANIFEST-V1` — matched by equality, never by domain-registry
membership, because a registry lookup would admit a module signature here.
There is no downgrade in either direction: adding or stripping the field changes
the canonical bytes and invalidates the signature outright.

Because old verifiers do not know the second form, they FAIL CLOSED on a
domain-signed manifest rather than accepting it unverified. A box therefore
needs a binary carrying this verifier before it can consume domain-signed
releases — which is why the lane's first delivery to any given box is a
bootstrap, not an update.

The trust store must hold the node's key in **SPKI DER base64**. The fleet
verifier also accepts raw hex, but the desktop verifier accepts SPKI only, so a
hex root produces a trust store that works on one side and fails on the other.
The endpoint logs the exact `{key_id: public_key}` pair to install at startup.

Trusted update roots are stored outside mutable update payloads:

- packaged desktop root trust store under app resources;
- OS-protected user-data trust store for emergency revocation lists;
- optional enterprise policy store when managed deployments need pinned roots.

Root keys do not sign routine payloads directly. A root key signs delegated
release keys with:

- delegated `key_id`;
- allowed channels;
- allowed target kinds;
- `not_before` and `not_after`;
- minimum accepted sequence.

Clients accept a payload only when its signing key chains to a trusted root and
the delegation permits the payload channel, target kind, platform, and sequence.
Key rotation is shipped as a signed trust-store update and takes effect only
after both the old and new root policies validate the transition.

## Updater Process Boundary

The SDN updater is a standalone helper process launched by SDN Desktop. The main
app may request update checks, display progress, and ask the helper to stage an
approved update, but it does not directly replace application/runtime files.

IPC commands:

- `check`: fetch release index metadata and report available manifests.
- `download`: download manifest and carrier into a staging directory.
- `verify`: run signature, target, expiration, sequence, and hash checks.
- `stage`: unpack the verified bundle into a versioned staging directory.
- `commit`: stop affected SDN processes, atomically swap staged files, and
  record the new manifest as current.
- `rollback`: restore the last known-good staged version when startup health
  checks fail.
- `history`: return audit records for UI display.

Privilege model:

- The Electron UI runs unprivileged.
- The helper owns file replacement and platform-specific elevation.
- Kubo shutdown/startup is coordinated through the desktop daemon lifecycle.
- No update payload receives execution rights during verification.

## Audit And Lifecycle

The updater writes append-only audit records for checks, downloads,
verification, staging, commit, rollback, and failures. Each record includes:

- timestamp;
- actor (`automatic`, `manual`, or `policy`);
- update id, version, sequence, target kind, and channel;
- manifest hash, bundle hash, signer key id, and verification result;
- previous version and new version;
- process lifecycle actions for SDN Desktop and Kubo;
- error code and remediation hint when failed.

The desktop UI should surface this history in the update/settings area before
automatic installation is enabled.

## SDN Update Feed

The SDN-owned update feed origin is:

```text
https://sdn.spaceaware.io/updates
```

Desktop application payloads are indexed under:

```text
/desktop/<channel>/<platform>/<arch>/index.json
/desktop/<channel>/<platform>/<arch>/<version>/manifest.json
/desktop/<channel>/<platform>/<arch>/<version>/update.wasm
```

Runtime and module-delivery payloads use their target kind in place of
`desktop`, for example:

```text
/kubo-runtime/<channel>/<platform>/<arch>/index.json
/module-delivery/<channel>/<platform>/<arch>/index.json
```

The feed index schema is `org.spacedatanetwork.update.index.v1`. It contains
only SDN-owned manifest and carrier URLs plus the manifest identity fields a
client needs before downloading a payload. The signed manifest remains the
authority for install decisions.

Feed assembly is implemented as static artifact tooling:

```sh
node deployment/release/build-sdn-update-feed.js \
  --out-dir dist/release/update-feed \
  --entry path/to/manifest.json:path/to/update.wasm
```

`deployment/release/assemble-release-artifacts.sh` also accepts
`SDN_UPDATE_FEED_ENTRIES` as a comma-separated list of
`manifest.json:update.wasm` pairs and writes the feed under
`dist/release/update-feed` by default. Publication to
`sdn.spaceaware.io/updates` is a release-operations step after artifact
verification; the tooling intentionally does not claim a live deployment.

## Electron Application Update Path

Branded SDN Desktop builds must use the SDN update feed origin for application
updates. Inherited IPFS Desktop GitHub release feeds are not accepted for SDN
application update metadata.

Manual update fallbacks may still open the SDN GitHub release page while
automatic SDN Desktop app updates remain disabled, but any Electron automatic
update feed must resolve to:

```text
https://sdn.spaceaware.io/updates/desktop/<channel>/<platform>/<arch>
```

Kubo runtime checks are separate from Electron app updates and may inspect
upstream `ipfs/kubo` release metadata only to select an explicit runtime
payload. They must never point the Electron app updater at upstream IPFS
Desktop releases.

## Upstream Refresh Process

Use this process for an upstream IPFS Desktop/WebUI/Kubo refresh:

1. Pin selected upstream revisions in the release notes or refresh manifest:
   IPFS Desktop commit/tag, IPFS WebUI commit/tag, and Kubo version/tag.
2. Refresh upstream mirror directories from those exact revisions without
   product edits in the mirror refresh commit.
3. Reapply SDN overlays, patches, generated vendor snapshots, branding,
   networking defaults, bootstrap policy, and SDN update feed configuration.
4. Run overlay application tests and focused updater tests proving inherited
   IPFS Desktop app update feeds are still disabled.
5. Build desktop/runtime/module payloads, sign manifests, verify payload hashes,
   and assemble the static SDN feed index.
6. Publish the feed tree to `sdn.spaceaware.io/updates`.
7. Publish the signed manifest through the SDN network release path so clients
   consume SDN-owned metadata only.

The upstream mirror refresh and SDN overlay reapplication should remain
separate commits where practical. That keeps future refresh failures easy to
attribute to upstream drift or SDN overlay drift.

## Rollback And Emergency Disable

Rollback uses a signed rollback manifest with `rollback.reason` and
`rollback.previous_sequence`. Clients reject rollback manifests that are
unsigned, expired, outside policy, or below the allowed rollback floor.

Emergency disable for a bad manifest or bad upstream refresh:

1. Remove or quarantine the affected `index.json` entry at
   `sdn.spaceaware.io/updates`.
2. Publish a corrected index that points clients at the last known-good signed
   manifest, or publish no update for that target while investigation is open.
3. Add the bad manifest sequence, update id, and signing key id to the
   revocation/policy store when key or signing-policy compromise is suspected.
4. Publish a signed rollback payload only after the rollback bundle has passed
   the same manifest, hash, target, and health checks as a forward update.
5. Record the incident in updater audit history and release notes, including
   the disabled sequence and the replacement sequence.

Do not delete audit records or mutate an already-published manifest in place.
Disable through the feed index and signed policy metadata so clients can retain
a verifiable history of what happened.

## CLI Bundle Update Payloads

Self-contained CLI bundles use the same signed envelope with `target.kind:
cli-bundle`. The verification side is the Go updater in
`sdn-server/internal/update/` (`spacedatanetwork update ...`); the release
side is the tooling in `deployment/release/`.

Feed layout follows the target-kind convention:

```text
/cli-bundle/<channel>/<platform>/<arch>/index.json
/cli-bundle/<channel>/<platform>/<arch>/<version>/manifest.json
/cli-bundle/<channel>/<platform>/<arch>/<version>/update.wasm
```

Lane-specific manifest rules:

- `target.kind` is `cli-bundle`.
- `target.platform` and `target.arch` use Go runtime names (`darwin`, `linux`,
  `windows` and `amd64`, `arm64`). The Go verifier also accepts the
  Electron-style `win32`/`x64` aliases used by the desktop lane.
- When the bundle wraps an upstream IPFS/Kubo runtime, the signed manifest
  records `upstream.kubo.source` and `upstream.kubo.version`. Clients still
  download through the SDN-owned feed and trust only the SDN signature; the
  upstream fields are provenance for the wrapped payload, not an upstream
  update authority.
- `bundle.format` is `tar.gz`: the carrier embeds the exact
  `spacedatanetwork-<version>-<os>-<arch>.tar.gz` archive produced by
  `deployment/release/build-self-contained-cli.mjs`.
- `expires_at` is 90 days after `created_at`. The payload builder requires an
  explicit `--created-at` so rebuilds are reproducible.

Trust roots ship inside the installed bundle at
`<bundle>/trust/update-roots.json`: a JSON object mapping `signing.key_id` to
a base64-encoded Ed25519 public key (SPKI DER as exported by the release
tooling, or a raw 32-byte key). `stageBundle` stages the file via its
`trustRootsPath` option; it is bundle metadata, so it is excluded from
`manifest.json` artifacts and `checksums.txt`, and the staged swap never
replaces it. `SDN_UPDATE_TRUST_ROOTS` overrides the path for tests and
managed deployments.

End-to-end release flow:

```sh
# 1. Wrap the CLI bundle archive in a carrier and sign the manifest.
#    The signing key comes from --key or SDN_UPDATE_SIGNING_KEY_PEM.
node deployment/release/build-cli-update-payload.mjs \
  --bundle-archive dist/release/spacedatanetwork-1.2.3-darwin-arm64.tar.gz \
  --version 1.2.3 \
  --sequence 7 \
  --channel beta \
  --platform darwin \
  --arch arm64 \
  --key-id sdn-release-2026 \
  --key key.pem \
  --created-at 2026-06-10T00:00:00Z \
  --upstream-kubo-version v0.35.0 \
  --out-dir dist/update/darwin-arm64

# 2. Assemble the static feed tree (index.json + payload copies).
node deployment/release/build-sdn-update-feed.js \
  --out-dir dist/release/update-feed \
  --entry dist/update/darwin-arm64/manifest.json:dist/update/darwin-arm64/update.wasm

# 3. Publish the feed tree, then on the target host stage and apply.
spacedatanetwork update stage \
  --manifest https://sdn.spaceaware.io/updates/cli-bundle/beta/darwin/arm64/1.2.3/manifest.json \
  --carrier https://sdn.spaceaware.io/updates/cli-bundle/beta/darwin/arm64/1.2.3/update.wasm
spacedatanetwork update apply
```

The carrier and manifest helpers are also usable standalone:

```sh
node deployment/release/build-update-carrier.mjs \
  --bundle path/to/bundle.tar.gz --out path/to/update.wasm
node deployment/release/sign-update-manifest.mjs \
  --manifest unsigned.json --key key.pem --out manifest.json
```

`update stage` re-runs the full manifest verification (signature against the
bundle trust roots, target, expiration, sequence, bundle/wasm hashes) before
writing anything under `updates/staged/`, and `update apply` re-verifies the
staged files before the atomic swap.
