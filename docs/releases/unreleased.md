# Next node release: release-notes text

**After this update a node imports only the lanes it subscribes to.**
`peers.trusted_import` and the five-minute shard catch-up are gone;
subscriptions are `$DSS` records (`subscriptions.dss`). A node updated from
1.1.9 converts its lane ledger (`trusted-import-lanes.json`) and its
`peers.trusted_import` allow list into subscriptions on its first start, each
from the trusted peer the lane's records came from and starting at that
peer's current batch, and records what it converted in
`<storage.path>/trusted-import-converted`. The ledger and the config are
left in place, and the converted subscriptions are kept where 1.1.9 does not
read them, so an update that rolls back to 1.1.9 by itself imports as 1.1.9
did and reads no lane from its beginning. Every start names each removed key
still in the config (log and the alert `subscription_conversion`); a new node
whose config carries `peers.trusted_import` refuses to start.

- No subscription is read from a lane's beginning unless it was made with
  `--start beginning` (or by a `keep-all` or `archive-all` lane's rule, after
  its size check). A producer that refuses a saved cursor (a rebuilt index or
  store) gets the lane started again at its current batch, with the alert
  `follow_cursor_refused`. On a lane whose history the node keeps, the
  records not read are kept with the subscription (`sync subscriptions`:
  "not read") until `sync subscribe --start beginning` reads the lane again
  or `sync subscribe --acknowledge-unread` accepts the gap.
- Each followed page, each fetch by content address of a current batch, and
  each `$PNM` publication import is checked before it is stored, against one
  reservation that all lanes share (released when each store returns, also
  when it fails): a lane stops, its cursor kept, before the store would pass
  `subscriptions.record_ceiling` or leave less than 1 GiB of free disk under
  `storage.path` (alert `follow_stopped`), and resumes once the page fits.
  `--force` skips only the check before the subscription.
- A record gossiped on a schema topic (`POST /api/v1/pubsub/publish`, the
  sdn-js `publish`) names no lane and is no longer stored by any node,
  whatever its trust of the sender or `network.accept_push_from`. Signed
  `$PNM` announcements (node identity, dataset publications) are unchanged.
- A lane whose followed stream is connected no longer fetches its
  publications' shards as well (an `archive-all` lane still keeps them),
  once it holds the producer's current batch; a lane whose stream the
  producer refuses, or whose current batch it cannot hold, imports through
  them. After a restart a publication waits (at most 5 minutes) until the
  lane's stream has held its batch again or failed to, so it is not imported
  a second time.
- A producer serves a peer it does not trust at most 8 followed streams
  (64 for all such peers); a node following more lanes of one producer is
  listed by it in `peers.trusted_peers` (16 streams, outside the 64),
  unless the producer may run 1.1.9 again: 1.1.9 imports every lane of a
  peer it trusts.
- `subscriptions.dss` and `follows.dss` are written `follows.dss` first. A
  file whose bytes do not decode at start is moved aside, not overwritten; a
  file whose read fails (permission, I/O) is left in place and read again at
  the next start; either way the node imports no lane and changes no
  subscription until the file reads (alert `subscriptions_unreadable`). A
  subscription naming a field this release does not know (a later version
  wrote it) is not run and kept as it is (alert `subscription_refused`); the
  other lanes run. An unsubscribe made under 1.1.9 during a rollback comes
  back with the next start of this release. `sync unsubscribe` clears the
  lane's alerts.

- `spacedatanetwork sync subscribe --schema <standard> --provider-id
  <provider> --source-name <source> --from <producer peer id>` subscribes,
  signed in to the running daemon with the node's root key. The first read is
  bounded by the lane's rule: a `replace-current` lane starts at the
  producer's current batch, a `keep-all` or `archive-all` lane at its
  beginning (`--start` overrides). The command prints the producer's size of
  that first read and the node refuses it when its free disk or its record
  ceiling (`subscriptions.record_ceiling`, default 32,000,000) cannot take
  it, unless `--force`. `sync subscribe --from-ledger` plans (and with
  `--yes` makes) the subscriptions of a 1.1.9 ledger and allow list. `sync
  unsubscribe` and `sync subscriptions` (each lane's producer, start time,
  records and bytes received, current batch and lag) complete the set; `sync
  status` reads through a running daemon instead of failing on the store it
  holds.
- Removed with the old gate: the feed-head announcements and their topic,
  the trust-promotion catch-up, the `$DSS` `Sync` action (refused: a followed
  lane is always current) and `security.require_signed_feed_heads`. A signed
  `$PNM` publication still imports, for a subscribed lane only.
- The lane statement is new on the wire (`read-chunk` with `PROJECTION`
  `STATEMENT`). A 1.1.9 producer answers it with an error: a new subscription
  to it is refused (`--force --start beginning` follows it unsized), and a
  converted subscription imports the lane's `$PNM` publications and waits to
  start its stream until the producer runs this release. A 1.1.9
  follower of a producer on this release keeps following: the followed
  stream is unchanged.
- `docs/SUBSCRIPTIONS.md` has the commands, the first-read rules, the
  upgrade and the API.

---

Text for the notes of the first node release that carries this change. It
replaces the paragraph "Linux: give the node a key password file" that the
v1.1.0 and v1.1.4 release notes carry, and
its three manual recipes for a fresh install; the manual recipe stays for
operators who prefer it (it keeps the file apart from the node's data), with a
umask so the file is never readable by others.

---

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

**A rare crash of a Linux node is fixed.** The node's WebAssembly runtime
detached threads that were already running: every call with a deadline (the
storage engine's control calls among them), every WebAssembly thread spawn,
and every compile of a module to native code (four at each start, and each
time the engine's compiled code is not cached). In glibc through at least
2.42 (sourceware bug 19951, fixed on glibc master, December 2025) detaching a
thread that is exiting at that moment can read memory that has just been
freed, and the node ends with a segmentation fault. Every supported Linux
host was exposed, Ubuntu 24.04 (glibc 2.39) included. The runtime now starts
these threads detached and links compiled code on one thread, so it never
detaches a running thread. `spacedatanetwork substrate-selftest` reports the
runtime's patch series as `patch_series` (10).

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

**Retention releases the windows of a failed publication.** Node 1.1.9
adopted the windows a publication wrote before it failed (rows, pins and the
announcements already sent) as a series of their batch at the lane's next
completed publication, and kept them like any other series. They are now
marked unfinished when written and never adopted, so the lane's retention
(`publishing.retention` with `keep_series` set) retires their rows and
unpins their shard, index and manifest CIDs at the lane's next completed
publication, as it does for any row no series names. With `keep_series: 0`
nothing is released, as before. A subscriber that fetched such a window from its
announcement keeps its copy; the producer no longer serves it once retention
released it. Windows that 1.1.9 already adopted stay adopted.

**Nodes no longer all restart for an update at the same moment.** A node
acts on an update it accepted (from a signal or from its feed check) at its
own time within `update.rollout_window_seconds` (default 1800, 30 minutes;
`0` acts at once, at most 86400). The delay is fixed by the node's peer id and the
update id, so two nodes almost never share one, and the node keeps the time it
accepted the update and its due time in `updates/pending.json`: a restart
during the wait keeps the due time. The node's log says when it acts and why
(`acts on it at <time> (rollout window ..., this node's delay ...)`), and
`spacedatanetwork update check` and `spacedatanetwork status` print
`update_pending=<update id> ... due_at=<time>`. `spacedatanetwork update
install` typed on the node still installs at once. The release signer can sign
a window into the signal (`sdn-update-signal-signer --rollout-window-seconds
N`, or `update sign-signals --rollout-window-seconds N`); it replaces the
node's setting for that update. The release workflow has an input,
`rollout_window_seconds` (default 1800), that it signs into every signal of
the release, so the publisher sets the pace of each push (the workflow's first
job refuses a value that is not a whole number from 0 to 86400, and both
signers refuse a negative `--rollout-window-seconds` instead of signing no
window); a node that finds
the release by its feed check (the feed carries no window) uses its own
setting. Nodes of 1.1.9 and earlier verify such a signal and ignore the
window: they act at once unless their own `update.max_delay_seconds` spreads
them. `update.max_delay_seconds` is replaced by
`update.rollout_window_seconds`; a config that still sets it has that line
ignored. The delays of two nodes are independent, so they can be down at the
same time: two nodes that each restart in five minutes overlap about 31% of
the time with the 30-minute default (16% with 60 minutes, 75% with 10
minutes, always with five minutes or less). `update install` typed on a node
installs at once; an operator who must not have two hosts down together
installs on one by hand first and checks `update fleet` before the push.
While a node waits, a wall clock set back does not lengthen the wait beyond
the window, and a pending update that is refused (for example quarantined
after a typed install of it was rolled back) is no longer reported as
pending.

A newer signal that arrives while a node waits for an older update ends the
wait and is handled under its own window (a window of 0 acts at once); the
older update is not installed. At the due time the node reads its feed index
again and does not install an update the index no longer lists. A pending
signal that expired while the node was down is cleared with a warning at the
next start, and the node checks its feed at once; the remaining wait after a
restart is never longer than the window. The node that published a signal
sends it again, for an hour, when a node that was part of its fleet at that
moment joins its update topic again and does not report having it, so a node
that was restarting when the signal went out still gets it; a node new to the
publisher, or one that comes back later, gets the release from its feed
check.

**Which version every node runs: `spacedatanetwork update fleet`.** Each
node now publishes its own update status on the update topic of its channel,
signed by its peer key: its version, an update it waits to act on and when,
an update that failed on it and whether it was rolled back (from its own
deploy ledger), and the newest signal it accepted. `spacedatanetwork update
fleet` prints one row for this node and for every SDN node it knows: name (as
the node publishes it in the peer directory), the end of its peer id,
version, where it comes from (`status`, the node's own statement, which this
node does not check, or `identify`), when it was last seen, its update state
(`current`, `pending <version> until <time>`, `failed <version>, rolled back
(<reason code>)`, or `not reporting`), and whether it accepted the newest
signal this node verified itself or already runs its sequence. `--json`, and
`GET /api/peers/versions` on the admin API (administrators only, like the
other `/api/peers` routes), give the same data. A failure is published as a
reason code from a fixed list; its text stays in the node's ledger. Every
peer on the topic reads a node's status, including when it will restart for
a pending update; `update.publish_status: false` (default true) publishes
none. A node accepts and relays a status only from a node it has identified
itself as an SDN node, dated within five minutes of its clock, at most six at
once and then one every ten seconds per node, with every field from a fixed
character set and length (at most 2 KiB in all); it drops a status not
renewed for 30 minutes and ignores a status no newer than the one it holds.
Peers known only by their identify agent string (which any peer chooses)
are bounded: at most 1,024 such authors active within a minute, and at most
512 of their statuses held, first come: a new such author's status is not kept once 512
are held. Pinned peers (`peers.trusted_peers` too), `network.bootstrap`
peers, peers the registry trusts and peers whose status the node already
holds are outside those bounds, so a flood of new identities cannot remove
the nodes already shown. A publisher's own fleet belongs in its pinned
(`peers.trusted_peers`) or `network.bootstrap` peers, so its rows are always
kept. `not reporting` is a node of 1.1.9 or earlier, a node
that does not publish, a node reached only through other nodes, or a status
that expired; nodes of 1.1.9 and earlier drop these status messages unread
and do not relay them, so two nodes joined only through them do not see each
other's status. The last-seen time of a connected peer on `/api/peers/sdn`
is now kept current: it stopped at the time the connection was made. The
continuous-integration proof that an installed, published node takes a
pushed update by itself runs the published 1.1.9 archive; it ran the 1.1.5
archive before, and 1.1.5 is no longer checked.

**An interrupted home install still leaves the new build on trial.** A node
that updates into its home folder wrote the new build's state and then its
trial record; stopped between the two, it ran the new build at the next start
as if it had passed its trial. The state write now marks the build as on
trial, and the next daemon start writes the missing trial record from it.
The opposite case is covered too: a build that answers ready records its
pass in one state write before it removes its trial record, and a start after
a stop between those steps finishes the pass, so a build that passed is never
tried again (a second trial could roll a good build back on a slow start).

**`update install --config <the node's config>` works while the node runs.**
With `--config`, a node whose admin listener serves a certificate that the
system does not trust (an origin certificate, as on a node behind a proxy)
was not recognized as running, so the command tried to install in place and
was refused with "store at ... is already open for writing". A config given
with `--config` or `SDN_CONFIG` that is the same file the running daemon was
started with (an absolute path on its command line, run by the same account,
root or a system account) is now treated as that daemon's config, so the
command trusts the daemon's certificate and hands the install to the helper.

**A catalog row can list several products of different standards.** A
paired catalog (`catalog_product_source`) now publishes rows that list, for
example, a GP OMM, an SGP4-XP set and an SDN-AP record side by side. Each
product resolves among the records of its own standard: retain one product
source per standard the rows name (`OCM.fbs` and `OMM.fbs`, both under the
provider and the source `catalog_product_source` names). A snapshot publishes
one series per product standard (OCM, then OMM), then the index. Before this
a listed `$OMM` product made the whole snapshot fail ("unresolved product
reference"), and an `OMM.fbs` source could not be retained.

- A catalog generation now admits four products per object (one per CAT
  product kind), and a closure of up to 400,000 product references; it was
  one product per object and 100,000 references. A CAT index and one product
  source configured for N objects reserve up to 30N references (capped at
  2,400,000); each further product source adds up to 5N.
- How to configure it: `sdn-server/docs/catalog-publication.md`.
