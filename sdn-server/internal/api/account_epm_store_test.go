package api

// What "pin" concretely means on a node with no Kubo attached — and that the
// fleet-law reconciler can restore a binding the store has lost.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const accountEPMTestPeerID = "12D3KooWAccountEPMNodePeerID"

// buildTestAccountEPM issues a signed account record the same way the endpoint
// does: the node's EPM service, a subject account key.
func buildTestAccountEPM(t *testing.T, dn string) ([]byte, string) {
	t.Helper()

	_, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	svc := epm.NewService(nil, nil, "", "", t.TempDir())
	if err := svc.SetRuntimeSigningKey(nodePriv, "sdn/runtime-signing"); err != nil {
		t.Fatalf("SetRuntimeSigningKey: %v", err)
	}
	accountPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate account key: %v", err)
	}
	subjectKey := hex.EncodeToString(accountPub)

	record, err := svc.BuildAccountEPM(&epm.Profile{DN: dn}, epm.AccountSubject{SigningPubKeyHex: subjectKey})
	if err != nil {
		t.Fatalf("BuildAccountEPM: %v", err)
	}
	return record, subjectKey
}

// A node with no Kubo attached still PINS: the record is retained by the store
// and a pin-ledger entry stands, which is what keeps GC off it.
func TestAccountEPMStore_StoresAndPinsWithoutKubo(t *testing.T) {
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	lane := NewAccountEPMStore(store, accountEPMTestPeerID, nil)

	record, subjectKey := buildTestAccountEPM(t, "Ada Lovelace")
	sourceName := auth.AccountEPMSourceName(subjectKey)

	cid, err := lane.StoreAccountEPM(context.Background(), sourceName, record)
	if err != nil {
		t.Fatalf("StoreAccountEPM: %v", err)
	}
	if cid != storage.ComputeCID(record) {
		t.Fatalf("cid = %q, want the record's content identifier %q", cid, storage.ComputeCID(record))
	}

	// The record is a real, queryable member of the account lane.
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "EPM.fbs",
		ProviderID: "account",
		SourceName: sourceName,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query account lane: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("account lane rows = %d, want 1", len(rows))
	}
	if err := epm.VerifyEPMSignature(rows[0].Data); err != nil {
		t.Fatalf("stored record does not verify: %v", err)
	}

	// The pin entry stands, so the CID reads back as pinned.
	pinned, err := lane.AccountEPMPinned(context.Background(), cid)
	if err != nil {
		t.Fatalf("AccountEPMPinned: %v", err)
	}
	if !pinned {
		t.Fatal("a stored account EPM is not reported as pinned")
	}

	// A CID this node never stored is honestly reported as not pinned.
	other, _ := buildTestAccountEPM(t, "Somebody Else")
	if pinned, err := lane.AccountEPMPinned(context.Background(), storage.ComputeCID(other)); err != nil || pinned {
		t.Fatalf("unstored CID reported pinned = %v (err %v)", pinned, err)
	}
}

// The fleet law end to end against the REAL lane: a node whose store no longer
// holds an account's record — restored from a backup taken before the publish —
// puts the record and its pin back from the persisted binding, under the same
// CID.
func TestAccountEPMReconciler_RestoresBindingIntoRealLane(t *testing.T) {
	store, _, _ := newDataAPITestStoreWithBasePath(t)
	lane := NewAccountEPMStore(store, accountEPMTestPeerID, nil)

	userStore, err := auth.NewUserStore(filepath.Join(t.TempDir(), "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	const xpub = "xpub-account-epm-reconcile"
	record, subjectKey := buildTestAccountEPM(t, "Ada Lovelace")
	if err := userStore.AddUser(xpub, "Ada", peers.Marginal, subjectKey); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	cid := storage.ComputeCID(record)
	if err := userStore.SaveAccountEPM(xpub, record, cid, ""); err != nil {
		t.Fatalf("SaveAccountEPM: %v", err)
	}

	if pinned, _ := lane.AccountEPMPinned(context.Background(), cid); pinned {
		t.Fatal("the store already holds a record it was never given")
	}

	result := auth.NewAccountEPMReconciler(userStore, lane).Run(context.Background())
	if result.Checked != 1 || result.Repinned != 1 || len(result.Unsatisfied) != 0 {
		t.Fatalf("reconcile pass = %+v, want 1 checked / 1 re-pinned / 0 unsatisfied", result)
	}

	pinned, err := lane.AccountEPMPinned(context.Background(), cid)
	if err != nil {
		t.Fatalf("AccountEPMPinned: %v", err)
	}
	if !pinned {
		t.Fatalf("reconciler did not restore the pin for %s", cid)
	}
	binding, ok, err := userStore.AccountEPM(xpub)
	if err != nil || !ok || binding.CID != cid {
		t.Fatalf("binding after reconcile = (%+v, %v, %v)", binding, ok, err)
	}
}
