# SDN Signed Updater Design

This document defines the SDN-owned updater target architecture. It replaces
inherited IPFS Desktop application update behavior with signed SDN payloads
while keeping Kubo/IPFS runtime refreshes explicit and separable.

## Current CLI execution path

The portable `bin/spacedatanetwork` launcher resolves symlinks, selects the
bundled WasmEdge runtime unless `WASMEDGE_DIR` explicitly overrides it, and
executes the bundled SDN daemon. The daemon supervises its separate Kubo child
with loopback RPC and gateway listeners. Kubo readiness requires a successful
version response, not merely an HTTP listener.

For automatic updates, the running daemon verifies the small signed pubsub
pointer, fetches and verifies its manifest, and checks their channel, target,
version, sequence and hashes agree before downloading the carrier. Downloads
remain HTTPS across redirects and use bounded sizes; a signed carrier size
tightens the payload limit. Staging verifies the actual carrier and bundle
hashes again. For an install folder the node's account can write, the copied
helper then performs the shutdown, swap, restart and health/rollback sequence
outside the replaceable bundle and service cgroup; for one it cannot write,
the daemon installs into its account's home and restarts itself through the
installed path, with no helper (below).

**Running dataset publications finish first** (stack issue 1044). When an
update asks the daemon to stop (the helper's shutdown handshake, or a home
bundle's restart), the daemon refuses every new dataset publication and
waits, at most 30 minutes, for the running ones to finish; only then does its
shutdown begin. A request for a publication that already runs still waits for
it; a new one answers HTTP 503 with `Retry-After`. The handshake's answer
states `runningPublications` and `waitBeforeStopSeconds`; the helper records
`stop-waits-for-publications` in the deploy ledger, prints `daemon_stop_wait=`, and waits that
much longer for the daemon's pid to exit before it escalates. While the daemon
waits, `updates/stop-wait.json` says so and `spacedatanetwork status` prints
`update_stop_wait=dataset-publications running=<n> pid=<pid> since=<time>
until=<time>`. A signal during the wait ends it.

A publication of a lane or a batch (with every other request field at its
default) that is still running at the bound, or that any stop or crash ends,
keeps a mark in the publication directory's `publication-runs` folder. So
does one refused while the node stops: during an update's wait, and during
any other stop (an operator's restart, a reboot); such a refusal answers HTTP
503 with `Retry-After` (before, a refusal during a stop answered 400). Once
the next start answers ready, the node handles the marks one at a time,
reading one row of the store per mark and no record. What it reads depends on
the lane's kind (`publishing.retention.lanes[].snapshot_batches`):

- on a snapshot lane (each batch a complete snapshot, such as a GP pull) the
  marks are handled newest first, and the row is the lane's newest series. If
  it started at or after the publication was requested, the publication is
  not published again: of the same batch, it completed; of another batch, the
  lane has newer data, and publishing the older batch would move the lane's
  newest series and IPNS pointer back to it. The same check is made again
  once the publication holds its lane, since another batch may have been
  publishing while it waited for the lane;
- on any other lane the batches are parts of one dataset, each published
  once (the cellular chunk lane): the marks are handled oldest first, in the
  order the parts were requested, and the row is the newest series of the
  mark's own batch. Only that batch's own completed publication removes the
  mark; a later part does not;
- a mark older than 24 hours is removed and not published;
- otherwise it is published again, at most 3 attempts in all across starts
  (counted in the mark before each attempt starts); after the third the node
  reports it FAILED at the step "publish again after the node's stop", the
  lane's `publication_failed` alert stays raised, and the mark is removed.

An interrupted run is reported as failed at the step "interrupted by the
node's stop" before it is published again, which raises the alert; the
report is cleared when the publication completes or when its mark is dropped
because the lane has moved on
(`sdn-server/internal/api/dataset_publication_resume.go`). Like any failed
publication it is a failed scope in the store, so a restart before then raises
the alert again (`sdn-server/internal/api/dataset_publication_alert.go`).

The helper adds the daemon's `waitBeforeStopSeconds` to its graceful-stop
grace, at most one hour; a larger value is clamped and printed as
`daemon_stop_wait_clamped`.

This path is implemented in Go (`sdn-server/internal/update/`,
`sdn-server/internal/node/update_signal.go`). The `packages/sdn-updater-module`
WASM package contains stub methods and does not drive updates. Besides acting
on signals, a node of 1.1.7 or later checks its feed every six hours
(`update.check_interval_seconds`, first check one to five minutes after the
start), so a node that was off when a signal went out still takes the
release; a signed withdrawal stops a release from being installed. A daemon
launched from a binary outside a portable bundle has no update path.

### The update root: the install folder, or the account's home

All of the above works on one folder, the update root (`update.PathsFor`):

- the install folder, when the account running the node can write it: the
  helper path above, unchanged;
- otherwise the install's home bundle, `<home>/.spacedatanetwork/bundle/<key>`:
  `<home>` is the home folder of the effective user's passwd entry (`$HOME`
  only when it resolves to that folder), `<key>` the first 16 hex characters
  of sha256 over the resolved install folder, so each install has its own.
  With no usable home folder (none in the passwd entry, one that does not
  exist, or one the account cannot enter: `EACCES` anywhere on the way, as
  under the shipped systemd unit's `ProtectHome=true`) it is
  `<storage.path>/update-bundle/<key>` (logged), so a service account under
  the shipped unit updates itself into its storage folder. A home folder that
  exists and fails the ownership checks below is refused, never replaced by
  the storage folder; the storage folder gets the same checks. No root,
  service account, unit change or write to the install folder is needed.

The home bundle of an install is moved only with it. A bundle whose mount
path changes at every start would get a new home bundle each time; no bundle
the node ships runs that way (the desktop app, AppImage included, does not
update itself).

**Who may use a home bundle.** A process whose effective user is root never
does: root with a read-only install folder is refused with a message to make
the folder writable or install by hand, and a home build started as root
exits. Every folder from the home bundle's `updates/` up to the home folder
(or the storage folder) must belong to the effective user and not be
writable by its group or by others. The path is also walked from `/` the way
the system resolves it, following every symlink: each folder and symlink on
it must belong to the effective user or to root, and a folder its group or
others can write must carry the sticky bit (`/tmp`), where only an entry's
owner can rename it. Inside the build, every folder and file must belong to
the effective user and not be writable by its group or by others, and every
symlink must belong to it; an update is unpacked with group and other write
bits removed, whatever the archive or the umask say. Otherwise the build is
refused with the reason, and the installed build runs.

**One daemon per home bundle.** The daemon holds `<key>.daemon.lock` beside
the bundle for its life; a second daemon from the same install and account is
refused at its start check (give each node its own install). The lock is an
`flock` on an open file that the start check takes and passes across each
`exec` (`SDN_UPDATE_DAEMON_LOCK_FD`, read and removed at start), so there is
no moment between the start check and the daemon it starts when another
daemon could take it. Every change of `trial.json` (pass, fail, signal stop,
start, revert) is made under `<key>.trial.lock`. An update is
installed under `<key>.install.lock`, so a typed `update install` and the
daemon's own install never run at once. Every process that runs a home build
holds `<key>.inuse.lock` shared, and a stale bundle (the installed build
changed, or its record cannot be read) is removed only when nothing holds it.

**Why the helper stays for a writable install folder.** The swap there
replaces the binary at the installed path, which is the code that would judge
and revert the new build: a build that fails before it can run any check of
its own would be restarted at that path with no older code left to revert it.
The helper is a copy of the old binary outside the swapped tree.

A home bundle is a bundle root like the installed one. It holds a payload
only once an update has been applied; its first rollback slot is empty, and
an empty slot stands for the installed build. Retention keeps five slots, so
after five home updates the empty slot is pruned; removing the home bundle
(node stopped) always returns to the installed build. `updates/home.json`
records the installed folder, its version, the sha256 of its `manifest.json`
and its sequence; an unreadable one is rebuilt at the next update. For each
build it holds, `updates/proofs/<update id>/` keeps the signed update manifest
and the bundle archive it signs. A release that replaces the update roots is
kept as its signed manifest in `trust/rotations/`; the roots a home build is
checked against are always the installed build's roots, advanced through each
kept rotation that verifies against the roots before it. `SDN_UPDATE_TRUST_ROOTS`
does not apply to that check. State files are written to a temporary file,
synced, renamed and the folder synced.

**No helper in a home bundle** (`sdn-server/internal/update/trial.go`). A
daemon run by a system unit as an ordinary account cannot create a transient
unit, and a helper started as its child is stopped with the unit's processes.
So:

1. **Install while serving.** The verified staged update is extracted and
   renamed into the home bundle (the installed bundle is never touched; a
   running home build keeps its open files after they move into its rollback
   slot), `updates/trial.json` marks it as a trial, and its files are verified
   against the signed archive. One that does not verify is rolled back at once.
2. **Restart through the installed path.** The daemon stops its services as
   for any shutdown (tor, its one child process, included; Kubo and the
   modules run in the daemon's process) and replaces its process (`exec`, the
   same pid) with the installed launcher and its own arguments, in the
   environment the installed launcher had: entries the start check removed are
   given back and those pointing into the home bundle removed. This holds under
   systemd with any restart policy and a daemon started by hand (launchd
   watches the same pid; not run end to end under launchd),
   except when the shutdown itself fails: if the 75-second shutdown watchdog
   ends the process, a service manager that restarts the node starts it again,
   and a node started by hand stays stopped until it is started. The update
   then runs as a trial at that start (it never ran, so nothing is rolled
   back). A drain that ends with an error still restarts in place.
3. **Trial start.** The installed build verifies the home build in full,
   counts the trial start in `trial.json`, writes its deadline there (now plus
   `update.health_timeout_seconds`, default 600 s, which covers stores that
   took four to five minutes to open after an update) and runs it with
   `SDN_UPDATE_TRIAL` set. Before anything else, the trial build starts a timer
   for that deadline. Its health gate reads its own readiness (the
   `/api/v1/ready` check, in process): ready records `health-passed` and ends
   the trial. At the deadline the gate, or the timer if the build is stuck
   anywhere else, writes the reason into `trial.json` and restarts through
   the installed path: through the ordinary shutdown once the daemon serves,
   and if that has not completed within the shutdown watchdog's bound, or the
   daemon never got that far, the timer stops tor and replaces the process
   itself. With the admin listener off there is no readiness to read: the
   trial passes at once and the log says so as an error.
4. **Revert.** A daemon start through the installed path that finds a trial
   started and not passed (after step 3, or after a crash the service manager
   restarted), and any start at all after a trial's deadline, records
   `health-failed`, rolls back to the previous slot (the build moves to
   `updates/failed/<update id>`, the quarantine; the ledger records
   `rollback`), records `health-reverted` and runs the previous build. A
   pending trial whose files fail verification, or whose `trial.json` cannot
   be read, is reverted the same way. A command other than `daemon` reverts
   only when no daemon holds the daemon lock: while the trial daemon is alive
   (stuck, or late after a suspend) it runs the installed build and leaves the
   revert to the next daemon start, so no running build has its files moved
   away. A revert works only on the build `state.json` names: a `trial.json`
   naming another update is what a revert stopped after its rollback leaves,
   and the next daemon start only clears it (writing `health-reverted` when
   the ledger lacks it), so the build that ran before the trial is never
   quarantined; `FailTrial` writes only while `trial.json` still names its
   update. Not covered: a trial build that hangs so that its timer cannot act
   keeps running until the service manager or an operator stops it; the next
   daemon start through the installed path rolls it back.
4a. **Stopped during a trial.** `SIGTERM` or `SIGINT` reaching the trial
   build before it passes (`systemctl restart`, a reboot) is not a failure:
   the trial build watches for it from its first instruction, records it in
   `trial.json` (`interruptions`, `starts` back to 0, no deadline) and ends
   as it would have; the next start runs the trial again with a new deadline.
   The bound is `update.MaxTrialInterruptions` = 3: after three such stops
   the trial runs a fourth time, and a stop during that one fails it, so a
   build that is always restarted before it is ready cannot stay on trial.
   A `SIGKILL`, a power loss or a crash counts as a failed trial start.
5. **Revert refused.** When the store-format guard refuses the rollback (the
   trial build moved the store to a format the previous build cannot open),
   nothing is swapped and the build that failed keeps running. The log says
   `was NOT rolled back`, the ledger records `revert-refused`, and the node
   raises the `update_failed` alert until a newer build runs. The operator
   installs a newer release, or restores the store from a snapshot taken
   before the update and runs `update rollback`.

The ledger, the slots and the quarantine are those of the helper path, so
`update slots`, `update rollback`, the `update_failed` alert and the refusal
of a quarantined update read them unchanged.

**The start check.** At every `daemon` start, and when a build is installed,
the home build is verified in full:

1. the installed folder and its `manifest.json` are the ones recorded (an
   administrator who installed another build wins; the next update starts the
   home bundle again from it);
2. the signed manifest of the build `state.json` names verifies against the
   roots above, targets this platform, and is newer than the installed build
   (higher sequence, version not lower);
3. the kept archive has the manifest's sha256 and size;
4. the payload on disk is exactly that archive's bundle: the same files (none
   added, none missing), bytes, executable bits and symlink targets.

The result is recorded in `updates/verdict.json` with the size, mode,
modification and change times, inode and device of every file under the home
bundle outside `updates/`, of `state.json`, `home.json` and the proof, and of
the installed `manifest.json` and update roots. Every other command runs the
home build when all of these are unchanged, and verifies in full otherwise.
This guards against a damaged or replaced file; it is not a defense against
the account that owns the home folder, which can run any program. On a v1.1.8
darwin-arm64 bundle (337 files) a full verification takes about 1.4 s and the
recorded check about 3 ms. The config is read only when it is needed: for a
daemon start that starts a trial, a revert's store-format guard, or the
storage fallback.

A build that fails the check is refused with
`update: the build under <folder> is refused: <why>; running the installed build`,
and the installed build runs (at a daemon start, a build still on trial is
rolled back instead). Everything that records this program's path for a later
start (`service install`, `remove`, the restart after an update) names the
installed launcher, never a home build. The home build is started with
`WASMEDGE_DIR` and library-path entries that point into the installed folder,
by either spelling of its path, removed, so its launcher uses its own runtime
files. Limits: below the installed build nothing can run, but between signed
builds newer than the installed one an account that can write its home can
choose any (as `update rollback` does); a home bundle takes full-bundle updates
only, not module-targeted ones; Windows has no `exec`, so it needs a writable
install folder, and nothing probes the install folder there. A read-only
install of a release older than the one that brings home updates cannot
update itself: install the new release by hand once.

## Updater Process Boundary

The SDN updater is a standalone helper process launched by SDN Desktop. The main
app may request update checks, display progress, and ask the helper to stage an
approved update, but it does not directly replace application/runtime files.

IPC commands:

- `check`: fetch release index metadata and report available manifests.
- `download`: download manifest and carrier into a staging directory.
- `verify`: run signature, target, expiration, sequence, and hash checks.
- `stage`: unpack the verified bundle into a versioned staging directory.
- `commit`: stop affected SDN processes, atomically swap staged files, and
  record the new manifest as current.
- `rollback`: restore the last known-good staged version when startup health
  checks fail.
- `history`: return audit records for UI display.

Privilege model:

- The Electron UI runs unprivileged.
- The helper owns file replacement and platform-specific elevation.
- Kubo shutdown/startup is coordinated through the desktop daemon lifecycle.
- No update payload receives execution rights during verification.

## Audit And Lifecycle

The updater writes append-only audit records for checks, downloads,
verification, staging, commit, rollback, and failures. Each record includes:

- timestamp;
- actor (`automatic`, `manual`, or `policy`);
- update id, version, sequence, target kind, and channel;
- manifest hash, bundle hash, signer key id, and verification result;
- previous version and new version;
- process lifecycle actions for SDN Desktop and Kubo;
- error code and remediation hint when failed.

The desktop UI should surface this history in the update/settings area before
automatic installation is enabled.

## SDN Update Feed

The SDN-owned update feed origin is:

```text
https://sdn.spaceaware.io/updates
```

Desktop application payloads are indexed under:

```text
/desktop/<channel>/<platform>/<arch>/index.json
/desktop/<channel>/<platform>/<arch>/<version>/manifest.json
/desktop/<channel>/<platform>/<arch>/<version>/update.wasm
```

Runtime and module-delivery payloads use their target kind in place of
`desktop`, for example:

```text
/kubo-runtime/<channel>/<platform>/<arch>/index.json
/module-delivery/<channel>/<platform>/<arch>/index.json
```

The feed index schema is `org.spacedatanetwork.update.index.v1`. It contains
only SDN-owned manifest and carrier URLs plus the manifest identity fields a
client needs before downloading a payload. The signed manifest remains the
authority for install decisions.

Feed assembly is implemented as static artifact tooling:

```sh
node deployment/release/build-sdn-update-feed.js \
  --out-dir dist/release/update-feed \
  --entry path/to/manifest.json:path/to/update.wasm
```

`deployment/release/assemble-release-artifacts.sh` also accepts
`SDN_UPDATE_FEED_ENTRIES` as a comma-separated list of
`manifest.json:update.wasm` pairs and writes the feed under
`dist/release/update-feed` by default. Publication to
`sdn.spaceaware.io/updates` is a release-operations step after artifact
verification; the tooling intentionally does not claim a live deployment.

### Release runbook: put a published release on the update channel

The release workflow (`beta-release-artifacts.yml`) builds, signs and verifies
the CLI update feed and attaches it to the GitHub release as
`spacedatanetwork-update-feed-<version>.tar.gz`. It does NOT put it on the
channel. Until the steps below run, no installed node can see the release:
v1.1.10 was published at 2026-11-21 16:45Z, and no node had it 20 minutes
later, because host-01's feed tree still ended at 1.1.9. Run these right after
the release is published, on host-01 (`ssh sdn.spaceaware.io`, as root). The
feed directory is `/opt/spacedatanetwork/update-feed`, the node's
`SDN_UPDATE_FEED_DIR`.

1. Download the feed and the checksums into a staging folder on the data
   volume, and verify:

   ```sh
   S=/mnt/volume_nyc3_01/sdn-release-<version>-staging; mkdir -p $S && cd $S
   B=https://github.com/DigitalArsenal/space-data-network/releases/download/v<version>
   curl -fsSL -o spacedatanetwork-checksums.txt $B/spacedatanetwork-checksums.txt
   curl -fsSL -o spacedatanetwork-update-feed-<version>.tar.gz $B/spacedatanetwork-update-feed-<version>.tar.gz
   grep spacedatanetwork-update-feed-<version>.tar.gz spacedatanetwork-checksums.txt | sha256sum -c -
   ```

2. Extract into a fresh folder (`mkdir extract && tar -xzf ... -C extract`).
   The tree is `update-feed/cli-bundle/release/<platform>/<arch>/`, with one
   `index.json` (only the new version) and a `<version>/` folder
   (`manifest.json`, `update.wasm`, `signal.json`) for each of linux/amd64,
   linux/arm64, darwin/arm64, darwin/amd64 and windows/amd64. Compare it with
   the live tree: the live `index.json` lists the previous version, and the
   older version folders stay as they are.
3. For each platform, keep a copy of the live `index.json`
   (`index.json.bak-<UTC time>-before-<version>`), copy the `<version>/`
   folder in, and replace `index.json` with the new one (write it next to the
   old one, then `mv -f`). Leave `cli-bundle/beta` and `ui-bundle` alone.
4. Check the public indexes:
   `curl https://sdn.spaceaware.io/updates/cli-bundle/release/<platform>/<arch>/index.json`
   lists the new version for each platform, and its `manifest.json` answers 200.
5. Announce the pipeline-signed signals, so nodes do not wait for their
   six-hourly feed check. Tell the operators of the console first: every
   bundle node then restarts within its rollout window.

   ```sh
   SSL_CERT_FILE=/etc/spacedatanetwork/tls/origin.crt \
   SDN_UPDATE_FEED_DIR=/opt/spacedatanetwork/update-feed \
     /opt/spacedatanetwork/sdn-bundle/runtime/sdn/spacedatanetwork update signal \
     --channel release --from-feed --version <version> \
     --node-url https://127.0.0.1:443 \
     -c /etc/space-data-network/config.module-delivery-sidecar.yaml
   ```

   It prints one `target=<platform>/<arch> sent=<update id>` line for each of
   the five targets. `--from-feed` takes no `--dry-run`, and it publishes the
   signals the pipeline signed with the release key. Two things are needed for
   the CLI to sign in to the daemon with its root key:
   - It must reach the daemon over loopback. Through `https://sdn.spaceaware.io`
     the request arrives through the proxy, and the node refuses it ("SEC-001
     hotfix: refused a root-key auth challenge").
   - It must trust the daemon's self-signed certificate. That certificate names
     127.0.0.1, so `SSL_CERT_FILE` pointing at it is enough.

   v1.1.10's signals went out this way at 2026-11-21 17:36Z.
6. Within the rollout window (default 30 minutes), check host-01 and host-02:
   `spacedatanetwork version`, `updates/state.json`, the services, and on
   host-01 `https://sdn.spaceaware.io/.well-known/sdn/modules.pmm` (200).

## Electron Application Update Path

Branded SDN Desktop builds must use the SDN update feed origin for application
updates. Inherited IPFS Desktop GitHub release feeds are not accepted for SDN
application update metadata.

Manual update fallbacks may still open the SDN GitHub release page while
automatic SDN Desktop app updates remain disabled, but any Electron automatic
update feed must resolve to:

```text
https://sdn.spaceaware.io/updates/desktop/<channel>/<platform>/<arch>
```

Kubo runtime checks are separate from Electron app updates and may inspect
upstream `ipfs/kubo` release metadata only to select an explicit runtime
payload. They must never point the Electron app updater at upstream IPFS
Desktop releases.

## Upstream Refresh Process

Use this process for an upstream IPFS Desktop/WebUI/Kubo refresh:

1. Pin selected upstream revisions in the release notes or refresh manifest:
   IPFS Desktop commit/tag, IPFS WebUI commit/tag, and Kubo version/tag.
2. Refresh upstream mirror directories from those exact revisions without
   product edits in the mirror refresh commit.
3. Reapply SDN overlays, patches, generated vendor snapshots, branding,
   networking defaults, bootstrap policy, and SDN update feed configuration.
4. Run overlay application tests and focused updater tests proving inherited
   IPFS Desktop app update feeds are still disabled.
5. Build desktop/runtime/module payloads, sign manifests, verify payload hashes,
   and assemble the static SDN feed index.
6. Publish the feed tree to `sdn.spaceaware.io/updates`.
7. Publish the signed manifest through the SDN network release path so clients
   consume SDN-owned metadata only.

The upstream mirror refresh and SDN overlay reapplication should remain
separate commits where practical. That keeps future refresh failures easy to
attribute to upstream drift or SDN overlay drift.

## Rollback And Emergency Disable

Rollback uses a signed rollback manifest with `rollback.reason` and
`rollback.previous_sequence`. Clients reject rollback manifests that are
unsigned, expired, outside policy, or below the allowed rollback floor.

Emergency disable for a bad manifest or bad upstream refresh:

1. Remove or quarantine the affected `index.json` entry at
   `sdn.spaceaware.io/updates`.
2. Publish a corrected index that points clients at the last known-good signed
   manifest, or publish no update for that target while investigation is open.
3. Add the bad manifest sequence, update id, and signing key id to the
   revocation/policy store when key or signing-policy compromise is suspected.
4. Publish a signed rollback payload only after the rollback bundle has passed
   the same manifest, hash, target, and health checks as a forward update.
5. Record the incident in updater audit history and release notes, including
   the disabled sequence and the replacement sequence.

Do not delete audit records or mutate an already-published manifest in place.
Disable through the feed index and signed policy metadata so clients can retain
a verifiable history of what happened.

## CLI Bundle Update Payloads

Self-contained CLI bundles use the same signed envelope with `target.kind:
cli-bundle`. The verification side is the Go updater in
`sdn-server/internal/update/` (`spacedatanetwork update ...`); the release
side is the tooling in `deployment/release/`.

Feed layout follows the target-kind convention:

```text
/cli-bundle/<channel>/<platform>/<arch>/index.json
/cli-bundle/<channel>/<platform>/<arch>/<version>/manifest.json
/cli-bundle/<channel>/<platform>/<arch>/<version>/update.wasm
```

Lane-specific manifest rules:

- `target.kind` is `cli-bundle`.
- `target.platform` and `target.arch` use Go runtime names (`darwin`, `linux`,
  `windows` and `amd64`, `arm64`). The Go verifier also accepts the
  Electron-style `win32`/`x64` aliases used by the desktop lane.
- When the bundle wraps an upstream IPFS/Kubo runtime, the signed manifest
  records `upstream.kubo.source` and `upstream.kubo.version`. Clients still
  download through the SDN-owned feed and trust only the SDN signature; the
  upstream fields are provenance for the wrapped payload, not an upstream
  update authority.
- `bundle.format` is `tar.gz`: the carrier embeds the exact
  `spacedatanetwork-<version>-<os>-<arch>.tar.gz` archive produced by
  `deployment/release/build-self-contained-cli.mjs`.
- `expires_at` is 90 days after `created_at`. The payload builder requires an
  explicit `--created-at` so rebuilds are reproducible.

Trust roots ship inside the installed bundle at
`<bundle>/trust/update-roots.json`: a JSON object mapping `signing.key_id` to
a base64-encoded Ed25519 public key (SPKI DER as exported by the release
tooling, or a raw 32-byte key). `stageBundle` stages the file via its
`trustRootsPath` option; it is bundle metadata, so it is excluded from
`manifest.json` artifacts and `checksums.txt`, and the staged swap never
replaces it. `SDN_UPDATE_TRUST_ROOTS` overrides the path for tests and
managed deployments.

### The demo root (2026-11-15)

The fleet roots (`deployment/release/fleet-trust-roots.json`, staged into
every bundle) are exactly one demo key, key id `12cb25de07c0`:

```json
{"12cb25de07c0":"MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM="}
```

The key id is the first 12 hex characters of sha256 over the raw 32-byte
public key (the last 32 bytes of the SPKI DER): `update.RootKeyID`. A demo key
signs the release channel, fleet releases and update signals until a
production key replaces it through a release that carries `trust_roots`.

- **Compiled-in fallback.** The same set is embedded in the binary
  (`sdn-server/internal/update/default_update_roots.json`, held byte-equal to
  the fleet file by a test). `LoadTrustRoots` uses it ONLY when the bundle's
  trust file is absent; a present file wins, so a rotation can drop the demo
  root. Absent means nothing at all at the path (a dangling symlink is an
  error), and once a release's `trust_roots` have been installed
  (`state.json` `trust_roots_from`, kept by every later apply and rollback) a
  missing trust file is an error too, so losing the file cannot bring back a
  root the rotation dropped. An explicit `SDN_UPDATE_TRUST_ROOTS` path that
  does not exist is an error, not an absence. The first binary after the
  production rotation should also drop the demo key from the compiled-in set.
- **One reading of every signed document.** A manifest, a signal and the
  trust file are refused when any object, at any depth, names a key twice
  (compared after unescaping, and case-folded as Go's decoder matches struct
  fields), or when anything follows the one JSON value. The canonical bytes a
  signature covers keep only the last copy of a repeated key, while the
  decoded manifest merged the copies: a feed host could prepend its own
  `"trust_roots"` to a signed rotation and have its root installed beside
  the signed one (`sdn-server/internal/update/strictjson.go`).
- **The SEC-001 key is never a root.** The root shipped up to v1.1.1,
  `d4a971a7e534`, is the raw key `0d80e1fd…fc8c`: the node identity leaked in
  SEC-001, derived from a published seed. Every key of that identity
  (`sdn-server/internal/sec001/denylist.json`) is refused wherever roots are
  loaded — the trust file, the override, the compiled-in set, a release's
  `trust_roots` — and as the signer of a manifest or a signal, whatever key id
  it is listed under.
- **Existing installs: the one manual step.** A command-line install re-runs
  the installer for the new release and restarts the daemon. Without a
  reinstall: replace `<bundle>/trust/update-roots.json` with the demo root
  (replace, do not add beside: keeping `d4a971a7e534` keeps a root anyone can
  sign for, and new builds refuse a file that lists it), stop the daemon, run
  `<bundle>/bin/spacedatanetwork update install --direct`, and start it. A
  helper from 2026-10-18 (47f043132) through v1.1.1 reverts every update it
  gates (stack issue 670), hence `--direct`. Until that step is done, a
  v1.1.x install should run with `update.enabled: false`: it trusts the
  SEC-001 root, and its signal lane acts on a signal from any peer, so
  anyone can make it install and start a build. Fleet hosts take the same
  hand edit, but they run the internal `beta` lane
  (`<feedBaseUrl>/cli-bundle/beta/...`, the default of
  `publish-fleet-update.mjs` and `publish-ui-update.mjs`), whose manifests are
  signed by the coordinator's key or an uploaded one: after the edit,
  `update install --direct` refuses them until a fleet release signed with
  the demo root is published on that lane (the demo key uploaded to the
  coordinator's Updater), so a fleet host either waits for that release or
  reinstalls. Desktop, package, VM and container installs install the new
  release.
- **A release is required for the channel.** The release channel carries
  nothing until a release is cut with `SDN_UPDATE_SIGNING_KEY_PEM` and
  `SDN_UPDATE_KEY_ID` set and its `spacedatanetwork-update-feed-<v>.tar.gz` is
  published to host-01's feed directory (v1.1.5's was, on 2026-11-16). The key
  is a secret of the `update-signing` GitHub Environment, which admits only
  `main`; only the release workflow's `update-signing-check` and
  `cli-update-feed` jobs name it, and the workflow guard
  (`beta-release-workflow.test.mjs`) refuses the secret anywhere else in any
  workflow, any `secrets` expression other than one named secret (reading
  expressions as the runner does, string literals included), any job that
  calls a reusable workflow, any other job naming the Environment, and any
  action not pinned to a commit in the key jobs and every job they need,
  directly or through other jobs. `update-signing-check` runs before any build: it fails a stable
  release without the key (a beta warns and ships no feed), derives the key id
  from the PEM and fails if it differs from `SDN_UPDATE_KEY_ID` or is not in
  `fleet-trust-roots.json` (`check-update-signing-key.mjs`; there is no
  fallback key id). `cli-update-payloads`, without the key, unpacks the CLI
  archives and writes each payload's carrier and its bundle's sha256, size
  and format. `cli-update-feed` unpacks nothing and runs no program another
  job built: it builds its signal signer (`cmd/sdn-update-signal-signer`, no
  cgo) from the checked-out commit, repeats the key check, signs each
  manifest over those digests and the carrier's own hash
  (`sign-cli-update-payload.mjs`), builds the index and signs the signals.
  `cli-update-feed-verify`, without the key, unpacks the feed and verifies
  every manifest against `fleet-trust-roots.json`
  (`verify-cli-update-feed.mjs`); the release waits for it. A dry run
  (`publish` false) skips those three jobs, so the key signs nothing:
  `cli-update-feed-dry-run` runs their reviewed steps with a key made in the
  run and a roots file holding only its root, verifies the feed against that
  root, and checks that `fleet-trust-roots.json` refuses it.
  `update-signing-check` still runs in a dry run.

### What a payload, a signal and the health gate must satisfy

- **No `trust/` in a payload.** Every node refuses a payload with `trust/` at
  its bundle root. The downloadable archive keeps `trust/` for a fresh
  install; `build-cli-update-payload.mjs` wraps that archive with `trust/`
  removed (and the archive itself unchanged when it has none).
- **A signal points only into the bundle's own feed.** A signal's
  `manifest_url` and `carrier_url`, and every redirect while fetching them,
  must lie under the receiving bundle's `update.feedBaseUrl`: same scheme,
  same host and port, a clean path below the feed's path, no query,
  fragment, credentials or percent-encoding. The signal's own
  `feed_base_url` is never used for this. A validly signed signal pointing
  elsewhere is dropped before anything is fetched. `update install` and
  `update check` apply the same rule to feed index entries and to every hop
  of the index, manifest and carrier downloads (HTTPS only, under the feed).
- **The index is not signed.** It only points: every entry must lie under
  the feed, its sequence must be above the installed one and equal the
  signed manifest's, and every hash it names must match the signed
  manifest, so it cannot lead to unsigned code or to a downgrade. What a
  feed host (or whoever controls it) can still do is serve a stale index,
  holding updates back, or choose which valid, unexpired, higher-sequence
  signed manifest `update install` takes. That is a design limit of an
  unsigned index; the signal lane, driven by signed signals, is not affected.
- **The helper gates on readiness.** After the restart the helper waits for
  `GET /api/v1/ready` to answer 2xx (the status code is the whole answer) and
  for `/api/v1/id` to report the installed build, for
  `update.health_timeout_seconds` (default 600, also when `update install`
  starts the helper; `--health-timeout` overrides). `spacedatanetwork status`
  reads `/api/v1/health`. Both used to decode `/api/v1/data/health`, which
  answers a `$QRP` FlatBuffer, so every helper-driven update was reverted
  (stack issue 670). `status` now prints `daemon_status=running` with
  `data_health=healthy`, or `data_health=degraded` plus `alerts_error` and
  `alerts_warning` while alerts are active (the daemon is serving; the lane's
  `verify-update-entrypoint.sh` accepts both); `data_runtime` is gone and
  `data_message` appears only when the answer cannot be read.
- **What `/ready` waits for.** On a node with a store: the engine answering
  and, with flow mounts, the hot-window hydration done (the same condition
  the mounts answer 503 on). A `mode: edge` node opens no store, so its
  readiness is the libp2p host alone. Two limits: a boot hydration that traps
  leaves the hydrated flag unset after the engine is recovered, so `/ready`
  reports "data services warming" until the next restart and the gate reverts
  (the restored build most likely does the same); and flow mounts load their
  artifact on first request, so the gate does not exercise them. `TestUpdateChannelRealDaemonE2E`
  (`SDN_E2E_REAL_DAEMON=1`, Linux; the ci.yml job "Update channel E2E (real
  daemon)") runs the pass, revert-and-quarantine and wrong-key cases against
  real daemons.

### When a node acts on an update, and what it tells the other nodes

- **Rollout window.** A node acts on an update it accepted (a signal that
  passed every check above, or a feed-check find) at its own time within
  `update.rollout_window_seconds` (default 1800; `0` acts at once; at most
  86400): the time it accepted the update plus `window x (first 8 bytes of
  sha256("SDN-UPDATE-ROLLOUT-V1" 0x00 peer-id 0x00 update-id)) / 2^64`,
  in whole seconds (`update.RolloutDelay`). It writes the accepted time, the
  delay and the due time to `updates/pending.json` before it waits (with the
  signal's bytes for a signal), so a restart keeps the due time: a pending
  signal is verified again from those bytes and waited out, and a pending
  feed-check find makes the first feed check run at its due time. On a
  restart the remaining wait is capped at the window, so a clock that was
  ahead when the update was accepted and was corrected since cannot stretch
  the wait to days; while the node runs, the wait ends at the latest one
  window after it began on the monotonic clock, so a wall clock set back
  during the wait does not stretch it either. A pending signal that no longer verifies at the start
  (it expired while the node was down) is cleared with a warning, and the
  node checks its feed at once. After the wait the node reads its state again
  and does nothing if the update is already installed. A pending record of an
  update the node refuses (quarantined in `updates/failed/`, for example
  after a typed install of it was rolled back, or a declared rollback) is
  cleared, so the node never reports a wait it is not in. `update install`
  typed by an operator never waits. A UI package is not delayed (nothing
  restarts); UI packages install one at a time, each checked again against
  the package installed when its turn comes, so the node never ends on an
  older package than one it installed. A signal is known by its signed content (its
  canonical document and signature), so a re-encoding of one signal is a
  copy: a copy of a signal being handled, or handled in the last 15 seconds,
  is dropped for the cost of a parse and a hash, and a UI package signal at
  or below the sequence being installed or waiting is dropped before it
  waits. The log line `Update signal handlers running: N; copies ...
  dropped: M` (at most every 5 seconds) shows both.
  The log line is `Update <id> (version <v>) accepted at <t>; this node acts
  on it at <due> (rollout window <w>s from config|signal, this node's delay
  <d>s)`; `update check` and `status` print `update_pending=<id> version=...
  trigger=... accepted_at=... due_at=... window_seconds=... window_from=...
  delay_seconds=...` or `update_pending=none`.
- **A newer signal replaces a wait.** A verified signal with a higher
  sequence that arrives while the node waits for an older update ends that
  wait: the newer update is handled under its own window (a window of 0 acts
  at once) and replaces `updates/pending.json`; the older update is not
  installed. This holds for a wait on a feed-check find too. A signal of an
  older release that arrives during a wait is not kept, and is never
  installed after the newer release's attempt (whether that attempt
  succeeds or fails).
- **Withdrawn during the wait.** At the due time the node reads its feed
  index again. An update the index no longer lists (withdrawn, even with its
  manifest left in place) is not installed, and its pending record is
  cleared. An index the node cannot read withdraws nothing: the install still
  verifies the signed manifest.
- **A window in the signal.** A signal may carry `rollout_window_seconds`
  (0 to 86400), signed with the rest of it; it replaces the node's setting
  for that update. `sdn-update-signal-signer --rollout-window-seconds N` and
  `spacedatanetwork update sign-signals --rollout-window-seconds N` sign it
  into every signal of a feed; without the flag no window is signed. The
  release workflow signs every release's signals with its
  `rollout_window_seconds` input (default 1800). The workflow's first job
  refuses a value that is not a whole number from 0 to 86400, before anything
  is signed or published, and both signers refuse a given flag outside 0..86400
  (a negative value is an error, not "no window"). The signature covers the
  canonical form of the signal as published, unknown fields included, and
  older nodes decode a signal into a structure that ignores fields it does
  not name, so a node of 1.1.9 or earlier verifies such a signal and acts on
  it at once (or after its own `update.max_delay_seconds`, a random delay
  those releases have). The signed-signal check of the publisher's feed
  (`MatchFeedSignal`) does not compare the window, which the index does not
  carry. The feed index and the manifests carry no window, so a node that
  finds a release by its feed check (it missed the signal) waits by its own
  `update.rollout_window_seconds`.
- **The signal again for a node that missed it.** A signal reaches the
  nodes on the update topic when it is published; a node that is restarting
  or disconnected at that moment does not get it from that publish. The node
  that published the signal through its admin API sends it again, for an
  hour after publishing it (or until the signal expires, if sooner), when a
  peer that was part of its fleet when the signal went out (on the topic, or
  a node whose status it held) joins its update topic again and that peer's
  own status, received in the six seconds after it joined, does not show it
  already has the signal or a later sequence (at most once every ten seconds).
  A node new to the publisher, one that comes back later than that, or one
  that reaches the publisher only through other nodes gets the release from
  its own feed check.
- **Not coordinated.** Each node's delay is its own, and the delays of two
  nodes are independent and spread evenly over the window W: two nodes that
  are each down for D seconds overlap with probability 1-(1-D/W)² (D < W).
  For five-minute restarts that is about 31% with the 30-minute default, 16%
  with 60 minutes, 75% with 10 minutes, and every push with a window of five
  minutes or less. Nodes do not agree a schedule among themselves; where two
  hosts must not be down together, install on one by hand first (below).
- **Pacing a push.** The publisher sets the pace of each release: the
  workflow's `rollout_window_seconds` input (or the signer's flag) is signed
  into the release's signals and replaces every receiving node's setting. A
  wider window keeps nodes apart more often (see above). To take one node
  first, run `spacedatanetwork update install` on it, check it with
  `spacedatanetwork update fleet`, then push the signal. Nodes of 1.1.9 and
  earlier ignore the window and act at once unless their own
  `update.max_delay_seconds` is set before the push.
- **Update status.** Each node publishes an
  `org.spacedatanetwork.update.status.v1` message (JSON, at most 2 KiB) on
  its channel's update topic: `peer_id`, `version` (its bundle version),
  `sequence`, `update_id`, `state` (`current`, `pending` or `failed`),
  `pending` (`update_id`, `version`, `sequence`, `due_at`), `failed`
  (`update_id`, `version`, `sequence`, `at`, `reason_code`, `rolled_back`,
  from its own deploy ledger: a `health-reverted` until the node runs that
  sequence or a later one, a `revert-refused` until it runs a later one, by
  the same rule as the `update_failed` alert), `acknowledged` (the newest
  signal it accepted) and `at`. `reason_code` is one of
  `not-ready-by-deadline`, `stopped-during-trial`, `trial-record-unreadable`,
  `health-failed` and `revert-refused`; the ledger's own text (gate errors,
  rollback errors, paths) stays on the node. It publishes when its update lane
  starts and the topic has a peer, when a peer joins the topic (at most once
  every ten seconds), when it starts to wait for an update, and every ten
  minutes. Every peer on the topic reads it, the time a node will restart for
  a pending update (`pending.due_at`) included; `update.publish_status: false`
  (default true) publishes no status at all, and other nodes then show the
  node "not reporting".
- **What a node accepts and relays.** The pub/sub message is signed by the
  publishing node's peer key (the router's strict signing policy). The topic
  validator accepts a status, and so relays it, only when its `peer_id` is
  the message's author; the author is a node this node identified itself as
  an SDN node (its identify agent version names `spacedatanetwork`, the test
  the peer directory admits a node by, or it is pinned; a DHT advertisement
  does not count); the status's own time is within five minutes of this
  node's clock; the author is within its rate (six at once, then one every
  ten seconds); and every field passes its check: `peer_id`, `version` and
  update ids from a fixed character set (letters, digits and `.+_-`, with
  `:` in update ids) and length, times in RFC 3339, `state` and
  `reason_code` from their lists, and the message at most 2 KiB. Fields this
  release does not know are ignored: never stored, never shown. A node keeps
  the newest status per peer; a status no newer than the one it holds
  changes nothing (a replayed status does not make a node look recently
  seen), and a status not renewed for 30 minutes is dropped. The agent string
  is the peer's own claim, so the bounds apply to peers known only by it: the
  rate table admits at most 1,024 such authors active within a minute, and a
  node holds at most 512 of their statuses, first come: once it holds 512, a status from
  a new such author is not kept, and the statuses held stay until they
  expire. A peer this node has reason to trust is outside both bounds: a pin
  (a `peers.trusted_peers` entry is one), a `network.bootstrap` peer, a
  peer its registry trusts, and a peer whose status it already holds. So a
  flood of new identities cannot remove the nodes a node already shows, or
  keep their next statuses out. The other side of first come: while 512
  such statuses are held, a node of the fleet that is known to this node
  only by its agent string and starts publishing later gets no row until
  some expire. A publisher's own fleet belongs in its `peers.trusted_peers`
  (pins) or `network.bootstrap` peers, so its rows are always kept. Nodes
  of 1.1.9 and earlier accept only signals on this topic: their validator
  rejects a status, so it is neither delivered to them nor relayed by them,
  and with no peer scoring in their pub/sub router the rejection costs the
  sender nothing.
- **The fleet view.** `spacedatanetwork update fleet` (`--json`) reads
  `GET /api/peers/versions` (admin only, as every `/api/peers` route): one
  row for this node, one for every SDN peer of `/api/peers/sdn`, and one for
  every node whose status this node accepted. Each row has the peer id, the
  name the node publishes in the peer directory, the source of its version
  and state (`local` for this node, `status` for a node's own statement,
  `identify` when only its identify version is known), the version, whether
  it is connected, when it was last seen (now when connected), the update
  state (`not reporting` without a status), and whether it accepted the
  newest signal this node verified itself (signed by a trusted root, for its
  channel and kind) or already runs that sequence or a later one; what other
  nodes claim never moves that baseline. The command prints every string
  another node or the peer directory supplied without control or format
  characters. The registry records a peer as seen when a connection to it is
  made; the node now also records every connected peer as seen once a minute,
  and every peer whose newer status it accepts, so `last_seen` on
  `/api/peers/sdn` no longer stops at the time the connection was made.
- **The fleet view's limits.** A row's version and update state are the
  node's own statement (`status`), signed by its peer key and not checked by
  the viewing node: a node that misreports is shown as it reports. A node of
  1.1.9 or earlier publishes no status and shows its identify version, `not
  reporting`. A node shows the status only of nodes it has identified itself,
  so a node reached only through other nodes (1.1.9 nodes relay no status at
  all; nodes of this release relay only statuses of nodes they identified)
  shows `not reporting`, and two nodes joined only through 1.1.9 nodes do
  not see each other's status.
- **Trial written with the state, and the pass written first.** A home
  install writes the signed proof, then `state.json` naming the new build
  with `trial_update_id` set to it, then `updates/trial.json`. A daemon start
  that finds `trial_update_id` equal to the build `state.json` names, no
  `trial.json`, and no `health-passed`, `health-reverted` or `revert-refused`
  for it in the ledger writes the trial record (holding the trial lock from
  its first read to its write) and runs the build as a trial, so no crash
  leaves a new build running without a trial once the state names it. The
  swap of the files comes before the state write, and a stop between those
  two is not covered by this record. The opposite risk is a build that passed
  its trial and is tried again: a second trial of a good build that is slow
  to start rolls it back. So when the trial build answers ready it first
  clears `trial_update_id` and sets `trial_passed` in one state write (that
  write is the pass), then removes `trial.json`, then writes `health-passed`;
  every start (a typed command too) finishes a pass that stopped after the
  state write, and the build is not run as a trial again.

End-to-end release flow:

```sh
# 1. Wrap the CLI bundle archive in a carrier and sign the manifest.
#    The signing key comes from --key or SDN_UPDATE_SIGNING_KEY_PEM.
node deployment/release/build-cli-update-payload.mjs \
  --bundle-archive dist/release/spacedatanetwork-1.2.3-darwin-arm64.tar.gz \
  --version 1.2.3 \
  --sequence 7 \
  --channel release \
  --platform darwin \
  --arch arm64 \
  --key-id 12cb25de07c0 \
  --key key.pem \
  --created-at 2026-06-10T00:00:00Z \
  --upstream-kubo-version v0.35.0 \
  --out-dir dist/update/darwin-arm64

# 2. Assemble the static feed tree (index.json + payload copies).
node deployment/release/build-sdn-update-feed.js \
  --out-dir dist/release/update-feed \
  --entry dist/update/darwin-arm64/manifest.json:dist/update/darwin-arm64/update.wasm

# 3. Publish the feed tree, then announce the release with the signals the
#    release workflow signed into it. Without this step nothing is pushed:
#    nodes of 1.1.7 and later find the release at their next feed check.
SDN_UPDATE_FEED_DIR=<published feed folder> \
  spacedatanetwork update signal --from-feed --channel release --version 1.2.3

# On one node by hand instead: fetch, verify, install, restart, health-check.
spacedatanetwork update install
```

The carrier and manifest helpers are also usable standalone:

```sh
node deployment/release/build-update-carrier.mjs \
  --bundle path/to/bundle.tar.gz --out path/to/update.wasm
node deployment/release/sign-update-manifest.mjs \
  --manifest unsigned.json --key key.pem --out manifest.json
```

`update stage` re-runs the full manifest verification (signature against the
bundle trust roots, target, expiration, sequence, bundle/wasm hashes) before
writing anything under `updates/staged/`, and `update apply` re-verifies the
staged files before the atomic swap.
