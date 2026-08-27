package logservice

import (
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// AppendEntryBatch must extend the same (publisher, schema) log AppendEntry
// does: the head sequence advances by the batch size, every PLG entry lands in
// the store, and a later single append continues the chain from the batch head.
func TestAppendEntryBatchExtendsTheSameLog(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const peer = "12D3KooWBatchTestPeer"
	svc := NewService(store, key, peer)

	// Single append first, so the batch has a real head to chain from.
	if _, seq, err := svc.AppendEntry("OMM.fbs", "bafycid-single-1", nil, ""); err != nil || seq != 1 {
		t.Fatalf("AppendEntry = seq %d, err %v; want seq 1", seq, err)
	}

	head, err := svc.AppendEntryBatch("OMM.fbs", []string{"bafycid-batch-1", "bafycid-batch-2", "bafycid-batch-3"})
	if err != nil {
		t.Fatalf("AppendEntryBatch: %v", err)
	}
	if head != 4 {
		t.Fatalf("batch head = %d, want 4 (1 single + 3 batched)", head)
	}

	// A later single append continues from the batch head.
	if _, seq, err := svc.AppendEntry("OMM.fbs", "bafycid-single-2", nil, ""); err != nil || seq != 5 {
		t.Fatalf("post-batch AppendEntry = seq %d, err %v; want seq 5", seq, err)
	}

	if gotSeq, gotHash, err := store.GetLogHead(peer, "OMM.fbs"); err != nil || gotSeq != 5 || gotHash == "" {
		t.Fatalf("GetLogHead = (%d, %q, %v), want sequence 5 with a head hash", gotSeq, gotHash, err)
	}

	// Every entry — batched and single — is a stored PLG record.
	entries, err := store.QueryLogEntries(peer, "OMM.fbs", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("stored PLG entries = %d, want 5", len(entries))
	}

	// Empty batch is a no-op, not an error.
	if _, err := svc.AppendEntryBatch("OMM.fbs", nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
}
