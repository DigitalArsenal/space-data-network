package node

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

func TestCatalogPublicationAssetsSkipsLicensingRuntimeModule(t *testing.T) {
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
			AllowedDomains:    []string{"localhost"},
			MaxGrantTimeoutMs: 30_000,
		},
	)

	assets := catalogPublicationAssets(reg)
	if len(assets) != 1 {
		t.Fatalf("catalogPublicationAssets() count = %d, want 1", len(assets))
	}
	if got := assets[0].ID; got != "com.orbpro.sgp4" {
		t.Fatalf("catalogPublicationAssets()[0].ID = %q, want com.orbpro.sgp4", got)
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

	wasmPath := filepath.Clean(filepath.Join(
		"..", "..", "..", "..",
		"space-data-network-plugins",
		"packages",
		"licensing",
		"dist",
		"isomorphic",
		"module.wasm",
	))
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	nodeCtx := &modulert.NodeContext{
		PeerID: "provider.orbpro.test",
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
