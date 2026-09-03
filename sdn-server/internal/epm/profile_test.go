package epm

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func profileTestService(t *testing.T) *Service {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("peer.Decode: %v", err)
	}
	service := NewService(nil, nil, peerID, "", t.TempDir())
	if err := service.SetRuntimeSigningKey(privateKey, "sdn/runtime-signing"); err != nil {
		t.Fatalf("SetRuntimeSigningKey: %v", err)
	}
	if err := service.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return service
}

func TestUpdateProfileFromEPMRoundTripsEditableFieldsAndResigns(t *testing.T) {
	t.Parallel()
	service := profileTestService(t)
	if err := service.UpdateProfile(&Profile{
		DN:             "Before",
		PhotoDataURL:   "data:image/png;base64,AAAA",
		SigningKeyPath: "m/44'/0'/0'/0/7",
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	proposalService := profileTestService(t)
	want := &Profile{
		DN: "Ada Lovelace", LegalName: "Augusta Ada King", JobTitle: "Analyst",
		Email: "ada@example.test", Address: &Address{Country: "GB", Locality: "London"},
	}
	if err := proposalService.UpdateProfile(want); err != nil {
		t.Fatalf("build proposal: %v", err)
	}

	if err := service.UpdateProfileFromEPM(proposalService.GetNodeEPM()); err != nil {
		t.Fatalf("UpdateProfileFromEPM: %v", err)
	}
	got := service.GetNodeProfile()
	if got.DN != want.DN || got.LegalName != want.LegalName || got.JobTitle != want.JobTitle || got.Email != want.Email {
		t.Fatalf("stored profile = %+v, want fields from %+v", got, want)
	}
	if got.Address == nil || got.Address.Country != "GB" || got.Address.Locality != "London" {
		t.Fatalf("stored address = %+v", got.Address)
	}
	if got.PhotoDataURL != "data:image/png;base64,AAAA" || got.SigningKeyPath != "m/44'/0'/0'/0/7" {
		t.Fatalf("wire-absent fields were reset: %+v", got)
	}
	if err := VerifyEPMSignature(service.GetNodeEPM()); err != nil {
		t.Fatalf("publisher signature: %v", err)
	}
}

func TestDecodeProfileEPMRejectsJSONAndMalformedSizePrefix(t *testing.T) {
	t.Parallel()
	malformed := append([]byte(nil), profileTestService(t).GetNodeEPM()...)
	malformed[0]++
	for _, data := range [][]byte{
		[]byte("\"{\\\"dn\\\":\\\"Ada\\\"}\""),
		malformed,
	} {
		if _, err := DecodeProfileEPM(data); !errors.Is(err, ErrInvalidProfileEPM) {
			t.Fatalf("DecodeProfileEPM error = %v, want ErrInvalidProfileEPM", err)
		}
	}
}
