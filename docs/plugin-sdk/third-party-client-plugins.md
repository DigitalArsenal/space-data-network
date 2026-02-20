# Third-Party Client Plugin Guide

This guide describes how to build a third-party **client plugin** that requests
user-account scoped license keys.

## Contract

- License request message:
  - `orbpro.thirdparty.v1.ThirdPartyClientLicenseRequest`
- License response message:
  - `orbpro.thirdparty.v1.ThirdPartyClientLicenseResponse`

## Scaffold starter

```bash
npm --prefix packages/plugin-sdk run scaffold:third-party-client -- --name "Acme Client Plugin" --vendor-id acme
```

## Required implementation steps

1. Build request with `encodeThirdPartyClientLicenseRequest`.
2. Include account hash, nonce, ephemeral public key, and challenge token.
3. Submit request to broker endpoint or SDN stream.
4. Decode and validate response with
   `decodeThirdPartyClientLicenseResponse`.
5. Enforce `status === 0`, expected `keyVersion`, and expiration time.

## Validation and test

```bash
npm --prefix packages/plugin-sdk run generate:all-bindings
npm --prefix packages/plugin-sdk run test:conformance
```
