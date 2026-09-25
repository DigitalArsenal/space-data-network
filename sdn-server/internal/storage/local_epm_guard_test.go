package storage

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func localEPMRowExists(t *testing.T, store *FlatSQLStore, peerID string) bool {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_local_epms WHERE peer_id = ?`, peerID).Scan(&n); err != nil {
		t.Fatalf("count local EPM rows: %v", err)
	}
	return n > 0
}

// 2026-09-25: one corrupt local EPM row (an empty envelope) failed every
// /api/v1/sync request, so the Store could list no datasets. A corrupt row is
// now skipped and deleted, and the delete survives a journal replay; a row
// that is well formed but will not authenticate is skipped and KEPT, because
// a changed machine key source is not corruption.
func TestCorruptLocalEPMRowsAreDroppedAndNeverFailTheLane(t *testing.T) {
	t.Setenv("SDN_EPM_STORE_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD", "")
	t.Setenv("SDN_KEY_PASSWORD_FILE", "")
	dir := t.TempDir()
	store := newLocalEPMKeyTestStore(t, dir)

	if err := store.SaveLocalEPM("good", sds.NewEPMBuilder().WithLegalName("Good node").Build()); err != nil {
		t.Fatalf("save good EPM: %v", err)
	}
	notEPM, err := store.encryptLocalEPMPayload([]byte("not a flatbuffer at all"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	wrongKey, _ := json.Marshal(localEPMEnvelope{Version: 1, Algorithm: "aes-256-gcm", KDF: "scrypt-system-derived",
		IV: base64.StdEncoding.EncodeToString(make([]byte, 12)), Ciphertext: base64.StdEncoding.EncodeToString([]byte("sealed under some other key"))})
	store.mu.Lock()
	for peer, raw := range map[string]string{"empty-envelope": "", "not-epm": notEPM, "other-key": string(wrongKey)} {
		if _, err := store.db.Exec(`INSERT INTO sdn_local_epms (peer_id, schema_name, encrypted_epm_bytes, updated_at) VALUES (?, 'EPM.fbs', ?, ?)`, peer, raw, time.Now().Unix()); err != nil {
			store.mu.Unlock()
			t.Fatalf("plant %s: %v", peer, err)
		}
	}
	store.mu.Unlock()

	store.mu.RLock()
	count, _, err := store.localEPMSummaryLocked()
	store.mu.RUnlock()
	if err != nil {
		t.Fatalf("summary failed on a corrupt row: %v", err)
	}
	if count != 1 {
		t.Fatalf("summary counted %d local EPMs, want only the good one", count)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (localEPMRowExists(t, store, "empty-envelope") || localEPMRowExists(t, store, "not-epm")) {
		time.Sleep(20 * time.Millisecond)
	}
	for _, peer := range []string{"empty-envelope", "not-epm"} {
		if localEPMRowExists(t, store, peer) {
			t.Fatalf("corrupt row %s was not deleted", peer)
		}
	}
	if !localEPMRowExists(t, store, "other-key") {
		t.Fatal("a well-formed row that fails authentication was deleted; it must be kept")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := newLocalEPMKeyTestStore(t, dir)
	defer reopened.Close()
	for _, peer := range []string{"empty-envelope", "not-epm"} {
		if localEPMRowExists(t, reopened, peer) {
			t.Fatalf("corrupt row %s came back after replay", peer)
		}
	}
	if got, err := reopened.LoadLocalEPM("good"); err != nil || len(got) == 0 {
		t.Fatalf("good EPM lost: %v", err)
	}
}
