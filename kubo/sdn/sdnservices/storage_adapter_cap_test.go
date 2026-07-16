package sdnservices_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnbackup"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// backupSourceFixture stands up a one-module BackupSource for the cap tests.
func backupSourceFixture(t *testing.T) (*sdnbackup.BackupSource, string, []byte) {
	t.Helper()
	ctx := context.Background()
	bs := blockstore.NewBlockstore(dssync.MutexWrap(ds.NewMapDatastore()))
	reg, err := sdnmodules.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	wasm := []byte("\x00asm\x01\x00\x00\x00; storage_adapter cap fixture module")
	hash, _, err := appmanifest.StoreModuleBytes(ctx, bs, wasm)
	if err != nil {
		t.Fatalf("store module: %v", err)
	}
	if err := reg.Put(sdnmodules.InstalledEntry{ID: "com.orbpro.fixture", ContentHash: hash, Enabled: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	src := &sdnbackup.BackupSource{Blockstore: bs, Registry: reg, Node: "test-node"}
	return src, hash, wasm
}

// TestStorageAdapterCapGatedAndFunctional proves the repurposed storage_adapter
// capability (spec A.4): with the grant, list_units + get_unit expose the
// node's backup units; without it they are denied; with no backup source they
// fail closed.
func TestStorageAdapterCapGatedAndFunctional(t *testing.T) {
	src, hash, wasm := backupSourceFixture(t)

	// Granted module: storage.adapter.* works.
	h := sdnservices.NewStorageCapFactoryWithSource(nil, "node", src)(nil, modulert.NewHostBridge(nil, []string{"storage_adapter"}))

	listResp := decodeCap(t, h, "storage.adapter.list_units", `{}`)
	if ok, _ := listResp["ok"].(bool); !ok {
		t.Fatalf("list_units denied for a granted module: %v", listResp)
	}
	listResult, _ := listResp["result"].(map[string]any)
	units, _ := listResult["units"].([]any)
	if len(units) != 1 {
		t.Fatalf("list_units returned %d units, want 1", len(units))
	}
	first, _ := units[0].(map[string]any)
	if first["contentHash"] != hash {
		t.Fatalf("unit hash = %v, want %s", first["contentHash"], hash)
	}
	if first["kind"] != "module_wasm" {
		t.Fatalf("unit kind = %v, want module_wasm", first["kind"])
	}

	getResp := decodeCap(t, h, "storage.adapter.get_unit", `{"contentHash":"`+hash+`"}`)
	if ok, _ := getResp["ok"].(bool); !ok {
		t.Fatalf("get_unit denied for a granted module: %v", getResp)
	}
	getResult, _ := getResp["result"].(map[string]any)
	b64, _ := getResult["bytes_b64"].(string)
	got, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("get_unit bytes not base64: %v", err)
	}
	if string(got) != string(wasm) {
		t.Fatal("get_unit bytes differ from the stored module")
	}

	// Ungranted module: both ops denied.
	hDenied := sdnservices.NewStorageCapFactoryWithSource(nil, "node", src)(nil, modulert.NewHostBridge(nil, []string{"storage_query"}))
	for _, op := range []string{"storage.adapter.list_units", "storage.adapter.get_unit"} {
		resp := decodeCap(t, hDenied, op, `{"contentHash":"`+hash+`"}`)
		if ok, _ := resp["ok"].(bool); ok {
			t.Fatalf("%s succeeded WITHOUT the storage_adapter grant", op)
		}
	}

	// No backup source wired: fail closed even with the grant.
	hNoSrc := sdnservices.NewStorageCapFactoryWithSource(nil, "node", nil)(nil, modulert.NewHostBridge(nil, []string{"storage_adapter"}))
	resp := decodeCap(t, hNoSrc, "storage.adapter.list_units", `{}`)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatal("list_units succeeded with no backup source configured (should fail closed)")
	}
}

func decodeCap(t *testing.T, h modulert.CapHandler, op, payload string) map[string]any {
	t.Helper()
	raw, err := h(op, []byte(payload))
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", op, err)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s response: %v", op, err)
	}
	return resp
}
