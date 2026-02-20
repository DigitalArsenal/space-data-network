# Third-Party Server Plugin Guide

This guide describes how to build a third-party **server plugin** for licensing
flows on Space Data Network.

## Contract

- Registration message:
  - `orbpro.thirdparty.v1.ThirdPartyServerPluginRegistration`
- Grant response message:
  - `orbpro.thirdparty.v1.ThirdPartyServerPluginGrant`

## Scaffold starter

```bash
npm --prefix packages/plugin-sdk run scaffold:third-party-server -- --name "Acme Server Plugin" --vendor-id acme
```

## Required implementation steps

1. Fill `plugin-manifest.json` with final plugin metadata.
2. Build registration payload with
   `encodeThirdPartyServerPluginRegistration`.
3. Submit registration to broker endpoint or SDN stream.
4. Validate grant with `decodeThirdPartyServerPluginGrant`.
5. Reject non-zero status and expired grants.

## Validation and test

```bash
npm --prefix packages/plugin-sdk run generate:all-bindings
npm --prefix packages/plugin-sdk run test:conformance
```
