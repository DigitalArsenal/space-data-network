# Building sdn-js with the HD wallet runtime externalised

## From npm: the `./external-wallet` subpath

Every package build emits the externalised form beside the default one, and
the npm package publishes it:

| import | file |
| --- | --- |
| `@spacedatanetwork/sdn-js/external-wallet` | `dist/external-wallet/index.mjs` |
| `@spacedatanetwork/sdn-js/external-wallet/ui` | `dist/external-wallet/ui/index.mjs` |
| `@spacedatanetwork/sdn-js/external-wallet/status` | `dist/external-wallet/status/index.mjs` |

Same API and declarations as `.`, `./ui` and `./status`. The bundles are built
with the committed adapter below, so they obtain the runtime from a provider
the host installs under `globalThis['sdn.hd-wallet-wasm.provider.v1']` (or the
node's staged runtime). `dist/external-wallet/` mirrors `dist/`, including the
`flatsql.wasm` copies, so an embedder can serve the directory as a whole. The
default `dist/` bytes are unchanged by it.

## From source: the one command

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

## Packaging ruling (2026-09-28, supersedes 2026-08-07)

The npm package **ships** the externalised build, as the explicit
`./external-wallet` subpath above.

The 2026-08-07 ruling published only the build target and told embedders to
build from source at the pin they track. The owner's published-dependencies law
(2026-08-21) forbids exactly that: builds consume published packages, and local
checkouts exist to develop and publish from. OrbPro therefore verified the npm
tarball, which inlines the runtime, and its Pages build could not pass. The
coordinator ruled on 2026-09-28 that the law outranks the packaging ruling.

Its two objections are answered this way:

1. *Inert without a provider.* The subpath is opt-in by name; nothing resolves
   to it through `.`, `./ui` or an export condition. Without a provider it
   falls back to the node's same-origin staged runtime and otherwise fails
   closed, naming the staging path.
2. *No single correct artifact.* The published subpath has one contract: the
   documented provider key. An embedder exposes its own runtime under that key
   (OrbPro's engine does, immutably) instead of compiling its contract into
   this package. `SDN_JS_HD_WALLET_ADAPTER` still selects an adapter for a
   from-source `dist/` build; it never changes the published subpath.
