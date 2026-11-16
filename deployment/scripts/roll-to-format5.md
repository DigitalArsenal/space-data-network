# Roll a stopped format-4 store to format 5

`roll-to-format5.sh` replaces a complete bundle, converts the store, and checks
readiness and identity. A failure after stopping the service triggers automatic
recovery. The script requires Linux, Bash, GNU coreutils, systemd and curl, and
runs as root. The service also runs as root. No schema or record data is printed
by the script. Conversion output comes from the node binary.

The release-source recovery contract is verified against v1.1.5. End-to-end
validation on production-store copies is required before deployment. The
included rehearsal installs systemd from official package archives; syntax checks do not
establish recovery safety.

## Preparation

1. Acquire the deployment lock with the deployment's exclusive lock procedure.
   Record the holder, token and manual-install reason in the deployment ledger.
   Keep that lock through recovery or success. The script records the supplied
   holder and token; it does not acquire or release an external lock.
2. Download the complete release archive, verify it against the release checksum
   file, and unpack it without preserving archive ownership. Verify the manifest,
   platform, version and trust roots. Stage it on the same filesystem as the
   bundle base. The live folder is named `sdn-bundle`. The store is outside both
   bundle directories. Neither bundle is an ancestor of the other.
3. Check the service configuration and store root agree. Check any module catalog
   hashes required by the deployment. Run `version`, `show-identity -c CONFIG`,
   and `key fingerprint-check` with the service's environment. Never add
   `--show-mnemonic`. Record the running daemon's peer ID.
4. Run the staged binary's `prewarm-aot --config CONFIG` as root with the
   service's environment and cache HOME. Record the old binary checksum.
5. Ensure the ledger is writable and private. Ensure the ready URL is the node's
   unauthenticated ready endpoint and is usable by both releases. A local URL
   avoids a proxy's cached response. Ensure the current invocation's journal
   contains the node's `Peer ID:` startup line.

The script reads `systemctl show -p Environment --value` without printing it.
It parses quoted words without evaluating shell code. Unsupported escape
sequences, non-root services and `EnvironmentFile` settings are refused before
the outage. Supply the service's HOME as the final argument when it differs
from the default. Explicit HOME takes precedence over the unit's Environment.
The service's PATH is honored when present; the default is the system PATH.
Service namespaces and filesystem isolation need equivalent command access:
configuration and store paths must identify the same files outside the unit.

The disk gate requires free bytes of at least:

```
3 * du_bytes(fsql4) + bytes(auxiliary.flatsqlmeta) + 1073741824 + 5000000000
```

An auxiliary metadata file is required because this procedure promises its
recovery snapshot. Symbolic links for the live bundle, fsql4 and auxiliary file
are refused. Check all paths and budgets before the outage.

## Invocation

Set the variables to the deployment's values, then start one transient service:

```bash
systemd-run --unit="$ROLL_UNIT" --collect \
  /bin/bash "$SCRIPT" \
  "$NODE_UNIT" "$BUNDLE_BASE" "$STAGED_BUNDLE" "$CONFIG" "$STORE_ROOT" \
  "$READY_URL" "$EXPECTED_PEER" "$LEDGER" "$HOLDER" "$TOKEN" \
  "$CONVERSION_MINUTES" "$READY_SECONDS" "$NODE_HOME"
journalctl -u "$ROLL_UNIT" -f
```

The first budget is a positive whole number of minutes; the second is a positive
whole number of seconds. The readiness budget applies separately to the new
node and to recovery. Stop waits are bounded at 90 seconds. Conversion is
supervised as a child of the script; its output remains attached to the script.
At the conversion deadline the script kills and waits for that exact child
before inspecting the switch. It does not leave an independent migration job.
The conversion command includes `--store STORE_ROOT` as well as `--config` to
bind the conversion to the checked root.

SIGTERM, SIGHUP and SIGINT sent to the script invoke the same recovery path.
SIGKILL, power loss, a kernel failure or the loss of the filesystem prevents
shell traps from running. The transient service detaches the outage from the
operator's login session. Do not stop the entire transient unit to simulate a
signal to the script: systemd can also terminate its children.

## Ledger

Every event is appended as one UTC line and also written to standard output:

```
<UTC> <holder> <EVENT> token=<token> <detail>
```

| Event | Meaning |
| --- | --- |
| PRECHECK | Read-only checks started, apart from the ledger and the identity command's permission enforcement. |
| DISK | Required and available byte counts. |
| RUNTIME_CHECK | The staged binary runs `substrate-selftest --require-patched` with the service environment. |
| RUNTIME_CHECK_OK | The staged runtime passed all required checks. |
| RUNTIME_CHECK_FAILED | The staged runtime refused; the service remains running. |
| PRECHECK_OK | Environment, identity, path, space and runtime checks passed. |
| REHEARSAL_PAUSED | An explicitly enabled rehearsal barrier awaits acknowledgment. |
| STOPPED | MainPID is zero and the unit is inactive or failed. |
| SWAPPED | The old bundle is retained and the staged bundle occupies the live name. |
| BACKED_UP | Stopped-state side files are copied beside the store. |
| CONVERTING | Exact conversion PID and time budget. |
| SWITCHED | Format bytes are `05 00`; fsql4 and its auxiliary snapshot exist. |
| SUCCESS | The new service answers ready and its invocation journal has the expected peer ID. |
| FAILED | A checked step failed; recovery follows if stopping began. |
| PRECHECK_FAILED | No outage began. |
| ROLLBACK | Recovery started; `after_switch` records whether fsql5 exists. |
| ROLLED_BACK_BEFORE_SWITCH | The original bundle is serving format 4 with the expected identity. |
| ROLLED_BACK_AFTER_SWITCH | Format 5 is retained under a failed name, side files and auxiliary metadata are restored, and the original bundle is serving. |
| ROLLBACK_FAILED | Recovery stopped at the named step. Human intervention is required. |

Argument and path-resolution errors can exit before ledger initialization.
Success exits zero. Refusal, conversion failure and completed recovery exit
nonzero. A failed recovery also exits nonzero.

## Recovery artifacts

The original bundle becomes `sdn-bundle.old-<UTC>-<PID>`. Recovery moves the
replacement to `sdn-bundle.failed-<UTC>-<PID>` and restores the original live
name. Side files are copied into `STORE_ROOT.roll-backup-<UTC>-<PID>`:
`dataset-catalogs`, `dataset-feed-head-sync`, `control.flatsqldb`,
`fsql4-migrate.json`, `source-metrics.db`, `storefront.db`, `peers.db`,
`trusted-import-lanes.json` and `homepage.json`, when present. SQLite `-wal`,
`-shm` and `-journal` companions of the three `.db` files are included.
`keys`, `secrets` and `auth.db` are excluded.

After a switch, recovery moves `fsql5` to `fsql5.failed-<UTC>-<PID>` and copies
`fsql4/rollback-auxiliary.flatsqlmeta` over the root auxiliary metadata. Side
files from the failed run are moved to the backup's `changed` folder before
restoring their originals, including original absence. No retained store or
bundle is deleted. Records accepted after conversion are absent from the old
store. Quiesce external ingestion during the deployment window.

The v1.1.5 converter removes its unpublished candidate when returning an error;
an abruptly terminated converter can leave `.store-format5-pending`. A subsequent
conversion reclaims only a candidate whose ownership marker matches the source.
The script leaves this candidate to the converter. A termination immediately
before the atomic switch can leave the auxiliary rollback snapshot in fsql4.

A repeat with the original arguments is refused: after success fsql5 exists and
the staged directory is consumed; after recovery the staged directory is also
consumed. A deliberate retry requires a newly staged bundle and fresh checks.

On `ROLLBACK_FAILED`, retain the lock and all artifacts. Read the named step,
ledger and service journals. Determine which bundle and store the unit selects
before starting it. Resolve the failed operation and complete the documented
recovery sequence manually. Do not delete fsql4, relabel a STORE marker, rerun
the script over partial recovery, or overwrite the preserved bundles.

## Rehearsal

See [the rehearsal harness](roll-to-format5-rehearsal/README.md). Its image runs
real systemd as PID 1. It uses isolated copies, generated test identities and no
external network. A passing report includes old/new per-standard counts, file
hashes, exit codes and the ledger for each store and failure case. Docker
measurements describe that environment and do not predict host outage times.

## Rehearsal barriers

`SDN_ROLL_REHEARSAL_PAUSE_DIR` enables two barriers in a dedicated, empty
absolute directory: `stopped.waiting` appears after the old service stops,
and `restored.waiting` appears after recovery restores the bundle, before the
old service starts. The rehearsal driver hashes fsql4 at these boundaries
and creates the corresponding `stopped.continue` or `restored.continue` file.
Each barrier has a five-minute limit. A stopped-barrier timeout triggers
recovery; a restored-barrier timeout records `ROLLBACK_FAILED`. Existing
barrier files are refused before the outage. Leave this variable unset in
production; the default has no pause. The directory belongs to the rehearsal
and contains no store or identity files.

The precheck measures the staged process. The migration process repeats its
own runtime guard, so a later refusal still invokes recovery. The self-test's
reported AOT speed ratio is diagnostic; the runtime's pass predicate checks
thread interruption, atomic operations and native execution. No threshold or
runtime check is weakened by this script.
