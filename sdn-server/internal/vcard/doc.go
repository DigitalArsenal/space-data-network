// Package vcard provides bidirectional conversion between EPM (Entity Profile Message)
// FlatBuffers, vCard 4.0 format, and QR codes.
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
// The following EPM fields are mapped to vCard 4.0 properties:
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
//	pngData, err := vcard.EPMToQR(epmBytes, 256) // 256x256 pixels
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
// The default QR code size is 256x256 pixels. The maximum supported size is
// 4096x4096 pixels. Larger QR codes can encode more data but require more
// storage space. For typical entity profiles, 256-512 pixels is sufficient.
//
// # Thread Safety
//
// All functions in this package are thread-safe and can be called concurrently.
package vcard
