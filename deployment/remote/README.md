# Remote node deployment (Docker over ssh)

One command takes a fresh server to a running, administrable SDN node:

```bash
./deployment/remote/sdn-remote-deploy.sh deploy \
    --host root@203.0.113.10 \
    --domain node.example.org
```

The long-form, illustrated version of this lives in the public onboarding guide
(`docs/onboarding.html`). This file is the operator's reference and the record
of *why* each decision is what it is.

## What the command does

| Step | What happens | Why |
| --- | --- | --- |
| Preflight | checks docker + compose on the host | fail before touching anything |
| Stage | copies pinned wallet assets into the build context | without them nobody can sign in |
| Build | `docker buildx build --platform linux/amd64` **locally** | standing owner law: never build on hosts |
| Backup | stopped-state `tar` of the data volume | every cutover is reversible |
| Ship | `docker save \| gzip \| ssh \| docker load` | no registry, no credentials on the host, no source on the host |
| Render | writes `node.yaml`, `docker-compose.yaml`, `sdn.env` | none of them contain a secret |
| Secrets | key password generated *on the host*; mnemonic staged to tmpfs | see below |
| Start | `docker compose up -d` | pinned hostname, single data volume |
| Verify | waits for healthy, confirms `keys/mnemonic` is on the volume | only then shreds the staged phrase |

`--registry REF` swaps the save/load stream for a registry push if you already
run one. `--no-build` ships an image you already built and verified.

## The secrets, and where they are not

Nothing secret is ever in the image, in a build arg, in the environment, in
`docker inspect`, in argv, in the compose file, or in your shell history.

**Mnemonic.** Read with echo off and piped straight down the ssh channel into
`/run/sdn-secrets/sdn_mnemonic`. `/run` is a tmpfs on any systemd host, so the
plaintext never reaches a disk and never survives a reboot. The node imports it,
encrypts it under the at-rest key, writes the ciphertext to the data volume, and
the script shreds the staging file — but *only after* confirming the encrypted
copy actually landed. If it cannot confirm, it refuses to shred and says so.

If the image predates the mnemonic-import contract, the script refuses to
prompt at all rather than accept a phrase the node would silently ignore.

**At-rest key password.** Generated *on the host* with `/dev/urandom`, so it
never crosses the wire in either direction. Stored at `/etc/sdn/key_password`,
mode 0400, owned by the container's uid. Mounted read-only at
`/run/sdn-key/key_password`.

> **Back up `/etc/sdn/key_password` separately from the data volume.**
> It is the only thing that can decrypt the mnemonic. The two together are your
> node's identity; either one alone is useless.

**Operator username and password.** There are none, and none are ever sent.
This node accepts its own root identity as the administrator — sign-in is a
challenge signed by the seed behind the mnemonic on the data volume. *The
recovery phrase you fed at deploy time is your admin credential.* You present it
through the in-page wallet, which the node serves from itself (`/wallet-wasm`,
`/wallet-ui`) and never from a website. Additional operators are granted
afterwards by xpub; `users:` entries hold only public material.

## Three ways to permanently destroy a node's identity

All three are designed against here. Do not undo the mitigations.

1. **Letting the container hostname float.** The credential keystore root key is
   derived partly from `os.Hostname()`, and `docker compose up -d` gives a
   recreated container a brand-new random hostname. Every routine image upgrade
   would silently orphan the stored provider credentials. The compose file pins
   `hostname:`. Never change that value.

2. **Putting the keys outside the volume.** The node derives its key directory
   from `storage.path`'s parent. `node.yaml` therefore sets
   `storage.path: /app/data/store` so keys land at `/app/data/keys` — *inside*
   the one named volume. Setting it to `/app/data` would put them at `/app/keys`,
   outside, and the next upgrade would delete them.

3. **Relying on the machine-derived at-rest key.** With no explicit password the
   node derives one from host RAM, CPU model and CPU count. Inside a container
   those come from the *host*, so resizing the VM, migrating it, restoring it
   elsewhere, or changing the cpuset silently changes the key and the encrypted
   mnemonic becomes undecryptable forever. This deployment always pins an
   explicit password file.

## Upgrades and rollback

Re-run `deploy` with a new `--tag`. The volume, the hostname and the identity
all persist; only the image changes. A stopped-state backup is taken first and
the previous tag is recorded, so:

```bash
./deployment/remote/sdn-remote-deploy.sh rollback --host root@203.0.113.10
```

restores the previous image and the newest backup.

## TLS

With `--domain`, the node terminates TLS itself: `tls_mode: managed`, ACME
HTTP-01 on `:80`, certificate cache on the persistent volume. No nginx required.
`tls_hosts` must be non-empty or no certificate is ever issued, and the cache
must stay on the volume or each recreate burns another of Let's Encrypt's five
certificates per domain per week — both are handled by the rendered config.

Without `--domain` the admin listener is bound to the host's loopback only and
you reach it over an ssh tunnel. It is never exposed unencrypted.

## Verified

Exercised end-to-end over real ssh against a scratch host with its own Docker
daemon (2026-07-27): image streamed and loaded, config rendered, key password
generated host-side, node reached healthy, `keys/mnemonic` confirmed on the
volume as ciphertext, `hasIdentity=true`, wallet assets served same-origin, and
the peer ID survived both a container recreate and an image upgrade. The
import-capability guard was confirmed to refuse before shipping anything.
