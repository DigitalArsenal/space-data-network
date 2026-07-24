package flowrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
)

func TestAssembleIsomorphicBundleVerifiesEveryWasmMemberIndependently(t *testing.T) {
	firstSigned := []byte("signed-node-artifact-a")
	secondSigned := []byte("signed-node-artifact-b")
	bundle := &modulert.VerifiedModuleBundle{Entries: []modulert.VerifiedBundleEntry{
		{EntryID: "node-a", MediaType: "application/wasm", Payload: firstSigned},
		{EntryID: "opaque-descriptor", MediaType: "application/octet-stream", Payload: []byte{9, 8, 7}},
		{EntryID: "node-b", MediaType: "application/wasm", Payload: secondSigned},
	}}
	verified := make(map[string]int)
	verify := func(signed []byte) ([]byte, modulert.ModuleSignatureStatus, error) {
		verified[string(signed)]++
		portable := append([]byte("portable:"), signed...)
		hash := sha256.Sum256(portable)
		return portable, modulert.ModuleSignatureStatus{
			Signed:         true,
			Verified:       true,
			SignatureScope: "bundle",
			ContentHash:    hex.EncodeToString(hash[:]),
		}, nil
	}

	metadata, err := assembleIsomorphicBundleMetadata(
		[]byte("exact-signed-flow-bundle"),
		[]byte("portable-flow-runtime"),
		bundle,
		modulert.ModuleSignatureStatus{Signed: true, Verified: true, SignatureScope: "bundle"},
		verify,
	)
	if err != nil {
		t.Fatalf("assembleIsomorphicBundleMetadata() error = %v", err)
	}
	if len(metadata.Nodes) != 2 {
		t.Fatalf("node count = %d, want 2", len(metadata.Nodes))
	}
	if !bytes.Equal(metadata.SignedArtifact, []byte("exact-signed-flow-bundle")) {
		t.Fatalf("signed parent bytes changed or were discarded: %q", metadata.SignedArtifact)
	}
	if verified[string(firstSigned)] != 1 || verified[string(secondSigned)] != 1 {
		t.Fatalf("independent verifier calls = %#v, want exactly one per node member", verified)
	}
	for i, node := range metadata.Nodes {
		wantID := fmt.Sprintf("node-%c", 'a'+rune(i))
		if node.EntryID != wantID {
			t.Fatalf("node %d entry id = %q, want %q", i, node.EntryID, wantID)
		}
		wantSigned := firstSigned
		if i == 1 {
			wantSigned = secondSigned
		}
		if !bytes.Equal(node.SignedArtifact, wantSigned) {
			t.Fatalf("node %s signed bytes changed: %q", node.EntryID, node.SignedArtifact)
		}
		if node.ContentHash == "" || !node.Signature.Verified {
			t.Fatalf("node %s metadata = %+v", node.EntryID, node)
		}
	}
}

func TestAssembleIsomorphicBundleRejectsUnsignedNodeBeforeReturningMetadata(t *testing.T) {
	bundle := &modulert.VerifiedModuleBundle{Entries: []modulert.VerifiedBundleEntry{
		{EntryID: "node-a", MediaType: "application/wasm", Payload: []byte("unsigned")},
	}}
	verify := func([]byte) ([]byte, modulert.ModuleSignatureStatus, error) {
		return nil, modulert.ModuleSignatureStatus{}, fmt.Errorf("signature required")
	}

	metadata, err := assembleIsomorphicBundleMetadata(
		[]byte("exact-signed-flow-bundle"),
		[]byte("portable-flow-runtime"),
		bundle,
		modulert.ModuleSignatureStatus{Signed: true, Verified: true},
		verify,
	)
	if err == nil || metadata != nil {
		t.Fatalf("unsigned member result = (%+v, %v), want nil metadata and rejection", metadata, err)
	}
}
