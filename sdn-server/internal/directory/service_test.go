package directory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
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
		"photo_data_url":  "data:image/png;base64,iVBORw0KGgo=",
		"signature":       "abcdef",
		"keys": []map[string]any{
			{
				"key_type":   "signing",
				"public_key": "ed25519-public",
			},
		},
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
	if canonical["photo_data_url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("photo_data_url = %v, want embedded profile image", canonical["photo_data_url"])
	}
	if canonical["signature"] != "abcdef" {
		t.Fatalf("signature = %v, want embedded EPM signature", canonical["signature"])
	}
	keys, ok := canonical["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("keys = %#v, want one preserved key", canonical["keys"])
	}
	if _, ok := canonical["DN"]; ok {
		t.Fatal("canonical JSON should not retain uppercase DN key")
	}
}

func TestDirectoryService_SearchNodesExcludesSelfAndGenericPeerConnectRows(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	svc.SetLocalPeerID("16Uiu2HAmSelf")

	records := []struct {
		peerID string
		name   string
		source string
	}{
		{
			peerID: "16Uiu2HAmSelf",
			name:   "This node",
			source: "local-node",
		},
		{
			peerID: "16Uiu2HAmAdvertised",
			name:   "Advertised peer",
			source: "sdn-advertisement-discovery",
		},
		{
			peerID: "16Uiu2HAmGenericConnect",
			name:   "Generic libp2p connect",
			source: "peer-connect",
		},
	}
	for _, record := range records {
		if err := svc.UpsertNodeEPMJSON(map[string]any{
			"peer_id": record.peerID,
			"dn":      record.name,
		}, "bafy-"+record.peerID, record.source); err != nil {
			t.Fatalf("UpsertNodeEPMJSON(%s) failed: %v", record.peerID, err)
		}
	}

	nodes, err := svc.SearchNodes("", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1: %#v", len(nodes), nodes)
	}
	if nodes[0].PeerID != "16Uiu2HAmAdvertised" {
		t.Fatalf("PeerID = %q, want advertised peer", nodes[0].PeerID)
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
		var got struct {
			Kind           string `json:"kind"`
			PeerID         string `json:"peer_id"`
			DN             string `json:"dn"`
			LegalName      string `json:"legal_name"`
			BitcoinAddress string `json:"bitcoin_address"`
			EPMCID         string `json:"epm_cid"`
			Source         string `json:"source"`
		}
		for _, result := range payload.Results {
			if result.PeerID == "16Uiu2HAmNode000" {
				got = result
				break
			}
		}
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

func TestAdminHTTPHandler_ImportsDirectoryEPMJSON(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	handler := NewAdminHTTPHandler(svc)

	body := `{
		"kind": "node",
		"source": "manual-upload",
		"epm_cid": "bafy-uploaded-node",
		"epm_json": {
			"peer_id": "16Uiu2HAmUploaded",
			"dn": "Uploaded Node",
			"bitcoin_address": "bc1quploaded"
		}
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/directory/import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"imported":1`) {
		t.Fatalf("response missing import count: %s", rec.Body.String())
	}

	nodes, err := svc.SearchNodes("bc1quploaded", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}
	if nodes[0].PeerID != "16Uiu2HAmUploaded" || nodes[0].EPMCID != "bafy-uploaded-node" {
		t.Fatalf("unexpected imported node: %#v", nodes[0])
	}
}

func TestAdminHTTPHandler_InfersImportedEPMKindFromEntityType(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	handler := NewAdminHTTPHandler(svc)

	body := `{
		"source": "manual-upload",
		"epm_cid": "bafy-uploaded-node-entity-type",
		"epm_json": {
			"entity_type": "node",
			"peer_id": "16Uiu2HAmEntityTypedNode",
			"dn": "Entity Typed Node",
			"bitcoin_address": "bc1qentitytypednode"
		}
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/directory/import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	nodes, err := svc.SearchNodes("entitytypednode", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}
	if nodes[0].Kind != KindNode {
		t.Fatalf("Kind = %q, want %q", nodes[0].Kind, KindNode)
	}
	if nodes[0].PeerID != "16Uiu2HAmEntityTypedNode" {
		t.Fatalf("PeerID = %q, want %q", nodes[0].PeerID, "16Uiu2HAmEntityTypedNode")
	}
}

func TestAdminHTTPHandler_DefaultsImportedEPMKindToUser(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	handler := NewAdminHTTPHandler(svc)

	body := `{
		"source": "manual-upload",
		"epm_json": {
			"peer_id": "16Uiu2HAmDefaultUser",
			"dn": "Default User",
			"legal_name": "Default User"
		}
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/directory/import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	users, err := svc.SearchUsers("Default User", 10)
	if err != nil {
		t.Fatalf("SearchUsers failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("SearchUsers returned %d records, want 1", len(users))
	}
	if users[0].Kind != KindUser {
		t.Fatalf("Kind = %q, want %q", users[0].Kind, KindUser)
	}
}

func TestAdminHTTPHandler_ImportsDirectoryVCard(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	handler := NewAdminHTTPHandler(svc)

	body := `{
		"kind": "node",
		"source": "manual-vcard-upload",
		"vcard": "BEGIN:VCARD\nVERSION:4.0\nFN:Uploaded vCard Node\nORG:Space Data Directory\nX-SDN-PEER-ID:16Uiu2HAmVCard\nX-SDN-BITCOIN-ADDRESS:bc1qvcard\nX-SDN-EPM-CID:bafy-vcard-node\nEND:VCARD"
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/directory/import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	nodes, err := svc.SearchNodes("bc1qvcard", 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}
	if nodes[0].PeerID != "16Uiu2HAmVCard" || nodes[0].DN != "Uploaded vCard Node" {
		t.Fatalf("unexpected imported node: %#v", nodes[0])
	}
	if nodes[0].EPMCID != "bafy-vcard-node" {
		t.Fatalf("EPMCID = %q, want %q", nodes[0].EPMCID, "bafy-vcard-node")
	}
}

func TestAdminHTTPHandler_ImportsEmbeddedSignedEPMFromVCard(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)
	handler := NewAdminHTTPHandler(svc)

	identityPriv, _, err := libp2pcrypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x71}, 64)))
	if err != nil {
		t.Fatalf("GenerateSecp256k1Key failed: %v", err)
	}
	signingPriv, signingPub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x72}, 64)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	peerID, err := peer.IDFromPublicKey(identityPriv.GetPublic())
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}
	identity := &wasm.DerivedIdentity{
		IdentityPrivKey:   identityPriv,
		IdentityPubKey:    identityPriv.GetPublic(),
		SigningPrivKey:    signingPriv,
		SigningPubKey:     signingPub,
		EncryptionPub:     bytes.Repeat([]byte{0x73}, 32),
		PeerID:            peerID,
		IdentityKeyPath:   "m/44'/0'/0'",
		SigningKeyPath:    "m/44'/0'/0'/0'/0'",
		EncryptionKeyPath: "m/44'/0'/0'/1'/0'",
	}
	epmSvc := epm.NewService(identity, nil, peerID, "xpub-test", t.TempDir())
	if err := epmSvc.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := epmSvc.UpdateProfile(&epm.Profile{DN: "Trusted Node"}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}
	vcard, err := epmSvc.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard failed: %v", err)
	}
	spoofed := strings.Replace(vcard, "FN:Trusted Node", "FN:Spoofed Node", 1)
	body, err := json.Marshal(map[string]string{
		"source": "manual-vcard-upload",
		"vcard":  spoofed,
	})
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/directory/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	nodes, err := svc.SearchNodes(peerID.String(), 10)
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}
	if got, want := nodes[0].DN, "Trusted Node"; got != want {
		t.Fatalf("DN = %q, want signed EPM value %q", got, want)
	}
	if strings.Contains(nodes[0].EPMJSON, "Spoofed Node") {
		t.Fatalf("directory stored spoofed vCard fields instead of signed EPM: %s", nodes[0].EPMJSON)
	}
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
