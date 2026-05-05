# Upstream IPFS Mirror Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a repeatable update workflow that keeps IPFS WebUI and IPFS Desktop upstream mirrors clean while reapplying SDN overlays and tests.

**Architecture:** `webui/` and `desktop/` remain upstream mirrors. SDN behavior is applied through `sdn-js/ui/src/upstream-webui/overrides/`, generated vendor snapshots, and explicit patch/adapter files. The update script refreshes upstream, reapplies SDN layers, and runs contract checks.

**Tech Stack:** Git subtree, Node scripts, npm, Vite, Vitest, Electron desktop tests.

---

### Task 1: Add Mirror Boundary Tests

**Files:**
- Modify: `sdn-js/src/ui/upstream-webui/branding.test.ts`
- Create: `sdn-js/src/ui/upstream-webui/upstream-mirror-boundary.test.ts`

- [x] **Step 1: Add a failing mirror boundary test**

Create `sdn-js/src/ui/upstream-webui/upstream-mirror-boundary.test.ts` with assertions that `webui/src/navigation/NavBar.js` does not import SDN assets or link `/webui`, and that desktop WebUI loading does not depend on `webui://-` in Kubo CORS instructions.

- [x] **Step 2: Verify the test fails on current drift**

Run:

```bash
npm --prefix sdn-js exec vitest run src/ui/upstream-webui/upstream-mirror-boundary.test.ts
```

Expected: fail if SDN-specific behavior remains in mirror files or custom scheme CORS is required.

- [x] **Step 3: Move mirror drift into overlays or patch files**

Move SDN behavior out of `webui/` and `desktop/` mirror paths into SDN-owned overlay or patch files. Keep upstream mirror files as close to upstream as possible.

- [x] **Step 4: Verify the boundary test passes**

Run:

```bash
npm --prefix sdn-js exec vitest run src/ui/upstream-webui/upstream-mirror-boundary.test.ts
```

Expected: pass.

2026-05-05 verification note: the new boundary test failed against the previous
desktop CORS migration because `desktop/src/daemon/config.js` and launch e2e
tests required `sdn://-` / `webui://-`. Desktop now serves bundled SDN UI and
WebUI assets from a local HTTP origin and configures Kubo CORS for that origin.
`npm --prefix sdn-js exec vitest run src/ui/upstream-webui/upstream-mirror-boundary.test.ts`
passes.

### Task 2: Add One Upstream Update Command

**Files:**
- Modify: `scripts/subtree-update.sh`
- Modify: `scripts/sync-upstream-webui-into-sdn-js.mjs`
- Create: `scripts/update-upstream-ipfs.sh`

- [x] **Step 1: Add the orchestration script**

Create `scripts/update-upstream-ipfs.sh` to fetch selected upstream IPFS WebUI and IPFS Desktop revisions, run subtree refreshes, refresh SDN vendor snapshots, apply SDN patch files, and run focused tests.

- [x] **Step 2: Add check-only mode**

Support `--check` so CI can verify the mirrors, generated vendor files, and overlays are current without modifying files.

- [ ] **Step 3: Verify check-only mode fails on stale generated files**

Run:

```bash
scripts/update-upstream-ipfs.sh --check
```

Expected: non-zero exit when vendor snapshots or overlays are stale.

- [ ] **Step 4: Verify update mode refreshes generated files**

Run:

```bash
scripts/update-upstream-ipfs.sh
```

Expected: upstream mirror refresh, generated vendor snapshot refresh, patch application, and focused tests.

2026-05-05 note: added `scripts/update-upstream-ipfs.sh`; update mode calls
the WebUI and Desktop subtree refreshes, refreshes the SDN vendor snapshot, and
runs focused mirror tests. `--check` runs the vendor snapshot check and focused
boundary/branding tests without mutating files.

### Task 3: Document And Enforce The Update Process

**Files:**
- Modify: `AGENTS.md`
- Modify: `deployment/spaceaware/README.md`
- Modify: `.github/workflows/*` if a suitable CI workflow exists

- [x] **Step 1: Keep AGENTS rules authoritative**

Ensure `AGENTS.md` states that `webui/` and `desktop/` are upstream mirrors and SDN changes must live in overlays, vendor snapshots, patches, or adapters.

- [x] **Step 2: Add deployment preflight instructions**

Document that deploys touching IPFS WebUI/Desktop must run the upstream mirror check and desktop launch verification before production deploy.

- [x] **Step 3: Add CI coverage**

Wire `scripts/update-upstream-ipfs.sh --check` into the relevant JS/UI CI path.

- [ ] **Step 4: Verify full focused checks**

Run:

```bash
node scripts/sync-upstream-webui-into-sdn-js.mjs --check
npm --prefix sdn-js run build:ui
npm --prefix sdn-js exec vitest run src/ui/upstream-webui/branding.test.ts src/ui/upstream-webui/upstream-mirror-boundary.test.ts
```

Expected: all checks pass.

2026-05-05 note: `AGENTS.md` already contains the upstream mirror and custom
CORS-origin rules. `deployment/spaceaware/README.md` now requires
`scripts/update-upstream-ipfs.sh --check` and desktop launch verification before
deploys that touch WebUI/Desktop. `scripts/ci-local.sh js` runs the mirror check,
which is covered by `.github/workflows/ci.yml` through `ci-local.sh quick`.
