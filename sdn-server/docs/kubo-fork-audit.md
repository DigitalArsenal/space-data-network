# The in-repo Kubo fork: what ships, what doesn't, and what to do

Audit date: 2026-09-18. Repo HEAD at audit: `050e58da7`.
Every claim below was re-verified in a worktree by running the command shown.
Where this audit corrects an earlier read-only pass, it says so.

---

## The one-sentence finding

**The `kubo/` fork ships nowhere, and has not since a hand-built tarball on
2026-07-28. Every production path downloads stock upstream Kubo v0.39.0 — while
the node reported `kubo_version: "0.40.0-dev"`, a string read from the fork,
which no release has ever contained.**

The version lie is fixed in this change. The fork's fate is an owner decision
and is written up in [Recommendation](#recommendation-drop-the-fork), not acted on.

---

## Q1 — Does the fork ship anywhere? No. Not one build path.

| Path | What it actually uses | Evidence |
|---|---|---|
| `sdn-server` binary | never links the fork | `sdn-server/go.mod` has no `github.com/ipfs/kubo` require and no `replace` |
| Docker image | never copies it | `deployment/docker/Dockerfile` COPYs `sdn-server/`, `webui/`, `config/`, `deployment/` — never `kubo/` |
| Beta release | stock upstream | `.github/workflows/beta-release-artifacts.yml` → `deployment/release/download-kubo.sh` → `https://dist.ipfs.tech/kubo/...` |
| Release deploy | stock upstream | `.github/workflows/release-deploy.yml` (was a hardcoded `v0.39.0` URL) |
| Local bundles | stock upstream | `deployment/release/build-local-node-bundle.sh`, `cut-release-local.sh` |
| Desktop | stock upstream | `desktop/scripts/stage-node-bundle.mjs` stages `runtime/kubo/ipfs` out of that bundle |
| CI | never builds it | `.github/workflows/ci.yml` and `scripts/ci-local.sh` contained **zero** occurrences of "kubo" |

`sdn-kubo` — the fork's own build output name — appears **only inside `kubo/`
itself** (its build scripts and its own README). No deployment, workflow or
release script mentions it:

```
$ grep -rn "sdn-kubo" --include="*.sh" --include="*.yml" --include="*.mjs" --include="*.go" . \
    | grep -v node_modules | grep -v "^./kubo/"
(no output)
```

### The premise behind the original question was inverted

A deployed node does **not** present `kubo/0.39.0` to peers and the accounts
board does **not** drop it. sdn-server is the SDN libp2p host:
`internal/node/node.go` passes `libp2p.UserAgent(versioninfo.AgentVersion)`,
and `AgentVersion = AgentName + "/" + Version()` = `spacedatanetwork/1.0.5`
(`internal/versioninfo/runtime.go`), enforced by `runtime_test.go`. The
2026-07-28 owner rule is already honored by the binary that actually ships.

### The real finding: a second, unaccounted public IPFS peer per box

`sdn-server/cmd/spacedatanetwork/kubo_managed.go` starts
`internal/kubo.Supervisor`, which runs the bundled **stock** `ipfs` as a child:

- `ipfs init --profile server --empty-repo` (`supervisor.go` `ensureRepo`) —
  **no key is imported**, so that repo gets a fresh, distinct peer ID.
- `applyConfig` sets only `Addresses.API`, `Addresses.Gateway`,
  `Gateway.NoFetch`. There is no `Swarm`, `Identity` or `Routing` handling
  anywhere in the file:

  ```
  $ grep -n "Identity\|Swarm\|PeerID\|Routing" sdn-server/internal/kubo/supervisor.go
  (no output)
  ```

- So `Addresses.Swarm` stays at kubo defaults (`kubo/config/init.go`), which
  include `/ip4/0.0.0.0/tcp/4001` and `/ip4/0.0.0.0/udp/4001/quic-v1/webtransport`.
- The `server` profile (`kubo/config/profile.go`) only filters private address
  ranges and disables mDNS/NAT-PMP. It does **not** make the node private.

Result: every SDN box runs a fully public IPFS peer advertising
`kubo/0.39.0/<commit>`, with its own peer ID, that nothing in the SDN accounts
board accounts for. The board stays clean — `internal/epm/sdnpeers.go`
`identifiesAsSDNAgent` filters it out, correctly — so the exposure is silent.
It is attack surface, not a board defect. **Not fixed here** (see
[Not addressed](#not-addressed-deliberately)).

---

## Q2 — Drift

The fork base is a pristine upstream subtree import at
`CurrentVersionNumber = "0.40.0-dev"`. That is a **real upstream string, not a
local bump**: upstream master carries `X.Y.Z-dev` mid-cycle, and
`kubo/docs/changelogs/v0.40.md` is present with *empty* changelog and
contributor sections — the pre-release master state after v0.39.0 was cut.

- Upstream today: **v0.43.1**, released 2026-09-15 (verified against
  `https://dist.ipfs.tech/kubo/versions` and the GitHub releases API).
- The fork base is ~3.5 minors behind.
- **What we actually ship (v0.39.0) is six releases behind**: 0.40.0, 0.40.1,
  0.41.0, 0.42.0, 0.43.0, 0.43.1.
- 107 commits touch `kubo/` (`git rev-list --count HEAD -- kubo/`).

---

## Q3 — What the delta is made of

Only **five** upstream files are modified:

- `kubo/plugin/loader/preload.go` + `preload_list` — registering
  `sdnflag`/`sdnruntime`/`sdnapi` through kubo's own documented preload
  mechanism. This is the "upstream Kubo + SDN bolted on" law honored exactly.
- `kubo/version.go` — the SDN agent identity. **Dead in the shipped
  architecture**: the same expectation is already enforced in sdn-server,
  which is the binary that holds the libp2p identity.
- `kubo/go.mod` / `go.sum` — pulls in go-vcard, gozxing, go-qrcode,
  flatbuffers, WasmEdge-go and the SDS lib.

Everything else is new under `kubo/sdn/**` (330 tracked files) and
`kubo/plugin/plugins/sdn{flag,runtime,api}/`. **Not upstream-mergeable**: it
pulls WasmEdge, flatbuffers and SDS into kubo.

The bulk is **maintained twins of sdn-server**:

| package | kubo files | sdn-server files |
|---|---|---|
| `flowrt` | 30 | 41 |
| `modulert` | 25 | 35 |
| `flatsqlrt` | 15 | 39 |
| `channels` | 3 | 18 |
| `credstore` | 9 | 7 |
| `sigdomain` | 2 | 2 |

The commit log says so outright — `5d72eb9a4` "the kubo **twin** gains
SDN-UPDATE-SIGNAL-V1", `123d0e791` "modulert: the **twins** agree on bundle
scope".

### The two twins flagged as "audit before deleting" are resolved

This was the open question in the earlier pass. Both are clear:

- **`credstore` (9 vs 7)** — the extra files are `crypto.go`, `machine.go`,
  `machine_{darwin,linux,other}.go`, and every one **documents itself as a port
  *from* sdn-server**: *"This is a self-contained port of the sdn-server
  internal/keys machine fingerprint + DeriveDefaultPassword … Ported here (not
  imported from sdn-server) so the kubo module tree carries no dependency on
  sdn-server internals."* The originals live in `sdn-server/internal/keys/`
  (`machine_fingerprint.go`, `derivation.go`, `mnemonic.go`). Direction is
  kubo ← sdn-server. **Nothing is unported.**
- **`sigdomain` (2 vs 2, contents differ)** — the files differ by a **17-line
  comment block and nothing else**:

  ```
  $ diff kubo/sdn/sigdomain/sigdomain.go sdn-server/internal/sigdomain/sigdomain.go | wc -l
  18
  ```

  The code is byte-identical. **Nothing is unported.**

---

## Q4 — Is it a liability? Yes, and it is already being paid

- **Double-entry cost.** Last 60 days: **21** commits to `kubo/` vs **566** to
  `sdn-server/`. `kubo/` last touched 2026-08-16 (`6ef4b6168`);
  `sdn-server/` touched today.
- **The suite reported a version nothing runs.** `generate-suite-version-info.js`
  parsed `kubo/version.go` into `versioninfo.KuboVersion = "0.40.0-dev"` while
  the fleet ran v0.39.0. Its single live consumer is
  `internal/api/coreapi.go` → the `kubo_version` field on `/api/v1/version`,
  which the dashboard header renders. **Fixed in this change.**
- **Dep drift.** kubo vendors SDS lib v1.154.0; sdn-server uses v1.217.0.
- **Security in the version we SHIP.** v0.39.0 pins go-libp2p v0.45.0 /
  quic-go v0.55.0; v0.43.1 pins v0.49.0 / v0.62.0. Verified verbatim against
  upstream `docs/changelogs/v0.43.md`, §"🔒 Security fixes: update recommended":
  - **CVE-2026-57497** WebTransport memory exhaustion — *"Affects any node
    listening on `/quic-v1/webtransport`, which is the default."* Ours is
    (the supervisor never overrides `Addresses.Swarm`).
  - **CVE-2026-40898** HTTP/3 trailer decompression — *"the quic-go v0.60.0 in
    this release includes the fix"*; we ship v0.55.0.
  - libp2p resource caps and a `routing findprovs` data-race daemon crash.
  - **Not exposed:** CVE-2026-46679 (pubsub — the supervisor never enables
    `Pubsub.Enabled`/`Ipns.UsePubsub`, and kubo defaults them off) and
    CVE-2026-39882 (OTLP HTTP trace export, not enabled).
- **The decisive fact**, verbatim from the v0.43 changelog:
  > v0.43 is the last Kubo release with new features from the Shipyard team.
  > **Our IPFS work ends on September 30, 2026.** … After that date, no one at
  > Shipyard maintains Kubo.

  That is **12 days from this audit**.

---

## Corrections to the earlier read-only pass

Three claims did not survive verification. They matter because two of them were
listed as risks.

1. **"`min_kubo_version` enforces a live gate against that fiction" — false
   today.** The gate is inert: `UpdateSubscriberDeps.InstalledKuboVersion` is
   **never set in production**. `NewUpdateSubscriber` has zero non-test callers:

   ```
   $ grep -rn "NewUpdateSubscriber" sdn-server --include="*.go" | grep -v _test.go
   (only its own definition and doc comments)
   $ grep -rn "InstalledKuboVersion:" sdn-server --include="*.go" | grep -v _test.go
   sdn-server/internal/node/update_subscription.go:473: InstalledKuboVersion: s.installedKuboVersion,
   ```

   `assertCompatibility` documents that it skips when the installed version is
   empty, so no published manifest can be wrongly admitted or refused by
   changing `KuboVersion`. This **de-risks** the version fix substantially: the
   only live consumer was an API display field.

2. **"55,893 lines CI has never compiled" — true that CI never compiles it,
   false that it is broken.** With the wrapper fixed, the fork builds clean:

   ```
   $ SDN_GO_MODULE_DIR=kubo ../scripts/go-with-wasmedge.sh build -o /tmp/probe ./cmd/ipfs
   EXIT=0          # 105,088,242-byte binary
   $ /tmp/probe version --all
   Kubo version: 0.40.0-dev
   ```

   The fork is unbuilt, not rotted. That is a weaker argument for deleting it
   than "it no longer compiles" would have been, and the audit should not claim
   the stronger one.

3. **"kubo/dist/ tarballs (~247 MB)" — not present in a clean checkout.**
   `kubo/dist/` is empty here; the tarball is untracked local state in the
   canonical checkout only. `du -sh kubo` = **19 MB**.

### One claim this audit could NOT settle

Two source comments assert the fork **is deployed**:

- `sdn-server/internal/node/module_signature_policy.go`: *"Both binaries run on
  host-01."*
- `kubo/sdn/sigdomain/sigdomain.go`: the same claim, as the reason the twins
  must stay byte-identical.
- `kubo/sdn/flowcc/toolchain/README.md` documents a host path
  `/opt/sdn-kubo/repo/...` and says to restart `sdn-kubo`.

Nothing in this repo can ship a binary there — no CI lane, no release script,
no Dockerfile. So either the claim is stale, or a **hand-built, unmanaged
binary from 2026-07-28 is running on host-01 from a tree no CI compiles**. The
second possibility is worse than the first, and it cannot be resolved from the
repo. **Verify on the host before deleting `kubo/`:**

```
ssh host-01 'ls -la /opt/sdn-kubo/ 2>/dev/null; pgrep -af sdn-kubo; ipfs id 2>/dev/null | head'
```

These comments were left in place rather than "corrected", because asserting
the opposite would be a guess.

---

## Recommendation: drop the fork

**Not ship it, not upstream it.** This is an owner decision and is NOT acted on
in this change.

- **Why not ship:** adopting it means taking on a tree CI has never compiled,
  on a base whose upstream stops being maintained in 12 days, duplicating six
  packages sdn-server already owns.
- **Why not upstream:** the payload is SDN-specific (WasmEdge, flatbuffers,
  SDS) and upstream is closed to features.
- **Why dropping is safe:** nothing regresses, because nothing ships it — and
  its one production-relevant behavior (the SDN agent string) is already
  implemented and tested in the binary that does ship
  (`internal/versioninfo/runtime.go` + `runtime_test.go`). Deleting `kubo/`
  does not abandon the 2026-07-28 owner ruling.
- **Preserve it as a tag** — e.g. `kubo-fork-final` at `6ef4b6168` — matching
  the `control-plane-pre-reset-2026-09-01` precedent in CLAUDE.md.
- **Precondition:** settle the host-01 question above first.

### The separate, larger decision

Who maintains the IPFS layer after **2026-09-30**? Dropping the fork does not
answer this, and neither does shipping it. v0.43.1 is very likely the last
version adoptable from a maintained upstream. This deserves its own task.

---

## The JS half has the same disease

The user asked about Helia too. It is the browser-side counterpart and it is
also stale — measured against the npm registry on 2026-09-18:

| package | `sdn-js` pins | resolved in `sdn-js/package-lock.json` | latest |
|---|---|---|---|
| `helia` | `^6.0.22` | **6.0.22** (published 2026-03-18) | **7.1.12** (2026-09-14) |
| `libp2p` | (transitive) | **1.9.4** | **3.3.11** (2026-09-02) |
| `@helia/unixfs` | `^7.1.0` | 7.1.0 | **8.0.7** (2026-09-14) |

A full major behind on Helia and **two** majors behind on js-libp2p, six months
stale. Helia is a Shipyard project too, so the 2026-09-30 sunset question
applies to both halves of the IPFS stack, not just Kubo. Not changed here —
a major bump of the browser p2p stack is its own task with its own testing.

---

## What this change actually did

Landed (low-risk, all verified):

1. **One source of truth for the shipped Kubo version.**
   `suite.versions.json` gained `kubo.shipped: "v0.39.0"`.
   `scripts/generate-suite-version-info.js` reads **that** instead of
   `kubo/version.go`, so `versioninfo.KuboVersion` is `0.39.0` — what the node
   runs. `scripts/kubo-version.sh` serves the same field to the release shell
   scripts. This is also the prerequisite for ever deleting `kubo/`: the
   generator no longer depends on it.
2. **`release-deploy.yml` no longer hardcodes the tag inside a URL** — it
   declares `KUBO_VERSION` like the other two workflows, so the pin is
   checkable.
3. **`scripts/check-kubo-pin.js`** — the check that would have caught all of
   this. It asserts the manifest, both generated constants, all three workflow
   pins, and `kubo-version.sh` agree, and fails on a stray hardcoded tag.
   Wired into `scripts/ci-local.sh` (`quick`, `full`, and a standalone
   `kubo-pin` mode), so the pre-push gate and CI both run it.
4. **The `SDNAgentVersion` drift it immediately found**: `kubo/version.go` said
   `1.0.4` while the suite was `1.0.5`, against its own "keep in step" comment.
5. **`scripts/go-with-wasmedge.sh` no longer builds the wrong module in
   silence.** It hardcoded `cd "$ROOT/sdn-server"` in both link branches; the
   repo has nine `go.mod` files and for eight of them the wrapper substituted a
   ninth. `SDN_GO_MODULE_DIR` now overrides, and standing in a different module
   with no override is a loud error instead of a confusing one.
6. **Two false comments corrected**: `coreapi.go`'s description of
   `kubo_version`, and `docs/AGENT-API-GUIDE.md`'s example response (which
   showed `1.0.4` / `0.40.0-dev`).

### Not addressed, deliberately

- **The v0.39.0 → v0.43.1 bump.** Owner decision with fleet-wide effect. It is
  now a **one-line change** to `kubo.shipped` in `suite.versions.json`; the
  check will then demand the three workflow mirrors move with it. Before
  flipping: six releases of behavior change; a named go-to-go WebTransport
  dialing regression (run `.github/workflows/live-dht-cross-platform.yml`,
  which exercises exactly that path, rather than trusting the changelog); a
  **one-way** repo migration — the supervisor already passes `--migrate=true`,
  and a rollback to v0.39.0 against a migrated repo will fail to open it, so
  stage the roll, keep a repo backup, and do not roll both hosts at once; and
  restarting the kubo child restarts the sidecar, which per the stored ops note
  has cost ~70 minutes of delivery outage.
- **Deleting `kubo/`.** Owner decision; settle the host-01 question first.
- **The managed child's public swarm exposure.** Narrowing `Addresses.Swarm` is
  a real behavior change to content delivery and needs the owner's intent, not
  an audit's.
- **A pre-existing, unrelated version mismatch.** `npm run check:versions`
  reports `sdn-js flatsql mismatch: sdn-js/package.json=2.0.3
  suite.versions.json=1.4.5`. **This is not mine** — neither input was touched
  here; it is pre-existing on `050e58da7`, and it is more evidence for the same
  root cause, because **no CI lane runs `check:versions` at all**. It lives
  only in the husky `precommit` hook, which `HUSKY=0` skips. That is why the
  new Kubo gate is a separate script: making the pre-push gate depend on the
  whole consistency checker would turn the lane red for a defect nobody here
  introduced. **Someone should decide whether flatsql is 1.4.5 or 2.0.3 and
  then wire `check:versions` into CI properly.**
