# SEC-001 node identity rotation runbook

**Why.** The BIP-39 seed behind `sdn.spaceaware.io` was committed to this public repository
(`sdn-js/verify-identity.mjs`, added in `0e16f8569693`). Everything derived from it is compromised:

- the libp2p identity (the bootstrap/relay peer and the module-delivery provider);
- the Ed25519 publisher key, which was also **the fleet update root** (`d4a971a7e534`);
- the licensing-grant key;
- the sealed-transport and X25519 encryption keys;
- both root-admin sign-in keys;
- the bond chain addresses.

Anyone holding the seed can push signed code to the whole fleet, act as root admin on host-01, mint grants, and
decrypt past sealed admin traffic.

The owner decided on 2026-10-15: **rotate fully now**. The owner generates the new seeds offline. Nobody else ever
sees them. This runbook covers everything else, in order.

> Never run the generator, or type a seed, in a session that records the terminal (`script`, tmux logging, screen
> sharing, an agent session). Never paste a seed into chat, a ticket or a commit.

## What is already in the code (branch `sec001-identity-rotation-20261015`)

| Piece | Where |
|---|---|
| One source of trust anchors | `deployment/identity/anchors.json`. Placeholders until step 2. |
| Derived copies, CI-checked | `sync-anchors.mjs` writes and `--check`s these: `sdn-server/internal/trustanchors/anchors.json` (Go embed), `sdn-js/src/identity-anchors.generated.ts`, `desktop/src/common/identity-anchors.json`, `deployment/release/fleet-trust-roots.json`, `deployment/module-placement.json`, and the marked lines in `deployment/{celestrak/config.yaml,config/servers.yaml,remote/node.yaml.template}` and `sdn-server/deploy/README.md`. |
| Release gate | The bundle builder refuses `--channel release` while any anchor is a placeholder. `publish-{fleet,ui}-update.mjs` refuse to sign without an anchored root. `SDN_IDENTITY_RELEASE=1` makes the anchor tests fail on placeholders (`npm run test:identity-rotation`, `go test ./internal/trustanchors`). |
| Denylist in the node | The connection gater never dials or accepts the old PeerID. Bootstrap and `trusted_peers` drop it. Update roots, manifests and signals signed by the old root are refused. Grants stamped with the old grant key are refused. Module-delivery requests to the old provider are refused. The daemon refuses to start as the old identity, and `key import` refuses to install it. |
| Denylist in sdn-js | Default, regional, env/WASM-supplied and delegated-routing relays naming the old peer are dropped. Provider descriptors and EPMs naming the old key, PeerID or grant key are refused. Grants stamped with the old grant key are refused. |
| Denylist in the desktop | `stage-node-bundle.mjs` refuses a bundle whose `trust/update-roots.json` names the old root. The bundled node enforces the rest. |
| Guards | `scripts/check-no-mnemonics.mjs` scans staged content, every tracked file and git history, in any file type. `scripts/check-no-denylisted-identity.mjs` fails CI on any copy of an old public value. Both run in `scripts/oss-preflight.sh`, which CI runs in its preflight job. |
| Removed | `sdn-js/verify-identity.mjs`. |

## Host table

| Host | Address | Service / config | PeerID today | Action |
|---|---|---|---|---|
| host-01 `sdn.spaceaware.io` (`space-data-network-01`, DO nyc3) | 159.203.150.8 (reserved), 104.131.11.220; Cloudflare in front of `/dns4/sdn.spaceaware.io/tcp/443/wss` | `space-data-network-module-delivery.service`, `--config /etc/space-data-network/config.module-delivery-sidecar.yaml`; keys at `<dirname(storage.path)>/keys/`; update feed `/opt/spacedatanetwork/update-feed` | `16Uiu2HAm1Lbv…Fm45` (**leaked**) | **Re-key** (step 5) |
| host-02 `celestrak.eth` (`space-data-network-02`) | 167.172.219.213 | `sdn-retriever.service`, `/etc/spacedatanetwork/retriever/config.yaml`, `SDN_KEY_PASSWORD_FILE=/etc/spacedatanetwork/retriever/key_password` | `16Uiu2HAmGjaPx…Yr2U` (own seed) | Config + binary only (step 5b) |
| vm-orbit-det-01 | behind relays | user unit, `~/.spacedatanetwork/config.yaml` | `16Uiu2HAmNVJA…Ktwi` (own seed) | Config + binary only (step 5b) |
| Local Mac nodes (`deployment/release/fleet-nodes.json`) | loopback | own identities | own | Update roots + binary (step 3/4) |

## 1. Owner: generate the new seeds (offline)

Generate on an offline machine you control, from a checkout of this branch. You need Node ≥ 24.7 (for built-in
Argon2id). Use the fixed hd-wallet-wasm 2.1.1:

- **npm**: `hd-wallet-wasm@2.1.1`, once published.
- **Branch build** until then: `<hd-wallet-wasm checkout>/wasm/dist/runtime/index.mjs`, built from
  `hd-wallet-size-2-1-1-20261015`. 2.1.0 from npm derives identically; the self-test checks this either way.

```sh
# 0. Rehearse with a throwaway phrase. It prints public values only and writes nothing.
node deployment/identity/new-node-identity.mjs --dry-run --hd-wallet /path/to/hd-wallet-wasm/wasm/dist/runtime/index.mjs

# Disconnect the network now.

# 1. The NODE identity (host-01). --password-file must be the password host-01's daemon
#    uses (the file its SDN_KEY_PASSWORD_FILE names), or omit it and type a strong
#    transport password; see step 5 for which.
node deployment/identity/new-node-identity.mjs \
  --hd-wallet /path/to/hd-wallet-wasm/wasm/dist/runtime/index.mjs \
  --password-file /path/to/host-01-key-password \
  --out-dir ./sec001-node

# 2. The FLEET DISTRIBUTION key (the new update root). This is a SEPARATE phrase, never the
#    node's: you will type it into the Updater page every time you approve a release.
node deployment/identity/new-node-identity.mjs --kind distribution \
  --hd-wallet /path/to/hd-wallet-wasm/wasm/dist/runtime/index.mjs \
  --anchors ./sec001-node/anchors.json \
  --out-dir ./sec001-distribution
```

Each run does the following:

1. Self-tests its derivation against the node's own (`testdata/derivation-vector.json`, written by
   `sdn-server/internal/wasm/rotation_vector_test.go`).
2. Shows the 24 words once. Write them down on paper.
3. Clears the screen and asks you to retype the words, with echo off.
4. Prints only public values.

Outputs:

- `sec001-node/node-identity.backup`: the seed, encrypted (Argon2id + XChaCha20-Poly1305). It is the exact
  format `spacedatanetwork key import --format backup` reads. **Secret at rest; move it only to host-01.**
- `sec001-node/public-identity.json`: public values.
- `sec001-distribution/anchors.json`: public. Once both kinds have run it is complete (`"status": "released"`).
  **Send this file to the repository.**

Keep both phrases offline on paper. host-01's root-admin sign-in follows the new node seed, so load it into the
owner wallet. There is nothing on disk to delete: the generator wrote no plaintext.

**Owner checks (only the owner can do these):**

- Was the phrase ever entered into the Updater page the same as host-01's? If so, the old distribution key is
  compromised too. Step 3 replaces it either way.
- Did celestrak.eth's legacy identity `16Uiu2HAm9oK2…` come from a committed seed? It is retired.
- History holds two checksum-valid 14-word runs in `sdn-server/cmd/spacedatanetwork/main_test.go` at
  `6b6d4323100d` that are not public test vectors. View them privately. If either is a real seed, rotate it and
  add the file to step 10.

## 2. Repo: commit the new anchors

```sh
cp sec001-distribution/anchors.json deployment/identity/anchors.json
node deployment/identity/sync-anchors.mjs                  # rewrites every derived copy
SDN_IDENTITY_RELEASE=1 npm run test:identity-rotation       # placeholders now fail
(cd sdn-server && SDN_IDENTITY_RELEASE=1 ../scripts/go-with-wasmedge.sh test ./internal/trustanchors ./internal/bootstrap ./internal/peers ./internal/update ./internal/deliveryclient)
./scripts/oss-preflight.sh
```

Commit on a task branch, review, and merge. The new PeerID cannot answer until step 5. Nodes built from this
commit therefore bootstrap via celestrak.eth until then, and never via the old identity.

## 3. Move the fleet's update roots (urgent; do it before step 5)

Installed nodes accept only a release signed by a root they already trust, which today is the leaked one. The
release that moves them must therefore be signed with it, and it must carry nothing but that move.

1. Write `new-roots.json` as the `updateRoots` object from the committed `anchors.json`.
2. Build the release binary from the step-2 commit. It carries the denylist and the new anchors.
3. Publish with the explicit, refused-by-default exception:
   ```sh
   node deployment/release/publish-fleet-update.mjs --binary <built daemon> --source-commit <step-2 sha> \
     --key-id d4a971a7e534 --trust-roots new-roots.json \
     --sec001-root-migration "SEC-001: move update roots off the published seed"
   ```
4. Approve it in host-01's Updater page with **the node key**. This is the last thing the old key ever signs, and
   host-01 still holds it until step 5.
5. Exclude host-01 from auto-apply. The new binary refuses to start as the denylisted identity, so host-01 takes
   this binary in step 5.

On every fleet node, `trust/update-roots.json` must now contain only the new root.

Anyone holding the leaked seed can race this with their own `trust_roots` release. Treat any node whose roots are
anything else as compromised: reinstall it from a step-4 bundle.

Then re-sign the update feed. Re-publish the current `cli-bundle/beta/{linux/amd64,darwin/arm64}` and
`ui-bundle/beta/any/any` payloads under the distribution root. Remove the 131 manifests signed with
`d4a971a7e534` from `/opt/spacedatanetwork/update-feed`; new binaries refuse them anyway.

## 4. Release the node and clients

- **sdn GitHub release.** Build with `SDN_IDENTITY_RELEASE=1`. `build-self-contained-cli.mjs --channel release`
  refuses placeholders by itself. Mark v1.0.5-beta.70 and older as superseded: their binaries embed the old root
  and bootstrap peer.
- **npm `@spacedatanetwork/sdn-js`.** Publish the next version, then run
  `npm deprecate @spacedatanetwork/sdn-js@"<=3.1.0" "SEC-001: pins a compromised identity; upgrade"`.
- **Desktop.** Rebuild it from the new bundle. `stage-node-bundle.mjs` refuses an old one.
- **Pages.** Redeploy `spacedatanetwork.org` (sdn `main:/docs`) once `docs/OPERATOR-ENROLLMENT.md` is fixed (see
  "Owed by other lanes").

## 5. Re-key host-01 (owner runs these; nobody else sees the seed)

```sh
# a. Record exactly how the daemon runs: User=, Environment=SDN_KEY_PASSWORD_FILE, --config.
systemctl cat space-data-network-module-delivery.service
#    Run EVERY command below as that User, with that environment. Otherwise the at-rest password
#    differs and the daemon fails closed (it stays down).
unset SDN_MNEMONIC_FILE   # the CLI honours it and the daemon ignores it; an import with it set lands nowhere

CFG=/etc/space-data-network/config.module-delivery-sidecar.yaml
BIN=/opt/spacedatanetwork/bin/spacedatanetwork   # the step-4 release binary

# b. Forensic copy of the OLD identity (it is evidence now, never a restore source).
$BIN key export --format backup -o /root/sec001-old-identity.backup --config $CFG

systemctl stop space-data-network-module-delivery.service
cp -a "$(dirname "$(awk '/^ *path:/{print $2; exit}' $CFG)")/keys" /root/sec001-old-keys   # check the path first

# c. Install the step-4 binary at $BIN.

# d. Import the new identity (it refuses the old one by itself):
#    - if the daemon uses SDN_KEY_PASSWORD_FILE and you generated with that file:
SDN_KEY_PASSWORD_FILE=<that file> $BIN key import --format backup --input node-identity.backup --force --config $CFG
#    - if its at-rest password is machine-derived (no SDN_KEY_PASSWORD_FILE): put the
#      transport password in a 0600 file and run
$BIN key import --format backup --backup-password-file ./transport-password --input node-identity.backup --force --config $CFG
#    then shred the transport-password file and node-identity.backup.

# e. node.key still holds the OLD key (encrypted); the daemon writes a new one on start.
mv "<keys dir>/node.key" /root/sec001-old-keys/node.key.retired

# f. Check public values only.
$BIN show-identity --config $CFG     # NEVER --show-mnemonic
#    PeerID and xpub must equal deployment/identity/anchors.json provider.peerId / provider.xpub.

systemctl start space-data-network-module-delivery.service
```

**Verify publicly**, from anywhere:

```sh
curl -s https://sdn.spaceaware.io/api/v1/id                       | jq -r .peer_id
curl -s https://sdn.spaceaware.io/api/module-delivery/provider    | jq '{peerId, publicKey, grantVerifierPublicKeys}'
curl -s https://sdn.spaceaware.io/api/node/info                   | jq '{peer_id, identity_pubkey_hex, signing_pubkey_hex, encryption_pubkey_hex}'
```

Every value must equal `anchors.json` `provider.*`, and none may equal a `denylist` value. Kubo takes the node
identity, so its PeerID follows on its own.

**Then fix the host-01 configuration:**

- Remove the old PeerID from `trusted_peers` in `config.module-delivery-sidecar.yaml`.
- Remove `0d80e1fd…fc8c` from `SDN_TRUSTED_PUBLISHER_KEYS`.
- **Remove both root admins**: `0d80e1fd…fc8c` and fingerprint `c9e84f37`. Register the new root-admin key, which
  is `provider.rootAdminPublicKey` and follows the new seed in the owner wallet.

### 5b. host-02 and vm-orbit-det-01 (they keep their own identities)

1. Install the step-4 binary.
2. In `network.bootstrap` and `peers.trusted_peers`, replace the old host-01 entry with
   `/ip4/159.203.150.8/tcp/4004/ws/p2p/<new provider.peerId>`. The new binary drops the old entry anyway.
3. Restart.
4. Confirm `trust/update-roots.json` names only the new root.

Never re-key any host with `deployment/remote/sdn-remote-deploy.sh`. It stages `SDN_MNEMONIC_FILE`, which the
daemon never reads, so a first boot on that path generates a fresh identity.

## 6. Republish the records

On host-01, after step 5:

- **EPM.** Run `spacedatanetwork identity wizard`, then publish. The anonymous vCard follows.
- **IPNS** `/ipns/<new PeerID>`.
- **Provider/DHT records.** The discovery CID changes with the key; it is `provider.discoveryCid`.
- **PNM and dataset publications.** Republish the CelesTrak mirror datasets so that peers (host-02, the VM) hold
  records signed by the new key.
- **`/.well-known/sdn/modules.pmm`.** Re-sign the SDN-MODULE-MANIFEST-V1 with the new publisher key, and publish
  the `_sdnkey.sdn.spaceaware.io` DNS TXT record for the NEW key.

## 7. Re-sign, re-issue and re-wrap

**Signed by the old keys:**

- **Update feed:** step 3.
- **modules.pmm:** step 6.
- **Node info signature and chain proofs:** regenerated on start.
- **Licensing and storefront grants (old key `8e2f6db0…`):** re-issue every active grant under the new grant key.
  New clients refuse the old ones. Old clients cannot be stopped from trusting forged ones, which is one more
  reason to ship step 4 fast.

**Encrypted to the old keys:**

- **X25519 wrapping key** (`licensing_bootstrap.go`, `node.go` `EncryptionKey`). Re-seal under the new key:
  - flow secrets sealed for `keyslot.unwrap`;
  - legacy V1/V2 protected plugin bundles without `key_path`;
  - key-broker staged envelopes.

  **Rotate the third-party credentials they protect as well** (Space-Track, cellular), because the old copies are
  decryptable. Catalog entries with their own `key_path` are SEC-006, not this.
- **secp256k1 sealed-transport key `0213dc85…`.** Every past `/api/rpc` admin exchange is decryptable. Rotate any
  secret ever sent over it. Publishers must re-read the node's new sealed-transport key before sealing releases.
- **Grant responses** wrap content keys to the requester, so they are not affected.

## 8. OrbPro (other session's lane)

The OrbPro lane receives the exact edit list in the SEC-001 report:

- descriptor fallback, pinned constants, discovery CID, gallery catalog and tests;
- re-vendor sdn-js;
- rebuild and redeploy `docs/` and gh-pages.

`resolve-pages-provider-descriptor.mjs` compares the live descriptor with its constants, so OrbPro builds fail
until those constants match the new identity. That failure is intended.

spaceaware.io and SpaceAware-UI need the same treatment: `sdnEndpoint.ts`, `node-avatar.js`, the
`celestrakSdnCatalog` shim, and the hashed gh-pages bundles.

## 9. Verify nothing presents or trusts the old identity

```sh
# Repo: no copy outside the anchors, its tests and this runbook
node scripts/check-no-denylisted-identity.mjs
# Live: the old PeerID is answered by nobody
curl -s "https://delegated-ipfs.dev/routing/v1/peers/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45" | jq '.Peers | length'   # 0
curl -s "https://delegated-ipfs.dev/routing/v1/providers/bafkreiceqr2v4fvjqddussy5wydruetwmupxzvlett4ezl3zywg4ndl2di" | jq '.Providers | length'  # 0
# Live endpoints and update feed carry no denylisted value
for u in /api/v1/id /api/node/info /api/module-delivery/provider /api/relay/status /.well-known/sdn/modules.pmm; do
  curl -s "https://sdn.spaceaware.io$u" | grep -c -e 16Uiu2HAm1Lbvwj -e 0d80e1fd5f9a -e 8e2f6db00489 -e d4a971a7e534
done
# Published artifacts: npm latest, the newest GitHub release, Pages (spaceaware.io, OrbPro, spacedatanetwork.org)
npm pack @spacedatanetwork/sdn-js@latest && tar -xzOf spacedatanetwork-sdn-js-*.tgz | grep -c 16Uiu2HAm1Lbvwj   # 0
```

On every fleet node:

- `trust/update-roots.json` names only the new root;
- `config.yaml` has no old entry;
- the new binary is running.

In every other repo (OrbPro, spaceaware.io, SpaceAware-UI, the stack, modules), `git grep` for the denylisted values
finds only historical notes.

## 10. History purge: a SEPARATE owner confirmation

This rewrites shared public history. Every branch SHA changes, and every clone, worktree and session must
re-clone. Gitlinks that pin sdn commits (`spacedatanetwork-stack`, OrbPro's vendored copies) must be re-pinned.
It removes the copies GitHub serves; it does not un-publish the seed. Rotation (steps 1–9) is the actual fix.

**Prepared, not run:**

```sh
# Fresh mirror, outside every working checkout
git clone --mirror git@github.com:DigitalArsenal/space-data-network.git sdn-purge.git
cd sdn-purge.git
git filter-repo --invert-paths \
  --path sdn-js/verify-identity.mjs \
  --path config/dev-wallet.env
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

These are visible in `check-no-denylisted-identity.mjs` output as PENDING:

- `docs/OPERATOR-ENROLLMENT.md:26`, plus the embedded `operator-enrol(l)ment.html` regenerated from it:
  `sdn-security-auth-20261015`.
- `sdn-server/cmd/spacedatanetwork/bond_attestation_test.go:338` (old BTC address): `sdn-security-auth-20261015`.
- `sdn-server/internal/node/node.go` non-HD `node.key` path: refuse a denylisted PeerID there too. Held by
  `sdn-security-auth-20261015`.
- `internal/epm/directory_record.go`, `internal/epm/signature.go` and `internal/pubsub/dataset_updates.go`: refuse
  EPM, directory and pubsub records authored by a denylisted identity even when another peer relays them.
  `module_signature_policy.go`: drop a denylisted key from `SDN_TRUSTED_PUBLISHER_KEYS`. All held by
  `sdn-security-trust-r2-20261015`.
- `.github/workflows/ci.yml`: `fetch-depth: 0` plus `SDN_MNEMONIC_HISTORY_RANGE=${{ github.event.before }}..${{ github.sha }}`
  on push, so CI scans every pushed commit and not just the tip. `beta-release-artifacts.yml`:
  `SDN_IDENTITY_RELEASE=1` on the test and build jobs. Both held by `ci-hardening-space-data-network-20261015`.
- `desktop/src/node/*`: refuse to spawn a bundle trusting a denylisted root (`identity-denylist.js` is ready).
  Held by `sdn-security-auth-20261015`.
- SpaceAware-UI `src/dashboard-tailadmin/data/node-avatar.js:9`. Its built copy in `embedded/dashboard.html` is
  already fixed and its CSP hash recomputed.
