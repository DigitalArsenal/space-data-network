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
  `follow_cursor_refused`.
- Each followed page is checked before it is stored: a lane stops, its
  cursor kept, before the store would pass `subscriptions.record_ceiling` or
  leave less than 1 GiB of free disk (alert `follow_stopped`), and resumes
  once the page fits. `--force` skips only the check before the subscription.
- A record gossiped on a schema topic (`POST /api/v1/pubsub/publish`, the
  sdn-js `publish`) names no lane and is no longer stored by any node,
  whatever its trust of the sender or `network.accept_push_from`. Signed
  `$PNM` announcements (node identity, dataset publications) are unchanged.
- A lane whose followed stream is connected no longer fetches its
  publications' shards as well; a lane whose stream the producer refuses
  imports through them.
- A producer serves a peer it does not trust at most 8 followed streams
  (64 for all such peers); a node following more lanes of one producer is
  listed by it in `peers.trusted_peers` (16 streams, outside the 64).

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
