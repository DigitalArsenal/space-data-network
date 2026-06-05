package channels

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestVerifySignedPNMEnvelopeRejectsUnsignedPNM(t *testing.T) {
	_, err := VerifySignedPNMEnvelope(buildVerifierTestPNM(t, verifierPNMOptions{
		CID:    "bafymanifest",
		FileID: "DPM",
	}))
	if err == nil {
		t.Fatal("expected unsigned PNM to be rejected")
	}
	if !strings.Contains(err.Error(), "SIGNATURE_TYPE") {
		t.Fatalf("error = %v, want SIGNATURE_TYPE rejection", err)
	}
}

func TestVerifySignedPNMEnvelopeAcceptsEd25519Envelope(t *testing.T) {
	signature := make([]byte, 64)
	for i := range signature {
		signature[i] = byte(i + 1)
	}
	evidence, err := VerifySignedPNMEnvelope(buildVerifierTestPNM(t, verifierPNMOptions{
		CID:           "bafymanifest",
		FileID:        "DPM",
		Signature:     hex.EncodeToString(signature),
		SignatureType: "Ed25519",
	}))
	if err != nil {
		t.Fatalf("VerifySignedPNMEnvelope failed: %v", err)
	}
	if evidence.CID != "bafymanifest" || evidence.FileID != "DPM" || evidence.SignatureType != "Ed25519" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
	if len(evidence.Signature) != 64 {
		t.Fatalf("signature length = %d, want 64", len(evidence.Signature))
	}
}

func TestVerifySignedPNMEnvelopeRejectsInvalidSignatureBytes(t *testing.T) {
	_, err := VerifySignedPNMEnvelope(buildVerifierTestPNM(t, verifierPNMOptions{
		CID:           "bafymanifest",
		FileID:        "DPM",
		Signature:     "not-hex",
		SignatureType: "Ed25519",
	}))
	if err == nil {
		t.Fatal("expected invalid signature bytes to be rejected")
	}
}

type verifierPNMOptions struct {
	CID           string
	FileID        string
	Signature     string
	SignatureType string
}

func buildVerifierTestPNM(t *testing.T, opts verifierPNMOptions) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(opts.CID)
	fileIDOffset := builder.CreateString(opts.FileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	signatureOffset := flatbuffers.UOffsetT(0)
	signatureTypeOffset := flatbuffers.UOffsetT(0)
	if opts.Signature != "" {
		signatureOffset = builder.CreateString(opts.Signature)
	}
	if opts.SignatureType != "" {
		signatureTypeOffset = builder.CreateString(opts.SignatureType)
	}

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	if signatureOffset != 0 {
		PNM.PNMAddSIGNATURE(builder, signatureOffset)
	}
	if signatureTypeOffset != 0 {
		PNM.PNMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	}
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}
