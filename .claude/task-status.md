# Protection Plugins — Task Status

## Completed

### Standalone Repos: Build & Test

- [x] Clone `space-data-network-plugin-delivery` to `/Users/tj/software/`
- [x] Clone `space-data-network-plugin-client-decrypt` to `/Users/tj/software/`
- [x] Fix dependency versions (`flatc-wasm@^26.1.29`, `space-data-module-sdk@^0.5.22`) in both repos
- [x] `npm install` both repos
- [x] Build plugin-delivery WASM (213 KB)
- [x] Build client-decrypt WASM (239 KB)
- [x] Add unit tests: `plugin-delivery/test/delivery.test.mjs` (5 tests)
- [x] Add unit tests: `client-decrypt/test/decrypt.test.mjs` (6 tests)
- [x] Add `"test"` script to both `package.json` files
- [x] All unit tests passing in both standalone repos

### Bug Fixes in plugin-delivery C++

- [x] Replace `AutoSeededRandomPool` with `WasiRNG` using WASI `__wasi_random_get` (no `/dev/urandom` in standalone WASM)
- [x] Add `NullRNG` for `get_public_key` (deterministic derivation, no randomness needed)
- [x] Fix FlatBuffer file identifier — `build_error_response` / `build_bytes_response` now use `FinishPluginInvokeResponseBuffer`
- [x] Fix IPFS fetch payload format — send `{"cid":"..."}` instead of `{"path":"/ipfs/..."}`
- [x] Fix IPFS fetch response parsing — parse JSON hostcall envelope, decode base64 bytes from `{"ok":true,"result":{"__type":"bytes","base64":"..."}}`
- [x] Remove `-sFILESYSTEM=0` from plugin-delivery build flags (needs WASI random)

### IPFS Integration Test

- [x] Create `tests/isomorphic/protection-roundtrip.test.mjs` (8 tests)
- [x] Add Helia + blockstore-core + datastore-core to isomorphic test deps
- [x] In-memory Helia node stores test artifact, plugin-delivery fetches via `ipfs.cat` host bridge
- [x] Full round-trip: Helia → plugin-delivery (encrypt) → client-decrypt (decrypt) → verify match
- [x] Cross-validation: JS WebCrypto decrypts C++ Crypto++ envelope
- [x] Cross-validation: C++ WASM decrypts JS WebCrypto envelope
- [x] All 8 integration tests passing

### Source Sync

- [x] Sync fixed C++ sources to `space-data-network/plugins/` (embedded copy)
- [x] Sync fixed C++ sources + build scripts + tests to `space-data-network-plugins/packages/` (monorepo)
- [x] Sync fixed C++ sources to `OrbPro/packages/space-data-network/plugins/` (OrbPro embedded copy)

---

## To Do — OrbPro Migration to space-data-module-sdk

### Remove `packages/plugin-sdk`

- [ ] Identify all files in `packages/plugin-sdk/` (schemas, C++ headers, TS generated types, runtime codec, build scripts)
- [ ] Identify every reference to `plugin-sdk` across OrbPro:
  - [ ] `package.json` dependencies (`"@orbpro/plugin-sdk": "file:../plugin-sdk"`)
  - [ ] JS/TS imports (`from "@orbpro/plugin-sdk/..."`, `from "../plugin-sdk/..."`)
  - [ ] C++ include paths (`packages/plugin-sdk/include/`, `packages/da-flatbuffers/include/`)
  - [ ] Build scripts referencing plugin-sdk paths (CMakeLists.txt, build.js, build.mjs)
  - [ ] `plugins/build.mjs` — `ORBPRO_PLUGIN_SDK_INCLUDE`, `ORBPRO_FLATBUFFERS_INCLUDE`
- [ ] Map plugin-sdk exports to space-data-module-sdk equivalents:
  - [ ] FlatBuffer schemas (PluginInvokeRequest, PluginInvokeResponse, TypedArenaBuffer, PluginManifest, FlowProgram, etc.)
  - [ ] Generated C++ headers (`*_generated.h`)
  - [ ] Generated TS/JS types
  - [ ] Runtime codec (`invoke/codec.js`)
  - [ ] Testing harness

### Update OrbPro Plugin Build Scripts

- [ ] Update `packages/orbpro-plugins/viewshed-shader/build.js` — replace plugin-sdk refs with space-data-module-sdk
- [ ] Update `packages/orbpro-plugins/sgp4/build.js`
- [ ] Update `packages/orbpro-plugins/hpop/build.js`
- [ ] Update `packages/orbpro-plugins/fastest-path/build.js`
- [ ] Update `packages/orbpro-plugins/sensor-shaders/build.js`
- [ ] Update `packages/orbpro-plugins/protection-key-server/build.js`
- [ ] Update `packages/orbpro-plugins/protection-runtime/build.js`
- [ ] Update any CMakeLists.txt files that reference plugin-sdk C++ headers/FlatBuffers

### Update OrbPro Plugin Runtime Code

- [ ] Update `packages/orbpro-plugins/sdnPluginDelivery.js` — any plugin-sdk imports
- [ ] Update `packages/orbpro-plugins/sdkProtectedArtifact.js`
- [ ] Update `packages/orbpro-plugins/protection-key-server/index.js`
- [ ] Update `packages/orbpro-plugins/protection-key-server/invokeRuntime.js`
- [ ] Update `packages/orbpro-plugins/protection-key-server/manifest.js`
- [ ] Update `packages/orbpro-plugins/protection-runtime/index.js`
- [ ] Update all plugin `index.js` files (sgp4, hpop, viewshed-shader, fastest-path, sensor-shaders, oem-stream, sdn-stream)

### Update OrbPro Plugin Tests

- [ ] Update test imports to use space-data-module-sdk codec/harness
- [ ] Verify all existing tests pass after migration
- [ ] Run `npm run build` for all plugins
- [ ] Run `npm test` for all plugins

### Update package.json Dependencies

- [ ] Remove `"@orbpro/plugin-sdk": "file:../plugin-sdk"` from `packages/orbpro-plugins/package.json`
- [ ] Add `"space-data-module-sdk": "^0.5.22"` to `packages/orbpro-plugins/package.json`
- [ ] Remove `packages/plugin-sdk` from OrbPro root `workspaces` array
- [ ] Remove `packages/plugin-sdk` directory
- [ ] Remove any `da-flatbuffers` references if also replaced by space-data-module-sdk

### Verify Full Build

- [ ] `npm install` from OrbPro root
- [ ] `npm run build` in `packages/orbpro-plugins` — all plugins build
- [ ] `npm test` in `packages/orbpro-plugins` — all tests pass
- [ ] Verify `sdnPluginDelivery.js` still works with standalone plugin-delivery WASM
- [ ] Verify protection runtime still works end-to-end
