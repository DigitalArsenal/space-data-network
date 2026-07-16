package nodeepm

// Ported from sdn-server/internal/vcard/qr.go (read-only source). QR generation
// is self-contained (github.com/skip2/go-qrcode, pure Go, no runtime or network
// dependency) so the node renders its own QR — never a remote chart service.
// Decoding (github.com/makiuchi-d/gozxing) backs the round-trip test only.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"strconv"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	qrgen "github.com/skip2/go-qrcode"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

var (
	ErrQREncode    = errors.New("failed to encode QR code")
	ErrQRDecode    = errors.New("failed to decode QR code")
	ErrInvalidSize = errors.New("invalid QR code size")
)

// DefaultQRSize is the default QR code edge length in pixels.
const DefaultQRSize = 512

// minEPMQRSize floors the QR size for an EPM payload: the embedded base64 EPM is
// dense, so a too-small module grid is unscannable.
const minEPMQRSize = 1024

// EPMToQR renders a signed node EPM to a QR code PNG.
//
// It prefers a vCard that embeds the full base64 EPM, so a scan reconstructs the
// exact signed record. A node EPM advertising many relay/transport multiaddrs
// can exceed the QR byte ceiling, though; rather than fail, it then falls back
// to a compact identity vCard (peer ID + Ed25519 signing key + signature) that
// always fits and still identifies and authenticates the node. The full record
// remains available via the EPM / vCard endpoints.
func EPMToQR(epmBytes []byte, size int) ([]byte, error) {
	if len(epmBytes) == 0 {
		return nil, ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, ErrInvalidEPMData
	}
	if size <= 0 {
		size = DefaultQRSize
	}
	if size < minEPMQRSize {
		size = minEPMQRSize
	}
	if size > 4096 {
		return nil, ErrInvalidSize
	}

	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	// Preferred payload: the full signed EPM, embedded base64.
	qr, err := qrgen.New(fullEPMQRVCard(epm, epmBytes), qrgen.Medium)
	if err != nil {
		// Too dense for a QR — fall back to the compact identity payload.
		qr, err = qrgen.New(compactIdentityQRVCard(epm), qrgen.Medium)
		if err != nil {
			return nil, errors.Join(ErrQREncode, err)
		}
	}
	pngData, err := qr.PNG(size)
	if err != nil {
		return nil, errors.Join(ErrQREncode, err)
	}
	return pngData, nil
}

// fullEPMQRVCard is the lossless QR payload: FN plus the base64 EPM embed.
func fullEPMQRVCard(epm *EPM.EPM, epmBytes []byte) string {
	return strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:" + escapeQRVCardValue(qrDisplayName(epm)),
		fieldSDNEPMBase64 + ":" + base64.StdEncoding.EncodeToString(epmBytes),
		"END:VCARD",
	}, "\r\n") + "\r\n"
}

// compactIdentityQRVCard is the always-fits fallback: the node peer ID, its
// Ed25519 signing key, and the EPM signature — enough to identify and verify.
func compactIdentityQRVCard(epm *EPM.EPM) string {
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:" + escapeQRVCardValue(qrDisplayName(epm)),
	}
	key := new(EPM.CryptoKey)
	for i := 0; i < epm.KEYSLength(); i++ {
		if epm.KEYS(key, i) && key.KEY_TYPE() == EPM.KeyTypeSigning {
			if pub := strings.TrimSpace(string(key.PUBLIC_KEY())); pub != "" {
				lines = append(lines, "X-SIGNING-KEY:"+pub)
				break
			}
		}
	}
	if sig := strings.TrimSpace(string(epm.SIGNATURE())); sig != "" {
		lines = append(lines, fieldSDNEPMSignature+":"+sig)
	}
	if ts := epm.SIGNATURE_TIMESTAMP(); ts != 0 {
		lines = append(lines, fieldSDNEPMSignatureTimestamp+":"+strconv.FormatInt(ts, 10))
	}
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func qrDisplayName(epm *EPM.EPM) string {
	if dn := strings.TrimSpace(string(epm.DN())); dn != "" {
		return dn
	}
	return "Space Data Network Node EPM"
}

// QRToText decodes a QR code PNG and returns its text payload. Used by the
// round-trip test to prove the rendered QR is scannable.
func QRToText(pngData []byte) (string, error) {
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
	result, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", errors.Join(ErrQRDecode, err)
	}
	return result.GetText(), nil
}

func escapeQRVCardValue(value string) string {
	return strings.NewReplacer(
		"\\", "\\\\",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
		",", "\\,",
		";", "\\;",
	).Replace(value)
}
