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

**A restart no longer deletes a subscribed lane's current records.** On a
node that subscribes to a lane and also holds an older publication of it
(one it imported as 1.1.9, or one it made of the lane itself), every start
logged "Retention for ...: keeping batch <old> as the current set — evicted
... records" and removed the records of the batch the producer states as
current; the lane then held them again only when its stream fetched them
anew. The retention pass at start now leaves subscribed lanes to their
subscription. This held for `--publications only` lanes too.

**`sync subscriptions` shows the lag the store holds.** The lag was the
count when the batch was last made current, so after its records were
removed the lane still read "lag: 0 records". It now counts the stated
batch's records the store does not hold at the time of the listing.

**A batch is no longer refused over records whose bytes came from another
peer first.** A producer named, as each record's origin on a followed lane,
the peer it first stored the record's bytes from. When a few records of its
own lane had first reached it from the follower (or any other peer), the
follower refused the whole batch with "... records whose ORIGIN_ID names
another peer ..." and asked again every backoff, and the lane stayed short
until the next publication was imported. The producer now names the lane's
producer. Both ends need this release: a follower on this release still
refuses such a batch from a producer on an earlier one.

**A batch that grows while it is listed is held.** A follower that listed a
batch the producer was still storing (its carried-over records are tagged
in place, so the stream is quiet and states the batch while it grows) found
more records than stated and raised `follow_refused` "the producer lists
more than N records in batch ...". It now asks the producer's statement
again and goes on when the same batch has grown.

**CLI config discovery ignores other accounts' processes.** A CLI command
given no `-c` and no `SDN_CONFIG` used to take its config from any running
process whose command line read `spacedatanetwork daemon --config ...`,
whoever ran it, so another local account could choose the config that
`init` loaded and saved. Only a daemon run by the same account, by root or by
a system account (such as the package's `sdn`) counts now; for another
account's node, pass `-c`. `install.sh` passes the node's own config to every
command it runs.
