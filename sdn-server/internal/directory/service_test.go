package directory

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func mustNewDirectoryStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "directory-test-*")
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

	return store
}

func TestDirectoryService_IndexesNodeEPMJSON(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)

	info := map[string]any{
		"peerID":          "16Uiu2HAmExample",
		"DN":              "SDN Node Example",
		"LEGAL_NAME":      "Space Data Node Example LLC",
		"BITCOIN_ADDRESS": "bc1qexample",
	}

	if err := svc.UpsertNodeEPMJSON(info, "bafyexample", ""); err != nil {
		t.Fatalf("UpsertNodeEPMJSON failed: %v", err)
	}

	nodes, err := svc.SearchNodes("bc1qexample", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}

	got := nodes[0]
	if got.Kind != "node" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "node")
	}
	if got.PeerID != "16Uiu2HAmExample" {
		t.Fatalf("PeerID = %q, want %q", got.PeerID, "16Uiu2HAmExample")
	}
	if got.BitcoinAddress != "bc1qexample" {
		t.Fatalf("BitcoinAddress = %q, want %q", got.BitcoinAddress, "bc1qexample")
	}
	if got.Source != "unknown" {
		t.Fatalf("Source = %q, want %q", got.Source, "unknown")
	}

	var canonical map[string]any
	if err := json.Unmarshal([]byte(got.EPMJSON), &canonical); err != nil {
		t.Fatalf("failed to unmarshal canonical JSON: %v", err)
	}
	if canonical["directory_kind"] != "node" {
		t.Fatalf("directory_kind = %v, want %q", canonical["directory_kind"], "node")
	}
	if canonical["peer_id"] != "16Uiu2HAmExample" {
		t.Fatalf("peer_id = %v, want %q", canonical["peer_id"], "16Uiu2HAmExample")
	}
	if canonical["dn"] != "SDN Node Example" {
		t.Fatalf("dn = %v, want %q", canonical["dn"], "SDN Node Example")
	}
	if canonical["legal_name"] != "Space Data Node Example LLC" {
		t.Fatalf("legal_name = %v, want %q", canonical["legal_name"], "Space Data Node Example LLC")
	}
	if canonical["bitcoin_address"] != "bc1qexample" {
		t.Fatalf("bitcoin_address = %v, want %q", canonical["bitcoin_address"], "bc1qexample")
	}
	if _, ok := canonical["DN"]; ok {
		t.Fatal("canonical JSON should not retain uppercase DN key")
	}
}

func TestHTTPHandler_ServesNodeAndUserSearches(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)

	for i := 0; i < 120; i++ {
		if err := svc.UpsertNodeEPMJSON(map[string]any{
			"peer_id":         fmt.Sprintf("16Uiu2HAmNode%03d", i),
			"dn":              "Discovery Node",
			"legal_name":      "Discovery Node LLC",
			"bitcoin_address": fmt.Sprintf("bc1qnodeexample%03d", i),
		}, fmt.Sprintf("bafy-node-%03d", i), "dht-discovery"); err != nil {
			t.Fatalf("UpsertNodeEPMJSON failed: %v", err)
		}
	}
	if err := svc.UpsertUserEPMJSON(map[string]any{
		"peer_id":    "16Uiu2HAmUser",
		"dn":         "Alice Example",
		"legal_name": "Alice Example",
	}, "bafy-user", "manual"); err != nil {
		t.Fatalf("UpsertUserEPMJSON failed: %v", err)
	}

	handler := NewHTTPHandler(svc)

	t.Run("nodes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/directory/nodes?q=Discovery&limit=120", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}

		var payload struct {
			Results []struct {
				Kind           string `json:"kind"`
				PeerID         string `json:"peer_id"`
				DN             string `json:"dn"`
				LegalName      string `json:"legal_name"`
				BitcoinAddress string `json:"bitcoin_address"`
				EPMCID         string `json:"epm_cid"`
				Source         string `json:"source"`
			} `json:"results"`
			Count int `json:"count"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("json decode failed: %v", err)
		}
		if payload.Count != 120 {
			t.Fatalf("count = %d, want 120", payload.Count)
		}
		if len(payload.Results) != 120 {
			t.Fatalf("results len = %d, want 120", len(payload.Results))
		}
		got := payload.Results[0]
		if got.Kind != "node" || got.PeerID != "16Uiu2HAmNode000" {
			t.Fatalf("unexpected node result: %#v", got)
		}
		if got.DN != "Discovery Node" || got.LegalName != "Discovery Node LLC" {
			t.Fatalf("unexpected node fields: %#v", got)
		}
		if got.Source != "dht-discovery" {
			t.Fatalf("Source = %q, want %q", got.Source, "dht-discovery")
		}
	})

	t.Run("users", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/directory/users?q=Alice", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}

		var payload struct {
			Results []struct {
				Kind      string `json:"kind"`
				PeerID    string `json:"peer_id"`
				DN        string `json:"dn"`
				LegalName string `json:"legal_name"`
			} `json:"results"`
			Count int `json:"count"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("json decode failed: %v", err)
		}
		if payload.Count != 1 || len(payload.Results) != 1 {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		got := payload.Results[0]
		if got.Kind != "user" || got.PeerID != "16Uiu2HAmUser" {
			t.Fatalf("unexpected user result: %#v", got)
		}
		if got.DN != "Alice Example" || got.LegalName != "Alice Example" {
			t.Fatalf("unexpected user fields: %#v", got)
		}
	})
}

func TestSearchNodesHonorsRequestedLimit(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)

	for i := 0; i < 120; i++ {
		if err := svc.UpsertNodeEPMJSON(map[string]any{
			"peer_id":         fmt.Sprintf("16Uiu2HAmNode%03d", i),
			"dn":              "Discovery Node",
			"legal_name":      "Discovery Node LLC",
			"bitcoin_address": fmt.Sprintf("bc1qnodeexample%03d", i),
		}, fmt.Sprintf("bafy-node-%03d", i), "dht-discovery"); err != nil {
			t.Fatalf("UpsertNodeEPMJSON failed: %v", err)
		}
	}

	nodes, err := svc.SearchNodes("Discovery", 120)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 120 {
		t.Fatalf("SearchNodes returned %d records, want 120", len(nodes))
	}
}
