# Module signature trust lists (audit, 2026-11-06)

Report-only audit after the owner-key signing decision (modules main `a838d84501`: `scripts/trusted-publishers.json` holds `module-owner-2026-10-15`; modules `cfd750eb8` ships sdn-ap 0.6.4 as the first signed module version). Question: which public keys do SDN hosts, the PMM catalog publisher, the deployment manifests, the console and the sdn-js loader trust for module signatures, and is the public SDK development key among them. Nothing was changed; no host was touched; no secret was read.

Keys named in this document (public halves only):

| Label | keyId / fingerprint | Public key (hex) | Custody |
| --- | --- | --- | --- |
| SDK development key | `sdm-dev-test-2026` | `cf4625795484d8efe18860141cfdeaaaed7bbee9209488405b6ddeac7543fe78` | Private seed committed in the public SDK repository (`space-data-module-sdk/test/support/dev-module-signing-keypair.json`). Anyone can sign as it. |
| Owner module-signing key | `module-owner-2026-10-15` | `c04e832f741859a36d6f50909668cfad836700da3dfd23cb737f4ef033265c34` | Keypair outside every repository, named by `SDM_MODULE_SIGNING_KEYPAIR_PATH`. |
| host-01 node publisher key | peer `16Uiu2HAmNpWvv4aG4U67cex3LfUJpdrXKXbvcxn6Gm33RPM3MTCf` | `018c8bb1bfc7d7f957e508bec81f6364e796ca7c950e24cb2ef3db9363de00c2` | The node's own Ed25519 signing key since the SEC-001 re-key of 2026-10-15 (`deployment/star-catalog-provider-node/host-01.sh:52-56`). Earlier records (`deployment/signing.json`, `deployment/topology.json:305-310`) name the pre-re-key fingerprint `0d80e1fd`. |
| Isomorphic module-artifact key | `spaceaware-sdn-isomorphic-2026-08-06` | `d4b97660cea81cff7db4bccc8be327efb259ec7b48cf8bf6c0d7d9819b1537fd` | Owner laptop only (`deployment/signing.json` key_custody.isomorphic_module_signing_key_20260806). |
| Orphan | none | `088ac3d85932dc6946e3ff62882afb48885c2df0cd8b34f2993c5885e3424d89` | No private half exists anywhere in the stack (`deployment/signing.json` live_key_facts_20260730). |
| OrbPro dev-host key | none | `eb632827fcd519eb81c454bd64822cc1a470a9ba694cb87a3c6dbc72c5ff4761` | Dev host keypair file, path only (`deployment/orbpro-dev-host/README.md:134`). |

## Finding

**No production trust list names the SDK development key.** Every list below was read from the repositories at the SHAs stated; the dev key appears only where it is refused (OrbPro's revocation list, the modules classifier) or in test fixtures. Production hosts (host-01 `sdn.spaceaware.io`, host-02 `celestrak.eth`) trust their own node key plus the operator env list `SDN_TRUSTED_PUBLISHER_KEYS`, last recorded as `088ac3d8…,d4b97660…` on both units (`deployment/topology.json:310`, measured 2026-08-26); that list carries the orphan key, not the dev key. Host state after 2026-08-26 is not verifiable from the repositories (no manifest in any repository sets that variable; it lives in systemd drop-ins on the hosts).

Related, not a trust-list finding: the public SDN-AP site serves two modules signed by the dev key (`foundation/frames` 0.2.0 and `files/ccsds-messages` 0.3.0; both in the modules classifier's dev-key row, `scripts/tests/module-signatures-20261106.md:12`) and loads them without any signature check. Documented in the companion sdn-ap audit.

## Search

Repositories and SHAs: sdn `5a348927d` (this checkout), stack `deployment/` at `bcf276620d`, modules `cfd750eb8` (contains `a838d84501`), SDK `5c6a05a49`, OrbPro `54d3fc350`, sdn-ap `64aef5032`. Excluding `node_modules`, `.git` and `dist`:

1. Fixed strings `sdm-dev-test-2026`, `module-owner-2026-10-15`, the dev key hex `cf462579…fe78` and the owner key hex `c04e832f…5c34` in every repository above.
2. Identifiers `SDN_TRUSTED_PUBLISHER_KEYS`, `SDN_MODULE_SIGNATURE_ENFORCE`, `SDM_TRUSTED_MODULE_SIGNERS`, `SDM_REQUIRE_MODULE_SIGNATURE`, `__SDM_TRUSTED_MODULE_SIGNERS__`, `__SDM_REQUIRE_MODULE_SIGNATURE__`, `TrustedSigners`, `trustedPublicKeys`, `trusted-publishers`, `trusted-module-signers`, `trusted-signer`, `--trusted-public-key`, `--signer-public-key`, `SIGNING_PUBLIC_KEY`, `verifyModuleArtifact`, `verifyNodeSignedModule`, `verifySignature`, `SetModuleSignaturePolicy`.
3. The sdn-js wasm load sites (`WebAssembly.compile`, `WebAssembly.instantiate`, `createBrowserModuleHarness`, `loadModule(`).

## Trust locations

Paths are repository-relative; `stack:` is the super-repository.

### SDN hosts (sdn-server, Go)

| Location | Trusted keys | Dev key? | On production hosts |
| --- | --- | --- | --- |
| `sdn-server/internal/node/module_signature_policy.go:79-121` `buildModuleSignaturePolicy` | Root 1: the node's own Ed25519 signing key (`signingPublicKey()`, lines 129-138). Root 2 (additive): every 64-hex key in env `SDN_TRUSTED_PUBLISHER_KEYS` (line 56; parser lines 148-172). | No. The node has no built-in key list; the dev key enters only if an operator puts it in the env. | Yes: `internal/node/node.go:957` attaches the policy to every node; `node.go:1196` hands it to the flow manager (`internal/flowrt/manager.go:48`). host-01 and host-02 units: `SDN_TRUSTED_PUBLISHER_KEYS=088ac3d8…,d4b97660…` (`stack:deployment/topology.json:310`, 2026-08-26). |
| `sdn-server/internal/node/module_signature_policy.go:66,80-83` `SDN_MODULE_SIGNATURE_ENFORCE` | Mode, not a key: unset = report-only (verify, log `module_signature_observe`, admit everything, `internal/modulert/publication_signature.go:151-171,410-415`); truthy = fail closed. | n/a | No repository record of the flag being set on host-01 or host-02; the last recorded state is report-only (`stack:deployment/signing.json` gaps[], `topology.json:420` for the catalog instances). |
| `sdn-server/internal/modulert/publication_signature.go:123-171,392` `ModuleSignaturePolicy` | `TrustedSigners` as built above; `AllowUnsignedByContentHash` (empty map at build). | No | Yes (the type the node attaches). |
| `sdn-server/cmd/spacedatanetwork/module_sign_cli.go:48-56,96` `plugins sign --trusted-public-key` | One expected key per invocation; the CLI verifies the signature the node just produced against it. Deploy scripts pass the node's own publisher key (`stack:deployment/star-catalog-provider-node/host-01.sh:56,150`; `terrain-refresh/deploy/host-01.sh:43`; `solar-system-provider-node/deploy/host-01.sh:85`; host-02 scripts read a `publisher-key` file beside the script, `object-covariance-provider-node/deploy/host-02.sh:31,37`, `space-weather-provider-node/deploy/host-02.sh:38`). | No | Per publication, not a standing list. |
| Capability ledger `<storage>/modules/capability_policy.json` (`plugins capabilities approve --signer-public-key`, `stack:deployment/star-catalog-provider-node/host-01.sh:161`, `object-covariance-provider-node/deploy/host-02.sh:38-42`) | Rows bind an approved capability to an exact portable hash and the signer public key (`018c8bb1…` on host-01; the host-02 `publisher-key` file). | No key in the repository is the dev key. | Yes, on-host data (not readable here). |
| `sdn-server/internal/update/trustroots.go:48`, `stack:deployment/release/fleet-trust-roots.json` | Update-manifest root `d4a971a7e534` (= pre-re-key node key `0d80e1fd`). Not a module trust list (`topology.json:310` "DO NOT CONFLATE LANES"). | No | Yes (update lane only). |

Config: `sdn-server/config.example.yaml` and `config/*.yaml` define no module-signer key; the only trust words there are account `trust_level` rows (`config/dev.yaml:63`, `config/dev-docker.yaml:64`).

### PMM catalog publisher and its trust anchor

| Location | Trusted keys | Dev key? | On production hosts |
| --- | --- | --- | --- |
| `sdn-server/internal/node/pmm_plugin.go:269-284` `pmm.TrustAnchor` | `SIGNING_PUBLIC_KEY` = the node's own signing key, read from the node identity at build time (`internal/pmm/manifest.go:43-51`); every `ARTIFACT_SIGNATURE` and the manifest signature resolve to it (`manifest.go:11-20,290-291`). | No | Yes: host-01 serves `/.well-known/sdn/modules.pmm` from `<storage>/modules/modules-catalog.json` (`stack:deployment/pmm-catalog/refresh-catalog.mjs:3-9,46-49`). |
| `stack:deployment/pmm-catalog/refresh-catalog.mjs:22,26,311-316,357-364` `--trusted-signer` | One key per `--add-artifact` / `--replace-artifact`; the artifact's `sds.signature` must verify under exactly that key. Lines 498-508 classify every served row's trailer signature against its own embedded key (informational, self-referential). | No default key; the operator names it. | The publish is the owner's (host-01 catalog publish). |
| `stack:deployment/ephemeris-provider-nodes/publish-catalogs.mjs:141-152`, `verify-catalogs.mjs:41-65` | `receipt.publicKey` / `installed.publicKey` = the provider node's own key, checked equal to `manifest.TRUST.SIGNING_PUBLIC_KEY`. | No | Provider-node catalogs (host-01, host-02 instances). |

### Deployment manifests (`stack:deployment/`, read-only)

| Location | Trusted keys | Dev key? | Production? |
| --- | --- | --- | --- |
| `signing.json:109-113` key_custody.dev_module_signing_key | Records the dev key as RETIRED and NEVER ALLOWLISTED (council 2026-07-28, owner 2026-07-30). Descriptive, not a trust list. | Named, refused. | n/a |
| `signing.json:271-274,345`; `topology.json:310` | Env list `088ac3d8…` (orphan) and, since 2026-08-26, `d4b97660…` on both live units. Filed for removal, not actioned. | No | Yes (host-01, host-02). |
| `orbpro-dev-host/host/trusted-module-signers.txt:9,12` | `eb632827…` (dev-host key) and `c04e832f…` (owner key). Served to pages as `__SDM_TRUSTED_MODULE_SIGNERS__` (`orbpro-dev-host/README.md:74-76,106`; `deploy-orbpro-dev.sh:230-258`; `verify-dev-host.mjs:223-257`). The engine's revocation list refuses a list naming the dev key (`deploy-orbpro-dev.sh:233-239`). | No | No: the OrbPro dev host. |
| `ut-austin-core/sign-artifact.mjs:20-23`, `stage.mjs:67-75`, `install-flow.mjs:22-26`; `open-meteo/sign-flow.mjs:38-39`, `gibs-provider-node/sign-flow.mjs:41-42`, `launch-provider-node/sign-flow.mjs:38-39`, `solar-system-provider-node/sign-flow.mjs:59-60` | `signed.public_key_hex` returned by `POST /api/v1/admin/modules/sign`: the signing node's own key, verified once after signing. | No | Publication helpers; no standing list. |
| `topology.json:77` (`science_role_assignments_20260906.nodes.ut_austin`) | `module_signature_policy: enforcing` on an owner-local development instance (`host: owner-local development instance`). | n/a | No. |

### Console (`sdn-js/spaceaware-ui`, pin `f8518df15c`)

| Location | Trusted keys | Dev key? | Production? |
| --- | --- | --- | --- |
| `src/orbital-console/data/modules/scenarioModules.ts:251-345` `compileVerifiedArtifact`; `verifyPmm.ts:112-117,187-237,254-273` | Only `manifest.TRUST.SIGNING_PUBLIC_KEY` (the node key; `moduleCatalogPort.ts:36`): portable bytes -> `CONTENT_HASH` -> `ARTIFACT_SIGNATURE` under that key. The appended SDS trailer, including any `sds.signature`, is stripped before hashing (`stripPublicationTrailer`, line 301) and never judged; the trust anchor's own weight is the PMM domain check and the DNS proof (`src/standards/PMM/PMMTrustAnchor.ts`). The compiled module is handed to the engine as a `WebAssembly.Module` (`scenarioModules.ts:468`). | No. The SDK/owner key layer does not exist on this path. | Yes: the signed-out console at spaceaware.io and the node homepage. |
| `src/dashboard-tailadmin/data/pmm.js:279,313` | Same anchor for the dashboard's manifest view. | No | Yes. |
| `src/tools/milky-way-model-protect.mjs:72` `OWNER_MODULE_SIGNER` | Owner key, build tool only. | No | Build tool. |
| `src/tools/make-pmm-fixture.mjs:177-182` | A freshly generated local key for a test fixture. | No | Test only. |

No console source sets `__SDM_TRUSTED_MODULE_SIGNERS__` or passes `trustedPublicKeys`; the ambient SDK policy is unset in the console.

### OrbPro engine as embedded by the console (`54d3fc350`)

| Location | Trusted keys | Dev key? |
| --- | --- | --- |
| `packages/engine/Source/Core/moduleSignerTrust.js:16-17` | Default signer: the owner key. | |
| `moduleSignerTrust.js:26-28` `REVOKED_MODULE_SIGNER_PUBLIC_KEYS` | The dev key, refused whatever a caller or deployment configures (`verifyModuleSignature.js:81-95,152-167`). | Named, refused. |
| `verifyModuleSignature.js:98-171` | No policy: bytes load unverified except a dev-key signature throws; `requireSignature: true` or any named key fails closed; `sdnModuleDeliveryDecryptor.js:866-884` requires a signature by default. | No |
| `packages/orbpro-integration/test/productionModuleSignerTrust.test.mjs` | Test that no shipped OrbPro source lists the dev key. | Enforced. |

### sdn-js loader (`sdn-js/src`)

| Location | Trusted keys | Dev key? | Production? |
| --- | --- | --- | --- |
| `sdn-js/src/module-artifact.ts:115-147` `verifyNodeSignedModule`; `local-flatsql.ts:248-252` | The node-signed form only (`SDN-MODULE-PUBLICATION-V1` statement); the single trusted key is `publisherKey`, which the node reports for its engine (`engine.publisherKey`; a missing key is an error, line 249). | No | Yes (the node homepage's FlatSQL engine download). |
| `sdn-js/src/ui/runtime/live-delivery.ts:189-215` `loadDecryptedModule` -> `createBrowserModuleHarness` | No `verifySignature` option; the SDK resolves the ambient policy (`space-data-module-sdk/src/bundle/signing.js:738-767`: explicit option, else `SDM_TRUSTED_MODULE_SIGNERS` / `__SDM_TRUSTED_MODULE_SIGNERS__`, else null = load unverified). The bytes reach this point only after the grant-bound client decrypt. | No | Yes (protected module delivery). |
| `sdn-js/src/module-lifecycle.ts:143,204` | `loadModule` is caller-supplied; no key list. | No | Library. |
| `sdn-js/src/edge-discovery.ts:484-492` | SRI hash from the descriptor, not a signature. | No | Yes. |

### Modules repository (`cfd750eb8`, signing landing `a838d84501`)

| Location | Trusted keys | Dev key? |
| --- | --- | --- |
| `scripts/trusted-publishers.json` | `module-owner-2026-10-15` only (`c04e832f…`, role owner, since 2026-11-06). | Named in the note as never trusted. |
| `scripts/check-module-signatures.mjs:9-12,36-39` | Classifies a dev-key signature as a failure. | Named, refused. |
| `scripts/tests/module-signatures-20261106.md:12` | 12 current dists still carry a dev-key trailer (analysis/launch-ascent, analysis/reentry, analysis/vcm-adapter, basilisk/runtime, data-source/gp-archive-records, data-source/spk-source, files/ccsds-messages, files/orbit-products, files/rinex, files/vcm-text, foundation/frames, propagator/hypersonics). They re-sign at their next version. | Dists, not a trust list. |
| `basilisk/runtime/build.mjs:15-20` | Stale comment "which Sandcastle trusts by default" and a dev-keypair fallback path: OrbPro now revokes that key, so a rebuild through this fallback produces a dist no loader accepts. Not a trust list. | Fallback signer. |

### SDK (`5c6a05a49`)

| Location | Trusted keys | Dev key? |
| --- | --- | --- |
| `src/bundle/signing.js:738-767` `resolveModuleSignaturePolicy` | No built-in key; explicit option, env, global, else null (unverified). | No |
| `src/bundle/signing.js:488-549` `verifyModuleArtifact` | An empty `trustedPublicKeys` with `requireSignature` refuses every signer (`untrusted_signer`); the SDK never defaults to its fixture key. | No |
| `bin/space-data-module.js:257-259` | Usage text names the fixture keypair for `sign`/`verify` examples. | Example only. |

## Observations for the SDN-AP session (no action taken here)

1. The host env list still names the orphan `088ac3d8…` on both production units (last measured 2026-08-26); under enforcement it would trust a key nobody controls. Removal is a deploy step (HEPHAESTUS); filed in `topology.json:310`, not actioned.
2. `deployment/signing.json` and `topology.json:305-310` name the host-01 publisher fingerprint `0d80e1fd`; the deploy scripts of 2026-10-15 and later name `018c8bb1…` (SEC-001 re-key). The console and the catalog follow the live key automatically (the anchor is read from the node); the stale records are documentation.
3. The owner key `module-owner-2026-10-15` is trusted by OrbPro's loaders and by the modules repository's check; no SDN host or console path judges it, because SDN's module trust is the node key by the owner ruling of 2026-07-30 (`signing.json` governance.ruling_of_record). A module signed only by the owner key is admitted on a host through the PMM publish (node re-signs the content hash) and through report-only mode, not by the owner key itself.
4. Enforcement (`SDN_MODULE_SIGNATURE_ENFORCE`) has no repository record of being set on host-01 or host-02; whether the hosts are report-only or enforcing can be read only on the hosts.
