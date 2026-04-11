package moduledelivery

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

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
		RequesterEncryptionPubKey: bytes.Repeat([]byte{0x33}, 32),
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
		RequesterEncryptionPubKey: bytes.Repeat([]byte{0x33}, 32),
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
	if grant.WrappedContentKey(nil) == nil {
		t.Fatal("expected wrapped content key")
	}
	if got := string(grant.WrappedContentKey(nil).WrappingAlgorithm()); got != wrapAlgorithm {
		t.Fatalf("wrapping algorithm = %q", got)
	}
	if got := ipfsAddCalls.Load(); got != 1 {
		t.Fatalf("IPFS add calls = %d, want 1", got)
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
