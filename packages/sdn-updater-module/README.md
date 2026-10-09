# SDN Updater

`org.spacedatanetwork.updater` is the update coordinator's page. A release
reaches the fleet only when an administrator approves it there: signed with the
coordinator node's own key by default, or with an uploaded key that is never
saved.

## How a release goes out

1. `publish-fleet-update.mjs` (binaries) or `publish-ui-update.mjs` (UI
   packages) seals the release for each node and submits it unsigned to the
   coordinator: `spacedatanetwork update distribute`.
2. In the coordinator's dashboard, open **Updater** under the node's modules
   and approve the release. The node signs it with its key, or, after you
   upload a key file, this module signs it.
3. The publisher uploads the files; the coordinator builds the signal from its
   feed. The page approves the signal the same way, the coordinator publishes
   it, and an uploaded key is wiped.

The coordinator accepts a signature only if it verifies against its own update
roots and covers exactly the submitted release.

## An uploaded key

An Ed25519 private key: a PKCS#8 PEM file (`openssl genpkey -algorithm
ed25519`) or the 32-byte seed as 64 hex characters. It lives in this module's
memory between `openKey` and `closeKey`; the page wipes it after the signal,
after 15 minutes without use, or when the page closes. Nodes accept what it
signs only once it is one of their update roots: publish a release approved
with a trusted key and `--trust-roots roots.json` naming it
(`{"<key id>": "<SPKI base64>"}`; the page shows both).

## Methods

| methodId | Input | Output |
|---|---|---|
| `openKey` | `{key_file}` | `{key_id, public_key}` |
| `signRelease` | `{kind: "manifest" \| "signal", document}` | the signed document |
| `closeKey` | `{}` | `{closed: true}` |

Signatures are Ed25519 over `DOMAIN || 0x00 || sha256(canonical document)`,
the canonical form Go's `update.CanonicalManifestBytes` writes. The key id is
the first 12 hex characters of sha256 over the public key.

## Build

```sh
npm ci
npm run build
```

`dist/isomorphic/module.wasm` holds the module and its page (`$APP`).
Monocypher 4.0.2 (Ed25519, SHA-512) is vendored in `third_party/monocypher`.

The tests are in `sdn-server/internal/api/update_distribution_e2e_test.go`:
they run this module from Go and distribute releases through the coordinator
routes, with the node key and with an uploaded key.
