# Isolated systemd rehearsal

This harness is an end-to-end test of `../roll-to-format5.sh`. It requires
Docker with privileged containers, a cgroup mount usable by systemd, Python 3.12
or later on the driver, and enough disk for a case store and record evidence. Completed case stores
and bundles are removed before the following case starts.
The image uses Ubuntu 24.04 for the selected node architecture, amd64 by default.
Systemd, the Python driver and their support libraries run on the build platform's native architecture;
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
  --record-evidence-dir "$PRIVATE_RECORD_EVIDENCE_DIRECTORY" \
  --conversion-minutes 90 --ready-seconds 300
```

Use the binary matching each store's installed build as OLD and the published
selected release archive as NEW. Each old binary has its bundle's `manifest.json` beside it.
The old bundle contains `bin/spacedatanetwork` and that manifest; the new bundle
retains the release archive's launcher and runtime layout. Old binary arguments
and their digests follow the order of the store arguments.
Changing `--new-archive` selects another release, with its corresponding checksum
file supplied through `--new-checksums`. The output directory must not exist.
A selective repeat uses `--case` with one or more case names.
Every repeat uses a fresh output directory, evidence directory and store copy.
Runtime prechecks and early migration runtime refusals use the script's bounded
retry policy. Exhausted retries remain failures; a repeat does not alter results.
Start Docker before
running and quit it after the run. The harness stops and removes only containers
it creates. It retains case logs, hashes, ledgers and results. Completed case
stores, backup directories, bundles and disk images are removed; record evidence
stays in the explicitly supplied private evidence directory.

The source stores are read only. Copies omit `keys`, `secrets` and `auth.db`.
Every case starts its old node from a fresh copy, with a generated test identity,
a generated test password, an engine cache separate from identity material, a stable container hostname,
`Restart=always` with `RestartSec=15`, loopback listeners, disabled DHT and mDNS, no trusted peers, no host ports and
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
| forward | Zero exit, SUCCESS, record-by-record preservation, ready and unchanged identity. A second invocation is refused. |
| interrupted | SIGTERM to the recorded migration PID after the pending directory appears, automatic recovery, ready, unchanged identity and identical fsql4 file hashes. |
| budget | One-minute conversion deadline, automatic recovery and identical fsql4 file hashes. A fast conversion that completes inside the budget fails this injection case. |
| after-switch | A unit pre-start condition rejects the new fsql5 selection. The original bundle restarts after recovery removes that selection. |
| wrong-peer | Wrong expected identity is refused before stopping, with unchanged PID, preserved records and bundle/store selection. |
| disk | Insufficient free bytes on the supplied filesystem are refused before stopping. A bounded ext4 loop filesystem supplies the space constraint. |
| script-term / script-hup | Signal to the exact roll-script MainPID during conversion invokes automatic recovery. |

The disk case creates a sparse ext4 image with capacity equal to the copied
store size plus 2 GiB, mounts it with a loop device, and copies the store into
that filesystem before startup. It checks the real free-space result against
the production formula. It does not fill a host disk or add a test-only script
argument. The image and original copy are removed after the case.

Record preservation uses the node's anonymous `/api/v1/data/scan` endpoint.
The driver waits for startup connector persistence, then scans every standard
through the snapshot cursor, requesting the supported maximum of 50,000 records
per page. Page size does not change the completeness or byte checks. It checks the scan's advertised total against every
record read. Stats counts sum source associations and can exceed unique record
counts; both figures are retained in `scan-versus-stats.json`. Their equality
is not a record-preservation condition. Before-state records stay in size-prefixed FlatBuffer streams in
`--record-evidence-dir`; an index contains SHA-256 content-address digests and
stream offsets. Working indexes live in the container temporary filesystem;
completed baseline indexes are copied into the evidence directory. Comparison
uses a temporary copy so the baseline evidence stays unchanged. The after-state scan looks up every content address and
compares the complete bytes against that stream. Missing occurrences, different
bytes, extra occurrences of an existing address and unexplained additions fail. Counts remain
observations rather than the preservation criterion.

The only allowed additions are startup ICN records: each must name a connector
present before, have an update timestamp after the rollout began, and occur
once per connector. Their number must equal the current invocation's logged
startup persistence count. No additions are allowed when the old invocation
remains running. `ConnectorsHandler.persistLoop` calls `PersistConnectors`
after a 15-second boot delay; `BuildConnectorFrames` builds one record per
provider/source pair from stored summaries and the source ledger and assigns
a fresh `UPDATED_AT` timestamp. This accounts for startup records without
allowing arbitrary changes to existing records.

Format-4 file hashes are taken at the script's explicit rehearsal barriers,
after the original service stops and after the bundle is restored but before
recovery starts that service. Only a newly added
`rollback-auxiliary.flatsqlmeta` is allowed; removed or changed files fail.
The conversion adds that snapshot immediately before activation. The barriers
are disabled unless the rehearsal explicitly supplies its pause directory.

`results.json` summarizes case outcomes. Each case contains `driver.exit`,
`roll.exit`, `roll.log`, conversion attempt logs, `ledger.log`, aggregate `counts-*.json`, file hash lists,
`journal.log` and `result.json`. Peak migration RSS is sampled from `/proc`;
conversion duration is sampled between CONVERTING and SWITCHED ledger events.
All times describe Docker on the test machine. Archive digests are recorded in
`archives.json`. Test-generated identity material stays in its case directory;
do not publish it or copy it into a deployment.

The harness and recovery script require execution on both source copies before
use on a production host. An image-build failure or incomplete case matrix
means the deployment gate has not passed.

## Rehearsal findings

The published v1.1.0 linux/arm64 archive fails to open the supplied format-4
copies and creates format-5 marker bytes under `fsql4` with an empty store.
The two installed old binaries, verified against their supplied digests, each
reach readiness on their matching copied store in the amd64 rehearsal.

The preceding v1.1.5 forward attempts stopped at the runtime check. A subsequent
study of that same verified linux/amd64 archive ran 60 self-tests while idle and
60 during a real conversion. All idle invocations passed. Two concurrent
invocations reported `interruptible_aot=false` and `patched=false`, with no
reported errors; the other 58 passed. Every concurrent invocation was bracketed
by a running converter. The source checks whether cancellation stops every
spawned executor within a short observation window. These observations establish
intermittent failures in this environment, without isolating their cause. The
reported AOT speed ratio does not decide the check's result.

The verified v1.1.6 archive also exercised the early conversion retry: its first
attempt refused the runtime patches after three seconds, and its second entered
conversion. The guard is not bypassed and node code is unchanged. Each retry
requires an empty format-5 destination and shares the original hard deadline.

The first input's post-switch case verifies 45 partitions and reaches SWITCHED
1,414 seconds after CONVERTING. At that point the retained format-4 tree,
including rollback material, contains 7,409,106,490 bytes and format 5 contains
11,421,381,390 bytes. The new unit's deliberate pre-start failure triggers
recovery to the old node, ready with the same running peer. A second invocation
is refused without restarting that node. These measurements describe emulated
Docker execution and are not a forecast for a deployment host.

The preceding rehearsal's aggregate comparisons differed only in ICN counts:
33 additional records on the first input and 11 on the second. Startup journal
lines report exactly those persistence counts, including the precheck controls
that never stop or convert the store. Active-daemon file hashes cannot isolate
conversion changes. The record comparison and stopped-state barriers replace
those measurements; they require a completed rehearsal before they establish
preservation. The earlier failing results remain evidence of those earlier
measurements.

The script is not cleared for deployment. Recovery ledger events alone do not
establish the required preservation proof or a successful new-node forward run.
Each retained case result and exit file determines whether the complete case
passed; an intended fault that was never injected is not a tested recovery leg.
