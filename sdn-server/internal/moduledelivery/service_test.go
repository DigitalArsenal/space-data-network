package moduledelivery

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	schema "github.com/spacedatanetwork/sdn-server/internal/moduledelivery/schema/v1"
)

func TestServiceHandleMessageChallengeAndGrantFlow(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	writeEncryptedModuleCatalog(t, baseDir, []byte("encrypted-module-bundle"), bytes.Repeat([]byte{0x42}, 32))

	var ipfsAddCalls atomic.Int32
	ipfsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/add" {
			http.NotFound(w, r)
			return
		}
		ipfsAddCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Hash":"bafy-module-cid","Size":"23"}`))
	}))
	defer ipfsServer.Close()

	svc, err := NewService(baseDir, "test-module-delivery", "12D3KooWProviderPeer", compressedProviderPublicKey(), ipfsServer.URL)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	defer svc.Close()

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	requesterEncryptionPriv, requesterEncryptionPub := requesterEncryptionKeyPair(t)
	clientPeerID, err := license.PeerIDFromEd25519PublicKey(clientPub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey failed: %v", err)
	}

	challengeBytes, err := svc.handleMessage(encodeGrantRequestEnvelope(t, grantRequestFixture{
		ReqID:                     "req-123",
		ModuleID:                  "com.space-data-network.fastest-path",
		ModuleVersion:             "0.5.22",
		RequesterPeerID:           clientPeerID,
		RequesterXPub:             "xpub-module-requester",
		RequesterSigningPublicKey: clientPub,
		RequesterEncryptionPubKey: requesterEncryptionPub,
		RequestedAtMilliseconds:   uint64(time.Now().UnixMilli()),
	}), "remote-peer-1")
	if err != nil {
		t.Fatalf("handleMessage(grant_request) failed: %v", err)
	}

	challengeMessage := schema.GetRootAsModuleDeliveryMessage(challengeBytes, 0)
	if got := challengeMessage.MessageType(); got != schema.ModuleDeliveryMessageTypeGRANT_CHALLENGE {
		t.Fatalf("challenge message type = %v", got)
	}
	challenge := challengeMessage.GrantChallenge(nil)
	if challenge == nil {
		t.Fatal("expected grant challenge payload")
	}
	if got := string(challenge.ProviderPeerId()); got != "12D3KooWProviderPeer" {
		t.Fatalf("provider peer id = %q", got)
	}

	signature := ed25519.Sign(clientPriv, challenge.ChallengeBytes())
	grantBytes, err := svc.handleMessage(encodeGrantProofEnvelope(t, grantProofFixture{
		ReqID:                     "req-123",
		ModuleID:                  "com.space-data-network.fastest-path",
		ModuleVersion:             "0.5.22",
		RequesterPeerID:           clientPeerID,
		RequesterSigningPublicKey: clientPub,
		RequesterEncryptionPubKey: requesterEncryptionPub,
		Challenge:                 challenge.ChallengeBytes(),
		Signature:                 signature,
		ProvedAtMilliseconds:      uint64(time.Now().UnixMilli()),
	}), "remote-peer-1")
	if err != nil {
		t.Fatalf("handleMessage(grant_proof) failed: %v", err)
	}

	grantMessage := schema.GetRootAsModuleDeliveryMessage(grantBytes, 0)
	if got := grantMessage.MessageType(); got != schema.ModuleDeliveryMessageTypeGRANT_RESPONSE {
		t.Fatalf("grant message type = %v", got)
	}
	grant := grantMessage.GrantResponse(nil)
	if grant == nil {
		t.Fatal("expected grant response payload")
	}
	if got := string(grant.ReqId()); got != "req-123" {
		t.Fatalf("grant req id = %q", got)
	}
	if got := string(grant.EntitlementStatus()); got != "active" {
		t.Fatalf("entitlement status = %q", got)
	}
	if grant.BundleDescriptor(nil) == nil {
		t.Fatal("expected bundle descriptor")
	}
	if got := string(grant.BundleDescriptor(nil).Cid()); got != "bafy-module-cid" {
		t.Fatalf("bundle cid = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).ModuleId()); got != "com.space-data-network.fastest-path" {
		t.Fatalf("bundle module id = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).ModuleVersion()); got != "0.5.22" {
		t.Fatalf("bundle module version = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).Abi()); got != "module-sdk/async-host-v1" {
		t.Fatalf("bundle abi = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).Entrypoint()); got != "plugin_invoke_stream" {
		t.Fatalf("bundle entrypoint = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).PublicationCid()); got != "bafy-module-cid" {
		t.Fatalf("bundle publication cid = %q", got)
	}
	if got := string(grant.BundleDescriptor(nil).EncryptionCodec()); got != "x25519-hkdf-sha256-aes-256-gcm" {
		t.Fatalf("bundle encryption codec = %q", got)
	}
	if grant.WrappedContentKey(nil) == nil {
		t.Fatal("expected wrapped content key")
	}
	if got := string(grant.WrappedContentKey(nil).WrappingAlgorithm()); got != wrapAlgorithm {
		t.Fatalf("wrapping algorithm = %q", got)
	}
	if got := grant.WrappedContentKey(nil).RecipientPublicKeyBytes(); !bytes.Equal(got, requesterEncryptionPub) {
		t.Fatalf("wrapped key recipient public key = %x", got)
	}
	unwrappedKey := unwrapWrappedContentKey(t, grant.WrappedContentKey(nil), requesterEncryptionPriv, requesterEncryptionPub)
	if want := bytes.Repeat([]byte{0x42}, 32); !bytes.Equal(unwrappedKey, want) {
		t.Fatalf("unwrapped bundle key = %x, want %x", unwrappedKey, want)
	}
	if got := ipfsAddCalls.Load(); got != 1 {
		t.Fatalf("IPFS add calls = %d, want 1", got)
	}
}

func TestServiceHandleGrantRequestRejectsRequestedModuleVersionMismatch(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	writeEncryptedModuleCatalog(t, baseDir, []byte("encrypted-module-bundle"), bytes.Repeat([]byte{0x42}, 32))

	ipfsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected IPFS publication for rejected request")
	}))
	defer ipfsServer.Close()

	svc, err := NewService(baseDir, "test-module-delivery", "12D3KooWProviderPeer", compressedProviderPublicKey(), ipfsServer.URL)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	defer svc.Close()

	clientPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	_, requesterEncryptionPub := requesterEncryptionKeyPair(t)
	clientPeerID, err := license.PeerIDFromEd25519PublicKey(clientPub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey failed: %v", err)
	}

	responseBytes, err := svc.handleMessage(encodeGrantRequestEnvelope(t, grantRequestFixture{
		ReqID:                     "req-version-mismatch",
		ModuleID:                  "com.space-data-network.fastest-path",
		ModuleVersion:             "9.9.9",
		RequesterPeerID:           clientPeerID,
		RequesterXPub:             "xpub-module-requester",
		RequesterSigningPublicKey: clientPub,
		RequesterEncryptionPubKey: requesterEncryptionPub,
		RequestedAtMilliseconds:   uint64(time.Now().UnixMilli()),
	}), "remote-peer-1")
	if err != nil {
		t.Fatalf("handleMessage(grant_request) failed: %v", err)
	}

	message := schema.GetRootAsModuleDeliveryMessage(responseBytes, 0)
	if got := message.MessageType(); got != schema.ModuleDeliveryMessageTypeERROR_RESPONSE {
		t.Fatalf("message type = %v, want error response", got)
	}
	errResp := message.ErrorResponse(nil)
	if errResp == nil {
		t.Fatal("expected error response payload")
	}
	if got := string(errResp.Code()); got != "module_version_mismatch" {
		t.Fatalf("error code = %q", got)
	}
}

type grantRequestFixture struct {
	ReqID                     string
	ModuleID                  string
	ModuleVersion             string
	RequesterPeerID           string
	RequesterXPub             string
	RequesterSigningPublicKey []byte
	RequesterEncryptionPubKey []byte
	RequestedAtMilliseconds   uint64
}

type grantProofFixture struct {
	ReqID                     string
	ModuleID                  string
	ModuleVersion             string
	RequesterPeerID           string
	RequesterSigningPublicKey []byte
	RequesterEncryptionPubKey []byte
	Challenge                 []byte
	Signature                 []byte
	ProvedAtMilliseconds      uint64
}

func encodeGrantRequestEnvelope(t *testing.T, fixture grantRequestFixture) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(256)

	reqID := builder.CreateString(fixture.ReqID)
	moduleID := builder.CreateString(fixture.ModuleID)
	moduleVersion := builder.CreateString(fixture.ModuleVersion)
	requesterPeerID := builder.CreateString(fixture.RequesterPeerID)
	requesterXPub := builder.CreateString(fixture.RequesterXPub)
	signingPub := builder.CreateByteVector(fixture.RequesterSigningPublicKey)
	encryptionPub := builder.CreateByteVector(fixture.RequesterEncryptionPubKey)

	schema.GrantRequestStart(builder)
	schema.GrantRequestAddReqId(builder, reqID)
	schema.GrantRequestAddModuleId(builder, moduleID)
	schema.GrantRequestAddModuleVersion(builder, moduleVersion)
	schema.GrantRequestAddRequesterPeerId(builder, requesterPeerID)
	schema.GrantRequestAddRequesterXpub(builder, requesterXPub)
	schema.GrantRequestAddRequesterSigningPublicKey(builder, signingPub)
	schema.GrantRequestAddRequesterEncryptionPublicKey(builder, encryptionPub)
	schema.GrantRequestAddRequestedAtMs(builder, fixture.RequestedAtMilliseconds)
	requestOffset := schema.GrantRequestEnd(builder)

	schema.ModuleDeliveryMessageStart(builder)
	schema.ModuleDeliveryMessageAddMessageType(builder, schema.ModuleDeliveryMessageTypeGRANT_REQUEST)
	schema.ModuleDeliveryMessageAddGrantRequest(builder, requestOffset)
	messageOffset := schema.ModuleDeliveryMessageEnd(builder)
	schema.FinishModuleDeliveryMessageBuffer(builder, messageOffset)
	return builder.FinishedBytes()
}

func encodeGrantProofEnvelope(t *testing.T, fixture grantProofFixture) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(256)

	reqID := builder.CreateString(fixture.ReqID)
	moduleID := builder.CreateString(fixture.ModuleID)
	moduleVersion := builder.CreateString(fixture.ModuleVersion)
	requesterPeerID := builder.CreateString(fixture.RequesterPeerID)
	signingPub := builder.CreateByteVector(fixture.RequesterSigningPublicKey)
	encryptionPub := builder.CreateByteVector(fixture.RequesterEncryptionPubKey)
	challenge := builder.CreateByteVector(fixture.Challenge)
	signature := builder.CreateByteVector(fixture.Signature)

	schema.GrantProofStart(builder)
	schema.GrantProofAddReqId(builder, reqID)
	schema.GrantProofAddModuleId(builder, moduleID)
	schema.GrantProofAddModuleVersion(builder, moduleVersion)
	schema.GrantProofAddRequesterPeerId(builder, requesterPeerID)
	schema.GrantProofAddRequesterSigningPublicKey(builder, signingPub)
	schema.GrantProofAddRequesterEncryptionPublicKey(builder, encryptionPub)
	schema.GrantProofAddChallenge(builder, challenge)
	schema.GrantProofAddSignature(builder, signature)
	schema.GrantProofAddProvedAtMs(builder, fixture.ProvedAtMilliseconds)
	proofOffset := schema.GrantProofEnd(builder)

	schema.ModuleDeliveryMessageStart(builder)
	schema.ModuleDeliveryMessageAddMessageType(builder, schema.ModuleDeliveryMessageTypeGRANT_PROOF)
	schema.ModuleDeliveryMessageAddGrantProof(builder, proofOffset)
	messageOffset := schema.ModuleDeliveryMessageEnd(builder)
	schema.FinishModuleDeliveryMessageBuffer(builder, messageOffset)
	return builder.FinishedBytes()
}

func writeEncryptedModuleCatalog(t *testing.T, baseDir string, encryptedBundle, bundleKey []byte) {
	t.Helper()

	pluginRoot := filepath.Join(baseDir, "license", "plugins")
	if err := os.MkdirAll(pluginRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "fastest-path.wasm.enc"), encryptedBundle, 0o600); err != nil {
		t.Fatalf("WriteFile(encrypted bundle) failed: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(pluginRoot, "fastest-path.key"),
		[]byte(base64.RawStdEncoding.EncodeToString(bundleKey)),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(bundle key) failed: %v", err)
	}

	catalog := license.PluginCatalogFile{
		Plugins: []license.PluginCatalogEntry{
			{
				ID:            "com.space-data-network.fastest-path",
				Version:       "0.5.22",
				RequiredScope: "orbpro:base",
				EncryptedPath: "fastest-path.wasm.enc",
				KeyPath:       "fastest-path.key",
				ContentType:   "application/wasm+encrypted",
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "catalog.json"), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}
}

func compressedProviderPublicKey() []byte {
	value, _ := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	return value
}

func requesterEncryptionKeyPair(t *testing.T) ([]byte, []byte) {
	t.Helper()

	privateKey := bytes.Repeat([]byte{0x33}, 32)
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive requester encryption public key: %v", err)
	}
	return privateKey, publicKey
}

func unwrapWrappedContentKey(t *testing.T, wrapped *schema.WrappedContentKey, recipientPrivateKey, recipientPublicKey []byte) []byte {
	t.Helper()
	if wrapped == nil {
		t.Fatal("wrapped content key is nil")
	}

	derivedRecipientPublicKey, err := curve25519.X25519(recipientPrivateKey, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive recipient public key: %v", err)
	}
	if !bytes.Equal(derivedRecipientPublicKey, recipientPublicKey) {
		t.Fatalf("derived recipient public key = %x, want %x", derivedRecipientPublicKey, recipientPublicKey)
	}

	sharedSecret, err := curve25519.X25519(recipientPrivateKey, wrapped.EphemeralPublicKeyBytes())
	if err != nil {
		t.Fatalf("derive shared secret: %v", err)
	}
	derivedKey := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, sharedSecret, nil, wrapInfo), derivedKey); err != nil {
		t.Fatalf("derive wrapping key: %v", err)
	}

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		t.Fatalf("aes.NewCipher failed: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM failed: %v", err)
	}

	sealed := append(append([]byte(nil), wrapped.CiphertextBytes()...), wrapped.TagBytes()...)
	plaintext, err := gcm.Open(nil, wrapped.NonceBytes(), sealed, nil)
	if err != nil {
		t.Fatalf("gcm.Open failed: %v", err)
	}
	return plaintext
}
