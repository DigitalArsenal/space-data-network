# Next node release: release-notes text

## 1.1.13: followed lanes cost their producer less; a lane can be imported from its publications alone

Since 1.1.10 a node imports a lane only if it subscribes to it, and a subscription follows the lane over a stream from its producer. On the production catalog lanes (`OMM` and `MPE` `space-data-network-02` / `celestrak-gp`: 10 to 12 million records in hundreds of batches, a new batch of about 33,000 records every 3 hours) the stream's requests cost the producer reads of the whole lane, and starved its own publication. Those two lanes were unsubscribed on the follower host, and its catalog stopped updating.

- **A lane's statement reads no record.** The producer answers it from its counters (milliseconds; 28 to 38 s on a copy of the production store before).
- **A follower backs off.** After a stream that delivered nothing, the wait before it opens again doubles from 1 s to 20 minutes, each wait drawn within 20% of it (it stopped at 30 s). The statement is waited for 5 minutes (60 s before), so a 1.1.12 producer answers once instead of being asked again.
- **A failed hold of the producer's current batch backs off too.** When the batch's list cannot be read or its records cannot be fetched, the batch is asked for again under the same backoff, at least 30 s after the last ask, while the stream goes on (it was asked again every 30 to 40 s for as long as it failed). A `busy` refusal's retry hint is waited at most 20 minutes.
- **`sync subscribe --publications only`** imports a lane from the producer's signed publications alone. The node opens no stream for the lane and asks the producer nothing for it (no statement, no batch list, no page). Each publication is fetched and imported as it comes, under the same disk and record checks, with the lane's retention rule; at start the node imports the newest publication of the lane it had received and not imported. A publication it did not receive (made while it was stopped or out of reach) is not fetched afterwards; the lane is current again at the producer's next publication. A `sync subscribe` for the lane without `--publications` is refused, so the producer is not asked for its statement by mistake. `--start` is refused with it. `sync subscriptions` shows the lane as "publications only, no stream" (`publications_only` in `--json`); the API is `POST /api/v1/sync?publications=only`. On a subscribed lane it changes only that; `--publications keep` or `stream` afterwards starts the stream at the producer's current batch.
- **The producer logs each read a follower asks of a lane** (statement, batch list page, records by content address, followed stream opened), with its duration.

Following a lane of several million records as a stream is not cheap for its producer: each new batch a follower holds still costs a walk of the whole lane per 8,192 records of the batch's list, on the engine lane the producer's own publication uses, and the producer does not stop such a read when the follower gives up. Subscribe to such a lane with `--publications only` when its producer publishes it. Its records then arrive with each publication instead of within seconds.

`sync subscribe` prints the producer's lane figure as "records counted per batch": it counts batch memberships, and a record two of the lane's batches hold counts in each (3.6 times the distinct records on a copy of the production `OMM` lane).

**Rollback.** A publications-only subscription carries `stream=none` in its `SYNC_FILTER`. 1.1.10, 1.1.11 and 1.1.12 do not know the field `stream`, so they hold such a subscription without running it (alert `subscription_refused`): no stream is opened for it and its publications are not imported until the node runs 1.1.13 again. The node's other subscriptions run. `follows.dss`, which 1.1.9 reads, never lists such a lane.

---

Text for the notes of the first node release that carries this change. It
replaces the paragraph "Linux: give the node a key password file" that the
v1.1.0 and v1.1.4 release notes carry, and
its three manual recipes for a fresh install; the manual recipe stays for
operators who prefer it (it keeps the file apart from the node's data), with a
umask so the file is never readable by others.

---

**A publication is held to the sizes its signed manifest states, and three smaller fixes to subscriptions.**

- A `$PNM` publication's shard and index are fetched up to the bytes its manifest states and no further. An index that lists more records than stated stores none. A manifest that states no records, or a shard of 0 bytes, is refused. Such a publication raises the alert `publication_misstated` on the lane, naming both figures, and is not fetched again.
- A stated size that no disk can hold stops the lane (`follow_stopped`) without overflowing the reservation all lanes share.
- A subscription whose `SYNC_FILTER` this build cannot read is refused alone (alert `subscription_refused`); the other lanes run, and the refused record is written back byte for byte.
- After a restart a lane's publications wait at most 5 minutes for its stream, whatever else runs the catch-up.

**Linux: a fresh node gets its key password file automatically.** The Linux
kernel makes `/sys/class/dmi/id/product_uuid` readable only by root, so on
most Linux machines a node that does not run as root cannot derive its
key-at-rest password from the machine. Before this release such a node
refused its first start until the operator made a password file by hand. Now
the installers do it for a fresh node: no identity yet, no `SDN_KEY_PASSWORD`
or `SDN_KEY_PASSWORD_FILE` and no key password in the config, and a machine
fingerprint that is refused for the node's account (`spacedatanetwork key
fingerprint-check` says `ok`, `absent` or `refused`). The file holds 32 random
bytes as 64 hex characters, mode 0400, owned by the node's account, and is
never overwritten. **Keep it for the life of the node: the node cannot open its
identity without it.** Where it is kept decides what a copy of the node's
data opens:

- `install.sh` as a regular user: `~/sdn-key-password`, outside
  `~/.spacedatanetwork`, recorded in the node's config as
  `security.key_password_file`, so every later start and every CLI command
  finds it with nothing exported. A copy of `~/.spacedatanetwork` alone does
  not open the identity; back the file up apart from it.
- `.deb` / `.rpm`: `/var/lib/spacedatanetwork/key-password` (`sdn:sdn`, the
  path of the v1.1.0 recipe) and the drop-in
  `/etc/systemd/system/spacedatanetwork.service.d/key-password.conf` that sets
  `SDN_KEY_PASSWORD_FILE` for the service. **The file is in the data
  directory: a copy of `/var/lib/spacedatanetwork` alone opens the node
  identity and its credential store.** Move it (and change the drop-in) to
  keep them apart. CLI commands need the variable too: `sudo -u sdn env
  SDN_KEY_PASSWORD_FILE=/var/lib/spacedatanetwork/key-password
  /opt/spacedatanetwork/bin/spacedatanetwork -c
  /etc/spacedatanetwork/config.yaml <command>`. The file is made at package
  install, so a machine image built with the package installed gives every
  clone one password: remove the file and the drop-in on each clone and
  reinstall the package before its first start. Removing or purging the
  package leaves both, like the identity.
- Container (uid 1000): `/app/data/key-password`. **It is in the data volume:
  a copy of the volume alone opens the node identity and its credential
  store.** A password file mounted from outside the volume with
  `SDN_KEY_PASSWORD_FILE` keeps them apart, and the entrypoint then generates
  nothing; that remains the recommended setup. Nothing is generated when the
  config keeps the key directory outside `/app/data`. `docker exec` does not
  pass through the entrypoint, so run CLI commands as `docker exec <container>
  /app/entrypoint.sh <command>`, for example `show-identity --config
  /app/config/full-docker.yaml --show-mnemonic`.

A node that already has an identity is not touched by an upgrade (its
identity stays sealed exactly as it is), and a node running as root, on macOS
or on Windows needs no file. A command refused on such a node now names the
generated file and the command line that passes it, and a first start that is
still refused says that no identity exists yet, why this host's fingerprint is
refused, and how to proceed, instead of saying a secret failed to open.

**The credential store follows the identity's key password.** It now uses
`SDN_KEY_PASSWORD`, `SDN_KEY_PASSWORD_FILE`, `security.key_password`,
`security.key_password_file`, in that order, so "credential store
unavailable" no longer appears on nodes whose identity needs a password file.
A store that an earlier release sealed under the machine key while the
node's password was in a file stays sealed that way and keeps opening, so a
rollback still opens it; only where the machine key is now refused is it
re-sealed under the password. A store this release CREATES on a node with a
key password file is sealed under that password, and an earlier release
(after a rollback, or the update helper's automatic revert) reports
"credential store unavailable" for it until the node runs this release again.

**CLI config discovery ignores other accounts' processes.** A CLI command
given no `-c` and no `SDN_CONFIG` used to take its config from any running
process whose command line read `spacedatanetwork daemon --config ...`,
whoever ran it, so another local account could choose the config that
`init` loaded and saved. Only a daemon run by the same account, by root or by
a system account (such as the package's `sdn`) counts now; for another
account's node, pass `-c`. `install.sh` passes the node's own config to every
command it runs.

**A manual run is no longer cut off when its command stops waiting.**
`spacedatanetwork apps run` gave up after about 65 seconds ("daemon
unavailable") and the node then cancelled the run ("context canceled"), so a
flow that needed longer never finished by hand. A manual run now belongs to
the node: it runs to its end whatever the command does, within a bound (the
flow service's `execution_timeout`, or 2 hours, including any wait for its
`exclusive_group`). The node's stop cancels it and answers a request waiting
for it at once. The command waits and
says every 30 s that the run is still going; `--timeout <duration>` bounds
the wait (the command then prints how to see the result and exits non-zero,
and the run continues), `--detach` returns at once, Ctrl-C stops waiting
(the run continues; `--cancel-on-interrupt` cancels it instead). The new
`apps status <app-id> [trigger-id]` shows a run's state and result, and
`apps cancel <app-id> [trigger-id]` cancels a running one; a cancelled run
gives back its attempt in the provider's minimum interval, so the next
manual run starts at once. A second `apps run` of a schedule that is running
joins that run; `--force` does not apply to a run started without it, and
the command says so. `apps run` follows its own run by id: when the node no
longer knows it (a restart), the command says so and exits non-zero instead
of reporting another run. Scripts use the same route:
`POST .../schedules/<trigger-id>/run?detach=1`, then `GET ...?id=<id>`;
`DELETE ...?id=<id>` cancels that run (404 when it is not the one running,
so a cancel never ends another run), `DELETE` with no id cancels whichever
runs. `apps run --cancel-on-interrupt` cancels its own run only. The
dashboard's Run now waits for the run as before; a page closed meanwhile no
longer cancels it.

**A publication alert is decided from the store, clears when its batch is
published or gone, and survives a restart.** A `publication_failed` alert
of a batch cleared only when that same batch was published, and a restart
dropped it whether or not anything was published. The node now records each
failed scope in the store and decides from the store when it clears. A
failed batch clears when a completed publication of the whole batch exported
the set it holds now (equal fingerprints), or when the batch holds no
records any more (a replace-current lane superseded them, they expired, or
the lane was retracted). A publication of another batch never clears it: if
every record of a failed batch also sits in other, published batches, the
alert still stays until that batch is published whole or its records go,
and publishing the batch whole clears it. The batch is checked at each of
its failures, after each completed publication of the whole batch, when a
supersede or an expiry removes records of its lane, and at start once the
node answers ready; a check costs one fingerprint of the failed batch,
whatever the size of the lane. A completed publication of the whole batch
clears it when it is reported; the check after it raises the alert again
when records arrived in the batch while that publication ran. A retraction
of the lane clears every failed scope of the lane. A batch the snapshot
guard refused says what clears it (a newer batch superseding it, or the
guard no longer refusing it) instead of advising a publication the guard
refuses again. A failed head of a batch or of the lane, a failed
publication of the whole lane, a failed announcement of existing
publications, and a failed batch of a datastore lane clear when the same
operation completes again; a publication of a head never clears a failed
batch. The windows a publication wrote before it failed partway are never
taken for a publication of the batch and are not announced again by an
announcement of existing publications. At most 16 failed batches are tracked
per lane; beyond them the alert says so and stays until a publication of the
whole lane completes.

**Retention releases the windows of a failed publication.** Node 1.1.11 and
earlier adopted the windows a publication wrote before it failed (rows, pins
and the announcements already sent) as a series of their batch at the lane's
next completed publication, and kept them like any other series. They are
now marked unfinished when written and never adopted, so the lane's retention
(`publishing.retention` with `keep_series` set) retires their rows and
unpins their shard, index and manifest CIDs at the lane's next completed
publication, as it does for any row no series names. With `keep_series: 0`
nothing is released, as before. A subscriber that fetched such a window from
its announcement keeps its copy; the producer no longer serves it once
retention released it. Windows an earlier release already adopted stay
adopted.
