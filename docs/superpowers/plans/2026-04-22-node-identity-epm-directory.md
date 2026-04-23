# Node Identity And EPM Directory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify the node and managed IPFS identity around the node mnemonic, add a separate node-management surface, persist node/user EPMs in FlatSQL for directory queries, and expose one shared directory query model to both daemon-backed and Helia-backed `sdn-js`.

**Architecture:** Keep node identity and user access separate. Extract the current HD-wallet-derived node identity into a canonical server-side identity bundle, project that identity into the managed IPFS repo, persist discovered and authored EPMs into a dedicated FlatSQL-backed directory index, and expose root-only SDN UI routes and runtime adapters that query the same node/user directory model from server and Helia runtimes.

**Tech Stack:** Go (`sdn-server`, SQLite/FlatSQL, libp2p, Kubo config integration), JavaScript/TypeScript (`sdn-js`, Vite, upstream IPFS WebUI overlays), canonical `spacedatastandards.org` EPM bindings, `hd-wallet-wasm`.

---

### Task 1: Make The SDN Root Status Page Use SDN Node Identity

**Files:**
- Create: `sdn-js/ui/src/upstream-webui/overrides/status/NodeInfo.js`
- Modify: `sdn-js/ui/vite.config.mts`
- Modify: `sdn-js/src/ui/vite-config.test.ts`
- Modify: `sdn-js/src/ui/upstream-webui/branding.test.ts`
- Test: `sdn-js/src/ui/vite-config.test.ts`
- Test: `sdn-js/src/ui/upstream-webui/branding.test.ts`

- [ ] **Step 1: Write the failing alias/branding tests**

```ts
// sdn-js/src/ui/vite-config.test.ts
const nodeInfoOverride = await brandingPlugin?.resolveId?.(
  './NodeInfo.js',
  '/Users/tj/software/space-data-network/webui/src/status/StatusPage.js',
)

expect(String(nodeInfoOverride)).toContain(
  '/sdn-js/ui/src/upstream-webui/overrides/status/NodeInfo.js',
)
```

```ts
// sdn-js/src/ui/upstream-webui/branding.test.ts
it('uses a root-only status node info override wired to the SDN node info endpoint', async () => {
  const source = await fs.readFile(
    path.join(uiSrcPath, 'overrides/status/NodeInfo.js'),
    'utf8',
  )

  expect(source).toContain('/api/node/info')
  expect(source).toContain("peer_id")
  expect(source).toContain('spacedatanetwork/')
  expect(source).not.toContain('useIdentity(')
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/vite-config.test.ts src/ui/upstream-webui/branding.test.ts
```

Expected: failure because `NodeInfo.js` is not yet in the root override map and the override file does not exist.

- [ ] **Step 3: Add the root-only Vite alias for status `NodeInfo.js`**

```ts
// sdn-js/ui/vite.config.mts
[
  path.resolve(upstreamWebUiRoot, 'src', 'status', 'NodeInfo.js'),
  path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'status', 'NodeInfo.js'),
],
```

- [ ] **Step 4: Implement the minimal SDN root `NodeInfo` override**

```js
// sdn-js/ui/src/upstream-webui/overrides/status/NodeInfo.js
import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Definition, DefinitionList } from '../../../../../../webui/src/components/definition/Definition.js'

export default function NodeInfo() {
  const { t } = useTranslation('app')
  const [nodeInfo, setNodeInfo] = useState(null)

  useEffect(() => {
    let cancelled = false
    fetch('/api/node/info', { credentials: 'include' })
      .then((resp) => resp.ok ? resp.json() : null)
      .then((json) => {
        if (!cancelled) setNodeInfo(json)
      })
      .catch(() => {
        if (!cancelled) setNodeInfo(null)
      })
    return () => { cancelled = true }
  }, [])

  const peerId = nodeInfo?.peer_id ?? t('loading')
  const agentVersion = nodeInfo?.version ?? t('loading')

  return (
    <DefinitionList>
      <Definition term={t('terms.peerId')} desc={peerId} />
      <Definition term={t('terms.agent')} desc={agentVersion} />
      <Definition term={t('terms.ui')} desc='Local' />
    </DefinitionList>
  )
}
```

- [ ] **Step 5: Re-run the focused tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/vite-config.test.ts src/ui/upstream-webui/branding.test.ts
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/ui/vite.config.mts \
  sdn-js/ui/src/upstream-webui/overrides/status/NodeInfo.js \
  sdn-js/src/ui/vite-config.test.ts \
  sdn-js/src/ui/upstream-webui/branding.test.ts
git commit -m "Fix SDN root status identity source"
```

### Task 2: Extract A Canonical Node Identity Bundle And Project It Into Managed IPFS

**Files:**
- Create: `sdn-server/internal/node/identity_bundle.go`
- Create: `sdn-server/internal/node/identity_bundle_test.go`
- Create: `sdn-server/internal/node/ipfs_repo_identity.go`
- Create: `sdn-server/internal/node/ipfs_repo_identity_test.go`
- Modify: `sdn-server/internal/node/node.go`
- Modify: `scripts/admin-dev.sh`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`
- Test: `sdn-server/internal/node/identity_bundle_test.go`
- Test: `sdn-server/internal/node/ipfs_repo_identity_test.go`

- [ ] **Step 1: Write the failing node identity bundle test**

```go
func TestLoadOrCreateIdentityBundle_ReusesEncryptedMnemonic(t *testing.T) {
	basePath := t.TempDir()
	n := newTestNodeWithHDWallet(t, basePath)

	first, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	second, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("second bundle: %v", err)
	}

	if first.PeerID != second.PeerID {
		t.Fatalf("peer id mismatch: %s != %s", first.PeerID, second.PeerID)
	}
	if first.BitcoinAddress == "" {
		t.Fatal("bitcoin address missing")
	}
}
```

- [ ] **Step 2: Write the failing managed IPFS repo identity sync test**

```go
func TestEnsureManagedIPFSRepoIdentity_WritesDerivedSecp256k1Identity(t *testing.T) {
	basePath := t.TempDir()
	bundle := mustTestIdentityBundle(t)

	repoPath := filepath.Join(basePath, "kubo")
	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
		t.Fatalf("sync repo identity: %v", err)
	}

	cfg := mustReadKuboConfig(t, repoPath)
	if got := cfg.Identity.PeerID; got != bundle.PeerID.String() {
		t.Fatalf("peer id = %s, want %s", got, bundle.PeerID)
	}
}
```

- [ ] **Step 3: Run the focused server tests to verify failure**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/node -run 'TestLoadOrCreateIdentityBundle_ReusesEncryptedMnemonic|TestEnsureManagedIPFSRepoIdentity_WritesDerivedSecp256k1Identity' -count=1
```

Expected: FAIL because the helper files and functions do not exist yet.

- [ ] **Step 4: Extract the mnemonic-backed node identity bundle**

```go
// sdn-server/internal/node/identity_bundle.go
type IdentityBundle struct {
	Mnemonic         string
	Identity         *wasm.DerivedIdentity
	PeerID           peer.ID
	BitcoinAddress   string
	IdentityKeyPath  string
	SigningKeyPath   string
	EncryptionKeyPath string
}

func (n *Node) loadOrCreateIdentityBundle() (*IdentityBundle, error) {
	mnemonic, err := n.loadOrCreateEncryptedMnemonic()
	if err != nil {
		return nil, err
	}
	identity, err := n.hdwallet.IdentityFromMnemonic(n.ctx, mnemonic, "", 0)
	if err != nil {
		return nil, err
	}
	return &IdentityBundle{
		Mnemonic: mnemonic,
		Identity: identity,
		PeerID: identity.PeerID,
		BitcoinAddress: identity.Addresses.Bitcoin,
		IdentityKeyPath: identity.IdentityKeyPath,
		SigningKeyPath: identity.SigningKeyPath,
		EncryptionKeyPath: identity.EncryptionKeyPath,
	}, nil
}
```

- [ ] **Step 5: Add the managed Kubo repo identity sync helper**

```go
// sdn-server/internal/node/ipfs_repo_identity.go
func EnsureManagedIPFSRepoIdentity(repoPath string, bundle *IdentityBundle) error {
	cfg, err := loadOrInitKuboConfig(repoPath)
	if err != nil {
		return err
	}

	rawKey, err := crypto.MarshalPrivateKey(bundle.Identity.IdentityPrivKey)
	if err != nil {
		return err
	}

	cfg.Identity.PeerID = bundle.PeerID.String()
	cfg.Identity.PrivKey = base64.StdEncoding.EncodeToString(rawKey)
	return writeKuboConfig(repoPath, cfg)
}
```

- [ ] **Step 6: Wire `node.go` to use the extracted bundle instead of open-coded mnemonic logic**

```go
bundle, err := n.loadOrCreateIdentityBundle()
if err != nil {
	return n.generateRandomKey(keyDir, keyPath)
}
n.identity = bundle.Identity
return bundle.Identity.IdentityPrivKey, nil
```

- [ ] **Step 7: Add a dev-mode guard in `scripts/admin-dev.sh`**

```bash
# scripts/admin-dev.sh
if command -v ipfs >/dev/null 2>&1; then
  echo "Managed IPFS repo path: ${admin_dev_ipfs_repo}"
  echo "Expected node peer identity is derived from ${repo_root}/data/admin-dev/keys/mnemonic"
fi
```

- [ ] **Step 8: Re-run the focused tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/node -run 'TestLoadOrCreateIdentityBundle_ReusesEncryptedMnemonic|TestEnsureManagedIPFSRepoIdentity_WritesDerivedSecp256k1Identity' -count=1
```

Expected: PASS

- [ ] **Step 9: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/node/identity_bundle.go \
  sdn-server/internal/node/identity_bundle_test.go \
  sdn-server/internal/node/ipfs_repo_identity.go \
  sdn-server/internal/node/ipfs_repo_identity_test.go \
  sdn-server/internal/node/node.go \
  scripts/admin-dev.sh \
  sdn-server/cmd/spacedatanetwork/main_test.go
git commit -m "Unify managed node and IPFS identity roots"
```

### Task 3: Add A FlatSQL-Backed Node/User EPM Directory Index

**Files:**
- Create: `sdn-server/internal/directory/types.go`
- Create: `sdn-server/internal/directory/service.go`
- Create: `sdn-server/internal/directory/service_test.go`
- Modify: `sdn-server/internal/storage/flatsql.go`
- Create: `sdn-server/internal/storage/flatsql_directory_test.go`
- Modify: `sdn-server/internal/epm/service.go`
- Test: `sdn-server/internal/directory/service_test.go`
- Test: `sdn-server/internal/storage/flatsql_directory_test.go`

- [ ] **Step 1: Write the failing FlatSQL directory storage test**

```go
func TestFlatSQLStore_UpsertAndQueryDirectoryRecord(t *testing.T) {
	store := mustNewFlatSQLStore(t)
	record := storage.DirectoryRecord{
		Kind: "node",
		PeerID: "16Uiu2HAmExample",
		DN: "SDN Node Example",
		BitcoinAddress: "bc1qexample",
		EPMCID: "bafyexample",
	}

	if err := store.UpsertDirectoryRecord(record); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	results, err := store.QueryDirectory(storage.DirectoryQuery{Kind: "node", Search: "bc1qexample"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
}
```

- [ ] **Step 2: Write the failing directory service normalization test**

```go
func TestDirectoryService_IndexesNodeEPMJSON(t *testing.T) {
	svc := NewService(mustNewFlatSQLStore(t))
	info := map[string]any{
		"peer_id": "16Uiu2HAmExample",
		"dn": "SDN Node Example",
		"bitcoin_address": "bc1qexample",
	}

	if err := svc.UpsertNodeEPMJSON(info, "bafyexample", "local"); err != nil {
		t.Fatalf("upsert node epm: %v", err)
	}

	nodes, err := svc.SearchNodes("bc1qexample")
	if err != nil {
		t.Fatalf("search nodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
}
```

- [ ] **Step 3: Run the failing tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/storage ./internal/directory -run 'TestFlatSQLStore_UpsertAndQueryDirectoryRecord|TestDirectoryService_IndexesNodeEPMJSON' -count=1
```

Expected: FAIL because the directory tables and service do not exist yet.

- [ ] **Step 4: Add directory tables and storage methods**

```go
// sdn-server/internal/storage/flatsql.go
CREATE TABLE IF NOT EXISTS sdn_directory (
  kind TEXT NOT NULL,
  peer_id TEXT NOT NULL,
  dn TEXT,
  legal_name TEXT,
  bitcoin_address TEXT,
  epm_cid TEXT,
  source TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  epm_json TEXT NOT NULL,
  PRIMARY KEY (kind, peer_id)
);
CREATE INDEX IF NOT EXISTS idx_sdn_directory_search ON sdn_directory (kind, dn, legal_name, bitcoin_address);
```

```go
type DirectoryRecord struct {
	Kind           string
	PeerID         string
	DN             string
	LegalName      string
	BitcoinAddress string
	EPMCID         string
	Source         string
	EPMJSON        string
	UpdatedAt      int64
}
```

- [ ] **Step 5: Add the directory service normalization layer**

```go
// sdn-server/internal/directory/service.go
func (s *Service) UpsertNodeEPMJSON(epmJSON map[string]any, epmCID, source string) error {
	record := DirectoryRecord{
		Kind: "node",
		PeerID: stringValue(epmJSON["peer_id"]),
		DN: stringValue(epmJSON["dn"]),
		LegalName: stringValue(epmJSON["legal_name"]),
		BitcoinAddress: stringValue(epmJSON["bitcoin_address"]),
		EPMCID: epmCID,
		Source: source,
		EPMJSON: mustJSONString(epmJSON),
		UpdatedAt: time.Now().Unix(),
	}
	return s.store.UpsertDirectoryRecord(record)
}
```

- [ ] **Step 6: Teach the EPM service to emit directory-friendly JSON**

```go
// sdn-server/internal/epm/service.go
func (s *Service) DirectoryRecordJSON() map[string]any {
	info := s.GetNodeEPMJSON()
	info["directory_kind"] = "node"
	return info
}
```

- [ ] **Step 7: Re-run the focused tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/storage ./internal/directory -run 'TestFlatSQLStore_UpsertAndQueryDirectoryRecord|TestDirectoryService_IndexesNodeEPMJSON' -count=1
```

Expected: PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/storage/flatsql.go \
  sdn-server/internal/storage/flatsql_directory_test.go \
  sdn-server/internal/directory/types.go \
  sdn-server/internal/directory/service.go \
  sdn-server/internal/directory/service_test.go \
  sdn-server/internal/epm/service.go
git commit -m "Add FlatSQL-backed EPM directory index"
```

### Task 4: Ingest Discovered EPMs And Expose Directory APIs

**Files:**
- Create: `sdn-server/internal/directory/http.go`
- Modify: `sdn-server/internal/node/node.go`
- Modify: `sdn-server/internal/node/advertisement_discovery.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`
- Test: `sdn-server/internal/directory/service_test.go`
- Test: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] **Step 1: Write the failing HTTP API test**

```go
func TestHandleDirectoryNodes_ReturnsIndexedNodeRecords(t *testing.T) {
	n := mustTestNodeWithDirectory(t)
	req := httptest.NewRequest(http.MethodGet, "/api/directory/nodes?q=bc1qexample", nil)
	rr := httptest.NewRecorder()

	handleDirectoryNodes(n).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "16Uiu2HAmExample") {
		t.Fatalf("body missing node record: %s", rr.Body.String())
	}
}
```

- [ ] **Step 2: Write the failing discovery-ingest test**

```go
func TestRecordDiscoveredNodeEPM_UpsertsDirectoryRecord(t *testing.T) {
	n := mustTestNodeWithDirectory(t)
	epmJSON := map[string]any{
		"peer_id": "16Uiu2HAmExample",
		"dn": "Discovered Node",
		"bitcoin_address": "bc1qexample",
	}

	if err := n.recordDiscoveredNodeEPM(epmJSON, "bafyexample"); err != nil {
		t.Fatalf("record discovered epm: %v", err)
	}

	results, err := n.Directory().SearchNodes("bc1qexample")
	if err != nil {
		t.Fatalf("search nodes: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
}
```

- [ ] **Step 3: Run the failing tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/directory ./internal/node ./cmd/spacedatanetwork -run 'TestHandleDirectoryNodes_ReturnsIndexedNodeRecords|TestRecordDiscoveredNodeEPM_UpsertsDirectoryRecord' -count=1
```

Expected: FAIL because the APIs and ingestion hooks do not exist.

- [ ] **Step 4: Add directory HTTP handlers**

```go
// sdn-server/internal/directory/http.go
func HandleNodes(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		nodes, err := svc.SearchNodes(q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, nodes)
	}
}
```

```go
func HandleUsers(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		users, err := svc.SearchUsers(q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, users)
	}
}
```

- [ ] **Step 5: Add node ingestion helpers and wire them into discovery**

```go
// sdn-server/internal/node/node.go
func (n *Node) recordDiscoveredNodeEPM(epmJSON map[string]any, epmCID string) error {
	if n.directory == nil {
		return nil
	}
	return n.directory.UpsertNodeEPMJSON(epmJSON, epmCID, "discovered")
}
```

```go
// after successful SDN peer discovery / EPM fetch
_ = n.recordDiscoveredNodeEPM(remoteEPMJSON, remoteCID)
```

- [ ] **Step 6: Mount the directory APIs in `main.go`**

```go
adminMux.HandleFunc("/api/directory/nodes", directory.HandleNodes(n.Directory()))
adminMux.HandleFunc("/api/directory/users", directory.HandleUsers(n.Directory()))
```

- [ ] **Step 7: Re-run the focused tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/directory ./internal/node ./cmd/spacedatanetwork -run 'TestHandleDirectoryNodes_ReturnsIndexedNodeRecords|TestRecordDiscoveredNodeEPM_UpsertsDirectoryRecord' -count=1
```

Expected: PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/directory/http.go \
  sdn-server/internal/node/node.go \
  sdn-server/internal/node/advertisement_discovery.go \
  sdn-server/cmd/spacedatanetwork/main.go \
  sdn-server/cmd/spacedatanetwork/main_test.go
git commit -m "Expose and ingest SDN EPM directory data"
```

### Task 5: Add Root-Only SDN Directory And Node Identity Management Routes

**Files:**
- Create: `sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/directory/DirectoryPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/identity/IdentityPage.js`
- Modify: `sdn-js/ui/vite.config.mts`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/App.js`
- Modify: `sdn-js/src/ui/upstream-webui/branding.test.ts`
- Modify: `sdn-js/src/ui/vite-config.test.ts`
- Test: `sdn-js/src/ui/upstream-webui/branding.test.ts`
- Test: `sdn-js/src/ui/vite-config.test.ts`

- [ ] **Step 1: Write the failing route and nav tests**

```ts
expect(String(routesOverride)).toContain(
  '/sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js',
)
expect(source).toContain("to='/directory'")
expect(source).toContain("to='/identity'")
expect(source).toContain("href='/webui'")
```

- [ ] **Step 2: Run the tests to verify failure**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/vite-config.test.ts src/ui/upstream-webui/branding.test.ts
```

Expected: FAIL because the routes override and new nav links do not exist.

- [ ] **Step 3: Add root-only route override wiring**

```ts
// sdn-js/ui/vite.config.mts
[
  path.resolve(upstreamWebUiRoot, 'src', 'bundles', 'routes.js'),
  path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'bundles', 'routes.js'),
],
```

- [ ] **Step 4: Implement the SDN-only route bundle**

```js
// sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js
import { createRouteBundle } from 'redux-bundler'
import StatusPage from '../../../../../../webui/src/status/LoadableStatusPage.js'
import DirectoryPage from '../directory/DirectoryPage.js'
import IdentityPage from '../identity/IdentityPage.js'
import PeersPage from '../../../../../../webui/src/peers/LoadablePeersPage.js'
import FilesPage from '../../../../../../webui/src/files/LoadableFilesPage.js'

export default createRouteBundle({
  '/directory*': DirectoryPage,
  '/identity*': IdentityPage,
  '/files*': FilesPage,
  '/peers': PeersPage,
  '/status*': StatusPage,
  '/': StatusPage,
  '': StatusPage,
}, { routeInfoSelector: 'selectHash' })
```

- [ ] **Step 5: Implement the minimal Directory and Identity pages**

```js
// DirectoryPage.js
export default function DirectoryPage() {
  return <div data-id='DirectoryPage'>Directory</div>
}
```

```js
// IdentityPage.js
export default function IdentityPage() {
  return <div data-id='IdentityPage'>Node Identity</div>
}
```

- [ ] **Step 6: Add root-only nav entries**

```js
<NavLink to='/directory' icon={StrokeCube}>Directory</NavLink>
<NavLink to='/identity' icon={StrokeSettings}>Identity</NavLink>
<a href='/webui' target='_blank' rel='noopener noreferrer' ...>IPFS</a>
```

- [ ] **Step 7: Re-run the focused tests**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/vite-config.test.ts src/ui/upstream-webui/branding.test.ts
```

Expected: PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/ui/vite.config.mts \
  sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js \
  sdn-js/ui/src/upstream-webui/overrides/directory/DirectoryPage.js \
  sdn-js/ui/src/upstream-webui/overrides/identity/IdentityPage.js \
  sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js \
  sdn-js/ui/src/upstream-webui/overrides/App.js \
  sdn-js/src/ui/upstream-webui/branding.test.ts \
  sdn-js/src/ui/vite-config.test.ts
git commit -m "Add SDN root directory and identity routes"
```

### Task 6: Add Shared Server/Helia Directory Adapters And Populate The New Views

**Files:**
- Create: `sdn-js/src/ui/runtime/directory.ts`
- Create: `sdn-js/src/ui/runtime/server-directory.ts`
- Create: `sdn-js/src/ui/runtime/server-directory.test.ts`
- Create: `sdn-js/src/ui/runtime/helia-directory.ts`
- Create: `sdn-js/src/ui/runtime/helia-directory.test.ts`
- Modify: `sdn-js/src/ui/runtime/types.ts`
- Modify: `sdn-js/src/ui/runtime/server-adapter.ts`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/directory/DirectoryPage.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/identity/IdentityPage.js`
- Test: `sdn-js/src/ui/runtime/server-directory.test.ts`
- Test: `sdn-js/src/ui/runtime/helia-directory.test.ts`

- [ ] **Step 1: Write the failing server directory adapter test**

```ts
it('loads node and user directory results from the daemon APIs', async () => {
  const fetch = vi.fn(async (input: string) => ({
    ok: true,
    status: 200,
    async json() {
      if (input.endsWith('/api/directory/nodes?q=')) return [{ peer_id: '16Uiu2HAmExample' }]
      if (input.endsWith('/api/directory/users?q=')) return [{ dn: 'Operator Example' }]
      return {}
    }
  }))

  const adapter = createServerDirectoryAdapter({ baseUrl: 'https://node.example', fetch })
  const snapshot = await adapter.search('')

  expect(snapshot.nodes).toHaveLength(1)
  expect(snapshot.users).toHaveLength(1)
})
```

- [ ] **Step 2: Write the failing Helia directory adapter test**

```ts
it('searches locally indexed Helia directory records with the same result shape', async () => {
  const adapter = createHeliaDirectoryAdapter({
    listDirectoryRecords: async () => [
      { kind: 'node', peer_id: '16Uiu2HAmExample', dn: 'Node Example', bitcoin_address: 'bc1qexample' },
      { kind: 'user', peer_id: '16Uiu2HAmUser', dn: 'Operator Example' },
    ],
  })

  const snapshot = await adapter.search('example')
  expect(snapshot.nodes[0]?.peer_id).toBe('16Uiu2HAmExample')
  expect(snapshot.users[0]?.dn).toBe('Operator Example')
})
```

- [ ] **Step 3: Run the tests to verify failure**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/runtime/server-directory.test.ts src/ui/runtime/helia-directory.test.ts
```

Expected: FAIL because the directory adapter files do not exist.

- [ ] **Step 4: Define the shared directory types**

```ts
// sdn-js/src/ui/runtime/directory.ts
export interface DirectoryNodeRecord {
  peer_id: string
  dn?: string
  bitcoin_address?: string
  epm_cid?: string
}

export interface DirectoryUserRecord {
  peer_id?: string
  dn?: string
  legal_name?: string
}

export interface DirectorySnapshot {
  nodes: DirectoryNodeRecord[]
  users: DirectoryUserRecord[]
}
```

- [ ] **Step 5: Implement the server adapter**

```ts
// sdn-js/src/ui/runtime/server-directory.ts
export function createServerDirectoryAdapter(deps: { baseUrl: string, fetch?: typeof globalThis.fetch }) {
  const fetcher = deps.fetch ?? globalThis.fetch.bind(globalThis)
  return {
    async search(query: string): Promise<DirectorySnapshot> {
      const encoded = encodeURIComponent(query)
      const [nodes, users] = await Promise.all([
        readJson(fetcher, `${deps.baseUrl}/api/directory/nodes?q=${encoded}`),
        readJson(fetcher, `${deps.baseUrl}/api/directory/users?q=${encoded}`),
      ])
      return { nodes, users }
    },
  }
}
```

- [ ] **Step 6: Implement the Helia adapter**

```ts
// sdn-js/src/ui/runtime/helia-directory.ts
export function createHeliaDirectoryAdapter(deps: { listDirectoryRecords: () => Promise<Array<Record<string, unknown>>> }) {
  return {
    async search(query: string): Promise<DirectorySnapshot> {
      const records = await deps.listDirectoryRecords()
      const q = query.trim().toLowerCase()
      return {
        nodes: records.filter((r) => r.kind === 'node' && matchesRecord(r, q)) as DirectoryNodeRecord[],
        users: records.filter((r) => r.kind === 'user' && matchesRecord(r, q)) as DirectoryUserRecord[],
      }
    },
  }
}
```

- [ ] **Step 7: Populate the new SDN pages with adapter-backed data**

```js
// DirectoryPage.js
const [snapshot, setSnapshot] = useState({ nodes: [], users: [] })
useEffect(() => {
  directoryAdapter.search(query).then(setSnapshot)
}, [query])
```

```js
// IdentityPage.js
useEffect(() => {
  fetch('/api/node/info', { credentials: 'include' })
    .then((resp) => resp.json())
    .then(setNodeInfo)
}, [])
```

- [ ] **Step 8: Re-run the runtime tests and UI build**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/runtime/server-directory.test.ts src/ui/runtime/helia-directory.test.ts
cd /Users/tj/software/space-data-network/sdn-js && npm run build:ui
```

Expected: PASS

- [ ] **Step 9: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/src/ui/runtime/directory.ts \
  sdn-js/src/ui/runtime/server-directory.ts \
  sdn-js/src/ui/runtime/server-directory.test.ts \
  sdn-js/src/ui/runtime/helia-directory.ts \
  sdn-js/src/ui/runtime/helia-directory.test.ts \
  sdn-js/src/ui/runtime/types.ts \
  sdn-js/src/ui/runtime/server-adapter.ts \
  sdn-js/ui/src/upstream-webui/overrides/directory/DirectoryPage.js \
  sdn-js/ui/src/upstream-webui/overrides/identity/IdentityPage.js
git commit -m "Share directory query model across server and Helia"
```

### Task 7: Full Verification And Deployment Readiness

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-04-22-node-identity-epm-directory-design.md` (only if implementation forced clarification)
- Test: existing focused suites from Tasks 1-6

- [ ] **Step 1: Run the focused server suite**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && ../scripts/go-with-wasmedge.sh test ./internal/node ./internal/storage ./internal/directory ./internal/epm ./cmd/spacedatanetwork -count=1
```

Expected: PASS

- [ ] **Step 2: Run the focused `sdn-js` suite**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run \
  src/ui/vite-config.test.ts \
  src/ui/upstream-webui/branding.test.ts \
  src/ui/runtime/server-directory.test.ts \
  src/ui/runtime/helia-directory.test.ts
```

Expected: PASS

- [ ] **Step 3: Run the SDN UI build**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npm run build:ui
```

Expected: PASS

- [ ] **Step 4: Update documentation for managed node identity and directory APIs**

```md
## Managed Node Identity

- The node mnemonic under `data/<node>/keys/mnemonic` is the single root secret.
- The SDN node and managed IPFS identity are derived from that root.
- `/api/directory/nodes` and `/api/directory/users` expose the local FlatSQL-backed EPM directory index.
```

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network
git add README.md
git commit -m "Document managed node identity and directory APIs"
```
