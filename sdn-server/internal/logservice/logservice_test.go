package logservice

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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
		sds.SignatureTypeEd25519,
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
		sds.SignatureTypeEd25519,
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
		sds.SignatureTypeEd25519,
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

// parsePLGForTest reads the fields buildPLGFlatBuffer writes back out of a
// stored, size-prefixed PLG buffer.
func parsePLGForTest(t *testing.T, data []byte) PLGInfo {
	t.Helper()
	if len(data) < 8 {
		t.Fatalf("PLG buffer too short: %d bytes", len(data))
	}
	buf := data[flatbuffers.SizeUint32:]
	tab := &flatbuffers.Table{Bytes: buf, Pos: flatbuffers.GetUOffsetT(buf)}
	str := func(slot flatbuffers.VOffsetT) string {
		if o := flatbuffers.UOffsetT(tab.Offset(slot)); o != 0 {
			return string(tab.ByteVector(o + tab.Pos))
		}
		return ""
	}
	u64 := func(slot flatbuffers.VOffsetT) uint64 {
		if o := flatbuffers.UOffsetT(tab.Offset(slot)); o != 0 {
			return tab.GetUint64(o + tab.Pos)
		}
		return 0
	}
	field := func(i int) flatbuffers.VOffsetT { return flatbuffers.VOffsetT(4 + 2*i) }
	return PLGInfo{
		Sequence:          u64(field(0)),
		SchemaType:        str(field(1)),
		PublisherPeerID:   str(field(2)),
		RecordCID:         str(field(3)),
		PreviousEntryHash: str(field(4)),
		EntryHash:         str(field(5)),
		Timestamp:         u64(field(6)),
		Signature:         []byte(str(field(7))),
		SignatureType:     str(field(8)),
	}
}

// entryPreimageForTest is the canonical PLG entry content, assembled here
// independently of ComputeEntryHash, so the test checks the signature against
// the specification and not against the code under test.
func entryPreimageForTest(e PLGInfo) []byte {
	var out []byte
	out = binary.LittleEndian.AppendUint64(out, e.Sequence)
	out = append(out, e.SchemaType...)
	out = append(out, e.PublisherPeerID...)
	out = append(out, e.RecordCID...)
	out = append(out, e.PreviousEntryHash...)
	out = binary.LittleEndian.AppendUint64(out, e.Timestamp)
	return out
}

// A secp256k1 publisher's log signatures are ECDSA over SHA-256 of the
// canonical entry, once (owner 2026-10-14: a single signature, no legacy
// double sign). The service appends real entries to a real store; what comes
// back out is checked by decred's verifier over sha256(preimage) and by
// VerifyChain, and a libp2p Sign(entryHash) signature — ECDSA over
// SHA-256(SHA-256(preimage)) — is refused.
func TestSecp256k1LogSignaturesHashOnce(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	priv, pub, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const peer = "16Uiu2HAmSecp256k1LogPeer"
	svc := NewService(store, priv, peer)
	for _, cid := range []string{"bafy-secp-1", "bafy-secp-2", "bafy-secp-3"} {
		if _, _, err := svc.AppendEntry("OMM.fbs", cid, nil, ""); err != nil {
			t.Fatalf("AppendEntry(%s): %v", cid, err)
		}
	}
	raw, err := store.QueryLogEntries(peer, "OMM.fbs", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 {
		t.Fatalf("stored PLG entries = %d, want 3", len(raw))
	}
	entries := make([]PLGInfo, len(raw))
	for i, data := range raw {
		entries[i] = parsePLGForTest(t, data)
	}

	rawPub, err := pub.Raw()
	if err != nil {
		t.Fatal(err)
	}
	decredPub, err := secp256k1.ParsePubKey(rawPub)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.SignatureType != sds.SignatureTypeSecp256k1 {
			t.Fatalf("seq %d SIGNATURE_TYPE = %q, want %q", e.Sequence, e.SignatureType, sds.SignatureTypeSecp256k1)
		}
		digest := sha256.Sum256(entryPreimageForTest(e))
		if hex.EncodeToString(digest[:]) != e.EntryHash {
			t.Fatalf("seq %d ENTRY_HASH is not sha256 of the canonical entry", e.Sequence)
		}
		sig, err := ecdsa.ParseDERSignature(e.Signature)
		if err != nil {
			t.Fatalf("seq %d signature is not strict DER: %v", e.Sequence, err)
		}
		if !sig.Verify(digest[:], decredPub) {
			t.Fatalf("seq %d: signature is not ECDSA over sha256(entry) once", e.Sequence)
		}
		twice := sha256.Sum256(digest[:])
		if sig.Verify(twice[:], decredPub) {
			t.Fatalf("seq %d: signature verifies over the double hash", e.Sequence)
		}
	}
	if err := VerifyChain(entries, pub); err != nil {
		t.Fatalf("VerifyChain on the stored secp256k1 log: %v", err)
	}

	// The legacy double hash: libp2p's secp256k1 Sign hashes its input, so
	// Sign(entryHash) is ECDSA over SHA-256(SHA-256(entry)). It must fail.
	legacy := append([]PLGInfo(nil), entries...)
	hashBytes, _ := hex.DecodeString(legacy[1].EntryHash)
	legacySig, err := priv.Sign(hashBytes)
	if err != nil {
		t.Fatal(err)
	}
	legacy[1].Signature = legacySig
	if err := VerifyChain(legacy, pub); err == nil {
		t.Fatal("VerifyChain accepted a double-hashed secp256k1 signature")
	}

	// A record that declares Ed25519 while carrying a secp256k1 signature is
	// mislabeled, not merely unverifiable.
	mislabeled := append([]PLGInfo(nil), entries...)
	mislabeled[0].SignatureType = sds.SignatureTypeEd25519
	if err := VerifyChain(mislabeled, pub); err == nil {
		t.Fatal("VerifyChain accepted a secp256k1 signature labeled Ed25519")
	}
}

// Ed25519 signs the 32-byte entry hash as its message, as it always has: the
// stored entries say so and VerifyChain accepts them.
func TestEd25519LogSignaturesUnchanged(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	priv, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const peer = "12D3KooWEd25519LogPeer"
	svc := NewService(store, priv, peer)
	if _, err := svc.AppendEntryBatch("OMM.fbs", []string{"bafy-ed-1", "bafy-ed-2"}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.QueryLogEntries(peer, "OMM.fbs", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]PLGInfo, len(raw))
	for i, data := range raw {
		entries[i] = parsePLGForTest(t, data)
		if entries[i].SignatureType != sds.SignatureTypeEd25519 {
			t.Fatalf("SIGNATURE_TYPE = %q, want Ed25519", entries[i].SignatureType)
		}
		rawPub, _ := pub.Raw()
		hash, _ := hex.DecodeString(entries[i].EntryHash)
		if !ed25519.Verify(ed25519.PublicKey(rawPub), hash, entries[i].Signature) {
			t.Fatalf("seq %d: Ed25519 signature is not over the entry hash", entries[i].Sequence)
		}
	}
	if err := VerifyChain(entries, pub); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
}
