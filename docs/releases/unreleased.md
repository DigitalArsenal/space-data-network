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

---

**Followed lanes of millions of records start, and a follower no longer
wears its producer down.** Since 1.1.10 a subscription to a lane starts with
the producer's statement of the lane. On a format 5 store the producer read
every record of the lane twice to answer it whenever the lane holds more
than one batch (a keep-all catalog lane of millions of records), so on the
catalog node each read took 1 to 3 minutes and the statement outlasted the
follower's 60 s wait. The follower asked again 30 s later while the producer
went on reading for the stream it had reset. Those reads held the store
engine's read lanes, so everything else queued behind them for minutes: the
followers' small lanes never got a first page, and the node's own catalog
publication failed at the engine's 5-minute request deadline. This release
fixes both sides:

- The producer answers a statement from its counters (the standard's newest
  sequence and the counters of the lane's batches), without reading the
  lane's records. The batch's and the lane's records and bytes it states are
  the sums of those counters, so a record that two batches hold counts in
  each. A followed stream from a lane's beginning no longer counts the lane
  first.
- Every read a follower asks for runs under its stream and stops when the
  follower resets the stream or the connection ends. Each has a bound (a
  statement 1 minute, a followed page 2 minutes), past which the producer
  answers `busy`.
- Other peers' reads can no longer starve the node's own publication. The
  store engine serves reads on 4 interactive lanes and 1 bulk lane; at most
  2 reads of other peers now run at once, and a follower's batch list no
  longer runs on the bulk lane, so the node's own window probes and
  fingerprint scans always have lanes of their own. A statement past the
  limit is refused `busy` at once; other reads wait their turn.
- The follower waits up to 5 minutes for a statement or a page, and after a
  stream that delivered nothing waits 1 s, then twice as long after each
  failure, up to 20 minutes (with 20% jitter), starting again at 1 s once a
  page arrives. Meanwhile the lane's `$PNM` publications import, as before.

Both sides work with a 1.1.12 node on the other: a 1.1.12 follower gets the
fast statement and its reset stops the producer's read; a 1.1.12 producer
gets one statement request at a time per lane, waited for 5 minutes, instead
of one every 90 s. `docs/SUBSCRIPTIONS.md` has the details.
