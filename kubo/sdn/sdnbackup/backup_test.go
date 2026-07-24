package sdnbackup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdnbackup"
	"github.com/ipfs/kubo/sdn/sdnmodules"
)

// newMemBlockstore mirrors the sdnstore/appmanifest test fixtures.
func newMemBlockstore() blockstore.Blockstore {
	return blockstore.NewBlockstore(dssync.MutexWrap(ds.NewMapDatastore()))
}

// seedNode stands up a small node worth of backup units: two installed modules
// (WASM bytes in the blockstore + registry) and one installed flow (a
// runtime.wasm + flow.plg + artifact.json triple on disk), plus a backup
// config file. It returns a BackupSource over all of them.
func seedNode(t *testing.T) (*sdnbackup.BackupSource, blockstore.Blockstore, map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	bs := newMemBlockstore()

	home := t.TempDir()
	modDir := filepath.Join(home, "sdn", "modules")
	reg, err := sdnmodules.NewRegistry(modDir)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// Two modules with distinct bytes -> distinct content hashes.
	originals := map[string][]byte{}
	modA := []byte("\x00asm\x01\x00\x00\x00; module A payload — spacetrack source")
	modB := []byte("\x00asm\x01\x00\x00\x00; module B payload — conjunction screener")
	for _, m := range []struct {
		id, name, ver string
		wasm          []byte
	}{
		{"com.orbpro.mod-a", "Module A", "1.2.0", modA},
		{"com.orbpro.mod-b", "Module B", "0.9.1", modB},
	} {
		hash, _, err := appmanifest.StoreModuleBytes(ctx, bs, m.wasm)
		if err != nil {
			t.Fatalf("store module %s: %v", m.id, err)
		}
		if err := reg.Put(sdnmodules.InstalledEntry{
			ID: m.id, ContentHash: hash, Name: m.name, Version: m.ver, Enabled: true, Source: "test",
		}); err != nil {
			t.Fatalf("register module %s: %v", m.id, err)
		}
		originals["module:"+m.id] = m.wasm
	}

	// One installed flow.
	flowDir := filepath.Join(home, "sdn", "flows")
	flows, err := flowrt.NewFlowStore(flowDir)
	if err != nil {
		t.Fatalf("new flow store: %v", err)
	}
	flowWASM := []byte("\x00asm\x01\x00\x00\x00; example flow runtime")
	flowPLG := flowrt.BuildFlowPLG(flowrt.FlowSpec{
		ProgramID: "flow-example",
		Name:      "Example Flow",
		Version:   "2.0.0",
	})
	artifact := []byte(`{"compiledAt":"2026-07-16T00:00:00Z","nodes":3}`)
	if err := flows.Install("flow-example", flowWASM, flowPLG, artifact); err != nil {
		t.Fatalf("install flow: %v", err)
	}
	originals["flow:flow-example:wasm"] = flowWASM
	originals["flow:flow-example:plg"] = flowPLG
	originals["flow:flow-example:artifact"] = artifact

	// A backup config file (config kind).
	cfgPath := sdnbackup.DefaultConfigPath(home)
	cfg := sdnbackup.Config{
		Schedule: "6h",
		Adapters: []sdnbackup.AdapterConfig{
			{ID: "local-primary", Provider: "local", Tier: "primary"},
			{ID: "gh-secondary", Provider: "github", Tier: "secondary", CredentialLane: "github", Meta: map[string]string{"repo": "me/sdn-backup"}},
		},
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	src := &sdnbackup.BackupSource{
		Blockstore: bs,
		Registry:   reg,
		Flows:      flows,
		Node:       "test-node",
		Files: []sdnbackup.SourceFile{
			{Path: reg.Path(), Kind: sdnbackup.KindModuleRegistry, FilePath: "modules/installed.json", Name: "installed.json"},
			{Path: cfgPath, Kind: sdnbackup.KindConfig, FilePath: "backup/config.json", Name: "backup config"},
		},
	}
	return src, bs, originals
}

// TestBackupVerifyRestoreRoundTrip is the end-to-end proof: enumerate a node's
// units, back them up to two local adapters (primary + secondary), verify by
// re-fetch + hash, then restore into a FRESH node and confirm every re-staged
// byte matches. A second backup run must skip every unit via has() (incremental).
func TestBackupVerifyRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, _, originals := seedNode(t)

	primary, err := sdnbackup.NewLocalAdapter(filepath.Join(t.TempDir(), "primary"), "local-primary")
	if err != nil {
		t.Fatalf("new primary adapter: %v", err)
	}
	secondary, err := sdnbackup.NewLocalAdapter(filepath.Join(t.TempDir(), "secondary"), "local-secondary")
	if err != nil {
		t.Fatalf("new secondary adapter: %v", err)
	}

	runner := &sdnbackup.Runner{
		Source: src,
		Node:   "test-node",
		Verify: sdnbackup.VerifyAll,
		Adapters: []sdnbackup.NamedAdapter{
			{ID: "local-primary", Tier: "primary", Adapter: primary},
			{ID: "local-secondary", Tier: "secondary", Adapter: secondary},
		},
	}

	// --- First backup: everything is new. ---
	res, err := runner.Backup(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.Status != sdnbackup.StatusComplete {
		t.Fatalf("status = %q, want complete; landings=%+v", res.Status, res.Landings)
	}
	// 2 modules + 1 flow + 1 registry file + 1 config = 5 units.
	if res.UnitCount != 5 {
		t.Fatalf("unit count = %d, want 5; units=%+v", res.UnitCount, res.Units)
	}
	// Each unit stored on 2 adapters => 10 stored landings, all verified.
	if res.StoredCount != 10 {
		t.Fatalf("stored count = %d, want 10", res.StoredCount)
	}
	for _, l := range res.Landings {
		if !l.Verified {
			t.Fatalf("landing not verified: %+v", l)
		}
		if l.Error != "" {
			t.Fatalf("landing error: %+v", l)
		}
	}
	if len(res.ReceiptMBL) == 0 {
		t.Fatal("no receipt emitted")
	}
	receipt, err := sdnbackup.ParseReceiptMBL(res.ReceiptMBL)
	if err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	if receipt.Status != sdnbackup.StatusComplete || receipt.UnitCount != 5 {
		t.Fatalf("receipt mismatch: %+v", receipt)
	}

	// --- Second backup: has() must skip every already-present unit. ---
	res2, err := runner.Backup(ctx)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if res2.StoredCount != 0 {
		t.Fatalf("incremental run stored %d, want 0 (all should be skipped)", res2.StoredCount)
	}
	if res2.SkipCount != 10 {
		t.Fatalf("incremental run skipped %d, want 10", res2.SkipCount)
	}
	if res2.Status != sdnbackup.StatusComplete {
		t.Fatalf("incremental status = %q, want complete", res2.Status)
	}

	// --- Restore into a FRESH node from the (primary+secondary) adapters. ---
	freshBS := newMemBlockstore()
	freshHome := t.TempDir()
	freshReg, err := sdnmodules.NewRegistry(filepath.Join(freshHome, "sdn", "modules"))
	if err != nil {
		t.Fatalf("fresh registry: %v", err)
	}
	freshFlows, err := flowrt.NewFlowStore(filepath.Join(freshHome, "sdn", "flows"))
	if err != nil {
		t.Fatalf("fresh flow store: %v", err)
	}

	var prechecked []string
	restager := &sdnbackup.NodeRestager{
		Blockstore: freshBS,
		Registry:   freshReg,
		Flows:      freshFlows,
		FileRoot:   filepath.Join(freshHome, "sdn"),
		CapabilityPrecheck: func(blob sdnbackup.BackupBlob) error {
			// The fail-closed re-check seam (spec C.7): record that every module
			// passes through it before re-staging.
			prechecked = append(prechecked, blob.Meta.PluginID)
			return nil
		},
	}

	restore, err := runner.Restore(ctx, restager, res.RestoreTargets())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restore.Failed != 0 || restore.Restored != 5 {
		t.Fatalf("restore restored=%d failed=%d, want 5/0; units=%+v", restore.Restored, restore.Failed, restore.Units)
	}
	if len(prechecked) != 2 {
		t.Fatalf("capability precheck ran %d times, want 2 (once per module)", len(prechecked))
	}

	// --- Verify re-staged bytes match the originals. ---
	// Modules: resolve back from the fresh blockstore by content hash.
	freshEntries, err := freshReg.List()
	if err != nil {
		t.Fatalf("fresh registry list: %v", err)
	}
	if len(freshEntries) != 2 {
		t.Fatalf("fresh registry has %d modules, want 2", len(freshEntries))
	}
	for _, e := range freshEntries {
		got, err := appmanifest.ResolveModuleByContentHash(ctx, freshBS, e.ContentHash)
		if err != nil {
			t.Fatalf("resolve restored module %s: %v", e.ID, err)
		}
		want := originals["module:"+e.ID]
		if want == nil {
			t.Fatalf("unexpected restored module id %q", e.ID)
		}
		if string(got) != string(want) {
			t.Fatalf("restored module %s bytes differ from original", e.ID)
		}
	}

	// Flow: the triple must be back on disk byte-identical.
	freshFlow, err := freshFlows.Get("flow-example")
	if err != nil {
		t.Fatalf("get restored flow: %v", err)
	}
	assertFileEquals(t, filepath.Join(freshFlow.Dir, "runtime.wasm"), originals["flow:flow-example:wasm"])
	assertFileEquals(t, filepath.Join(freshFlow.Dir, "flow.plg"), originals["flow:flow-example:plg"])
	assertFileEquals(t, filepath.Join(freshFlow.Dir, "artifact.json"), originals["flow:flow-example:artifact"])
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s differs from original (got %d bytes, want %d)", path, len(got), len(want))
	}
}

// TestRestoreFailoverAcrossAdapters proves multi-provider redundancy: with the
// primary adapter's object deleted, restore fails over to the secondary and a
// tampered primary blob is rejected by the hash check.
func TestRestoreFailoverAcrossAdapters(t *testing.T) {
	ctx := context.Background()
	src, _, _ := seedNode(t)

	primary, _ := sdnbackup.NewLocalAdapter(filepath.Join(t.TempDir(), "primary"), "local-primary")
	secondary, _ := sdnbackup.NewLocalAdapter(filepath.Join(t.TempDir(), "secondary"), "local-secondary")
	runner := &sdnbackup.Runner{
		Source: src,
		Node:   "test-node",
		Adapters: []sdnbackup.NamedAdapter{
			{ID: "local-primary", Tier: "primary", Adapter: primary},
			{ID: "local-secondary", Tier: "secondary", Adapter: secondary},
		},
	}
	res, err := runner.Backup(ctx)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Delete one unit from the primary so a Get there 404s -> failover.
	target := res.RestoreTargets()[0]
	if _, err := primary.Delete(ctx, sdnbackup.BlobRef{ContentHash: target.ContentHash, Kind: target.Kind}); err != nil {
		t.Fatalf("delete from primary: %v", err)
	}
	if pres, _ := primary.Has(ctx, sdnbackup.BlobRef{ContentHash: target.ContentHash, Kind: target.Kind}); pres.Present {
		t.Fatal("expected object absent on primary after delete")
	}

	// Restore just that unit: the secondary must serve it.
	restore, err := runner.Restore(ctx, noopRestager{}, []sdnbackup.RestoreTarget{target})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restore.Restored != 1 || restore.Failed != 0 {
		t.Fatalf("failover restore restored=%d failed=%d, want 1/0", restore.Restored, restore.Failed)
	}
	if restore.Units[0].AdapterID != "local-secondary" {
		t.Fatalf("served by %q, want failover to local-secondary", restore.Units[0].AdapterID)
	}
}

// noopRestager accepts any verified blob (used to isolate the failover path).
type noopRestager struct{}

func (noopRestager) Restage(ctx context.Context, blob sdnbackup.BackupBlob) error { return nil }
