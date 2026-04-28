package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"golang.org/x/crypto/curve25519"
)

func TestModuleUploadProtocolStoresEnvelopeBackedModule(t *testing.T) {
	t.Parallel()

	providerHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		t.Fatalf("provider libp2p host: %v", err)
	}
	defer providerHost.Close()

	clientHost, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		t.Fatalf("client libp2p host: %v", err)
	}
	defer clientHost.Close()

	root := t.TempDir()
	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}

	signerPub, signerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	signerPubHex := hex.EncodeToString(signerPub)
	uploaderXPub := "xpub-upload-test"
	encryptedBundle := []byte("encrypted wasm bundle bytes")
	contentKey := bytes.Repeat([]byte{0x45}, 32)
	bundleHash := sha256.Sum256(encryptedBundle)
	signatureHex := hex.EncodeToString(ed25519.Sign(signerPriv, bundleHash[:]))

	providerPrivateKey := bytes.Repeat([]byte{0x2d}, 32)
	providerPublicKey, err := curve25519.X25519(providerPrivateKey, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive provider public key: %v", err)
	}
	envelope, err := license.WrapProviderContentKey(contentKey, providerPublicKey, license.ProviderContentKeyAAD{
		ModuleID:           "com.spaceaware.test-protocol",
		Version:            "0.0.1",
		BundleSHA256:       hex.EncodeToString(bundleHash[:]),
		SignerPublicKeyHex: signerPubHex,
		ProviderPeerID:     providerHost.ID().String(),
	})
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}

	publishedID := make(chan string, 1)
	service := &ModuleUploadProtocolService{
		Registry:       reg,
		ProviderPeerID: providerHost.ID().String(),
		KeyLookup: func(gotXPub string) (string, error) {
			if gotXPub != uploaderXPub {
				return "", fmt.Errorf("key lookup xpub = %q, want %q", gotXPub, uploaderXPub)
			}
			return signerPubHex, nil
		},
		AfterUpload: func(pluginID string) error {
			publishedID <- pluginID
			return nil
		},
	}
	service.Register(providerHost)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	stream, err := clientHost.NewStream(ctx, providerHost.ID(), PluginModuleUploadProtocolID)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	defer stream.Close()
	if err := stream.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline failed: %v", err)
	}

	request := map[string]interface{}{
		"version": 1,
		"metadata": map[string]interface{}{
			"id":                   "com.spaceaware.test-protocol",
			"version":              "0.0.1",
			"required_scope":       "spaceaware:test",
			"allowed_domains":      []string{"spaceaware.io"},
			"max_grant_timeout_ms": 120000,
		},
		"uploader_xpub":         uploaderXPub,
		"signer_public_key_hex": signerPubHex,
		"signature_hex":         signatureHex,
		"content_key_envelope":  envelope,
		"encrypted_bundle_b64":  base64.RawURLEncoding.EncodeToString(encryptedBundle),
	}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		t.Fatalf("encode upload request: %v", err)
	}
	if closeWriter, ok := stream.(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Fatalf("CloseWrite failed: %v", err)
		}
	}

	var response struct {
		OK                 bool   `json:"ok"`
		Error              string `json:"error,omitempty"`
		ID                 string `json:"id,omitempty"`
		ContentKeyHex      string `json:"content_key_hex,omitempty"`
		ContentKeyEnvelope string `json:"content_key_envelope,omitempty"`
	}
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !response.OK {
		t.Fatalf("upload response failed: %+v", response)
	}
	if response.ID != "com.spaceaware.test-protocol" {
		t.Fatalf("response id = %q", response.ID)
	}
	if response.ContentKeyHex != "" || response.ContentKeyEnvelope != "" {
		t.Fatalf("upload response exposed content key material: %+v", response)
	}
	select {
	case got := <-publishedID:
		if got != "com.spaceaware.test-protocol" {
			t.Fatalf("published plugin id = %q", got)
		}
	default:
		t.Fatal("expected after-upload publication callback")
	}

	asset, ok := reg.Get("com.spaceaware.test-protocol")
	if !ok {
		t.Fatal("expected uploaded asset in registry")
	}
	if asset.BundleSHA256 != hex.EncodeToString(bundleHash[:]) {
		t.Fatalf("BundleSHA256 = %q, want %q", asset.BundleSHA256, hex.EncodeToString(bundleHash[:]))
	}
	if got, _, err := reg.ReadEncryptedBundle("com.spaceaware.test-protocol"); err != nil {
		t.Fatalf("ReadEncryptedBundle failed: %v", err)
	} else if !bytes.Equal(got, encryptedBundle) {
		t.Fatalf("encrypted bundle bytes changed")
	}
	if got, err := reg.ReadBundleKeyWithProviderKey("com.spaceaware.test-protocol", providerPrivateKey, providerHost.ID().String()); err != nil {
		t.Fatalf("ReadBundleKeyWithProviderKey failed: %v", err)
	} else if !bytes.Equal(got, contentKey) {
		t.Fatalf("content key bytes changed")
	}

	rawCatalog, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if strings.Contains(string(rawCatalog), "key_path") {
		t.Fatalf("catalog should not contain key_path: %s", rawCatalog)
	}
	if !strings.Contains(string(rawCatalog), "key_envelope_path") {
		t.Fatalf("catalog should contain key_envelope_path: %s", rawCatalog)
	}
	if _, err := os.Stat(filepath.Join(root, "com.spaceaware.test-protocol", "bundle.key")); !os.IsNotExist(err) {
		t.Fatalf("bundle.key must not exist")
	}
}
