package vcard

import (
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// OWNER LAW 2026-07-31 + §21 amendment (2026-08-19): every scannable card
// carries the full crypto identity — sign + encrypt literal-key aliases and
// the epmsig chain — or it is not a servable QR card at all. The xpub alias is
// retired under §21; the gate no longer requires it.
func TestCardCarriesCryptoIdentityAcceptsFullChain(t *testing.T) {
	card := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:sdn.spaceaware.io",
		"EMAIL;type=INTERNET;type=sign:o7LQ1OX2obLDXaGy9PVqd6Y2obLDXaGy9PVqd6Y2ob",
		" L@sign.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=encrypt:o8MR2PY3pcMEYbHz9QWre7Z3pcMEYbHz9QWre7Z3pc",
		" M@encrypt.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=epmsig:pPiwij9fiUMf80Jhi8vKRdiGBfdYSdcrrQsnuCV@ep",
		" msig.spacedatanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
	if !CardCarriesCryptoIdentity(card) {
		t.Error("full-chain card must pass the crypto-identity gate")
	}
}

func TestCardCarriesCryptoIdentityRejectsMinimalCard(t *testing.T) {
	minimal := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"N:;SDN Node bcPpYr2U;;;",
		"FN:SDN Node bcPpYr2U",
		"EMAIL;type=INTERNET;type=peer:16Uiu2HAmGjaPxkWFSXBbmhs9K5x1@peer.spacedata",
		" network.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
	if CardCarriesCryptoIdentity(minimal) {
		t.Error("name+peer-id-only card must NEVER pass the crypto-identity gate")
	}
}

func TestCardCarriesCryptoIdentityRejectsPartialChain(t *testing.T) {
	// sign + encrypt but no epmsig is still not a servable identity.
	partial := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:half",
		"EMAIL;type=INTERNET;type=sign:o7LQ1OX2obLDXaGy9PVqd6Y2obLDXaGy@sign.spaceda",
		" tanetwork.org",
		"EMAIL;type=INTERNET;type=encrypt:o8MR2PY3pcMEYbHz9QWre7Z3pcMEYb@encrypt.spa",
		" cedatanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
	if CardCarriesCryptoIdentity(partial) {
		t.Error("card lacking epmsig must fail the gate")
	}
}

// TestCardCarriesCryptoIdentityDoesNotRequireXpub locks the §21 retirement:
// a card with sign + encrypt + epmsig but NO xpub alias passes the gate. The
// xpub is no longer on the card.
func TestCardCarriesCryptoIdentityDoesNotRequireXpub(t *testing.T) {
	card := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:no-xpub",
		"EMAIL;type=INTERNET;type=sign:o7LQ1OX2obLDXaGy9PVqd6Y2obLDXaGy@sign.spaceda",
		" tanetwork.org",
		"EMAIL;type=INTERNET;type=encrypt:o8MR2PY3pcMEYbHz9QWre7Z3pcMEYb@encrypt.spa",
		" cedatanetwork.org",
		"EMAIL;type=INTERNET;type=epmsig:pPiwij9fiUMf80Jhi8vKRdiGBfdYSdcrrQsnuCV@ep",
		" msig.spacedatanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
	if !CardCarriesCryptoIdentity(card) {
		t.Error("§21 card with sign+encrypt+epmsig but no xpub must pass the gate")
	}
}

// buildAliasTestEPM builds a §21-shaped signed EPM (literal PUBLIC_KEYs, no
// xpub/path on the keys) — the shape of every EPM the exchange/discovery paths
// store, so the gate must pass its compact card.
func buildAliasTestEPM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Alias Gate Node")
	sigKeyOff := builder.CreateString(testSigningPubKeyHex)
	encKeyOff := builder.CreateString(testEncryptionPubKeyHex)
	// SIGNATURE is stored as hex — the alias emitter b64url-encodes the
	// decoded bytes and skips non-hex values entirely.
	signatureOff := builder.CreateString("a4f38ec1d2b90755e6a1c8f0d94b3a72e5c61f88a90d4b2c7e31f6a8b5d90c4e" +
		"1f2a3b4c5d6e7f8091a2b3c4d5e6f70813579bdf02468ace13579bdf02468ace")

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
	encCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(encCryptoKey)
	builder.PrependUOffsetT(sigCryptoKey)
	keysVec := builder.EndVector(2)

	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddKEYS(builder, keysVec)
	EPM.EPMAddSIGNATURE(builder, signatureOff)
	EPM.EPMAddSIGNATURE_TIMESTAMP(builder, 1785508236)
	epmOffset := EPM.EPMEnd(builder)

	builder.FinishSizePrefixedWithFileIdentifier(epmOffset, []byte(EPM.EPMIdentifier))
	return builder.FinishedBytes()
}

func TestCompactQRVCardPassesOwnGate(t *testing.T) {
	// The real compact card built from a signed EPM must pass the gate the
	// serving paths apply, or every QR endpoint would go dark.
	card, err := CompactQRVCard(buildAliasTestEPM(t))
	if err != nil {
		t.Fatalf("CompactQRVCard failed: %v", err)
	}
	if !CardCarriesCryptoIdentity(card) {
		t.Errorf("CompactQRVCard output failed the crypto-identity gate:\n%s", card)
	}
}
