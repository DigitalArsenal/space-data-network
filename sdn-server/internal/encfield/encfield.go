// Package encfield implements SDS field-level encryption: given a FlatBuffers
// root-table record and a schema's registered `(encrypted)` fields (per the
// SDS IDL attribute — see internal/sds/schemas/KMF.fbs:44's
// `KEY_BYTES: [ubyte] (encrypted);`), it transparently encrypts those fields'
// raw bytes in place for a recipient and decrypts them back for an authorized
// reader, leaving every other byte of the record — every other field's value,
// the FlatBuffers vtable, and (at any JSON projection layer) every field
// NAME's exact schema capitalization — completely untouched. FlatBuffers
// records carry no field-name strings at all, so there is nothing for the
// codec to rename even by accident.
//
// This package does not invent new cryptography. It composes two primitives
// that already exist in this tree:
//
//  1. internal/ecies.Wrap / Unwrap — the SDS $ENC + $KMF envelope
//     (docs/UNIFIED_ECIES.md) that seals a 32-byte key for a recipient over
//     X25519 or secp256k1. Seal below generates a fresh random 32-byte
//     per-record "field seed" and calls ecies.Wrap to seal THAT seed (not the
//     field value itself, which may be any length) for the recipient; Open
//     calls ecies.Unwrap to recover the identical seed for an authorized
//     reader holding the matching private key.
//
//  2. The flatbuffers field sub-KDF documented in
//     flatbuffers/encryption.h (DeriveFieldKey/DeriveFieldIV + EncryptVector)
//     and already implemented — for the single hardcoded case of
//     KMF.KEY_BYTES (field id 4, record index 0) — as the unexported
//     deriveField/fieldCipherXOR pair in internal/ecies/ecies.go:
//
//     fieldKey = HKDF-SHA256(ikm=seed, info="flatbuffers-field"+be16(fieldID)+be32(recordIndex))
//     fieldIV  = HKDF-SHA256(ikm=seed, info="flatbuffers-iv"  +be16(fieldID)+be32(recordIndex))
//     ciphertext = AES-256-CTR(fieldKey, fieldIV) XOR plaintext
//
//     ecies.go cannot export this (it is intentionally pinned to the one KMF
//     field it wraps), so cryptFieldInPlace below re-implements the exact
//     same formula, parameterized over ANY registered field ID. This is what
//     turns the KMF.KEY_BYTES one-off into the general path: TestFieldCipher
//     pins byte-for-byte agreement with internal/ecies's derivation via a
//     shared conformance-style vector so the two never drift apart.
//
// AES-256-CTR has no authentication, so an attacker (or a caller with the
// wrong key) cannot be detected from the field ciphertext alone — Unwrap-ing
// a $KMF envelope with the wrong private key silently returns a garbage
// 32-byte seed instead of erroring (see internal/ecies's own tests). Seal
// therefore stamps a keyed seed-verification tag (HMAC-SHA256, truncated) into
// the frame; Open recomputes it after Unwrap and fails cleanly, before
// touching any field bytes, if the seed does not check out.
//
// Only `[ubyte]` vector fields are supported — KMF.KEY_BYTES's type, and the
// only `(encrypted)`-attributed field across the ~150 in-tree SDS schemas
// today. Fixed-width scalar and string fields are a documented follow-up:
// locateByteVectorField is the one function a sibling implementation would
// need (scalars: fixed offset off VOffsetT; strings: same vector shape as
// [ubyte] but usually excluding the NUL terminator FlatBuffers appends).
package encfield

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/hkdf"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// FieldSpec identifies one `(encrypted)` FlatBuffers field within a schema's
// root table by its declaration-order field ID (== its builder slot index,
// which is also the vtable slot index used to compute the field's
// VOffsetT — see locateByteVectorField). Name is documentation/error-message
// only; it is never written anywhere and never affects the wire format.
type FieldSpec struct {
	Name    string
	FieldID uint16
}

var (
	registryMu sync.RWMutex
	registry   = map[string][]FieldSpec{}
)

// RegisterSchema declares the `(encrypted)` field set for a schema (table)
// name, e.g. "KMF" (internal/sds/schemas/KMF.fbs's root_type, matching the
// table name FlatSQLStore uses — sds.SchemaNameToTable strips the ".fbs"
// suffix). Safe for concurrent use. Passing an empty/nil fields slice removes
// the schema from the registry.
func RegisterSchema(schemaName string, fields []FieldSpec) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if len(fields) == 0 {
		delete(registry, schemaName)
		return
	}
	registry[schemaName] = append([]FieldSpec(nil), fields...)
}

// EncryptedFields returns the registered `(encrypted)` fields for
// schemaName, or nil if it has none.
func EncryptedFields(schemaName string) []FieldSpec {
	registryMu.RLock()
	defer registryMu.RUnlock()
	fields := registry[schemaName]
	if len(fields) == 0 {
		return nil
	}
	return append([]FieldSpec(nil), fields...)
}

// HasEncryptedFields reports whether schemaName has any registered
// `(encrypted)` field. Storage write/read hot paths call this to skip the
// codec entirely for the ~149/150 schemas that have no encrypted fields.
func HasEncryptedFields(schemaName string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return len(registry[schemaName]) > 0
}

const (
	fieldSeedBytes = 32 // ecies content-key size (ecies.Wrap requires exactly this)
	aesKeyBytes    = 32
	ctrIVBytes     = 16
	seedTagBytes   = 16
	magicV1        = "SDF1" // "SDN Data Field-encryption, v1"
	frameVersion1  = byte(1)
)

// deriveField reproduces internal/ecies.deriveField / the flatbuffers
// EncryptVector sub-KDF exactly: HKDF-SHA256(ikm=seed, salt=∅,
// info=label+be16(fieldID)+be32(recordIndex)). See TestFieldCipher for a
// byte-for-byte cross-check against internal/ecies's own KMF.KEY_BYTES
// derivation (fieldID=4, recordIndex=0).
func deriveField(seed []byte, label string, fieldID uint16, recordIndex uint32, outLen int) []byte {
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
	_, _ = io.ReadFull(r, out)
	return out
}

// cryptFieldInPlace applies AES-256-CTR(fieldKey, fieldIV) over data in
// place. AES-CTR is its own inverse, so this is used for both encryption and
// decryption.
func cryptFieldInPlace(seed []byte, fieldID uint16, recordIndex uint32, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	key := deriveField(seed, "flatbuffers-field", fieldID, recordIndex, aesKeyBytes)
	iv := deriveField(seed, "flatbuffers-iv", fieldID, recordIndex, ctrIVBytes)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("encfield: aes cipher: %w", err)
	}
	cipher.NewCTR(block, iv).XORKeyStream(data, data)
	return nil
}

// rootTable resolves the root table position of a (non-size-prefixed)
// FlatBuffers buffer — the same `n := GetUOffsetT(buf[0:])` every generated
// GetRootAsXxx does, independent of schema.
func rootTable(buf []byte) (flatbuffers.Table, error) {
	if len(buf) < 4 {
		return flatbuffers.Table{}, errors.New("encfield: buffer too short to contain a FlatBuffers root table")
	}
	n := flatbuffers.GetUOffsetT(buf)
	if uint64(n) >= uint64(len(buf)) {
		return flatbuffers.Table{}, errors.New("encfield: invalid FlatBuffers root offset")
	}
	return flatbuffers.Table{Bytes: buf, Pos: n}, nil
}

// locateByteVectorField returns the [start,end) byte range of a `[ubyte]`
// vector field's element data within buf's root table. ok=false means the
// field is absent/unset (nothing to encrypt — the schema default, matching
// FlatBuffers' own "unset field" semantics). This uses only the generic
// flatbuffers.Table accessors (Offset/Vector/VectorLen) that every generated
// accessor (e.g. KMF.KEY_BYTESBytes()) is itself built from, so it works for
// any schema/field without per-schema generated code.
func locateByteVectorField(buf []byte, fieldID uint16) (start, end int, ok bool, err error) {
	tab, err := rootTable(buf)
	if err != nil {
		return 0, 0, false, err
	}
	vo := flatbuffers.VOffsetT((fieldID + 2) * 2)
	o := flatbuffers.UOffsetT(tab.Offset(vo))
	if o == 0 {
		return 0, 0, false, nil
	}
	dataPos := int(tab.Vector(o))
	length := tab.VectorLen(o)
	end = dataPos + length
	if dataPos < 0 || length < 0 || end > len(buf) {
		return 0, 0, false, fmt.Errorf("encfield: field %d vector range [%d,%d) out of bounds (buffer len %d)", fieldID, dataPos, end, len(buf))
	}
	return dataPos, end, true, nil
}

// cryptRecordFields XORs every registered `(encrypted)` field of schemaName
// in buf (a root-table FlatBuffers record) in place using seed. Called
// symmetrically by Seal (encrypt) and Open (decrypt). Fields that are absent
// in this particular record are silently skipped (nothing to protect).
func cryptRecordFields(schemaName string, buf []byte, seed []byte) (touched int, err error) {
	for _, f := range EncryptedFields(schemaName) {
		start, end, ok, err := locateByteVectorField(buf, f.FieldID)
		if err != nil {
			return touched, fmt.Errorf("encfield: locate %s.%s: %w", schemaName, f.Name, err)
		}
		if !ok || start == end {
			continue
		}
		if err := cryptFieldInPlace(seed, f.FieldID, 0, buf[start:end]); err != nil {
			return touched, fmt.Errorf("encfield: crypt %s.%s: %w", schemaName, f.Name, err)
		}
		touched++
	}
	return touched, nil
}

// seedTag is a keyed integrity tag over the per-record seed. AES-CTR alone
// cannot detect a wrong decryption key (there is no authentication), so
// without this an Open with the wrong recipient key would silently return
// corrupted field plaintext instead of failing. HMAC-SHA256 is a stdlib
// primitive already available; this adds no new dependency.
func seedTag(seed []byte) []byte {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte("encfield/seed-verify/v1"))
	return mac.Sum(nil)[:seedTagBytes]
}

// Seal encrypts every registered `(encrypted)` field of schemaName's record
// `data` (a root-table FlatBuffers buffer) in place for recipientPub and
// returns a self-describing frame carrying the $ENC/$KMF envelope an
// authorized reader needs to recover the per-record seed (recipient key id,
// algorithm, and nonce all live in the $ENC header — see
// internal/sds/schemas/ENC.fbs). data is never mutated; Seal always returns a
// fresh copy.
//
// If schemaName has no registered `(encrypted)` field, Seal returns a copy of
// data completely unchanged (no envelope, no overhead) — callers may call
// Seal unconditionally on every write and let the registry decide.
func Seal(schemaName string, data []byte, recipientPub []byte, opts ecies.WrapOptions) ([]byte, error) {
	if !HasEncryptedFields(schemaName) {
		return append([]byte(nil), data...), nil
	}
	buf := append([]byte(nil), data...) // Seal must never mutate the caller's slice.
	seed := make([]byte, fieldSeedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("encfield: generate field seed: %w", err)
	}
	encBytes, kmfBytes, err := ecies.Wrap(recipientPub, seed, opts)
	if err != nil {
		return nil, fmt.Errorf("encfield: wrap field seed: %w", err)
	}
	if _, err := cryptRecordFields(schemaName, buf, seed); err != nil {
		return nil, err
	}
	return buildFrame(seedTag(seed), encBytes, kmfBytes, buf), nil
}

// IsSealed reports whether frameBytes begins with an encfield v1 envelope
// (as produced by Seal). Unsealed bytes (any schema with no registered
// encrypted fields, or data predating this codec) are always left as-is.
func IsSealed(frameBytes []byte) bool {
	return len(frameBytes) >= 5 && string(frameBytes[:4]) == magicV1 && frameBytes[4] == frameVersion1
}

// Open reverses Seal. If frameBytes is not a Seal-produced frame,
// it is returned as-is (wasSealed=false) — the identity transform for every
// schema with no encrypted fields. Otherwise Open unwraps the per-record seed
// with recipientPriv (context must match the CONTEXT Seal's opts used; empty
// uses ecies.DefaultGrantContext), verifies the seed's integrity tag, and —
// only once verified — decrypts the registered fields in place, returning the
// original record bytes byte-for-byte.
//
// A wrong recipientPriv (or a corrupted envelope) fails cleanly with an error
// and returns nil data: it never returns partially- or wrongly-decrypted
// field bytes, because AES-256-CTR alone would silently "succeed" into
// garbage without the seedTag check.
func Open(schemaName string, frameBytes []byte, recipientPriv []byte, context string) (data []byte, wasSealed bool, err error) {
	if !IsSealed(frameBytes) {
		return append([]byte(nil), frameBytes...), false, nil
	}
	tag, rest, err := splitFixed(frameBytes[5:], seedTagBytes)
	if err != nil {
		return nil, true, fmt.Errorf("encfield: parse seed tag: %w", err)
	}
	encBytes, rest, err := readChunk(rest)
	if err != nil {
		return nil, true, fmt.Errorf("encfield: parse ENC chunk: %w", err)
	}
	kmfBytes, rest, err := readChunk(rest)
	if err != nil {
		return nil, true, fmt.Errorf("encfield: parse KMF chunk: %w", err)
	}
	record := append([]byte(nil), rest...)

	seed, err := ecies.Unwrap(recipientPriv, encBytes, kmfBytes, context)
	if err != nil {
		return nil, true, fmt.Errorf("encfield: unwrap field seed: %w", err)
	}
	if !hmac.Equal(seedTag(seed), tag) {
		return nil, true, errors.New("encfield: seed verification failed (wrong key or corrupted envelope)")
	}
	if _, err := cryptRecordFields(schemaName, record, seed); err != nil {
		return nil, true, err
	}
	return record, true, nil
}

// Recipient identifies one SealForRecipients target. It is a direct alias of
// ecies.Recipient (X25519 or secp256k1 public key, optional KeyID) so the
// multi-recipient path is literally the same type the storefront/channel
// delivery code already builds.
type Recipient = ecies.Recipient

const magicMultiV1 = "SDFN" // "SDN Data Field-encryption, N recipients, v1"

// SealForRecipients is the multi-recipient counterpart of Seal. It encrypts
// schemaName's `(encrypted)` fields exactly ONCE with a shared per-record
// seed, then wraps that seed independently for every recipient via
// ecies.WrapForRecipients — the existing encrypt-once/N-wrapped-keys
// primitive the storefront/channel group-delivery path already uses — so ANY
// one recipient can later call Open with only their own private key; no
// recipient needs the others' keys, and none can open another's envelope
// (ecies.WrapForRecipients already guarantees per-recipient isolation).
//
// If schemaName has no registered `(encrypted)` field, SealForRecipients
// returns a copy of data unchanged, exactly like Seal.
func SealForRecipients(schemaName string, data []byte, recipients []Recipient, ctx string) ([]byte, error) {
	if !HasEncryptedFields(schemaName) {
		return append([]byte(nil), data...), nil
	}
	buf := append([]byte(nil), data...) // never mutate the caller's slice.
	seed := make([]byte, fieldSeedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("encfield: generate field seed: %w", err)
	}
	envs, err := ecies.WrapForRecipients(seed, recipients, ctx)
	if err != nil {
		return nil, fmt.Errorf("encfield: wrap field seed for recipients: %w", err)
	}
	if _, err := cryptRecordFields(schemaName, buf, seed); err != nil {
		return nil, err
	}
	return buildMultiFrame(seedTag(seed), envs, buf), nil
}

// IsSealedForRecipients reports whether frameBytes begins with an encfield
// multi-recipient (SealForRecipients) envelope.
func IsSealedForRecipients(frameBytes []byte) bool {
	return len(frameBytes) >= 5 && string(frameBytes[:4]) == magicMultiV1 && frameBytes[4] == frameVersion1
}

// OpenAny reverses SealForRecipients for whichever recipient holds
// recipientPriv: it tries each envelope in the frame in turn (there are
// normally only a handful of recipients per record) and, for the one whose
// $ENC/$KMF unwraps AND whose seed passes the integrity tag check, decrypts
// the registered fields and returns the original record bytes. If none of the
// envelopes authorize recipientPriv, it returns a clean error — never
// partially- or wrongly-decrypted field bytes. If frameBytes is not a
// SealForRecipients frame it is returned unchanged (wasSealed=false),
// matching Open's identity-transform behavior for unsealed data.
func OpenAny(schemaName string, frameBytes []byte, recipientPriv []byte, ctx string) (data []byte, wasSealed bool, err error) {
	if !IsSealedForRecipients(frameBytes) {
		return append([]byte(nil), frameBytes...), false, nil
	}
	tag, rest, err := splitFixed(frameBytes[5:], seedTagBytes)
	if err != nil {
		return nil, true, fmt.Errorf("encfield: parse seed tag: %w", err)
	}
	if len(rest) < 2 {
		return nil, true, errors.New("encfield: truncated recipient count")
	}
	count := int(binary.LittleEndian.Uint16(rest[:2]))
	rest = rest[2:]

	type envelope struct{ enc, kmf []byte }
	envs := make([]envelope, 0, count)
	for i := 0; i < count; i++ {
		var keyID []byte
		keyID, rest, err = readChunk(rest)
		if err != nil {
			return nil, true, fmt.Errorf("encfield: parse recipient %d key id: %w", i, err)
		}
		_ = keyID // documentation-only round trip; OpenAny tries every envelope.
		var encBytes, kmfBytes []byte
		encBytes, rest, err = readChunk(rest)
		if err != nil {
			return nil, true, fmt.Errorf("encfield: parse recipient %d ENC: %w", i, err)
		}
		kmfBytes, rest, err = readChunk(rest)
		if err != nil {
			return nil, true, fmt.Errorf("encfield: parse recipient %d KMF: %w", i, err)
		}
		envs = append(envs, envelope{enc: encBytes, kmf: kmfBytes})
	}
	record := append([]byte(nil), rest...)

	for _, env := range envs {
		seed, unwrapErr := ecies.Unwrap(recipientPriv, env.enc, env.kmf, ctx)
		if unwrapErr != nil {
			continue
		}
		if !hmac.Equal(seedTag(seed), tag) {
			continue
		}
		if _, err := cryptRecordFields(schemaName, record, seed); err != nil {
			return nil, true, err
		}
		return record, true, nil
	}
	return nil, true, errors.New("encfield: no recipient envelope authorized this key (wrong key or corrupted envelope)")
}

func buildMultiFrame(tag []byte, envs []ecies.RecipientEnvelope, record []byte) []byte {
	out := make([]byte, 0, 5+len(tag)+2+len(record))
	out = append(out, magicMultiV1...)
	out = append(out, frameVersion1)
	out = append(out, tag...)
	var count [2]byte
	binary.LittleEndian.PutUint16(count[:], uint16(len(envs)))
	out = append(out, count[:]...)
	for _, env := range envs {
		out = appendChunk(out, env.KeyID)
		out = appendChunk(out, env.ENC)
		out = appendChunk(out, env.KMF)
	}
	return append(out, record...)
}

func buildFrame(tag, encBytes, kmfBytes, record []byte) []byte {
	out := make([]byte, 0, 5+len(tag)+4+len(encBytes)+4+len(kmfBytes)+len(record))
	out = append(out, magicV1...)
	out = append(out, frameVersion1)
	out = append(out, tag...)
	out = appendChunk(out, encBytes)
	out = appendChunk(out, kmfBytes)
	out = append(out, record...)
	return out
}

func appendChunk(b, chunk []byte) []byte {
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(chunk)))
	b = append(b, length[:]...)
	return append(b, chunk...)
}

func readChunk(b []byte) (chunk, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("truncated length prefix")
	}
	n := binary.LittleEndian.Uint32(b[:4])
	b = b[4:]
	if uint64(len(b)) < uint64(n) {
		return nil, nil, errors.New("truncated chunk")
	}
	return b[:n], b[n:], nil
}

func splitFixed(b []byte, n int) (head, rest []byte, err error) {
	if len(b) < n {
		return nil, nil, errors.New("truncated fixed-length field")
	}
	return b[:n], b[n:], nil
}
