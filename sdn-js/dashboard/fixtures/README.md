# Dashboard test fixtures

These four files are a VERBATIM COPY of the stack's design fixtures, taken from
`docs/fixtures/ui-dummy-data/` in `spacedatanetwork-stack` on 2026-07-31. That
directory is the SOURCE OF TRUTH; this copy exists because `sdn-js` is a
published package whose tests must run from a standalone checkout, and a test
that reaches out of its own repository is a test that only passes on one machine.

| file | what it is | what it proves here |
|---|---|---|
| `pmm-modules.json` | a `$PMM` provider manifest, 10 modules, every entry state and access policy | statement rebuild, verification failure modes, DATA/FUNCTIONALITY sectioning |
| `pmm-modules.statement.txt` | the canonical `SDN-MODULE-MANIFEST-V1` bytes for that manifest | the rebuild is byte-exact — the one test that would catch a divergence from `internal/pmm/manifest.go` |
| `api-peers.json` | `/api/peers` trust-registry rows, mixed tiers | the registry table's search / trust filter / sort |
| `api-auth-users.json` | `/api/auth/users` operator keys, mixed tiers and provenance | the same, for the operator table |

**They contain no real key material, no real xpubs, no real chain addresses and
no real peer identities** — every identifier carries the correct prefix, length
and character set with obviously fake content (`…EXAMPLE…`). The owner rule
`no-demo-keys-in-prod` (2026-07-29) applies with full force: these are here to be
RENDERED and asserted against, never to be installed into a node, a config, a
keystore or a deployment.

The manifest fixture is deliberately signed with `secp256k1-sha256`, an algorithm
this dashboard does not implement — so it is also the fixture that proves the
verifier refuses what it cannot check instead of quietly passing it.
