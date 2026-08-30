package api

// Resolved sources against a REAL store: a producing peer with a signed
// publisher profile in the directory resolves to its organization; a peer
// without one resolves to a NIL organization — never a guess.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestResolvedSourcesComposesDirectoryIdentity(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	seed := func(sourceName, peerID string, base, n int) {
		tags := storage.SourceTags{
			ProviderID:     "test-provider",
			SourceName:     sourceName,
			BatchID:        "b-" + sourceName,
			ContentKeyID:   "public",
			ProducerPeerID: peerID,
		}
		for i := 0; i < n; i++ {
			record := sds.NewOMMBuilder().
				WithNoradCatID(uint32(base + i)).
				WithObjectName(fmt.Sprintf("%s-%02d", sourceName, i)).
				WithEpoch("2026-05-12T00:00:00Z").
				Build()
			if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:"+sourceName, nil, tags); err != nil {
				t.Fatalf("store: %v", err)
			}
		}
	}
	seed("lane-known", "16UiuKNOWNPEER", 30000, 5)
	seed("lane-unknown", "16UiuMYSTERY", 40000, 3)

	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "16UiuKNOWNPEER",
		DN:        "obs-01",
		LegalName: "State University Radio Observatory",
		EPMCID:    "bafkreiepmtest",
		Source:    "test",
	}); err != nil {
		t.Fatalf("UpsertDirectoryRecord: %v", err)
	}

	h := &DataQueryHandler{store: store}
	r := httptest.NewRequest("GET", "/api/v1/data/sources", nil)
	w := httptest.NewRecorder()
	h.handleResolvedSources(w, r)
	if w.Code != 200 {
		t.Fatalf("resolved sources -> %d: %s", w.Code, w.Body.String())
	}
	var resp resolvedSourcesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byLane := map[string]resolvedSourceRow{}
	for _, row := range resp.Sources {
		byLane[row.SourceName] = row
	}

	known, ok := byLane["lane-known"]
	if !ok {
		t.Fatalf("lane-known missing from %+v", resp.Sources)
	}
	if known.Organization == nil || known.Organization.Name != "State University Radio Observatory" {
		t.Fatalf("known lane organization = %+v, want the directory legal name", known.Organization)
	}
	if known.Organization.State != "signed" {
		t.Fatalf("known lane state = %q, want signed", known.Organization.State)
	}
	if known.ProducerPeerID != "16UiuKNOWNPEER" {
		t.Fatalf("known lane producer = %q", known.ProducerPeerID)
	}
	if known.Count != 5 {
		t.Fatalf("known lane count = %d, want 5", known.Count)
	}

	unknown, ok := byLane["lane-unknown"]
	if !ok {
		t.Fatalf("lane-unknown missing")
	}
	if unknown.Organization != nil {
		t.Fatalf("unknown lane must resolve to a NIL organization (never a guess), got %+v", unknown.Organization)
	}
}
