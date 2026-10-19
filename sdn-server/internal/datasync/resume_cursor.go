package datasync

// Resume cursors ($QRP RESUME_CURSOR, SDS 1.257.0). The last page of a
// complete scan carries one. A scan Request that sends it back returns only
// the records stored after that scan's sequence boundary, and its own last
// page carries a fresh one.
//
// The token is opaque to clients and only this store can issue it. It is
// hex text of
//
//	version(1) | boundary(8, big endian) | query hash(16) | MAC(16)
//
// where MAC = HMAC-SHA256(store scan generation, domain | the 25 bytes before
// it) truncated to 16 bytes. The query hash is the first 16 bytes of
// rawRecordQueryHash, so a token resumes only the query that earned it. The
// generation turns on a rebuild (storage/scan_generation.go). A token that no
// longer verifies has expired: the client rescans from the first page.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	// ErrResumeCursorInvalid: the text is not a resume cursor (400).
	ErrResumeCursorInvalid = errors.New("RESUME_CURSOR is not a resume cursor this node issued")
	// ErrResumeCursorExpired: the store was rebuilt, or the token was not
	// issued under its current generation (410). Rescan from the first page.
	ErrResumeCursorExpired = errors.New("RESUME_CURSOR expired: the store was rebuilt since it was issued; rescan from the first page")
	// ErrResumeCursorQuery: the token was issued for a different query (400).
	ErrResumeCursorQuery = errors.New("RESUME_CURSOR belongs to a different query; resume with the query that issued it")
)

const (
	resumeCursorVersion = 1
	resumeHashBytes     = 16
	resumeMACBytes      = 16
	resumeBodyBytes     = 1 + 8 + resumeHashBytes
	resumeCursorBytes   = resumeBodyBytes + resumeMACBytes
	resumeMACDomain     = "sdn/qrp-resume-cursor/v1\x00"
)

func resumeMAC(generation, body []byte) []byte {
	mac := hmac.New(sha256.New, generation)
	mac.Write([]byte(resumeMACDomain))
	mac.Write(body)
	return mac.Sum(nil)[:resumeMACBytes]
}

func resumeQueryHash(queryHash string) []byte {
	raw, err := hex.DecodeString(queryHash)
	if err != nil || len(raw) < resumeHashBytes {
		sum := sha256.Sum256([]byte(queryHash))
		raw = sum[:]
	}
	return raw[:resumeHashBytes]
}

// issueResumeCursor is the token that resumes queryHash after boundary.
func issueResumeCursor(generation []byte, boundary int64, queryHash string) string {
	token := make([]byte, resumeBodyBytes, resumeCursorBytes)
	token[0] = resumeCursorVersion
	binary.BigEndian.PutUint64(token[1:9], uint64(max(boundary, 0)))
	copy(token[9:], resumeQueryHash(queryHash))
	token = append(token, resumeMAC(generation, token)...)
	return hex.EncodeToString(token)
}

// resumeSnapshotID names the range (after, through] of a resumed scan.
func resumeSnapshotID(queryHash string, after, through int64) string {
	sum := sha256.Sum256(binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint64([]byte(queryHash), uint64(after)), uint64(through)))
	return hex.EncodeToString(sum[:])
}

// openResumeCursor verifies a token for queryHash under generation and
// returns its boundary.
func openResumeCursor(generation []byte, text, queryHash string) (int64, error) {
	token, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil || len(token) != resumeCursorBytes || token[0] != resumeCursorVersion {
		return 0, ErrResumeCursorInvalid
	}
	body := token[:resumeBodyBytes]
	if !hmac.Equal(token[resumeBodyBytes:], resumeMAC(generation, body)) {
		return 0, ErrResumeCursorExpired
	}
	if !hmac.Equal(body[9:], resumeQueryHash(queryHash)) {
		return 0, ErrResumeCursorQuery
	}
	boundary := binary.BigEndian.Uint64(body[1:9])
	if boundary > 1<<62 {
		return 0, ErrResumeCursorInvalid
	}
	return int64(boundary), nil
}
