# Isolated systemd rehearsal

This harness is an end-to-end test of `../roll-to-format5.sh`. It requires
Docker with privileged containers, a cgroup mount usable by systemd, Python 3.12
or later on the driver, and enough disk for independent copies of every case.
The image uses Ubuntu 24.04 for the selected node architecture, amd64 by default.
Systemd and its support libraries run on the build platform's native architecture;
node binaries and target runtime libraries retain the selected architecture.
This supports systemd service execution when the emulator rejects the amd64
systemd executor. Container binary-format management is masked so it cannot
replace the Docker kernel's emulator registrations.

The Dockerfile installs systemd, D-Bus, curl, Python and supporting utilities
from Ubuntu's official package archives. Package downloads occur only during
the image build. Image-build failure is a blocked rehearsal. Each source store's old binary and SHA-256, plus the new release archive and
its release checksum file, are explicit inputs; checksum mismatch aborts
before store copies or containers are made.

```bash
python3 run.py \
  --old-binary "$FIRST_OLD_BINARY" --old-sha256 "$FIRST_OLD_SHA256" \
  --old-binary "$SECOND_OLD_BINARY" --old-sha256 "$SECOND_OLD_SHA256" \
  --new-archive "$NEW_ARCHIVE" --new-checksums "$NEW_CHECKSUMS" \
  --arch amd64 \
  --store "$FIRST_STORE_COPY" --store "$SECOND_STORE_COPY" \
  --work-dir "$NEW_REHEARSAL_DIRECTORY" \
  --conversion-minutes 90 --ready-seconds 300
```

Use the binary matching each store's installed build as OLD and published
v1.1.5 as NEW. Each old binary has its bundle's `manifest.json` beside it.
The old bundle contains `bin/spacedatanetwork` and that manifest; the new bundle
retains the release archive's launcher and runtime layout. Old binary arguments
and their digests follow the order of the store arguments.
Changing `--new-archive` selects another release, with its corresponding checksum
file supplied through `--new-checksums`. The output directory must not exist.
A selective repeat uses `--case` with one or more case names.
`--retry-before-roll --case forward` reuses a failed forward setup only when
no rollout ledger or format-5 selection exists and the old binary still matches
its verified digest. Previous results and logs move into an `attempt-*` directory.
The test identity remains in place. Other cases always use fresh copies. Start Docker before
running and quit it after the run. The harness stops and removes only containers
it creates. It retains case directories, logs, hashes, ledgers and results.

The source stores are read only. Copies omit `keys`, `secrets` and `auth.db`.
Every case starts its old node from a fresh copy, with a generated test identity,
a generated test password, an engine cache separate from identity material, a stable container hostname,
loopback listeners, disabled DHT and mDNS, no trusted peers, no host ports and
`--network=none`. The stable hostname allows the release to create a durable
test identity without Docker's ephemeral-hostname refusal. These releases
replace an empty bootstrap list with public defaults. A synthetic peer identity
at unused loopback port 9 suppresses that fallback without naming a real peer.
Docker's network namespace has no external interface. The loopback admin
listener has `require_auth: false` to expose aggregate stats without credentials.
Provider flow services are explicitly empty, so prewarm checks only the
engine artifacts required by the isolated node. The running peer identity comes from the current systemd invocation journal.
`--cache-dir` reuses compiled engine artifacts across cases; identity material
is never placed in that directory. The new release's engine
is prewarmed before stopping the old node. No runtime permission patch or fake
systemctl is used. A failure to start the old release is recorded as a failed
case and requires inspection; the harness does not replace the release or
silently create an empty store.

| Case | Required evidence |
| --- | --- |
| forward | Zero exit, SUCCESS, identical per-standard public stats, ready and unchanged identity. A second invocation is refused. |
| interrupted | SIGTERM to the recorded migration PID after the pending directory appears, automatic recovery, ready, unchanged identity and identical fsql4 file hashes. |
| budget | One-minute conversion deadline, automatic recovery and identical fsql4 file hashes. A fast conversion that completes inside the budget fails this injection case. |
| after-switch | A unit pre-start condition rejects the new fsql5 selection. The original bundle restarts after recovery removes that selection. |
| wrong-peer | Wrong expected identity is refused before stopping, with unchanged PID, fsql4 hashes and bundle/store selection. |
| disk | Insufficient free bytes on the supplied filesystem are refused before stopping. A bounded ext4 loop filesystem supplies the space constraint. |
| script-term / script-hup | Signal to the exact roll-script MainPID during conversion invokes automatic recovery. |

The disk case creates a sparse ext4 image with capacity equal to the copied
store size plus 2 GiB, mounts it with a loop device, and copies the store into
that filesystem before startup. It checks the real free-space result against
the production formula. It does not fill a host disk or add a test-only script
argument. The image and original copy remain as evidence.
Copied-store hashes are taken after the old node first becomes ready. The node
can perform background maintenance; a hash mismatch is reported as failure,
not waived. The public `/api/v1/stats` response supplies schema/count aggregates;
an empty response or a changed count fails rather than claiming preservation.

`results.json` summarizes case outcomes. Each case contains `driver.exit`,
`roll.exit`, `roll.log`, `ledger.log`, aggregate `counts-*.json`, file hash lists,
`journal.log` and `result.json`. Peak migration RSS is sampled from `/proc`;
conversion duration is sampled between CONVERTING and SWITCHED ledger events.
All times describe Docker on the test machine. Archive digests are recorded in
`archives.json`. Test-generated identity material stays in its case directory;
do not publish it or copy it into a deployment.

The harness and recovery script require execution on both source copies before
use on a production host. An image-build failure or incomplete case matrix
means the deployment gate has not passed.

## Release baseline result

The published v1.1.0 linux/arm64 archive, verified against its release checksum
file, fails to open both supplied format-4 store copies. Startup reports
`P4_E_FORMAT` and `flatsql_init returned status -5` before readiness. The same
archive reaches readiness with an empty store in the same systemd image.
That control creates `fsql4/STORE` with bytes 4 and 5 equal to `05 00`, while
both supplied stores contain `04 00`. The binary reports version 1.1.0.
No store marker is relabeled and no node code is changed.

The old-node baseline gate has failed. Forward conversion, failure recovery,
repeat invocation and precheck cases have no end-to-end result on these copies.
The v1.1.5 archive checksum passes, but its conversion and recovery behavior
remain unverified. The script requires a passing baseline and complete case
matrix before deployment.
