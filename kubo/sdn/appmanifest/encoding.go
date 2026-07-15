package appmanifest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// UIContentEncoding is the string form an inline UIPage.Content is stored in,
// mirroring schema/APP's appContentEncoding value-for-value. The JSON values
// are the lowercase of the .fbs enum names, so the $APP round-trip
// (see encToFBName / encFromFBName in app_fb.go) is a mechanical name map:
//
//	utf8          <-> UTF8
//	base64        <-> BASE64
//	base64_gzip   <-> BASE64_GZIP
//	base64_brotli <-> BASE64_BROTLI
type UIContentEncoding string

const (
	EncodingUTF8         UIContentEncoding = "utf8"          // .fbs UTF8
	EncodingBase64       UIContentEncoding = "base64"        // .fbs BASE64
	EncodingBase64Gzip   UIContentEncoding = "base64_gzip"   // .fbs BASE64_GZIP
	EncodingBase64Brotli UIContentEncoding = "base64_brotli" // .fbs BASE64_BROTLI
)

// normalize maps an empty encoding to the schema default (UTF8), matching the
// .fbs `ENCODING:appContentEncoding = UTF8` default so an omitted encoding and
// an explicit "utf8" are the same thing.
func (e UIContentEncoding) normalize() UIContentEncoding {
	if e == "" {
		return EncodingUTF8
	}
	return e
}

// valid reports whether e is one of the four recognized encodings (an empty
// encoding normalizes to utf8 and is therefore valid).
func (e UIContentEncoding) valid() bool {
	switch e.normalize() {
	case EncodingUTF8, EncodingBase64, EncodingBase64Gzip, EncodingBase64Brotli:
		return true
	default:
		return false
	}
}

// encodeContent renders raw page bytes into the string form named by e.
//
// base64_brotli is a recognized encoding value (it round-trips through $APP and
// validates as an enum), but no brotli codec is vendored in this build, so
// encoding/decoding a brotli page returns an explicit error rather than
// silently guessing. The conjunction app uses base64_gzip (stdlib), so this
// gap does not affect the shipped record.
func (e UIContentEncoding) encodeContent(raw []byte) (string, error) {
	switch e.normalize() {
	case EncodingUTF8:
		return string(raw), nil
	case EncodingBase64:
		return base64.StdEncoding.EncodeToString(raw), nil
	case EncodingBase64Gzip:
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return "", err
		}
		// A zeroed header (no name/comment/mtime, OS=unknown) keeps the CONTENT
		// string reproducible across runs. The drift gate only compares DECODED
		// bytes, but a stable CONTENT is friendlier to record diffs/signing.
		zw.Header = gzip.Header{OS: 255}
		if _, err := zw.Write(raw); err != nil {
			return "", err
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
	case EncodingBase64Brotli:
		return "", fmt.Errorf("appmanifest: encoding %q not supported in this build (no brotli codec vendored)", e)
	default:
		return "", fmt.Errorf("appmanifest: unknown content encoding %q", e)
	}
}

// decodeContent recovers the original page bytes from the string form named by
// e. It is the exact inverse of encodeContent for utf8/base64/base64_gzip.
func (e UIContentEncoding) decodeContent(s string) ([]byte, error) {
	switch e.normalize() {
	case EncodingUTF8:
		return []byte(s), nil
	case EncodingBase64:
		return base64.StdEncoding.DecodeString(s)
	case EncodingBase64Gzip:
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("appmanifest: base64 decode: %w", err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("appmanifest: gzip reader: %w", err)
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("appmanifest: gzip decode: %w", err)
		}
		return out, nil
	case EncodingBase64Brotli:
		return nil, fmt.Errorf("appmanifest: encoding %q not supported in this build (no brotli codec vendored)", e)
	default:
		return nil, fmt.Errorf("appmanifest: unknown content encoding %q", e)
	}
}

// sha256Hex returns the lowercase hex SHA-256 of b — the form
// UIPage.ContentSHA256 / APPUIPage.CONTENT_SHA256 carries.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
