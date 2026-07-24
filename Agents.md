# Space Data Network Repo Contract

## Runtime Contract

- Browser and Node consumers use `sdn-js` plus the generic async capability surfaces from `space-data-module-sdk`.
- Public encrypted module delivery stays on `/space-data-network/module-delivery/1.0.0`.
- Provider discovery is anchored by the provider compressed secp256k1 public key and the DHT namespace `space-data-network/module-delivery/provider-pubkey`.
- Clients must trust the provider descriptor's advertised relay addresses. Production runs two bootstrap nodes: `sdn.spaceaware.io` (`159.203.150.8`, PeerID `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`) and `celestrak.eth` (`167.172.219.213`, PeerID `16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3`); direct pinned IP multiaddrs remain authoritative until `_dnsaddr.bootstrap.spacedatanetwork.org` TXT records are published.
- Module bundles stay encrypted in transit and at rest and are only decrypted locally after the requester receives the wrapped content key in `GrantResponse`.
- The browser path stays direct and must not route through a helper service or the legacy discovery bootstrap.
- Seed from the production bootstrap nodes, then expand discovery with libp2p and the DHT.
- `hd-wallet-wasm` and `hd-wallet-ui` are the canonical address, signature, and vCard identity surfaces.
- Ownership boundary stays split as follows: `spacedatastandards.org` owns canonical FlatBuffer schemas, `space-data-module-sdk` owns shared invoke/licensing helpers and runtime host surfaces, `sdn-server` owns the provider-side host bridge and unified licensing-module runtime loading path, and `sdn-js` owns requester-side discovery, relay selection, and encrypted bundle fetch behavior.

## Application-Blind Go Host

- Supplemental OMM is one standalone signed WASM/PLG flow bundle composed of
  independently signed and instantiated WASM nodes. Its application identity,
  providers, graph, scheduling semantics, state, routes, and UI must not be
  implemented or special-cased in handwritten Go.
- Handwritten Go may provide only application-blind runtime capabilities such
  as generic signature verification, artifact loading, generic wakeups, HTTP
  mounting, networking, filesystem access, shared-arena validation, and opaque
  binary persistence bridging. It may not implement cron policy, FlatSQL,
  schema-aware storage, or any flow node.
- Canonical SDS OMM schema and binding support remains allowed because OMM is a
  data standard, not application control-plane code.
- `scripts/check-no-app-specific-go.mjs` enforces this boundary across every
  tracked Go path and source file outside `vendor/` and `third_party/`. There is
  no handwritten-Go exception process; application behavior belongs in the
  signed WASM artifact.
- Supplemental OMM implementation work is scoped to
  `repos/main-packages/space-data-network-modules/flows/supplemental-omm/` by
  default. Before editing any handwritten Go for this effort, stop and obtain
  explicit owner approval for the exact Go files and generic host capability
  involved. Do not infer that approval from an implementation plan or from the
  fact that a proposed change is application-blind.

## Isomorphic Flow Host Boundary

- A signed flow uses the exact same `dist/isomorphic/module.wasm` bytes and
  content hash in the JavaScript/browser harness and WasmEdge. Do not create a
  Go-controlled server variant or a JavaScript-controlled browser variant.
- Go and JavaScript hosts are parity implementations of generic adapters only:
  artifact verification/loading, declared capability enforcement, opaque byte
  persistence, clocks/wakeups, network byte streams, shared arenas, bounds
  checks, threads, cancellation, and teardown.
- FlatSQL must run as a signed, pluggable WASM node in the flow. Hosts must not
  expose FlatSQL tables, rows, SQL/query operations, schema-aware indexes, or a
  `storage_engine_link` substitute. The flow compiler must not statically fold
  FlatSQL into a consuming application module.
- Cron parsing, timezone, retry, misfire, and trigger policy belong to a timer
  WASM node. The host may only provide a clock and generic wakeup delivery.
- Every module input and output port supports both canonical SDS FlatBuffer and
  aligned-binary representations for the same schema identity. `PIV` and
  `TAB.WIRE_FORMAT` select per frame.
- Use aligned-binary only inside a proven-compatible shared arena. Network,
  process, persistence, publication, and incompatible-memory edges fall back to
  canonical FlatBuffer bytes. Hosts route and bounds-check descriptors but do
  not decode or transcode application schemas.

## Product Surfaces

- `/` is the SDN UI.
- `/webui` is the upstream-style IPFS WebUI.
- `/admin` is reserved for admin and auth flows.

Keep those surfaces separate. Do not reintroduce a combined admin/WebUI mount.

## Node Security Guardrails

- Public server routes must be explicit and method-aware. Do not add broad
  public prefixes such as all of `/api/auth/`, `/api/v0/`, or `/api/v1/data/`
  when only specific read/challenge endpoints should be unauthenticated.
- Server control-plane mutations must require the wallet-cookie auth scheme
  (`sdn_wallet_session`) and the appropriate trust level. Kubo RPC proxy
  routes, frontend management, plugin upload, module runtime actions, peer ACLs,
  and auth user management are admin-only unless a narrower documented
  capability flow exists.
- Admin grant/revoke flows use `/api/auth/users` and `/api/auth/users/{xpub}`.
  Config-managed admins are authoritative config entries and must not appear
  revocable through the UI or API unless the config itself changes.
- Local node EPM profile edits must persist as encrypted size-prefixed
  `EPM.fbs` bytes in the FlatSQL-backed EPM store. Do not persist editable
  profile JSON, EPM JSON projections, plaintext `keys/epm-profile.json`, or ad
  hoc JSON files as the active source of truth; JSON views must be derived from
  the FlatBuffer only at compatibility/API edges.
- Backend-to-frontend EPM transport must use raw FlatBuffer bytes from
  `/api/node/epm` with `application/x-flatbuffers`. The UI may decode those
  bytes locally for forms, but it must not prefer `/api/node/epm/json`,
  `/api/node/info`, or any other JSON profile endpoint for node identity data.
- SDN UI node-self profile edits must call `/api/node/epm`. Hosted identity
  rows may use `/api/identity/epms/{id}`, but `/api/identity/epms/self` must
  not become a second node-profile write path.

## Desktop Peer And Gateway Guardrails

- The desktop SDN menu must route to SDN UI pages under `/sdn`; the IPFS menu
  may route to `/webui`. Do not send SDN status, files, peers, explore,
  settings, or dashboard actions to upstream IPFS WebUI routes.
- Never treat SSH host aliases, DNS names, or configured seed labels such as
  `space-data-network-01`, `sdn.spaceaware.io`, or `celestrak.eth` as libp2p
  Peer IDs. Configured hosts may seed connection attempts, but the UI may show
  an SDN peer only when Kubo or SDN identify data provides a real Peer ID and
  SDN protocol or agent evidence.
- The local desktop SDN static server must expose the SDN API routes used by
  bundled SDN UI pages, including `/api/peers/sdn`, `/api/peers`,
  `/api/peers/graph`, and `/api/node/epm/json`.
- The local desktop SDN static server must remain loopback-bound and must reject
  requests whose `Host` header is not a local host form (`localhost`,
  `127.0.0.1`, `0.0.0.0`, or `::1`) before API or static routes respond.
- Desktop WebUI and SDN UI windows must inject both the live Kubo RPC API
  address and the live Kubo gateway address before the bundled UI initializes.
  The IPLD explorer must use the injected gateway, not a hard-coded `8080`.
- Keep desktop bootstrap configuration on upstream-compatible IPFS defaults
  plus real SDN seed multiaddrs with real `/p2p/<peer-id>` values.

## Upstream IPFS Mirrors

- Treat `webui/` and `desktop/` as upstream IPFS WebUI and IPFS Desktop mirrors.
- Do not add long-lived SDN product behavior directly inside those mirrors.
- SDN-specific behavior belongs in `sdn-js/ui/src/upstream-webui/overrides/`,
  generated snapshots under `sdn-js/ui/src/upstream-webui/vendor/`, or explicit
  patch/adapter files that can be reapplied after an upstream refresh.
- Do not make Kubo depend on SDN-only custom protocol origins such as
  `webui://-` or `sdn://-` for WebUI RPC access. Prefer upstream-compatible
  HTTP origins and upstream desktop loading patterns.
- Upstream update work must fetch the chosen upstream revision, refresh the
  mirror tree, refresh SDN vendor snapshots, reapply overlays, and run focused
  contract tests before deployment.
- Any direct edit to `webui/` or `desktop/` must be identified as either an
  upstream mirror refresh or temporary migration debt with a same-change plan to
  move the behavior into the overlay/patch layer.

## Marketplace And Schemas

- `spacedatastandards.org` owns the canonical FlatBuffer schemas.
- `PLG` is the single canonical signed marketplace manifest and storefront listing for a module version.
- There should be exactly one canonical listing per `PLUGIN_ID + VERSION`.
- OrbPro module-delivery seed catalogs must not publish built-in Sandcastle
  SDK artifacts as remote downloadable modules. Built-in OrbPro/Sandcastle
  module bytes are shipped locally as encrypted artifacts and SDN provides only
  the `com.orbpro.wasm-engine@1.0.0` grant/key needed to decrypt those bytes.
  There is no `com.orbpro.wasm-engine-sdk` listing, catalog entry, or
  publication.
- Storefront and third-party modules remain allowed to publish encrypted module
  bytes by module ID through the decentralized module-delivery path. Keep tests
  for remote module download by ID, but do not use that path for built-in
  OrbPro/Sandcastle dependencies.
- When deploying or repairing the live OrbPro licensing provider, verify the
  active plugin root loaded by the running service, not only a mirror or stale
  data directory. The active `com.orbpro.wasm-engine` key must match the current
  OrbPro built-in wasm-engine artifacts, otherwise the browser can receive a
  grant but decrypt local bytes into non-WASM garbage.
- Search and discovery must derive from `PLG` itself, not a second listing record.
- If `PLG` needs new fields, define them upstream first and consume the published bindings here.
- Do not create repo-local `.fbs` files or shadow schema bindings.

## Current TODOs

- Publish the canonical `PLG` storefront extension from `spacedatastandards.org`.
- Consume the published `spacedatastandards.org` versions in the JavaScript and Go paths used by this repo.
- Ship the `sdn-js` browser UI so it can run browser-only and server-hosted from the SDN daemon with the same UI core, using the generic async capability surfaces from `space-data-module-sdk`.
- Show the real module-delivery flow in real time from live comms: provider discovery or connect, challenge, grant, encrypted CID fetch, content-key unwrap, decrypt, SDK load, and invoke result.
- Show `Observed SDN peers` based on real seed, DHT, provider, protocol, and identity evidence.
- Add blockchain address lookup through a deterministic DHT namespace and verified EPM chain proofs.
- Embed `hd-wallet-ui` directly into the SDN Identity view instead of rebuilding that UI locally.
- Keep the IPFS WebUI entrypoint IPFS-only and available separately at `/webui`.
- Remove `spacedatastandards-site` as an active product surface.
- Never revive legacy `/orbpro/*` broker or bootstrap paths.

## Editing Rules

- Prefer focused patches in the smallest owned area.
- Use `apply_patch` for manual edits.
- When a sibling repo is dirty, use a clean worktree instead of editing through the dirty checkout.
- Do not revert unrelated user changes.
- Verify route changes with focused Go tests and WebUI builds.
- Verify UI/runtime changes with focused `sdn-js` tests and the UI build.
- For any change under `desktop/` or any behavior that affects SDN Desktop
  packaging, startup, tray/menu routing, updater behavior, or bundled WebUI/SDN
  UI assets, package the desktop app, reinstall or refresh the local installed
  app, and restart the local desktop app before reporting completion. If one of
  those steps cannot be completed, report the exact blocker and do not present
  the desktop work as fully installed locally.
