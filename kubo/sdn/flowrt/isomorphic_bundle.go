package flowrt

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ipfs/kubo/sdn/modulert"
)

// IsomorphicNodeArtifact is one independently verified WASM member of a
// signed flow bundle. SignedArtifact is retained byte-for-byte for identity and
// audit; PortableArtifact is the trailer-stripped payload passed to a runtime.
type IsomorphicNodeArtifact struct {
	EntryID          string
	EntryHash        string
	ContentHash      string
	SignedArtifact   []byte
	PortableArtifact []byte
	Signature        modulert.ModuleSignatureStatus
}

// IsomorphicBundleMetadata is the application-blind result of verifying a
// signed flow bundle and every application/wasm member it carries.
type IsomorphicBundleMetadata struct {
	ContentHash      string
	SignedArtifact   []byte
	PortableArtifact []byte
	Signature        modulert.ModuleSignatureStatus
	Bundle           *modulert.VerifiedModuleBundle
	Nodes            []IsomorphicNodeArtifact
}

type nodeArtifactVerifier func([]byte) ([]byte, modulert.ModuleSignatureStatus, error)

// LoadIsomorphicBundleMetadata applies the same trusted-signer policy to the
// whole flow bundle and each independently carried WASM node.
func LoadIsomorphicBundleMetadata(signedArtifact []byte, trustedSigners []ed25519.PublicKey) (*IsomorphicBundleMetadata, error) {
	portable, bundle, status, err := modulert.VerifyModuleBundle(signedArtifact, trustedSigners)
	if err != nil {
		return nil, fmt.Errorf("verify signed flow bundle: %w", err)
	}
	policy := &modulert.ModuleSignaturePolicy{TrustedSigners: trustedSigners}
	return assembleIsomorphicBundleMetadata(signedArtifact, portable, bundle, status, func(member []byte) ([]byte, modulert.ModuleSignatureStatus, error) {
		return modulert.EnforceModuleSignaturePolicy(policy, member)
	})
}

func assembleIsomorphicBundleMetadata(signedArtifact, portable []byte, bundle *modulert.VerifiedModuleBundle, status modulert.ModuleSignatureStatus, verify nodeArtifactVerifier) (*IsomorphicBundleMetadata, error) {
	if bundle == nil || !status.Verified {
		return nil, fmt.Errorf("flow bundle signature is not verified")
	}
	if verify == nil {
		return nil, fmt.Errorf("node artifact verifier is required")
	}
	flowHash := sha256.Sum256(portable)
	metadata := &IsomorphicBundleMetadata{
		ContentHash:      hex.EncodeToString(flowHash[:]),
		SignedArtifact:   append([]byte(nil), signedArtifact...),
		PortableArtifact: append([]byte(nil), portable...),
		Signature:        status,
		Bundle:           bundle,
	}
	for _, entry := range bundle.Entries {
		if !isWasmMediaType(entry.MediaType) {
			continue
		}
		nodePortable, nodeStatus, err := verify(entry.Payload)
		if err != nil {
			return nil, fmt.Errorf("verify node member %q: %w", entry.EntryID, err)
		}
		if !nodeStatus.Verified {
			return nil, fmt.Errorf("verify node member %q: signature is not verified", entry.EntryID)
		}
		nodeHash := nodeStatus.ContentHash
		if nodeHash == "" {
			hash := sha256.Sum256(nodePortable)
			nodeHash = hex.EncodeToString(hash[:])
		}
		metadata.Nodes = append(metadata.Nodes, IsomorphicNodeArtifact{
			EntryID:          entry.EntryID,
			EntryHash:        entry.SHA256Hex,
			ContentHash:      nodeHash,
			SignedArtifact:   append([]byte(nil), entry.Payload...),
			PortableArtifact: append([]byte(nil), nodePortable...),
			Signature:        nodeStatus,
		})
	}
	return metadata, nil
}

func isWasmMediaType(mediaType string) bool {
	base := strings.TrimSpace(strings.ToLower(strings.SplitN(mediaType, ";", 2)[0]))
	return base == "application/wasm"
}
