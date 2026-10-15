# SEC-001 node identity rotation runbook

**Why.** The BIP-39 seed behind `sdn.spaceaware.io` (host-01) was committed to this public repository in
`sdn-js/verify-identity.mjs` (pre-purge commits `0e16f8569693` and its sibling `9e21580db908`); the same seed also
sat in `config/dev-wallet.env`, which OrbPro's module publish script fell back to. Both paths were purged from
every branch and tag on 2026-10-15 (step 10), which does not un-publish the seed. Everything derived from it is
compromised:

- the libp2p identity (the bootstrap/relay peer and the module-delivery provider);
- the Ed25519 node signing key `0d80e1fd…` (key id `d4a971a7e534`), which was **also the fleet update root and the
  module publisher key** (stack `deployment/signing.json` `update_feed_root_key`: the feed has been signed by the
  node key since 2026-07-30);
- the licensing-grant key, the sealed-transport and X25519 encryption keys;
- both root-admin sign-in keys (`0d80e1fd…` and the legacy `c9e84f37…`);
- the bond chain addresses (unfunded, no transactions).

Anyone holding the seed could push signed code to the whole fleet (an update signal's `manifest_url` may be any
HTTPS URL), act as root admin on host-01, mint grants, and decrypt past sealed admin traffic.

**Owner decisions, 2026-10-15:**

1. Rotate fully, now. The owner generates every new key offline; nobody else ever sees them.
2. The fleet's update root moves **out-of-band**: each node's `trust/update-roots.json` is replaced directly
   (over SSH on the hosts, locally on the Mac nodes). A release then embeds **only** the new root.
3. **Nothing is ever signed with the leaked key again.** There is no in-band `trust_roots` release; the old
   `--sec001-root-migration` exception is withdrawn and refused.
4. The new update root is a **separate key** from the new node identity: a standalone Ed25519 keypair, not
   derived from any seed.

> Never run a generator, or type a seed or key, in a session that records the terminal (`script`, tmux logging,
> screen sharing, an agent session). Never paste a seed or key into chat, a ticket or a commit.

## What is already in the code (branch `task/sec001-identity-rotation-r2-20261015`)

| Piece | Where |
|---|---|
| One source of trust anchors | `deployment/identity/anchors.json`. Placeholders until step 2. |
| Derived copies, CI-checked | `sync-anchors.mjs` writes and `--check`s the Go embed, `sdn-js/src/identity-anchors.generated.ts`, `desktop/src/common/identity-anchors.json`, `deployment/release/fleet-trust-roots.json`, `deployment/module-placement.json`, and the marked lines in the YAML configs and `sdn-server/deploy/README.md`. |
| Generators (owner-run, offline) | `new-node-identity.mjs` (node identity: an encrypted seed backup) and `new-update-root.mjs` (fleet update root: an Ed25519 PKCS#8 PEM). Both refuse a non-TTY or CI run, refuse an output path inside any git work tree, check every path before a secret exists, and print public values only. |
| Per-node helper (owner-run) | `sec001-node-check.py`: the running binary and its sha256 (path taken from the unit), the trust-roots file in use, every ledger line and `updates/` entry since 2026-07-30, and the out-of-band root swap (`--swap-roots … --apply`). Read-only without `--apply`; never deletes. |
| Release gates | No bundle is staged on **any** channel while its trust roots are placeholders, mislabelled or denylisted (`build-self-contained-cli.mjs`, `desktop/scripts/stage-node-bundle.mjs`). `--channel release` also needs a complete `anchors.json`. Release signing refuses the old key id outright; `--trust-roots` must equal the committed `updateRoots` exactly. `SDN_IDENTITY_RELEASE=1` makes the anchor tests fail on placeholders. |
| Denylist in the node | The connection gater never dials or accepts the old PeerID. The peer registry refuses it any positive trust on every path (config `trusted_peers` in the daemon and the server, the admin API, a persisted registry, which now reads as Never). Bootstrap drops it. Update roots, manifests, signals and `trust_roots` using the old root are refused. Grants stamped with the old grant key and requests to the old provider are refused. The daemon refuses to start as the old identity; `key import` refuses to install it. A node holding a denylisted key gets no update signer (sign-manifest, the Updater page's approve button and signal signing all refuse). A module signed by a denylisted publisher key is never trusted, whatever `SDN_TRUSTED_PUBLISHER_KEYS` says. A persisted account whose xpub or signing key is denylisted (the old root's own row, an enrolled old root key) cannot sign in. Uncompressed secp256k1 forms are denied too. Root sign-in is loopback-only until the re-key (step 0.2). |
| Denylist in sdn-js | Every libp2p node sdn-js builds refuses the old PeerID at dial and after any handshake, inbound or outbound (DHT, delegated routing and pubsub included). Relays naming it are dropped from every source. Provider descriptors and EPMs naming its keys, PeerID, grant key or addresses are refused (JSON and `$EPM` paths), the EPM resolver never resolves or caches it, and the exported `verifyEPMSignature` returns false for it. Keys are matched compressed or uncompressed. |
| Denylist in the desktop | `stage-node-bundle.mjs` refuses a bundle whose roots name the old root or a placeholder. |
| Guards | `scripts/check-no-mnemonics.mjs` scans staged content, every tracked file and git history (every added line, merge-only content, commit and tag messages, whatever the user's diff config), in any file type; words join across any run of non-letters (only NUL or control bytes end a run), a key repeated between the words does not hide a phrase, and separators encoded as escapes, entities, percent-encoding or tags are decoded, as are base64 values and UTF-16 text. `scripts/check-no-denylisted-identity.mjs` fails CI on any copy of an old public value. Both run in `scripts/oss-preflight.sh`, which also blocks tracked `*.backup` / `*.pem` files. |
| Removed | `sdn-js/verify-identity.mjs`; it and `config/dev-wallet.env` are purged from history (step 10). |
| Public advisory (draft) | `deployment/identity/ADVISORY-SEC-001.md`, published in step 4. |

## Host table

| Host | Address | Service / config | Bundle and binary | PeerID today | Action |
|---|---|---|---|---|---|
| host-01 `sdn.spaceaware.io` (`space-data-network-01`, DO nyc3) | 159.203.150.8 (reserved), 104.131.11.220; Cloudflare in front of `/dns4/sdn.spaceaware.io/tcp/443/wss` | `space-data-network-module-delivery.service`, `--config /etc/space-data-network/config.module-delivery-sidecar.yaml`; keys at `<dirname(storage.path)>/keys/`; update feed `/opt/spacedatanetwork/update-feed` | Take it from the unit (`ExecStart`, `MainPID`). The stack's `host-01.sh` runs `/opt/spacedatanetwork/sdn-bundle/bin/spacedatanetwork` with `HOME=/root WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge-0.16.4 LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge-0.16.4/lib64` | `16Uiu2HAm1Lbv…Fm45` (**leaked**) | Roots swap (3), re-key (5) |
| host-02 `celestrak.eth` (`space-data-network-02`) | 167.172.219.213 | `sdn-retriever.service`, `/etc/spacedatanetwork/retriever/config.yaml`, `SDN_KEY_PASSWORD_FILE=/etc/spacedatanetwork/retriever/key_password` | from the unit | `16Uiu2HAmGjaPx…Yr2U` (own seed) | Roots swap (3), binary + config (5b) |
| vm-orbit-det-01 | behind relays | user unit `spacedatanetwork.service`, `~/.spacedatanetwork/config.yaml` | from the unit | `16Uiu2HAmNVJA…Ktwi` (own seed) | Roots swap (3), binary + config (5b) |
| Local Mac nodes `dev`, `ut-austin`, `css-tiangong`, `launch-notices`, `vimpel` (`deployment/release/fleet-nodes.json`) | loopback 7173/7174/7208/7217/7184 | own configs | `ps -o command= -p "$(lsof -ti tcp:<port> -sTCP:LISTEN)"` | own | Roots swap (3), binary (4) |

## 0. Containment: DONE, 2026-10-15 (owner-approved)

Two containments are live while the rest of this runbook runs.

### 0.1 Self-update off on every node (13:13–13:25Z)

Signal-driven self-upgrade is **off** on every node, so nothing signed with the leaked root can install itself.

| Node | Change | Restarted, logs | Running binary sha256 |
|---|---|---|---|
| host-01 | `update: {enabled: false}` in `/etc/space-data-network/config.module-delivery-sidecar.yaml` | yes; `Update signal lane DISABLED` | `5f2ae665276463b1295416ddf942cd4819ed26209ceec111bb9a656a045bdece` (bundle `1.0.6-updatelane.24c0512bc`, installed 2026-10-09) |
| host-02 | same, in `/etc/spacedatanetwork/retriever/config.yaml` | yes; same line | `5f2ae665276463b1295416ddf942cd4819ed26209ceec111bb9a656a045bdece` (same bundle) |
| vm-orbit-det-01 | same, in `~/.spacedatanetwork/config.yaml` | yes; same line | `4c3c82a630a911568e88f1dd9a2a18886332751d9a45bf61d3cf50a785ce2b78` |
| Mac nodes (dormant) | configs patched the same way | not running | — |

**Verify it is still in force** (read-only):

```sh
ssh space-data-network-01 "grep -n -A1 '^update:' /etc/space-data-network/config.module-delivery-sidecar.yaml; \
  journalctl -u space-data-network-module-delivery.service -b | grep -c 'Update signal lane DISABLED'"
ssh space-data-network-02 "grep -n -A1 '^update:' /etc/spacedatanetwork/retriever/config.yaml; \
  journalctl -u sdn-retriever.service -b | grep -c 'Update signal lane DISABLED'"
ssh vm-orbit-det-01 "grep -n -A1 '^update:' ~/.spacedatanetwork/config.yaml; \
  journalctl --user -u spacedatanetwork.service -b | grep -c 'Update signal lane DISABLED'"
```

Each must show `enabled: false` and a count of at least 1 since the current boot of the unit.

**Re-enable `update.enabled` on a node only when all of these hold for that node:**

1. step 0b found nothing unexplained on it;
2. step 3 is done: `sec001-node-check.py` shows its trust-roots file naming **only** the new root's key id;
3. it runs a step-4 binary (one that carries the denylist).

Then set `update: {enabled: true}` in that node's config, restart its unit, and confirm the log no longer says
`Update signal lane DISABLED`. Never re-enable a node whose roots still name `d4a971a7e534`.

### 0.2 Root sign-in on host-01 only from the box itself (live 15:11Z)

host-01's root admin keys (`0d80e1fd…`, `c9e84f37…`) derive from the published seed, so until the re-key the
node admits a root key **only from loopback with no proxy headers**, at challenge, verify and signed request; the
public listener and the sealed `/api/rpc` transport never see a loopback peer. At startup it revokes every
stored root session and root-wallet delegation.

- **Code:** owner-approved hotfix `0de18ab2b` on `hotfix/sec001-host01-root-loopback-20261015`, built on the
  deployed source `32abc438d` (pre-purge `24c0512bc`) for host-01 only; cherry-picked onto this branch so every
  later release carries it (`sdn-server/internal/auth/root_loopback_hotfix.go`).
- **Rollback path on host-01:** `/opt/spacedatanetwork/_rollback-sec001-hotfix-20261015T150958Z` (the outgoing
  binary). Rolling back reopens root sign-in to the network; do it only if the hotfix itself breaks the node,
  and redo 0.1's checks afterwards.
- **Verify** (read-only): the boot log carries `SEC-001 hotfix: the root account's stored sessions are revoked`
  and `root sign-in is loopback-only`; an off-box root challenge gets an authentication failure and logs
  `SEC-001 hotfix: refused a root-key auth challenge`.
  ```sh
  ssh space-data-network-01 "journalctl -u space-data-network-module-delivery.service -b | grep -c 'root sign-in is loopback-only'"
  ```
- The running binary's sha256 changed at 15:11Z: step 0b compares host-01 against the hotfix build, and the
  pre-hotfix value above is the one in the rollback directory.

## 0b. Compromise check, on every node (list, never delete)

Treat every node as possibly compromised since 2026-07-30, when the leaked node key became the update root.

1. **What is running.** Copy the helper and run it read-only; keep the output as evidence.
   ```sh
   scp deployment/identity/sec001-node-check.py space-data-network-01:/root/sec001/
   ssh space-data-network-01 'sudo python3 -I /root/sec001/sec001-node-check.py --unit space-data-network-module-delivery.service' \
     | tee evidence/host-01.check.txt
   ssh space-data-network-02 'sudo python3 -I /root/sec001/sec001-node-check.py --unit sdn-retriever.service' | tee evidence/host-02.check.txt
   ssh vm-orbit-det-01 'python3 -I ~/sec001/sec001-node-check.py --unit spacedatanetwork.service --user' | tee evidence/vm.check.txt
   python3 -I deployment/identity/sec001-node-check.py --bundle <mac node bundle root> | tee evidence/<name>.check.txt
   ```
   (Copy the script to host-02 and the VM the same way first.) The binary's sha256 must equal the value recorded
   in step 0; the script says so or prints `NOT one of the values recorded at containment`. host-01 has run the
   step-0.2 hotfix build since 15:11Z: pass that build's recorded sha256 with `--expect-sha256 <hex>`, and check
   the pre-hotfix binary in `/opt/spacedatanetwork/_rollback-sec001-hotfix-20261015T150958Z` against `5f2ae665…`.

2. **Reproducibility.** Build the recorded commit from source and compare.
   ```sh
   git worktree add /tmp/sdn-24c0512bc 24c0512bcdbb52a4de6359467ffe685b8d58c284
   (cd /tmp/sdn-24c0512bc && docker buildx build --platform linux/amd64 --target builder \
      -f deployment/docker/Dockerfile -t sdn-build:24c0512bc --load .)
   node deployment/release/extract-release-binary.mjs --image sdn-build:24c0512bc \
     --container-path /out/spacedatanetwork --out /tmp/buildout-24c0512bc --arch amd64
   shasum -a 256 /tmp/buildout-24c0512bc        # want 5f2ae665…5bdece
   curl -s https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.6-updatelane.24c0512bc/manifest.json \
     | jq -r .provenance.binary_sha256           # what was published
   ```
   - If all three agree, the host binaries are the published build of our own commit.
   - If the fresh build differs, compare `go version -m` of both (module versions, `vcs.revision`, build
     settings). Toolchain or path differences alone explain a mismatch; any other difference means the node is
     compromised.
   - The VM's binary (`4c3c82a6…`) is a different build: read its version (`<binary> version`) and repeat for that
     commit.

3. **Updates applied or staged since 2026-07-30.** The step-1 output lists every `deploy-ledger.jsonl` line (in
   the bundle), `/var/log/sdn-deploy-lock.ledger`, the retired `/opt/spacedatanetwork/deploy-ledger.log`, and every
   `updates/{staged,incoming,rollback,failed}` entry, with update id, version, signer and source commit. Compare
   each with what we published:
   - our publisher's record on host-01: `/opt/spacedatanetwork/publish-ledger.log`;
   - the public feed index. As captured on 2026-10-15 06:28Z it held 122 `cli-bundle/beta/linux/amd64`, 7
     `darwin/arm64` and 2 `ui-bundle/beta/any/any` manifests, all signed by `d4a971a7e534`; the newest was
     `1.0.6-updatelane.24c0512bc` (sequence 1791578258, 2026-10-09T20:37:38Z).

   Also list the signals the lane acted on:
   ```sh
   ssh space-data-network-01 "journalctl -u space-data-network-module-delivery.service --since 2026-07-30 | grep -i 'update signal'"
   ```
   Flag any `manifest_url` outside `https://sdn.spaceaware.io/updates/`.

4. **Record and decide.** Anything not in our publisher ledger is a finding: write it in the evidence folder and
   **do not delete it**. A node with an unexplained binary, ledger entry, staged update or signal is compromised.
   Rebuild it from a step-4 bundle on a clean install and rotate its own identity as well.

## 1. Owner: generate the keys (offline)

Use an offline machine you control, a checkout of this branch, and removable media **outside every git
checkout**. Both generators refuse an output path inside a work tree.

- You need Node ≥ 24.7 (built-in Argon2id) and hd-wallet-wasm 2.1.1: npm `hd-wallet-wasm@2.1.1` once published,
  or until then the branch build `<hd-wallet-wasm>/wasm/dist/runtime/index.mjs` from
  `hd-wallet-size-2-1-1-20261015`. 2.1.0 derives identically, and the self-test checks this either way.

```sh
HW=/path/to/hd-wallet-wasm/wasm/dist/runtime/index.mjs
OUT=/Volumes/SEC001            # removable media; not inside any git checkout

# Rehearse both: throwaway material, public values only, nothing written.
node deployment/identity/new-node-identity.mjs --dry-run --hd-wallet "$HW"
node deployment/identity/new-update-root.mjs --dry-run

# Disconnect the network now.

# 1a. The NODE identity (host-01). --password-file: the file host-01's daemon reads
#     (SDN_KEY_PASSWORD_FILE), if it has one. Omit it to type a transport password instead (step 5).
node deployment/identity/new-node-identity.mjs --hd-wallet "$HW" --out-dir "$OUT/node" \
  [--password-file /path/to/host-01-key-password]

# 1b. The fleet UPDATE ROOT: a separate, standalone Ed25519 key.
node deployment/identity/new-update-root.mjs --anchors "$OUT/node/anchors.json" \
  --key-out "$OUT/update-root/sdn-update-root-ed25519.pem" --out-dir "$OUT/update-root"
```

**1a** refuses before a phrase exists if `--out-dir` is in a work tree (as written or through any symlink,
dangling or not) or already holds a backup. A dry run that fails removes its throwaway backup. It then:

1. takes the password;
2. shows the 24 words once;
3. clears the screen and asks for a hidden retype.

It writes:

- `node/node-identity.backup`: secret, encrypted, mode 0600;
- `node/public-identity.json`;
- `node/anchors.json`, with the provider filled in.

**1b** refuses before a key exists if `--key-out` is in a work tree or exists. It then generates the key and
checks it:

- it self-tests by signing a synthetic manifest exactly as the Updater page and `sign-update-manifest.mjs` sign;
- it verifies that signature with the fleet's verifier under `{key_id: SPKI}`;
- it refuses a key that is denylisted or equals any node key.

It writes:

- `update-root/sdn-update-root-ed25519.pem`: **secret**, mode 0600 in a 0700 directory, unencrypted PKCS#8 PEM.
  This is the format `sign-update-manifest.mjs --key` and `SDN_UPDATE_SIGNING_KEY_PEM` read. It stays on the
  offline media: releases are signed offline (step 4), never by uploading it to a page.
- `update-root/update-roots.json`: public; it is what every node's trust file becomes in step 3.
- `update-root/public-update-root.json`: public.
- `update-root/anchors.json`: public and complete (`"status": "released"`). **Send this file to the repository.**

Keep the phrase on paper, and the backup and PEM on the offline media. host-01's root-admin sign-in follows the
new node seed, so load it into the owner wallet.

**Owner checks (only the owner can do these):**

- **Resolved by repo evidence (verifier N7):** the old update root *was* host-01's node key.
- Did celestrak.eth's legacy identity `16Uiu2HAm9oK2…` come from a committed seed? It is retired.
- **Resolved (verifier r2 R4):** the two checksum-valid 14-word runs once flagged in
  `sdn-server/cmd/spacedatanetwork/main_test.go` are the public "abandon" ×11 + "about" vector plus one
  adjacent word each; they are allowlisted by fingerprint for that path. Nothing to rotate.

## 2. Repo: commit the new anchors

```sh
cp "$OUT/update-root/anchors.json" deployment/identity/anchors.json
node deployment/identity/sync-anchors.mjs                  # rewrites every derived copy
SDN_IDENTITY_RELEASE=1 npm run test:identity-rotation       # placeholders now fail
(cd sdn-server && SDN_IDENTITY_RELEASE=1 ../scripts/go-with-wasmedge.sh test ./internal/trustanchors ./internal/bootstrap ./internal/peers ./internal/update ./internal/deliveryclient)
./scripts/oss-preflight.sh
```

Commit on a task branch, review, and merge. The new PeerID cannot answer until step 5. Nodes built from this
commit therefore bootstrap via celestrak.eth until then, and never via the old identity.

## 3. Swap the update roots out-of-band, on every node

Nothing is signed with the leaked key. Each node's trust file is replaced directly with
`$OUT/update-root/update-roots.json`, then its unit is restarted so the signal lane loads the new roots. The
helper:

- requires `--anchors`, the committed `anchors.json`, and refuses a file that is not **exactly** its
  `updateRoots` (verifier r2 R2), as well as the published root under any id, a placeholder, or a key under the
  wrong id;
- copies the old file to `--evidence-dir`;
- writes the new one atomically with the old owner and mode, and reads it back by hash.

```sh
R="$OUT/update-root/update-roots.json"
A=deployment/identity/anchors.json      # the COMMITTED anchors (step 2); the helper installs exactly its updateRoots

# host-01 (it keeps its old identity until step 5; only the roots change here)
scp deployment/identity/sec001-node-check.py "$R" "$A" space-data-network-01:/root/sec001/
ssh space-data-network-01 'sudo python3 -I /root/sec001/sec001-node-check.py --unit space-data-network-module-delivery.service \
  --swap-roots /root/sec001/update-roots.json --anchors /root/sec001/anchors.json --evidence-dir /root/sec001/evidence --apply \
  && sudo systemctl restart space-data-network-module-delivery.service'

# host-02
scp deployment/identity/sec001-node-check.py "$R" "$A" space-data-network-02:/root/sec001/
ssh space-data-network-02 'sudo python3 -I /root/sec001/sec001-node-check.py --unit sdn-retriever.service \
  --swap-roots /root/sec001/update-roots.json --anchors /root/sec001/anchors.json --evidence-dir /root/sec001/evidence --apply \
  && sudo systemctl restart sdn-retriever.service'

# vm-orbit-det-01 (user unit)
ssh vm-orbit-det-01 'mkdir -p ~/sec001' && scp deployment/identity/sec001-node-check.py "$R" "$A" vm-orbit-det-01:sec001/
ssh vm-orbit-det-01 'python3 -I ~/sec001/sec001-node-check.py --unit spacedatanetwork.service --user \
  --swap-roots ~/sec001/update-roots.json --anchors ~/sec001/anchors.json --evidence-dir ~/sec001/evidence --apply \
  && systemctl --user restart spacedatanetwork.service'

# each Mac node, locally (restart it however it runs)
python3 -I deployment/identity/sec001-node-check.py --bundle <bundle root> \
  --swap-roots "$R" --anchors "$A" --evidence-dir ~/sec001-evidence/<name> --apply
```

The trust file is `<bundle>/trust/update-roots.json`, or the file `SDN_UPDATE_TRUST_ROOTS` names in the daemon's
environment. The helper reads that from the running process (`/proc/<pid>/environ`), or else from the unit's
`Environment=` and `EnvironmentFile=` files. Re-run the helper with `--anchors` and without `--swap-roots` on each node. It must show
`key ids: <new root key id>` and nothing else, and `MATCHES anchors.json updateRoots`.

`update.enabled` stays `false` until the step-0 conditions hold.

## 4. Release the node and clients, signed by the new root only

**Wait for these lanes first (verifier S9).** The release must carry:

- the non-HD `node.key` guard and the desktop runtime check (`sdn-security-auth-20261015`);
- the refusal of relayed EPM, directory and pubsub records authored by the old identity
  (`sdn-security-trust-r2-20261015`).

Then:

- **Fleet lane: signed OFFLINE (verifier r2 R1).** The update-root PEM never leaves the offline media: it is
  not uploaded to the Updater page, and the node key cannot sign (a node holding a denylisted key gets no
  update signer at all, so the page's approve button and `sign-manifest` refuse).
  1. `publish-fleet-update.mjs --binary <built daemon> --source-commit <step-2 or later sha>`, without
     `--trust-roots`; the nodes already hold the new root. It submits the unsigned release to the coordinator
     and waits.
  2. Copy the waiting manifest out:
     `GET /api/v1/admin/updates/distributions/<id>` → `manifest.json`. Carry it to the offline machine.
  3. Sign it offline:
     ```sh
     node deployment/release/sign-update-manifest.mjs --kind manifest --document manifest.json \
       --key "$OUT/update-root/sdn-update-root-ed25519.pem" --out manifest.signed.json
     ```
     It fills in `signing.key_id` and `signing.public_key` from the key, signs
     `SDN-UPDATE-MANIFEST-V1 || 0x00 || sha256(canonical manifest)` exactly as the node and the fleet verifiers
     check it, and refuses a denylisted key or an already-signed document.
  4. Carry `manifest.signed.json` back and submit it:
     `POST /api/v1/admin/updates/distributions/<id>/manifest`. The coordinator accepts it only if it verifies
     against its (new) roots and covers exactly the submitted release.
  5. Once the files are served, the coordinator builds the signal. Repeat 2–4 for it with `--kind signal`
     (`SDN-UPDATE-SIGNAL-V1`) and `POST …/<id>/signal`, which publishes it.
  Because `update.enabled` is off, install by hand on each node:
  ```sh
  spacedatanetwork update stage --manifest <feed>/manifest.json --carrier <feed>/update.wasm
  spacedatanetwork update apply
  ```
  Each node verifies the release against its new root. Then re-enable `update.enabled` per step 0.
- **sdn GitHub release.** Build with `SDN_IDENTITY_RELEASE=1`, on the offline machine or with the PEM only on
  removable media. Sign the CLI update payloads with `--key "$OUT/update-root/sdn-update-root-ed25519.pem"`.
- **Public security advisory (verifier r2 R3).** Every public build from 2026-08-01 to now — `v1.0.4-beta.18`
  and every `v1.0.5-beta` tag through `v1.0.5-beta.83` — trusts `d4a971a7e534` with `update.enabled: true` by
  default, so anyone holding the seed can push code to third-party installs. Marking them "superseded" does not
  protect anyone. Publish the advisory drafted in `deployment/identity/ADVISORY-SEC-001.md` (GitHub Security
  Advisory, the fixed release's notes, spacedatanetwork.org) once the new root and the fixed release exist,
  filling in its `<…>` values. Then edit each affected release's notes to point at it.
- **npm `@spacedatanetwork/sdn-js`.** Publish the next version, then
  `npm deprecate @spacedatanetwork/sdn-js@"<=3.1.0" "SEC-001: pins a compromised identity; upgrade"`.
- **Desktop.** Rebuild from the new bundle. `stage-node-bundle.mjs` refuses an old one.
- **Pages.** Redeploy `spacedatanetwork.org` (sdn `main:/docs`) once `docs/OPERATOR-ENROLLMENT.md` is fixed (see
  "Owed by other lanes").

## 5. Re-key host-01, in this order (owner runs it; nobody else sees the seed)

Preconditions: step 0b is clean for host-01, step 3 is done on it, and the step-4 binary is built.

```sh
UNIT=space-data-network-module-delivery.service
CFG=/etc/space-data-network/config.module-delivery-sidecar.yaml

# a. Take the binary, user and environment from the unit itself, never from a remembered path (verifier S9).
systemctl cat $UNIT
systemctl show -p User -p Environment -p ExecStart $UNIT
BIN=<the path in ExecStart>
HOSTENV="HOME=/root WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge-0.16.4 LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge-0.16.4/lib64"
#    (use the unit's Environment if it differs). Run EVERY command below as that User, with that environment;
#    otherwise the at-rest password differs and the daemon fails closed (it stays down).
unset SDN_MNEMONIC_FILE   # the CLI honours it, the daemon ignores it: an import with it set lands nowhere

# b. Stop, then install the step-4 binary at $BIN and check its sha256 against the release provenance.
systemctl stop $UNIT

# c. Forensic copy of the OLD identity (evidence, never a restore source), sealed under a transport password
#    so it can be opened off the box:
install -m 600 /dev/null /root/sec001/transport-password && $EDITOR /root/sec001/transport-password
env $HOSTENV $BIN key export --format backup --backup-password-file /root/sec001/transport-password \
  -o /root/sec001/evidence/old-identity.backup --config $CFG
cp -a "$(dirname "$(awk '/^ *path:/{print $2; exit}' $CFG)")/keys" /root/sec001/evidence/old-keys   # check the path first
env $HOSTENV $BIN admin list --config $CFG > /root/sec001/evidence/admins-before.txt              # sign-in keys (public)

# d. Import the new identity (key import refuses the old one by itself):
#    - if the daemon uses SDN_KEY_PASSWORD_FILE and 1a used that file:
env $HOSTENV SDN_KEY_PASSWORD_FILE=<that file> $BIN key import --format backup --input node-identity.backup --force --config $CFG
#    - if its at-rest password is machine-derived: the transport password from 1a in a 0600 file:
env $HOSTENV $BIN key import --format backup --backup-password-file ./transport-password --input node-identity.backup --force --config $CFG
#    then shred node-identity.backup and the transport-password file.

# e. Retire node.key. LOAD-BEARING: if the WASM module ever fails to load, loadOrCreateKey (node.go) falls
#    back to node.key, which still holds the OLD key and (until sdn-security-auth lands) is not denylist-checked.
mv "<keys dir>/node.key" /root/sec001/evidence/node.key.retired

# f. Check public values only (NEVER --show-mnemonic): PeerID and xpub must equal anchors.json provider.*.
env $HOSTENV $BIN show-identity --config $CFG

# g. Root admins: remove BOTH old ones (0d80e1fd… and the legacy c9e84f37…, full value in admins-before.txt),
#    add the new provider.rootAdminPublicKey.
env $HOSTENV $BIN admin remove <old sign-in key> --yes --config $CFG      # twice
env $HOSTENV $BIN admin add --name Owner --source pubkey --pubkey <provider.rootAdminPublicKey> --yes --config $CFG

# h. Config: remove every DENYLISTED value (not only 0d80e1fd) from peers.trusted_peers in $CFG and from
#    SDN_TRUSTED_PUBLISHER_KEYS in the unit environment (topology records 088ac3d8…,d4b97660… there today).

systemctl start $UNIT
```

**Verify publicly with the full denylist** (verifier N5):

```sh
DENY=$(node -e 'const d=require("./deployment/identity/anchors.json").denylist;console.log([...d.peerIds,...d.secp256k1PublicKeys,...d.ed25519PublicKeys,...(d.x25519PublicKeys||[]),...d.xpubs,...d.updateRootKeyIds,...d.discoveryCids,...d.chainAddresses].join("\n"))')
for u in /api/v1/id /api/node/info /api/module-delivery/provider /api/relay/status /.well-known/sdn/modules.pmm; do
  printf '%s ' "$u"; curl -s "https://sdn.spaceaware.io$u" | grep -c -i -F -f <(printf '%s\n' "$DENY")   # 0 each
done
```

Every published identity value must equal `anchors.json` `provider.*`. Kubo takes the node identity, so its PeerID
follows on its own.

**Then, offline:** open the forensic copy and add the two values no public source carries to the denylist: the
old X25519 wrapping key and the full legacy root-admin key (verifier S6).

```sh
node deployment/identity/new-node-identity.mjs --describe-backup old-identity.backup \
  --password-file transport-password --hd-wallet "$HW"
```

Paste the printed `denylist` additions into `anchors.json`, run `sync-anchors.mjs`, and commit. The next release
carries them.

### 5b. host-02 and vm-orbit-det-01 (they keep their own identities)

1. Install the step-4 binary (step 4, by hand).
2. In `network.bootstrap` and `peers.trusted_peers`, replace the old host-01 entry with
   `/ip4/159.203.150.8/tcp/4004/ws/p2p/<new provider.peerId>`. The new binary drops the old entry anyway.
3. Restart. Re-run `sec001-node-check.py`.

Never re-key any host with `deployment/remote/sdn-remote-deploy.sh`. It stages `SDN_MNEMONIC_FILE`, which the
daemon never reads, so a first boot on that path generates a fresh identity.

## 6. Republish the records, in this order (host-01, after step 5)

1. **Provider and DHT record.** The node announces itself on start. The discovery CID changes with the key; it
   must be `provider.discoveryCid`. Check with
   `curl -s "https://delegated-ipfs.dev/routing/v1/providers/<provider.discoveryCid>" | jq '.Providers | length'`.
   The old CID's providers prove nothing either way: the seed holder can announce it anywhere.
2. **EPM.** Run `spacedatanetwork identity wizard`, then publish. The anonymous vCard follows.
3. **IPNS** `/ipns/<new PeerID>`.
4. **`/.well-known/sdn/modules.pmm`.** The node re-signs the SDN-MODULE-MANIFEST-V1 with its new signing key when
   it builds the manifest. Check that its trust anchor names the new PeerID and key.
   - Then **create** the `_sdnkey.sdn.spaceaware.io` DNS TXT record for the NEW key. It does not exist today
     (verifier N8).
   - Clients that cached the old manifest still hold it until 2026-11-14. It names a denylisted peer and key, which
     new clients refuse.
5. **The update feed.** All 131 manifests (122 `cli-bundle/beta/linux/amd64`, 7 `darwin/arm64`, 2
   `ui-bundle/beta/any/any`) are signed by `d4a971a7e534`. **Retire them; do not re-sign them:**
   - Re-signing old bytes with the new root would make it vouch for binaries built before the denylist, inside
     the exposure window.
   - Nodes on the new root already refuse them.

   ```sh
   ssh space-data-network-01 'cd /opt/spacedatanetwork && mkdir -p update-feed-retired-sec001 \
     && cp -a update-feed "update-feed-retired-sec001/snapshot-$(date -u +%Y%m%dT%H%M%SZ)" \
     && python3 -I - <<"PY"
   import json, os, shutil
   feed = "/opt/spacedatanetwork/update-feed"
   for root, dirs, files in os.walk(feed):
       if "manifest.json" in files:
           m = json.load(open(os.path.join(root, "manifest.json")))
           if (m.get("signing") or {}).get("key_id") == "d4a971a7e534":
               dest = root.replace(feed, "/opt/spacedatanetwork/update-feed-retired-sec001/retired", 1)
               os.makedirs(os.path.dirname(dest), exist_ok=True)
               shutil.move(root, dest)
               print("retired", os.path.relpath(root, feed))
   PY'
   ```

   Then publish the current payloads under the new root: the step-4 `cli-bundle/beta` linux/amd64 and
   darwin/arm64 builds, and `ui-bundle` with `publish-ui-update.mjs`. Each publish regenerates its index. If a
   rollback target is ever needed, rebuild it from source and publish it with `--rollback "<why>"`. Never
   re-sign an old artifact.
6. **PNM and dataset publications.** Republish the CelesTrak mirror datasets so that peers (host-02, the VM) hold
   records signed by the new key.

## 7. Re-sign, re-issue and re-wrap

**Signed by the old keys:**

- **Update feed** and **modules.pmm**: step 6.
- **Node info signature and chain proofs**: regenerated on start.
- **Licensing and storefront grants (old key `8e2f6db0…`)**: re-issue every active grant under the new grant key.
  New clients refuse the old ones. Old clients cannot be stopped from trusting forged ones, which is one more
  reason to ship step 4 fast.

**Encrypted to the old keys:**

- **X25519 wrapping key** (`licensing_bootstrap.go`, `node.go` `EncryptionKey`). Re-seal these under the new key:
  - flow secrets sealed for `keyslot.unwrap`;
  - legacy V1/V2 protected plugin bundles without `key_path`;
  - key-broker staged envelopes.

  **Rotate the third-party credentials they protect as well** (Space-Track, cellular): the old copies are
  decryptable. Catalog entries with their own `key_path` are SEC-006, not this.
- **secp256k1 sealed-transport key `0213dc85…`.** Every past `/api/rpc` admin exchange is decryptable, so rotate
  any secret ever sent over it. Publishers must re-read the node's new sealed-transport key before sealing
  releases.
- **Grant responses** wrap content keys to the requester, so they are not affected.

## 8. Other repositories

- **OrbPro** (the other session's lane): the exact edit list is in the SEC-001 report. It covers:
  - the descriptor fallback, pinned constants, discovery CID, gallery catalog and tests;
  - re-vendoring sdn-js;
  - removing `scripts/publish-orbpro-module-catalog.mjs`'s fallback to `config/dev-wallet.env` (:261-262): that
    file held the same leaked seed;
  - rebuilding `docs/` and gh-pages.

  `resolve-pages-provider-descriptor.mjs` compares the live descriptor with its constants, so OrbPro builds fail
  until those constants match the new identity. That failure is intended.
- **SpaceAware-UI** (`sdn-js/spaceaware-ui`; its console files are held by the console lanes): `sdnEndpoint.ts`
  (both pins), `imageryCatalog.ts`, `node-avatar.js`, the `celestrakSdnCatalog` shim, and the `sdnEndpoint` and
  `PnmStream` tests. The built copy in `embedded/dashboard.html` is already fixed.
- **spaceaware.io:** `src/lib/catalog/sdn/sdnEndpoint.ts`, plus pruning the stale hashed gh-pages bundles.
- **spacedatanetwork-stack:** `deployment/topology.json`, `deployment/signing.json` (its PEERID_PROOF gate is
  re-scoped for the rotation) and `deployment/gibs-provider-node/deploy/host-01.sh` `PEER=`, in their own task.

## 9. Verify nothing presents or trusts the old identity

```sh
node scripts/check-no-denylisted-identity.mjs          # repo: no copy outside the anchors, their tests and this runbook
node -e 'process.exit(require("./deployment/identity/anchors.json").denylist.x25519PublicKeys.length ? 0 : 1)'   # step 5 filled it
# Live endpoints: the full-denylist loop in step 5 returns 0 for every path.
npm pack @spacedatanetwork/sdn-js@latest && tar -xzOf spacedatanetwork-sdn-js-*.tgz | grep -c -F -f <(printf '%s\n' "$DENY")   # 0
```

On every fleet node, `sec001-node-check.py` must show:

- the trust file names only the new root;
- the binary is a step-4 build;
- no unexplained ledger or `updates/` entry.

Also check that `config.yaml` has no denylisted value, and that `update.enabled` is back on only where step 0
allows it.

In every other repo (OrbPro, spaceaware.io, SpaceAware-UI, the stack, modules), `git grep` for the denylisted values
finds only historical notes.

The old PeerID can still be announced by whoever holds the seed. "No delegated-routing peers" is informational
only: it proves nothing about the rotation (verifier N5). What matters is that nothing we ship trusts it.

## 10. History purge: DONE 2026-10-15 (owner-ordered)

`git filter-repo` removed `sdn-js/verify-identity.mjs` and `config/dev-wallet.env` from every branch and tag;
every SHA changed (commit map: `~/software/sec001-purge/work.git/filter-repo/commit-map`). The `exposed` lines are
gone from `scripts/mnemonic-scan-allowlist.txt`, its `commit:` entries are remapped through the commit map, and
`node scripts/check-no-mnemonics.mjs --history --strict --all` passes. Still to do from the list below: the
GitHub Support request for cached views and `refs/pull/*`, fork owners, and re-pinning the stack's sdn
gitlinks.

As planned (kept for the record). This rewrites shared public history:

- Every branch SHA changes, and every clone, worktree and session must re-clone.
- Gitlinks that pin sdn commits (`spacedatanetwork-stack`, OrbPro's vendored copies) must be re-pinned.
- It removes the copies GitHub serves; it does not un-publish the seed. Rotation (steps 0–9) is the actual fix.

**Prepared, not run:**

```sh
# Fresh mirror, outside every working checkout
git clone --mirror git@github.com:DigitalArsenal/space-data-network.git sdn-purge.git
cd sdn-purge.git
git filter-repo --invert-paths \
  --path sdn-js/verify-identity.mjs \
  --path config/dev-wallet.env
# The path purge covers both commits that added the seed (0e16f8569693 and 9e21580db908).
# If step 1's owner check found a real seed in sdn-server/cmd/spacedatanetwork/main_test.go,
# add:  --replace-text <owner-local file holding "<phrase>==>REDACTED">   (never commit that file)

# Audit the rewritten history: known exposures must now be gone
git --git-dir=. worktree add ../sdn-purge-check HEAD
(cd ../sdn-purge-check && node scripts/check-no-mnemonics.mjs --history --strict --all)

git push --force --mirror git@github.com:DigitalArsenal/space-data-network.git
```

Afterwards:

- Ask GitHub Support to purge cached views and unreferenced objects, including the `refs/pull/*` heads that a
  push cannot rewrite.
- Ask fork owners to delete or re-fork.
- Remove the `exposed` lines from `scripts/mnemonic-scan-allowlist.txt`.
- Re-pin the stack's sdn gitlinks.

## Owed by other lanes

These show up as PENDING in `check-no-denylisted-identity.mjs` output, or are listed here:

- **`sdn-security-auth-20261015`:**
  - `docs/OPERATOR-ENROLLMENT.md:26` and the embedded `operator-enrol(l)ment.html` regenerated from it;
  - `sdn-server/cmd/spacedatanetwork/bond_attestation_test.go:338` (old BTC address);
  - `sdn-server/internal/node/node.go`: the non-HD `node.key` path must refuse a denylisted PeerID. The daemon's
    `peers.trusted_peers` loop (`upsertConfiguredTrustedPeer`) is already covered by the registry refusal, but
    it still declares a board pin for a denylisted entry; drop that too;
  - `desktop/src/node/*`: refuse to spawn a bundle trusting a denylisted root (`identity-denylist.js` is ready).
- **`sdn-security-trust-r2-20261015`:**
  - `internal/epm/directory_record.go`, `internal/epm/signature.go` and `internal/pubsub/dataset_updates.go`:
    refuse EPM, directory and pubsub records authored by a denylisted identity, even when another peer relays
    them;
  - `module_signature_policy.go`: drop a denylisted key from `SDN_TRUSTED_PUBLISHER_KEYS`.
- **`ci-hardening-space-data-network-20261015`:**
  - `.github/workflows/ci.yml`: `fetch-depth: 0` plus
    `SDN_MNEMONIC_HISTORY_RANGE=${{ github.event.before }}..${{ github.sha }}` on push, so CI scans every pushed
    commit and not just the tip;
  - `beta-release-artifacts.yml`: `SDN_IDENTITY_RELEASE=1` on the test and build jobs.
- **Console lanes (SpaceAware-UI):** the step-8 SpaceAware-UI list.
