# Module-delivery grant policy

`grant-policy.json` deploys to a provider's **plugin root**, beside `catalog.json`:

```
/opt/data/license/plugins/grant-policy.json      # host-01
```

It is read once at boot by `license.LoadPluginRegistry` and enforced at the admit
point in `node.planCatalogPublication`. Changing it needs an edit and a restart —
never a rebuild.

## Why it exists

The only entitlement control on the wire is `PLG.ALLOWED_XPUBS`, and the licensing
key server reads an **empty** allowlist as *unrestricted*. That is a reasonable
rule and it is also unfalsifiable: an empty allowlist cannot tell "this module is
genuinely open" apart from "nobody set one". On 2026-08-07 a throwaway anonymous
identity holding no entitlement was issued a grant for `com.orbpro.ffm`, and an
audit of the live catalog found **all 43 published modules with an empty
allowlist**. See `graph/tasks/sdn-allowed-xpubs-not-enforced.md`.

The policy names the difference. It does not move any grant decision into the Go
host — grants stay in the licensing WASM key server, which is application logic.
The host decides only whether it provisions a module's **content key** at all.
A module the host refuses has no key inside the guest, so no grant for it is
reachable by any path, and the key server answers `module_not_found` — a refusal
distinguishable from a transport failure.

## Where paid modules live (owner 2026-10-02)

A paid module (`ENTITLED` in the provider's `modules.pmm`) lives in exactly one
of two places:

- **The Sandcastle gallery**, for gallery use. The gallery host
  (orbpro.edgesource.dev, behind Cloudflare Access) ships the module files and
  the gallery loads them from there. It never asks SDN.
- **This node, protected by key.** The module's rule is `allowlist` and its
  catalog entry lists the entitled wallets in `allowed_xpubs`. A grant also
  needs the wallet's proof that it holds that account key (the requester `$EPM`
  ChainProof, verified by the licensing key server).

A paid module is never `open` on SDN. Free modules and the test fixtures the SDN
test page loads are `open`.

## Policies

| policy | meaning |
| --- | --- |
| `allowlist` | **Default, fail-closed.** Only `ALLOWED_XPUBS` members. An allowlist policy with an *empty* list publishes nothing. |
| `open` | Any identity that passes the licensing challenge. Explicit, never inferred. |
| `link-key` | Legacy label for `open` (the private-gallery capability URL). Unused since 2026-10-02: the gallery no longer asks SDN. |

Precedence, highest first:

1. `enforce_allowlist_only` — the operator lockdown switch.
2. the module's own `catalog.json` `grant_policy` (the publisher's declaration).
3. the first matching rule in `modules` (`match` supports one trailing `*`).
4. `default_policy`.
5. the built-in default, `allowlist`.

A module declared `open`/`link-key` that *also* carries a non-empty allowlist is
**narrowed** to `allowlist`: a declared list is a restriction, and the
restriction wins.

## Lockdown

```json
{ "enforce_allowlist_only": true }
```

Every module snaps back to `allowlist`; anything unentitled stops being served.
To close a single module instead, delete its rule or set its policy to
`allowlist`.

## Entitling a module

Edit `allowed_xpubs` on the module's `catalog.json` entry and restart (both
files are read only at boot), or publish it with the list:

```
spacedatanetwork plugins publish-orbpro --allowed-xpub <xpub> --grant-policy allowlist ...
```

The xpub is the wallet's account xpub (`m/44'/0'/0'`). The wallet must also sign
the session key's proof for the requesting origin, so a copied xpub is not
enough.

## Audit

### FFM compatibility publication (2026-10-17)

Host-01 serves `com.orbpro.ffm@1.4.1` alongside `com.orbpro.hpop@1.0.0`.
Both license entries and their policy rules use `allowlist` with `allowed_xpubs: ["*"]`;
the requester must still prove its account key for the requesting origin.
Keep HPOP available while older engines remain in use.

The FFM license publication's version must match the signed PMM row exactly.
The console's protected delivery worker verifies the trailer-stripped SHA-256
against that row before returning a compiled module. A fresh account for
`https://spaceaware.io` must pass this delivery and compilation check after a
publication. Catalog and policy changes use the existing node binary; binary
deployments remain a separate coordinated operation.

Every boot writes one `grant-audit dir=publish` line per module (module, policy,
source, allowlist size, outcome) plus a summary naming the full open set and the
full refused set. Every licensing frame that crosses the libp2p boundary writes a
`grant-audit dir=request|response` line (module, requester **fingerprint**,
policy in force, outcome, provider reason). Raw xpubs never appear in a log:
they are wallet-linkable across every module a party ever requests, so only
`xpub:<first 8 bytes of sha256, hex>` is printed.

```
journalctl -u <unit> | grep grant-audit
```
