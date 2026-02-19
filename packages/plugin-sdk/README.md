# SDN Plugin SDK

This package is the source of truth for OrbPro v1.0 key-broker contracts on
Space Data Network. It defines the wire schemas, generated bindings, protocol
IDs, and validation tools used by plugin implementers and node operators.

## OrbPro v1.0 Contract

OrbPro v1.0 key exchange uses libp2p streams with FlatBuffer envelopes only.
There is no legacy transport fallback in this contract.

Required protocols:

1. `/orbpro/public-key/1.0.0`
2. `/orbpro/key-broker/1.0.0`

Required file identifiers:

1. `PublicKeyResponse` -> `OBPK`
2. `KeyBrokerRequest` -> `OBKQ`
3. `KeyBrokerResponse` -> `OBKS`

Discovery flow:

1. Resolve node data from `GET /api/node/info`
2. Select a reachable libp2p listen address from node info
3. Dial `/orbpro/public-key/1.0.0` to fetch server public key
4. Dial `/orbpro/key-broker/1.0.0` to perform request/response exchange

## Schema Source Of Truth

Canonical schemas are versioned in this package:

- `schemas/orbpro/key-broker/PublicKeyResponse.fbs`
- `schemas/orbpro/key-broker/KeyBrokerRequest.fbs`
- `schemas/orbpro/key-broker/KeyBrokerResponse.fbs`

Do not maintain parallel or forked schema copies in client/server repos. Update
the schema here, then regenerate bindings.

## Code Generation (`flatc-wasm`)

Generate plugin SDK JS/TS bindings from local schemas:

```bash
npm run generate:key-broker-bindings
```

Generated outputs:

- `src/generated/orbpro/keybroker/*.ts`
- `src/generated/orbpro/keybroker/*.js`

From the `space-data-network` repo root, regenerate both plugin SDK bindings
and SDN server Go bindings in one step:

```bash
npm run generate:orbpro-key-broker-bindings
```

That command updates:

1. `packages/plugin-sdk/src/generated/orbpro/keybroker/*`
2. `sdn-server/internal/wasiplugin/fbs/orbpro/keybroker/*`

## Runtime Plugin ABI (SDN WASI Host)

For OrbPro key-broker plugins loaded by the SDN WASI runtime, the module must
export:

1. `malloc`
2. `free`
3. `plugin_init`
4. `plugin_get_public_key`
5. `plugin_handle_request`
6. `plugin_get_metadata`

The runtime provides:

1. `wasi_snapshot_preview1.*`
2. `sdn.clock_now_ms`
3. `sdn.random_bytes`

The runtime will call `_initialize` when present before invoking plugin APIs.

OrbPro distribution convention for this plugin binary is:

- `orbpro-licensing-server.sdn.plugin`

## OrbPro Release Layout Contract

For OrbPro release staging, SDN integration expects:

1. `Build/OrbPro/<version>/npm`
2. `Build/OrbPro/<version>/licensing-server`

The licensing artifact filename is fixed:

- `Build/OrbPro/<version>/licensing-server/orbpro-licensing-server.sdn.plugin`

`<version>` is the OrbPro SemVer version string (with patch as build counter).

## Test Client

Run a protocol smoke test against an SDN node:

```bash
npm run test:key-broker-client -- --node-info-url http://127.0.0.1:5010/api/node/info
```

Optional key-broker request test (raw protocol packet wrapped in
`KeyBrokerRequest`):

```bash
npm run test:key-broker-client -- --request-hex 01020304
```

Direct multiaddr override:

```bash
npm run test:key-broker-client -- --multiaddr /ip4/127.0.0.1/tcp/8080/ws/p2p/<peer-id>
```

The test client validates:

1. Node-info discovery and address selection
2. `/orbpro/public-key/1.0.0` decode via `PublicKeyResponse`
3. Optional `/orbpro/key-broker/1.0.0` request/response decode

## Local Development Guidance

For local testing, run an SDN node bound to loopback addresses and point
clients at local `node-info` (for example `127.0.0.1:5001` or `127.0.0.1:5010`).
Do not rely on production endpoints during local plugin bring-up.

Minimum plugin environment for local SDN daemon:

1. `ORBPRO_KEY_BROKER_WASM_PATH` (path to `.sdn.plugin`)
2. `ORBPRO_SERVER_PRIVATE_KEY_HEX` (P-256 private key, 32 bytes hex)
3. `DERIVATION_SECRET` (shared secret used by the plugin runtime)
4. `ORBPRO_KEYSERVER_ALLOWED_DOMAINS` (comma-separated local origins)

Typical local values:

```bash
ORBPRO_KEYSERVER_ALLOWED_DOMAINS=localhost,127.0.0.1
```

If local node-info is unavailable, treat this as an environment setup failure
and fix local SDN startup first before running protocol tests.
