# SDN Updater

`org.spacedatanetwork.updater` is the update coordinator. Releases reach the
fleet only with the distribution key, which an administrator enters in this
module's page each time. The key is never saved.

## How a release goes out

1. `publish-fleet-update.mjs` (binaries) or `publish-ui-update.mjs` (UI
   packages) seals the release for each node and submits it unsigned to the
   coordinator: `spacedatanetwork update distribute`.
2. In the coordinator's dashboard, open **Updater** under the node's modules.
   Enter the recovery phrase (and passphrase, if any). Sign the release.
3. The publisher uploads the files; the coordinator builds the signal from its
   feed. The page signs the signal with the same key, the coordinator publishes
   it, and the page wipes the key.

The coordinator accepts a signature only if it verifies against its own update
roots and covers exactly the submitted release.

## The key

The SLIP-0010 Ed25519 key at `m/44'/0'/0'/3'/0'` of the phrase's BIP-39 seed:
purpose 3 of the node key grammar, which no node derives. Its key id is the
first 12 hex characters of sha256 over the public key. The key lives in the
module instance between `openKey` and `closeKey`, and the page closes it after
the signal, after 15 minutes without use, or when the page closes.

## Methods

| methodId | Input | Output |
|---|---|---|
| `openKey` | `{phrase, passphrase}` | `{key_id, public_key, path}` |
| `signRelease` | `{kind: "manifest" \| "signal", document}` | the signed document |
| `closeKey` | `{}` | `{closed: true}` |

Signatures are Ed25519 over `DOMAIN || 0x00 || sha256(canonical document)`,
the canonical form Go's `update.CanonicalManifestBytes` writes.

## Build

```sh
npm ci
npm run build
```

`dist/isomorphic/module.wasm` holds the module and its page (`$APP`).
Monocypher 4.0.2 (Ed25519, SHA-512) is vendored in `third_party/monocypher`.

The test is `sdn-server/internal/api/update_distribution_e2e_test.go`: it runs
this module from Go, checks its key against the node's wallet derivation, and
distributes a release through the coordinator routes.
