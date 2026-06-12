package channels

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var (
	ErrDPMRequired        = errors.New("DPM bytes are required")
	ErrDPMMissingIdentity = errors.New("DPM buffer missing identifier")
	ErrDPMMissingFileID   = errors.New("DPM missing FILE_ID")
	ErrDPMMissingSign     = errors.New("DPM missing provider signature")
)

type DPMTrustEvidence struct {
	ManifestCID   string
	FileID        string
	SignatureType string
	Signature     []byte
	ProviderPeer  string
	Encrypted     bool
	ContentKeyID  string
	PolicyID      string
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
	evidence := DPMTrustEvidence{
		FileID:        fileID,
		SignatureType: signatureType,
		Signature:     signature,
		ProviderPeer:  strings.TrimSpace(string(manifest.PROVIDER_PEER_ID())),
	}
	if enc := manifest.ENCRYPTION(nil); enc != nil {
		evidence.Encrypted = enc.ENCRYPTED()
		evidence.ContentKeyID = strings.TrimSpace(string(enc.CONTENT_KEY_ID()))
		evidence.PolicyID = strings.TrimSpace(string(enc.POLICY_ID()))
	}
	return evidence, nil
}

func VerifySignedDPMManifestWithProviderKey(manifestBytes []byte, expectedFileID string, providerPublicKey ed25519.PublicKey) (DPMTrustEvidence, error) {
	if len(providerPublicKey) != ed25519.PublicKeySize {
		return DPMTrustEvidence{}, fmt.Errorf("ed25519 provider public key is required")
	}
	structuralEvidence, err := VerifySignedDPMManifest(manifestBytes, expectedFileID)
	if err != nil {
		return DPMTrustEvidence{}, err
	}
	verifiedEvidence, err := storage.VerifySignedDatasetPublicationManifest(manifestBytes, providerPublicKey)
	if err != nil {
		return DPMTrustEvidence{}, err
	}
	if strings.TrimSpace(expectedFileID) != "" && verifiedEvidence.FileID != strings.TrimSpace(expectedFileID) {
		return DPMTrustEvidence{}, fmt.Errorf("DPM FILE_ID %q does not match PNM FILE_ID %q", verifiedEvidence.FileID, strings.TrimSpace(expectedFileID))
	}
	structuralEvidence.ManifestCID = verifiedEvidence.ManifestCID
	structuralEvidence.FileID = verifiedEvidence.FileID
	structuralEvidence.SignatureType = verifiedEvidence.SignatureType
	structuralEvidence.ProviderPeer = verifiedEvidence.ProviderPeer
	structuralEvidence.Encrypted = verifiedEvidence.Encrypted
	structuralEvidence.ContentKeyID = verifiedEvidence.ContentKeyID
	structuralEvidence.PolicyID = verifiedEvidence.PolicyID
	return structuralEvidence, nil
}
