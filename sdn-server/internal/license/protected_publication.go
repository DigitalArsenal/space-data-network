package license

// SDS protected-publication artifacts.
//
// Every artifact the module-SDK publication lane writes is
//
//	ciphertext || <symmetric trailer> || REC(flatbuffer) || uint32le(REC len) || "$REC"
//
// i.e. the payload with an SDS $REC record collection appended and a fixed
// 8-byte footer. The record collection carries an $ENC record describing how
// the payload was sealed (ephemeral X25519 public key, nonce, KDF context) and
// optionally $MBL / $PNM records describing the bundle and its publication.
//
// The recipient key is NOT the node identity. Each artifact is sealed to the
// per-plugin X25519 private key stored beside it as `bundle.key`, which the
// registry already reads for the grant lane (ReadBundleKey). Key resolution
// therefore lives here, in the registry that owns the directory, rather than in
// the four callers of DecryptBundle.
//
// The canonical implementation this mirrors is
// space-data-module-sdk/src/transport/pki.js :: decryptProtectedBytes. The one
// byte-exact obligation is the AAD: the GCM AAD is the $ENC record RE-ENCODED
// as a standalone $ENC buffer, so encodeENCRecord below must reproduce the JS
// builder's output bit for bit. protected_publication_test.go pins that against
// real artifacts pulled off the delivery node.
//
// ORDINAL WARNING (Janus, binding, 2026-08-07): the RecordType union ordinals
// are NOT wire-stable — they have renumbered at least three times, and the
// 2026-07-10 generation of artifacts on the delivery node carries MBL=67
// ENC=34 PNM=98 against today's very different numbering. The only stable
// discriminator is Record.standard, the string. Nothing here reads value_type.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	encsds "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// The REC record collection is walked with the raw flatbuffers table API rather
// than the generated $REC package, for two independent reasons:
//
//  1. The generated package DOES NOT COMPILE at the pin this repo vendors
//     (lib/go v1.177.0): REC/PRW.go, REC/SDL.go and REC/SPP.go each redeclare a
//     method. That is the Go-side twin of the C++ $CES `MAX` enum collision
//     Janus hit, and it is filed as sds-go-rec-package-duplicate-methods. The
//     node cannot import $REC today even if it wanted to.
//  2. Walking the table directly means nothing here can accidentally start
//     depending on a union ordinal — the exact drift JANUS's ABI ruling
//     forbids. Only `standard` and the union's value offset are read.
//
// Field slots, from the generated accessors (vtable offsets 4, 6, 8 == slots
// 0, 1, 2): REC { version:string, RECORDS:[Record] };
// Record { value_type:union tag, value:union table, standard:string }.
const (
	recVTOffsetRecords    = flatbuffers.VOffsetT(6)
	recordVTOffsetValue   = flatbuffers.VOffsetT(6)
	recordVTOffsetStandrd = flatbuffers.VOffsetT(8)
)

const (
	// publicationTrailerMagic is the 4-byte footer sentinel. It is the SDS $REC
	// file identifier reused as a trailer marker.
	publicationTrailerMagic = "$REC"
	// publicationFooterLength is uint32le(record collection length) + magic.
	publicationFooterLength = 8
	// publicationGCMTagLength — the ENC schema has no tag field, so the GCM tag
	// travels appended to the ciphertext.
	publicationGCMTagLength = 16

	// encStandard is the Record.standard string that selects the $ENC record.
	encStandard = "ENC"

	// Symmetric algorithm ordinals.
	//
	// The generated SDS enum (Go lib v1.177.0 AND JS 1.178.0) publishes only
	// AES_256_CTR = 0; AES_256_GCM = 1 is on the wire but not yet in the
	// schema's SymmetricAlgo enum. The module SDK carries the same local
	// constant (src/transport/records.js SYMMETRIC_ALGO_AES_256_GCM = 1). Both
	// values are handled here because both exist on the delivery node: the
	// 2026-07-10 generation is CTR, everything published since is GCM.
	// Filed for Themis as sds-enc-symmetric-algo-missing-gcm.
	symmetricAES256CTR = int8(0)
	symmetricAES256GCM = int8(1)

	keyExchangeX25519 = int8(0)
	kdfHKDFSHA256     = int8(0)
)

// publicationENC is the decoded $ENC record. Byte fields keep their "absent vs
// empty" distinction only where the encoder cares: the JS encoder always emits
// the four byte-vector fields (empty when absent) and omits the two string
// fields when they are empty, so that is what encodeENCRecord reproduces.
type publicationENC struct {
	Version            byte
	KeyExchange        int8
	Symmetric          int8
	KeyDerivation      int8
	EphemeralPublicKey []byte
	NonceStart         []byte
	RecipientKeyID     []byte
	Context            string
	SchemaHash         []byte
	RootType           string
	Timestamp          uint64
}

// hasPublicationTrailer reports whether data ends in a well-formed SDS $REC
// footer. It checks the magic only; parsePublicationTrailer does the bounds and
// flatbuffer validation, so a truncated or bogus trailer is a hard error rather
// than a silent fall-through to a legacy branch that would MAC the wrong bytes.
func hasPublicationTrailer(data []byte) bool {
	if len(data) < publicationFooterLength {
		return false
	}
	return string(data[len(data)-4:]) == publicationTrailerMagic
}

// parsePublicationTrailer splits data into the sealed payload and the REC
// record-collection bytes.
func parsePublicationTrailer(data []byte) (payload, recordCollection []byte, err error) {
	if !hasPublicationTrailer(data) {
		return nil, nil, errors.New("no $REC publication trailer")
	}
	footerAt := len(data) - publicationFooterLength
	recLen := binary.LittleEndian.Uint32(data[footerAt : footerAt+4])
	if uint64(recLen) > uint64(footerAt) {
		return nil, nil, fmt.Errorf("publication trailer length %d exceeds artifact body %d", recLen, footerAt)
	}
	start := footerAt - int(recLen)
	recordCollection = data[start:footerAt]
	if !flatbuffers.BufferHasIdentifier(recordCollection, publicationTrailerMagic) {
		return nil, nil, errors.New("publication trailer is not a $REC buffer")
	}
	return data[:start], recordCollection, nil
}

// rootTable opens the root table of a finished flatbuffer.
func rootTable(buf []byte) (flatbuffers.Table, error) {
	if len(buf) < 8 {
		return flatbuffers.Table{}, errors.New("flatbuffer is too short to hold a root table")
	}
	pos := flatbuffers.UOffsetT(flatbuffers.GetUOffsetT(buf))
	if uint64(pos) >= uint64(len(buf)) {
		return flatbuffers.Table{}, errors.New("flatbuffer root offset is out of range")
	}
	return flatbuffers.Table{Bytes: buf, Pos: pos}, nil
}

// selectENCRecord finds the $ENC record in a REC collection BY STANDARD STRING.
// Never by union ordinal: see the ORDINAL WARNING at the top of this file.
func selectENCRecord(recordCollection []byte) (*publicationENC, error) {
	collection, err := rootTable(recordCollection)
	if err != nil {
		return nil, err
	}
	recordsOffset := flatbuffers.UOffsetT(collection.Offset(recVTOffsetRecords))
	if recordsOffset == 0 {
		return nil, errors.New("publication trailer carries no records")
	}
	count := collection.VectorLen(recordsOffset)
	vectorStart := collection.Vector(recordsOffset)

	for i := 0; i < count; i++ {
		recordPos := collection.Indirect(vectorStart + flatbuffers.UOffsetT(i)*4)
		record := flatbuffers.Table{Bytes: collection.Bytes, Pos: recordPos}

		standardOffset := flatbuffers.UOffsetT(record.Offset(recordVTOffsetStandrd))
		if standardOffset == 0 || string(record.ByteVector(standardOffset+record.Pos)) != encStandard {
			continue
		}
		valueOffset := flatbuffers.UOffsetT(record.Offset(recordVTOffsetValue))
		if valueOffset == 0 {
			return nil, errors.New("$ENC record has no value table")
		}
		var table flatbuffers.Table
		record.Union(&table, valueOffset)

		var enc encsds.ENC
		enc.Init(table.Bytes, table.Pos)
		return &publicationENC{
			Version:            enc.VERSION(),
			KeyExchange:        int8(enc.KEY_EXCHANGE()),
			Symmetric:          int8(enc.SYMMETRIC()),
			KeyDerivation:      int8(enc.KEY_DERIVATION()),
			EphemeralPublicKey: append([]byte(nil), enc.EPHEMERAL_PUBLIC_KEYBytes()...),
			NonceStart:         append([]byte(nil), enc.NONCE_STARTBytes()...),
			RecipientKeyID:     append([]byte(nil), enc.RECIPIENT_KEY_IDBytes()...),
			Context:            string(enc.CONTEXT()),
			SchemaHash:         append([]byte(nil), enc.SCHEMA_HASHBytes()...),
			RootType:           string(enc.ROOT_TYPE()),
			Timestamp:          enc.TIMESTAMP(),
		}, nil
	}
	return nil, fmt.Errorf("publication trailer carries no %q record", encStandard)
}

// encodeENCRecord re-serializes the $ENC record as a standalone $ENC buffer.
//
// This is the GCM AAD, so it must be byte-identical to the JS SDK's
// encodeEncRecord(). The field-creation order below (four byte vectors and two
// strings, then the table in field order) mirrors ENCT.pack() exactly; the
// scalar defaults mirror the generated adders (VERSION defaults to 1, the three
// enums to 0), which is why a VERSION of 1 and an X25519/HKDF_SHA256 record
// omit those slots on both sides.
func encodeENCRecord(rec *publicationENC) []byte {
	b := flatbuffers.NewBuilder(256)

	// The byte vectors are ALWAYS emitted, empty ones included: the JS encoder
	// normalizes an absent vector to [] and still creates it, so an omitted
	// slot here would change the vtable and break the AAD.
	ephemeralOffset := b.CreateByteVector(rec.EphemeralPublicKey)
	nonceOffset := b.CreateByteVector(rec.NonceStart)
	recipientKeyIDOffset := b.CreateByteVector(rec.RecipientKeyID)
	var contextOffset flatbuffers.UOffsetT
	if rec.Context != "" {
		contextOffset = b.CreateString(rec.Context)
	}
	schemaHashOffset := b.CreateByteVector(rec.SchemaHash)
	var rootTypeOffset flatbuffers.UOffsetT
	if rec.RootType != "" {
		rootTypeOffset = b.CreateString(rec.RootType)
	}

	encsds.ENCStart(b)
	b.PrependByteSlot(0, rec.Version, 1)
	b.PrependInt8Slot(1, rec.KeyExchange, 0)
	b.PrependInt8Slot(2, rec.Symmetric, 0)
	b.PrependInt8Slot(3, rec.KeyDerivation, 0)
	b.PrependUOffsetTSlot(4, ephemeralOffset, 0)
	b.PrependUOffsetTSlot(5, nonceOffset, 0)
	b.PrependUOffsetTSlot(6, recipientKeyIDOffset, 0)
	b.PrependUOffsetTSlot(7, contextOffset, 0)
	b.PrependUOffsetTSlot(8, schemaHashOffset, 0)
	b.PrependUOffsetTSlot(9, rootTypeOffset, 0)
	b.PrependUint64Slot(10, rec.Timestamp, 0)
	root := encsds.ENCEnd(b)
	encsds.FinishENCBuffer(b, root)

	return append([]byte(nil), b.FinishedBytes()...)
}

// derivePublicationKey is HKDF-SHA256 with an EMPTY salt and the ENC context
// string as info. Note this is deliberately NOT derivePluginBundleKey, which
// uses a 32-zero-byte salt and the fixed "orbpro-plugin-v1" info — the two
// schedules are different and conflating them is what produced three weeks of
// "HMAC verification failed" on artifacts that were never V1.
func derivePublicationKey(sharedSecret []byte, context string) ([]byte, error) {
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, sharedSecret, nil, []byte(context))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("hkdf read: %w", err)
	}
	return key, nil
}

// decryptProtectedPublication opens an SDS protected-publication artifact with
// the recipient's 32-byte X25519 private key.
func decryptProtectedPublication(data []byte, recipientPrivateKey []byte) ([]byte, error) {
	if len(recipientPrivateKey) != 32 {
		return nil, fmt.Errorf("recipient key must be 32 bytes, got %d", len(recipientPrivateKey))
	}
	payload, recordCollection, err := parsePublicationTrailer(data)
	if err != nil {
		return nil, err
	}
	rec, err := selectENCRecord(recordCollection)
	if err != nil {
		return nil, err
	}
	if rec.KeyExchange != keyExchangeX25519 {
		return nil, fmt.Errorf("unsupported $ENC key exchange %d (only X25519 is supported)", rec.KeyExchange)
	}
	if rec.KeyDerivation != kdfHKDFSHA256 {
		return nil, fmt.Errorf("unsupported $ENC key derivation %d (only HKDF_SHA256 is supported)", rec.KeyDerivation)
	}
	if len(rec.EphemeralPublicKey) != 32 {
		return nil, fmt.Errorf("$ENC ephemeral public key must be 32 bytes, got %d", len(rec.EphemeralPublicKey))
	}
	if len(rec.NonceStart) != 12 {
		return nil, fmt.Errorf("$ENC nonce must be 12 bytes, got %d", len(rec.NonceStart))
	}

	sharedSecret, err := curve25519.X25519(recipientPrivateKey, rec.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}
	defer zeroBytes(sharedSecret)

	symmetricKey, err := derivePublicationKey(sharedSecret, rec.Context)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(symmetricKey)

	block, err := aes.NewCipher(symmetricKey)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	switch rec.Symmetric {
	case symmetricAES256GCM:
		if len(payload) < publicationGCMTagLength {
			return nil, errors.New("AES-256-GCM protected payload is truncated")
		}
		aead, err := cipher.NewGCMWithNonceSize(block, len(rec.NonceStart))
		if err != nil {
			return nil, fmt.Errorf("create AES-GCM: %w", err)
		}
		// Go's Open expects ciphertext||tag, which is exactly the wire layout.
		plaintext, err := aead.Open(nil, rec.NonceStart, payload, encodeENCRecord(rec))
		if err != nil {
			return nil, fmt.Errorf("AES-256-GCM authentication failed: %w", err)
		}
		return plaintext, nil

	case symmetricAES256CTR:
		// The 2026-07-10 generation. No tag: the whole payload is ciphertext,
		// and the CTR IV is the 12-byte nonce left-aligned in a 16-byte block
		// with a zero counter. Unauthenticated by construction — the $MBL/$PNM
		// records and the delivery lane's CID hash-validation are what bind
		// these bytes, which is why the format moved to GCM.
		iv := make([]byte, aes.BlockSize)
		copy(iv, rec.NonceStart)
		plaintext := make([]byte, len(payload))
		cipher.NewCTR(block, iv).XORKeyStream(plaintext, payload)
		return plaintext, nil

	default:
		return nil, fmt.Errorf("unsupported $ENC symmetric algorithm %d", rec.Symmetric)
	}
}

// DecryptProtectedPublication opens the SDK REC/ENC publication envelope. The
// caller must authenticate the publication and authorize the recipient key.
func DecryptProtectedPublication(data, recipientPrivateKey []byte) (plain []byte, err error) {
	defer func() {
		if recover() != nil {
			plain = nil
			err = errors.New("malformed protected publication")
		}
	}()
	return decryptProtectedPublication(data, recipientPrivateKey)
}
