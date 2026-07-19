# flowcc bake toolchain — durable packaging

The flowcc baker (`sdn/flowcc`, `sdn/flowrt`) composes a flow's `runtime.wasm`
by running **emception's `llvm-box.wasm`** (clang + wasm-ld in a single WASM
artifact, ~58 MB) against an **extracted emscripten sysroot** (~36 MB) inside the
node's own WasmEdge. Those two large binaries plus two small template files must
be staged into the node-data **flowcc home** before the bake path turns on:

- `home.Staged()` gates the baker in `plugin/plugins/sdnapi/sdnapi.go:300-342`.
- `POST /api/v1/flows/bake` returns **501** until the home is staged, **200**
  after.
- Home resolution (`flowcc.ResolveHome()`, `sdn/flowcc/nodedata.go:55`):
  `$SDN_FLOWCC_HOME` → `$IPFS_PATH/sdn/flowcc` → `~/.ipfs/sdn/flowcc`.

Historically the box + sysroot existed only in an ephemeral session scratchpad,
so a fresh node / host-01 could not stage deterministically. **This directory
closes that gap.** The large binaries ride a checksummed tarball (kept OUT of
git); only scripts, checksums, docs, and one 2161-byte header live here.

## Home layout (what "staged" means)

```
{home}/
  llvm-box.wasm                         # emception clang+wasm-ld (~58 MB)
  sysroot/                              # extracted emscripten/clang-16 sysroot (~36 MB, 976 files)
    include/…  lib/wasm32-emscripten/…  bin/…
  template/
    flow_runtime.cpp                    # present ONLY to satisfy home.Staged()
    space_data_module_invoke.h          # READ by the baker at every bake
```

The tarball's internal tree is exactly this layout rooted at `flowcc-toolchain/`,
so **extracting it into a resolved home IS staging** — no Go code runs. The
programmatic equivalent is `flowcc.StageToolchain` (used by the bake tests);
`stage-toolchain.sh` lands the identical on-disk layout without a Go build.

## Provenance + checksums (authoritative pins in `SHA256SUMS`)

| component | sha256 | size / count | origin |
|---|---|---|---|
| `llvm-box.wasm` | `7950db61e4cee2e05a6cb9c56785ebc20e126040065c03ea97fa04bbe199486d` | 57 943 729 B | emception `build/emception/packages/wasm.pack`, guest path `/wasm/llvm-box.wasm` |
| `sysroot/` (rollup) | `8224438ea32168faaf295d5146a77b71769fcdd8083b9f9bb60b87bfcecfeb46` | 976 files | emception `build/emception/packages/emscripten.pack` (clang-16 emscripten cache/sysroot) |
| `template/space_data_module_invoke.h` | `9bee46147b255c4591c007b09a825a16fc2abd87df92a43f9314f1b1ea2dd574` | 2161 B | git — every module's `src/cpp/include/space_data_module_invoke.h` (see note) |
| `template/flow_runtime.cpp` | `f1e47fa5a901ac92b1f01d138553ba1e71b4254601b15921319238b430257300` | 35 383 B | git — SDK `src/flow/runtime-src/flow_runtime.cpp` |

The **sysroot rollup** is `sha256( find . -type f | LC_ALL=C sort | xargs
shasum -a256 )` — tar-independent, so it verifies content regardless of how the
tree was archived. It, the box sha, and the two template shas are the
**authoritative integrity gate**; the `TARBALL_SHA256` is a convenience pin
(tar/gzip metadata is not bit-stable across tar implementations, though
`package-toolchain.sh` normalizes mtime + uid/gid for a reproducible archive).

### Note on the invoke header (two committed variants — do not conflate)

`space_data_module_invoke.h` has **two** byte-distinct variants across the module
repos:

- `9bee461…` (2161 B) — sgp4, conjunction-assessment (+2). **This is the staged
  variant** (OD-relevant + the module-structure template).
- `2e99833…` (2794 B) — 8 other modules; a comment-annotated superset that adds
  two host-import declarations (`plugin_set_output_frame_id`,
  `plugin_set_output_stream_frame`). Struct layout is identical (comment-only
  delta).

Staging the `9bee461` variant is safe for **all** flows because the composed
runtime **defines** those two streaming functions itself
(`sdn/flowcc/runtime-src/flow_runtime.cpp:282` and `:289`), so the runtime
exports the streaming ABI regardless of which header a module was compiled
against. This is why the 5 bake tests pass against a `9bee461`-staged home.

## Build the tarball (`package-toolchain.sh`)

```
sdn/flowcc/toolchain/package-toolchain.sh \
  --src <staged-home-dir> \
  [--out ~/.spacedatanetwork/flowcc-toolchain] [--version v1]
```

`--src` is a directory holding `llvm-box.wasm`, `sysroot/`, and
`template/{space_data_module_invoke.h,flow_runtime.cpp}` — a PROVEN staged home
(the one the bake tests passed against) or a home re-extracted from emception
(below). It writes `flowcc-toolchain-<ver>.tar.gz` + `SHA256SUMS` to `--out` and
prints the sums so they can be committed here as the pin.

Default artifact store: `~/.spacedatanetwork/flowcc-toolchain/` — a durable local
dir (same place the project keeps Space-Track creds) that survives session
scratchpad cleanup. It is the local "release artifact"; on deploy it is scp'd to
the target node.

## Stage on a node (`stage-toolchain.sh`)

```
sdn/flowcc/toolchain/stage-toolchain.sh \
  [--home <dir>] \
  [--tarball ~/.spacedatanetwork/flowcc-toolchain/flowcc-toolchain-v1.tar.gz] \
  [--sums <SHA256SUMS>]
```

Resolves the home exactly like `flowcc.ResolveHome()`, verifies the tarball,
extracts into the home, then **asserts the invariants**: box (regular file, sha +
size), sysroot (dir, rollup + file count), `template/flow_runtime.cpp` (regular
file — the `Staged()` gate), and `template/space_data_module_invoke.h` (regular
file + sha — the file the baker READS; a partial extraction without it yields
`Staged()==true` but every bake fails). On success the node's `/api/v1/flows/bake`
returns 200 once that home is the resolved flowcc home.

## Deploy to host-01 (Phase 5 — NOT part of the prep task)

1. Build + verify the tarball locally (above).
2. `scp` `flowcc-toolchain-v1.tar.gz` + `SHA256SUMS` to host-01.
3. On host-01: `stage-toolchain.sh --home /opt/sdn-kubo/repo/sdn/flowcc
   --tarball <path> --sums <path>` (or point `$IPFS_PATH` / `$SDN_FLOWCC_HOME` at
   the node's data dir). Restart `sdn-kubo`; the plugin log flips from
   "bake path DISABLED … 501" to "bake path ENABLED".

## Re-extraction from emception (deterministic recovery path)

If the tarball is lost, rebuild a byte-identical toolchain from emception
(`/Users/tj/software/emception`, or the `sdn-emception` npm package —
`build/emception/`):

- **llvm-box.wasm** lives at guest path `/wasm/llvm-box.wasm`, shipped inside
  `build/emception/packages/wasm.pack` (loaded by `LlvmBoxProcess.mjs` via
  `FS.readFile("/wasm/llvm-box.wasm")`). Unpack `wasm.pack` with emception's
  `wasm-package` tool (`build/emception/wasm-package/`) and extract that path.
  Verify `sha256 == 7950db61…` and size `== 57943729`.
- **sysroot/** is the emscripten cache/sysroot (clang-16 libc++/libc++abi/libc +
  the include tree + `lib/wasm32-emscripten`), shipped inside
  `build/emception/packages/emscripten.pack`. Extract the include/ + lib/ subtree
  the sysroot manifest lists. Verify the rollup `== 8224438e…` and file count
  `== 976`.
- **template files** come straight from git: `flow_runtime.cpp` = SDK
  `src/flow/runtime-src/flow_runtime.cpp`; `space_data_module_invoke.h` = the
  `9bee461` variant committed here under `template/`.

Assemble those four into a `--src` dir and re-run `package-toolchain.sh`; the
content shas will match this `SHA256SUMS` bit-for-bit.
