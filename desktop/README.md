# Space Data Network desktop

A desktop shell that runs a Space Data Network node and shows the node's own
dashboard.

The app is thin on purpose. It starts the bundled `spacedatanetwork` daemon,
waits for it to answer, and then loads the dashboard the node serves. The
dashboard, the API, the wallet sign-in assets and the Kubo instance the node
manages all live inside the node bundle, and the node serves them itself; the
shell adds a window, a tray item and a log directory, and nothing else. It
fetches nothing from the network — the only HTTP it makes is a readiness probe
against the node on loopback.

## What it does on launch

1. Finds the bundled node: `<app resources>/sdn-node` when packaged,
   `desktop/assets/sdn-node/` in development, or `$SDN_DESKTOP_BUNDLE_DIR`.
2. Writes `<userData>/node/config.yaml` on first run — store under
   `<userData>/node/data`, keys beside it in `<userData>/node/keys`, the admin
   listener on a free loopback port, authentication as the node defaults it, and
   Kubo left for the node to manage out of the bundle.
3. Runs `spacedatanetwork init` once, if the node has no identity yet.
4. Runs `spacedatanetwork daemon --config <that file>` as a child process and
   polls `GET /api/v1/ready` until it answers 200.
5. Loads the dashboard in the window. Until then the window shows the current
   phase — starting, opening the store, or failed with the last log lines.

Quit sends the node `SIGTERM`, waits up to 30 seconds, and only then kills it.
Closing the window leaves the node running; the tray item quits it.

## Where things live

| | |
| --- | --- |
| node configuration | `<userData>/node/config.yaml` |
| record store | `<userData>/node/data` |
| node identity (encrypted mnemonic) | `<userData>/node/keys` |
| managed Kubo repository | `<userData>/node/kubo` |
| logs | `<userData>/logs/` (`desktop.log`, `node.log`, `error.log`) |

`<userData>` is `~/Library/Application Support/Space Data Network` on macOS,
`%APPDATA%\Space Data Network` on Windows, and
`~/.config/Space Data Network` on Linux.

**To reset the node**, quit the app and delete `<userData>/node`. The next
launch writes a fresh configuration and creates a NEW identity — the node's
peer ID and its keys change, so write the recovery phrase down first
(tray → *Show recovery phrase*, or the *Node* menu). To keep the identity and
throw away only the data, delete `<userData>/node/data` instead.

## Build

The app ships a node bundle, so a build needs one. On an Apple Silicon Mac:

```sh
bash desktop/scripts/build-local.sh
```

That builds the darwin-arm64 node bundle with
`deployment/release/build-local-node-bundle.sh` (the same binary, WasmEdge,
Kubo and wallet-asset steps the release lane runs), stages it into
`desktop/assets/sdn-node/`, and runs electron-builder. The result is
`desktop/dist/space-data-network-desktop-<version>-mac-arm64.dmg` and `.zip`.

With a bundle already built or downloaded:

```sh
node desktop/scripts/stage-node-bundle.mjs --from <bundle-dir-or-archive>
npm --prefix desktop exec -- electron-builder --publish never --mac dmg zip --arm64
```

The staged bundle is gitignored and must never be committed.

Without a signing identity the app is ad-hoc signed by
`pkgs/macos/adhoc-sign.js`, which is what lets it launch on Apple Silicon.
Export `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD` and `APPLE_TEAM_ID` to
notarize; with none of them set, notarization is skipped.

## Develop

```sh
npm --prefix desktop ci
node desktop/scripts/stage-node-bundle.mjs --from <bundle-dir>
npm --prefix desktop start
```

```sh
npm --prefix desktop test   # unit tests, no Electron window
npm --prefix desktop run lint
```

## License

MIT. This app began as a fork of [IPFS Desktop](https://github.com/ipfs/ipfs-desktop)
(© 2019 Protocol Labs, Inc.); see `LICENSE`.
