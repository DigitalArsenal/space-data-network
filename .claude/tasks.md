# SDN Tasks — Dependency Modernization & Automation

## Overview

Remove vendored/local copies of core dependencies from `packages/` and migrate to published package manager versions. Create automation to keep versions in sync and validate on every commit.

### Core Dependencies

| Dependency | JS (npm) | Go | Current Version | Target |
|---|---|---|---|---|
| flatc-wasm | `flatc-wasm` | WASM via wazero | 26.1.17 (schemas/sds) | latest |
| hd-wallet-wasm | `hd-wallet-wasm` | WASM via wazero | 0.2.1 (sdn-js) | latest |
| spacedatastandards.org | `spacedatastandards.org` | `github.com/DigitalArsenal/spacedatastandards.org/lib/go` | 1.64.0 (schemas/sds) | 1.65.0+ (latest) |
| flatbuffers | `flatbuffers` | `github.com/google/flatbuffers` | 23.3.3 (schemas/sds), 25.12.19 (sdn-server) | latest |

---

## Phase 1: Audit & Inventory

- [ ] **1.1 Map all usages of vendored packages**
  - `packages/emsdk/` — Emscripten SDK (used to compile flatc-wasm)
  - `packages/sdn-xtce/` — XTCE converter package
  - `schemas/sds/` submodule — local spacedatastandards.org checkout
  - Identify which vendored code can be fully replaced by npm/Go module versions

- [ ] **1.2 Check published package availability**
  - Verify `flatc-wasm@latest` on npm — confirm Go WASM compatibility
  - Verify `hd-wallet-wasm@latest` on npm — confirm Go WASM compatibility
  - Verify `spacedatastandards.org@1.65.0` on npm
  - Verify `github.com/DigitalArsenal/spacedatastandards.org/lib/go` on Go proxy
  - Verify `github.com/google/flatbuffers` latest on Go proxy
  - Document anything NOT published that needs to remain local

- [ ] **1.3 Identify version mismatches**
  - `flatbuffers` is 23.3.3 in schemas/sds but 25.12.19 in sdn-server — unify
  - `spacedatastandards.org` local submodule vs published npm/Go versions
  - Document all version pins and why they exist

---

## Phase 2: Migrate to Published Packages (JavaScript/TypeScript)

- [ ] **2.1 Update sdn-js/package.json**
  - Upgrade `hd-wallet-wasm` from 0.2.1 to latest
  - Add `flatc-wasm@latest` if not already a dependency
  - Add `spacedatastandards.org@latest` (>=1.65.0) if needed
  - Run `npm install` and verify no breaking changes

- [ ] **2.2 Update schemas/sds/package.json**
  - Upgrade `flatbuffers` from 23.3.3 to latest
  - Upgrade `flatc-wasm` from 26.1.17 to latest
  - Upgrade package version from 1.64.0 to 1.65.0+
  - Run `npm install` and verify builds

- [ ] **2.3 Update webui/package.json (if applicable)**
  - Add any needed dependencies from published packages
  - Remove any local path references

- [ ] **2.4 Remove packages/emsdk/**
  - Confirm flatc-wasm npm package replaces the need to compile from Emscripten
  - Delete `packages/emsdk/` directory
  - Delete `packages/.emsdk_version`
  - Update any build scripts that reference emsdk

- [ ] **2.5 Evaluate packages/sdn-xtce/**
  - Determine if sdn-xtce should become its own published npm package or stay local
  - If staying local, update its dependencies to use published versions
  - If publishable, publish and reference from npm

---

## Phase 3: Migrate to Published Packages (Go)

- [ ] **3.1 Update sdn-server/go.mod**
  - Upgrade `github.com/google/flatbuffers` to latest (unify with JS version)
  - Replace local `spacedatastandards.org/lib/go` path replacement with published Go module version
  - Remove `replace` directive: `replace github.com/DigitalArsenal/spacedatastandards.org/lib/go => ../schemas/sds/lib/go`
  - Run `go mod tidy` and verify builds

- [ ] **3.2 Update schemas/sds/lib/go/go.mod**
  - Upgrade `github.com/google/flatbuffers/go` from 23.3.3 to latest
  - Ensure this module is published to Go proxy

- [ ] **3.3 Update sdn-wasi/go.mod (if applicable)**
  - Verify wazero version compatibility with updated WASM modules
  - Update dependencies

- [ ] **3.4 Update WASM module loading**
  - `sdn-server/internal/wasm/flatc.go` — verify it loads flatc-wasm correctly from published package
  - `sdn-server/internal/wasm/hdwallet.go` — verify it loads hd-wallet-wasm correctly
  - Update WASM file paths if they change with new versions

- [ ] **3.5 Verify Go test suite passes**
  - `cd sdn-server && go test ./...`
  - `sdn-server/internal/wasm/flatc_test.go`
  - `sdn-server/internal/wasm/hdwallet_test.go`
  - `sdn-server/internal/sds/roundtrip_test.go`
  - `sdn-server/internal/sds/validator_test.go`

---

## Phase 4: Remove Vendored / Local Copies

- [ ] **4.1 Remove packages/ directory contents**
  - Delete `packages/emsdk/` (replaced by flatc-wasm npm package)
  - Delete `packages/.emsdk_version`
  - Evaluate `packages/sdn-xtce/` — keep if not publishable, remove if published

- [ ] **4.2 Update schemas/sds submodule reference**
  - If spacedatastandards.org is now fully consumed from npm/Go modules, evaluate whether the submodule is still needed
  - If submodule is still needed (for .fbs source files), ensure it points to the 1.65.0 tag
  - Remove any local `replace` directives in go.mod files

- [ ] **4.3 Clean up legacy/ references**
  - `legacy/` directory has old flatbuffers usage — verify it's not referenced by anything active
  - Remove or archive if unused

- [ ] **4.4 Update .gitignore and .gitmodules**
  - Remove entries for deleted vendored packages
  - Update submodule references if changed

---

## Phase 5: Version Sync Automation Script

- [ ] **5.1 Create `scripts/update-deps.sh`**
  - Automatically update all dependency versions across JS and Go:
    ```
    npm run update:deps
    ```
  - Steps the script performs:
    1. Fetch latest versions of flatc-wasm, hd-wallet-wasm, spacedatastandards.org, flatbuffers from npm
    2. Fetch latest Go module versions from Go proxy
    3. Update `sdn-js/package.json` with latest versions
    4. Update `schemas/sds/package.json` with latest versions
    5. Update `sdn-server/go.mod` with latest versions
    6. Update `schemas/sds/lib/go/go.mod` with latest versions
    7. Run `npm install` in each JS workspace
    8. Run `go mod tidy` in each Go module
    9. Print summary of version changes

- [ ] **5.2 Add npm script to root package.json**
  ```json
  "update:deps": "bash scripts/update-deps.sh"
  ```

- [ ] **5.3 SDN version tracking**
  - The SDN software version should encode the spacedatastandards.org version it depends on
  - Example: SDN 2.0.0 depends on SDS 1.65.0 — document this mapping
  - Script outputs a version compatibility matrix

---

## Phase 6: Pre-Commit Hook & CI Validation

- [ ] **6.1 Install and configure pre-commit hook**
  - Use husky (or existing pre-commit package from desktop/package.json)
  - Hook runs on every commit:
    1. Check that dependency versions are consistent across all package.json and go.mod files
    2. Run `npm run test` (which runs both `test:go` and `test:js`)
    3. Block commit if tests fail

- [ ] **6.2 Create version consistency checker**
  - `scripts/check-version-consistency.sh` or Node script
  - Verifies:
    - flatbuffers version matches in schemas/sds/package.json, sdn-server/go.mod, and schemas/sds/lib/go/go.mod
    - spacedatastandards.org version matches in npm and Go module
    - hd-wallet-wasm version matches across all consumers
    - flatc-wasm version matches across all consumers
  - Exit code 1 if any mismatch found

- [ ] **6.3 Add npm scripts for validation**
  ```json
  "precommit": "npm run check:versions && npm run test",
  "check:versions": "node scripts/check-version-consistency.js"
  ```

- [ ] **6.4 Update root package.json pre-commit config**
  - Currently only desktop has pre-commit (`"pre-commit": ["lint"]`)
  - Add to root: version check + full test suite

---

## Phase 7: Verify Everything Works

- [ ] **7.1 Run full test suite**
  - `npm run test` (Go + JS)
  - `npm run test:go` — all sdn-server tests
  - `npm run test:js` — all sdn-js tests
  - Verify WASM modules load correctly with new versions

- [ ] **7.2 Build and run desktop app**
  - `npm run desktop` — verify webui builds and Electron launches
  - Verify schema count shows correct number (39)
  - Verify SDN peer detection works

- [ ] **7.3 Build and run webui dev server**
  - `npm run webui` — verify dev server starts with IPFS daemon
  - Navigate all pages, verify no regressions

- [ ] **7.4 Run stress tests (optional)**
  - `npm run stress:quick` — verify high-volume FlatBuffer operations still work
  - Confirm no performance regressions from version updates

---

## Notes

- The SDN software version is dependent on the spacedatastandards.org version — this coupling must be explicit and automated
- `packages/` directory should be eliminated or minimized — only keep local packages that are NOT published to npm/Go
- All WASM modules (flatc-wasm, hd-wallet-wasm) must work in both Go (via wazero) and JavaScript (via browser/Node.js)
- The pre-commit hook must not be slow — version consistency check should be fast, full tests can be parallelized
- If any package is not published to npm or Go proxy, document why and add a TODO to publish it
