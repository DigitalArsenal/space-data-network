package vcard

import (
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// OWNER LAW 2026-07-31: every scannable card carries the full crypto
// identity — xpub, sign + encrypt HD paths, and the epmsig chain — or it is
// not a servable QR card at all.
func TestCardCarriesCryptoIdentityAcceptsFullChain(t *testing.T) {
	card := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:sdn.spaceaware.io",
		"EMAIL;type=INTERNET;type=sign:bS80NCcvMCcvMCcvMC8w@sign.spacedatanetwork.o",
		" rg",
		"EMAIL;type=INTERNET;type=xpub:xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEg@",
		" xpub.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=encrypt:bS80NCcvMCcvMCcvMS8w@encrypt.spacedatanet",
		" work.org",
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
	// xpub without the signature chain is still not a servable identity.
	partial := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:half",
		"EMAIL;type=INTERNET;type=xpub:xpub6DKCyLbCHZLFR4XpFg26royZdkx@xpub.spaceda",
		" tanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
	if CardCarriesCryptoIdentity(partial) {
		t.Error("card lacking sign/encrypt paths or epmsig must fail the gate")
	}
}

// buildAliasTestEPM is createDerivationPathEPM plus a SIGNATURE — the shape
// of every EPM the exchange/discovery paths actually store (both verify the
// signature before storing), so the gate must pass its compact card.
func buildAliasTestEPM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(1024)

	dnOffset := builder.CreateString("Alias Gate Node")
	xpubOff := builder.CreateString(testAliasXPub)
	sigKeyOff := builder.CreateString("0xsigningkey123")
	sigPathOff := builder.CreateString(testSigningKeyPath)
	encKeyOff := builder.CreateString("0xencryptionkey456")
	encPathOff := builder.CreateString(testEncryptionKeyPath)
	// SIGNATURE is stored as hex — the alias emitter b64url-encodes the
	// decoded bytes and skips non-hex values entirely.
	signatureOff := builder.CreateString("a4f38ec1d2b90755e6a1c8f0d94b3a72e5c61f88a90d4b2c7e31f6a8b5d90c4e" +
		"1f2a3b4c5d6e7f8091a2b3c4d5e6f70813579bdf02468ace13579bdf02468ace")

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, sigKeyOff)
	EPM.CryptoKeyAddXPUB(builder, xpubOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, sigPathOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	sigCryptoKey := EPM.CryptoKeyEnd(builder)

	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encKeyOff)
	EPM.CryptoKeyAddXPUB(builder, xpubOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, encPathOff)
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
