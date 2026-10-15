# DRAFT security advisory: SEC-001, compromised update-signing key and node identity

> **DRAFT. NOT PUBLISHED.** The owner publishes this (GitHub Security Advisory on
> `DigitalArsenal/space-data-network`, the release notes of the fixed release, and spacedatanetwork.org)
> as ROTATION.md step 4 says, after the new update root and the fixed release exist. Fill in every
> `<…>` first. Never include a seed, private key, or any value not listed here as public.

**Severity:** Critical. **Affected:** Space Data Network node (`spacedatanetwork`) and desktop releases built
from 2026-08-01 to 2026-10-15; `@spacedatanetwork/sdn-js` 3.1.0 and earlier (see below).
**Fixed in:** `<fixed node release, e.g. v1.0.5-beta.NN>`, `@spacedatanetwork/sdn-js <fixed version>`.
**Identifier:** `<GHSA id>` / `<CVE id if assigned>`.

## Summary

The recovery phrase (BIP-39 seed) of the project's public node `sdn.spaceaware.io` was committed to this
repository on 2026-02-18 and stayed in its public history until it was purged on 2026-10-15. Every key
derived from it must be treated as known to anyone. One of those keys, the Ed25519 key with key id
`d4a971a7e534`, was also the **update-signing root** that SDN releases trust. An installed node that still
trusts that root and has the update lane enabled (the default) can be made to install code signed by anyone
holding the published seed.

## Affected versions

- **Node / CLI bundles and the desktop app** whose `trust/update-roots.json` names key id `d4a971a7e534`:
  every release built from 2026-08-01 onward, which is `v1.0.4-beta.18` and every `v1.0.5-beta` tag up to and
  including `v1.0.5-beta.83`. GitHub release pages exist for `v1.0.4-beta.18`, `v1.0.5-beta.1`, `.58`, `.59`,
  `.65`, `.67`, `.68`, `.69` and `.70`; tags `v1.0.5-beta.71` to `.83` carry the same root. All of them ship
  with `update: {enabled: true}` as the default.
- **`@spacedatanetwork/sdn-js` 3.1.0 and earlier**: they pin the compromised peer
  `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45` as a default relay/bootstrap and provider. They do
  not install updates, but an attacker holding the seed can impersonate that peer to them.

## Impact

Anyone holding the published seed can:

- sign an update manifest and update signal that affected nodes accept and install, i.e. run code on them
  (only nodes whose update lane is enabled and whose roots still include `d4a971a7e534`);
- impersonate `sdn.spaceaware.io` (PeerID above) to peers and browsers that pin it, and mint module-delivery
  grants that pinned clients accept;
- decrypt anything ever encrypted to that node's published encryption keys (for example the sealed admin
  transport to `sdn.spaceaware.io`).

The project's own fleet disabled the update lane on 2026-10-15 13:13–13:25Z and is moving to a new update root
out-of-band. We have found `<no evidence / evidence — fill in from ROTATION.md step 0b>` that the key was
used by anyone else.

## What to do now

1. **Turn off signal-driven self-update** on every node you run, then restart it:
   ```yaml
   update:
     enabled: false
   ```
   The log then says `Update signal lane DISABLED`.
2. **Check what is installed.** Compare the running binary's sha256 with the published release checksums, and
   look in `<bundle>/deploy-ledger.jsonl` and `<bundle>/updates/` for any update applied since 2026-08-01 that
   you did not install yourself. `deployment/identity/sec001-node-check.py` in the repository lists them; it
   changes nothing unless asked.
3. **Replace the update root.** Overwrite `<bundle>/trust/update-roots.json` (or the file
   `SDN_UPDATE_TRUST_ROOTS` names) with exactly:
   ```json
   {"<new key id>": "<new SPKI base64>"}
   ```
   Verify this value against at least two of: this advisory on GitHub, the fixed release's notes, and
   `deployment/identity/anchors.json` (`updateRoots`) on the `main` branch.
4. **Upgrade** to `<fixed release>` (it trusts only the new root, refuses the old root and the old peer, and
   ships the denylist), then set `update.enabled` back to `true` if you want automatic updates.
5. **Remove the old peer** `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45` from `network.bootstrap` and
   `peers.trusted_peers`, and the key `0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c` from
   `SDN_TRUSTED_PUBLISHER_KEYS`. Fixed builds drop them by themselves.
6. **Browser and npm users:** upgrade `@spacedatanetwork/sdn-js` to `<fixed version>`; 3.1.0 and earlier are
   deprecated on npm.
7. **If you sent secrets** to `sdn.spaceaware.io` over its sealed admin transport, or sealed credentials to its
   wrapping key, rotate those secrets.

## Public values of the compromised identity (for blocklists)

| What | Value |
|---|---|
| PeerID | `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45` |
| Update root key id / Ed25519 key | `d4a971a7e534` / `0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c` |
| Grant verifier key | `8e2f6db0048925ce413512df5b4261cee5ad7e04e65588d1a9af26cd4254fa2b` |
| xpub | `xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa` |
| Full list | `deployment/identity/anchors.json`, `denylist` |

## Timeline (UTC)

- 2026-02-18: the seed is committed in `sdn-js/verify-identity.mjs`; the same seed also sat in `config/dev-wallet.env` in the history.
- 2026-08-01: the node key becomes the fleet update root (`fleet-trust-roots.json`).
- 2026-10-15: found in a security audit; the fleet's update lane is disabled (13:13–13:25Z); a hotfix limits
  root sign-in on `sdn.spaceaware.io` to the box itself (15:11Z); the history is purged; the rotation begins.
- `<date>`: new update root installed on the project's nodes; `<fixed release>` published; this advisory.

## Credit

Found by the project's internal security audit.
