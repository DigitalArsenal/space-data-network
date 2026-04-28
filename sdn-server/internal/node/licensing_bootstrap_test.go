package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
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
			ID:              licensingModuleID,
			Version:         "0.1.0",
			RequiredScope:   "orbpro:runtime",
			EncryptedPath:   "licensing.wasm.enc",
			KeyEnvelopePath: "licensing.key-envelope.json",
			ContentType:     "application/wasm+encrypted",
		},
		license.PluginCatalogEntry{
			ID:                "com.orbpro.sgp4",
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "sgp4.wasm.enc",
			KeyEnvelopePath:   "sgp4.key-envelope.json",
			ContentType:       "application/wasm+encrypted",
			AllowedDomains:    []string{"localhost"},
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
			KeyEnvelopePath:   "sgp4.key-envelope.json",
			ContentType:       "application/wasm+encrypted",
			AllowedDomains:    []string{"localhost"},
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

	wasmPath := testsupport.MustFindLicensingModuleWasmPath(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	wrappingKey := licensingTestProviderWrappingKey()
	nodeCtx := &modulert.NodeContext{
		PeerID:        licensingTestProviderPeerID,
		EncryptionKey: wrappingKey,
		KeySlots: map[string][]byte{
			providerSigningSlotID: {
				1, 2, 3, 4, 5, 6, 7, 8,
				9, 10, 11, 12, 13, 14, 15, 16,
				17, 18, 19, 20, 21, 22, 23, 24,
				25, 26, 27, 28, 29, 30, 31, 32,
			},
			providerWrappingSlotID: wrappingKey,
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
