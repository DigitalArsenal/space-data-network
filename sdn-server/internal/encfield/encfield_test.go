package encfield

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

func init() {
	// Mirrors the registration internal/storage/field_encryption.go performs
	// for the real store: KMF.KEY_BYTES (field id 4) is the schema's only
	// `(encrypted)` field (internal/sds/schemas/KMF.fbs:44).
	RegisterSchema("KMF", []FieldSpec{{Name: "KEY_BYTES", FieldID: 4}})
}

func x25519Keypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

// buildKMFRecord constructs a fully populated $KMF FlatBuffers record (every
// field set, not just KEY_BYTES) so tests can confirm the OTHER fields are
// left completely untouched by field-level encryption.
func buildKMFRecord(t *testing.T, keyID string, keyBytes []byte, version uint32, expiresAt uint64) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	keyIDOff := b.CreateString(keyID)
	keyBytesOff := b.CreateByteVector(keyBytes)
	kmf.KMFStart(b)
	kmf.KMFAddKEY_ID(b, keyIDOff)
	kmf.KMFAddROLE(b, 1)
	kmf.KMFAddALGORITHM(b, 5)
	kmf.KMFAddENCODING(b, 2)
	kmf.KMFAddKEY_BYTES(b, keyBytesOff)
	kmf.KMFAddVERSION(b, version)
	kmf.KMFAddEXPIRES_AT(b, expiresAt)
	root := kmf.KMFEnd(b)
	kmf.FinishKMFBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func mustGetKMF(t *testing.T, data []byte) *kmf.KMF {
	t.Helper()
	if !kmf.KMFBufferHasIdentifier(data) {
		t.Fatalf("data is not a $KMF buffer (len=%d)", len(data))
	}
	return kmf.GetRootAsKMF(data, 0)
}

// TestSealOpenRoundTrip pins the core contract: a record with an
// `(encrypted)` field round-trips through Seal/Open back to the exact
// original plaintext bytes for the authorized recipient.
func TestSealOpenRoundTrip(t *testing.T) {
	priv, pub := x25519Keypair(t)
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "grant-key-1", plainKey, 7, 1234567890)

	sealed, err := Seal("KMF", original, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsSealed(sealed) {
		t.Fatal("sealed frame does not report IsSealed")
	}

	opened, wasSealed, err := Open("KMF", sealed, priv, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !wasSealed {
		t.Fatal("Open reported wasSealed=false for a Seal-produced frame")
	}
	if !bytes.Equal(opened, original) {
		t.Fatalf("round-trip mismatch:\n got  %x\n want %x", opened, original)
	}

	// Sanity: the recovered bytes really do decode as the original record.
	rec := mustGetKMF(t, opened)
	if string(rec.KEY_ID()) != "grant-key-1" {
		t.Fatalf("KEY_ID after Open = %q, want %q", rec.KEY_ID(), "grant-key-1")
	}
	if !bytes.Equal(rec.KEY_BYTESBytes(), plainKey) {
		t.Fatalf("KEY_BYTES after Open = %x, want %x", rec.KEY_BYTESBytes(), plainKey)
	}
}

// TestSealCiphertextNotPlaintext confirms the plaintext field value is
// genuinely absent from the stored (sealed) blob -- not just re-encoded.
func TestSealCiphertextNotPlaintext(t *testing.T) {
	_, pub := x25519Keypair(t)
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "grant-key-2", plainKey, 1, 0)

	sealed, err := Seal("KMF", original, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plainKey) {
		t.Fatal("sealed frame leaks the plaintext KEY_BYTES value")
	}

	// The KEY_BYTES field inside the sealed record itself must decode as
	// ciphertext, not the plaintext, even without unwrapping the envelope.
	rec := mustGetKMF(t, sealed[frameHeaderLenForTest(sealed):])
	if bytes.Equal(rec.KEY_BYTESBytes(), plainKey) {
		t.Fatal("KEY_BYTES field is still plaintext inside the sealed record")
	}
}

// frameHeaderLenForTest re-parses a v1 frame's header length so the test can
// hand the trailing record bytes to kmf.GetRootAsKMF directly (whitebox: same
// package as buildFrame/readChunk).
func frameHeaderLenForTest(frame []byte) int {
	if !IsSealed(frame) {
		return 0
	}
	rest := frame[5+seedTagBytes:]
	_, rest, err := readChunk(rest) // ENC chunk
	if err != nil {
		return 0
	}
	_, rest, err = readChunk(rest) // KMF chunk
	if err != nil {
		return 0
	}
	return len(frame) - len(rest)
}

// TestOpenWrongKeyFailsCleanly confirms a decrypt attempt with a wrong
// recipient key errors instead of returning garbage.
func TestOpenWrongKeyFailsCleanly(t *testing.T) {
	_, pub := x25519Keypair(t)
	wrongPriv, _ := x25519Keypair(t)
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "grant-key-3", plainKey, 2, 99)

	sealed, err := Seal("KMF", original, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	data, wasSealed, err := Open("KMF", sealed, wrongPriv, "")
	if err == nil {
		t.Fatal("Open with the wrong private key returned no error")
	}
	if !wasSealed {
		t.Fatal("Open should report wasSealed=true even on a failed decrypt")
	}
	if data != nil {
		t.Fatalf("Open with the wrong key returned non-nil data instead of failing cleanly: %x", data)
	}
}

// TestOpenUnsealedIsIdentity confirms Open is a no-op passthrough for data
// that was never Sealed (the ~149/150 schemas with no encrypted fields).
func TestOpenUnsealedIsIdentity(t *testing.T) {
	priv, _ := x25519Keypair(t)
	plain := []byte("not a sealed frame, just a plain FlatBuffers record")
	data, wasSealed, err := Open("OMM", plain, priv, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if wasSealed {
		t.Fatal("Open reported wasSealed=true for unsealed data")
	}
	if !bytes.Equal(data, plain) {
		t.Fatal("Open did not return unsealed data unchanged")
	}
}

// TestSealNoRegisteredFieldsIsIdentity confirms Seal is a safe no-op for a
// schema with no registered `(encrypted)` field (the general case).
func TestSealNoRegisteredFieldsIsIdentity(t *testing.T) {
	_, pub := x25519Keypair(t)
	plain := []byte("OMM record bytes, no encrypted fields registered")
	sealed, err := Seal("OMM", plain, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !bytes.Equal(sealed, plain) {
		t.Fatal("Seal modified data for a schema with no registered encrypted fields")
	}
	if IsSealed(sealed) {
		t.Fatal("Seal produced a sealed frame for a schema with no registered encrypted fields")
	}
}

// TestNonEncryptedFieldUntouched confirms every KMF field OTHER than
// KEY_BYTES is byte-identical before and after Seal.
func TestNonEncryptedFieldUntouched(t *testing.T) {
	_, pub := x25519Keypair(t)
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "untouched-key-id", plainKey, 42, 555)
	before := mustGetKMF(t, original)

	sealed, err := Seal("KMF", original, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	after := mustGetKMF(t, sealed[frameHeaderLenForTest(sealed):])

	if string(before.KEY_ID()) != string(after.KEY_ID()) {
		t.Fatalf("KEY_ID changed: %q -> %q", before.KEY_ID(), after.KEY_ID())
	}
	if before.ROLE() != after.ROLE() {
		t.Fatalf("ROLE changed: %v -> %v", before.ROLE(), after.ROLE())
	}
	if before.ALGORITHM() != after.ALGORITHM() {
		t.Fatalf("ALGORITHM changed: %v -> %v", before.ALGORITHM(), after.ALGORITHM())
	}
	if before.ENCODING() != after.ENCODING() {
		t.Fatalf("ENCODING changed: %v -> %v", before.ENCODING(), after.ENCODING())
	}
	if before.VERSION() != after.VERSION() {
		t.Fatalf("VERSION changed: %v -> %v", before.VERSION(), after.VERSION())
	}
	if before.EXPIRES_AT() != after.EXPIRES_AT() {
		t.Fatalf("EXPIRES_AT changed: %v -> %v", before.EXPIRES_AT(), after.EXPIRES_AT())
	}
}

// TestJSONKeyCapitalizationPreserved pins the repo-wide hard rule: SDS-record
// JSON keys must keep exact schema capitalization (KEY_BYTES, not key_bytes).
// FlatBuffers records carry no field-name bytes at all, so encryption cannot
// alter them; this test demonstrates that end-to-end by projecting the
// decrypted record into JSON with the schema's literal field names and
// checking the map keys survive unchanged.
func TestJSONKeyCapitalizationPreserved(t *testing.T) {
	priv, pub := x25519Keypair(t)
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "case-key", plainKey, 3, 44)

	sealed, err := Seal("KMF", original, pub, ecies.WrapOptions{KeyExchange: ecies.X25519})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, _, err := Open("KMF", sealed, priv, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := mustGetKMF(t, opened)

	// Project using the EXACT schema capitalization (internal/sds/schemas/KMF.fbs).
	projected := map[string]any{
		"KEY_ID":     string(rec.KEY_ID()),
		"KEY_BYTES":  rec.KEY_BYTESBytes(),
		"VERSION":    rec.VERSION(),
		"EXPIRES_AT": rec.EXPIRES_AT(),
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{"KEY_ID", "KEY_BYTES", "VERSION", "EXPIRES_AT"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("JSON round-trip lost or re-cased key %q; got keys %v", key, keysOf(decoded))
		}
	}
	for lowered := range map[string]bool{"key_id": true, "key_bytes": true} {
		if _, ok := decoded[lowered]; ok {
			t.Fatalf("JSON round-trip introduced a lower-cased key %q", lowered)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSealForRecipientsMultiRecipient: each authorized recipient can Open the
// SAME sealed record with only its own private key; a non-recipient cannot.
func TestSealForRecipientsMultiRecipient(t *testing.T) {
	aPriv, aPub := x25519Keypair(t)
	bPriv, bPub := x25519Keypair(t)
	strangerPriv, _ := x25519Keypair(t)

	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecord(t, "multi-recipient-key", plainKey, 9, 100)

	const ctx = "space-data-network/storage/field-encryption/test/v1"
	recipients := []Recipient{
		{PublicKey: aPub, KeyExchange: ecies.X25519, KeyID: []byte("recipient-a")},
		{PublicKey: bPub, KeyExchange: ecies.X25519, KeyID: []byte("recipient-b")},
	}
	sealed, err := SealForRecipients("KMF", original, recipients, ctx)
	if err != nil {
		t.Fatalf("SealForRecipients: %v", err)
	}
	if !IsSealedForRecipients(sealed) {
		t.Fatal("sealed frame does not report IsSealedForRecipients")
	}

	for _, priv := range [][]byte{aPriv, bPriv} {
		opened, wasSealed, err := OpenAny("KMF", sealed, priv, ctx)
		if err != nil {
			t.Fatalf("OpenAny: %v", err)
		}
		if !wasSealed {
			t.Fatal("OpenAny reported wasSealed=false for a SealForRecipients frame")
		}
		if !bytes.Equal(opened, original) {
			t.Fatalf("recipient round-trip mismatch:\n got  %x\n want %x", opened, original)
		}
	}

	if data, _, err := OpenAny("KMF", sealed, strangerPriv, ctx); err == nil {
		t.Fatalf("a non-recipient opened the record: %x", data)
	}
}

// TestFieldCipherMatchesDocumentedFormula pins deriveField/cryptFieldInPlace
// against the formula documented at the top of this package (and mirrored
// from internal/ecies/ecies.go's own doc comment): HKDF-SHA256(ikm=seed,
// salt=∅, info=label+be16(fieldID)+be32(recordIndex)), computed here via an
// independent, inline reference so drift from the documented spec fails this
// test even if deriveField's implementation changes.
func TestFieldCipherMatchesDocumentedFormula(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	const fieldID = uint16(4) // KMF.KEY_BYTES's field id (ecies.go's kmfKeyBytesFieldID)
	const recordIndex = uint32(0)

	wantKey := referenceDeriveField(t, seed, "flatbuffers-field", fieldID, recordIndex, 32)
	wantIV := referenceDeriveField(t, seed, "flatbuffers-iv", fieldID, recordIndex, 16)

	gotKey := deriveField(seed, "flatbuffers-field", fieldID, recordIndex, 32)
	gotIV := deriveField(seed, "flatbuffers-iv", fieldID, recordIndex, 16)
	if !bytes.Equal(gotKey, wantKey) {
		t.Fatalf("derived field key mismatch:\n got  %x\n want %x", gotKey, wantKey)
	}
	if !bytes.Equal(gotIV, wantIV) {
		t.Fatalf("derived field IV mismatch:\n got  %x\n want %x", gotIV, wantIV)
	}

	// Domain separation: a different field ID or record index must derive a
	// different key/IV (otherwise two encrypted fields in the same record
	// would reuse a CTR keystream).
	otherFieldKey := deriveField(seed, "flatbuffers-field", fieldID+1, recordIndex, 32)
	if bytes.Equal(otherFieldKey, gotKey) {
		t.Fatal("different field IDs derived the same key")
	}
	otherRecordKey := deriveField(seed, "flatbuffers-field", fieldID, recordIndex+1, 32)
	if bytes.Equal(otherRecordKey, gotKey) {
		t.Fatal("different record indexes derived the same key")
	}
}

func referenceDeriveField(t *testing.T, seed []byte, label string, fieldID uint16, recordIndex uint32, outLen int) []byte {
	t.Helper()
	info := make([]byte, 0, len(label)+6)
	info = append(info, []byte(label)...)
	var fid [2]byte
	binary.BigEndian.PutUint16(fid[:], fieldID)
	info = append(info, fid[:]...)
	var rec [4]byte
	binary.BigEndian.PutUint32(rec[:], recordIndex)
	info = append(info, rec[:]...)
	out := make([]byte, outLen)
	r := hkdf.New(sha256.New, seed, nil, info)
	if _, err := io.ReadFull(r, out); err != nil {
		t.Fatal(err)
	}
	return out
}
