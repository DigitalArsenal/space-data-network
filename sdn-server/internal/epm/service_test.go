package epm

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnvcard "github.com/spacedatanetwork/sdn-server/internal/vcard"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestGetNodeEPMJSONIncludesSecp256k1IdentitySigningKey(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	info := service.GetNodeEPMJSON()
	rawIdentityPubKey, err := identity.IdentityPubKey.Raw()
	if err != nil {
		t.Fatalf("IdentityPubKey.Raw failed: %v", err)
	}

	keys, ok := info["keys"].([]map[string]interface{})
	if !ok {
		t.Fatalf("keys field type = %T", info["keys"])
	}

	for _, key := range keys {
		if key["key_type"] == "signing" && key["address_type"] == "secp256k1" {
			if got, want := key["public_key"], hex.EncodeToString(rawIdentityPubKey); got != want {
				t.Fatalf("public_key = %v, want %q", got, want)
			}
			if got, want := key["key_address"], identity.IdentityKeyPath; got != want {
				t.Fatalf("key_address = %v, want %q", got, want)
			}
			return
		}
	}

	t.Fatal("expected secp256k1 signing key in EPM keys")
}

func TestNodeEPMUsesRuntimeEd25519SigningKeyWithoutHDIdentity(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}

	service := NewService(nil, peers.NewRegistry(false, nil), peerID, "", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := service.SetRuntimeSigningKey(priv, "sdn/dataset-publication/v1"); err != nil {
		t.Fatalf("SetRuntimeSigningKey failed: %v", err)
	}

	epmBytes := service.GetNodeEPM()
	if err := VerifyEPMSignature(epmBytes); err != nil {
		t.Fatalf("VerifyEPMSignature failed: %v", err)
	}
	info := service.GetNodeEPMJSON()
	keys, ok := info["keys"].([]map[string]interface{})
	if !ok {
		t.Fatalf("keys field type = %T", info["keys"])
	}

	for _, key := range keys {
		if key["key_type"] == "signing" && key["address_type"] == "ed25519" {
			if got, want := key["public_key"], hex.EncodeToString(pub); got != want {
				t.Fatalf("public_key = %v, want %q", got, want)
			}
			if got, want := key["key_address"], "sdn/dataset-publication/v1"; got != want {
				t.Fatalf("key_address = %v, want %q", got, want)
			}
			return
		}
	}

	t.Fatal("expected runtime Ed25519 signing key in EPM keys")
}

func TestGetNodeEPMJSONProjectsRuntimeIdentityFields(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	info := service.GetNodeEPMJSON()
	signingPubBytes, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw failed: %v", err)
	}

	if got, want := info["signing_pubkey_hex"], hex.EncodeToString(signingPubBytes); got != want {
		t.Fatalf("signing_pubkey_hex = %v, want %q", got, want)
	}
	if got, want := info["signing_key_path"], identity.SigningKeyPath; got != want {
		t.Fatalf("signing_key_path = %v, want %q", got, want)
	}
	if got, want := info["encryption_pubkey_hex"], hex.EncodeToString(identity.EncryptionPub); got != want {
		t.Fatalf("encryption_pubkey_hex = %v, want %q", got, want)
	}
	if got, want := info["encryption_key_path"], identity.EncryptionKeyPath; got != want {
		t.Fatalf("encryption_key_path = %v, want %q", got, want)
	}
	if got, want := info["xpub"], "xpub-test"; got != want {
		t.Fatalf("xpub = %v, want %q", got, want)
	}
	if got, want := info["directory_kind"], "node"; got != want {
		t.Fatalf("directory_kind = %v, want %q", got, want)
	}
	if got, want := info["peer_id"], identity.PeerID.String(); got != want {
		t.Fatalf("peer_id = %v, want %q", got, want)
	}
	if got, want := info["bitcoin_address"], "bc1qtestidentityaddress0000000000000000000000"; got != want {
		t.Fatalf("bitcoin_address = %v, want %q", got, want)
	}
	if got, want := info["bitcoin_key_path"], identity.BitcoinKeyPath; got != want {
		t.Fatalf("bitcoin_key_path = %v, want %q", got, want)
	}
	if got, want := info["ethereum_address"], "0x1234567890abcdef1234567890ABCDEF12345678"; got != want {
		t.Fatalf("ethereum_address = %v, want %q", got, want)
	}
	if got, want := info["ethereum_key_path"], identity.EthereumKeyPath; got != want {
		t.Fatalf("ethereum_key_path = %v, want %q", got, want)
	}
	if got, want := info["solana_address"], "So1anaAddressForIdentityProjection11111111111111"; got != want {
		t.Fatalf("solana_address = %v, want %q", got, want)
	}
	if got, want := info["solana_key_path"], identity.SolanaKeyPath; got != want {
		t.Fatalf("solana_key_path = %v, want %q", got, want)
	}
}

func TestNodeEPMIdentifiesAsNodeEntityType(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	info := service.GetNodeEPMJSON()
	if got, want := info["entity_type"], "node"; got != want {
		t.Fatalf("entity_type = %v, want %q", got, want)
	}
	if got, want := info["agent_version"], versioninfo.AgentVersion; got != want {
		t.Fatalf("agent_version = %v, want %q", got, want)
	}
	if got, want := info["suite_version"], versioninfo.SuiteVersion; got != want {
		t.Fatalf("suite_version = %v, want %q", got, want)
	}
	if got, want := info["standards_version"], versioninfo.SpaceDataStandardsVersion; got != want {
		t.Fatalf("standards_version = %v, want %q", got, want)
	}
	if got, want := info["advertisement_flag"], versioninfo.CurrentAdvertisementFlag; got != want {
		t.Fatalf("advertisement_flag = %v, want %q", got, want)
	}

	epm := EPM.GetSizePrefixedRootAsEPM(service.GetNodeEPM(), 0)
	if got, want := epm.ENTITY_TYPE(), EPM.EntityTypeNode; got != want {
		t.Fatalf("ENTITY_TYPE = %v, want %v", got, want)
	}
}

func TestNodeEPMSignatureVerifiesAndCoversTimestamp(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	epmBytes := service.GetNodeEPM()
	if err := VerifyEPMSignature(epmBytes); err != nil {
		t.Fatalf("VerifyEPMSignature failed: %v", err)
	}

	tampered := append([]byte(nil), epmBytes...)
	epmRecord := EPM.GetSizePrefixedRootAsEPM(tampered, 0)
	if !epmRecord.MutateSIGNATURE_TIMESTAMP(epmRecord.SIGNATURE_TIMESTAMP() + 1) {
		t.Fatal("failed to mutate signature timestamp")
	}
	if err := VerifyEPMSignature(tampered); err == nil {
		t.Fatal("VerifyEPMSignature accepted tampered signature timestamp")
	}
}

func TestNodeVCardIncludesDirectoryMetadataAndPhoto(t *testing.T) {
	t.Parallel()

	peerID, err := peer.Decode("16Uiu2HAm9RZz2EQx8eTsnNCD4v3HVzPf1EfBxqPLqYMXeCQFjaoz")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}

	service := NewService(nil, peers.NewRegistry(false, nil), peerID, "", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := service.UpdateProfile(&Profile{
		DN:           "SpaceAware Node",
		PhotoDataURL: "data:image/png;base64,iVBORw0KGgo=",
	}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	vcard, err := service.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard failed: %v", err)
	}
	if !strings.Contains(vcard, "X-SDN-DIRECTORY-KIND:node") {
		t.Fatalf("vCard missing directory kind: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-PEER-ID:"+peerID.String()) {
		t.Fatalf("vCard missing peer ID: %s", vcard)
	}
	if !strings.Contains(vcard, "PHOTO;ENCODING=b;MEDIATYPE=image/png:iVBORw0KGgo=") {
		t.Fatalf("vCard missing profile photo: %s", vcard)
	}
}

func TestNodeVCardIncludesSignedEPMPayload(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	vcard, err := service.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard failed: %v", err)
	}
	unfolded := unfoldVCardForTest(vcard)

	if !strings.Contains(vcard, "X-SDN-EPM-CID:") {
		t.Fatalf("vCard missing EPM CID: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-EPM-SIGNATURE:") {
		t.Fatalf("vCard missing EPM signature: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-EPM-SIGNATURE-TIMESTAMP:") {
		t.Fatalf("vCard missing EPM signature timestamp: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-EPM-B64:") {
		t.Fatalf("vCard missing embedded EPM payload: %s", vcard)
	}
	signingPubBytes, err := identity.SigningPubKey.Raw()
	if err != nil {
		t.Fatalf("SigningPubKey.Raw failed: %v", err)
	}
	if !strings.Contains(unfolded, hex.EncodeToString(signingPubBytes)+"@signing.digitalarsenal.io") {
		t.Fatalf("vCard missing iPhone-visible signing public key alias: %s", vcard)
	}
	if !strings.Contains(unfolded, hex.EncodeToString(identity.EncryptionPub)+"@encryption.digitalarsenal.io") {
		t.Fatalf("vCard missing iPhone-visible encryption public key alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Bitcoin.Address+"@bitcoin.digitalarsenal.io") {
		t.Fatalf("vCard missing iPhone-visible bitcoin address alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Ethereum.Address+"@ethereum.digitalarsenal.io") {
		t.Fatalf("vCard missing iPhone-visible ethereum address alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Solana.Address+"@solana.digitalarsenal.io") {
		t.Fatalf("vCard missing iPhone-visible solana address alias: %s", vcard)
	}
	if !strings.Contains(vcard, "X-ABLabel:Binary EPM") {
		t.Fatalf("vCard missing Apple related-name binary EPM label: %s", vcard)
	}
}

func TestDirectoryRecordJSONEmbedsBinaryEPMForVCFExport(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	epmBytes := service.GetNodeEPM()
	info, err := DirectoryRecordJSONFromEPM(epmBytes, "")
	if err != nil {
		t.Fatalf("DirectoryRecordJSONFromEPM failed: %v", err)
	}
	payload, ok := info["epm_base64"].(string)
	if !ok || strings.TrimSpace(payload) == "" {
		t.Fatalf("directory record missing epm_base64: %#v", info)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("epm_base64 is not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, epmBytes) {
		t.Fatalf("epm_base64 payload does not match source EPM")
	}
}

func TestNodeQRUsesCompactVCardWithoutEmbeddedEPMPayload(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-test", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	pngData, err := service.GetNodeQR(320)
	if err != nil {
		t.Fatalf("GetNodeQR failed: %v", err)
	}
	vcard, err := sdnvcard.QRToVCard(pngData)
	if err != nil {
		t.Fatalf("QRToVCard failed: %v", err)
	}
	unfolded := unfoldVCardForTest(vcard)

	if strings.Contains(vcard, "X-SDN-EPM-B64:") {
		t.Fatalf("QR vCard should not embed the full binary EPM payload: %s", vcard)
	}
	if strings.Contains(vcard, "Binary EPM") {
		t.Fatalf("QR vCard should not include an Apple related-name binary EPM payload: %s", vcard)
	}
	if !strings.Contains(vcard, "VERSION:3.0") {
		t.Fatalf("QR vCard should use iPhone-compatible vCard 3.0: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-EPM-CID:") {
		t.Fatalf("QR vCard missing EPM CID: %s", vcard)
	}
	if !strings.Contains(vcard, "X-SDN-EPM-SIGNATURE:") {
		t.Fatalf("QR vCard missing EPM signature: %s", vcard)
	}
	if !strings.Contains(unfolded, "@signing.digitalarsenal.io") {
		t.Fatalf("QR vCard missing iPhone-visible signing key alias: %s", vcard)
	}
	if !strings.Contains(unfolded, "@encryption.digitalarsenal.io") {
		t.Fatalf("QR vCard missing iPhone-visible encryption key alias: %s", vcard)
	}
	if !strings.Contains(unfolded, "@bitcoin.digitalarsenal.io") ||
		!strings.Contains(unfolded, "@ethereum.digitalarsenal.io") ||
		!strings.Contains(unfolded, "@solana.digitalarsenal.io") {
		t.Fatalf("QR vCard missing iPhone-visible chain address aliases: %s", vcard)
	}
	if !strings.Contains(vcard, "X-ABRELATEDNAMES:") {
		t.Fatalf("QR vCard missing Apple related-name fields: %s", vcard)
	}
}

func testDerivedIdentity() (*wasm.DerivedIdentity, error) {
	identityPrivKey, _, err := crypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)))
	if err != nil {
		return nil, err
	}
	signingPrivKey, signingPubKey, err := crypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)))
	if err != nil {
		return nil, err
	}
	peerID, err := peer.IDFromPublicKey(identityPrivKey.GetPublic())
	if err != nil {
		return nil, err
	}

	return &wasm.DerivedIdentity{
		IdentityPrivKey:    identityPrivKey,
		IdentityPubKey:     identityPrivKey.GetPublic(),
		SigningPrivKey:     signingPrivKey,
		SigningPubKey:      signingPubKey,
		EncryptionKey:      bytes.Repeat([]byte{0x33}, 32),
		EncryptionPub:      bytes.Repeat([]byte{0x44}, 32),
		PeerID:             peerID,
		IdentityKeyPath:    "m/44'/0'/0'",
		SigningKeyPath:     "m/44'/0'/0'/0'/0'",
		EncryptionKeyPath:  "m/44'/0'/0'/1'/0'",
		BitcoinKeyPath:     "m/44'/0'/0'/0/0",
		BitcoinPrivateKey:  bytes.Repeat([]byte{0x55}, 32),
		EthereumKeyPath:    "m/44'/60'/0'/0/0",
		EthereumPrivateKey: bytes.Repeat([]byte{0x66}, 32),
		SolanaKeyPath:      "m/44'/501'/0'/0'",
		SolanaPrivateKey:   bytes.Repeat([]byte{0x77}, 32),
		Addresses: &wasm.CoinAddresses{
			Bitcoin: &wasm.CoinAddress{
				Address: "bc1qtestidentityaddress0000000000000000000000",
				Path:    "m/44'/0'/0'/0/0",
			},
			Ethereum: &wasm.CoinAddress{
				Address: "0x1234567890abcdef1234567890ABCDEF12345678",
				Path:    "m/44'/60'/0'/0/0",
			},
			Solana: &wasm.CoinAddress{
				Address: "So1anaAddressForIdentityProjection11111111111111",
				Path:    "m/44'/501'/0'/0'",
			},
		},
	}, nil
}

func unfoldVCardForTest(vcardStr string) string {
	lines := strings.Split(strings.ReplaceAll(vcardStr, "\r\n", "\n"), "\n")
	unfolded := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, " ") && len(unfolded) > 0 {
			unfolded[len(unfolded)-1] += strings.TrimPrefix(line, " ")
			continue
		}
		unfolded = append(unfolded, line)
	}
	return strings.Join(unfolded, "\n")
}
