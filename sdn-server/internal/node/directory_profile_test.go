package node

import (
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestIndexLocalNodeEPMStoresNodeProfileInDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "node-directory-profile-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	peerID, err := peer.Decode("16Uiu2HAm9RZz2EQx8eTsnNCD4v3HVzPf1EfBxqPLqYMXeCQFjaoz")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}
	epmSvc := epm.NewService(nil, nil, peerID, "", tmpDir)
	if err := epmSvc.Init(); err != nil {
		t.Fatalf("EPM Init failed: %v", err)
	}

	n := &Node{
		epmService:   epmSvc,
		directorySvc: directory.NewService(store),
	}
	if err := n.indexLocalNodeEPM(); err != nil {
		t.Fatalf("indexLocalNodeEPM failed: %v", err)
	}

	records, err := n.directorySvc.SearchNodes(peerID.String(), 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(records))
	}
	if records[0].PeerID != peerID.String() {
		t.Fatalf("PeerID = %q, want %q", records[0].PeerID, peerID.String())
	}
	if records[0].Kind != directory.KindNode {
		t.Fatalf("Kind = %q, want %q", records[0].Kind, directory.KindNode)
	}
	expectedCID, err := epmSvc.GetNodeEPMCID()
	if err != nil {
		t.Fatalf("GetNodeEPMCID failed: %v", err)
	}
	if records[0].EPMCID != expectedCID {
		t.Fatalf("EPMCID = %q, want %q", records[0].EPMCID, expectedCID)
	}
}
