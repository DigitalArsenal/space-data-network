# Building sdn-js with the HD wallet runtime externalised

## The one command

From a clean checkout of this package:

```sh
npm ci
npm run build:browser-external-wallet
```

That is the whole procedure. It writes the same `dist/` layout as
`npm run build:core`, with `hd-wallet-wasm`'s ~5 MB emscripten runtime replaced
by an adapter that obtains the runtime from the host at run time.

| entry | `build:core` | `build:browser-external-wallet` |
| --- | --- | --- |
| `dist/index.mjs` | ~10.4 MB | ~4.9 MB |
| `dist/ui/index.mjs` | ~18.0 MB | ~7.8 MB |
| `dist/status/index.mjs` | ~5.6 MB | ~70 KB |

Both targets are byte-stable across repeated builds on the same pin, and the
default target's bytes are unchanged by the existence of this mode.

## Why the mode exists

An embedder that already ships its own reviewed copy of `hd-wallet-wasm` must
not have a second full runtime on the page, and enforces that with a
publish-time guard (OrbPro `scripts/hd-wallet-runtime-guard.mjs`). Before this
target existed, the only way to satisfy that guard was for the embedder to
**rewrite `scripts/build-package-entry.mjs` on disk** around the build and
restore it afterwards. That made the passing artifact unreproducible: this
package's `build:core` and the published npm tarball both produce the inlined
form, so a clean checkout could not build what the guard demanded. The mode is
now a committed, named target with a committed adapter.

## Where the runtime comes from at run time

`scripts/wallet/external-hd-wallet-adapter.mjs` resolves the runtime in this
order, and fails closed with the staging path named if neither is available:

1. A provider the host installs before use — `registerHdWalletProvider(runtime)`
   or `globalThis['sdn.hd-wallet-wasm.provider.v1'] = runtime`. An embedding
   engine uses this so its own copy is the only one on the page.
2. The **same-origin** runtime an SDN node stages at
   `/wallet-wasm/runtime/index.mjs` (see the node's `/wallet-wasm/*` handler and
   `deployment/wallet-wasm/stage-wallet-wasm.sh`). Override the URL with
   `globalThis['sdn.hd-wallet-wasm.runtime-url.v1']`.

Only the runtime is externalised. EPM attestation is pure JavaScript over
already-derived key material, so it stays bundled and works with no provider.

## Embedder hooks (no source patching)

| variable | meaning |
| --- | --- |
| `SDN_JS_EXTERNAL_WALLET=1` | build with the runtime externalised (what the named script sets) |
| `SDN_JS_HD_WALLET_ADAPTER` | path to the module that replaces `hd-wallet-wasm`; defaults to the committed adapter |
| `SDN_JS_ESBUILD_PLUGIN_MODULES` | `,`/`:`-separated modules exporting `createEsbuildPlugin({ packageRoot })`, appended after the in-package plugins |

`node scripts/build-package-entry.mjs --external-wallet` is equivalent to the
environment variable.

## Packaging ruling (2026-08-07, Hermes)

The npm package **does not** ship a second, externalised `dist`, and there is no
`dist-external/` export path or export condition.

1. The externalised bundle is inert without a host-installed provider. Shipping
   it behind an export condition would let a consumer resolve, with no
   type-level signal, a bundle that throws at first wallet use.
2. There is no single correct externalised artifact. An embedder's guard
   requires *its* provider contract compiled in; a node UI wants the
   `/wallet-wasm` staging path. One prebuilt tarball variant could serve at most
   one of them.

So the reproducible thing that is published is the **build target and the
adapter contract**, not a second tarball. Consumers that need the externalised
form build it from source at the pin they already track, with one command.
