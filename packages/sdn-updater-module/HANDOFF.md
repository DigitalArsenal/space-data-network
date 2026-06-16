# SDN Updater Module — Handoff Brief

Self-contained brief for a fresh LLM session picking up this work. Assume the
reader has no prior context.

## Repo

`/Users/tj/software/orbpro-stack/repos/main-packages/space-data-network` — Space
Data Network monorepo. Mixed Go/TS/C++. Uses `space-data-module-sdk` for WASM
modules, FlatBuffers for ABI, libp2p for transport, IPFS/Kubo + browser-helia
for storage. Dev wallet trust root at `config/dev-wallet.env`.

## Goal

Build one `space-data-module-sdk`-compliant C++ WebAssembly module —
`org.spacedatanetwork.updater` — that orchestrates updates of the **entire**
SDN stack (kubo, helia, sdn-server Go binary, desktop electron app, plugins)
across all SDN clients. The **same artifact** runs:

- on `sdn.spaceaware.io` granted `wallet_sign` + `ipfs` (add+pin) +
  `pubsub.publish` capabilities → acts as **update publisher** (polls upstream
  sources → builds signed manifest → pins to IPFS → broadcasts on
  `/sdn/updates/v1/<channel>`)
- on every other client (Go server, electron desktop, browser via helia)
  granted only consumer capabilities → acts as **update applier**

Role is determined by capability grants, not a config flag.

## Locked design decisions

| Axis            | Decision                                                                                                    |
|-----------------|-------------------------------------------------------------------------------------------------------------|
| Update scope    | **Whole stack**: kubo, helia, sdn-server, desktop app, plugins                                              |
| Distribution    | **IPFS-first** (libp2p `/space-data-network/module-delivery/1.0.0` + pubsub), **GitHub Releases fallback**  |
| Trust root      | **Reuse admin xpub + Ed25519** from `config/dev-wallet.env` (same key that signs `module-publish/1.0.0`)    |
| Module hosts    | Every SDN client + browser. Browser does live re-import of new updater wasm. Browser bundler consumers get a tiny npm package that **always fetches the latest wasm at runtime** (no version baked in; assume internet) |
| Update server   | **Not a separate Go binary** — the same updater wasm runs on `sdn.spaceaware.io` with publisher capabilities. A small `cmd/sdn-github-mirror/` Go sidecar (≈150 LoC) handles the GitHub Releases mirror leg so the wasm never holds a GitHub PAT |

## Manifest format (already in repo, just needs to be filled out)

`desktop/src/sdn-updater/manifest.js` already defines
`org.spacedatanetwork.update.v1` — Ed25519-signed, with `sequence`, `channel`,
`publishedAt`, `bundle hash`, `wasm hash`. The auto-updater plumbing in
`desktop/src/auto-updater/index.js` is wired but gated off
(`SDN_DESKTOP_AUTO_UPDATES_ENABLED: false` "awaiting update server"). The gap
this project fills is the update server + the C++ module.

Extend it with:

- `updaterWasm: { cid, githubUrl, sha256, version }` — the updater itself, so
  clients can self-bootstrap to a new updater before applying other updates.
- `artifacts: [{ kind, platform, arch, version, cid, githubUrl, sha256, size, deps }]`

## Module's 11 methods (all currently stubs)

- **Consumer:** `checkForUpdates`, `planUpdate`, `fetchArtifact`,
  `verifyArtifact`, `stageArtifact`, `applyStaged`
- **Publisher:** `pollUpstream`, `buildManifest`, `signManifest`,
  `publishManifest`
- **Both:** `selfUpgrade` (detects own wasm is stale, triggers host re-import /
  restart; runs **before** other updates so a broken updater always gets
  replaced first)

`applyStaged` + `selfUpgrade` need a new capability `host_control` that doesn't
exist in `space-data-module-sdk` yet — adding it to the SDK is a separate task.
Each host implements it differently: Go server = restart, electron =
`app.relaunch()`, browser = re-import wasm.

## SDK ABI — IMPORTANT: use the right toolchain

The repo has `node_modules/sdn-emception` (an embedded emscripten-in-wasm npm
package) and `node_modules/space-data-module-sdk`. **Use the SDK's
`compileModuleFromSource` API.** Do not hand-roll wasi-sdk Makefiles. The SDK
auto-generates:

- `plugin_get_manifest_flatbuffer` / `plugin_get_manifest_flatbuffer_size`
  (embedded manifest)
- `plugin_invoke_stream`, `plugin_alloc`, `plugin_free` (canonical ABI exports)
- A dispatch table that calls user-defined C functions matching `methodId`
  names
- Auto-included header `space_data_module_invoke.h` exposing input/output
  helpers

You just write:

```cpp
#include "space_data_module_invoke.h"
#include <string.h>

extern "C" int checkForUpdates(void) {
    // Read input frames via plugin_get_input_count() / plugin_get_input_frame(idx)
    // Push output via plugin_push_output(port_id, schema_name, file_id, bytes, len)
    // Set errors via plugin_set_error(code, message)
    const char* result = "{\"updatesAvailable\":false}";
    plugin_push_output("result", "raw.json", "JSON",
                       (const uint8_t*)result, (uint32_t)strlen(result));
    return 0; // status_code on the response
}
```

Method names must be valid C identifiers (the manifest's methodIds already are).

Compile via a Node script that calls:

```js
import { compileModuleFromSource } from "space-data-module-sdk/compiler";
import { readFile, writeFile, mkdir } from "node:fs/promises";

const manifest = JSON.parse(await readFile("manifest.json", "utf8"));
const sourceCode = await readFile("src/module.cpp", "utf8");
await mkdir("dist/isomorphic", { recursive: true });

const { wasmBytes } = await compileModuleFromSource({
    manifest,
    sourceCode,
    language: "c++",
    outputPath: "dist/isomorphic/module.wasm",
});
console.log(`built ${wasmBytes.length} bytes`);
```

`runtimeTargets: ["BROWSER", "WASMEDGE", "WASI"]` in the manifest defaults to
single-thread, which uses embedded emception — **no system emscripten on PATH
required**.

## What's currently in `packages/sdn-updater-module/`

The first scaffold attempt used the **wrong toolchain** (wasi-sdk + Makefile +
hand-rolled FlatBuffer dispatch + manual manifest embedding). Some files are
correct; most need to go.

### Keep

- `manifest.json` — **correct**. 11 methods with proper input/output ports, 12
  capabilities all marked optional, declares `direct` + `command` invoke
  surfaces, `runtimeTargets: ["WASMEDGE", "BROWSER", "WASI"]`.
- `.gitignore` — fine (`dist/`, `generated/`, `*.wasm`).
- `README.md` — content is mostly right but build instructions reference
  `make`/wasi-sdk; rewrite the "Build" + "Known shortcuts" sections.

### Delete (wrong toolchain / made redundant by SDK auto-generation)

- `Makefile`
- `scripts/build-embedded.mjs` (SDK embeds the manifest itself)
- `src/plugin_entry.cpp` (SDK generates `plugin_invoke_stream` + alloc/free)
- `src/methods.hpp` (SDK generates the dispatch table)
- `src/sdn_host.hpp` (wrong import namespace and old bridge shape — the
  canonical bridge imports `space_data_module_host.call` with the binary
  hostcall envelope plus `response_len` / `read_response` /
  `last_status_code` / `clear_response`; expose only what methods need)
- `src/methods/check_for_updates.cpp`
- `generated/` (whole directory)
- `test/smoke.test.mjs` (assumes wasi-sdk artifact + hand-rolled FlatBuffer
  parsing)

> Note: `rm -rf` from the previous LLM's sandbox hit `Operation not permitted`.
> Delete these by hand or via your editor.

### Write fresh

- `package.json` — npm scripts: `build` (runs `build.mjs`), `check` (runs
  `npx space-data-module check --manifest ./manifest.json --wasm
  ./dist/isomorphic/module.wasm`), `test` (runs `node --test
  test/smoke.test.mjs`).
- `build.mjs` — reads `manifest.json` + `src/module.cpp`, calls
  `compileModuleFromSource({ language: "c++" })`, writes to
  `dist/isomorphic/module.wasm`.
- `src/module.cpp` — single source file with one `extern "C" int
  <methodId>(void)` stub per method. Each stub pushes a JSON
  `{"status":"stub"}` output frame and returns 0.
- `test/smoke.test.mjs` — instantiates the built `.wasm` under Node's `wasi`
  (no real host capabilities needed), verifies exports include
  `plugin_invoke_stream` + `plugin_get_manifest_flatbuffer`, builds a minimal
  `PluginInvokeRequest` (file id `PINQ`) with `method_id: "checkForUpdates"`
  and asserts the response (file id `PINS`) has status_code 0.
- Updated `README.md` Build section: `npm run build`, `npm run check`,
  `npm test`. Drop the wasi-sdk / Makefile instructions.

## Build plan (9 steps; step 1 is what's in progress)

1. **Scaffold the C++ module** — buildable artifact via
   `compileModuleFromSource`, ABI exports correct, all 11 methods stubbed.
   ← Currently broken (wrong toolchain). Cleanup + rewrite needed.
2. Implement `checkForUpdates` + `verifyArtifact` against a hand-crafted,
   hand-signed manifest on a gist (`http` + `crypto_verify` capabilities only).
3. Implement publisher methods (`pollUpstream` → `buildManifest` →
   `signManifest` → `publishManifest`) and test on a dev SDN node with one
   watcher (kubo).
4. Add `host_control` capability to `space-data-module-sdk` (this is a separate
   PR), implement in Go server modulert + electron host + browser host. Wire
   `applyStaged`.
5. Deploy module to `sdn.spaceaware.io` via existing `module-publish/1.0.0`
   protocol — production update server is now live.
6. Build `cmd/sdn-github-mirror/` Go sidecar (~150 LoC: subscribes to pubsub
   topic, copies CIDs to GitHub Releases assets via gh CLI).
7. Browser host + npm package `@spacedatanetwork/updater-runtime` (always-
   latest, fetches wasm at runtime, baked-in admin pubkey for signature
   verification).
8. `selfUpgrade` end-to-end (browser re-import, server/desktop restart).
9. Helia / sdn-server / desktop / plugin watchers in the publisher path.

## Key existing infrastructure to reuse (do not reinvent)

- `desktop/src/sdn-updater/manifest.js` — manifest schema (Ed25519, sequenced).
- `desktop/src/auto-updater/index.js` + `desktop/src/sdn-updater/runtime-feeds.js`
  — auto-updater plumbing gated by `SDN_DESKTOP_AUTO_UPDATES_ENABLED`.
- `desktop/pkgs/macos/build-universal-kubo-binary.js` — pulls kubo from
  `dist.ipfs.tech` per-arch and `lipo`s into universal. Extend this for the
  publisher's kubo watcher.
- `sdn-server/internal/license/publish_protocol.go` —
  `/space-data-network/module-publish/1.0.0` (already authorizes by xpub).
- `sdn-js/src/module-delivery.ts` —
  `/space-data-network/module-delivery/1.0.0` (licensing challenge → IPFS CID →
  decrypt).
- `scripts/seed-orbpro-module-catalog.mjs` — module catalog seeding patterns.
- `config/dev-wallet.env` — admin Ed25519 signing key
  (`SDN_TRACKED_DEV_ADMIN_SIGNING_PUBKEY_HEX`).

## SDK FlatBuffer schemas (for reference)

- `node_modules/space-data-module-sdk/schemas/PluginManifest.fbs` (file id `PMAN`)
- `node_modules/space-data-module-sdk/schemas/PluginInvokeRequest.fbs` (file id `PINQ`)
- `node_modules/space-data-module-sdk/schemas/PluginInvokeResponse.fbs` (file id `PINS`)
- `node_modules/space-data-module-sdk/schemas/TypedArenaBuffer.fbs`

## `space_data_module_host` import ABI (for when methods actually call out)

Imports under module `space_data_module_host` (per SDK README §Host ABI):

- `call(request_ptr, request_len) -> i32`
- `response_len() -> i32`
- `read_response(dst_ptr, dst_len) -> i32`
- `last_status_code() -> i32`
- `clear_response() -> i32`

Capability vocabulary (per SDK README §Host Capabilities) — `clock`, `random`,
`logging`, `timers`, `schedule_cron`, `http`, `tls`, `websocket`, `mqtt`, `tcp`,
`udp`, `network`, `filesystem`, `pipe`, `pubsub`, `protocol_handle`,
`protocol_dial`, `database`, `storage_adapter`, `storage_query`,
`storage_write`, `context_read`, `context_write`, `process_exec`,
`crypto_hash`, `crypto_sign`, `crypto_verify`, `crypto_encrypt`,
`crypto_decrypt`, `crypto_key_agreement`, `crypto_kdf`, `wallet_sign`, `ipfs`,
`scene_access`, `entity_access`, `render_hooks`.

**`host_control` is not in this list yet — adding it is a separate task.**

## Immediate next action

1. Delete the seven files/dirs listed under "Delete" above.
2. Write fresh: `package.json`, `build.mjs`, `src/module.cpp`,
   `test/smoke.test.mjs`, updated `README.md` Build section.
3. Run `npm install` from `packages/sdn-updater-module/` (no new deps needed;
   `space-data-module-sdk` is already a workspace dep — verify with
   `node -e "import('space-data-module-sdk/compiler').then(m =>
   console.log(typeof m.compileModuleFromSource))"`).
4. `npm run build` → produces `dist/isomorphic/module.wasm`.
5. `npm run check` → validates manifest + wasm against SDK compliance checker.
6. `npm test` → smoke test instantiates and dispatches `checkForUpdates`.

Once that's green, step 2 of the build plan (real `checkForUpdates` +
`verifyArtifact`) is the next session.
