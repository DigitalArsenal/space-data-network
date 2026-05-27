# SDN Updater Module

`org.spacedatanetwork.updater` is a single `space-data-module-sdk`-compliant
WebAssembly module that will orchestrate updates of kubo, helia, the
`sdn-server` binary, the desktop app, and plugins across SDN clients.

The same artifact is loaded:

- on `sdn.spaceaware.io` with publisher capabilities, acting as the update
  publisher;
- on every other SDN client with consumer capabilities, acting as the update
  applier.

Role is determined by granted host capabilities, not by a module-side role flag.

## What's Here

This package is the initial SDK scaffold. `manifest.json` declares the updater
methods, capabilities, runtime targets, and invoke surfaces. `src/module.cpp`
contains one C++ stub per declared method. The SDK compiler generates the
manifest exports, canonical invoke ABI, allocator exports, and dispatch table.

```text
packages/sdn-updater-module/
├── manifest.json
├── package.json
├── build.mjs
├── src/
│   └── module.cpp
├── test/
│   └── smoke.test.mjs
└── dist/isomorphic/module.wasm
```

`dist/` is generated and ignored by git.

## Build

From this package directory:

```bash
npm run build
npm run check
npm test
```

`npm run build` calls `compileModuleFromSource` from
`space-data-module-sdk/compiler` and writes `dist/isomorphic/module.wasm`.
Because the manifest includes the browser runtime target, the SDK selects the
single-thread emception path; no system Emscripten or wasi-sdk install is
required for this scaffold build.

`npm run check` validates `manifest.json` and the generated wasm with the SDK
compliance checker. `npm test` instantiates the wasm under Node's WASI preview1
import, verifies the canonical ABI exports, checks the embedded manifest, and
dispatches every declared updater method through `plugin_invoke_stream`.

## Methods

All methods currently return the JSON stub payload `{"status":"stub"}`.

| methodId           | Role      | Needs caps after stubs are replaced          |
|--------------------|-----------|----------------------------------------------|
| `checkForUpdates`  | consumer  | `http` or `ipfs` or `pubsub`                 |
| `planUpdate`       | consumer  | none                                         |
| `fetchArtifact`    | consumer  | `http`, `ipfs`                               |
| `verifyArtifact`   | consumer  | `crypto_hash`, `crypto_verify`               |
| `stageArtifact`    | consumer  | `storage_write`                              |
| `applyStaged`      | consumer  | `host_control` once the SDK supports it      |
| `selfUpgrade`      | both      | `host_control` once the SDK supports it      |
| `pollUpstream`     | publisher | `http`                                       |
| `buildManifest`    | publisher | `crypto_hash`, `clock`                       |
| `signManifest`     | publisher | `wallet_sign`                                |
| `publishManifest`  | publisher | `ipfs` add/pin and `pubsub.publish`          |

## Known Scaffold Shortcuts

- The method bodies are stubs. Step 2 is to implement `checkForUpdates` and
  `verifyArtifact` against a hand-signed manifest using `http` and
  `crypto_verify`.
- `fetchArtifact` returns the same JSON stub payload on its declared `bytes`
  output port. Real artifact fetching will return verified bytes.
- `applyStaged` and `selfUpgrade` are blocked on a future `host_control`
  capability in `space-data-module-sdk` plus matching Go server, Electron, and
  browser host implementations.
- The publisher path is not implemented yet. `pollUpstream`, `buildManifest`,
  `signManifest`, and `publishManifest` will be filled in after the consumer
  verification path works.

## Next Steps

1. Implement `checkForUpdates` and `verifyArtifact` for a hand-crafted,
   hand-signed manifest.
2. Implement the publisher method chain and test on a dev SDN node with one
   watcher.
3. Add `host_control` to the SDK and host implementations, then wire
   `applyStaged` and `selfUpgrade`.
4. Deploy the updater module to `sdn.spaceaware.io` through the existing
   `/space-data-network/module-publish/1.0.0` path.
