# PMM Listing Submission — the self-serve lane

The provider module manifest (`$PMM`, served at
`https://<provider>/.well-known/sdn/modules.pmm`) is the node's signed catalog
of modules. Until now, the only way to get a module listed was to ask the
node's operator to hand-edit the on-disk catalog — a tribal-knowledge path that
has no public contract.

This document is that contract. It describes the **anonymous self-serve
submission lane**: a way for anyone to list a PLAINTEXT (unencrypted) module
with a $PMM provider, with no admin wallet, no operator round-trip, and a
verifiable receipt.

## What you can and cannot get listed

The lane serves exactly one kind of listing, and that is policy, not
negotiable. Every accepted submission is materialized as:

| Field            | Value       | Meaning                                                        |
|------------------|-------------|----------------------------------------------------------------|
| `ACCESS_POLICY`  | `ANONYMOUS` | the artifact is plaintext and publicly served                  |
| `TRUST_TIER`     | `OPTIONAL`  | listed, NOT endorsed by this node                              |
| `ENTRY_STATE`    | `ACTIVE`    | listed now; withdrawal is the operator's act                   |
| `DEFAULT_ENABLED`| `false`     | a client auto-enables only endorsed modules                    |

The protected publish lane is untouched: submissions can never be
`ENTITLED`/encrypted, can never claim `CORE` or `RECOMMENDED` trust, and the
node's admin-wallet gate still guards the encrypted-bundle lane. The manifest
remains **signed by the node key** over the canonical statement, so anonymous
consumers verify integrity and provenance exactly as before — the node vouches
for the bytes it serves (content hash), never for a module's behavior.

## What to send

```
POST https://<provider>/.well-known/sdn/modules.pmm/submissions
Content-Type: multipart/form-data
```

Two parts:

- **`metadata`** — one JSON object (≤ 64 KiB), all keys lowercase:
  - `module_id` (required) — reverse-DNS style, `[A-Za-z0-9][A-Za-z0-9._-]*`,
    must not contain `..`, ≤ 200 chars.
  - `name` (required) — ≤ 200 chars.
  - `version` (required) — `[A-Za-z0-9][A-Za-z0-9._+-]*`, must not contain
    `..`, ≤ 64 chars.
  - `plugin_type` (required) — a real `pluginCategory` symbol (for example
    `Propagator`, `Sensor`, `Comms`, `Unspecified`). A blank value is
    **rejected**, never defaulted.
  - `description`, `license` (≤ 256 chars), `documentation_url`, `icon_url`
    (absolute `http(s)` URLs, ≤ 512 chars each), `runtime_targets`,
    `required_schemas`, `min_permissions` (each ≤ 16 entries, each entry ≤ 128
    chars), `submitter_contact` (≤ 256 chars) — all optional.
  - Any other key is ignored. In particular, `access_policy`, `trust_tier`,
    `entry_state` and `default_enabled` are **not** settable here — the values
    above are unconditional.
- **`artifact`** — a file field: the module's WebAssembly binary, ≤ 64 MiB,
  starting with the standard wasm magic (`\0asm`) and version 1. An
  already-published trailered container (payload `||` REC-flatbuffer `||`
  length `||` `$REC`) is fine: the lane hashes and serves exactly the portable
  bytes the loader compiles.

## What you get back

**`202 Accepted`** with a JSON receipt:

```json
{
 "status": "accepted",
 "submitted_at": "2026-08-21T12:00:00.000Z",
 "module_id": "com.example.demo",
 "name": "Demo Module",
 "version": "1.0.0",
 "plugin_type": "Propagator",
 "content_hash": "sha256...",
 "artifact_size_bytes": 123456,
 "access_policy": "ANONYMOUS",
 "trust_tier": "OPTIONAL",
 "entry_state": "ACTIVE",
 "default_enabled": false,
 "supersedes_content_hash": "",
 "artifact_path": "/modules/submissions/com.example.demo/1.0.0/module.wasm",
 "manifest_url": "/.well-known/sdn/modules.pmm",
 "listed": true,
 "note": ""
}
```

- `content_hash` is the SHA-256 of the portable payload **as the node stored
  it**. Verify your submission by hashing the bytes you sent (stripping any
  publication trailer) and comparing.
- `listed` is `true` when the manifest was re-signed immediately with your
  entry. If the node could not rebuild the manifest (for example, mid-refresh),
  the receipt says `listed: false` and the `note` says so: your submission is
  stored and appears on the next manifest refresh — at most 6 hours.
- Your listing appears in the manifest at `manifest_url`. The manifest is
  verifiable: it carries the canonical signed statement over
  `module <MODULE_ID> <VERSION> <CONTENT_HASH> <TRUST_TIER> <ACCESS_POLICY>
  <enabled> <ENTRY_STATE>` lines, so a client can re-derive the statement from
  the record and check `SIGNATURE` against the provider key.

### Rejections

| Status | Meaning                                                                 |
|--------|-------------------------------------------------------------------------|
| 400    | contract violation: missing/invalid field, `plugin_type` not a symbol, empty or non-wasm artifact |
| 409    | `MODULE_ID` is already managed by this provider's operator catalog, **or** an identical `MODULE_ID`/`VERSION` listing already exists |
| 413    | request body or metadata beyond the caps                                |
| 415    | content type is not `multipart/form-data`                               |
| 405    | anything other than `POST` (the lane is POST-only)                      |

## Lifecycle

- **Versions are immutable.** A resubmission with the same `MODULE_ID` and
  `VERSION` is refused even if the bytes differ — a listed artifact path never
  changes.
- **New versions chain.** Submitting a new `VERSION` of the same `MODULE_ID`
  replaces the listing and sets `SUPERSEDES_CONTENT_HASH` to the previous
  version's hash. The old bytes stay on disk, content-addressed (caches and
  verifiers may still hold them), until the operator cleans up.
- **Withdrawal is the operator's act** — delete the submission's record and
  artifact on the node (`<storage>/modules/submissions/*.json` and
  `<storage>/modules/submissions/artifacts/...`). A submission that cannot be
  re-hashed from disk — deleted, tampered, or corrupt — drops out of the
  manifest on the next refresh with a logged reason and can never take the
  signed manifest down.
- **The operator catalog always wins.** If the operator later takes over your
  `MODULE_ID`, their entry supersedes yours in the manifest; submissions whose
  `MODULE_ID` the operator already manages are refused at submit time.

## Integrator notes

- `TRUST_TIER: OPTIONAL` means this node does not endorse your module. Clients
  that auto-enable only endorsed modules will list it but not run it by
  default. That is the point of the lane: your module is discoverable and
  fetchable; earning a higher tier is a relationship with the operator.
- `ARTIFACT_SIGNATURE` is deliberately empty on every listing (operator and
  submission alike) pending an owner ruling on the Seal Council's
  domain-separation dissent. The manifest's `SIGNATURE` already binds every
  `CONTENT_HASH` via the canonical statement, so an empty value costs a
  bandwidth optimisation, not integrity; when the ruling lands, signed
  artifact digests can be added without a schema change.
