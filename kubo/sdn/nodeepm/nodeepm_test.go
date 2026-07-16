package nodeepm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// testIdentity builds an Identity backed by a fresh Ed25519 key, mirroring how
// the plugin adapts core.IpfsNode.PrivateKey (a libp2p Ed25519 key produces a
// standard ed25519 signature that verifies under the raw public key).
func testIdentity(t *testing.T) (Identity, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	id := Identity{
		PeerID:     "12D3KooWTestNodePeerIDForRoundTripAcceptance000000",
		SigningPub: pub,
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWTestNodePeerIDForRoundTripAcceptance000000"},
		Sign:       func(payload []byte) ([]byte, error) { return ed25519.Sign(priv, payload), nil },
	}
	return id, pub
}

func TestBuildNodeEPMRoundTrip(t *testing.T) {
	id, pub := testIdentity(t)

	epmBytes, err := BuildNodeEPM(id)
	if err != nil {
		t.Fatalf("BuildNodeEPM: %v", err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		t.Fatal("built EPM is not a size-prefixed $EPM buffer")
	}

	// 1) Signature verifies under the node's own public key.
	if err := VerifyEPMSignature(epmBytes); err != nil {
		t.Fatalf("VerifyEPMSignature: %v", err)
	}

	rec := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	// Entity type is Node.
	if rec.ENTITY_TYPE() != EPM.EntityTypeNode {
		t.Fatalf("ENTITY_TYPE = %v, want Node", rec.ENTITY_TYPE())
	}
	// DN carries the peer ID.
	if got := string(rec.DN()); got != id.PeerID {
		t.Fatalf("DN = %q, want peer id %q", got, id.PeerID)
	}
	// The Ed25519 signing key on the wire is exactly the node public key.
	wantPubHex := hex.EncodeToString(pub)
	var sawSigningKey bool
	key := new(EPM.CryptoKey)
	for i := 0; i < rec.KEYSLength(); i++ {
		if !rec.KEYS(key, i) {
			continue
		}
		if key.KEY_TYPE() == EPM.KeyTypeSigning {
			sawSigningKey = true
			if string(key.PUBLIC_KEY()) != wantPubHex {
				t.Fatalf("signing PUBLIC_KEY = %q, want %q", string(key.PUBLIC_KEY()), wantPubHex)
			}
			if strings.ToLower(string(key.ADDRESS_TYPE())) != "ed25519" {
				t.Fatalf("signing ADDRESS_TYPE = %q, want ed25519", string(key.ADDRESS_TYPE()))
			}
		}
		// No encryption key is fabricated for a node identity.
		if key.KEY_TYPE() == EPM.KeyTypeEncryption {
			t.Fatalf("node EPM must not carry a fabricated encryption key")
		}
	}
	if !sawSigningKey {
		t.Fatal("node EPM has no Ed25519 signing key")
	}
	// /p2p/<peerID> is advertised so the peer ID is recoverable.
	var sawP2P bool
	for i := 0; i < rec.MULTIFORMAT_ADDRESSLength(); i++ {
		if strings.Contains(string(rec.MULTIFORMAT_ADDRESS(i)), "/p2p/"+id.PeerID) {
			sawP2P = true
		}
	}
	if !sawP2P {
		t.Fatal("node EPM MULTIFORMAT_ADDRESS is missing /p2p/<peerID>")
	}

	// 2) vCard is non-empty and carries the peer id + signing key + embedded EPM.
	card, err := EPMToVCard(epmBytes)
	if err != nil {
		t.Fatalf("EPMToVCard: %v", err)
	}
	if strings.TrimSpace(card) == "" {
		t.Fatal("EPMToVCard returned empty")
	}
	for _, needle := range []string{"BEGIN:VCARD", "FN:" + id.PeerID, "X-SIGNING-KEY", fieldSDNEPMBase64} {
		if !strings.Contains(card, needle) {
			t.Fatalf("vCard missing %q\n%s", needle, card)
		}
	}

	// 3) QR renders and decodes back to a payload embedding the same EPM.
	qrPNG, err := EPMToQR(epmBytes, 0)
	if err != nil {
		t.Fatalf("EPMToQR: %v", err)
	}
	if len(qrPNG) == 0 {
		t.Fatal("EPMToQR returned no bytes")
	}
	text, err := QRToText(qrPNG)
	if err != nil {
		t.Fatalf("QRToText: %v", err)
	}
	wantEmbed := fieldSDNEPMBase64 + ":" + base64.StdEncoding.EncodeToString(epmBytes)
	if !strings.Contains(text, wantEmbed) {
		t.Fatalf("decoded QR does not embed the EPM base64")
	}

	// 4) JSON projection reports the node identity.
	js, err := EPMToJSON(epmBytes)
	if err != nil {
		t.Fatalf("EPMToJSON: %v", err)
	}
	if js["entity_type"] != "Node" {
		t.Fatalf("json entity_type = %v, want Node", js["entity_type"])
	}
	if js["peer_id"] != id.PeerID {
		t.Fatalf("json peer_id = %v, want %q", js["peer_id"], id.PeerID)
	}
	if _, ok := js["signature"].(string); !ok {
		t.Fatal("json is missing signature")
	}
}

// A node advertising many relay/transport multiaddrs produces an EPM whose
// full base64 embed exceeds the QR ceiling; EPMToQR must still render and the
// scanned payload must carry the compact identity (peer id + signing key).
func TestEPMToQRFallsBackWhenLarge(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	peer := "12D3KooWTestNodePeerIDForRoundTripAcceptance000000"
	// ~14 verbose relay-circuit multiaddrs — comfortably over the QR byte limit
	// once base64-embedded.
	var addrs []string
	for i := 0; i < 14; i++ {
		addrs = append(addrs, "/ip6/2001:41d0:000a:525a::1/udp/4001/quic-v1/webtransport/certhash/uEiDX6qA46nB-AnAUaKmhHYaUC_-toXKXZH8IQx6c7IkBNQ/p2p/12D3KooWN8J9vkx3msuDnYH3WGkoDBqYtYQaiPYgPVX6HFgWu6oK/p2p-circuit/p2p/"+peer)
	}
	id := Identity{
		PeerID:     peer,
		SigningPub: pub,
		Multiaddrs: addrs,
		Sign:       func(p []byte) ([]byte, error) { return ed25519.Sign(priv, p), nil },
	}
	epmBytes, err := BuildNodeEPM(id)
	if err != nil {
		t.Fatalf("BuildNodeEPM: %v", err)
	}
	// Sanity: the full base64 embed is genuinely too big for a QR.
	if base64.StdEncoding.EncodedLen(len(epmBytes)) <= 2953 {
		t.Skip("EPM small enough to embed; fallback path not exercised")
	}
	qrPNG, err := EPMToQR(epmBytes, 0)
	if err != nil {
		t.Fatalf("EPMToQR (fallback): %v", err)
	}
	text, err := QRToText(qrPNG)
	if err != nil {
		t.Fatalf("QRToText (fallback): %v", err)
	}
	if !strings.Contains(text, "X-SIGNING-KEY:"+hex.EncodeToString(pub)) {
		t.Fatalf("compact QR missing the signing key:\n%s", text)
	}
	if !strings.Contains(text, "FN:"+peer) {
		t.Fatalf("compact QR missing the peer id:\n%s", text)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	id, _ := testIdentity(t)
	epmBytes, err := BuildNodeEPM(id)
	if err != nil {
		t.Fatalf("BuildNodeEPM: %v", err)
	}
	// Flip a byte in the middle of the buffer; the signature must no longer verify.
	tampered := make([]byte, len(epmBytes))
	copy(tampered, epmBytes)
	tampered[len(tampered)/2] ^= 0xFF
	if err := VerifyEPMSignature(tampered); err == nil {
		t.Fatal("VerifyEPMSignature accepted a tampered EPM")
	}
}

func TestBuildNodeEPMValidatesInput(t *testing.T) {
	good, _ := testIdentity(t)

	noPeer := good
	noPeer.PeerID = "  "
	if _, err := BuildNodeEPM(noPeer); err != ErrNoPeerID {
		t.Fatalf("empty peer id err = %v, want ErrNoPeerID", err)
	}

	badKey := good
	badKey.SigningPub = []byte{1, 2, 3}
	if _, err := BuildNodeEPM(badKey); err != ErrBadSigningKey {
		t.Fatalf("bad key err = %v, want ErrBadSigningKey", err)
	}

	noSign := good
	noSign.Sign = nil
	if _, err := BuildNodeEPM(noSign); err != ErrNoSigner {
		t.Fatalf("nil signer err = %v, want ErrNoSigner", err)
	}
}
