# Signed releases and the public installers

Security audit finding SEC-045: the one-line installer ran `scripts/install.sh`
from `main`, picked the newest `v*` release, and trusted the
`spacedatanetwork-checksums.txt` published in that same release. Anyone who
could push to `main` or create a release controlled every new install. The
installers now trust a release only when its checksum list carries a valid
signature by an **offline release key** that CI never holds.

## What checks what

| Piece | Trusts |
| --- | --- |
| `docs/install.sh` (served at `https://spacedatanetwork.org/install.sh`) | Runs `install.sh` from ONE pinned release (`SDN_INSTALLER_RELEASE_TAG`), only if its SHA-256 equals `SDN_INSTALLER_SHA256` and the release's signed checksum list gives it that same SHA-256. Never reads `main`. |
| `scripts/install.sh` (the release asset `install.sh`) | Considers the newest node releases, skips any whose `spacedatanetwork-checksums.txt.sig` does not verify against the embedded public key or whose list does not name this platform's archive, then checks the archive against that verified list. A pinned `SDN_VERSION` must verify too. |
| `docs/install.ps1` | Refuses to run until the PowerShell installer verifies the same signature. |
| `beta-release-artifacts.yml` | Runs from `main` only and creates the release as a **draft**. It cannot make a release installable. |
| `release-deploy.yml` | Runs only from the `v*` tag it releases (no version input), refuses a tag whose commit is not on `main`, creates a **draft**, and never replaces the assets of a published release. |
| `docker-publish.yml` | Builds only the `v*` tag it runs from (no ref input), and only if that commit is on `main`. |
| `pages.yml` | The only path from `docs/` to spacedatanetwork.org once Pages' source is "GitHub Actions": from `main`, in the `github-pages` environment, so every change to `docs/install.sh` waits for the owner's approval. |
| `sign-release-checksums.sh sign` | Refuses unless the draft's `install.sh` is byte-identical to `scripts/install.sh` at the tag, embeds the public half of the key signing it, and is listed at its real digest; the tag must be on `origin/main` and the list must name only this version's installer archives. |

Both installers fail closed: no key embedded, no `openssl`, a missing or bad
signature, a missing archive entry or a checksum mismatch all stop the install
before anything is extracted or run.

Signatures are ECDSA P-256 over SHA-256 of `spacedatanetwork-checksums.txt`
(DER, as `openssl dgst -sha256 -sign` writes them). Archive names carry the
version, so a signed list cannot be replayed under another tag.

## One-time: the release key (owner, offline)

1. On an offline machine:
   `deployment/release/sign-release-checksums.sh keygen <dir-on-removable-media>`
2. Keep `release-key.pem` offline (an HSM or an encrypted removable volume, with
   a backup). It must never be committed, uploaded as a GitHub secret, or copied
   to a networked machine.
3. Paste `release-key.pub.pem` between the
   `# BEGIN SDN RELEASE SIGNING KEY` / `# END SDN RELEASE SIGNING KEY` markers
   in **both** `scripts/install.sh` and `docs/install.sh`, as
   `SDN_RELEASE_SIGNING_KEY_PEM='-----BEGIN PUBLIC KEY-----` … `-----END PUBLIC KEY-----'`.
   Commit that through a reviewed pull request.

Until step 3 lands, both installers refuse to install anything.

## Every release

1. Dispatch **SDN Beta Release Artifacts** from `main`. Its publishing jobs
   wait for approval in their environments (`dockerhub`, `update-signing`,
   `macos-signing`, `release`). The run ends with a **draft** release.
2. On the signing machine, in a checkout of this repository, fetch the tag
   and `main`, then the draft's checksum list and installer:
   `git fetch origin main --tags`
   `gh release download <tag> --repo DigitalArsenal/space-data-network -D draft -p spacedatanetwork-checksums.txt -p install.sh`
   (add `-p 'spacedatanetwork-*.tar.gz'` to have every archive checked too).
3. Sign it offline:
   `deployment/release/sign-release-checksums.sh sign <release-key.pem> <tag> draft`
   It refuses unless `draft/install.sh` is exactly `scripts/install.sh` at
   `<tag>`, that installer embeds the public half of `<release-key.pem>`, the
   list gives it its real SHA-256, `<tag>` is on `origin/main`, and the list
   names no installer archive of another version. Then it prints the list,
   signs it and verifies the signature.
4. Upload the signature and publish:
   `gh release upload <tag> draft/spacedatanetwork-checksums.txt.sig --repo DigitalArsenal/space-data-network`
   `gh release edit <tag> --draft=false --latest --repo DigitalArsenal/space-data-network`
   Publishing triggers **Smoke published installers**.
5. To move the public stub to this release's installer:
   `deployment/release/sign-release-checksums.sh installer-pin draft/spacedatanetwork-checksums.txt`
   prints the SHA-256 to set as `SDN_INSTALLER_SHA256` in `docs/install.sh`,
   with `SDN_INSTALLER_RELEASE_TAG=<tag>`. Land it through a reviewed pull
   request; `pages.yml` deploys it after the `github-pages` environment's
   reviewer approves. While Pages still builds from the branch (Settings ->
   Pages -> Source "Deploy from a branch"), any push to `main` changes the
   stub with no approval: switching the source to "GitHub Actions" is part of
   closing SEC-045.

## Re-running a release by hand

Dispatch on the tag, never with a name typed into an input:
`gh workflow run release-deploy.yml --ref <tag>` and
`gh workflow run docker-publish.yml --ref <tag>`. Both refuse a branch, a
non-semver tag and a tag whose commit is not on `main`.

## Rotating the key

Generate a new pair, embed the new public key in both installers, re-sign the
checksum lists of the releases that should stay installable, and land the
installer change before retiring the old key. Installers already downloaded
keep trusting the key they carry, so a compromised key also needs a published
advisory.
