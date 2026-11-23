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

**Followers' reads can no longer take the producer's own engine lanes.**
1.1.13 answers a lane's statement from counters and makes the follower back
off; what a follower's other reads cost the producer was unbounded. The
store engine (FlatSQL 3.12.14) serves reads on 4 interactive lanes, 1 bulk
lane and 1 sandbox lane on any host, whatever its cores:

- A statement, a followed page, a page of a batch's list, a fetch by
  content address and a `read-chunk` scan each take a slot while they read
  the store, and one peer holds at most 1. Peers the producer trusts take
  any free slot and have 1 of their own; all other peers share 2 (1 on a
  host of 1 core). At most 3 such reads run at once (2 on 1 core), all on
  interactive lanes, so the node's own reads (its publications' window
  probes and fingerprint scans, its API) always have an interactive lane
  and the bulk lane, and throwaway peer IDs never keep a trusted follower
  waiting. A statement waits up to 20 seconds for a slot, then is refused
  `busy` and the follower backs off; a followed page, a batch's list, a
  fetch by content address or a scan waits for a slot within its bound.
- A follower's batch list runs on an interactive lane, not on the bulk lane
  the node's own publication scans use. On a copy of a production store
  with seven lanes followed by a 1.1.12 build, the node's own scans queued
  up to 29 s behind followers' batch lists on that lane (1.1.13 and 1.1.14
  run the lists there too).
- These reads run under their stream: when the peer resets the stream or
  the connection closes (also after the peer closed its side of the stream
  following its request, as `sdn-js` does), the producer cancels its engine
  read, and the engine stops it within milliseconds and frees its lane. A
  statement has a bound of 1 minute and the others 90 seconds, the wait for
  a slot included, past which the producer answers `busy`. A 1.1.13 or
  1.1.14 follower waits 2 minutes for a page, so it gets that `busy` before
  it gives up. On a store of another format than 5 a read runs to its end
  and is answered `busy` then if it passed its bound.
- A follower that gives up on a batch's list or a fetch, or whose
  subscription is removed or stopped meanwhile, resets the stream (1.1.13
  and 1.1.14 close it when they give up, which the producer reads as the
  end of the request, so the read goes on to its bound; a subscription
  removed meanwhile leaves the stream open until their 2-minute wait ends).
- A followed stream from the lane's beginning reads forward from the start
  without counting the lane first.
- The follower waits 5 minutes for a page (2 minutes before), longer than
  the producer's bound, so a slow producer answers `busy` instead of being
  asked again.

A page of a batch's list still walks the whole lane on a keep-all lane of
millions of records (10 to 16 s per 8,192 records on that copy), so
`--publications only` remains the way to subscribe to such a lane.
