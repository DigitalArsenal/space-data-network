package peers

// THE SILENT NO-OP (graph task sdn-peer-modal-trust-apply-honesty, owner report
// 2026-07-30). The owner set FULL on a peer through the dashboard, the request
// answered 200 OK, and EFFECTIVE stayed STANDARD. The node was not refusing the
// write and the session was not under-privileged: sdn.spaceaware.io's FlatSQL
// engine was POISONED, every peer-registry Save() failed, and Registry.save()
// logged a warning and threw the error away — so SetTrustLevel returned nil and
// the API reported success for a change that existed only in RAM.
//
// journalctl, host-01, 2026-07-30T21:17:04Z, verbatim:
//
//	WARN sdn-peers peers/trust.go:1359 Failed to persist peer registry:
//	peers: load PRR records: raw record query failed: flatsqlrt: query refused:
//	engine is poisoned and awaiting replacement
//
// These tests are the lock: a registry mutation that did not reach storage must
// report ErrNotPersisted, and the admin API must answer a 5xx the operator can
// read — never 200/204.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// poisonedPersistence stands in for a FlatSQL engine that is refusing writes.
type poisonedPersistence struct {
	loaded map[peer.ID]*TrustedPeer
	saves  int
}

func (p *poisonedPersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	p.saves++
	return fmt.Errorf("flatsqlrt: query refused: engine is poisoned and awaiting replacement")
}

func (p *poisonedPersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	if p.loaded == nil {
		p.loaded = map[peer.ID]*TrustedPeer{}
	}
	return p.loaded, map[string]*PeerGroup{}, nil
}

func TestSetTrustLevel_ReportsWhenStorageRefusedTheWrite(t *testing.T) {
	store := &poisonedPersistence{}
	registry := NewRegistry(false, store)
	id, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	// AddPeer itself cannot persist either — that is expected and is not what
	// this test is about, so its error is deliberately ignored here.
	_ = registry.AddPeer(&TrustedPeer{ID: id, TrustLevel: Standard, Name: "peer"})

	err := registry.SetTrustLevel(id, Trusted)

	if err == nil {
		t.Fatal("SetTrustLevel reported success for a write storage refused — this is the owner's silent no-op")
	}
	if !errors.Is(err, ErrNotPersisted) {
		t.Fatalf("want ErrNotPersisted, got %v", err)
	}
	// The running node still honours the operator's intent until it restarts.
	if got := registry.GetTrustLevel(id); got != Trusted {
		t.Fatalf("in-memory level should still be Trusted, got %v", got)
	}
}

func TestAPI_TrustWriteThatDidNotPersistIsNot200(t *testing.T) {
	store := &poisonedPersistence{}
	registry := NewRegistry(false, store)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	id, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	_ = registry.AddPeer(&TrustedPeer{ID: id, TrustLevel: Standard, Name: "peer"})

	body := bytes.NewBufferString(`{"trust_level":"full"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/peers/"+id.String()+"/trust", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("the API answered 200 for a trust change that was never written to storage")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (body %s)", w.Code, w.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("refusal must be JSON the dashboard can turn into a sentence: %v", err)
	}
	if payload["code"] != "not_persisted" {
		t.Fatalf(`want code "not_persisted", got %q`, payload["code"])
	}
	if payload["message"] == "" {
		t.Fatal("a refusal with no message is the raw-HTTP-number defect again")
	}
}

func TestAPI_RemoveThatDidNotPersistIsNot204(t *testing.T) {
	store := &poisonedPersistence{}
	registry := NewRegistry(false, store)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	id, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	_ = registry.AddPeer(&TrustedPeer{ID: id, TrustLevel: Standard, Name: "peer"})

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/"+id.String(), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code == http.StatusNoContent {
		t.Fatal("the API answered 204 for a removal that will be undone by the next restart")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (body %s)", w.Code, w.Body.String())
	}
}

// A healthy node must be entirely unaffected: this is the "before" half of the
// before/after pair, and it is what proves the fix did not turn working writes
// into failures.
func TestSetTrustLevel_HealthyStorageStillSucceeds(t *testing.T) {
	registry := NewRegistry(false, nil) // nil persistence == nothing to fail
	id, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err := registry.AddPeer(&TrustedPeer{ID: id, TrustLevel: Standard}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	if err := registry.SetTrustLevel(id, Trusted); err != nil {
		t.Fatalf("SetTrustLevel on a healthy registry must succeed, got %v", err)
	}
	if got := registry.GetTrustLevel(id); got != Trusted {
		t.Fatalf("want Trusted, got %v", got)
	}
}
