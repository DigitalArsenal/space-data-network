# Next node release: release-notes text

Text for the notes of the first node release that carries this change. It
replaces the paragraph "Linux: give the node a key password file" that the
v1.1.0 and v1.1.4 release notes carry, and
its three manual recipes for a fresh install; the manual recipe stays for
operators who prefer it (it keeps the file apart from the node's data), with a
umask so the file is never readable by others.

---

**A publication stays available while the node's IPFS repository holds it.** A follower's lane retention removes the files of the batches it no longer keeps as current. On 2026-11-21 host-01 listed four of host-02's five CelesTrak GP batches as `unavailable (shard_missing)`, although it pinned their content and served it over Bitswap. Readers that drop unavailable rows therefore had no earlier batch to fall back to. A shard or index whose file is gone now counts as present when the repository holds all of its content (every block local, with no fetch), and a shard must also be the published size. Such a row lists as `available`, and `read-shard` and `read-index` serve the bytes from the repository.

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

**A publication alert clears once the failed batch's records are published,
and survives a restart.** A `publication_failed` alert of a batch cleared
only when that same batch was published, so the alert of a scheduled run
whose publication failed stayed (health `degraded`) through every later
successful run until the node restarted, and a restart dropped it whether
or not anything was published. The node now records each failed scope in
the store and decides from the store when it clears. A failed batch clears
when a count of the records it holds now finds none unpublished: every one
of them is in a completed publication's export (what a later run's
publication carries when its pull returns those records again), or the batch
holds none any more (expired or superseded); a completed publication of the
whole batch itself clears it at once. The count runs after every
failure in the lane, after every completed publication of a whole batch or
of the whole lane, and at start; while any record is unpublished, the alert
stays and says how many, and says that publishing the batch whole clears it.
Only completed publications count, and only for the set they exported: a
batch that gained records after its publication counts for none of its
records until it is published whole again, and on a lane that publishes the
publication journal's retained selection a record counts only when it was in
that selection. A failed head of a batch or of the lane, a failed
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
