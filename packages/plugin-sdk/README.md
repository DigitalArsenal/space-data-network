# SDN Plugin SDK

This package is the source of truth for OrbPro key-broker and third-party
plugin contracts on
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
- `schemas/orbpro/third-party/v1/ThirdPartyClientLicenseRequest.fbs`
- `schemas/orbpro/third-party/v1/ThirdPartyClientLicenseResponse.fbs`
- `schemas/orbpro/third-party/v1/ThirdPartyServerPluginRegistration.fbs`
- `schemas/orbpro/third-party/v1/ThirdPartyServerPluginGrant.fbs`

Do not maintain parallel or forked schema copies in client/server repos. Update
the schema here, then regenerate bindings.

## Third-Party Plugin Contract (v1)

Third-party plugins are account-scoped and split into two roles:

1. Client plugin flow:
   - `ThirdPartyClientLicenseRequest`
   - `ThirdPartyClientLicenseResponse`
2. Server plugin flow:
   - `ThirdPartyServerPluginRegistration`
   - `ThirdPartyServerPluginGrant`

Reference protocol IDs exported by `src/index.js`:

- `THIRDPARTY_CLIENT_LICENSE_PROTOCOL_ID`
- `THIRDPARTY_SERVER_PLUGIN_PROTOCOL_ID`

## Code Generation (`flatc-wasm`)

Generate plugin SDK JS/TS bindings from local schemas:

```bash
npm run generate:key-broker-bindings
```

Generated outputs:

- `src/generated/orbpro/keybroker/*.ts`
- `src/generated/orbpro/keybroker/*.js`

Generate third-party bindings:

```bash
npm run generate:third-party-bindings
```

Generated outputs:

- `src/generated/orbpro/thirdparty/v1/*.ts`
- `src/generated/orbpro/thirdparty/v1/*.js`
- `src/generated-go/orbpro/thirdparty/v1/*.go`

Generate deterministic fixture vectors:

```bash
npm run generate:third-party-fixtures
```

From the `space-data-network` repo root, regenerate both plugin SDK bindings
and SDN server Go bindings in one step:

```bash
npm run generate:plugin-sdk:key-broker-bindings
```

That command updates:

1. `packages/plugin-sdk/src/generated/orbpro/keybroker/*`
2. `sdn-server/internal/wasiplugin/fbs/orbpro/keybroker/*`

## Third-Party Scaffolding

Generate starter projects for external implementers:

```bash
npm run scaffold:third-party-client -- --name "Example Client Plugin" --vendor-id example
npm run scaffold:third-party-server -- --name "Example Server Plugin" --vendor-id example
```

Templates live in:

- `templates/third-party-client-plugin/`
- `templates/third-party-server-plugin/`

## Mock Broker + Harness

Start a local mock broker:

```bash
node scripts/mock-third-party-broker.mjs --host 127.0.0.1 --port 8899
```

Run both client and server test flows against it:

```bash
node scripts/mock-third-party-plugin-harness.mjs --base-url http://127.0.0.1:8899
```

## Conformance Suite

Run fixture and scaffold conformance checks:

```bash
npm run test:conformance
```

This validates:

1. Golden vectors decode/round-trip correctly.
2. Invalid identifier fixtures fail as expected.
3. Generated client/server scaffold manifests meet minimum contract shape.

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

The artifact is expected to be a single encrypted JSON envelope (not raw
WASM). Required top-level fields:

1. `format` (`orbpro-key-server-artifact-v3`)
2. `path` (`dist/orbpro-licensing-server.sdn.plugin`)
3. `keyEncryption`
4. `contentEncryption`

## OrbPro Release Layout Contract

For OrbPro release staging, SDN integration expects:

1. `Build/OrbPro/<version>/npm`
2. `Build/OrbPro/<version>/licensing-server`

The licensing artifact filename is fixed:

- `Build/OrbPro/<version>/licensing-server/orbpro-licensing-server.sdn.plugin`

`<version>` is the OrbPro SemVer version string (with patch as build counter).

`licensing-server/` must contain only this file for release packaging.

## Key Version Contract (Minor-Based)

OrbPro key version is derived from `MAJOR.MINOR`:

1. `keyVersion = MAJOR * 1000 + MINOR`
2. Patch builds reuse the same key version.

Examples:

1. `1.137.45` -> `1137`
2. `1.137.46` -> `1137`
3. `1.138.1` -> `1138`

Operational policy:

1. Mark current minor key version as `active`
2. Keep previous 11 minor key versions as `grace`
3. Total rolling retention: last 12 minor key versions

## Sandcastle Local Smoke Example

From the OrbPro repo, a local plugin-init smoke path is:

```bash
npm run build:key-server
node scripts/local-sdn-dev.mjs --run npm run start -- --port 8081
```

Then open:

```text
http://localhost:8081/Apps/Sandcastle2/index.html?id=key-broker-single-file
```

Expected milestone in the demo overlay:

1. `Initialize plugins (WASM)` is marked done.

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
2. `ORBPRO_SERVER_PRIVATE_KEY_FILE` (path to 32-byte hex private key file)
3. `DERIVATION_SECRET` (shared secret used by the plugin runtime)
4. `ORBPRO_KEYSERVER_ALLOWED_DOMAINS` (comma-separated local origins)
5. `ORBPRO_KEYSERVER_ACTIVE_KEY_VERSION` (optional; defaults from OrbPro version)

Security note:

1. Do not pass private key material in environment variables.
2. `ORBPRO_SERVER_PRIVATE_KEY_HEX` is forbidden in current runtime config.

Typical local values:

```bash
ORBPRO_KEYSERVER_ALLOWED_DOMAINS=localhost,127.0.0.1
```

If local node-info is unavailable, treat this as an environment setup failure
and fix local SDN startup first before running protocol tests.

## Additional Docs

- `../../docs/plugin-sdk/third-party-schema-policy.md`
- `../../docs/plugin-sdk/third-party-server-plugins.md`
- `../../docs/plugin-sdk/third-party-client-plugins.md`
- `../../docs/plugin-sdk/third-party-custom-clients.md`
