# CLI Service Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add cross-platform persistent CLI service controls and an uninstall/remove path for the current SDN install.

**Architecture:** Keep `daemon` as the foreground runtime. Add a focused service-management layer that renders native user-scoped service definitions, runs native service commands, and plans/removes the current self-contained install without touching user data unless explicitly requested.

**Tech Stack:** Go Cobra CLI, launchd LaunchAgents, systemd user units, Windows Scheduled Tasks, existing self-contained bundle layout.

---

### Task 1: Command Registration And Help

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Create: `sdn-server/cmd/spacedatanetwork/service.go`
- Test: `sdn-server/cmd/spacedatanetwork/service_test.go`

- [x] Add failing tests that assert `start`, `stop`, `restart`, `remove`, and `service status/install/uninstall` are registered and appear in root help.
- [x] Implement Cobra commands and attach them to `rootCmd`.
- [x] Run `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestUserFacingCLICommandsAreRegistered|TestRootHelpListsServiceManagementCommands'`.

### Task 2: Native Service Definitions

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/service.go`
- Test: `sdn-server/cmd/spacedatanetwork/service_test.go`

- [x] Add failing tests for macOS LaunchAgent plist, Linux systemd user unit, and Windows Scheduled Task command rendering.
- [x] Implement pure rendering/planning functions that accept executable path, config path, working directory, and OS name.
- [x] Run `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'Test.*Service.*'`.

### Task 3: Service Actions

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/service.go`
- Test: `sdn-server/cmd/spacedatanetwork/service_test.go`

- [x] Add failing tests for action command plans: start installs/enables/starts, stop disables/stops, restart stops then starts, uninstall removes native service definitions.
- [x] Implement command execution through a small injectable runner so tests never call host service managers.
- [x] Run focused Go service tests.

### Task 4: Remove Current Install

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/service.go`
- Test: `sdn-server/cmd/spacedatanetwork/service_test.go`

- [x] Add failing tests for remove planning: active bundle detection, alias cleanup, default data preservation, and `--purge-data` data removal.
- [x] Implement `remove`, `--dry-run`, and `--purge-data`.
- [x] Add Windows delayed cleanup script generation for self-removal.
- [x] Run focused Go remove tests.

### Task 5: README And Verification

**Files:**
- Modify: `README.md`
- Test: existing Go/Node release tests

- [x] Update README quick-start and download sections with `start`, `stop`, `restart`, `service`, and `remove`.
- [x] Run `bash -n scripts/install.sh docs/install.sh deployment/release/download-kubo.sh`.
- [x] Run focused Go CLI tests with `../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork`.
- [x] Run release script tests that cover install/archive behavior.
- [ ] Run stack verification from `/Users/tj/software/orbpro-stack`: `git submodule status` and `git submodule foreach 'git status --short --branch'`.
