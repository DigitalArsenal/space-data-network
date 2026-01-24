// Package vcard provides QR code generation and scanning for vCard/EPM data.
package vcard

import (
	"bytes"
	"errors"
	"image"
	"image/png"

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

// EPMToQR converts an EPM FlatBuffer directly to a QR code PNG.
func EPMToQR(epmBytes []byte, size int) ([]byte, error) {
	vcardStr, err := EPMToVCard(epmBytes)
	if err != nil {
		return nil, err
	}
	return VCardToQR(vcardStr, size)
}

// QRToEPM scans a QR code image and converts to EPM FlatBuffer.
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
