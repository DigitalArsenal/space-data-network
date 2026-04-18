# SDN Admin Isomorphic Shell Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the inline `sdn-server` `/admin` template with a shared `sdn-js` isomorphic admin shell, then add the backend switching, linked directory/store surfaces, frontend IDE, wallet/account integration, progressive-backoff hardening, and Docker delivery required by the approved design.

**Architecture:** The implementation keeps one client app in `sdn-js` and two runtime adapters: `Local` for browser-owned Helia/libp2p and `Server` for `sdn-server` admin/data APIs. `sdn-server` becomes a host plus API provider for the admin shell rather than the owner of admin UI behavior, and the same client build must ship both in the server host path and in browser-only package form.

**Tech Stack:** `sdn-js` UI (Vite, TypeScript, Helia/libp2p, `hd-wallet-ui`, Monaco), `sdn-server` (Go `net/http`), existing trust/auth/frontend APIs, Docker/`ghcr.io` container workflow.

---

## File Structure

### Client app and runtime

- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`
  - replace the current single-page live-delivery dashboard markup with the new admin shell layout
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/main.ts`
  - bootstrap the admin shell, runtime adapters, route state, and initial server/local mode handling
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/styles.css`
  - shell layout, icon rail, top bar, responsive workspace styling
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-adapter.ts`
  - shared runtime adapter interfaces for local/server mode
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/local-adapter.ts`
  - Helia-backed adapter implementation
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/server-adapter.ts`
  - fetch-driven adapter implementation for `sdn-server`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-state.ts`
  - active backend context, server target, auth context, and workspace state
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/account-menu.ts`
  - wallet/account modal and sign-out integration
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/frontend-workspace.ts`
  - file tree/editor workspace state and API bridge
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/store-relations.ts`
  - initial linked `Modules`/`Data` relation model

### Server hosting and APIs

- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/adminui/host.go`
  - static asset hosting for the built `sdn-js` admin app with SPA fallback
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/adminui/host_test.go`
  - tests for `/admin` asset serving and SPA fallback
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/cmd/spacedatanetwork/main.go`
  - stop wiring the inline admin template and instead serve the built app at `/admin`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/frontend/manager.go`
  - add richer file-tree and rename/move APIs needed by the new IDE shell
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/frontend/manager_test.go`
  - validate the expanded frontend admin API contract
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/gitapi/handler.go`
  - server-backed repo status/log/diff/commit/push contract for `Server` mode
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/gitapi/handler_test.go`
  - tests for allowed git actions and auth gating
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/security/backoff.go`
  - progressive backoff primitives for control APIs
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/security/backoff_test.go`
  - tests for escalating cooldown behavior
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/auth/handler.go`
  - apply progressive backoff to challenge/verify and expose cooldown metadata

### Packaging and container delivery

- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/deployment/docker/Dockerfile.full`
  - ensure the built `sdn-js` admin assets are included in the server image
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/.github/workflows/docker-publish.yml`
  - ensure the server image build path includes the admin-shell assets and tests
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/README.md`
  - document `/admin`, `/webui`, and container expectations

### Tests

- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/app-shell.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-state.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/server-adapter.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/local-adapter.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/frontend-workspace.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/store-relations.test.ts`

## Task 1: Replace the inline `/admin` host with the shared `sdn-js` admin app

**Files:**
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/adminui/host.go`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/adminui/host_test.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/main.ts`
- Test: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] **Step 1: Write the failing server-host test**

```go
func TestMakeAdminUIHandlerServesIndexAndAssets(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "index.html"), []byte("<!doctype html><html><body>admin-ui</body></html>"), 0o644); err != nil {
		t.Fatalf("write index failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "assets", "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatalf("write asset failed: %v", err)
	}

	handler, err := adminui.NewHost(tempDir)
	if err != nil {
		t.Fatalf("NewHost failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin-ui") {
		t.Fatalf("body = %q, want admin-ui index", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the targeted Go test and verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server && go test ./cmd/spacedatanetwork -run TestMakeAdminUIHandlerServesIndexAndAssets -count=1`

Expected: FAIL because `adminui.NewHost` and the new `/admin` host path do not exist yet.

- [ ] **Step 3: Implement the admin static host**

```go
// /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/adminui/host.go
package adminui

func NewHost(buildDir string) (http.Handler, error) {
	// Validate buildDir/index.html
	// Serve existing assets directly
	// Fall back to index.html for SPA routes under /admin
}
```

- [ ] **Step 4: Wire `/admin` to the new host in `main.go`**

```go
adminUIHandler, err := adminui.NewHost(cfg.Admin.AdminUIPath)
if err != nil {
	return fmt.Errorf("admin_ui_path %q: %w", cfg.Admin.AdminUIPath, err)
}
adminMux.HandleFunc("/admin", redirectToAdminSlash)
adminMux.Handle("/admin/", http.StripPrefix("/admin", adminUIHandler))
```

- [ ] **Step 5: Give the existing `sdn-js` UI shell an admin-oriented starter frame**

```ts
// /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts
root.innerHTML = `
  <div class="sdn-admin-shell">
    <aside class="sdn-admin-rail">…</aside>
    <div class="sdn-admin-main">
      <header class="sdn-admin-topbar">…</header>
      <section id="sdn-admin-workspace"></section>
    </div>
  </div>
`;
```

- [ ] **Step 6: Run the focused server and UI tests**

Run:
- `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server && go test ./cmd/spacedatanetwork -run 'TestMakeAdminUIHandlerServesIndexAndAssets|TestIsPublicAPIPathAllowsProviderDescriptorRoute' -count=1`
- `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts`

Expected: PASS for the new host wiring and the starter admin shell render.

- [ ] **Step 7: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-server/internal/adminui sdn-server/cmd/spacedatanetwork/main.go sdn-js/ui/src/app.ts sdn-js/ui/src/main.ts sdn-js/src/ui/app-shell.test.ts
git commit -m "feat: host shared admin shell from sdn-js"
```

## Task 2: Add shared admin runtime state and backend adapter contracts

**Files:**
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-adapter.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-state.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/admin-state.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/local-adapter.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/local-adapter.test.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/server-adapter.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/server-adapter.test.ts`

- [ ] **Step 1: Write failing adapter-contract tests**

```ts
it('switches from local mode to server mode and replaces effective node context', async () => {
  const state = createAdminState(fakeLocalAdapter, fakeServerFactory)
  await state.connectLocal()
  expect(state.snapshot().mode).toBe('local')

  await state.connectServer({ baseUrl: 'https://node.example' })
  expect(state.snapshot().mode).toBe('server')
  expect(state.snapshot().serverTarget?.baseUrl).toBe('https://node.example')
  expect(state.snapshot().permissions.role).toBe('admin')
})
```

- [ ] **Step 2: Run the targeted Vitest suite and verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/runtime/admin-state.test.ts src/ui/runtime/local-adapter.test.ts src/ui/runtime/server-adapter.test.ts`

Expected: FAIL because the runtime contracts do not exist yet.

- [ ] **Step 3: Implement the adapter interfaces**

```ts
export interface AdminAdapter {
  readonly mode: 'local' | 'server';
  getNodeContext(): Promise<NodeContext>;
  getDirectorySnapshot(): Promise<DirectorySnapshot>;
  getStoreSnapshot(): Promise<StoreSnapshot>;
  getFrontendWorkspace(): Promise<FrontendWorkspaceHandle>;
  signOut?(): Promise<void>;
}
```

- [ ] **Step 4: Implement local and server adapters with fakeable dependencies**

```ts
export function createLocalAdapter(deps: LocalAdapterDeps): AdminAdapter { … }
export function createServerAdapter(deps: ServerAdapterDeps): AdminAdapter { … }
```

- [ ] **Step 5: Connect the admin shell bootstrap to `AdminState`**

```ts
const adminState = createAdminState({
  localAdapter: () => createLocalAdapter(...),
  serverAdapter: (target) => createServerAdapter({ target, fetch: window.fetch.bind(window) }),
})
```

- [ ] **Step 6: Run the runtime tests**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/runtime/admin-state.test.ts src/ui/runtime/local-adapter.test.ts src/ui/runtime/server-adapter.test.ts`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/src/ui/runtime/admin-adapter.ts sdn-js/src/ui/runtime/admin-state.ts sdn-js/src/ui/runtime/local-adapter.ts sdn-js/src/ui/runtime/server-adapter.ts sdn-js/src/ui/runtime/*.test.ts sdn-js/ui/src/main.ts
git commit -m "feat: add admin runtime adapters"
```

## Task 3: Build the shell chrome: icon rail, top bar, backend switch, account menu, IPFS dashboard link

**Files:**
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/styles.css`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/account-menu.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/account-menu.test.ts`

- [ ] **Step 1: Write the failing shell test**

```ts
it('renders a left icon rail, mode switch, connect-server button, account button, and ipfs dashboard link', async () => {
  await renderAppShell(root, { mountWalletUI })
  expect(root.innerHTML).toContain('data-nav="directory"')
  expect(root.innerHTML).toContain('id="sdn-mode-switch"')
  expect(root.innerHTML).toContain('id="sdn-connect-server"')
  expect(root.innerHTML).toContain('id="sdn-account-button"')
  expect(root.innerHTML).toContain('data-nav="ipfs-dashboard"')
})
```

- [ ] **Step 2: Run the failing test**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts src/ui/runtime/account-menu.test.ts`

Expected: FAIL because the new chrome is not rendered yet.

- [ ] **Step 3: Implement the shell markup and wallet/account menu wiring**

```ts
<button id="sdn-mode-switch" type="button">Server</button>
<button id="sdn-connect-server" type="button">Connect Server</button>
<button id="sdn-account-button" type="button" aria-haspopup="dialog">Account</button>
<a data-nav="ipfs-dashboard" target="_blank" rel="noreferrer">IPFS Dashboard</a>
```

- [ ] **Step 4: Style the rail/top bar to match the IPFS admin layout direction**

```css
.sdn-admin-rail { width: 88px; display: flex; flex-direction: column; }
.sdn-admin-topbar { display: grid; grid-template-columns: 1fr auto auto auto; }
```

- [ ] **Step 5: Run the shell tests**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts src/ui/runtime/account-menu.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/app.ts sdn-js/ui/src/styles.css sdn-js/src/ui/runtime/account-menu.ts sdn-js/src/ui/runtime/account-menu.test.ts sdn-js/src/ui/app-shell.test.ts
git commit -m "feat: add admin shell chrome"
```

## Task 4: Deliver the `Directory` and linked `Store` workspaces on top of existing APIs

**Files:**
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/main.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/store-relations.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/store-relations.test.ts`

- [ ] **Step 1: Write failing tests for directory/store workspace rendering**
- [ ] **Step 2: Render `Directory` cards using current trust/auth/node APIs in `Server` mode**
- [ ] **Step 3: Render `Store` with separate `Modules` and `Data` tabs**
- [ ] **Step 4: Implement initial relation stitching between items**
- [ ] **Step 5: Run Vitest for the workspace render and relation logic**
- [ ] **Step 6: Commit**

## Task 5: Implement the `Frontend` workspace with Monaco, file tree, upload, and IDE state

**Files:**
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/styles.css`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/frontend-workspace.ts`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/frontend-workspace.test.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/frontend/manager.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/frontend/manager_test.go`

- [ ] **Step 1: Write failing server and client tests for tree metadata, rename, and IDE state**
- [ ] **Step 2: Expand the server frontend API for file-tree and rename/move support**
- [ ] **Step 3: Add Monaco-backed editor state and a resizable file tree**
- [ ] **Step 4: Add drag/drop and upload-button flows**
- [ ] **Step 5: Run Go tests and Vitest for the frontend workspace**
- [ ] **Step 6: Commit**

## Task 6: Add git operations and deterministic SSH identity integration

**Files:**
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/gitapi/handler.go`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/gitapi/handler_test.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/frontend-workspace.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/app.ts`

- [ ] **Step 1: Write failing git API and frontend git-panel tests**
- [ ] **Step 2: Implement server-side git status/log/diff/commit/push/pull endpoints**
- [ ] **Step 3: Add wallet-derived SSH identity plumbing**
- [ ] **Step 4: Surface git operations in the frontend workspace**
- [ ] **Step 5: Run Go tests and Vitest for git flows**
- [ ] **Step 6: Commit**

## Task 7: Apply progressive backoff to control APIs

**Files:**
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/security/backoff.go`
- Create: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/security/backoff_test.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/auth/handler.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/frontend/manager.go`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/internal/gitapi/handler.go`

- [ ] **Step 1: Write failing progressive-backoff tests**
- [ ] **Step 2: Implement per-IP and per-identity cooldown primitives**
- [ ] **Step 3: Apply them to auth and control mutations**
- [ ] **Step 4: Return cooldown metadata the client can honor**
- [ ] **Step 5: Run Go tests**
- [ ] **Step 6: Commit**

## Task 8: Package and publish the admin shell in the `sdn-server` Docker image

**Files:**
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/deployment/docker/Dockerfile.full`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/.github/workflows/docker-publish.yml`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server/README.md`

- [ ] **Step 1: Write the failing container packaging check**

```bash
docker build -f deployment/docker/Dockerfile.full .
docker run --rm -p 18443:8443 IMAGE_UNDER_TEST
curl -I http://127.0.0.1:18443/admin/
```

- [ ] **Step 2: Update the Docker build so the image contains the built `sdn-js` admin assets**
- [ ] **Step 3: Ensure the publish workflow runs the admin-shell-aware image build**
- [ ] **Step 4: Run local image smoke checks**
- [ ] **Step 5: Commit**

## Final Verification

- [ ] **Step 1: Build `sdn-js`**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm run build`

Expected: PASS with `ui/dist` emitted.

- [ ] **Step 2: Run focused `sdn-js` tests**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts src/ui/runtime/admin-state.test.ts src/ui/runtime/local-adapter.test.ts src/ui/runtime/server-adapter.test.ts src/ui/runtime/frontend-workspace.test.ts src/ui/runtime/store-relations.test.ts`

Expected: PASS.

- [ ] **Step 3: Run focused `sdn-server` tests**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-server && go test ./cmd/spacedatanetwork ./internal/adminui ./internal/frontend ./internal/gitapi ./internal/security -count=1`

Expected: PASS.

- [ ] **Step 4: Run Docker smoke verification**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui && docker build -f deployment/docker/Dockerfile.full .`

Expected: PASS with an image that serves `/admin` and `/webui`.

- [ ] **Step 5: End-to-end admin-shell smoke test**

Run:
- start the server locally or on the test node
- load `/admin`
- switch `Local` / `Server`
- open `IPFS Dashboard`
- open the account menu
- browse `Directory` and `Store`
- open the `Frontend` workspace

Expected: the same shared shell works in both runtime modes.
