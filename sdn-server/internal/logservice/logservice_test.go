package logservice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func TestComputeEntryHash(t *testing.T) {
	hash := ComputeEntryHash(1, "OMM.fbs", "12D3KooWTestPeer", "bafyTestCID", "", 1700000000)

	// Hash should be a 64-char hex string (SHA-256)
	if len(hash) != 64 {
		t.Errorf("Expected 64-char hex hash, got %d chars: %s", len(hash), hash)
	}

	// Verify determinism
	hash2 := ComputeEntryHash(1, "OMM.fbs", "12D3KooWTestPeer", "bafyTestCID", "", 1700000000)
	if hash != hash2 {
		t.Errorf("Hash not deterministic: %s != %s", hash, hash2)
	}

	// Different inputs should produce different hashes
	hash3 := ComputeEntryHash(2, "OMM.fbs", "12D3KooWTestPeer", "bafyTestCID", hash, 1700000001)
	if hash == hash3 {
		t.Error("Different inputs produced same hash")
	}
}

func TestComputeEntryHashCanonical(t *testing.T) {
	// Verify the hash matches manual SHA-256 computation
	seq := uint64(42)
	schemaType := "CDM.fbs"
	peerID := "12D3KooWABC"
	recordCID := "bafyCID123"
	prevHash := "aabbccdd"
	ts := uint64(1700000000)

	expected := sha256.New()
	seqBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(seqBytes, seq)
	expected.Write(seqBytes)
	expected.Write([]byte(schemaType))
	expected.Write([]byte(peerID))
	expected.Write([]byte(recordCID))
	expected.Write([]byte(prevHash))
	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, ts)
	expected.Write(tsBytes)
	expectedHex := hex.EncodeToString(expected.Sum(nil))

	got := ComputeEntryHash(seq, schemaType, peerID, recordCID, prevHash, ts)
	if got != expectedHex {
		t.Errorf("Hash mismatch:\n  expected: %s\n  got:      %s", expectedHex, got)
	}
}

func TestComputePLHHash(t *testing.T) {
	hash := ComputePLHHash("OMM.fbs", "12D3KooWTestPeer", 10, "abcdef1234", 1700000000)

	if len(hash) != 64 {
		t.Errorf("Expected 64-char hex hash, got %d chars", len(hash))
	}

	// Determinism
	hash2 := ComputePLHHash("OMM.fbs", "12D3KooWTestPeer", 10, "abcdef1234", 1700000000)
	if hash != hash2 {
		t.Errorf("PLH hash not deterministic: %s != %s", hash, hash2)
	}
}

func TestVerifyChainValid(t *testing.T) {
	peerID := "12D3KooWTestPeer"
	schemaType := "OMM.fbs"

	// Build a 3-entry chain
	entries := make([]PLGInfo, 3)
	prevHash := ""
	for i := 0; i < 3; i++ {
		seq := uint64(i + 1)
		ts := uint64(1700000000 + i)
		cid := "bafyCID" + string(rune('A'+i))
		hash := ComputeEntryHash(seq, schemaType, peerID, cid, prevHash, ts)
		entries[i] = PLGInfo{
			Sequence:          seq,
			SchemaType:        schemaType,
			PublisherPeerID:   peerID,
			RecordCID:         cid,
			PreviousEntryHash: prevHash,
			EntryHash:         hash,
			Timestamp:         ts,
		}
		prevHash = hash
	}

	// Verify without signature checking
	if err := VerifyChain(entries, nil); err != nil {
		t.Errorf("Valid chain failed verification: %v", err)
	}
}

func TestVerifyChainBrokenHash(t *testing.T) {
	peerID := "12D3KooWTestPeer"
	schemaType := "OMM.fbs"

	// Build 2 entries, tamper with the hash of the first
	hash1 := ComputeEntryHash(1, schemaType, peerID, "bafyCID1", "", 1700000000)
	hash2 := ComputeEntryHash(2, schemaType, peerID, "bafyCID2", hash1, 1700000001)

	entries := []PLGInfo{
		{Sequence: 1, SchemaType: schemaType, PublisherPeerID: peerID,
			RecordCID: "bafyCID1", PreviousEntryHash: "", EntryHash: "tampered_hash_value_0000000000000000000000000000000000",
			Timestamp: 1700000000},
		{Sequence: 2, SchemaType: schemaType, PublisherPeerID: peerID,
			RecordCID: "bafyCID2", PreviousEntryHash: hash1, EntryHash: hash2,
			Timestamp: 1700000001},
	}

	err := VerifyChain(entries, nil)
	if err == nil {
		t.Error("Expected error for tampered entry hash, got nil")
	}
}

func TestVerifyChainBrokenLink(t *testing.T) {
	peerID := "12D3KooWTestPeer"
	schemaType := "OMM.fbs"

	hash1 := ComputeEntryHash(1, schemaType, peerID, "bafyCID1", "", 1700000000)
	// Entry 2 claims a wrong previous hash
	wrongPrev := "000000000000000000000000000000000000000000000000000000000000dead"
	hash2 := ComputeEntryHash(2, schemaType, peerID, "bafyCID2", wrongPrev, 1700000001)

	entries := []PLGInfo{
		{Sequence: 1, SchemaType: schemaType, PublisherPeerID: peerID,
			RecordCID: "bafyCID1", PreviousEntryHash: "", EntryHash: hash1,
			Timestamp: 1700000000},
		{Sequence: 2, SchemaType: schemaType, PublisherPeerID: peerID,
			RecordCID: "bafyCID2", PreviousEntryHash: wrongPrev, EntryHash: hash2,
			Timestamp: 1700000001},
	}

	err := VerifyChain(entries, nil)
	if err == nil {
		t.Error("Expected error for broken chain link, got nil")
	}
}

func TestVerifyChainWithSignatures(t *testing.T) {
	// Generate an Ed25519 key pair
	privKey, pubKey, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	peerID := "12D3KooWSignedPeer"
	schemaType := "CDM.fbs"

	// Build a signed 2-entry chain
	entries := make([]PLGInfo, 2)
	prevHash := ""
	for i := 0; i < 2; i++ {
		seq := uint64(i + 1)
		ts := uint64(1700000000 + i)
		cid := "bafySigned" + string(rune('A'+i))
		hash := ComputeEntryHash(seq, schemaType, peerID, cid, prevHash, ts)

		hashBytes, _ := hex.DecodeString(hash)
		sig, err := privKey.Sign(hashBytes)
		if err != nil {
			t.Fatalf("Failed to sign entry %d: %v", i, err)
		}

		entries[i] = PLGInfo{
			Sequence:          seq,
			SchemaType:        schemaType,
			PublisherPeerID:   peerID,
			RecordCID:         cid,
			PreviousEntryHash: prevHash,
			EntryHash:         hash,
			Timestamp:         ts,
			Signature:         sig,
			SignatureType:     "Ed25519",
		}
		prevHash = hash
	}

	// Valid chain with valid signatures
	if err := VerifyChain(entries, pubKey); err != nil {
		t.Errorf("Valid signed chain failed verification: %v", err)
	}

	// Tamper with a signature
	entries[0].Signature = []byte("bad-signature-bytes")
	err = VerifyChain(entries, pubKey)
	if err == nil {
		t.Error("Expected error for bad signature, got nil")
	}
}

func TestBuildPLGFlatBuffer(t *testing.T) {
	data := buildPLGFlatBuffer(
		1,
		"OMM.fbs",
		"12D3KooWTestPeer",
		"bafyTestCID",
		"",
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		1700000000,
		[]byte{1, 2, 3, 4},
		[]string{"SAT-25544", "SAT-12345"},
		"2024-01-15",
	)

	if len(data) == 0 {
		t.Error("buildPLGFlatBuffer returned empty data")
	}

	// Size-prefixed FlatBuffer: first 4 bytes are the size
	if len(data) < 8 {
		t.Errorf("PLG FlatBuffer too short: %d bytes", len(data))
	}
}

func TestBuildPLHFlatBuffer(t *testing.T) {
	data := buildPLHFlatBuffer(
		"OMM.fbs",
		"12D3KooWTestPeer",
		42,
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		1000,
		"/dns4/spaceaware.io/tcp/443",
		1700000000,
		[]byte{5, 6, 7, 8},
		"2024-01-01",
		"2024-06-15",
	)

	if len(data) == 0 {
		t.Error("buildPLHFlatBuffer returned empty data")
	}

	if len(data) < 8 {
		t.Errorf("PLH FlatBuffer too short: %d bytes", len(data))
	}
}

func TestParsePLH(t *testing.T) {
	// Build a PLH then parse it
	data := buildPLHFlatBuffer(
		"CAT.fbs",
		"12D3KooWTestPeer",
		5,
		"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		500,
		"/dns4/example.com/tcp/443",
		1700000000,
		nil,
		"2024-01-01",
		"2024-12-31",
	)

	info, err := ParsePLH(data)
	if err != nil {
		t.Fatalf("ParsePLH failed: %v", err)
	}
	if info == nil {
		t.Fatal("ParsePLH returned nil info")
	}
}

func TestVerifyChainEmpty(t *testing.T) {
	// Empty chain should verify successfully
	if err := VerifyChain(nil, nil); err != nil {
		t.Errorf("Empty chain should verify: %v", err)
	}
	if err := VerifyChain([]PLGInfo{}, nil); err != nil {
		t.Errorf("Empty slice chain should verify: %v", err)
	}
}

func TestComputeEntryHashEmptyPrevious(t *testing.T) {
	// First entry in a chain has empty previous hash
	hash := ComputeEntryHash(1, "OMM.fbs", "peer1", "cid1", "", 1000)
	if hash == "" {
		t.Error("Hash should not be empty")
	}

	// Same inputs with non-empty previous should differ
	hash2 := ComputeEntryHash(1, "OMM.fbs", "peer1", "cid1", "someprevhash", 1000)
	if hash == hash2 {
		t.Error("Hash with empty vs non-empty previous should differ")
	}
}
