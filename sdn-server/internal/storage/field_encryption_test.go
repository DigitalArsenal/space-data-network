package storage

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func x25519PublicKeyForTest(priv []byte) ([]byte, error) {
	return curve25519.X25519(priv, curve25519.Basepoint)
}

func wrapOptionsForTest() ecies.WrapOptions {
	return ecies.WrapOptions{KeyExchange: ecies.X25519, Context: fieldEncryptionContext}
}

// buildKMFRecordForTest constructs a fully populated $KMF FlatBuffers record
// (mirrors internal/encfield's own test helper) so storage-level tests can
// confirm fields other than KEY_BYTES survive the round trip untouched.
func buildKMFRecordForTest(t *testing.T, keyID string, keyBytes []byte, version uint32) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	keyIDOff := b.CreateString(keyID)
	keyBytesOff := b.CreateByteVector(keyBytes)
	kmf.KMFStart(b)
	kmf.KMFAddKEY_ID(b, keyIDOff)
	kmf.KMFAddROLE(b, 2)
	kmf.KMFAddALGORITHM(b, 5)
	kmf.KMFAddKEY_BYTES(b, keyBytesOff)
	kmf.KMFAddVERSION(b, version)
	root := kmf.KMFEnd(b)
	kmf.FinishKMFBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func newFieldEncryptionTestStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("sds.NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestStoreEncryptsKMFKeyBytesAtRest is the storage-level proof case: writing
// a $KMF record through the ordinary public Store API leaves the plaintext
// KEY_BYTES value out of every durable byte on disk, while Get transparently
// returns the exact original record for this node (the default recipient).
func TestStoreEncryptsKMFKeyBytesAtRest(t *testing.T) {
	store := newFieldEncryptionTestStore(t)

	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecordForTest(t, "at-rest-key", plainKey, 3)

	cid, err := store.Store("KMF.fbs", original, "peer-1", nil)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	// The durable stream file backing KMF.fbs must not contain the plaintext
	// key bytes anywhere -- this is the "stored form is ciphertext" check.
	streamFile := filepath.Join(store.basePath, flatSQLStreamDirName, "KMF.flatsql")
	onDisk, err := os.ReadFile(streamFile)
	if err != nil {
		t.Fatalf("read KMF stream file: %v", err)
	}
	if bytes.Contains(onDisk, plainKey) {
		t.Fatal("plaintext KEY_BYTES found in the on-disk FlatSQL stream file")
	}
	if !bytes.Contains(onDisk, []byte(encfieldMagicForTest)) {
		t.Fatal("on-disk stream frame is missing the encfield seal marker; record was not sealed")
	}

	// Get (the public read API) must transparently return the exact original
	// plaintext record: same bytes, same field values, same capitalization.
	got, err := store.Get("KMF.fbs", cid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("Get did not transparently decrypt:\n got  %x\n want %x", got, original)
	}
	rec := kmf.GetRootAsKMF(got, 0)
	if string(rec.KEY_ID()) != "at-rest-key" {
		t.Fatalf("KEY_ID after Get = %q, want %q", rec.KEY_ID(), "at-rest-key")
	}
	if !bytes.Equal(rec.KEY_BYTESBytes(), plainKey) {
		t.Fatalf("KEY_BYTES after Get = %x, want %x", rec.KEY_BYTESBytes(), plainKey)
	}
	if rec.VERSION() != 3 {
		t.Fatalf("VERSION after Get = %d, want 3", rec.VERSION())
	}

	// GetRecord/QueryAll flow through the same readFlatSQLStreamRecord choke
	// point; confirm they also transparently decrypt.
	record, err := store.GetRecord("KMF.fbs", cid)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if !bytes.Equal(record.Data, original) {
		t.Fatalf("GetRecord did not transparently decrypt:\n got  %x\n want %x", record.Data, original)
	}
	all, err := store.QueryAll("KMF.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(all) != 1 || !bytes.Equal(all[0], original) {
		t.Fatalf("QueryAll did not transparently decrypt: %v", all)
	}
}

// encfieldMagicForTest mirrors encfield's unexported v1 frame magic so this
// test can assert the on-disk bytes were actually sealed, without exporting
// the magic constant purely for test use.
const encfieldMagicForTest = "SDF1"

// TestStoreDoesNotSealNonEncryptedSchema confirms schemas with no registered
// `(encrypted)` field (the ~149/150 majority) are written byte-identical to
// today's behavior: no magic prefix, no envelope overhead.
func TestStoreDoesNotSealNonEncryptedSchema(t *testing.T) {
	store := newFieldEncryptionTestStore(t)

	data := sds.NewOMMBuilder().WithNoradCatID(42).WithObjectName("UNENCRYPTED").Build()
	cid, err := store.Store("OMM.fbs", data, "peer-1", nil)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	streamFile := filepath.Join(store.basePath, flatSQLStreamDirName, "OMM.flatsql")
	onDisk, err := os.ReadFile(streamFile)
	if err != nil {
		t.Fatalf("read OMM stream file: %v", err)
	}
	if bytes.Contains(onDisk, []byte(encfieldMagicForTest)) {
		t.Fatal("non-encrypted schema's stream frame was sealed")
	}

	got, err := store.Get("OMM.fbs", cid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("non-encrypted schema round trip changed the bytes")
	}
}

// TestStoreFieldEncryptionIdentityPersistsAcrossReopen confirms the
// lazily-provisioned default-recipient keypair is durable: closing and
// reopening the same store must still be able to decrypt records written
// before the restart.
func TestStoreFieldEncryptionIdentityPersistsAcrossReopen(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("sds.NewValidator: %v", err)
	}

	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecordForTest(t, "reopen-key", plainKey, 1)

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	cid, err := store.Store("KMF.fbs", original, "peer-1", nil)
	if err != nil {
		store.Close()
		t.Fatalf("Store: %v", err)
	}
	_, firstPub, err := store.fieldEncryptionKeys()
	if err != nil {
		store.Close()
		t.Fatalf("fieldEncryptionKeys: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("reopen NewFlatSQLStore: %v", err)
	}
	defer reopened.Close()

	_, secondPub, err := reopened.fieldEncryptionKeys()
	if err != nil {
		t.Fatalf("fieldEncryptionKeys after reopen: %v", err)
	}
	if !bytes.Equal(firstPub, secondPub) {
		t.Fatal("field-encryption identity was not persisted across reopen (different public key)")
	}

	got, err := reopened.Get("KMF.fbs", cid)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("record did not decrypt correctly after reopen")
	}
}

// TestOpenRecordFieldsWrongIdentityFailsCleanly proves the storage-level
// decrypt path surfaces a clean error (not garbage) when the stored envelope
// cannot be opened with this store's own key -- exercised directly against
// openRecordFields using a frame sealed for a DIFFERENT recipient than the
// store's own provisioned identity.
func TestOpenRecordFieldsWrongIdentityFailsCleanly(t *testing.T) {
	store := newFieldEncryptionTestStore(t)

	// Force this store to provision its own identity first.
	if _, _, err := store.fieldEncryptionKeys(); err != nil {
		t.Fatalf("fieldEncryptionKeys: %v", err)
	}

	// Seal a record for an unrelated recipient (never registered with this
	// store), simulating a foreign/corrupted envelope.
	strangerPriv := make([]byte, 32)
	if _, err := rand.Read(strangerPriv); err != nil {
		t.Fatal(err)
	}
	plainKey := make([]byte, 32)
	if _, err := rand.Read(plainKey); err != nil {
		t.Fatal(err)
	}
	original := buildKMFRecordForTest(t, "foreign-key", plainKey, 1)

	foreignPub, err := x25519PublicKeyForTest(strangerPriv)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := encfield.Seal("KMF", original, foreignPub, wrapOptionsForTest())
	if err != nil {
		t.Fatalf("encfield.Seal: %v", err)
	}

	data, err := store.openRecordFields("KMF", sealed)
	if err == nil {
		t.Fatal("openRecordFields succeeded against a foreign envelope")
	}
	if data != nil {
		t.Fatalf("openRecordFields returned non-nil data on failure: %x", data)
	}
}
