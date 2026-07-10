package modulert

import "encoding/binary"

// Publication-protected module artifacts (space-data-module-sdk
// docs/module-publication-standard.md) append an SDS $REC record collection
// to the wasm payload:
//
//	payload || REC-flatbuffer-bytes || uint32le(REC length) || "$REC"
//
// The trailer carries MBL bundle metadata, the PNM publication notice, and
// (for encrypted delivery) an ENC record. Runtimes strip the trailer before
// wasm validation/compilation (loader expectation 6 in the standard);
// signature verification against the trailer records is a separate,
// host-policy concern.
const (
	publicationTrailerFooterLength = 8
	publicationTrailerMagic        = "$REC"
)

// StripPublicationTrailer returns the wasm payload bytes with any appended
// SDS $REC publication trailer removed. Bytes without a well-formed trailer
// are returned unchanged (the function never fails: a malformed footer is
// treated as absent and left for wasm validation to reject).
func StripPublicationTrailer(bytes []byte) []byte {
	if len(bytes) < publicationTrailerFooterLength {
		return bytes
	}
	footer := len(bytes) - publicationTrailerFooterLength
	if string(bytes[footer+4:]) != publicationTrailerMagic {
		return bytes
	}
	recLength := int(binary.LittleEndian.Uint32(bytes[footer : footer+4]))
	payloadLength := footer - recLength
	if recLength == 0 || payloadLength < 0 {
		return bytes
	}
	return bytes[:payloadLength]
}

// HasPublicationTrailer reports whether bytes end in a well-formed SDS $REC
// publication trailer.
func HasPublicationTrailer(bytes []byte) bool {
	return len(StripPublicationTrailer(bytes)) != len(bytes)
}

// PublicationTrailerRecordBytes returns the appended SDS $REC record
// collection bytes (the encoded REC FlatBuffer carrying MBL/PNM/ENC) for a
// well-formed publication trailer — the region between the portable payload
// and the 8-byte footer. Returns nil when bytes has no well-formed trailer
// (mirrors HasPublicationTrailer/StripPublicationTrailer: a malformed
// footer is treated as absent).
func PublicationTrailerRecordBytes(bytes []byte) []byte {
	if !HasPublicationTrailer(bytes) {
		return nil
	}
	footer := len(bytes) - publicationTrailerFooterLength
	recLength := int(binary.LittleEndian.Uint32(bytes[footer : footer+4]))
	return bytes[footer-recLength : footer]
}
