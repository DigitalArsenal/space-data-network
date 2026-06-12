# Multi-Enclave Classification-Aware Data Handling for SDN
## DIU Hydra PROJ00677 — Design Document

**Status:** Draft for review  
**Date:** 2026-06-10  
**Scope:** IL4/IL5/IL6 deployment across USSF Mission Deltas; 18-month Hydra objective

---

## 1. Problem Statement

SDN today operates as a single-enclave, unclassified-only network. The only
classification artifact in the codebase is the CCSDS `CLASSIFICATION_TYPE`
field in `internal/sds/builders.go`, which was hardcoded to `"U"` until the
change described in this document. Crypto primitives that could enforce
releasability exist but are not classification-aware:

- Per-export content keys in `internal/storage/export.go:37-38` (the
  `ContentKeyID` field on `DatasetExport` and `DatasetExportSourceBatch`)
- ECIES/X25519 encrypted module delivery in
  `internal/sds/schemas/{ENC,LWK,PLK}.fbs` and
  `internal/node/licensing_bootstrap.go:374-393`
- A newly-landed ABAC engine (`internal/abac/policy.go`) whose `Resource`
  model already carries a `Classification` attribute

The 18-month DIU Hydra objective requires "seamless automated data sharing and
deconflicted mission execution" across IL4/IL5/IL6 enclaves. This document
specifies how to achieve that goal in three concrete phases without breaking
existing call sites.

---

## 2. Marking Model

### 2.1 Authoritative field

The CCSDS `CLASSIFICATION_TYPE` string field inside each FlatBuffer record is
the per-record authoritative marking. It is written at ingest time and survives
the entire pipeline: local FlatSQL store, dataset export shard, DPM manifest,
pubsub message payload.

With the change landed today (`internal/sds/builders.go:WithClassificationType`,
`internal/ingest/udl.go:ingestUDLElsetRecords`), UDL elset records now carry
their `CLASSIFICATION_MARKING` value directly in the serialised OMM
`CLASSIFICATION_TYPE` field. CelesTrak-sourced records remain `"U"`.

### 2.2 Normalised vocabulary

| Token   | Meaning                                      | Use in SDN              |
|---------|----------------------------------------------|-------------------------|
| `U`     | Unclassified                                 | Default; public network |
| `CUI`   | Controlled Unclassified Information          | IL4 deployment          |
| `C`     | Confidential (rarely used in SSA context)    | Future                  |
| `S`     | Secret                                       | IL5 deployment          |
| `TS`    | Top Secret                                   | IL6 deployment          |
| `TS//SCI` | TS with SCI compartment indicator          | IL6 deployment          |

Caveat strings (e.g. `U//FOUO`, `S//REL TO USA,FVEY`) are preserved verbatim
as received from the authoritative source; no normalisation is applied at
ingest. The ABAC engine performs case-insensitive matching
(`internal/abac/policy.go:278`).

### 2.3 Marking required at publish time (classified deployments)

In IL5/IL6 deployments the node configuration must set
`policies.enabled: true` and include a rule that denies `publish` for any
record whose `classification` attribute is empty or unrecognised. This is an
operator responsibility enforced by the ABAC layer (see Section 5).

---

## 3. Topic Isolation

### 3.1 Per-enclave topic namespaces

Current topic prefix (`internal/pubsub/topics.go:17`):

```
/spacedatanetwork/sds/<SCHEMA>
```

Extended namespace for classified enclaves:

```
/spacedatanetwork/<enclave>/sds/<SCHEMA>
```

Where `<enclave>` is one of: `il4`, `il5`, `il6` (or `u` for the existing
public namespace, preserving backward compatibility).

The `TopicPrefix` constant and `TopicName`/`SchemaFromTopic` helpers in
`internal/pubsub/topics.go` must be made enclave-aware. A new `EnclaveID`
field on `TopicManager` controls which namespace the node joins at startup.

### 3.2 Enclave membership enforcement

A node joins **only** topics matching its configured enclave accreditation.
High-side nodes MAY join lower-side topics as read-only subscribers (for the
guard-node downgrade path described below); they MUST NOT publish to them
directly.

### 3.3 Cross-enclave flow: guard nodes

Cross-domain transfer is mediated by a dedicated guard-node process that:

1. Subscribes to the high-side topic (e.g. `/spacedatanetwork/il5/sds/OMM.fbs`)
2. Applies a human-in-the-loop or automated sanitisation/review step
3. Publishes approved records to the low-side topic (e.g.
   `/spacedatanetwork/il4/sds/OMM.fbs`) after downgrading the
   `CLASSIFICATION_TYPE` field to the target enclave's maximum level

The guard node is architecturally separate from the normal SDN server process.
Its review hook is a pluggable function called before publication to the
lower-side topic. For Phase 2 the hook is a human approval queue; for Phase 3
it can be an automated declassification/sanitisation module delivered via the
existing WASM module runtime.

One-way enforcement (high→low only) is maintained by network topology: the
guard node has two separate libp2p instances (one per enclave), not a single
node with both topics joined.

---

## 4. Releasability by Encryption

### 4.1 Existing mechanism

The `DatasetExport.ContentKeyID` field (`internal/storage/export.go:37`)
already tags each exported shard with the content key used to encrypt it. When
`ContentKeyID` is `"public"` the shard is not encrypted; any other value
indicates an enclave-scoped key. The `DPMEncryptionBinding.ENCRYPTED` flag in
the manifest (`internal/storage/manifest.go:876`) reflects this state.

The licensed-module delivery path (`internal/node/licensing_bootstrap.go:374`)
already generates per-module X25519 wrapping keys: the provider holds an
Ed25519 signing key and an X25519 private key (`providerWrappingSlotID`); the
licensee's ephemeral X25519 public key is used for ECDH key agreement to wrap
the per-module AES-256-GCM content key (described in
`internal/sds/schemas/LWK.fbs` and `internal/sds/schemas/PLK.fbs`).

### 4.2 Enclave content keys

For multi-enclave classification-aware delivery, each enclave maintains a
symmetric AES-256-GCM content key:

```
content_key_il4  →  all CUI records exported to IL4
content_key_il5  →  all S records exported to IL5
content_key_il6  →  all TS/SCI records exported to IL6
```

The key is wrapped for each authorised recipient using the existing X25519
ECDH mechanism (`X25519_HKDF_SHA256_AES_256_GCM` as defined in
`internal/sds/schemas/LWK.fbs:6`). Enclave membership is equivalent to
possession of a key slot containing the unwrapped enclave content key.

Key rotation on membership change (node leave/join) requires re-wrapping the
content key for all remaining members and re-encrypting or re-keying any shards
still in the distribution window. This is handled out-of-band by the enclave
key manager role.

### 4.3 Publish-time enforcement

At dataset export time (`internal/storage/export.go:ExportDatasetWindow`), the
ingest runner selects the correct `ContentKeyID` based on the highest
`CLASSIFICATION_TYPE` value present in the export window. Records with
mixed classifications in a single export batch are rejected; the operator must
issue separate export queries per classification level.

---

## 5. ABAC Integration

### 5.1 Existing engine

`internal/abac/policy.go` provides a pure, deterministic engine. The
`Resource` struct already carries:

```go
Classification string  // "U", "CUI", "S", "TS", …
```

The `ResourceFilter.Classifications` slice supports case-insensitive matching
(`policy.go:278`).

### 5.2 Classification gating rules

Minimal policy for an IL5 node (extend from `sdn-server/docs/abac-policies.md`
example):

```yaml
default_effect: deny

rules:
  # Admins can do anything.
  - effect: allow
    description: "unrestricted admin access"
    subjects:
      min_trust: 4
    actions: [read, publish, subscribe, admin]

  # Cleared subjects (org=IL5_USER) may read Secret records.
  - effect: allow
    description: "IL5 cleared subjects may read S records"
    subjects:
      min_trust: 2
      orgs: ["IL5_USER", "USSF_DELTA_2", "USSF_DELTA_4", "USSF_DELTA_6"]
    actions: [read, subscribe]
    resources:
      classifications: ["S"]

  # Only IL5 cleared subjects may publish Secret records.
  - effect: allow
    description: "IL5 cleared subjects may publish S records"
    subjects:
      min_trust: 3
      orgs: ["IL5_USER"]
    actions: [publish]
    resources:
      classifications: ["S"]

  # Explicitly deny TS/SCI at IL5 boundary.
  - effect: deny
    description: "TS/SCI not handled at IL5"
    actions: [read, publish, subscribe]
    resources:
      classifications: ["TS", "TS//SCI"]

  # Standard users may read/subscribe to unclassified.
  - effect: allow
    description: "standard+ may read U records"
    subjects:
      min_trust: 2
    actions: [read, subscribe]
    resources:
      classifications: ["U"]
```

### 5.3 Subject clearance attributes from EPM

Subject `org` is currently an opaque string that must be set externally
(`abac-policies.md:Roadmap item 3`). For classified deployments, `org` is
sourced from the node's EPM identity record (`EPM.LEGAL_NAME` or a dedicated
clearance attribute in `EPM.KEYS`). The node operator sets the value at
deployment time in the node configuration; automated EPM-resolver population
is a Phase 2/3 enhancement.

---

## 6. Accreditation Path

### IL4 (CUI, Platform One / Iron Bank)

Platform One's Iron Bank provides DoD-hardened container images suitable for
IL4 cATO. SDN server is distributed as a single Go binary with minimal
external dependencies (libp2p, FlatSQL, WasmEdge). The containerisation path
is:

- Base image: Iron Bank `ubi9/ubi-minimal` or `cbi/golang` for the build stage
- Runtime: `ubi9-micro` with the SDN binary, config volume, and data volume
- cATO tooling: `Anchore Enterprise` or `Prisma Cloud` for image scanning;
  STIG STIGs for RHEL 9 apply to the base OS layer
- Secret injection: Vault Agent sidecar or Kubernetes Secrets mounted as files
  (credentials, enclave content key)

No code changes are required for IL4 containerisation; the binary already
reads all secrets from environment variables or config-file paths.

### IL5 (Secret, classified on-prem or GovCloud)

IL5 deployment requires a classified network environment (e.g. AWS GovCloud
Secret Region or a DISA MilCloud 2.0 Secret enclave). SDN's libp2p transport
is already TLS 1.3 authenticated with per-node keypairs; no additional
transport-layer changes are required. The IL5 delta introduces:

1. Operator-supplied IL5 content key injected at node startup
2. ABAC policy file restricting `S`-classified records to cleared subjects
3. Guard-node process on the IL4/IL5 boundary (Phase 2)
4. Audit log forwarding to a DoD-accredited SIEM (splunk or Elastic SIEM in the
   Secret enclave) — the `internal/audit` tamper-evident hash chain provides
   the event stream; the forwarding adapter is a thin wrapper

cATO path: follow the DISA Cloud Computing SRG for IL5; use DoD-approved
cryptographic modules (BoringCrypto or Go FIPS mode) for all key operations.
Go's `GOFLAGS=-tags boringcrypto` build flag enables the BoringCrypto backend
without any code changes.

### IL6 (TS/SCI, SCIF/tactical edge)

IL6 deployment is the most constrained: air-gapped or minimally-connected,
SCIF or tactical hardware. SDN's libp2p peer discovery is already configurable
to use static peer lists only (no DHT, no mDNS) — the relevant config is in
`internal/peers/config.go`. For IL6:

1. Remove all public bootstrap peers from the peer list; use only pre-approved
   node identities
2. Disable DHT (`RoutingType: none`), rely on static peer addressing
3. TS/SCI enclave content key generated on-enclave, never leaves the enclave
4. ABAC policy enforces `TS//SCI` classification for all publish operations
5. Guard-node on IL5/IL6 boundary with mandatory human review for any downgrade

No existing code prohibits IL6 operation; the constraints are purely
operational and policy. A SCIFable deployment image (single binary +
air-gapped OCI image) can be built from existing code today with the
boringcrypto flag and static peer config.

---

## 7. Phasing Matched to Hydra 18-Month Objective

### Phase 1 (0–6 months): Single Enclave at Secret with Markings Enforced

**Goal:** One IL5 deployment with `S`-classification enforced end-to-end.

**What exists today (with today's changes):**

| Capability | File:Line |
|---|---|
| CCSDS CLASSIFICATION_TYPE field in OMM FlatBuffer | `internal/sds/builders.go:32` |
| Per-record classification setter (new) | `internal/sds/builders.go:WithClassificationType` |
| UDL ingest passes CLASSIFICATION_MARKING to record | `internal/ingest/udl.go:ingestUDLElsetRecords` |
| ABAC engine with Classification resource attribute | `internal/abac/policy.go:52-61` |
| Per-export ContentKeyID tagging | `internal/storage/export.go:37` |
| X25519 key wrapping (module delivery) | `internal/node/licensing_bootstrap.go:374-393` |
| ABAC resource filter case-insensitive classification match | `internal/abac/policy.go:274-284` |
| Per-batch classification_markings in provenance | `internal/ingest/runner.go:89-92` |

**What must be built (Phase 1):**

1. Wire ABAC engine into the publish handler and enforce per-record
   `CLASSIFICATION_TYPE` against the requester's clearance. Currently ABAC is
   checked at the API layer but the `Resource.Classification` is not populated
   from the record payload — the handler must deserialise the FlatBuffer header
   to extract the marking (~150 LOC).
2. Expose `classification_type` as an indexed query field in FlatSQL
   (`internal/storage/flatsql.go`) so export windows can be filtered by
   classification (~80 LOC + migration).
3. Add `IL5ContentKeyID` config option and set it on export shards that contain
   only `S`-or-above records (~40 LOC in `internal/ingest/runner.go`).
4. IL5 deployment runbook (not code): node config, ABAC policy file, cATO
   artefacts.

**Rough size:** ~270 LOC of Go, one config schema change, operator runbook.

---

### Phase 2 (6–12 months): Two Enclaves with Guard-Node Cross-Domain Flow

**Goal:** IL4 and IL5 nodes running concurrently; approved S→CUI downgrade
path via guard node.

**What exists today:**

| Capability | File:Line |
|---|---|
| Topic namespace constant (extensible) | `internal/pubsub/topics.go:17` |
| Separate libp2p host construction | `internal/node/node.go` |
| Storefront multi-topic publish | `internal/pubsub/topics.go:97-105` |
| Module WASM runtime (future review hook) | `internal/node/module_publish.go` |

**What must be built (Phase 2):**

1. Enclave-scoped topic namespace: add `EnclaveID` to `TopicManager` and
   `TopicName` helper; update `SetupSDSTopics` to join
   `/spacedatanetwork/<enclave>/sds/<schema>` (~100 LOC).
2. Guard-node binary: thin SDN process variant with two libp2p hosts (IL4 and
   IL5), a human approval queue (simple HTTP UI + approve/deny REST endpoint),
   and the downgrade publish path (~500 LOC new binary).
3. Per-enclave content key injection in export path: classify export windows
   and set the correct `ContentKeyID` based on max classification in window
   (~100 LOC).
4. EPM clearance attribute integration: populate `Subject.Org` from the
   authenticated node's EPM record during session creation (~120 LOC in
   `internal/auth` or `internal/api`).

**Rough size:** ~820 LOC, one new guard-node binary, updated deployment charts.

---

### Phase 3 (12–18 months): Three Enclaves + Automated Releasability Review

**Goal:** IL4/IL5/IL6 simultaneously; automated guard-node using WASM module
runtime for declassification/sanitisation decisions.

**What exists today:**

| Capability | File:Line |
|---|---|
| WASM module runtime (WasmEdge) | `internal/node/key_broker_loader.go` |
| ECIES-encrypted module delivery | `internal/sds/schemas/ENC.fbs` |
| Licensing grant flow (module key delivery) | `internal/node/licensing_bootstrap.go` |
| Audit tamper-evident hash chain | `internal/audit` |

**What must be built (Phase 3):**

1. Automated review hook in guard node: call a WASM module (delivered via the
   existing licensing/module-delivery path) that inspects the record payload,
   checks releasability rules (e.g. source markings, caveats, content
   fingerprints), and returns allow/deny/sanitise (~200 LOC hook + WASM module
   developed separately).
2. IL5→IL6 guard node (second instance) with TS/SCI-to-S downgrade path.
3. Per-enclave key rotation tooling: CLI command to re-wrap enclave content
   keys and re-encrypt shards in the distribution window (~200 LOC).
4. Full audit log integration: route `PolicyDenied` and guard-node events
   through `internal/audit.Logger` for tamper-evident storage and SIEM
   forwarding (`abac-policies.md:Roadmap item 4`).
5. Three-enclave deployment charts and cATO artefacts for all three IL levels.

**Rough size:** ~400 LOC Go + WASM module (separate artefact), deployment
automation.

---

## 8. Open Questions / Assumptions

1. **Marking at ingest for non-UDL sources:** CelesTrak and Space-Track data is
   always unclassified (`"U"`). The operator is responsible for not feeding
   classified data through unclassified ingest paths.
2. **Mixed-classification export batches:** The design rejects them at Phase 1.
   A future enhancement could split a mixed batch into two shards at export
   time.
3. **Vocabulary authoritative source:** The CCSDS specification defines
   `CLASSIFICATION_TYPE` as a free string. The SDN vocabulary table in Section
   2.2 is normative for SDN deployments; sources that use non-standard strings
   (e.g. `UNCLASSIFIED` instead of `U`) will need a normalisation map, which
   is deferred to Phase 1 implementation.
4. **Guard-node authority model:** Who approves a high→low transfer? For Phase
   2, a human operator; for Phase 3, a WASM policy module accredited separately.
   The design intentionally leaves the review hook abstract.
