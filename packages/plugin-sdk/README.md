# SDN Plugin SDK

`@spacedatanetwork/plugin-sdk` v2 is the public contract for Space Data
Network module delivery over:

- `/space-data-network/module-delivery/1.0.0`

This package owns the canonical FlatBuffer schemas, generated bindings,
fixtures, and codec helpers for `space_data_network.module_delivery.v1`.

## Public Contract

The stream uses one typed envelope:

- `ModuleDeliveryMessage`

Supported message variants:

- `GrantRequest`
- `GrantChallenge`
- `GrantProof`
- `GrantResponse`
- `ErrorResponse`

`GrantResponse` carries:

- entitlement or grant status
- capability token
- `BundleDescriptor`
- `WrappedContentKey`

The provider never sends a plaintext content key over the wire.

Requester-side loaders normalize provider identity from a descriptor object or
EPM bytes, but the compressed secp256k1 provider public key remains the trust
root. `cid` and `ipns` are locators only.

The public browser-facing contract does not use:

- `/api/node/info`
- `/orbpro/public-key/1.0.0`
- `/orbpro/challenge/1.0.0`
- `/orbpro/key-broker/1.0.0`

## Schema Source Of Truth

Canonical schemas live under:

- `schemas/space-data-network/module-delivery/v1/ModuleDeliveryMessage.fbs`
- `schemas/space-data-network/module-delivery/v1/ModuleDeliveryMessageType.fbs`
- `schemas/space-data-network/module-delivery/v1/GrantRequest.fbs`
- `schemas/space-data-network/module-delivery/v1/GrantChallenge.fbs`
- `schemas/space-data-network/module-delivery/v1/GrantProof.fbs`
- `schemas/space-data-network/module-delivery/v1/GrantResponse.fbs`
- `schemas/space-data-network/module-delivery/v1/ErrorResponse.fbs`
- `schemas/space-data-network/module-delivery/v1/BundleDescriptor.fbs`
- `schemas/space-data-network/module-delivery/v1/WrappedContentKey.fbs`

Do not maintain forked schema copies in `sdn-js`, `sdn-server`, or OrbPro.

## Code Generation

Generate JS/TS and Go bindings:

```bash
npm run generate:module-delivery-bindings
```

Outputs:

- `src/generated/space-data-network/module-delivery/v1/*.ts`
- `src/generated/space-data-network/module-delivery/v1/*.js`
- `src/generated-go/space_data_network/module_delivery/v1/*.go`

Generate deterministic fixture vectors:

```bash
npm run generate:module-delivery-fixtures
```

Outputs:

- `fixtures/module-delivery/v1/*.hex`
- `fixtures/module-delivery/v1/fixture-manifest.json`

From the `space-data-network` repo root:

```bash
npm run generate:plugin-sdk:module-delivery-bindings
npm run generate:plugin-sdk:module-delivery-fixtures
```

## Conformance

Run the public module-delivery conformance suite:

```bash
npm run test:module-delivery
```

From the repo root:

```bash
npm run test:plugin-sdk:module-delivery
npm run ci:plugin-sdk
```

The conformance suite validates:

1. Golden vectors for every public message payload.
2. Nested descriptor and wrapped-key decode semantics.
3. Corrupt identifier failure handling.
4. Unknown envelope message type failure handling.

## Test Client

Decode a fixture directly:

```bash
npm run test:module-delivery-client -- --fixture grant_response
```

Wrap a fixture payload in the public envelope and decode it again:

```bash
npm run test:module-delivery-client -- --fixture grant_response --wrap
```

## Internal Legacy Tooling

Older OrbPro key-broker, third-party, and generic HTTP/IPFS helper surfaces
still exist for internal compatibility during the wider rollout. They are not
the public `@spacedatanetwork/plugin-sdk` v2 module-delivery contract even
when their helpers remain exported for other in-repo suites.
