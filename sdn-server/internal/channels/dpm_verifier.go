package channels

import (
	"errors"
	"fmt"
	"strings"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
)

var (
	ErrDPMRequired        = errors.New("DPM bytes are required")
	ErrDPMMissingIdentity = errors.New("DPM buffer missing identifier")
	ErrDPMMissingFileID   = errors.New("DPM missing FILE_ID")
	ErrDPMMissingSign     = errors.New("DPM missing provider signature")
)

type DPMTrustEvidence struct {
	FileID        string
	SignatureType string
	Signature     []byte
	ProviderPeer  string
}

func IsDPMManifest(manifestBytes []byte) bool {
	return dpm.DPMBufferHasIdentifier(manifestBytes)
}

func VerifySignedDPMManifest(manifestBytes []byte, expectedFileID string) (DPMTrustEvidence, error) {
	if len(manifestBytes) == 0 {
		return DPMTrustEvidence{}, ErrDPMRequired
	}
	if !dpm.DPMBufferHasIdentifier(manifestBytes) {
		return DPMTrustEvidence{}, ErrDPMMissingIdentity
	}
	manifest := dpm.GetRootAsDPM(manifestBytes, 0)
	fileID := strings.TrimSpace(string(manifest.FILE_ID()))
	if fileID == "" {
		return DPMTrustEvidence{}, ErrDPMMissingFileID
	}
	if strings.TrimSpace(expectedFileID) != "" && fileID != strings.TrimSpace(expectedFileID) {
		return DPMTrustEvidence{}, fmt.Errorf("DPM FILE_ID %q does not match PNM FILE_ID %q", fileID, strings.TrimSpace(expectedFileID))
	}
	signatureType := strings.TrimSpace(string(manifest.SIGNATURE_TYPE()))
	if signatureType != "Ed25519" {
		return DPMTrustEvidence{}, fmt.Errorf("DPM SIGNATURE_TYPE = %q, want Ed25519", signatureType)
	}
	signature := append([]byte(nil), manifest.PROVIDER_SIGNATUREBytes()...)
	if len(signature) == 0 {
		return DPMTrustEvidence{}, ErrDPMMissingSign
	}
	if len(signature) != 64 {
		return DPMTrustEvidence{}, fmt.Errorf("DPM provider signature length = %d, want 64", len(signature))
	}
	return DPMTrustEvidence{
		FileID:        fileID,
		SignatureType: signatureType,
		Signature:     signature,
		ProviderPeer:  strings.TrimSpace(string(manifest.PROVIDER_PEER_ID())),
	}, nil
}
