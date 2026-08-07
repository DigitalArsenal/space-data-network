package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
)

// fieldEncryptionContext is the encfield/ecies domain separator for storage's
// field-encryption envelopes -- distinct from ecies.DefaultGrantContext
// (module-delivery grants) so the two purposes can never be cross-unwrapped.
const fieldEncryptionContext = "space-data-network/storage/field-encryption/v1"

// fieldEncryptionIdentityFileName is the durable sidecar file (under
// s.basePath, next to flatsql-streams/ and the record catalog journal) that
// holds this store's field-encryption keypair. It deliberately does NOT live
// in sdn_metadata/the SQL layer, and the reason has CHANGED SHAPE without going
// away.
//
// It used to be absolute: FlatSQLStore's `db` was a driver over an IN-MEMORY
// FlatSQL engine, so a raw `INSERT INTO sdn_metadata` simply vanished on
// restart. That engine is now disk-backed (flatsql_boot_state.go) and
// sdn_metadata does survive — the warm-boot mark itself lives there.
//
// But it survives CONDITIONALLY. Every recovery path in that file discards the
// control database and re-derives the whole catalog from the journals: a corrupt
// file, a compacted journal, a format bump, an engine that cannot reach a
// filesystem. Anything reconstructed by replay comes back; anything that was
// only ever an arbitrary INSERT does not. A KEYPAIR IS NOT RECOVERABLE, so it
// must not depend on a file whose documented recovery is "delete it". A plain
// JSON sidecar, written the same atomic write-then-rename way
// datastore_identity.go's writeDatastoreRegistry already does, is unconditional.
// Same for datastore_identity.go's own row, which NewFlatSQLStoreForIdentity
// re-supplies and re-records on every open rather than relying on read-back.
const fieldEncryptionIdentityFileName = "field-encryption-identity.json"

func (s *FlatSQLStore) fieldEncryptionIdentityPath() string {
	return filepath.Join(s.basePath, fieldEncryptionIdentityFileName)
}

func init() {
	// KMF.KEY_BYTES (internal/sds/schemas/KMF.fbs:44) is, today, the only
	// `(encrypted)`-attributed field across the ~150 in-tree SDS schemas --
	// this registration is what makes it flow through the general encfield
	// path (Append/readFlatSQLStreamRecord below) instead of remaining the
	// one-off internal/ecies wrap used only by the module-license-grant path.
	// Adding a future schema's encrypted field only requires one more
	// RegisterSchema call; no other storage code changes.
	encfield.RegisterSchema("KMF", []encfield.FieldSpec{{Name: "KEY_BYTES", FieldID: 4}})
}

// fieldEncryptionIdentity is the on-disk JSON shape of
// fieldEncryptionIdentityFileName.
type fieldEncryptionIdentity struct {
	PublicKey  string `json:"public_key_hex"`
	PrivateKey string `json:"private_key_hex"`
}

// fieldEncryptionKeys returns this store's X25519 field-encryption keypair,
// generating and durably persisting one (fieldEncryptionIdentityFileName,
// under this store's own basePath) the first time a writable store needs it.
// This is the DEFAULT RECIPIENT decision documented in E1: with no
// per-consumer recipient list wired in yet, every `(encrypted)` field is
// sealed to the node's own key so it round-trips transparently for this
// node; delivering individually to OTHER recipients is the documented
// follow-up (encfield.SealForRecipients / OpenAny already implement the
// multi-recipient primitive for when that plumbing lands -- see
// internal/encfield).
//
// Guarded by fieldEncMu, which is intentionally separate from s.mu: this is
// called from inside Append (holding s.mu.Lock, via storeOne/storeBatchChunk)
// and from inside readFlatSQLStreamRecord (holding s.mu.RLock or no lock at
// all, depending on caller), and sync.RWMutex is not reentrant.
func (s *FlatSQLStore) fieldEncryptionKeys() (priv, pub []byte, err error) {
	s.fieldEncMu.Lock()
	defer s.fieldEncMu.Unlock()
	if len(s.fieldEncPriv) == 32 && len(s.fieldEncPub) == 32 {
		return s.fieldEncPriv, s.fieldEncPub, nil
	}

	path := s.fieldEncryptionIdentityPath()
	raw, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		var identity fieldEncryptionIdentity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, nil, fmt.Errorf("parse field-encryption identity file %s: %w", path, err)
		}
		priv, err = hex.DecodeString(identity.PrivateKey)
		if err != nil {
			return nil, nil, fmt.Errorf("decode field-encryption private key: %w", err)
		}
		pub, err = hex.DecodeString(identity.PublicKey)
		if err != nil {
			return nil, nil, fmt.Errorf("decode field-encryption public key: %w", err)
		}
	case os.IsNotExist(readErr):
		if s.readOnly {
			return nil, nil, fmt.Errorf("field-encryption identity not yet provisioned and store is read-only: %w", ErrStoreReadOnly)
		}
		priv = make([]byte, 32)
		if _, err := rand.Read(priv); err != nil {
			return nil, nil, fmt.Errorf("generate field-encryption private key: %w", err)
		}
		pub, err = curve25519.X25519(priv, curve25519.Basepoint)
		if err != nil {
			return nil, nil, fmt.Errorf("derive field-encryption public key: %w", err)
		}
		encoded, err := json.MarshalIndent(fieldEncryptionIdentity{
			PublicKey:  hex.EncodeToString(pub),
			PrivateKey: hex.EncodeToString(priv),
		}, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("encode field-encryption identity: %w", err)
		}
		tmpPath := path + ".tmp"
		if err := os.WriteFile(tmpPath, encoded, 0600); err != nil {
			return nil, nil, fmt.Errorf("write field-encryption identity file: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return nil, nil, fmt.Errorf("finalize field-encryption identity file: %w", err)
		}
	default:
		return nil, nil, fmt.Errorf("read field-encryption identity file %s: %w", path, readErr)
	}

	s.fieldEncPriv, s.fieldEncPub = priv, pub
	return priv, pub, nil
}

// sealRecordFields encrypts schemaName's registered `(encrypted)` fields
// (encfield.HasEncryptedFields) in data for this store's own default
// recipient key, returning a self-describing frame. For any schema with no
// registered encrypted fields it returns a copy of data unchanged and never
// touches the field-encryption identity (no on-disk key material is
// provisioned unless a record actually needs it).
func (s *FlatSQLStore) sealRecordFields(schemaName string, data []byte) ([]byte, error) {
	if !encfield.HasEncryptedFields(schemaName) {
		return data, nil
	}
	_, pub, err := s.fieldEncryptionKeys()
	if err != nil {
		return nil, fmt.Errorf("field-encryption identity: %w", err)
	}
	sealed, err := encfield.Seal(schemaName, data, pub, ecies.WrapOptions{
		KeyExchange: ecies.X25519,
		Context:     fieldEncryptionContext,
	})
	if err != nil {
		return nil, fmt.Errorf("seal %s encrypted fields: %w", schemaName, err)
	}
	return sealed, nil
}

// openRecordFields decrypts a frame previously produced by sealRecordFields
// for schemaName. Bytes that were never sealed (encfield.IsSealed is false --
// true for every schema with no registered encrypted fields, which is the
// overwhelming majority) are returned completely unchanged.
func (s *FlatSQLStore) openRecordFields(schemaName string, data []byte) ([]byte, error) {
	if !encfield.IsSealed(data) {
		return data, nil
	}
	priv, _, err := s.fieldEncryptionKeys()
	if err != nil {
		return nil, fmt.Errorf("field-encryption identity: %w", err)
	}
	opened, _, err := encfield.Open(schemaName, data, priv, fieldEncryptionContext)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s encrypted fields: %w", schemaName, err)
	}
	return opened, nil
}

// streamRecordSchemaName recovers the schema (table) name a FlatSQL stream
// frame belongs to from its relative path
// ("flatsql-streams/<table>.flatsql" -- see newFlatSQLStreamAppender), which
// is all readFlatSQLStreamRecord has to go on (it is keyed by
// streamPath/streamOffset/recordLength, not schemaName). sds.SchemaNameToTable
// only strips the ".fbs" suffix, so this is exactly its inverse.
func streamRecordSchemaName(streamPath string) string {
	base := filepath.Base(streamPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
