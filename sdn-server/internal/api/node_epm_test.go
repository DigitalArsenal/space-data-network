package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

func apiEPMService(t *testing.T) *epm.Service {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("peer.Decode: %v", err)
	}
	service := epm.NewService(nil, nil, peerID, "", t.TempDir())
	if err := service.SetRuntimeSigningKey(privateKey, "sdn/runtime-signing"); err != nil {
		t.Fatalf("SetRuntimeSigningKey: %v", err)
	}
	if err := service.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return service
}

func TestNodeEPMHandlerFlatBufferRoundTripStoresAndResigns(t *testing.T) {
	t.Parallel()
	service := apiEPMService(t)
	proposal := apiEPMService(t)
	want := &epm.Profile{
		DN: "Flight Operations", LegalName: "Digital Arsenal", Email: "ops@example.test",
		Address: &epm.Address{Country: "US", Region: "VA"},
	}
	if err := proposal.UpdateProfile(want); err != nil {
		t.Fatalf("proposal UpdateProfile: %v", err)
	}

	var committed []byte
	handler := NewNodeEPMHandler(service, func(_ context.Context, data []byte) error {
		committed = append([]byte(nil), data...)
		return nil
	})
	request := httptest.NewRequest(http.MethodPut, "/api/node/epm", bytes.NewReader(proposal.GetNodeEPM()))
	request.Header.Set("Content-Type", epm.EPMContentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != epm.EPMContentType {
		t.Fatalf("Content-Type = %q", got)
	}
	if response.Header().Get("X-SDN-EPM-CID") == "" {
		t.Fatal("PUT returned no EPM CID")
	}
	if !bytes.Equal(response.Body.Bytes(), committed) {
		t.Fatal("response did not equal the stored publisher-signed record")
	}
	if err := epm.VerifyEPMSignature(committed); err != nil {
		t.Fatalf("stored publisher signature: %v", err)
	}
	got, err := epm.DecodeProfileEPM(committed)
	if err != nil {
		t.Fatalf("DecodeProfileEPM: %v", err)
	}
	if got.DN != want.DN || got.LegalName != want.LegalName || got.Email != want.Email {
		t.Fatalf("stored profile = %+v, want fields from %+v", got, want)
	}
	if got.Address == nil || got.Address.Country != "US" || got.Address.Region != "VA" {
		t.Fatalf("stored address = %+v", got.Address)
	}
}

func TestNodeEPMHandlerRejectsJSONStringBody(t *testing.T) {
	t.Parallel()
	handler := NewNodeEPMHandler(apiEPMService(t), func(context.Context, []byte) error {
		t.Fatal("commit called for JSON input")
		return nil
	})
	request := httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader("\"{\\\"dn\\\":\\\"Ada\\\"}\""))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("PUT JSON = %d, want 415; body %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "JSON profiles are not accepted") {
		t.Fatalf("unclear JSON rejection: %s", response.Body.String())
	}
}
