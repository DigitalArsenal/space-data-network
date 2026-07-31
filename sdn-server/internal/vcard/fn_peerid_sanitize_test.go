package vcard

import (
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// Default node EPMs carry libp2p's "<peer.ID 16*abc123>" short form as DN.
// The full .vcf must sanitize it the same way the compact cards already do:
// never surface it as FN, and never emit an FN-less vCard 3.0.
func minimalEPMWithDN(dn string) []byte {
	builder := flatbuffers.NewBuilder(256)
	var dnOffset flatbuffers.UOffsetT
	if dn != "" {
		dnOffset = builder.CreateString(dn)
	}
	EPM.EPMStart(builder)
	if dn != "" {
		EPM.EPMAddDN(builder, dnOffset)
	}
	epmOffset := EPM.EPMEnd(builder)
	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte("$EPM"))
	return builder.FinishedBytes()
}

func TestEPMToVCardSanitizesPeerIDShortFormDN(t *testing.T) {
	vcardStr, err := EPMToVCard(minimalEPMWithDN("<peer.ID 16*W1Ktwi>"))
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}
	if strings.Contains(vcardStr, "peer.ID") {
		t.Errorf("FN leaked the peer.ID short form:\n%s", vcardStr)
	}
	if !strings.Contains(vcardStr, "FN:SDN Node") {
		t.Errorf("expected sanitized FN:SDN Node fallback:\n%s", vcardStr)
	}
}

func TestEPMToVCardAlwaysEmitsFN(t *testing.T) {
	vcardStr, err := EPMToVCard(minimalEPMWithDN(""))
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}
	if !strings.Contains(vcardStr, "FN:SDN Node") {
		t.Errorf("DN-less EPM must still carry FN:SDN Node:\n%s", vcardStr)
	}
}

func TestEPMToVCardKeepsRealDN(t *testing.T) {
	vcardStr, err := EPMToVCard(minimalEPMWithDN("sdn.spaceaware.io"))
	if err != nil {
		t.Fatalf("EPMToVCard failed: %v", err)
	}
	if !strings.Contains(vcardStr, "FN:sdn.spaceaware.io") {
		t.Errorf("real DN must pass through unchanged:\n%s", vcardStr)
	}
}
