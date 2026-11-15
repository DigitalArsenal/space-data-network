# Next node release: release-notes text

Text for the next node release's notes. It replaces the v1.1.0 to v1.1.3
paragraph "Linux: give the node a key password file" and its three manual
recipes, which are no longer needed for a fresh install.

---

**Linux: a fresh node gets its key password file automatically.** The Linux
kernel makes `/sys/class/dmi/id/product_uuid` readable only by root, so on
most Linux machines a node that does not run as root cannot derive its
key-at-rest password from the machine. Through v1.1.3 such a node refused its
first start until the operator made a password file by hand. Now the
installers do it for a fresh node: no identity yet, no `SDN_KEY_PASSWORD` or
`SDN_KEY_PASSWORD_FILE` and no key password in the config, and a machine
fingerprint that is refused for the node's account (`spacedatanetwork key
fingerprint-check` says `ok`, `absent` or `refused`). The file holds 32 random
bytes as 64 hex characters, mode 0400, owned by the node's account, and is
never overwritten:

- `install.sh` as a regular user: `~/.spacedatanetwork/key-password`, recorded
  in the node's config as `security.key_password_file`, so every later start
  finds it with nothing exported.
- `.deb` / `.rpm`: `/var/lib/spacedatanetwork/key-password` (`sdn:sdn`) and the
  drop-in `/etc/systemd/system/spacedatanetwork.service.d/key-password.conf`
  that sets `SDN_KEY_PASSWORD_FILE`. Removing or purging the package leaves
  both, like the identity.
- Container (uid 1000): `/app/data/keys/.key-password` in the data volume; the
  entrypoint logs one line naming it and uses it on every later start. A
  mounted secret with `SDN_KEY_PASSWORD_FILE` is still the better custody, and
  the entrypoint then generates nothing.

**Keep that file for the life of the node, and back it up apart from the
node's data**: the node cannot open its identity without it. A node that
already has an identity is not touched by an upgrade — its identity stays
sealed exactly as it is — and a node running as root, on macOS or on Windows
needs no file. The credential store now uses the same key password as the
identity (`SDN_KEY_PASSWORD`, `SDN_KEY_PASSWORD_FILE`, `security.key_password`,
`security.key_password_file`, in that order), so "credential store
unavailable" no longer appears on such nodes; a store that was sealed under the
machine key while the node's password was in a file (which the store used to
ignore) keeps opening, and is re-sealed under the password. A first start
that is still refused now says that no identity exists yet, why this host's
fingerprint is refused, and how to proceed, instead of saying a secret failed
to open.
