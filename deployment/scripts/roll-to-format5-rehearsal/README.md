# Isolated systemd rehearsal

This harness is an end-to-end test of `../roll-to-format5.sh`. It requires
Docker with privileged containers, a cgroup mount usable by systemd, Python 3.12
or later on the driver, and enough disk for independent copies of every case.
The image uses Ubuntu 22.04 for the selected architecture, arm64 by default.

The Dockerfile installs systemd, D-Bus, curl, Python and supporting utilities
from Ubuntu's official package archives. Package downloads occur only during
the image build. Image-build failure is a blocked rehearsal. The node's old and new release archives and
each release's own checksum file are explicit inputs; checksum mismatch aborts
before store copies or containers are made.

```bash
python3 run.py \
  --old-archive "$OLD_ARCHIVE" --old-checksums "$OLD_CHECKSUMS" \
  --new-archive "$NEW_ARCHIVE" --new-checksums "$NEW_CHECKSUMS" \
  --arch arm64 \
  --store "$FIRST_STORE_COPY" --store "$SECOND_STORE_COPY" \
  --work-dir "$NEW_REHEARSAL_DIRECTORY" \
  --conversion-minutes 90 --ready-seconds 300
```

Use published v1.1.0 as OLD and v1.1.5 as NEW for the initial deployment test.
Changing `--new-archive` selects another release, with its corresponding checksum
file supplied through `--new-checksums`. The output directory must not exist.
A selective repeat uses `--case` with one or more case names. Start Docker before
running and quit it after the run. The harness stops and removes only containers
it creates. It retains case directories, logs, hashes, ledgers and results.

The source stores are read only. Copies omit `keys`, `secrets` and `auth.db`.
Every case starts its old node from a fresh copy, with a generated test identity,
a stable container hostname, loopback listeners, no host ports and
`--network=none`. The stable hostname allows the release to create a durable
test identity without Docker's ephemeral-hostname refusal. The new release's engine
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
| disk | Insufficient free bytes on the supplied filesystem are refused before stopping. This case is explicitly not run when space is sufficient. |
| script-term / script-hup | Signal to the exact roll-script MainPID during conversion invokes automatic recovery. |

The disk case does not fill a host disk or add a test-only script argument.
Supply a work directory on a genuinely constrained filesystem for that case.
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
