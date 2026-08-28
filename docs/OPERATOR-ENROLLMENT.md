# OPERATOR ENROLLMENT

## 1. Operators and peers: one kind of thing with a trust level

A **peer** is a network identity: a libp2p peer ID that talks to this node over the network, carries data, and can be granted a trust level. An **operator** is a login identity: a BIP-32 account extended public key (an **xpub**) recorded in the node's user store, used with a wallet to sign in to the dashboard. Every operator derives a peer ID from its xpub, so one identity can be both a network participant and a login account.

`accounts list` shows the merged view — one row per identity, whether enrolled as a peer, an operator, or both:

```
$ spacedatanetwork accounts list
{
  "accounts": [
    {
      "can_sign_in": true,
      "kind": "operator",
      "name": "Node Root",
      "peer_id": "16Uiu2HAkuSSuf8u32gYjS24jmER35Le5GFYLJfJeaQNfPEWPsJuA",
      "source": "database",
      "trust_level": "admin",
      "xpub": "xpub6CfBgD57CQrTjuj9FDcVjvfwmu2beKsKqTYLt2rQFsm6..."
    },
    {
      "can_sign_in": false,
      "kind": "peer",
      "name": "...",
      "peer_id": "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
      "trust_level": "full"
    }
  ]
}
```

A merged row has two underlying records (a peer entry and an operator entry), so every trust change must say which one it targets — the CLI is explicit about this (Section 6).

Two identities are special:

- **The node's own key** (the root derived from the node's seed). Whoever holds the seed can always sign in as administrator, with or without any user-store row. It appears in `accounts list` as "Node Root".
- **The first admin bootstrap** (Section 5): a node with no administrators mints the first sign-in as "Initial Admin".

## 2. The key handover, end to end

An operator row needs **two public values** before it can ever sign in:

| Field | What it is | Where it comes from |
|---|---|---|
| `xpub` | BIP-32 account extended public key | the prospective operator's wallet |
| `signing_pubkey_hex` | 64-hex Ed25519 **public** signing key | the same wallet |

The `name` is optional. The trust level is chosen by the enrolling admin, not by the key holder.

Sign-in works like this: the wallet sends a challenge request containing its Ed25519 public key; the node looks the operator up **by that signing key, not by xpub**; the wallet then signs the challenge with the matching private key. So the row must carry the signing public key or the node has nothing to match. The xpub is the row's identity — every endpoint addresses the row by it — and the signing key is the possession proof.

### Step 1 — the prospective operator generates a key on their own machine

**Shipped path: the in-browser key ceremony** — the sanctioned way to generate an SDN key. Dashboard → **Accounts → Operator keys** → **"GENERATE A KEY"** dialog (the other half of the paste-an-xpub "APPROVE A KEY" form; both land in the same operator table). Some newer dashboard builds show a paste-only "Enrol a key" form without the generation button; the generation dialog's public output — xpub + signing public key + peer ID + fingerprint — is also reachable from the HD-wallet wallet UIs' copy buttons. The ceremony:

- generates a fresh 24-word recovery phrase entirely in the browser (HD-wallet module, seeded from the browser's own random source);
- derives and shows **only public material**: the account xpub, the peer ID, an xpub fingerprint, and the Ed25519 **public** key;
- never lets the phrase leave the page — not posted, not saved, not written to storage; it disappears when the dialog closes.

The printed signing public key is computed by the exact same unlock path the wallet uses at sign-in, so it is byte-identical to what the phrase presents on login.

**CLI alternative (xpub half only).** If the operator already has a BIP-39 recovery phrase (e.g. from any offline BIP-39 tool), the CLI derives the standard account xpub at `m/44'/0'/0'`:

```
$ spacedatanetwork derive-xpub
Enter your BIP-39 mnemonic phrase:
--- SDN Identity ---
XPub (BIP-32):     xpub6BosfCnifzxcFwrSzQiqu2DBVTshkCXacvNsWGYJVVhhawA7d4R5WSWGFNbi8Aw6ZRc1brxMyWMzG3DSSSSoekkudhUd9yLb6qx39T9nMdj

Add to config.yaml:
users:
  - xpub: "xpub6BosfCnifzxcFwrSzQiqu2DBVTshkCXacvNsWGYJVVhhawA7d4R5WSWGFNbi8Aw6ZRc1brxMyWMzG3DSSSSoekkudhUd9yLb6qx39T9nMdj"
    trust_level: "admin"
    name: "Operator"
xpub6BosfCnifzxcFwrSzQiqu2DBVTshkCXacvNsWGYJVVhhawA7d4R5WSWGFNbi8Aw6ZRc1brxMyWMzG3DSSSSoekkudhUd9yLb6qx39T9nMdj
```

The bare xpub goes to **standard output** (for scripts); the paste-ready config block goes to standard error. If the module file cannot be found, point at it explicitly: `--wasm <path>` or the `HD_WALLET_WASM_PATH` environment variable.

> **Not yet supported:** `derive-xpub` prints only the xpub. The matching Ed25519 signing public key currently comes from the browser ceremony or the HD-wallet wallet UI (a proposed CLI printing both fields: Section 8). A command named `identity new`, mentioned in some dev environment examples, does **not** exist in the current binary; `identity` offers only `directory`, `export`, `gen-key`, `keys`, `set`, `wizard`.

### Step 2 — what gets handed over

The prospective operator passes to an existing administrator **exactly**:

1. the **xpub** (starts with `xpub`),
2. the **64-hex Ed25519 signing public key**,
3. their preferred display **name** (optional).

**What must never be handed over, in any channel:** the recovery phrase, any private key, the extended private key. If a private value ever enters a chat, a paste, a logged terminal, or an agent transcript, the identity is compromised — generate a new one. (The CLI's custody classification: `key export --format mnemonic` is labelled "THE ENTIRE NODE IDENTITY IN PLAINTEXT" and `base64`/`hex`/`kubo`/`libp2p` formats are labelled "SECRET".) Nothing in enrollment ever asks for a private value; an enrollment request that does is a scam or a bug.

### Step 3 — what binds on first login (TOFU)

The signing key is bound **on first successful login — trust on first use**:

- Signing key pre-seeded at enrollment (recommended; it is a required field in the dashboard form): login just works; any other key is refused from day one.
- Row created without a signing key (possible via config): the **first** wallet that completes a successful sign-in binds its key permanently; a conflicting key presented later is refused.
- The binding is a pin — whoever holds the phrase that first signed in owns the account permanently. An admin can pin the key in advance (dashboard form or config `signing_pubkey_hex`), mapping enrollment exactly to the key the holder presented.

Failure modes to avoid:

- **Enrolling the wrong signing key** — the node holds a pin the operator cannot produce; the account can never sign in.
- **Omitting the signing key entirely** — the row is a dead record: the dashboard sends no xpub at sign-in, the node resolves the operator by signing key, and a keyless row matches nothing.

## 3. Path A — enrol from the CLI (and the config path)

`accounts trust --xpub` is the only CLI command that touches operator rows, and it **updates an existing operator — it cannot create one**. Proven on the wire against a live node:

```
$ spacedatanetwork accounts trust \
    --xpub xpub6BosfCnifzxcFwrSzQiqu2DBVTshkCXacvNsWGYJVVhhawA7d4R5WSWGFNbi8Aw6ZRc1brxMyWMzG3DSSSSoekkudhUd9yLb6qx39T9nMdj \
    --level admin
Error: PUT /api/auth/users/xpub6Bosf...: 400 Bad Request:
{"code":"update_failed","message":"user not found"}
```

An xpub with no row gets "user not found" — nothing is created. The create endpoint (`POST /api/auth/users`) is called by the dashboard form alone; no CLI command performs it.

**The real CLI-compatible creation path is `config.yaml`** — also the path that works before any admin exists (Section 5):

1. Have the prospective operator run `derive-xpub` and hand you the xpub (and, from the browser ceremony or wallet UI, the signing public key).
2. Add a `users:` entry — this is exactly what `derive-xpub` prints:

```yaml
users:
  - xpub: "xpub6BosfCnifzxcFwrSzQiqu2DBVTshkCXacvNsWGYJVVhhawA7d4R5WSWGFNbi8Aw6ZRc1brxMyWMzG3DSSSSoekkudhUd9yLb6qx39T9nMdj"
    signing_pubkey_hex: "e31e29b7c8b76bd08012ae0a917eced0067a183e9f1de980a7ef377115578f93"   # 64 hex chars, required in practice
    trust_level: "admin"
    name: "Operator"
```

   (`signing_pubkey_hex` is optional in the config parser and required in practice, for the same dead-record reason as in the UI. Without it, the node logs that the signing key "will be bound on first login (TOFU)".)
3. Restart the daemon (`spacedatanetwork restart`, or stop/start).
4. The operator signs in; the account is live. Two consequences:

   - entries from `config.yaml` **cannot be changed or removed through the API or dashboard** — a config row is re-imposed from the file on every read, so changing trust or name means editing the file and restarting;
   - a config row **can** carry trust spellings the API refuses: `never` and `ultimate`.

Once a row exists, the CLI manages it:

```
# change an operator's trust level (or rename)
spacedatanetwork accounts trust --xpub <xpub> --level admin
spacedatanetwork accounts trust --xpub <xpub> --name "New Name"

# view the merged view (peers, operators, and identities that are both)
spacedatanetwork accounts list
```

Every admin command authenticates by signing in as the node's own root key against the live daemon, or pass a session token explicitly with `--session-token <token>` or the `SDN_SESSION_TOKEN` environment variable.

> **Not yet supported:** no CLI command creates an operator row — `accounts add` creates **peer** rows only and explicitly refuses xpubs ("it identifies a wallet account, not a libp2p host"). Proposal: Section 8.

## 4. Path B — enrol from the dashboard UI

1. Sign in as an administrator.
2. Open **Accounts**, then the **Operators** tab.
3. In the **"Enrol a key"** form enter:
   - **Account xpub** — pasted from the prospective operator's key ceremony or from `derive-xpub`;
   - **Name** — optional, their display name;
   - **Trust** — one of: `unknown`, `marginal`, `standard`, `full`, `admin` (the dropdown offers exactly these; see Section 6);
   - **Signing public key (hex)** — the 64-hex Ed25519 public key from the same ceremony, lowercased; the wallet UIs' convenience copy button produces this directly.
4. Click **Enrol**. The node answers `201` for a new row; a duplicate xpub answers `409 user_exists`.

The form posts exactly the two-value payload above to `POST /api/auth/users` with the session cookie. Any admin session can also edit a row's trust/name/signing key (the row's own edit control in the table) and **Withdraw** a row — see Section 7 for what withdrawal refuses.

## 5. Bootstrapping: where the first administrator comes from

On a brand-new node, exactly three ways an administrator exists:

**1. The node's own key — always.** The node derives its identity from its seed. Whoever holds that seed signs in as administrator no matter what the user store contains (the "Node Root" row). This is the fallback that means the operator tier can never be permanently destroyed (Section 7).

**2. `config.yaml` seeding.** Before first start, or at any time, add a `users:` entry with `trust_level: "admin"` as in Section 3. The seeded key signs in immediately.

**3. First-admin bootstrap — automatic.** When no administrator exists anywhere (no config rows, no database rows at admin tier), the node arms one bootstrap path: the first wallet that completes a full sign-in not matching the node's own root is minted as **"Initial Admin"** at admin trust, and its signing key is bound at that moment. Two constraints are enforced deliberately:

- the xpub must be an account-level xpub — a BIP-32 **master** xpub (depth 0) is *refused* at both the challenge and the minting step, because it would store a key that enumerates an entire wallet. Any xpub from `derive-xpub` (account 0) qualifies;
- the path only exists while `HasAdmin()` is false — the moment any administrator row appears (including a config row), the bootstrap stops arming, and unknown keys get unverifiable challenges instead.

**Legacy setup mode:** on a daemon with no identity key yet, the terminal banner prints a one-time setup token and the root page redirects to a setup page that creates the initial node identity plus a legacy username/password administrator. Old pre-wallet flow; still present on fresh boxes.

## 6. Trust levels, and what each one grants

The scale is the classic PGP ownertrust ladder, from hard veto to self-identity:

| Level | Numeric | Grants |
|---|---|---|
| `never` | -1 | Explicit distrust — hard veto, never overridden |
| `unknown` | 0 | No assertion; the fail-closed default |
| `marginal` | 1 | Weakest positive assertion |
| `standard` | 2 | Normal access; the default for newly added peers |
| `full` | 3 | Confident trust |
| `admin` | 4 | **Operator tier: administrative API access, can manage other accounts** |
| `ultimate` | 5 | Reserved for the node's own identity — never granted to anyone else |

Admin grants: all `/api/auth/users*` management (the Enrol/editing surface), the merged accounts view, peer trust management, and the rest of the admin-gated API. Every admin gate is a numeric comparison against tier 4.

**Who can assign what:**

- **Operators (the API):** only `unknown` through `admin`. The API refuses `never` (operator lockout) and `ultimate` (self-identity) — wiring-proof:

```
$ spacedatanetwork accounts trust --xpub <xpub> --level never
Error: PUT /api/auth/users/<xpub>: 400 Bad Request:
{"code":"invalid_trust_level","message":"trust level \"never\" is not
assignable via this API: must be between \"unknown\" and \"admin\"
(ultimate is reserved for the node's own identity; never as an operator
lockout is not yet supported)"}
```

  (The CLI help line listing `never|unknown|marginal|standard|full|admin|ultimate` is the *peer* path's full vocabulary; the operator path always rejects `never` and `ultimate`.)

- **Peers (the CLI `accounts trust --peer-id` path):** the full range, including `never` and `ultimate`, with no ceiling.
- **Config rows:** any spelling is accepted from the file (`never` and `ultimate` included) — the ceiling exists only on the API.

## 7. Withdrawal, and the guards around it

**The dashboard blocks three withdrawals client-side**, with the reason in place of a confirmation:

- **From config** — the row is owned by the config file; edit the file and restart instead.
- **Your key** — withdrawing your own key from the node that authenticated you would lock the session's owner out.
- **Last admin** — if this row is the only one at admin tier, withdrawing it would leave the node with no administrator at all.

The server itself has only one gate: the caller must hold an admin session. There is no server-side "self" or "last admin" check — an API caller *can* delete the last row; the guard lives in the UI. Deleting the last row is not permanent: the node re-arms the first-admin bootstrap, so the next wallet to sign in is minted "Initial Admin" again. With the root key's unconditional admission, the operator tier can never be permanently destroyed.

Two more withdrawal facts:

- **Config rows cannot be removed or trust-changed via the API at all** (errors are explicit: "config users cannot be removed" / "config-managed users cannot have trust changed through the API").
- **Live sessions survive a demotion or removal.** A session carries the trust level minted when it was created; it is not re-validated against the row on every request. A withdrawn operator keeps what they already hold in an open session until it expires — withdrawal stops *future* sign-ins immediately (the row is gone from the resolution path) but does not retroactively void open sessions.

## 8. Not yet supported, and the minimal scheme that would close it

**Gap 1 — the CLI prints only half the handover.** `derive-xpub` prints the xpub but never the signing public key, so a CLI-only operator cannot produce both enrollment fields without a browser. **Proposed (not built):** extend `derive-xpub` to also derive and print the Ed25519 signing public key through the exact wallet unlock path sign-in uses (the same byte-identity guarantee the browser ceremony already guarantees), so its output becomes the complete handover block.

**Gap 2 — no CLI create for operators.** `accounts trust --xpub` updates only; creation lives in the dashboard form, first-admin bootstrap, and config seeding. **Proposed (not built):** a `POST /api/auth/users` call from the CLI (e.g. an `accounts add --xpub ... --signing-pubkey-hex ... --level ...` mode that disambiguates from peer creation by requiring the xpub flag), or a first-class `users` command group. Until one exists, the CLI path is: `derive-xpub` → edit `config.yaml` → restart.

Neither proposal exists in the shipped binary; treat both as design.

## 9. Security notes: what never leaves the operator's machine

- **The recovery phrase.** The browser ceremony generates it locally and drops it when the dialog closes. Never type it into a chat, an email, a ticket, or a terminal command; a phrase that has appeared in a transcript or a log is compromised — generate a new one.
- **Private keys.** The node never asks for them. Enrollment transports exactly two public values: the xpub and the Ed25519 public key.
- **The node's own key material.** At-rest keys are encrypted with a machine-derived key and fail closed if the machine changes. `key export --format mnemonic` prints the entire node identity in plaintext — that command belongs on the box, in a trusted session, not in any pipeline you cannot vouch for.
- **Master xpubs.** A depth-0 master xpub is refused by the first-admin bootstrap because it would enumerate an entire wallet; always enrol the account-level xpub (`derive-xpub` output).
- **No enumeration.** A sign-in attempt with an unknown xpub receives a valid-looking challenge that *can never verify* — probing for whether an account exists gets you silence, not information.
- **Session tokens.** Browser sessions ride an HttpOnly cookie. Scripted administration passes a session token explicitly (`--session-token` / `SDN_SESSION_TOKEN`) — treat it as a bearer credential.

---

*Verified against the shipped binary `sdn-server/spacedatanetwork`, its real help output, the live daemon on `127.0.0.1:7173`, and the node source (`sdn-server/internal/auth/`, `sdn-server/internal/peers/`, `sdn-server/cmd/spacedatanetwork/`, and the dashboard sources under `sdn-js/spaceaware-ui/`).*
