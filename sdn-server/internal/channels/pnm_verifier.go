package channels

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
)

var (
	ErrPNMRequired        = errors.New("PNM bytes are required")
	ErrPNMMissingIdentity = errors.New("PNM buffer missing identifier")
	ErrPNMMissingCID      = errors.New("PNM missing CID")
	ErrPNMMissingFileID   = errors.New("PNM missing FILE_ID")
	ErrPNMMissingSign     = errors.New("PNM missing signature")
)

type PNMTrustEvidence struct {
	CID               string
	FileID            string
	SignatureType     string
	Signature         []byte
	ProviderPublicKey []byte
	EnvelopeBytes     []byte
}

func VerifySignedPNMEnvelope(pnmBytes []byte) (PNMTrustEvidence, error) {
	if len(pnmBytes) == 0 {
		return PNMTrustEvidence{}, ErrPNMRequired
	}
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		return PNMTrustEvidence{}, ErrPNMMissingIdentity
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	cidValue := strings.TrimSpace(string(pnm.CID()))
	if cidValue == "" {
		return PNMTrustEvidence{}, ErrPNMMissingCID
	}
	fileID := strings.TrimSpace(string(pnm.FILE_ID()))
	if fileID == "" {
		return PNMTrustEvidence{}, ErrPNMMissingFileID
	}
	signatureType := strings.TrimSpace(string(pnm.SIGNATURE_TYPE()))
	if signatureType != "Ed25519" {
		return PNMTrustEvidence{}, fmt.Errorf("PNM SIGNATURE_TYPE = %q, want Ed25519", signatureType)
	}
	signatureHex := strings.TrimSpace(string(pnm.SIGNATURE()))
	if signatureHex == "" {
		return PNMTrustEvidence{}, ErrPNMMissingSign
	}
	signature, err := hex.DecodeString(signatureHex)
	if err != nil {
		return PNMTrustEvidence{}, fmt.Errorf("decode PNM signature: %w", err)
	}
	if len(signature) != 64 {
		return PNMTrustEvidence{}, fmt.Errorf("PNM signature length = %d, want 64", len(signature))
	}
	return PNMTrustEvidence{
		CID:           cidValue,
		FileID:        fileID,
		SignatureType: signatureType,
		Signature:     signature,
		EnvelopeBytes: append([]byte(nil), pnmBytes...),
	}, nil
}

func VerifySignedPNMEnvelopeWithProviderKey(pnmBytes []byte, providerPublicKey ed25519.PublicKey) (PNMTrustEvidence, error) {
	if len(providerPublicKey) != ed25519.PublicKeySize {
		return PNMTrustEvidence{}, fmt.Errorf("ed25519 provider public key is required")
	}
	evidence, err := VerifySignedPNMEnvelope(pnmBytes)
	if err != nil {
		return PNMTrustEvidence{}, err
	}
	if !ed25519.Verify(providerPublicKey, datasetPublicationPNMSignaturePayload(evidence.CID, evidence.FileID), evidence.Signature) {
		return PNMTrustEvidence{}, fmt.Errorf("invalid PNM signature")
	}
	evidence.ProviderPublicKey = append([]byte(nil), providerPublicKey...)
	return evidence, nil
}

func datasetPublicationPNMSignaturePayload(manifestCID, fileID string) []byte {
	payload := make([]byte, 0, len(manifestCID)+len(fileID)+18)
	payload = append(payload, []byte("SDN-DPM-PNM\x00")...)
	payload = append(payload, fileID...)
	payload = append(payload, 0)
	payload = append(payload, manifestCID...)
	return payload
}
