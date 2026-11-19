# Next node release: release-notes text

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
512 of their statuses held, where a new author's status is not kept once 512
are held. Pinned peers (`peers.trusted_peers` too), `network.bootstrap`
peers, peers the registry trusts and peers whose status the node already
holds are outside those bounds, so a flood of new identities cannot remove
the nodes already shown. `not reporting` is a node of 1.1.9 or earlier, a node
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
