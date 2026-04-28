# SDN JS CLI Module Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a public `sdn` CLI that creates an encrypted local wallet, packages encrypted/signed WASM module artifacts, uploads them to an authorized SDN node, and verifies delivery through the public module-delivery protocol.

**Architecture:** `sdn-js` owns the Node CLI, encrypted file wallet, authenticated node session, module packaging, and protocol query command. `sdn-server` owns the encrypted plugin-module upload/list API and catalog persistence; uploaded artifacts become standard module-delivery catalog entries so the existing `/space-data-network/module-delivery/1.0.0` flow serves them. The first live path uses existing wallet challenge auth for upload permissions, then a test protocol query using `SDNNode.requestEncryptedModuleBundle(...)`.

**Tech Stack:** TypeScript ESM, Node 18+, Vitest, `hd-wallet-wasm`, `space-data-module-sdk`, Go `net/http`, existing SDN auth/session middleware, existing encrypted plugin catalog/runtime bootstrap.

---

## Assumptions

- CLI command name is `sdn`.
- CLI wallet home is `~/.spacedatanetwork/sdn-js` unless `SDN_CLI_HOME` is set.
- Upload permission maps to existing SDN auth trust level `admin` for this iteration.
- The live `https://sdn.spaceaware.io` node already has or can receive an admin wallet session capable of adding the generated test wallet.
- Encrypted bundle bytes stay encrypted at rest and in transit; the content key is accepted only over HTTPS from an authenticated admin upload and is stored as the existing catalog `key_path`.
- Hot upload publishes the artifact into the licensing runtime when a licensing module is already loaded; full hot-replacement of the licensing runtime itself is a separate follow-up.

## Files

- Create: `sdn-js/src/cli/index.ts` for command dispatch.
- Create: `sdn-js/src/cli/wallet.ts` for encrypted file wallet storage.
- Create: `sdn-js/src/cli/auth.ts` for challenge/verify login and admin user registration.
- Create: `sdn-js/src/cli/module-package.ts` for encrypted artifact packaging and signing.
- Create: `sdn-js/src/cli/module-upload.ts` for encrypted upload/list/status/query commands.
- Create: `sdn-js/src/cli/cli.test.ts` for CLI wallet/package/auth command smoke tests.
- Modify: `sdn-js/package.json` to expose `bin.sdn`.
- Modify: `sdn-js/scripts/build-package-entry.mjs` to build the CLI as Node ESM.
- Modify: `sdn-js/src/package-build.test.ts` to assert the CLI dist entry exists.
- Modify: `sdn-server/internal/license/plugins.go` to add encrypted catalog insertion.
- Create: `sdn-server/internal/license/module_upload.go` for encrypted upload/list handler.
- Create: `sdn-server/internal/license/module_upload_test.go` for upload/list/permission tests.
- Modify: `sdn-server/cmd/spacedatanetwork/main.go` to register `/api/v1/plugin-modules` and `/api/v1/plugin-modules/upload`.
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go` to assert plugin-module route protection and public module-delivery compatibility.
- Modify: `sdn-server/internal/peers/admin.go` to add a Plugin Modules admin tab backed by `/api/v1/plugin-modules`.
- Modify: `sdn-server/internal/node/licensing_bootstrap.go` to publish one uploaded catalog asset through the existing licensing runtime helper.
- Modify: `sdn-server/internal/node/node.go` to retain the licensing module reference for post-upload publishing.
- Modify: `sdn-js/README.md` and root `README.md` for CLI wallet/package/upload/query examples.

## Task 1: CLI Package Surface And Wallet

- [x] **Step 1: Write failing tests**

  Add `sdn-js/src/cli/cli.test.ts` tests that use `SDN_CLI_HOME` in a temp directory and verify:
  - `createWallet({ password, name })` creates `wallet.json` below the hidden CLI home.
  - `loadWallet({ password })` returns `xpub`, `peerId`, and `signingPublicKeyHex`.
  - loading with a wrong password rejects.
  - package build emits `dist/cli/index.mjs`.

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts src/package-build.test.ts`
  Expected: FAIL because CLI modules and bin entry do not exist yet.

- [x] **Step 2: Implement encrypted wallet and CLI entry**

  Implement `sdn-js/src/cli/wallet.ts` with AES-256-GCM + PBKDF2/SHA-256 using Node `crypto.webcrypto`, `fs/promises`, and `os.homedir()`. Store only public metadata unencrypted; encrypt mnemonic and derived key material. Enforce directory mode `0700` and file mode `0600`.

  Implement `sdn-js/src/cli/index.ts` commands:
  - `sdn wallet init --password-env SDN_WALLET_PASSWORD`
  - `sdn wallet info --password-env SDN_WALLET_PASSWORD`

  Modify `sdn-js/package.json` with `"bin": { "sdn": "./dist/cli/index.mjs" }`.

  Modify `sdn-js/scripts/build-package-entry.mjs` so browser package entries stay browser-targeted and CLI builds as `platform: "node"`.

- [x] **Step 3: Verify wallet tests**

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts src/package-build.test.ts`
  Expected: PASS.

## Task 2: CLI Node Auth And Test Wallet Registration

- [x] **Step 1: Write failing auth tests**

  Extend `sdn-js/src/cli/cli.test.ts` with fetch-mocked tests for:
  - `loginToNode` posts `/api/auth/challenge`, signs the returned challenge with wallet signing key, posts `/api/auth/verify`, and stores the `sdn_wallet_session` cookie per node.
  - `addUploadUser` posts `/api/auth/users` with `trust_level: "admin"` and the wallet `signing_pubkey_hex`.

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts`
  Expected: FAIL because auth helpers do not exist yet.

- [x] **Step 2: Implement auth helpers and commands**

  Implement `sdn-js/src/cli/auth.ts`:
  - `loginToNode({ nodeUrl, wallet, fetchImpl })`
  - `addUploadUser({ nodeUrl, sessionCookie, walletInfo, trustLevel })`
  - session cookie persistence under `~/.spacedatanetwork/sdn-js/sessions.json`.

  Add CLI commands:
  - `sdn auth login --node https://sdn.spaceaware.io`
  - `sdn auth add-current-wallet --node https://sdn.spaceaware.io --trust admin`

- [x] **Step 3: Verify auth tests**

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts`
  Expected: PASS.

## Task 3: Server Encrypted Plugin-Module Upload API

- [x] **Step 1: Write failing Go tests**

  Add `sdn-server/internal/license/module_upload_test.go` for:
  - unauthenticated upload returns 401/403 through handler xpub lookup.
  - authenticated upload accepts `bundle`, `content_key_hex`, `metadata`, and `signature_hex`.
  - registry stores `encrypted_path` and `key_path`, not `plain_path`.
  - `GET /api/v1/plugin-modules` returns public descriptors without key material.

  Run: `go test ./sdn-server/internal/license`
  Expected: FAIL because encrypted upload API is not implemented.

- [x] **Step 2: Implement encrypted catalog insertion and handler**

  Add `PluginRegistry.AddEncryptedPlugin(...)` in `plugins.go`. It writes:
  - `<plugin-id>/bundle.wasm.enc`
  - `<plugin-id>/bundle.key`
  - `catalog.json` entry with normalized `allowed_domains`, `required_scope`, `content_type`, `cache_control`, `max_grant_timeout_ms`, signature audit fields.

  Add `module_upload.go` handler:
  - `GET /api/v1/plugin-modules`
  - `POST /api/v1/plugin-modules/upload`
  - verify Ed25519 signature over `sha256(encryptedBundleBytes)`
  - require the session xpub to resolve to a bound signing key.
  - return a public manifest descriptor with status, allowed domains, grant timeout, hash, and audit fields but no key paths.

- [x] **Step 3: Register protected routes**

  Update `main.go`:
  - register `/api/v1/plugin-modules`
  - register `/api/v1/plugin-modules/upload`
  - classify upload as admin-only.
  - keep `/api/module-delivery/provider` and `/api/module-delivery/listings` public.

  Update `/admin`:
  - add a dedicated Plugin Modules tab.
  - list installed/running module status and public manifest fields from `/api/v1/plugin-modules`.

- [x] **Step 4: Verify Go API tests**

  Run: `go test ./sdn-server/internal/license ./sdn-server/cmd/spacedatanetwork`
  Expected: PASS.

## Task 4: CLI Package, Upload, List, And Protocol Query

- [x] **Step 1: Write failing CLI packaging tests**

  Extend `sdn-js/src/cli/cli.test.ts` for:
  - `packageModule` encrypts bytes using a generated 32-byte content key.
  - `packageModule` signs `sha256(encryptedBundleBytes)`.
  - `uploadModule` posts multipart fields accepted by the server handler.
  - `listModules` reads `/api/v1/plugin-modules`.

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts`
  Expected: FAIL because package/upload helpers do not exist yet.

- [x] **Step 2: Implement packaging and upload commands**

  Implement `module-package.ts` and `module-upload.ts`.

  Add CLI commands:
  - `sdn module package --wasm ./module.wasm --module-id com.example.test --version 0.0.1 --allow-domain spaceaware.io --out ./dist`
  - `sdn module upload --node https://sdn.spaceaware.io --package ./dist/com.example.test-0.0.1.sdn-module.json`
  - `sdn module publish` as package + upload.
  - `sdn module list --node https://sdn.spaceaware.io`.

- [x] **Step 3: Implement protocol query command**

  Add `sdn module query --node https://sdn.spaceaware.io --module-id com.example.test --requester-domain spaceaware.io`.

  The command:
  - loads wallet identity,
  - creates `SDNNode`,
  - calls `requestEncryptedModuleBundle` through `MODULE_DELIVERY_PROTOCOL_ID`,
  - unwraps/decrypts with public UI helpers,
  - reports encrypted bytes, decrypted bytes, module id, version, and CID.

- [x] **Step 4: Verify CLI tests**

  Run: `npm --prefix sdn-js test -- src/cli/cli.test.ts src/module-delivery.test.ts src/module-delivery-sdk-compat.test.ts`
  Expected: PASS.

## Task 5: Docs And Live Test Protocol

- [x] **Step 1: Document commands**

  Update `sdn-js/README.md` and root `README.md` with:
  - global install
  - wallet init/info/login
  - admin adds test wallet as upload capable
  - module package/upload/list/query
  - OrbPro/SpaceAware target provider URL `https://sdn.spaceaware.io/api/module-delivery/provider`.

- [x] **Step 2: Build a tiny test WASM payload**

  Use the existing module SDK fixture or smallest repo fixture that can be encrypted and delivered as `com.spaceaware.test-protocol`.

- [x] **Step 3: Live `sdn.spaceaware.io` smoke**

  Completed on 2026-04-27. `~/.ssh/config` now aliases
  `sdn.spaceaware.io` to the existing `space-data-network-01` host at
  `159.203.150.8` as `root`. The durable test wallet at
  `/Users/tj/.spacedatanetwork/sdn-js-live-upload-test-20260427` was added to
  the live node as upload-capable, and `sdn auth login --node
  https://sdn.spaceaware.io` returns an admin session for
  `SDN Add Two Upload Test`.

  The live daemon was updated with the plugin-module upload/list API after a
  startup fix for sanitized SDS SQLite table names. Uploading
  `com.spaceaware.test.add-two@0.0.1` succeeded with encrypted bundle SHA-256
  `9c7a70d909dd54fdd1a5dc1b5c2a54a2e252c43d6477d2312cd08945d8c1426a`.
  The module appears in `/api/v1/plugin-modules` and in the public
  `/api/module-delivery/listings` PLG feed.

  Public `sdn-js` imports completed the round trip:
  challenge/grant over `/space-data-network/module-delivery/1.0.0`, IPFS CID
  fetch through `https://sdn.spaceaware.io/api/v0/cat`, grant-key unwrap,
  decrypt, SDK browser harness load, and `add_two` invoke. The delivered CID
  was `QmX4CmBGMWfGN4574rvqUPw7fUEYcxCkhc4Vb3Qcw8eG5y`; encrypted size was
  79,146 bytes, decrypted WASM size was 79,118 bytes, and invoking `add_two`
  with `[40, 2]` returned `42` with status code `0`.

## Task 6: Final Verification

- [x] **Step 1: Run focused tests**

  ```bash
  npm --prefix sdn-js test -- src/cli/cli.test.ts src/module-delivery.test.ts src/module-delivery-sdk-compat.test.ts src/storefront/storefront.test.ts
  ../scripts/go-with-wasmedge.sh test ./internal/license ./internal/peers ./internal/node ./cmd/spacedatanetwork -count=1
  npm run check:versions
  git diff --check
  ```

- [x] **Step 2: Update this checklist**

  Mark completed steps with `[x]` and leave any blocked live deployment step unchecked with the exact blocker.

- [x] **Step 3: Commit**

  Commit once focused verification passes.

## Task 7: Transport And Go CLI Parity Tightening

- [x] **Step 1: Add non-Helia content fetch fallbacks**

  `SDNNode` now supports encrypted CID fetch through a configured node IPFS API
  and gateway before falling back to direct Helia/libp2p. The CLI query path
  configures `https://sdn.spaceaware.io/api/v0` and
  `https://sdn.spaceaware.io/ipfs` from the supplied node origin.

- [x] **Step 2: Make the Go binary CLI isomorphic**

  The Go `spacedatanetwork` binary exposes the same wallet, auth, and module
  command groups as the npm `sdn` CLI. Go-only wallet runtime selection is
  exposed as `--wallet-wasm`; deprecated `--wasm` aliases for wallet runtime
  are hidden so `--wasm` remains the plugin payload flag on module packaging.

- [x] **Step 3: Verify live delivery after tightening**

  A rebuilt `sdn` CLI queried `com.spaceaware.test.add-two@0.0.1` from
  `https://sdn.spaceaware.io`, received a grant over
  `/space-data-network/module-delivery/1.0.0`, fetched CID
  `QmX4CmBGMWfGN4574rvqUPw7fUEYcxCkhc4Vb3Qcw8eG5y` through the node IPFS path,
  and decrypted 79,146 encrypted bytes to 79,118 WASM bytes.

- [x] **Step 4: Publish npm package**

  Published `@spacedatanetwork/sdn-js@2.0.8` with npm `latest` through the
  GitHub trusted publishing workflow.
