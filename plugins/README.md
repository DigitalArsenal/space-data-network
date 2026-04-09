# SDN WASM Plugins

Two Crypto++-based WASM modules implementing the SDN `ecies-x25519-hkdf-sha256-aes-256-gcm` artifact encryption scheme.

## Modules

### `plugin-delivery`

Server-side module. Declares libp2p protocol `/sdn/plugin-delivery/1.0.0`.

| Method | Inputs | Output |
|--------|--------|--------|
| `deliver_plugin` | [0] client X25519 pub key (32 bytes), [1] plugin CID (UTF-8) | JSON envelope |
| `get_public_key` | — | server X25519 pub key (32 bytes) |

The server private key is baked at build time (`SDN_SERVER_PRIVATE_KEY_HEX` env var or generated).

### `client-decrypt`

Client-side module. Decrypts envelopes produced by `plugin-delivery`.

| Method | Inputs | Output |
|--------|--------|--------|
| `decrypt_artifact` | [0] JSON envelope (UTF-8), [1] recipient X25519 priv key (32 bytes) | plaintext bytes |

## Build

```bash
# Prerequisites: emcc in PATH, internet access for Crypto++ 8.9.0

# Build both
node plugins/build.mjs

# Build individual
node plugins/build.mjs plugin-delivery
node plugins/build.mjs client-decrypt

# Use local Crypto++ checkout (faster)
CRYPTOPP_SOURCE_DIR=/path/to/cryptopp node plugins/build.mjs

# Supply server private key
SDN_SERVER_PRIVATE_KEY_HEX=<64 hex chars> node plugins/build.mjs plugin-delivery
```

Outputs:
- `plugins/plugin-delivery/dist/plugin-delivery.wasm`
- `plugins/client-decrypt/dist/client-decrypt.wasm`

## Loading

```javascript
import { createModuleRunner } from "@spacedatanetwork/module-runner";
import fs from "node:fs";

const wasmBytes = fs.readFileSync("plugins/client-decrypt/dist/client-decrypt.wasm");
const runner = await createModuleRunner({ wasmSource: wasmBytes });

const result = await runner.invoke("decrypt_artifact", [
  { payload: new TextEncoder().encode(envelopeJson) },
  { payload: recipientPrivKey },
]);
const plaintext = result.outputs[0].payload;
```

## Separate Repositories

These modules are designed to live at:
- `https://github.com/DigitalArsenal/sdn-plugin-delivery`
- `https://github.com/DigitalArsenal/sdn-client-decrypt`

They also appear in the `space-data-network-plugins` registry at
`https://github.com/DigitalArsenal/space-data-network-plugins`.

## Envelope Format

```json
{
  "keyEncryption": {
    "scheme": "ecies-x25519-hkdf-sha256-aes-256-gcm",
    "ephemeralPublicKeyHex": "<64 hex chars>",
    "hkdfSaltB64": "<base64 32 bytes>",
    "wrapIvB64": "<base64 12 bytes>",
    "wrappedKeyB64": "<base64 32 bytes>",
    "wrappedKeyTagB64": "<base64 16 bytes>"
  },
  "contentEncryption": {
    "algorithm": "aes-256-gcm",
    "ivB64": "<base64 12 bytes>",
    "tagB64": "<base64 16 bytes>",
    "ciphertextB64": "<base64>"
  }
}
```
