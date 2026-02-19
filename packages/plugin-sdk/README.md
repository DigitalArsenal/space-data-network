# SDN Plugin SDK

This package contains the OrbPro v1.0 plugin/server protocol contracts and
tooling for third-party plugin/server developers.

## Schema Source Of Truth

Key broker FlatBuffer schemas live here:

- `schemas/orbpro/key-broker/PublicKeyResponse.fbs`
- `schemas/orbpro/key-broker/KeyBrokerRequest.fbs`
- `schemas/orbpro/key-broker/KeyBrokerResponse.fbs`

Any generated bindings in other repositories should be produced from these
files. There is no legacy parallel schema source.

## Generate Bindings

```bash
npm run generate:key-broker-bindings
```

Outputs:

- `src/generated/orbpro/keybroker/*.ts`
- `src/generated/orbpro/keybroker/*.js`

## Test Client

Run a protocol smoke client against an SDN node:

```bash
npm run test:key-broker-client -- --node-info-url http://127.0.0.1:5001/api/node/info
```

Optional request test (sends a raw protocol packet inside `KeyBrokerRequest`):

```bash
npm run test:key-broker-client -- --request-hex 01020304
```

This client verifies:

1. Node-info discovery
2. `/orbpro/public-key/1.0.0` FlatBuffer decode (`OBPK`)
3. Optional `/orbpro/key-broker/1.0.0` FlatBuffer request/response (`OBKQ`/`OBKS`)
