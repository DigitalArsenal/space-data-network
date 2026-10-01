// Package cidv1 converts record CIDs between their text form and the 36-byte
// binary form the format-4 wire carries (contract §3.1: 01 55 12 20 + the
// sha256 digest). Every format-4 record CID is a CIDv1, raw codec, sha2-256:
// its text is "b" + lower-case unpadded base32 of those 36 bytes ("bafkrei…").
package cidv1

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
)

// Prefix is the binary CID's header: version 1, raw, sha2-256, 32 bytes.
var Prefix = [4]byte{0x01, 0x55, 0x12, 0x20}

// Len is the binary CID's length.
const Len = 36

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrForm is a CID that is not a CIDv1 raw sha2-256.
var ErrForm = errors.New("not a CIDv1 raw sha2-256 CID (bafkrei…)")

// Parse decodes a bafkrei… CID.
func Parse(text string) ([Len]byte, error) {
	var out [Len]byte
	if len(text) != 59 || text[0] != 'b' {
		return out, ErrForm
	}
	raw, err := b32.DecodeString(strings.ToUpper(text[1:]))
	if err != nil || len(raw) != Len || [4]byte(raw[:4]) != Prefix {
		return out, ErrForm
	}
	copy(out[:], raw)
	if Text(out) != text { // canonical (lower-case) only
		return out, ErrForm
	}
	return out, nil
}

// Text encodes a binary CID.
func Text(b [Len]byte) string { return "b" + strings.ToLower(b32.EncodeToString(b[:])) }

// Of returns the CID of a plaintext record.
func Of(plain []byte) string {
	var b [Len]byte
	copy(b[:], Prefix[:])
	sum := sha256.Sum256(plain)
	copy(b[4:], sum[:])
	return Text(b)
}
