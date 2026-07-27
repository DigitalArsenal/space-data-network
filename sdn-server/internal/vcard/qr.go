// Package vcard provides QR code generation and scanning for vCard/EPM data.
package vcard

import (
	"bytes"
	"errors"
	"image"
	"image/png"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	qrgen "github.com/skip2/go-qrcode"
)

// QR code errors
var (
	ErrQREncode    = errors.New("failed to encode QR code")
	ErrQRDecode    = errors.New("failed to decode QR code")
	ErrInvalidSize = errors.New("invalid QR code size")
)

// DefaultQRSize is the default QR code size in pixels.
const DefaultQRSize = 256

// VCardToQR generates a QR code PNG from a vCard string.
func VCardToQR(vcardStr string, size int) ([]byte, error) {
	if vcardStr == "" {
		return nil, ErrEmptyVCard
	}
	if size <= 0 {
		size = DefaultQRSize
	}
	if size > 4096 {
		return nil, ErrInvalidSize
	}

	qr, err := qrgen.New(vcardStr, qrgen.Medium)
	if err != nil {
		return nil, errors.Join(ErrQREncode, err)
	}

	pngData, err := qr.PNG(size)
	if err != nil {
		return nil, errors.Join(ErrQREncode, err)
	}

	return pngData, nil
}

// QRToVCard scans a QR code image and extracts the vCard string.
func QRToVCard(pngData []byte) (string, error) {
	if len(pngData) == 0 {
		return "", ErrQRDecode
	}

	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}

	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}

	reader := qrcode.NewQRCodeReader()
	result, err := reader.Decode(bmp, nil)
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}

	return result.GetText(), nil
}

// EPMToQR renders an EPM as a scannable QR code.
//
// OWNER DIRECTIVE 2026-07-27: this used to encode the ENTIRE serialized EPM as
// an X-SDN-EPM-B64 blob, which is why it needed a 1024px minimum — the payload
// was the whole record. It now renders the SAME compact identity card the node
// serves at /identity/<peer>.qr.vcf (CompactQRVCard): contact fields plus the
// verification-chain email aliases (xpub, sign/encrypt derivation paths,
// epmsig, epmts, epmcid).
//
// That card is strictly more useful at a fraction of the density. A phone can
// actually import it as a contact, and a verifier gets the complete chain: derive
// the secp256k1 key from xpub + path, fetch the authoritative record by CID, and
// check the signature. The blob was redundant to that chain — the record is
// retrievable, so shipping a copy of it inside a QR bought nothing and cost the
// scannability.
func EPMToQR(epmBytes []byte, size int) ([]byte, error) {
	if len(epmBytes) == 0 {
		return nil, ErrEmptyEPM
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, ErrInvalidEPM
	}
	card, err := CompactQRVCard(epmBytes)
	if err != nil {
		return nil, err
	}
	return VCardToQR(card, size)
}

// QRToEPM scans a QR code image and converts it to EPM FlatBuffer bytes.
//
// ⚠ LEGACY INBOUND PATH. It works only on QR codes that carry an embedded
// X-SDN-EPM-B64 blob — i.e. codes produced BEFORE the 2026-07-27 owner
// directive, or by a third party that still embeds one. Codes this node emits
// today carry the compact identity card instead (see EPMToQR), from which the
// record is FETCHED by its epmcid alias rather than unpacked in place. The
// reader is kept because refusing to read older cards would strand them; it is
// not a round trip of what we now emit.
func QRToEPM(pngData []byte) ([]byte, error) {
	vcardStr, err := QRToVCard(pngData)
	if err != nil {
		return nil, err
	}
	return VCardToEPM(vcardStr)
}

// VCardToQRImage generates a QR code as an image.Image from a vCard string.
func VCardToQRImage(vcardStr string, size int) (image.Image, error) {
	if vcardStr == "" {
		return nil, ErrEmptyVCard
	}
	if size <= 0 {
		size = DefaultQRSize
	}
	if size > 4096 {
		return nil, ErrInvalidSize
	}

	qr, err := qrgen.New(vcardStr, qrgen.Medium)
	if err != nil {
		return nil, errors.Join(ErrQREncode, err)
	}

	return qr.Image(size), nil
}

// QRImageToVCard scans a QR code from an image.Image and extracts the vCard string.
func QRImageToVCard(img image.Image) (string, error) {
	if img == nil {
		return "", ErrQRDecode
	}

	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}

	reader := qrcode.NewQRCodeReader()
	result, err := reader.Decode(bmp, nil)
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}

	return result.GetText(), nil
}
