// Package addressbook is a node's Node Directory (owner 2026-10-06, named
// 2026-10-07): the contact cards the node has signed. A card is in it only
// through the node's $ABA attestation, made with its libp2p identity key and
// kept with the card, so anyone can check an entry against the node's peer ID
// alone. Every entry is public (owner 2026-10-07: "all should be visible");
// $ABA's VISIBILITY is always public in what this node signs.
package addressbook

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	standardsABA "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ABA"
	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/crypto"
	cryptopb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Visibility says who may read an entry. The values are $ABA's.
type Visibility int8

const (
	Private Visibility = 0
	Public  Visibility = 1
)

// ParseVisibility reads "public" or "private".
func ParseVisibility(s string) (Visibility, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "public":
		return Public, true
	case "private":
		return Private, true
	}
	return Private, false
}

func (v Visibility) String() string {
	if v == Public {
		return "public"
	}
	return "private"
}

// algorithmSecp256k1 is libp2p's secp256k1 signature, the only kind an SDN
// identity key makes (docs/ABA.md).
const algorithmSecp256k1 = "secp256k1"

const (
	maxEntryIDBytes = 128
	maxNoteRunes    = 280
)

// Record is one signed $ABA revision of an entry.
type Record struct {
	EntryID       string
	NodePeerID    string
	PublicKey     []byte
	Algorithm     string
	Profile       []byte // the EPM exactly as attested
	ProfileSHA256 []byte
	Visibility    Visibility
	CreatedAt     uint64
	UpdatedAt     uint64
	Deleted       bool
	Note          string
	Signature     []byte
}

// sign makes r this node's statement: its identity, its literal key, and its
// signature over the canonical form.
func (r *Record) sign(key crypto.PrivKey) error {
	if key == nil || key.Type() != cryptopb.KeyType_Secp256k1 {
		return errors.New("this node's identity key cannot sign address book entries")
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return err
	}
	raw, err := key.GetPublic().Raw()
	if err != nil {
		return err
	}
	r.NodePeerID = id.String()
	r.PublicKey = raw
	r.Algorithm = algorithmSecp256k1
	r.Signature = nil
	if err := r.validate(false); err != nil {
		return err
	}
	signature, err := key.Sign(r.signingBytes())
	if err != nil {
		return err
	}
	r.Signature = signature
	return r.Verify()
}

// Verify checks r the way any reader can: the key is the one NODE_PEER_ID
// names, the signature covers the canonical form, and the profile is the
// exact EPM its digest names.
func (r Record) Verify() error {
	if err := r.validate(true); err != nil {
		return err
	}
	if r.Algorithm != algorithmSecp256k1 {
		return fmt.Errorf("unsupported signature algorithm %q", r.Algorithm)
	}
	key, err := crypto.UnmarshalSecp256k1PublicKey(r.PublicKey)
	if err != nil {
		return errors.New("PUBLIC_KEY is not a secp256k1 key")
	}
	if id, err := peer.IDFromPublicKey(key); err != nil || id.String() != r.NodePeerID {
		return errors.New("PUBLIC_KEY is not the key of NODE_PEER_ID")
	}
	if ok, err := key.Verify(r.signingBytes(), r.Signature); err != nil || !ok {
		return errors.New("SIGNATURE does not verify")
	}
	return nil
}

func (r Record) validate(requireSignature bool) error {
	switch {
	case r.EntryID == "" || len(r.EntryID) > maxEntryIDBytes || !utf8.ValidString(r.EntryID):
		return errors.New("ENTRY_ID is missing or invalid")
	case !utf8.ValidString(r.Note) || utf8.RuneCountInString(r.Note) > maxNoteRunes:
		return errors.New("NOTE is invalid or too long")
	case !utf8.ValidString(r.Algorithm):
		return errors.New("SIGNATURE_ALGORITHM is invalid")
	case len(r.PublicKey) != 33:
		return errors.New("PUBLIC_KEY must be a 33-byte compressed key")
	case !isEPM(r.Profile):
		return errors.New("PROFILE_BYTES is not an EPM")
	case !bytes.Equal(r.ProfileSHA256, digest(r.Profile)):
		return errors.New("PROFILE_SHA256 does not match PROFILE_BYTES")
	case r.Visibility != Private && r.Visibility != Public:
		return errors.New("VISIBILITY is out of range")
	case r.CreatedAt == 0 || r.UpdatedAt < r.CreatedAt:
		return errors.New("CREATED_AT and UPDATED_AT are invalid")
	case requireSignature && len(r.Signature) == 0:
		return errors.New("SIGNATURE is missing")
	}
	if _, err := peer.Decode(r.NodePeerID); err != nil {
		return errors.New("NODE_PEER_ID is not a peer ID")
	}
	return nil
}

func digest(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func isEPM(b []byte) bool {
	return len(b) >= 8 && (standardsEPM.SizePrefixedEPMBufferHasIdentifier(b) || standardsEPM.EPMBufferHasIdentifier(b))
}

// signingBytes is the canonical form the signature covers (docs/ABA.md): the
// RFC 8785 JSON of every field in key order, byte vectors as arrays of
// numbers, timestamps as decimal strings, VISIBILITY as its ordinal and
// SIGNATURE empty.
func (r Record) signingBytes() []byte {
	var b bytes.Buffer
	b.Grow(4*len(r.Profile) + 512)
	b.WriteString(`{"CREATED_AT":`)
	writeString(&b, strconv.FormatUint(r.CreatedAt, 10))
	b.WriteString(`,"DELETED":`)
	b.WriteString(strconv.FormatBool(r.Deleted))
	b.WriteString(`,"ENTRY_ID":`)
	writeString(&b, r.EntryID)
	b.WriteString(`,"NODE_PEER_ID":`)
	writeString(&b, r.NodePeerID)
	b.WriteString(`,"NOTE":`)
	writeString(&b, r.Note)
	b.WriteString(`,"PROFILE_BYTES":`)
	writeBytes(&b, r.Profile)
	b.WriteString(`,"PROFILE_SHA256":`)
	writeBytes(&b, r.ProfileSHA256)
	b.WriteString(`,"PUBLIC_KEY":`)
	writeBytes(&b, r.PublicKey)
	b.WriteString(`,"SIGNATURE":[],"SIGNATURE_ALGORITHM":`)
	writeString(&b, r.Algorithm)
	b.WriteString(`,"UPDATED_AT":`)
	writeString(&b, strconv.FormatUint(r.UpdatedAt, 10))
	b.WriteString(`,"VISIBILITY":`)
	b.WriteString(strconv.Itoa(int(r.Visibility)))
	b.WriteByte('}')
	return b.Bytes()
}

// writeString writes s as RFC 8785 serializes it, which is JSON.stringify's
// escaping: quote, backslash and control characters only.
func writeString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

func writeBytes(b *bytes.Buffer, v []byte) {
	b.WriteByte('[')
	for i, c := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(int(c)))
	}
	b.WriteByte(']')
}

// enumOf converts an ordinal to a generated enum type the bindings do not
// export.
func enumOf[T ~int8](_ map[string]T, n int8) T { return T(n) }

// Frame is r as one size-prefixed $ABA FlatBuffer.
func (r Record) Frame() []byte {
	b := flatbuffers.NewBuilder(len(r.Profile) + 512)
	entryID := b.CreateString(r.EntryID)
	nodePeerID := b.CreateString(r.NodePeerID)
	publicKey := b.CreateByteVector(r.PublicKey)
	algorithm := b.CreateString(r.Algorithm)
	profile := b.CreateByteVector(r.Profile)
	profileSHA256 := b.CreateByteVector(r.ProfileSHA256)
	var note, signature flatbuffers.UOffsetT
	if r.Note != "" {
		note = b.CreateString(r.Note)
	}
	if len(r.Signature) != 0 {
		signature = b.CreateByteVector(r.Signature)
	}
	standardsABA.ABAStart(b)
	standardsABA.ABAAddENTRY_ID(b, entryID)
	standardsABA.ABAAddNODE_PEER_ID(b, nodePeerID)
	standardsABA.ABAAddPUBLIC_KEY(b, publicKey)
	standardsABA.ABAAddSIGNATURE_ALGORITHM(b, algorithm)
	standardsABA.ABAAddPROFILE_BYTES(b, profile)
	standardsABA.ABAAddPROFILE_SHA256(b, profileSHA256)
	standardsABA.ABAAddVISIBILITY(b, enumOf(standardsABA.EnumValuesabaDisclosure, int8(r.Visibility)))
	standardsABA.ABAAddCREATED_AT(b, r.CreatedAt)
	standardsABA.ABAAddUPDATED_AT(b, r.UpdatedAt)
	standardsABA.ABAAddDELETED(b, r.Deleted)
	if note != 0 {
		standardsABA.ABAAddNOTE(b, note)
	}
	if signature != 0 {
		standardsABA.ABAAddSIGNATURE(b, signature)
	}
	standardsABA.FinishSizePrefixedABABuffer(b, standardsABA.ABAEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

// DecodeFrame reads one size-prefixed $ABA FlatBuffer. It does not verify.
func DecodeFrame(frame []byte) (r Record, err error) {
	defer func() {
		if recover() != nil {
			r, err = Record{}, errors.New("malformed $ABA record")
		}
	}()
	if len(frame) < 12 || int(binary.LittleEndian.Uint32(frame)) != len(frame)-4 || !standardsABA.SizePrefixedABABufferHasIdentifier(frame) {
		return Record{}, errors.New("not one size-prefixed $ABA record")
	}
	root := standardsABA.GetSizePrefixedRootAsABA(frame, 0)
	return Record{
		EntryID:       string(root.ENTRY_ID()),
		NodePeerID:    string(root.NODE_PEER_ID()),
		PublicKey:     append([]byte(nil), root.PUBLIC_KEYBytes()...),
		Algorithm:     string(root.SIGNATURE_ALGORITHM()),
		Profile:       append([]byte(nil), root.PROFILE_BYTESBytes()...),
		ProfileSHA256: append([]byte(nil), root.PROFILE_SHA256Bytes()...),
		Visibility:    Visibility(root.VISIBILITY()),
		CreatedAt:     root.CREATED_AT(),
		UpdatedAt:     root.UPDATED_AT(),
		Deleted:       root.DELETED(),
		Note:          string(root.NOTE()),
		Signature:     append([]byte(nil), root.SIGNATUREBytes()...),
	}, nil
}
