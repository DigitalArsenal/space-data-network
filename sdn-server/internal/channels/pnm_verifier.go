package channels

import (
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
	CID           string
	FileID        string
	SignatureType string
	Signature     []byte
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
	}, nil
}
