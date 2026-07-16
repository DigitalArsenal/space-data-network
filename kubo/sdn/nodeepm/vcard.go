package nodeepm

// Ported from sdn-server/internal/vcard/vcard.go (read-only source), reduced to
// the fields a node EPM actually carries. The node has no HD-wallet chain
// proofs or alternate names, so the Apple-identity alias machinery and the
// person-name (N/ORG/ADR) mapping are dropped; what remains is the same vCard
// 3.0 shape and the same SDN extension fields (X-SIGNING-KEY, the EPM signature
// pair, and the base64 EPM embed) so a node vCard round-trips like any other.

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

const (
	iphoneVCardProdID = "-//Apple Inc.//iPhone OS 15.1.1//EN"

	fieldSDNEPMBase64             = "X-SDN-EPM-B64"
	fieldSDNEPMSignature          = "X-SDN-EPM-SIGNATURE"
	fieldSDNEPMSignatureTimestamp = "X-SDN-EPM-SIGNATURE-TIMESTAMP"
)

// EPMToVCard converts a signed node EPM (size-prefixed FlatBuffer) into an
// iPhone-compatible vCard 3.0 string. The peer ID (DN) becomes FN; the node's
// multiaddrs become URL lines; the Ed25519 signing key becomes X-SIGNING-KEY;
// and the full signed EPM is embedded base64 for a lossless round-trip.
func EPMToVCard(epmBytes []byte) (string, error) {
	if len(epmBytes) == 0 {
		return "", ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return "", ErrInvalidEPMData
	}
	epm := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	card := vcard.Card{}
	card.Set("VERSION", &vcard.Field{Value: "3.0"})
	card.Set("PRODID", &vcard.Field{
		Value:  iphoneVCardProdID,
		Params: vcard.Params{"VALUE": []string{"TEXT"}},
	})

	// DN (the node peer ID) -> FN.
	if dn := epm.DN(); dn != nil {
		card.Add("FN", &vcard.Field{Value: string(dn)})
	}

	// Multiformat addresses (/p2p/<peerID>, listen multiaddrs) -> URL.
	for i := 0; i < epm.MULTIFORMAT_ADDRESSLength(); i++ {
		if addrBytes := epm.MULTIFORMAT_ADDRESS(i); addrBytes != nil {
			if addrStr := strings.TrimSpace(string(addrBytes)); addrStr != "" {
				card.Add("URL", &vcard.Field{Value: addrStr})
			}
		}
	}

	// Signing / encryption public keys -> X-SIGNING-KEY / X-ENCRYPTION-KEY.
	key := new(EPM.CryptoKey)
	for i := 0; i < epm.KEYSLength(); i++ {
		if epm.KEYS(key, i) {
			if pubKey := key.PUBLIC_KEY(); pubKey != nil {
				var fieldName string
				switch key.KEY_TYPE() {
				case EPM.KeyTypeSigning:
					fieldName = "X-SIGNING-KEY"
				case EPM.KeyTypeEncryption:
					fieldName = "X-ENCRYPTION-KEY"
				default:
					fieldName = "X-PUBLIC-KEY"
				}
				card.Add(fieldName, &vcard.Field{Value: string(pubKey)})
			}
		}
	}

	if signature := epm.SIGNATURE(); signature != nil {
		card.Add(fieldSDNEPMSignature, &vcard.Field{Value: string(signature)})
	}
	if ts := epm.SIGNATURE_TIMESTAMP(); ts != 0 {
		card.Add(fieldSDNEPMSignatureTimestamp, &vcard.Field{Value: strconv.FormatInt(ts, 10)})
	}
	card.Add(fieldSDNEPMBase64, &vcard.Field{Value: base64.StdEncoding.EncodeToString(epmBytes)})

	var b strings.Builder
	enc := vcard.NewEncoder(&b)
	if err := enc.Encode(card); err != nil {
		return "", err
	}
	return b.String(), nil
}
