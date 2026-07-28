package epm

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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
	derived, ok := derivePublicIdentityKeysFromXPub("xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm", identity.Account)
	if !ok {
		t.Fatal("derivePublicIdentityKeysFromXPub failed")
	}

	keys, ok := info["keys"].([]map[string]interface{})
	if !ok {
		t.Fatalf("keys field type = %T", info["keys"])
	}

	for _, key := range keys {
		if key["key_type"] == "signing" && key["address_type"] == "secp256k1" {
			if got, want := key["public_key"], derived.SigningPublicKey; got != want {
				t.Fatalf("public_key = %v, want %q", got, want)
			}
			if got, want := key["key_address"], derived.SigningKeyPath; got != want {
				t.Fatalf("key_address = %v, want %q", got, want)
			}
			if got, want := key["xpub"], derived.XPub; got != want {
				t.Fatalf("xpub = %v, want %q", got, want)
			}
			return
		}
	}

	t.Fatal("expected secp256k1 signing key in EPM keys")
}

func TestNodeEPMCarriesExactlyOneEncryptionKey(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	epmBytes := service.GetNodeEPM()
	if len(epmBytes) == 0 {
		t.Fatal("GetNodeEPM returned no bytes")
	}
	root := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	var encCurves []string
	var encPaths []string
	var key EPM.CryptoKey
	for i := 0; i < root.KEYSLength(); i++ {
		if !root.KEYS(&key, i) {
			continue
		}
		if key.KEY_TYPE() == EPM.KeyTypeEncryption {
			encCurves = append(encCurves, strings.ToLower(string(key.ADDRESS_TYPE())))
			encPaths = append(encPaths, string(key.KEY_ADDRESS()))
		}
	}

	// OWNER RULE: ONE encryption path on the card/QR/EPM. The advertised key is
	// the secp256k1 one at a NON-hardened path, because that is the only
	// encryption key a holder of the XPUB can re-derive (BIP-32 CKDpub) and
	// therefore verify. The identity's X25519 key sits at a HARDENED path,
	// is structurally underivable from an xpub, and must NOT be advertised.
	if len(encCurves) != 1 {
		t.Fatalf("EPM encryption key count = %d, want exactly 1: curves=%v paths=%v",
			len(encCurves), encCurves, encPaths)
	}
	if encCurves[0] != "secp256k1" {
		t.Errorf("EPM encryption curve = %q, want secp256k1", encCurves[0])
	}
	if strings.Contains(strings.TrimSuffix(encPaths[0], "'"), "'/") &&
		strings.HasSuffix(encPaths[0], "'") {
		t.Errorf("advertised encryption path %q is fully hardened; it must be xpub-derivable", encPaths[0])
	}
	if encPaths[0] == identity.EncryptionKeyPath {
		t.Errorf("EPM advertises the hardened X25519 path %q; expected the xpub-derivable secp256k1 path",
			encPaths[0])
	}
}

// The CLI must name the SAME encryption key the card advertises. One encrypt
// path is only true if it is true everywhere it is shown: a node whose card
// says one path and whose `show-identity` says another still looks to an
// operator like it has two. AdvertisedEncryptionKey is the shared accessor the
// CLI displays, so it is locked against the EPM the node actually publishes.
func TestAdvertisedEncryptionKeyMatchesTheCard(t *testing.T) {
	t.Parallel()

	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	var cardPub, cardPath string
	root := EPM.GetSizePrefixedRootAsEPM(service.GetNodeEPM(), 0)
	var key EPM.CryptoKey
	for i := 0; i < root.KEYSLength(); i++ {
		if root.KEYS(&key, i) && key.KEY_TYPE() == EPM.KeyTypeEncryption {
			cardPub = string(key.PUBLIC_KEY())
			cardPath = string(key.KEY_ADDRESS())
			break
		}
	}
	if cardPath == "" {
		t.Fatal("EPM advertises no encryption key")
	}

	gotPub, gotPath, ok := AdvertisedEncryptionKey(xpub, 0, nil)
	if !ok {
		t.Fatal("AdvertisedEncryptionKey could not derive the advertised key")
	}
	if gotPub != cardPub {
		t.Errorf("CLI would show encryption key %q; the card advertises %q", gotPub, cardPub)
	}
	if gotPath != cardPath {
		t.Errorf("CLI would show encryption path %q; the card advertises %q", gotPath, cardPath)
	}
	if gotPath == identity.EncryptionKeyPath {
		t.Errorf("CLI would show the hardened X25519 path %q", gotPath)
	}
}

// A rotated encryption path (§18) must follow through to the CLI too, or the
// rule breaks again the first time an operator rotates a key.
func TestAdvertisedEncryptionKeyFollowsRotatedPath(t *testing.T) {
	t.Parallel()

	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"

	defaultPub, defaultPath, ok := AdvertisedEncryptionKey(xpub, 0, nil)
	if !ok {
		t.Fatal("could not derive the default advertised key")
	}

	rotated, err := NextKeyPath(defaultPath, SlotXPubDerivable)
	if err != nil {
		t.Fatalf("NextKeyPath(%q): %v", defaultPath, err)
	}

	gotPub, gotPath, ok := AdvertisedEncryptionKey(xpub, 0, &Profile{EncryptionKeyPath: rotated})
	if !ok {
		t.Fatal("could not derive the rotated advertised key")
	}
	if gotPath != rotated {
		t.Errorf("advertised path = %q, want the rotated path %q", gotPath, rotated)
	}
	if gotPub == defaultPub {
		t.Errorf("rotated path %q still yields the pre-rotation key %q", rotated, gotPub)
	}
}

func TestServicePersistsProfileThroughEncryptedFlatSQLStore(t *testing.T) {
	t.Parallel()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("peer.Decode failed: %v", err)
	}
	dataDir := t.TempDir()
	service := NewService(nil, peers.NewRegistry(false, nil), peerID, "", dataDir)
	service.SetProfileStore(store)
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := service.UpdateProfile(&Profile{
		DN:    "Jane Example",
		Email: "jane@example.com",
	}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "keys", profileFileName)); !os.IsNotExist(err) {
		t.Fatalf("plaintext profile file exists after encrypted FlatSQL save: %v", err)
	}

	rawEPM, err := store.LoadLocalEPM(peerID.String())
	if err != nil {
		t.Fatalf("LoadLocalEPM failed: %v", err)
	}
	epmRecord := EPM.GetSizePrefixedRootAsEPM(rawEPM, 0)
	if got := string(epmRecord.EMAIL()); got != "jane@example.com" {
		t.Fatalf("stored EPM EMAIL = %q, want jane@example.com", got)
	}
	localRecord, err := store.GetLocalEPMRecord(peerID.String())
	if err != nil {
		t.Fatalf("GetLocalEPMRecord failed: %v", err)
	}
	if !bytes.Equal(localRecord.EPMBytes, rawEPM) {
		t.Fatal("stored local EPM record did not return raw EPM.fbs bytes")
	}

	reloaded := NewService(nil, peers.NewRegistry(false, nil), peerID, "", dataDir)
	reloaded.SetProfileStore(store)
	if err := reloaded.Init(); err != nil {
		t.Fatalf("reload Init failed: %v", err)
	}
	if got := reloaded.GetNodeProfile().Email; got != "jane@example.com" {
		t.Fatalf("reloaded Email = %q, want jane@example.com", got)
	}
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

func TestNodeVCardCarriesTheVerificationChainWithoutKeyMaterial(t *testing.T) {
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
	// OWNER DIRECTIVE 2026-07-27: the serialized record no longer rides the
	// card. The CID + signature + timestamp above ARE the chain — a verifier
	// fetches the authoritative record by CID rather than trusting a copy
	// carried alongside it. Asserting the blob's presence would now be
	// asserting the defect.
	if strings.Contains(vcard, "X-SDN-EPM-B64") || strings.Contains(unfolded, "Binary EPM") {
		t.Fatalf("node vCard still embeds the serialized EPM: %s", vcard)
	}
	for _, banned := range []string{"X-SIGNING-KEY", "X-ENCRYPTION-KEY",
		"signing.spacedatanetwork.org", "encryption.spacedatanetwork.org"} {
		if strings.Contains(unfolded, banned) {
			t.Fatalf("node vCard still carries key material %q: %s", banned, vcard)
		}
	}
	// The signing/encryption PUBLIC KEY aliases that used to be asserted here
	// are gone with the rest of the key material. The chain aliases that remain
	// — xpub, the sign/encrypt DERIVATION PATHS, epmsig/epmts/epmcid — plus the
	// chain ADDRESS aliases below are what the identity contract actually needs:
	// addresses are derived public identifiers, not signing/encryption key
	// bytes, and they stay per the locked vCard alias contract.
	if !strings.Contains(unfolded, "@xpub.spacedatanetwork.org") {
		t.Fatalf("vCard missing the xpub alias — the verifier derives keys from it: %s", vcard)
	}
	if !strings.Contains(unfolded, "@sign.spacedatanetwork.org") {
		t.Fatalf("vCard missing the signing DERIVATION PATH alias: %s", vcard)
	}
	if !strings.Contains(unfolded, "@encrypt.spacedatanetwork.org") {
		t.Fatalf("vCard missing the encryption DERIVATION PATH alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Bitcoin.Address+"@bitcoin.spacedatanetwork.org") {
		t.Fatalf("vCard missing iPhone-visible bitcoin address alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Ethereum.Address+"@ethereum.spacedatanetwork.org") {
		t.Fatalf("vCard missing iPhone-visible ethereum address alias: %s", vcard)
	}
	if !strings.Contains(unfolded, identity.Addresses.Solana.Address+"@solana.spacedatanetwork.org") {
		t.Fatalf("vCard missing iPhone-visible solana address alias: %s", vcard)
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

func TestNodeQRUsesCompactContactAndXPubVCard(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := service.UpdateProfile(&Profile{
		DN:              "Dr. Alice Q. Example",
		FamilyName:      "Example",
		GivenName:       "Alice",
		AdditionalName:  "Q.",
		HonorificPrefix: "Dr.",
		HonorificSuffix: "PhD",
		Email:           "alice@example.com",
		Telephone:       "+1 555 0100",
		Address: &Address{
			POBox:      "Box 42",
			Street:     "1 Orbit Way",
			Locality:   "Cape Canaveral",
			Region:     "FL",
			PostalCode: "32920",
			Country:    "USA",
		},
	}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	qrVCard, err := service.GetNodeQRVCard()
	if err != nil {
		t.Fatalf("GetNodeQRVCard failed: %v", err)
	}
	unfolded := unfoldVCardForTest(qrVCard)

	for _, required := range []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"N:Example;Alice;Q.;Dr.;PhD",
		"FN:Dr. Alice Q. Example",
		"EMAIL:alice@example.com",
		"TEL:+1 555 0100",
		"ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA",
		"END:VCARD",
	} {
		if !strings.Contains(unfolded, required) {
			t.Fatalf("QR vCard missing %q: %s", required, qrVCard)
		}
	}
	xpubAlias := "EMAIL;type=INTERNET;type=xpub:" + xpub + "@xpub.spacedatanetwork.org"
	if got := strings.Count(unfolded, xpubAlias); got != 1 {
		t.Fatalf("QR vCard xpub alias count = %d, want 1: %s", got, qrVCard)
	}
	// Owner directive (nst-qr-identity-verify): the scannable card carries
	// the COMPLETE verification chain as email aliases — the keys' HD
	// derivation paths (base64url of the EPM KEYS' KEY_ADDRESS), the EPM
	// record's embedded signature + timestamp, and the record CID.
	// The ed25519 signing key rides the card at its hardened path: it produces
	// the EPM self-signature, so a verifier needs those exact bytes.
	signAlias := "EMAIL;type=INTERNET;type=sign:" +
		base64.RawURLEncoding.EncodeToString([]byte(identity.SigningKeyPath)) +
		"@sign.spacedatanetwork.org"
	if got := strings.Count(unfolded, signAlias); got != 1 {
		t.Fatalf("QR vCard sign alias count = %d, want 1: %s", got, qrVCard)
	}

	// OWNER RULE: exactly ONE encrypt alias, carrying the EPM's single
	// advertised (xpub-derivable secp256k1) encryption path — never the
	// hardened X25519 path, which no xpub holder could verify.
	advertisedEncPath := ""
	encAliasCount := strings.Count(unfolded, "@encrypt.spacedatanetwork.org")
	{
		root := EPM.GetSizePrefixedRootAsEPM(service.GetNodeEPM(), 0)
		var k EPM.CryptoKey
		for i := 0; i < root.KEYSLength(); i++ {
			if root.KEYS(&k, i) && k.KEY_TYPE() == EPM.KeyTypeEncryption {
				advertisedEncPath = string(k.KEY_ADDRESS())
				break
			}
		}
	}
	if advertisedEncPath == "" {
		t.Fatal("EPM advertises no encryption key")
	}
	if advertisedEncPath == identity.EncryptionKeyPath {
		t.Errorf("card carries the hardened X25519 path %q", advertisedEncPath)
	}
	encAlias := "EMAIL;type=INTERNET;type=encrypt:" +
		base64.RawURLEncoding.EncodeToString([]byte(advertisedEncPath)) +
		"@encrypt.spacedatanetwork.org"
	if got := strings.Count(unfolded, encAlias); got != 1 {
		t.Fatalf("QR vCard encrypt alias count = %d, want 1: %s", got, qrVCard)
	}
	if encAliasCount != 1 {
		t.Fatalf("QR vCard has %d encrypt aliases, want exactly 1: %s", encAliasCount, qrVCard)
	}
	epmBytes := service.GetNodeEPM()
	epmRecord := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	sigBytes, err := hex.DecodeString(strings.TrimSpace(string(epmRecord.SIGNATURE())))
	if err != nil || len(sigBytes) == 0 {
		t.Fatalf("test EPM has no decodable signature: %v", err)
	}
	sigAlias := "EMAIL;type=INTERNET;type=epmsig:" +
		base64.RawURLEncoding.EncodeToString(sigBytes) + "@epmsig.spacedatanetwork.org"
	if !strings.Contains(unfolded, sigAlias) {
		t.Fatalf("QR vCard missing EPM signature alias %q: %s", sigAlias, qrVCard)
	}
	tsAlias := "EMAIL;type=INTERNET;type=epmts:" +
		strconv.FormatInt(epmRecord.SIGNATURE_TIMESTAMP(), 10) + "@epmts.spacedatanetwork.org"
	if !strings.Contains(unfolded, tsAlias) {
		t.Fatalf("QR vCard missing signature timestamp alias %q: %s", tsAlias, qrVCard)
	}
	epmCid, err := ComputeEPMCID(epmBytes)
	if err != nil {
		t.Fatalf("ComputeEPMCID failed: %v", err)
	}
	cidAlias := "EMAIL;type=INTERNET;type=epmcid:" + epmCid + "@epmcid.spacedatanetwork.org"
	if !strings.Contains(unfolded, cidAlias) {
		t.Fatalf("QR vCard missing EPM CID alias %q: %s", cidAlias, qrVCard)
	}
	for _, forbidden := range []string{
		"PRODID", "ORG:", "TITLE:", "ROLE:", "UID:", "X-SDN-", "X-ABRELATEDNAMES:",
		"@signing.spacedatanetwork.org", "@encryption.spacedatanetwork.org",
		"@bitcoin.spacedatanetwork.org", "@ethereum.spacedatanetwork.org", "@solana.spacedatanetwork.org",
		identity.PeerID.String(),
	} {
		if strings.Contains(unfolded, forbidden) {
			t.Fatalf("QR vCard contains forbidden %q: %s", forbidden, qrVCard)
		}
	}
	// Density budget: contact fields + the verification chain (xpub 111B +
	// two b64url paths + sig 86B + timestamp + CID 59B, folded). Still a
	// comfortably scannable QR (~byte-mode version 25 at EC-M).
	if got := len([]byte(qrVCard)); got > 1400 {
		t.Fatalf("QR vCard is %d bytes, want <= 1400: %s", got, qrVCard)
	}
	for _, line := range strings.Split(strings.TrimSuffix(qrVCard, "\r\n"), "\r\n") {
		if got := len([]byte(line)); got > 75 {
			t.Fatalf("QR vCard physical line is %d bytes, want <= 75: %q", got, line)
		}
	}
	qrPNG, err := service.GetNodeQR(512)
	if err != nil {
		t.Fatalf("GetNodeQR failed: %v", err)
	}
	if len(qrPNG) == 0 {
		t.Fatal("GetNodeQR returned an empty PNG")
	}
}

func TestNodeQRVCardDoesNotLeakPeerIDWithoutName(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}

	const xpub = "xpub6DEcA45Z68pwH3NrnV1Tee1pLNfJYruoQkKZJxmeRdBaQAtZg9Vf5LzHVZoBR5dGpmHxWzUXTGo8w1nRS13AvmhbRcBVzduCL3TGsCsj9Mm"
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, xpub, t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := service.UpdateProfile(&Profile{}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	qrVCard, err := service.GetNodeQRVCard()
	if err != nil {
		t.Fatalf("GetNodeQRVCard failed: %v", err)
	}
	for _, forbidden := range []string{identity.PeerID.String(), identity.PeerID.ShortString()} {
		if strings.Contains(qrVCard, forbidden) {
			t.Fatalf("QR vCard leaks PeerID fragment %q: %s", forbidden, qrVCard)
		}
	}
	if !strings.Contains(qrVCard, "N:;SDN Node;;;\r\nFN:SDN Node\r\n") {
		t.Fatalf("QR vCard missing neutral unnamed-node fallback: %s", qrVCard)
	}
}

func TestNodeQRVCardRequiresXPub(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity failed: %v", err)
	}
	service := NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "", t.TempDir())
	if err := service.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := service.GetNodeQRVCard(); err == nil || !strings.Contains(err.Error(), "HD extended public key") {
		t.Fatalf("GetNodeQRVCard error = %v, want required HD extended public key error", err)
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
