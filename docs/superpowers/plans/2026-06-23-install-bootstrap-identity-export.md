# Install Bootstrap Identity Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the installed SDN CLI initialize node identity automatically and export its public EPM/contact card in text, JSON, CSV, and terminal QR formats.

**Architecture:** Move identity bootstrap into `init`, share HD wallet WASM resolution between daemon/init/show-identity, have installer invoke init after linking binaries, and add an `identity export` command backed by the local daemon EPM HTTP endpoints. Keep mnemonic output limited to the existing explicit sensitive flag.

**Tech Stack:** Go Cobra CLI, SDN config/node/epm packages, Bash installer, Node test runner for installer release checks, Go tests for CLI helpers.

---

### Task 1: Installer Bootstrap

**Files:**
- Modify: `scripts/install.sh`
- Modify: `deployment/release/install-script.test.mjs`

- [ ] Add a failing release test that requires the installer to run `spacedatanetwork init` unless `SDN_SKIP_INIT=1`.
- [ ] Run `node --test deployment/release/install-script.test.mjs` and confirm the test fails.
- [ ] Add installer bootstrap after command link verification.
- [ ] Re-run `node --test deployment/release/install-script.test.mjs` and confirm it passes.

### Task 2: Init Creates Identity

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] Add a failing Go test for an init helper that creates the encrypted mnemonic file in the keys directory.
- [ ] Add a failing Go test that repeated init preserves the existing mnemonic file.
- [ ] Implement a helper used by `runInit` to load config, create the HD wallet module using shared WASM path resolution, generate the mnemonic when missing, and leave it untouched when present.
- [ ] Run targeted Go tests and confirm they pass.

### Task 3: Bundle WASM Resolution

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/internal/node/node.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] Add a failing test that identity commands resolve `runtime/modules/hd-wallet-wasi.wasm` from the release bundle.
- [ ] Extract shared HD wallet WASM path candidates and use them from daemon/init/show-identity/derive-xpub.
- [ ] Run targeted Go tests and confirm they pass.

### Task 4: Identity Export CLI

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] Add failing tests for `identity export --format text|json|csv|qrcode`.
- [ ] Implement local daemon fetches for `/api/node/epm/vcard` and `/api/node/epm/json`.
- [ ] Format text as vCard, JSON as indented JSON, CSV as one header row plus one data row, and QR as terminal QR text.
- [ ] Run targeted Go tests and confirm they pass.

### Task 5: Release Verification

**Files:**
- Run only unless tests expose a required edit.

- [ ] Run `bash -n scripts/install.sh docs/install.sh deployment/release/download-kubo.sh`.
- [ ] Run `node --test deployment/release/install-script.test.mjs deployment/release/beta-release-workflow.test.mjs`.
- [ ] Run targeted Go tests for `sdn-server/cmd/spacedatanetwork` and `sdn-server/internal/node`.
- [ ] Run `git diff --check`.
- [ ] Run stack-required `git submodule status` and `git submodule foreach 'git status --short --branch'` from the superproject.
