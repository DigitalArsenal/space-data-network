# Space Data Network Repo Contract

## Runtime Contract

- Browser and Node consumers use `sdn-js` plus the generic async capability surfaces from `space-data-module-sdk`.
- Public encrypted module delivery stays on `/space-data-network/module-delivery/1.0.0`.
- Provider discovery is anchored by the provider compressed secp256k1 public key and the DHT namespace `space-data-network/module-delivery/provider-pubkey`.
- Clients must trust the provider descriptor's advertised relay addresses. The live demo seed currently advertises `104.131.11.220`; do not force the older `159.203.150.8` address.
- Module bundles stay encrypted in transit and at rest and are only decrypted locally after the requester receives the wrapped content key in `GrantResponse`.
- The browser path stays direct and must not route through a helper service or the legacy discovery bootstrap.
- Seed from the approved demo relay, then expand discovery with libp2p and the DHT.
- `hd-wallet-wasm` and `hd-wallet-ui` are the canonical address, signature, and vCard identity surfaces.
- Ownership boundary stays split as follows: `spacedatastandards.org` owns canonical FlatBuffer schemas, `space-data-module-sdk` owns shared invoke/licensing helpers and runtime host surfaces, `sdn-server` owns the provider-side host bridge and unified licensing-module runtime loading path, and `sdn-js` owns requester-side discovery, relay selection, and encrypted bundle fetch behavior.

## Product Surfaces

- `/` is the SDN UI.
- `/webui` is the upstream-style IPFS WebUI.
- `/admin` is reserved for admin and auth flows.

Keep those surfaces separate. Do not reintroduce a combined admin/WebUI mount.

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
