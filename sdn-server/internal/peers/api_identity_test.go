package peers

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newIdentityTestAPI(t *testing.T) (*APIHandler, *Registry) {
	t.Helper()
	registry := NewRegistry(false, nil)
	return NewAPIHandler(registry, NewTrustedConnectionGater(registry)), registry
}

// newAddPeerKey returns a freshly generated ed25519 public key in hex plus the
// peer ID it derives. Never a fixed key: this test file must not carry material
// that resembles a real identity.
func newAddPeerKey(t *testing.T) (string, peer.ID) {
	t.Helper()
	_, pub, err := libp2pcrypto.GenerateKeyPairWithReader(libp2pcrypto.Ed25519, 256, rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	raw, err := pub.Raw()
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	return hex.EncodeToString(raw), id
}

func postJSON(t *testing.T, handler *APIHandler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// peerVCard is a contact card carrying the SDN peer-ID property plus enough
// contact fields for vcard.VCardToEPM to build a record. All values are
// obviously synthetic — the registry never invents contact data, and neither
// does this test.
func peerVCard(peerID peer.ID) string {
	return strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Example Operator",
		"N:Operator;Example;;;",
		"ORG:Example Test Org",
		"EMAIL:operator@example.invalid",
		"X-SDN-PEER-ID:" + peerID.String(),
		"X-SDN-TRUST-LEVEL:marginal",
		"END:VCARD",
		"",
	}, "\r\n")
}

// TestAddPeerByPublicKeyDerivesTheRegistryKey locks deliverable 2's first half:
// an operator handed a KEY rather than an ID can add the peer, and the record
// lands under exactly the peer ID libp2p derives from that key.
func TestAddPeerByPublicKeyDerivesTheRegistryKey(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	keyHex, wantID := newAddPeerKey(t)

	rec := postJSON(t, handler, http.MethodPost, "/api/peers", map[string]any{
		"public_key":  keyHex,
		"trust_level": "marginal",
		"name":        "Key Only Peer",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	tp, err := registry.GetPeer(wantID)
	if err != nil {
		t.Fatalf("peer was not stored under the derived ID %s: %v", wantID, err)
	}
	if tp.TrustLevel != Marginal {
		t.Fatalf("trust level = %s, want %s", tp.TrustLevel, Marginal)
	}
	if tp.Name != "Key Only Peer" {
		t.Fatalf("name = %q", tp.Name)
	}
}

// TestAddPeerAcceptsPeerIDAlias locks that peer_id is honoured as an alias for
// id, so a caller using the contract's field name is not silently rejected.
func TestAddPeerAcceptsPeerIDAlias(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	_, wantID := newAddPeerKey(t)

	rec := postJSON(t, handler, http.MethodPost, "/api/peers", map[string]any{
		"peer_id":     wantID.String(),
		"trust_level": "standard",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if _, err := registry.GetPeer(wantID); err != nil {
		t.Fatalf("peer was not stored under %s: %v", wantID, err)
	}
}

// TestAddPeerRejectsDisagreeingIdentifiers locks the fail-closed rule: a
// request naming one peer by id and another by public_key is refused rather
// than resolved to either.
func TestAddPeerRejectsDisagreeingIdentifiers(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	keyHex, _ := newAddPeerKey(t)
	_, otherID := newAddPeerKey(t)

	rec := postJSON(t, handler, http.MethodPost, "/api/peers", map[string]any{
		"id":          otherID.String(),
		"public_key":  keyHex,
		"trust_level": "standard",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if registry.PeerCount() != 0 {
		t.Fatalf("peer count = %d, want 0 — a refused request must store nothing", registry.PeerCount())
	}
}

// TestAddPeerStoresVCardAndDerivedEPM locks deliverable 2's second half on the
// JSON path: an optional vCard is stored verbatim AND its derivable EPM record
// is stored with it, so the peer's contact card is usable by every existing
// EPM/vCard/QR read on /api/peers/:id/epm.
func TestAddPeerStoresVCardAndDerivedEPM(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	keyHex, wantID := newAddPeerKey(t)
	card := peerVCard(wantID)

	rec := postJSON(t, handler, http.MethodPost, "/api/peers", map[string]any{
		"public_key":  keyHex,
		"trust_level": "standard",
		"vcard":       card,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	tp, err := registry.GetPeer(wantID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if strings.TrimSpace(tp.VCardData) != strings.TrimSpace(card) {
		t.Fatalf("VCardData was not stored verbatim:\n%q", tp.VCardData)
	}
	if len(tp.EPMData) == 0 {
		t.Fatal("EPMData is empty; the EPM derivable from the vCard was not stored")
	}
}

// TestUpdatePeerRejectsBodyIdentifyingAnotherPeer locks that the URL owns the
// identity of an update: a public_key in the body that derives a different peer
// must not redirect the write.
func TestUpdatePeerRejectsBodyIdentifyingAnotherPeer(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	_, targetID := newAddPeerKey(t)
	otherKey, otherID := newAddPeerKey(t)

	if err := registry.AddPeer(&TrustedPeer{ID: targetID, TrustLevel: Standard, Name: "Target"}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	rec := postJSON(t, handler, http.MethodPut, "/api/peers/"+targetID.String(), map[string]any{
		"public_key":  otherKey,
		"trust_level": "admin",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	tp, err := registry.GetPeer(targetID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if tp.TrustLevel != Standard {
		t.Fatalf("trust level = %s, want unchanged %s", tp.TrustLevel, Standard)
	}
	if _, err := registry.GetPeer(otherID); err == nil {
		t.Fatal("the mismatched public_key created a peer record")
	}
}

// TestUpdatePeerStoresVCardAndDerivedEPM locks that an existing peer can be
// given (or re-given) a contact card through the same field.
func TestUpdatePeerStoresVCardAndDerivedEPM(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	_, targetID := newAddPeerKey(t)
	if err := registry.AddPeer(&TrustedPeer{ID: targetID, TrustLevel: Standard}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	rec := postJSON(t, handler, http.MethodPut, "/api/peers/"+targetID.String(), map[string]any{
		"vcard": peerVCard(targetID),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	tp, err := registry.GetPeer(targetID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if tp.VCardData == "" {
		t.Fatal("VCardData was not stored")
	}
	if len(tp.EPMData) == 0 {
		t.Fatal("EPMData is empty; the EPM derivable from the vCard was not stored")
	}
}

// TestVCardImportDerivesEPMForASingleCard locks the raw-vCard endpoint's half
// of deliverable 2: a single-card payload yields both VCardData and EPMData.
func TestVCardImportDerivesEPMForASingleCard(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	_, wantID := newAddPeerKey(t)

	req := httptest.NewRequest(http.MethodPost, "/api/peers/import/vcard", strings.NewReader(peerVCard(wantID)))
	req.Header.Set("Content-Type", "text/vcard")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	tp, err := registry.GetPeer(wantID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if tp.VCardData == "" {
		t.Fatal("VCardData was not stored")
	}
	if len(tp.EPMData) == 0 {
		t.Fatal("EPMData is empty; the EPM derivable from the vCard was not stored")
	}
	if tp.TrustLevel != Marginal {
		t.Fatalf("trust level = %s, want %s from X-SDN-TRUST-LEVEL", tp.TrustLevel, Marginal)
	}
}

// TestVCardImportDoesNotMisattributeContactDataAcrossCards locks the
// never-invent-contact-data rule: a multi-card payload describes distinct
// peers, so neither the whole payload nor the first card's EPM may be attached
// to any of them.
func TestVCardImportDoesNotMisattributeContactDataAcrossCards(t *testing.T) {
	t.Parallel()

	handler, registry := newIdentityTestAPI(t)
	_, firstID := newAddPeerKey(t)
	_, secondID := newAddPeerKey(t)

	payload := peerVCard(firstID) + peerVCard(secondID)
	req := httptest.NewRequest(http.MethodPost, "/api/peers/import/vcard", strings.NewReader(payload))
	req.Header.Set("Content-Type", "text/vcard")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	for _, id := range []peer.ID{firstID, secondID} {
		tp, err := registry.GetPeer(id)
		if err != nil {
			t.Fatalf("GetPeer(%s): %v", id, err)
		}
		if tp.VCardData != "" {
			t.Fatalf("peer %s carries a multi-card payload as its own card", id)
		}
		if len(tp.EPMData) != 0 {
			t.Fatalf("peer %s carries an EPM derived from another peer's card", id)
		}
	}
}
