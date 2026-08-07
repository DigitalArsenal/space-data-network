package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	lcf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
)

func TestCatalogPublicationAssetsIncludesLicensingRuntimeModule(t *testing.T) {
	t.Parallel()

	reg := writeTestPluginRegistry(
		t,
		license.PluginCatalogEntry{
			ID:            licensingModuleID,
			Version:       "0.1.0",
			RequiredScope: "orbpro:runtime",
			EncryptedPath: "licensing.wasm.enc",
			KeyPath:       "licensing.key",
			ContentType:   "application/wasm+encrypted",
		},
		license.PluginCatalogEntry{
			ID:                "com.orbpro.sgp4",
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "sgp4.wasm.enc",
			KeyPath:           "sgp4.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)

	assets := catalogPublicationAssets(reg)
	if len(assets) != 2 {
		t.Fatalf("catalogPublicationAssets() count = %d, want 2", len(assets))
	}
	if got := assets[0].ID; got != "com.orbpro.sgp4" {
		t.Fatalf("catalogPublicationAssets()[0].ID = %q, want com.orbpro.sgp4", got)
	}
	if got := assets[1].ID; got != licensingModuleID {
		t.Fatalf("catalogPublicationAssets()[1].ID = %q, want %q", got, licensingModuleID)
	}
}

func TestBootstrapLicensingModulePublishesCatalogModulesAndHandlesChallenge(t *testing.T) {
	t.Parallel()

	reg := writeTestPluginRegistry(
		t,
		license.PluginCatalogEntry{
			ID:                "com.orbpro.sgp4",
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "sgp4.wasm.enc",
			KeyPath:           "sgp4.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)

	mod := newLicensingTestModule(t)
	defer func() {
		if err := mod.Close(); err != nil {
			t.Fatalf("Close() failed: %v", err)
		}
	}()

	if err := bootstrapLicensingModule(mod, reg); err != nil {
		t.Fatalf("bootstrapLicensingModule() failed: %v", err)
	}

	challengeResponse, err := mod.InvokeMethod(
		context.Background(),
		"server_handle_message",
		buildChallengeRequestFrame(
			"req-sgp4-001",
			"com.orbpro.sgp4",
			"1.0.0",
			"requester.orbpro.test",
			"localhost",
			"provider.orbpro.test",
		),
	)
	if err != nil {
		t.Fatalf("InvokeMethod(server_handle_message) failed: %v", err)
	}
	if !flatbuffers.BufferHasIdentifier(challengeResponse, "$LCH") {
		t.Fatalf("expected LCH challenge response identifier, got %q", challengeResponse)
	}

	messageType, role := decodeChallengeHeader(t, challengeResponse)
	if messageType != 1 {
		t.Fatalf("challenge response MESSAGE_TYPE = %d, want 1 (Response)", messageType)
	}
	if role != 1 {
		t.Fatalf("challenge response ROLE = %d, want 1 (Provider)", role)
	}
}

func TestBuildModuleNodeContextFallsBackToServerIdentity(t *testing.T) {
	t.Parallel()

	basePath := t.TempDir()
	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}
	identity, err := keyMgr.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() failed: %v", err)
	}

	n := &Node{
		config: &config.Config{
			Storage: config.StorageConfig{
				Path: filepath.Join(basePath, "data"),
			},
		},
	}

	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		t.Fatalf("buildModuleNodeContext() failed: %v", err)
	}

	if got := nodeCtx.KeySlots[providerSigningSlotID]; !bytes.Equal(got, identity.SigningKey.PrivateKey[:32]) {
		t.Fatalf("provider signing slot = %x, want %x", got, identity.SigningKey.PrivateKey[:32])
	}
	if got := nodeCtx.KeySlots[providerWrappingSlotID]; !bytes.Equal(got, identity.EncryptionKey.PrivateKey) {
		t.Fatalf("provider wrapping slot = %x, want %x", got, identity.EncryptionKey.PrivateKey)
	}
	// Loop B9.5: every provisioned slot must carry an algorithm declaration,
	// or the keyslot oracle fails closed and the slot is unusable.
	if got := nodeCtx.KeySlotAlgorithms[providerSigningSlotID]; got != modulert.KeySlotAlgorithmEd25519 {
		t.Fatalf("provider signing slot algorithm = %q, want %q", got, modulert.KeySlotAlgorithmEd25519)
	}
	if got := nodeCtx.KeySlotAlgorithms[providerWrappingSlotID]; got != modulert.KeySlotAlgorithmX25519 {
		t.Fatalf("provider wrapping slot algorithm = %q, want %q", got, modulert.KeySlotAlgorithmX25519)
	}
}

func TestBuildPublicationContentKeyFrameUsesDecryptKeyForRecProtectedArtifacts(t *testing.T) {
	t.Parallel()

	asset := &license.PluginAsset{
		ID:      "com.orbpro.rec-protected",
		Version: "2.0.0",
	}
	keyBytes := bytes.Repeat([]byte{0x7b}, 32)

	frame, err := buildPublicationContentKeyFrame(asset, buildRecProtectedContentFixture(t), keyBytes)
	if err != nil {
		t.Fatalf("buildPublicationContentKeyFrame() failed: %v", err)
	}

	keyMaterial := kmf.GetRootAsKMF(frame, 0)
	if got := keyMaterial.ROLE(); got != keyMaterialRoleDecryptKey {
		t.Fatalf("KMF ROLE = %d, want %d (DecryptKey)", got, keyMaterialRoleDecryptKey)
	}
	if got := keyMaterial.ALGORITHM(); got != keyMaterialAlgorithmX25519Private {
		t.Fatalf("KMF ALGORITHM = %d, want %d (X25519Private)", got, keyMaterialAlgorithmX25519Private)
	}
}

func TestPublicationContentKeyFrameEmitsKMFIdentifier(t *testing.T) {
	t.Parallel()

	asset := &license.PluginAsset{
		ID:      "com.orbpro.rec-protected",
		Version: "2.0.0",
	}
	frame, err := buildPublicationContentKeyFrame(asset, buildRecProtectedContentFixture(t), bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("buildPublicationContentKeyFrame() failed: %v", err)
	}
	if !kmf.KMFBufferHasIdentifier(frame) {
		t.Fatalf("publication content key frame is not a $KMF FlatBuffer")
	}
}

func TestFindPluginDecryptPrivateKeyFallsBackToServerIdentity(t *testing.T) {
	t.Parallel()

	basePath := t.TempDir()
	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}
	identity, err := keyMgr.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() failed: %v", err)
	}

	n := &Node{
		config: &config.Config{
			Storage: config.StorageConfig{
				Path: filepath.Join(basePath, "data"),
			},
		},
	}

	key, err := n.findPluginDecryptPrivateKey()
	if err != nil {
		t.Fatalf("findPluginDecryptPrivateKey() failed: %v", err)
	}
	if !bytes.Equal(key, identity.EncryptionKey.PrivateKey) {
		t.Fatalf("findPluginDecryptPrivateKey() = %x, want %x", key, identity.EncryptionKey.PrivateKey)
	}
}

func newLicensingTestModule(t *testing.T) *modulert.Module {
	t.Helper()

	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	// loop B1: the real licensing manifest declares sensitive capabilities
	// (ipfs, protocol_dial, wallet_sign) that now require an explicit
	// operator approval before NewModule will load it (default-deny). This
	// helper backs every test in this file with a permissive in-memory
	// policy pre-approving exactly what the artifact declares, so bootstrap
	// behavior is still exercised end-to-end; approval enforcement itself
	// is covered by capability_policy_test.go / module tests in
	// internal/modulert.
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	moduleHash := modulert.ContentHashHex(wasmBytes)
	for _, capability := range []string{"ipfs", "protocol_dial", "wallet_sign"} {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "licensing",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	nodeCtx := &modulert.NodeContext{
		PeerID:           "provider.orbpro.test",
		CapabilityPolicy: policy,
		KeySlots: map[string][]byte{
			providerSigningSlotID: {
				1, 2, 3, 4, 5, 6, 7, 8,
				9, 10, 11, 12, 13, 14, 15, 16,
				17, 18, 19, 20, 21, 22, 23, 24,
				25, 26, 27, 28, 29, 30, 31, 32,
			},
			providerWrappingSlotID: {
				32, 31, 30, 29, 28, 27, 26, 25,
				24, 23, 22, 21, 20, 19, 18, 17,
				16, 15, 14, 13, 12, 11, 10, 9,
				8, 7, 6, 5, 4, 3, 2, 1,
			},
		},
	}

	capReg := modulert.NewCapabilityRegistry()
	cryptoFactory := caps.NewCryptoCapFactory()
	capReg.Register("crypto_hash", cryptoFactory)
	capReg.Register("crypto_sign", cryptoFactory)
	capReg.Register("crypto_verify", cryptoFactory)
	capReg.Register("crypto_encrypt", cryptoFactory)
	capReg.Register("crypto_decrypt", cryptoFactory)
	capReg.Register("crypto_key_agreement", cryptoFactory)
	capReg.Register("crypto_kdf", cryptoFactory)
	capReg.Register("wallet_sign", caps.NewKeyslotCapFactory())
	capReg.Register("ipfs", func(_ *modulert.Module) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			return fakeIPFSCapResponse(t, operation, payload), nil
		}
	})

	mod, err := modulert.NewModule(wasmBytes, capReg, nodeCtx)
	if err != nil {
		t.Fatalf("NewModule() failed: %v", err)
	}
	return mod
}

func fakeIPFSCapResponse(t *testing.T, operation string, payload []byte) []byte {
	t.Helper()

	if operation != "ipfs.add" {
		t.Fatalf("unexpected IPFS operation %q", operation)
	}
	return []byte(`{"ok":true,"result":{"Hash":"bafytestcid000000000000000000000000","Size":16}}`)
}

func buildChallengeRequestFrame(
	requestID string,
	moduleID string,
	moduleVersion string,
	requesterPeerID string,
	requestedDomain string,
	providerPeerID string,
) []byte {
	builder := flatbuffers.NewBuilder(512)

	requestIDOffset := builder.CreateString(requestID)
	moduleIDOffset := builder.CreateString(moduleID)
	moduleVersionOffset := builder.CreateString(moduleVersion)
	requesterPeerIDOffset := builder.CreateString(requesterPeerID)
	requesterXPubOffset := builder.CreateString("xpub-test-requester")
	requesterSigningPubKeyOffset := builder.CreateByteVector([]byte{
		11, 12, 13, 14, 15, 16, 17, 18,
		19, 20, 21, 22, 23, 24, 25, 26,
		27, 28, 29, 30, 31, 32, 33, 34,
		35, 36, 37, 38, 39, 40, 41, 42,
	})
	requesterEphemeralPubKeyOffset := builder.CreateByteVector([]byte{
		42, 41, 40, 39, 38, 37, 36, 35,
		34, 33, 32, 31, 30, 29, 28, 27,
		26, 25, 24, 23, 22, 21, 20, 19,
		18, 17, 16, 15, 14, 13, 12, 11,
	})
	requestedDomainOffset := builder.CreateString(requestedDomain)
	providerPeerIDOffset := builder.CreateString(providerPeerID)

	builder.StartObject(17)
	builder.PrependByteSlot(0, 0, 0)
	builder.PrependByteSlot(1, 0, 0)
	builder.PrependUOffsetTSlot(2, requestIDOffset, 0)
	builder.PrependUOffsetTSlot(3, moduleIDOffset, 0)
	builder.PrependUOffsetTSlot(4, moduleVersionOffset, 0)
	builder.PrependUOffsetTSlot(5, requesterPeerIDOffset, 0)
	builder.PrependUOffsetTSlot(6, requesterXPubOffset, 0)
	builder.PrependUOffsetTSlot(7, requesterSigningPubKeyOffset, 0)
	builder.PrependUOffsetTSlot(8, requesterEphemeralPubKeyOffset, 0)
	builder.PrependUOffsetTSlot(9, requestedDomainOffset, 0)
	builder.PrependUint64Slot(10, 30_000, 0)
	builder.PrependUint64Slot(11, uint64(time.Now().UnixMilli()), 0)
	builder.PrependUOffsetTSlot(14, providerPeerIDOffset, 0)
	root := builder.EndObject()
	builder.FinishWithFileIdentifier(root, []byte("$LCH"))
	return builder.FinishedBytes()
}

func decodeChallengeHeader(t *testing.T, data []byte) (byte, byte) {
	t.Helper()

	root := &flatbuffers.Table{
		Bytes: data,
		Pos:   flatbuffers.GetUOffsetT(data),
	}

	var messageType byte
	if offset := root.Offset(4); offset != 0 {
		messageType = root.GetByte(flatbuffers.UOffsetT(offset) + root.Pos)
	}

	var role byte
	if offset := root.Offset(6); offset != 0 {
		role = root.GetByte(flatbuffers.UOffsetT(offset) + root.Pos)
	}

	return messageType, role
}

func buildRecProtectedContentFixture(t *testing.T) []byte {
	t.Helper()

	recordBuilder := flatbuffers.NewBuilder(128)
	versionOffset := recordBuilder.CreateString("1.0.0")
	standardOffset := recordBuilder.CreateString("ENC")

	recordBuilder.StartObject(3)
	recordBuilder.PrependByteSlot(0, 0, 0)
	recordBuilder.PrependUOffsetTSlot(2, standardOffset, 0)
	recordOffset := recordBuilder.EndObject()

	recordsOffset := recordBuilder.CreateVectorOfTables([]flatbuffers.UOffsetT{recordOffset})

	recordBuilder.StartObject(2)
	recordBuilder.PrependUOffsetTSlot(0, versionOffset, 0)
	recordBuilder.PrependUOffsetTSlot(1, recordsOffset, 0)
	recOffset := recordBuilder.EndObject()
	recordBuilder.FinishWithFileIdentifier(recOffset, []byte(publicationTrailerMagicText))

	recordCollectionBytes := recordBuilder.FinishedBytes()
	payloadBytes := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	footer := make([]byte, publicationTrailerFooterLength)
	binary.LittleEndian.PutUint32(footer[:4], uint32(len(recordCollectionBytes)))
	copy(footer[4:], []byte(publicationTrailerMagicText))

	protectedContent := append([]byte{}, payloadBytes...)
	protectedContent = append(protectedContent, recordCollectionBytes...)
	protectedContent = append(protectedContent, footer...)
	return protectedContent
}

// TestLicensingRuntimeConfigFramePublishesProviderSigningPublicKey is the regression
// guard for upstream-sdn-3. The node used to emit PROVIDER_SIGNING_KEY with no
// PUBLIC_KEY at all; the licensing key server then "left it zeroed when absent"
// (key_server.cpp:1107-1118) and stamped 32 zero bytes into every issued grant as
// GRANT_VERIFIER_PUBKEY, so ed25519.verify(msg, sig, 0x00*32) failed for EVERY browser
// client. Nothing on either side asserted the field was non-zero, which is why a
// one-line omission cost a full debugging cycle. Assert all three properties: present,
// correct length, and NOT all zero.
func TestLicensingRuntimeConfigFramePublishesProviderSigningPublicKey(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	wrapping := make([]byte, 32)
	for i := range wrapping {
		wrapping[i] = byte(0xA0 + i%16)
	}

	frame, err := buildLicensingRuntimeConfigFrame(&modulert.NodeContext{
		PeerID: "16Uiu2HAmTestPeerIDForLicensingConfigFrame",
		KeySlots: map[string][]byte{
			providerSigningSlotID:  seed,
			providerWrappingSlotID: wrapping,
		},
	})
	if err != nil {
		t.Fatalf("buildLicensingRuntimeConfigFrame: %v", err)
	}

	cfg := lcf.GetRootAsLCF(frame, 0)
	signingKey := cfg.PROVIDER_SIGNING_KEY(nil)
	if signingKey == nil {
		t.Fatal("PROVIDER_SIGNING_KEY is absent from the licensing config frame")
	}

	got := signingKey.PUBLIC_KEYBytes()
	if len(got) == 0 {
		t.Fatal("PROVIDER_SIGNING_KEY.PUBLIC_KEY is absent — the key server will zero it and every grant will fail verification (upstream-sdn-3)")
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("PROVIDER_SIGNING_KEY.PUBLIC_KEY must be %d bytes, got %d", ed25519.PublicKeySize, len(got))
	}
	if isAllZero(got) {
		t.Fatal("PROVIDER_SIGNING_KEY.PUBLIC_KEY is all zero bytes — this is the exact upstream-sdn-3 defect")
	}

	// It must be the key the SIGNER will actually use, i.e. byte-identical to the
	// derivation in internal/modulert/caps/keyslot.go.
	want := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(got, want) {
		t.Fatalf("PUBLIC_KEY does not match ed25519.NewKeyFromSeed(seed).Public()\n got: %x\nwant: %x", got, want)
	}

	// A grant signed by the slot must verify against the published key. This is the
	// end-to-end property the browser depends on.
	msg := []byte("upstream-sdn-3 grant verification round trip")
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg)
	if !ed25519.Verify(ed25519.PublicKey(got), msg, sig) {
		t.Fatal("signature made with the provider signing slot does not verify against the published PUBLIC_KEY")
	}
}

func TestBuildLicensingRuntimeConfigFrameRejectsShortSigningSeed(t *testing.T) {
	t.Parallel()

	if _, err := buildLicensingRuntimeConfigFrame(&modulert.NodeContext{
		PeerID: "16Uiu2HAmTestPeerIDForLicensingConfigFrame",
		KeySlots: map[string][]byte{
			providerSigningSlotID:  make([]byte, 16),
			providerWrappingSlotID: make([]byte, 32),
		},
	}); err == nil {
		t.Fatal("expected a short provider signing seed to be rejected, got nil error")
	}
}
