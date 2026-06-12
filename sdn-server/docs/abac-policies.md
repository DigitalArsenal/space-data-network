# ABAC Policies

SDN ships a minimal, deterministic attribute-based access control (ABAC) layer
(`internal/abac`) for DIU Hydra PROJ00677.  The policy engine is layered on top
of the existing five-level trust ordinal — it is additive, not a replacement.

---

## Attribute model

### Subject (the caller)

| Attribute     | Source                                      | Example              |
|---------------|---------------------------------------------|----------------------|
| `xpub`        | BIP-32 extended public key from session     | `xpubABCD1234…`      |
| `trust_level` | Numeric ordinal: 0=untrusted … 4=admin      | `2` (standard)       |
| `org`         | EPM identity attestation (opaque string)    | `"DIU"`              |
| `peer_id`     | libp2p peer ID                              | `"QmPeer1…"`         |
| `attrs`       | Arbitrary string key-value pairs            | `{"cui": "true"}`    |

### Action

One of: `read`, `publish`, `subscribe`, `admin`.

### Resource

| Attribute        | Meaning                                   | Example          |
|------------------|-------------------------------------------|------------------|
| `schema`         | FlatBuffer schema name                    | `"OMM.fbs"`      |
| `topic`          | Pubsub topic name                         | `"/sdn/data/OMM.fbs"` |
| `classification` | Data sensitivity label                    | `"U"`, `"CUI"`, `"S"` |
| `provider_id`    | Data provider identifier                  | `"provider-42"`  |

---

## Policy document (YAML)

A policy is an ordered list of rules.  Evaluation is **first-match-wins**; if no
rule matches, the `default_effect` is applied (default: `deny`).

```yaml
# docs/example-policy.yaml
default_effect: deny

rules:
  # Admins (trust_level >= 4) may do anything.
  - effect: allow
    description: "unrestricted admin access"
    subjects:
      min_trust: 4
    actions: [read, publish, subscribe, admin]

  # Org "DIU" may publish OMM data at standard trust or above.
  - effect: allow
    description: "DIU standard+ may publish OMM"
    subjects:
      min_trust: 2
      orgs: ["DIU"]
    actions: [publish]
    resources:
      schemas: ["OMM.fbs", "OMM.*"]

  # CUI-classified resources are denied globally (any subject not already
  # matched by an earlier allow wins the deny).
  # Subjects with clearance should be handled by an allow rule placed
  # earlier in the list, matched via xpub or org.
  - effect: deny
    description: "block CUI for uncleared subjects"
    actions: [read, publish, subscribe]
    resources:
      classifications: ["CUI"]

  # Allow standard+ to read unclassified data.
  - effect: allow
    description: "standard+ may read unclassified"
    subjects:
      min_trust: 2
    actions: [read]
    resources:
      classifications: ["U"]
```

### Rule fields

| Field             | Type             | Required | Notes                                       |
|-------------------|------------------|----------|---------------------------------------------|
| `effect`          | `allow`\|`deny`  | yes      |                                             |
| `description`     | string           | no       | Human-readable label, appears in audit log  |
| `subjects`        | SubjectFilter    | no       | Empty = matches all subjects                |
| `actions`         | []Action         | no       | Empty = matches all actions                 |
| `resources`       | ResourceFilter   | no       | Empty = matches all resources               |

#### SubjectFilter

| Field       | Type     | Notes                                                   |
|-------------|----------|---------------------------------------------------------|
| `min_trust` | int      | Subject must have `trust_level >= min_trust`            |
| `xpubs`     | []string | Exact xpub match (OR within list); empty = no constraint |
| `orgs`      | []string | Exact org match (OR within list); empty = no constraint  |

#### ResourceFilter

| Field             | Type     | Notes                                                          |
|-------------------|----------|----------------------------------------------------------------|
| `schemas`         | []string | Glob patterns; `*` matches any sequence, `?` matches one char  |
| `classifications` | []string | Case-insensitive exact match (OR within list)                  |
| `providers`       | []string | Exact provider_id match (OR within list)                       |

---

## Configuration

Enable the engine and point at a policy file (or supply inline rules) in
`~/.spacedatanetwork/config.yaml`:

```yaml
policies:
  enabled: true
  default_effect: deny
  path: /etc/sdn/policy.yaml        # optional: path to YAML policy file
  inline_rules: []                  # optional: rules inlined in config
```

When `policies.enabled` is `false` (the default), the engine is never consulted
and all existing trust-level checks operate exactly as before.

---

## Enforcement points

| Endpoint                          | Check                              |
|-----------------------------------|------------------------------------|
| `POST /api/v1/data/publish/{schema}`       | `action=publish, resource.schema={schema}` |
| `POST /api/v1/data/publish/batch/{schema}` | same                               |
| `POST /api/v1/pubsub/publish`              | `action=publish, resource.schema={req.schema}` |
| Middleware (`RequirePolicy`)               | Generic: wraps any handler         |

The ABAC check is placed **after** the trust-level gate in every case — both
must pass.  Disabling ABAC (default) leaves the trust gate as sole enforcement.

### Audit / logging

Policy denials are emitted as structured `WARN` log entries (via the `sdn/auth`
logger) containing: `xpub`, `trust_level`, `action`, `schema`,
`classification`, `provider_id`, `reason`, `rule_index`, `remote_addr`, `path`.

When the full audit logger (`internal/audit`) is reachable from the handler
(currently only from `internal/server`), a `PolicyDenied` event should be wired
there; this is deferred to the follow-up work described below.

---

## Roadmap

The following extensions are tracked but out of scope for this initial layer:

1. **Per-topic subscribe enforcement at the pubsub layer** — connect the ABAC
   engine to `internal/pubsub`'s topic join/receive path so that `subscribe`
   actions are evaluated at the libp2p message handler, not just the REST API.

2. **Releasability-by-encryption (multi-enclave)** — classify data at ingest,
   encrypt per-classification with enclave-specific keys, and gate decryption on
   ABAC `read` decisions.  This is the natural follow-on for multi-enclave SDN
   deployments.

3. **Attribute propagation from EPM** — today `org` is an opaque string that
   must be set externally.  A future integration with the EPM identity resolver
   should populate `org` (and potentially `attrs["clearance"]`) automatically
   from the authenticated xpub's identity attestation at session creation time.

4. **Full audit logger integration** — route `PolicyDenied` events through
   `internal/audit.Logger` rather than the package logger, to benefit from the
   tamper-evident hash chain.
