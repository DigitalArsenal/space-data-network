package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func newFixtureStoreWithOptions(t *testing.T, basePath string, opts ...StoreOption) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator(): %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator, opts...)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", basePath, err)
	}
	return store
}

// appendFatAuxFrames writes frames big enough that a 512-FRAME chunk would be a
// multi-megabyte transaction — the shape the frame count alone cannot bound.
func appendFatAuxFrames(t *testing.T, store *FlatSQLStore, frames, padBytes int) {
	t.Helper()
	pad := strings.Repeat("p", padBytes)
	for i := 0; i < frames; i++ {
		if err := store.auxiliaryMetadata.Append(auxiliaryMetadataEvent{
			Kind: auxiliaryEventDirectoryUpsert,
			Directory: &DirectoryRecord{
				Kind:      "node",
				PeerID:    fmt.Sprintf("16Uiu2HAmFat%06d", i),
				DN:        fmt.Sprintf("fat %d %s", i, pad),
				Source:    "local",
				EPMJSON:   "{}",
				UpdatedAt: int64(1700000000 + i),
			},
		}); err != nil {
			t.Fatalf("append frame %d: %v", i, err)
		}
	}
}

// A byte budget must bound the transaction WITHOUT losing a frame. The failure
// this guards is specific: replayChunkLocked reports "the journal ends here"
// with the same signal it used to use for "the chunk is full", so a budget stop
// mistaken for a torn tail would silently truncate the replay and the node
// would boot with a partial control database and call it success.
func TestAuxiliaryReplayByteBudgetSplitsChunksWithoutLosingFrames(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	// 4 KiB budget against ~1 KiB frames: several chunks for well under one
	// frame-count chunk, so the byte budget is demonstrably the thing cutting.
	store := newFixtureStoreWithOptions(t, basePath, WithAuxiliaryReplayChunkBytes(4<<10))

	const frames = 64
	appendFatAuxFrames(t, store, frames, 1024)
	journalEnd := store.auxiliaryMetadata.validLength()
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	if err := removeControlDatabaseFiles(filepath.Join(basePath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database: %v", err)
	}
	cold := newFixtureStoreWithOptions(t, basePath, WithAuxiliaryReplayChunkBytes(4<<10))
	defer cold.Close()

	if cold.bootAuxFrames != frames {
		t.Fatalf("byte-budgeted replay applied %d frames, want all %d", cold.bootAuxFrames, frames)
	}
	if got := cold.auxAppliedOffset.Load(); got != journalEnd {
		t.Fatalf("byte-budgeted replay applied through %d, want the whole journal %d", got, journalEnd)
	}
	var rows int
	if err := cold.db.QueryRow(
		`SELECT COUNT(*) FROM sdn_directory WHERE peer_id LIKE '16Uiu2HAmFat%'`).Scan(&rows); err != nil {
		t.Fatalf("count replayed rows: %v", err)
	}
	if rows != frames {
		t.Fatalf("byte-budgeted replay landed %d rows, want %d", rows, frames)
	}
}

// A frame LARGER than the whole budget is applied whole. Frames are never
// split, and a replay that refuses its own journal would be a worse failure
// than one big transaction.
func TestAuxiliaryReplayByteBudgetNeverStallsOnAnOversizedFrame(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStoreWithOptions(t, basePath, WithAuxiliaryReplayChunkBytes(256))

	const frames = 8
	appendFatAuxFrames(t, store, frames, 4096) // every frame exceeds the budget
	journalEnd := store.auxiliaryMetadata.validLength()
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	if err := removeControlDatabaseFiles(filepath.Join(basePath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database: %v", err)
	}
	cold := newFixtureStoreWithOptions(t, basePath, WithAuxiliaryReplayChunkBytes(256))
	defer cold.Close()

	if cold.bootAuxFrames != frames {
		t.Fatalf("oversized-frame replay applied %d frames, want %d", cold.bootAuxFrames, frames)
	}
	if got := cold.auxAppliedOffset.Load(); got != journalEnd {
		t.Fatalf("oversized-frame replay applied through %d, want %d", got, journalEnd)
	}
}
