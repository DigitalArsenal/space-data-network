// Package vcard provides bidirectional conversion between EPM (Entity Profile Message)
// FlatBuffers, iPhone-compatible vCard format, and QR codes.
//
// This package enables interoperability between Space Data Network entity profiles
// and standard contact management systems. It supports the full roundtrip:
//
//	EPM (binary) --> vCard (string) --> QR Code (PNG)
//	                                         |
//	                                         v
//	EPM (binary) <-- vCard (string) <-- QR Scan (decoded)
//
// # Field Mapping
//
// The following EPM fields are mapped to vCard properties:
//
//	EPM Field              vCard Property
//	---------              --------------
//	DN                     FN (Formatted Name)
//	LEGAL_NAME             ORG (Organization)
//	FAMILY_NAME            N (part 1)
//	GIVEN_NAME             N (part 2)
//	ADDITIONAL_NAME        N (part 3)
//	HONORIFIC_PREFIX       N (part 4)
//	HONORIFIC_SUFFIX       N (part 5)
//	EMAIL                  EMAIL
//	TELEPHONE              TEL
//	ADDRESS                ADR
//	JOB_TITLE              TITLE
//	OCCUPATION             ROLE
//	MULTIFORMAT_ADDRESS    URL (IPNS addresses)
//	KEYS (Signing)         X-SIGNING-KEY
//	KEYS (Encryption)      X-ENCRYPTION-KEY
//	KEYS / chain proofs    itemN.X-ABRELATEDNAMES and EMAIL aliases
//	ALTERNATE_NAMES        X-ALTERNATE-NAME
//
// # vCard Conversion
//
// Convert EPM to vCard string:
//
//	vcardStr, err := vcard.EPMToVCard(epmBytes)
//
// Convert vCard string back to EPM:
//
//	epmBytes, err := vcard.VCardToEPM(vcardStr)
//
// # QR Code Generation and Scanning
//
// Generate a QR code PNG from an EPM:
//
//	pngData, err := vcard.EPMToQR(epmBytes, 1024) // dense EPM payloads need large QR images
//
// Scan a QR code PNG and recover the EPM:
//
//	epmBytes, err := vcard.QRToEPM(pngData)
//
// You can also work directly with vCard strings:
//
//	pngData, err := vcard.VCardToQR(vcardStr, 256)
//	vcardStr, err := vcard.QRToVCard(pngData)
//
// # QR Code Size
//
// The default QR code size is 256x256 pixels for plain vCards. EPM QR output
// is rendered at a minimum of 1024x1024 pixels because the embedded signed EPM
// payload is dense. Product QR flows should prefer compact vCards that carry an
// EPM CID and visible identity aliases instead of a full binary EPM payload.
//
// # Thread Safety
//
// All functions in this package are thread-safe and can be called concurrently.
package vcard
