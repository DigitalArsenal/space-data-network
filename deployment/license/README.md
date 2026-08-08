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
identity holding no entitlement was issued a grant for `com.orbpro.hpop`, and an
audit of the live catalog found **all 43 published modules with an empty
allowlist**. See `graph/tasks/sdn-allowed-xpubs-not-enforced.md`.

The policy names the difference. It does not move any grant decision into the Go
host — grants stay in the licensing WASM key server, which is application logic.
The host decides only whether it provisions a module's **content key** at all.
A module the host refuses has no key inside the guest, so no grant for it is
reachable by any path, and the key server answers `module_not_found` — a refusal
distinguishable from a transport failure.

## Policies

| policy | meaning |
| --- | --- |
| `allowlist` | **Default, fail-closed.** Only `ALLOWED_XPUBS` members. An allowlist policy with an *empty* list publishes nothing. |
| `open` | Any identity that passes the licensing challenge. Explicit, never inferred. |
| `link-key` | `open`, with its reason recorded: the credential is a capability URL whose UUID derives the requester's key. Behaves identically on the wire; exists so the "for now" is greppable and listed on every boot. |

Precedence, highest first:

1. `enforce_allowlist_only` — the operator lockdown switch.
2. the module's own `catalog.json` `grant_policy` (the publisher's declaration).
3. the first matching rule in `modules` (`match` supports one trailing `*`).
4. `default_policy`.
5. the built-in default, `allowlist`.

A module declared `open`/`link-key` that *also* carries a non-empty allowlist is
**narrowed** to `allowlist`: a declared list is a restriction, and the
restriction wins.

## Ending a "for now"

```json
{ "enforce_allowlist_only": true }
```

Every module snaps back to `allowlist`; anything unentitled stops being served.
To close a single module instead, delete its rule or set its policy to
`allowlist`.

## Entitling a module

```
spacedatanetwork plugins publish-orbpro --allowed-xpub <xpub> --grant-policy allowlist ...
```

or edit `allowed_xpubs` on the catalog entry.

## Audit

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
