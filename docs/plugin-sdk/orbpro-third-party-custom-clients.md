# OrbPro Third-Party Custom Clients (Non-OrbPro)

This guide is for external clients that do not embed OrbPro runtime code but
still need to interoperate with third-party plugin licensing.

## Minimum interoperability requirements

- Support FlatBuffers parsing/serialization for:
  - `ThirdPartyClientLicenseRequest`
  - `ThirdPartyClientLicenseResponse`
  - `ThirdPartyServerPluginRegistration`
  - `ThirdPartyServerPluginGrant`
- Preserve file identifiers exactly.
- Validate response status and expiration before use.

## Reference generation commands

```bash
npm --prefix packages/plugin-sdk run generate:third-party-bindings
npm --prefix packages/plugin-sdk run generate:third-party-fixtures
```

## Golden vectors

Use fixtures in `packages/plugin-sdk/fixtures/third-party/v1/` to verify your
encoder/decoder implementation before connecting to a live broker.

## Conformance

Run SDK conformance and compare your implementation outputs to fixture hashes:

```bash
npm --prefix packages/plugin-sdk run test:conformance
```
