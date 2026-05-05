# SDN Signed Updater Design

This document defines the SDN-owned updater target architecture. It replaces
inherited IPFS Desktop application update behavior with signed SDN payloads
while keeping Kubo/IPFS runtime refreshes explicit and separable.

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
must never instantiate or execute the WASM module before all signature and hash
checks pass.

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
