# Self-Contained SDN CLI Design

## Summary

Space Data Network needs a downloadable native CLI distribution, similar in
spirit to Kubo, that cleanly encapsulates the SDN runtime on every supported
desktop/server OS. The primary command is `spacedatanetwork`; `sdn` is a
first-class alias. The CLI bundle replaces the unsigned Electron desktop app as
the beta distribution path while keeping browser UI access through the local
daemon.

The release unit is a self-contained OS/architecture archive. It includes the
SDN daemon binary, a bundled Kubo/IPFS executable, SDN UI assets, IPFS WebUI
assets, the SDN updater module, a signed release manifest, and checksums. After
download and extraction, `spacedatanetwork daemon` must be able to run the local
SDN stack without fetching runtime dependencies.

## Goals

- Provide native downloadable archives for macOS, Linux, and Windows.
- Use `spacedatanetwork` as the primary command and `sdn` as an alias.
- Bundle Kubo/IPFS with the SDN runtime instead of requiring a separate install.
- Bundle SDN UI and IPFS WebUI assets so the local daemon serves a complete UI.
- Connect every installation to the signed SDN update channel.
- Update SDN, Kubo, UI assets, updater wasm, and modules from one signed
  manifest.
- Keep the archive simple to inspect, verify, and debug.
- Preserve the existing Go `spacedatanetwork` command and extend it rather than
  replacing it with a new language/runtime.

## Non-Goals

- Do not use Electron as the primary beta distribution artifact.
- Do not require Docker for the main install path.
- Do not download Kubo on first run.
- Do not make a single opaque fat executable that hides all runtime assets.
- Do not enable unsigned or unverified update application.
- Do not remove the Electron app from the repo; it can remain a dev shell and
  can return as a signed desktop distribution after the CLI release path is
  stable.

## Release Artifacts

The release lane publishes one archive per supported OS/architecture:

```text
spacedatanetwork-<version>-darwin-arm64.tar.gz
spacedatanetwork-<version>-darwin-amd64.tar.gz
spacedatanetwork-<version>-linux-amd64.tar.gz
spacedatanetwork-<version>-linux-arm64.tar.gz
spacedatanetwork-<version>-windows-amd64.zip
```

Linux package formats can continue to exist for operators, but the portable
archive is the canonical cross-platform download. The release also publishes
`spacedatanetwork-checksums.txt` and a signed SDN update manifest.

## Bundle Layout

Each archive extracts into a versioned directory:

```text
spacedatanetwork-<version>-<os>-<arch>/
  bin/
    spacedatanetwork
    sdn
  runtime/
    kubo/
      ipfs
    modules/
      org.spacedatanetwork.updater.wasm
    ui/
      sdn/
      webui/
  manifest.json
  checksums.txt
  LICENSE
  README.md
```

On Unix-like platforms, `bin/sdn` is a symlink or tiny launcher that delegates
to `bin/spacedatanetwork`. On Windows, `sdn.exe` is a launcher or copied binary
with identical behavior.

The runtime directory is read-only after installation except during update
staging. Mutable node data stays in the configured SDN data directory, not in
the extracted bundle.

## Command Surface

The CLI exposes these user-facing commands:

```bash
spacedatanetwork init
spacedatanetwork daemon
spacedatanetwork status
spacedatanetwork open
spacedatanetwork config
spacedatanetwork update check
spacedatanetwork update apply
spacedatanetwork version
```

The `sdn` alias supports the same command surface:

```bash
sdn daemon
sdn update check
```

Existing specialized commands such as release verification, module publish, and
dataset import remain available under `spacedatanetwork` unless they conflict
with the user-facing command names above.

## Runtime Ownership

`spacedatanetwork daemon` owns the local runtime stack:

- starts and supervises the bundled Kubo executable;
- starts the SDN node;
- serves the SDN UI at `/`;
- serves IPFS WebUI at `/webui`;
- proxies Kubo RPC and gateway routes when configured;
- loads the SDN updater wasm;
- exposes local status and management APIs for the CLI.

The daemon must not rely on globally installed `ipfs`, Kubo, web UI assets, or
Electron desktop resources. If a config explicitly points at external assets,
that override is allowed, but the default path uses bundled assets.

## Update Model

The bundle participates in the signed SDN update channel through
`org.spacedatanetwork.updater`.

The daemon loads the updater wasm on startup and periodically checks for update
manifests. The preferred update path is SDN/IPFS/pubsub. GitHub Releases remains
the fallback source for clients that are not yet connected to the network.

The signed manifest covers:

- `spacedatanetwork` binary;
- Kubo/IPFS executable;
- SDN UI assets;
- IPFS WebUI assets;
- updater wasm;
- modules and plugins;
- release metadata and compatibility rules.

Update application order:

1. Check update manifest signature and trust root.
2. Run updater `selfUpgrade` first when the updater wasm is stale.
3. Fetch artifact bytes by CID first, GitHub URL fallback second.
4. Verify artifact SHA-256 and manifest signature.
5. Stage the new bundle next to the current bundle.
6. Stop affected child processes.
7. Atomically swap staged bundle to active bundle.
8. Restart the daemon if required.
9. Retain the previous known-good bundle for rollback.

The update subsystem must reject unsigned manifests, expired manifests,
rollback-below-floor manifests, unsupported OS/architecture targets, and
artifacts whose hashes do not match.

## Install Behavior

The install script detects OS and architecture, downloads the correct archive,
verifies checksums, extracts it, and links both commands into the install
directory:

```text
/usr/local/bin/spacedatanetwork
/usr/local/bin/sdn
```

On Windows, the ZIP includes `bin/spacedatanetwork.exe` and `bin/sdn.exe`.
Windows installer work belongs after the portable archive lane is working; the
ZIP must be usable without an installer.

On macOS, CLI binary signing is desirable but not required for the first beta
design. The portable archive path avoids the Electron `.app` Gatekeeper problem
as the primary distribution blocker. If the binary is signed, signing must not
invalidate the signed SDN update manifest or checksums.

## Configuration And Data

The extracted bundle contains runtime code and static assets only. Runtime state
uses existing SDN config and data paths:

- config: existing `config.DefaultPath()` behavior;
- storage: existing `storage.path` behavior;
- Kubo repo: managed under the SDN data root unless configured otherwise;
- update staging: a dedicated staging directory under the SDN data root.

The default config generated by `spacedatanetwork init` should reference bundled
runtime paths through relative bundle discovery, not absolute paths baked at
build time.

## Compatibility

The CLI bundle must run on:

- macOS Apple Silicon;
- macOS Intel;
- Linux amd64;
- Linux arm64;
- Windows amd64.

Each platform package includes only native runtime binaries for that platform.
Cross-architecture universal bundles are not required for this design.

## Security

The SDN release trust root is embedded in the CLI and/or bundled manifest. Kubo
and UI assets are treated as update artifacts under the SDN manifest, not as
untrusted external downloads.

The update channel must provide:

- signed manifests;
- SHA-256 artifact hashes;
- monotonic sequence numbers;
- channel names;
- compatibility ranges;
- rollback floor;
- audit records for checks, staging, application, rollback, and failure.

No downloaded artifact is executed before signature and hash verification pass.

## Testing

Focused tests should cover:

- archive layout for every supported target;
- `spacedatanetwork` and `sdn` alias behavior;
- default bundled Kubo path resolution;
- default UI asset path resolution;
- update manifest parsing and rejection cases;
- staged update layout and atomic swap planning;
- checksum generation and verification;
- release manifest coverage for every artifact in the archive.

End-to-end smoke tests should extract each archive, run `spacedatanetwork
version`, run `sdn version`, and verify the daemon can start with temporary
config and data directories.

## Rollout

Phase 1 creates the portable self-contained archive lane and aliases while
keeping existing Linux packages and Docker artifacts.

Phase 2 wires the bundled Kubo lifecycle into `spacedatanetwork daemon`.

Phase 3 connects the CLI bundle to the signed SDN updater module and enables
check/stage/apply flows behind explicit CLI commands.

Phase 4 enables automatic update checks in the daemon once update staging and
rollback have passed platform smoke tests.
