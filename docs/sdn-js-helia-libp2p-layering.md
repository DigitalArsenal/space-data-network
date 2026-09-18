# sdn-js: helia / libp2p layering audit

> **STATUS 2026-09-18: THE SPLIT DESCRIBED BELOW IS CLOSED. This document is the
> record of why it existed and what it cost, not a description of the tree.**
>
> `sdn-js` now resolves **one** libp2p (3.3.11), **one** `@libp2p/interface`
> (3.3.0) and **one** helia (7.1.12), and every shim this audit catalogues —
> `withHeliaStreamHandlerCompat`, `withHeliaDialProtocolStreamCompat`,
> `addLegacyEventedStreamCompat`, `addLegacyWritableStreamCompat` — is deleted.
> `identifyCapabilityOnly()` stays, because it was never a version bridge: see
> its doc comment in `sdn-js/src/helia.ts`.
>
> Closing the split is also what fixed **GHSA-vrf4-mx87-p53w** (8.2,
> `@libp2p/peer-store` PeerRecord poisoning). libp2p 1.x was EOL at 1.9.4, so
> that advisory had no in-range fix while this arrangement stood.
> `npm audit --omit=dev` went from 8 vulnerabilities (1 low, 7 high) to 0.
>
> What replaced the parts this audit names:
>
> | this audit | now |
> |---|---|
> | `libp2p@1.9.4` + nested `libp2p@3.2.0` | one hoisted `libp2p@3.3.11` |
> | `@chainsafe/libp2p-{gossipsub,noise,yamux}` | `@libp2p/{gossipsub,noise,yamux}` (renamed into the js-libp2p monorepo) |
> | `@spacedatanetwork/libp2p-webrtc-v1` (= `@libp2p/webrtc@4`) | `@libp2p/webrtc@6` |
> | `@helia/block-brokers` | `@helia/bitswap` + `@helia/trustless-gateway-client` |
> | `@helia/routers` | `@helia/fallback-router`, plus a libp2p router adapter in `helia.ts` that helia 7 no longer exports |
> | `@libp2p/websockets/filters` | deleted; v10's `dialFilter` already accepts `/ws` and `/wss` |
> | `circuitRelayTransport({ discoverRelays })` | a `/p2p-circuit` entry in `addresses.listen` |
> | `peerIdFromKeys(pub, priv)` + `libp2pOpts.peerId` | `privateKeyFromProtobuf()` + `privateKey` alone, pinned by `src/peer-id-derivation.test.ts` |
> | stream `sink`/`source` | `MessageStream` `send()`/`onDrain()`/`close()` plus async iteration |
> | a 418-line replacement for `@libp2p/crypto/keys` in the bundle | upstream `@libp2p/crypto@5`, with only its WebCrypto leaf substituted |
>
> `scripts/check-sdn-js-dependency-layering.mjs` was rewritten to assert the
> opposite invariant — exactly one copy of each critical package — so the split
> cannot come back quietly.
>
> **The gap this audit ended on ("Not verified: I did not run a live
> browser↔Go handshake") is now closed by two checked-in tests:**
> `sdn-js/src/go-libp2p-interop.test.ts` dials a real go-libp2p host built from
> `sdn-server/cmd/js-interop-host`, from source AND from the published bundle,
> and `sdn-js/src/stream-exchange.test.ts` drives both SDN stream exchanges
> against real libp2p 3 streams.

---

Audited 2026-09-18 against `sdn-js@3.0.0`. Everything below marked **verified**
was executed, not reasoned about; everything marked **inferred** was not.

## The shape (historical)

`sdn-js` runs **two incompatible libp2p majors in one dependency tree**, and the
compatibility shims in `sdn-js/src/helia.ts` are what hold them together.

```
node_modules/libp2p                    1.9.4   <- sdn-js/package.json depends on this
node_modules/helia/node_modules/libp2p 3.2.0   <- helia@6.0.22 declares libp2p ^3.0.6
node_modules/@helia/utils/.../libp2p   3.2.0
```

**Verified** by reading `sdn-js/package-lock.json`. npm satisfies both ranges:
ours hoists, helia's nests. We build a libp2p@1 node and hand it to
`createHelia`.

The layering is **not** deep imports. All four import sites use package-root
public API (`helia`, `@helia/unixfs`, `@helia/block-brokers`, `@helia/routers`);
zero subpath imports into helia internals. **Verified** by grep over `sdn-js/src`.

The layering is **runtime patching of the libp2p instance to impersonate a newer
major**. The file says so itself at `helia.ts:114-116`:

```
// Helia 6 registers some stream handlers as `(stream, connection)`, while the
// libp2p instance resolved in this workspace invokes handlers with a single
// `{ stream, connection }` object. Adapt only the two-argument form.
```

| shim | where | what it fakes |
|---|---|---|
| `withHeliaStreamHandlerCompat` | `helia.ts:118` | rebinds `libp2p.handle` |
| `withHeliaDialProtocolStreamCompat` | `helia.ts:376` | rebinds `libp2p.dialProtocol` |
| `addLegacyEventedStreamCompat` | `helia.ts:295` | synthesizes `addEventListener` + `message`/`remoteCloseWrite`/`close` over a libp2p@1 it-stream |
| `addLegacyWritableStreamCompat` | `helia.ts:~300+` | synthesizes `send()`/`onDrain()` with its own 1 MiB high water / 8 MiB cap over a libp2p@1 `sink` |
| `identifyCapabilityOnly()` | `helia.ts:96`, `node.ts:99` | fabricates `[serviceCapabilities]: ['@libp2p/identify']` so helia's dependency check passes without identify running |
| `as never` casts | `helia.ts:419-425` | the types genuinely do not line up |

One thing that is **luck-free**: `serviceCapabilities` is
`Symbol.for('@libp2p/service-capabilities')` in **both** `@libp2p/interface`
1.7.0 and 3.2.2, so the capability stub is cross-realm legitimate.
**Verified** by importing both copies and comparing — the symbols are `===`.

**It works today.** 37/37 helia tests pass (33 in `src/helia.test.ts`, 4 in
`src/helia-trustless-gateway.test.ts`); the full suite is 931 passing.
**Verified** by running `npm test`. The problem is unreviewed risk, not a break.

Nothing in semver protects this. A helia **6.1.x patch** that changes how it
calls `handle()` or `dialProtocol()` breaks us with no major bump. This is live
code: the orbital console loads source shards through it.

## Why it never surfaced

`.github/workflows/security.yml` ran `npm audit --json > report || true` and
turned any high/critical count into `::warning::`. It could not fail — and it ran
on `workflow_dispatch` **only**, so nothing scheduled it either. Seven high
advisories reported green.

## What was landed (2026-09-18)

Conservative pass. No major upgrade.

1. **Honest ranges.** Ten libp2p-family carets raised from a floor that lied to
   the version actually resolved and tested (`libp2p ^1.2.0` → `^1.9.4`,
   `@libp2p/kad-dht ^12.0.0` → `^12.1.5`, and so on). A floor below the resolved
   version tells a consumer that a libp2p we have never built against is an
   acceptable resolution for our subtree, and the shims are not calibrated for
   it. **Verified**: the lockfile's `version`/`resolved`/`integrity` fields did
   not move at all — the only delta is the mirrored range strings.

   Deliberately **not** raised: `multiformats`, `idb`, `qrcode`, `uint8arrays`.
   Raising those forces nested copies on consumers pinned lower, and
   `multiformats` in two copies breaks `CID` identity checks. No honesty payoff,
   real dedup cost.

2. **`overrides.dompurify` 3.4.3 → 3.4.15.** The advisory range is `<=3.4.12`;
   3.4.13 is the fix line. The override was added 2026-05-15 (`d5e5ef595`) to
   hold dompurify at a non-vulnerable version, and had fallen behind its own
   purpose. **Verified**: `npm audit --omit=dev` goes 10 vulnerabilities
   (1 low / 2 moderate / 7 high) → 8 (1 low / 7 high); both moderates clear.

3. **`scripts/check-sdn-js-dependency-layering.mjs`** — pins the reviewed
   layering (both libp2p copies, all four helia packages, both `@libp2p/interface`
   copies), the exact set of libp2p copies in the tree, and the range-floor
   honesty rule. Fails when any of it moves, naming `helia.ts` and the shims.
   Runs in `oss-preflight.sh`, so it gates every push and PR, and again in the
   npm publish lane.

4. **`scripts/check-kubo-lockstep.mjs`** — see "Kubo" below.

5. **`scripts/check-npm-audit.mjs`** — a real npm audit gate with a dated,
   reasoned allowlist. Also fails on an **expired** deferral, a **stale** one
   (advisory gone, entry left behind), and a **new** advisory against an
   already-deferred package.

6. **`scripts/check-govulncheck.mjs` + `scripts/run-govulncheck.sh`** — the same
   treatment for Go. See "Go side".

7. **`security.yml` rewritten**: runs on a weekly schedule, on PRs touching any
   dependency manifest, and on pushes to main that touch one. npm and Go scans
   are gates. `go-version` was hardcoded `'1.21'` while `sdn-server/go.mod` asks
   for 1.25 — the security lane was scanning a build no other lane makes; it now
   uses `go-version-file`. CodeQL's `npm run build || true` (which let a broken
   build still report success) is now a real build.

8. **`npm-publish-sdn-js.yml` runs the test suite.** The lane was
   `npm ci` → `build:package` → `publish`, and `prepublishOnly` is only
   `check:versions && build:package`. It publishes on `release: published` and on
   `workflow_dispatch`, neither tied to a green run on the published commit.

## Found in passing: the publish lane is already red

`npm run check:versions` **exits 1 on a pristine `main`**, before any change in
this pass:

```
FAIL: sdn-js flatsql mismatch: sdn-js/package.json=2.0.3 suite.versions.json=1.4.5
```

**Verified** by restoring `sdn-js/package.json` and `package-lock.json` to
`HEAD` and re-running: same failure, same exit code. Neither input is touched by
this pass.

`prepublishOnly` is `check:versions && build:package`, so `npm publish` of
`@spacedatanetwork/sdn-js` fails at this gate today. Not fixed here — aligning
`flatsql` across the suite manifest is somebody's deliberate call about which of
2.0.3 and 1.4.5 is correct, not an incidental edit in a dependency audit.

## Corrections to the read-only pass

The read-only audit that preceded this was substantially right. Four things it
got wrong, all **verified**:

- **"The published package has never run the vitest suite in CI."** Overstated.
  `ci.yml` → `scripts/ci-local.sh quick` → `run_sdn_js` runs `npm test` on every
  push and PR to main. What was true is narrower: the *publish workflow* had no
  test gate, and it can fire from a release cut off any commit. Fixed anyway.

- **"`monaco-editor` is a production dependency dragging the vulnerable
  dompurify."** True, and worse than stated: `monaco-editor` has **zero imports
  anywhere in the repository** — grep over all `.ts/.js/.mjs/.svelte/.html/.json`
  outside `node_modules` finds only its own line in `package.json`. The override
  was protecting code that never loads.

- **"`metro`/`image-size` highs arrive via `@libp2p/webrtc@6.0.16`."** Half
  right. They arrive via **both** webrtc copies: `@libp2p/webrtc@6.0.16` →
  `react-native-webrtc@124`, and the `@spacedatanetwork/libp2p-webrtc-v1` alias
  (`@libp2p/webrtc@4.1.10`) → `react-native-webrtc@118`. Both declare
  `react-native` as a **peerDependency**, which npm auto-installs. **Measured**:
  removing `@libp2p/webrtc@^6.0.16` entirely leaves the advisory count unchanged
  at 8. It is dead weight (also zero imports — only the v1 alias is imported),
  but it is not a security lever.

- **"`/sdn/pnm/1.0.0` (epm-resolver.ts:89) has no server-side match — worth
  confirming whether that is a dead constant."** It is not a stream protocol, so
  the absence of a `handle()` on the Go side proves nothing. It is a **pubsub
  topic** (`pnmTopic`), live at `epm-resolver.ts:234` (`pubsub.subscribe`) and
  `:238`. The real finding stands and is sharper: `grep -rni '/sdn/pnm' sdn-server
  --include='*.go'` is empty, so sdn-js subscribes to a topic no Go node
  publishes. A live but inert subscription.

## Wire protocols

**Verified** byte-identical across the two implementations:

| protocol | JS | Go |
|---|---|---|
| `/space-data-network/flatsql-sync/1.0.0` | `flatsql-sync.ts:1` | `internal/datasync/datasync.go:21` |
| `/space-data-network/module-delivery/1.0.0` | `module-delivery.ts:30` | `internal/modulert/manifest.go:28` |

The Go node deliberately omits `dht.ProtocolPrefix` so it speaks stock
`/ipfs/kad/1.0.0` rather than a private swarm (`node.go:338-343`, comment quoted
verbatim there). Both JS entry points build the DHT with `clientMode: true`
(`helia.ts:621`, `node.ts:355`).

**Identify is stubbed by default on both JS entry points**:
`config.enableIdentify === true ? identify() : identifyCapabilityOnly()`
(`helia.ts:624`, `node.ts:359-361`). A browser tells our Go node nothing about
itself unless explicitly enabled. `internal/node/peer_admission_policy.go`
documents the 2026-08-06 outage this caused and the fix that landed — recognize
browsers by SDN topic membership plus loopback-tunnel provenance rather than by
protocols. That fix is in place.

**Not verified**: I did not run a live browser↔Go handshake. The claim that the
`peer_admission_policy.go:199-202` comment is now partly stale (it describes what
Identify reports, which only holds when `enableIdentify` is set) is **inferred**
from reading both sides, not observed.

## Vulnerabilities

`npm audit --omit=dev` after the dompurify fix: **8 — 1 low, 7 high, 0 moderate.**
All seven highs are deferred with dates in `scripts/check-npm-audit.mjs`.

Two matter:

1. **GHSA-vrf4-mx87-p53w, CVSS 8.2** — `@libp2p/peer-store >=8.0.0 <12.0.24`
   accepts attacker-signed PeerRecords for a victim peer ID and stores certified
   attacker addresses. All three installed copies affected (ours at 10.1.5 via
   libp2p 1.9.4; `@helia/utils` and `helia` nested at 12.0.15). An
   address-poisoning primitive against a *client*, and our clients dial bootstrap
   and relay peers. **There is no in-range fix**: 1.x is EOL at 1.9.4. Only the
   libp2p 3.x move reaches it. Deferred to 2026-12-31.

2. **GHSA-32mq-hpph-xfvr, CVSS 7.5** — `@libp2p/kad-dht <16.2.6`, unvalidated
   `PUT_VALUE` → unbounded disk exhaustion on DHT **server** nodes. Mitigated for
   us by `clientMode: true` at both call sites. Residual exposure is a Node
   consumer of the published package that enables DHT server mode itself.

The rest is React Native build tooling that never executes in a browser or in the
published `dist/`, deferred to 2027-03-31 with that reasoning recorded.

## Go side

`govulncheck ./...` against `sdn-server`, **verified** by running it
(`scripts/run-govulncheck.sh`): **30 vulnerabilities affecting called code.**
They split cleanly:

- **20 stdlib**, all fixed in go1.26.3–go1.26.6 (scanned on go1.26.1). These are a
  **toolchain** lever, not a `go.mod` one, and differ per runner — which is why
  `check-govulncheck.mjs` reports but does not gate them.
- **10 module**, decided by `go.mod` and identical on every runner, so they are
  baselined and gated:

| id | module | found | fixed in |
|---|---|---|---|
| GO-2026-6165 | `github.com/pion/dtls/v3` | v3.0.6 | v3.1.4 |
| GO-2026-6099 | `github.com/quic-go/webtransport-go` | v0.9.0 | v0.11.1 |
| GO-2026-4488/4485/4483 | `github.com/quic-go/webtransport-go` | v0.9.0 | v0.10.0 |
| GO-2026-5676 | `github.com/quic-go/quic-go` | v0.58.1 | v0.59.1 |
| GO-2026-5970 | `golang.org/x/text` | v0.37.0 | v0.39.0 |
| GO-2026-5026 | `golang.org/x/net` | v0.54.0 | v0.55.0 |
| GO-2026-4479 | `github.com/pion/dtls/v2` | v2.2.12 | **no fix** |
| GO-2024-3218 | `github.com/libp2p/go-libp2p-kad-dht` | v0.36.0 | **no fix** |

GO-2024-3218 (CVE-2023-26248) has `introduced: 0` and no fixed event — unfixed in
every version, a property of the Amino DHT rather than an upgrade lever.

Eight of the ten have fixes available and want their own reviewed `go.mod` bump.
Not done here: that is a Go dependency wave, not a helia audit, and bundling it
would make a failure unbisectable.

## Kubo

`sdn-server` does **not** depend on kubo as a module. **Verified**: no `go.work`
in the tree, no `replace` in `sdn-server/go.mod`, and
`grep -rn 'ipfs/kubo' sdn-server --include='*.go'` is empty. There is instead a
fully tracked vendored kubo source tree at `kubo/` — its own module
`github.com/ipfs/kubo` at `CurrentVersionNumber = "0.40.0-dev"`.

So the bolt-on is **two independent Go modules kept in lockstep by hand**, and
until `scripts/check-kubo-lockstep.mjs` nothing asserted the lockstep. Both trees
keep compiling when they drift; the divergence surfaces only as peers that stop
interoperating.

**Verified** by parsing both `go.mod` files: 138 shared modules, 19 divergent.
Of the 34 shared libp2p/IPFS/multiformats modules, **33 agree exactly**,
including the two that decide interop:

```
github.com/libp2p/go-libp2p            v0.46.0   (both)
github.com/libp2p/go-libp2p-kad-dht    v0.36.0   (both)
github.com/libp2p/go-libp2p-pubsub     v0.15.0   (both)
github.com/multiformats/go-multiaddr   v0.16.1   (both)
```

The one divergence is `github.com/ipfs/boxo` — sdn-server v0.35.2 (indirect) vs
kubo v0.35.3-0.20260109213916-89dc184784f2 (direct). Recorded as a dated
`DECLARED_DIVERGENCES` entry rather than papered over; the check fails if it ever
*converges* too, so the entry cannot rot.

Worth a separate look, **not** acted on here: `kubo/go.mod` pins
`spacedatastandards.org/lib/go v1.154.0` (through a `replace` to
`kubo/sdn/third_party/spacedatastandards-go`) while `sdn-server` is on
**v1.217.0**. 63 minor versions apart, inside a tree whose whole premise is
lockstep.

Upstream kubo is v0.43.1 (2026-09-14) vs our 0.40.0-dev, on go-libp2p v0.49.0 /
kad-dht v0.42.2 / boxo v0.43.0. Moving the vendored tree means moving
`sdn-server`'s pins with it, in one commit, or the lockstep check fires — which
is the point.

## The upgrade plan (not done here)

Ordered. Do not bundle.

**Step 1 — helia family minors**, as its own reviewed commit: 6.0.22→6.1.4,
unixfs 7.1.0→7.2.1, block-brokers 5.1.4→5.2.4, routers 5.0.3→5.1.1. Fixes nothing
security-wise. Its value is that it stops being one `npm update` away from
happening unreviewed — and `check-sdn-js-dependency-layering.mjs` now forces it
to be a deliberate act. **This is the step most likely to break the shims**: they
are calibrated against 6.0.22's exact calling convention for `handle()` and
`dialProtocol()`, and a 6.1.x minor is precisely where upstream would change that
without considering it breaking. The 37 helia tests exercise the backpressure and
verification paths and are a real gate.

**Step 2 — the real one: our direct libp2p 1.9.4 → 3.3.11**, and the `@libp2p/*`
family with it. This is the only way to reach `@libp2p/peer-store >=12.0.24` and
close the 8.2. It pays twice: it collapses the dual stack (helia@6 wants
`libp2p ^3.0.6`, so afterward there is ONE libp2p 3.x), and it *should* let the
entire compat layer be deleted.

Wire interop is **not** the danger — the protocol IDs are byte-identical across
majors. The dangers are API churn, and these are **verified** present in our
source:

- `connectionEncryption` was renamed `connectionEncrypters` in libp2p 2.x. We use
  the old name at `helia.ts:633` and `node.ts:373`.
- `peerIdFromKeys` was **removed** in `@libp2p/peer-id` 5.x. We import and call it
  at `node.ts:20` and `node.ts:399`, feeding it `marshalSecp256k1PrivateKey` /
  `marshalSecp256k1PublicKey` output. This is identity derivation: getting it
  wrong changes every browser's PeerID.
- `@spacedatanetwork/libp2p-webrtc-v1` is an alias to `@libp2p/webrtc@^4.1.10`
  held deliberately old alongside the current `@libp2p/webrtc@^6.0.16`. It is the
  one that is actually imported (`node.ts:10`, `helia.ts:33`,
  `ui/runtime/sdn-backend-libp2p-sync.ts:527`). Somebody must state why before it
  moves; that pin looks load-bearing.

That deleting the shims follows from Step 2 is **inferred** from helia 6's
declared deps and the shim comments — it was not proved by doing the upgrade. If
helia 6 calls into libp2p in ways 3.2.0 also does not satisfy, some shim
survives.

**Step 3 — defer helia 6→7.** helia 7 requires `multiformats ^14` (we are on
13.4.2), `@helia/unixfs` 8 requires `@helia/interface ^7.1.1`, and helia 7 drops
`libp2p` as a dependency entirely in favour of `@helia/libp2p ^2.1.3`, changing
node construction. It is a multiformats 13→14 migration across the whole package,
since `multiformats` is imported well beyond helia. Bundled with Step 2 it would
be unbisectable.

**Dead weight to clear whenever a consumer-visible dependency change is
acceptable** (neither is a security lever; both measured):

- `monaco-editor@^0.55.1` — production dependency, zero imports repo-wide.
- `@libp2p/webrtc@^6.0.16` — production dependency, zero imports; only the v1
  alias is used.

## What was not verified

- No upgrade of any kind was attempted.
- No live browser↔Go interop test was run.
- The Go fleet's own DHT-server exposure to the `PUT_VALUE` class of issue is a
  separate implementation (`go-libp2p-kad-dht` v0.36.0) and a separate question
  from the JS advisory.
- Trivy and CodeQL findings were not triaged; both lanes remain reporting-only on
  purpose, because a gate over untriaged findings is a gate people learn to
  ignore.
