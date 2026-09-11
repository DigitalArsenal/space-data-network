package node

import (
	"context"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

func TestSettledEntitlementBridgePublishesAndRevokesWithoutRestart(t *testing.T) {
	const (
		moduleID = "com.example.paid-module"
		buyerA   = "xpub-settled-buyer-a"
		buyerB   = "xpub-unpaid-buyer-b"
	)
	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyAllowlist},
		license.PluginCatalogEntry{
			ID:                moduleID,
			Version:           "1.0.0",
			RequiredScope:     "module:invoke",
			EncryptedPath:     "paid.wasm.enc",
			KeyPath:           "paid.key",
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

	bridge := NewSettledEntitlementBridge(&Node{pluginRegistry: reg, licensingModule: mod})
	if err := bridge.ProvisionSettledEntitlement(context.Background(), moduleID, buyerA); err != nil {
		t.Fatalf("ProvisionSettledEntitlement() failed: %v", err)
	}
	if err := bridge.ProvisionSettledEntitlement(context.Background(), moduleID, buyerA); err != nil {
		t.Fatalf("duplicate ProvisionSettledEntitlement() failed: %v", err)
	}
	asset, ok := reg.Get(moduleID)
	if !ok || len(asset.AllowedXpubs) != 1 || asset.AllowedXpubs[0] != buyerA {
		t.Fatalf("allowed xpubs after idempotent provision = %#v", asset)
	}
	assertLicensingChallengeOutcome(t, mod, moduleID, buyerA, 1, "")
	assertLicensingChallengeOutcome(t, mod, moduleID, buyerB, 2, "xpub_not_allowed")

	if err := bridge.RevokeSettledEntitlement(context.Background(), moduleID, buyerA); err != nil {
		t.Fatalf("RevokeSettledEntitlement() failed: %v", err)
	}
	asset, ok = reg.Get(moduleID)
	if !ok || len(asset.AllowedXpubs) != 0 {
		t.Fatalf("allowed xpubs after revoke = %#v", asset)
	}
	// The last-xpub case cannot be represented by publishing an empty vector:
	// the guest reads that as open. The bridge's in-process reset removes the
	// stale publication, so even the formerly paid buyer now gets module_not_found.
	assertLicensingChallengeOutcome(t, mod, moduleID, buyerA, 2, "module_not_found")
}

func TestSettledEntitlementBridgeDoesNotNarrowOpenPolicy(t *testing.T) {
	const moduleID = "com.example.open-module"
	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyOpen},
		license.PluginCatalogEntry{
			ID:            moduleID,
			Version:       "1.0.0",
			EncryptedPath: "open.wasm.enc",
			KeyPath:       "open.key",
		},
	)
	bridge := NewSettledEntitlementBridge(&Node{pluginRegistry: reg})
	if err := bridge.ProvisionSettledEntitlement(context.Background(), moduleID, "xpub-buyer"); err != nil {
		t.Fatal(err)
	}
	asset, _ := reg.Get(moduleID)
	if len(asset.AllowedXpubs) != 0 {
		t.Fatalf("open policy was narrowed by a settled purchase: %v", asset.AllowedXpubs)
	}
}

func assertLicensingChallengeOutcome(t *testing.T, mod interface {
	InvokeMethod(context.Context, string, []byte) ([]byte, error)
}, moduleID, xpub string, wantMessageType byte, wantErrorCode string) {
	t.Helper()
	response, err := mod.InvokeMethod(
		context.Background(),
		"server_handle_message",
		buildChallengeRequestFrameForXpub("req-"+xpub, moduleID, xpub),
	)
	if err != nil {
		t.Fatalf("server_handle_message(%s) failed: %v", xpub, err)
	}
	messageType, _ := decodeChallengeHeader(t, response)
	if messageType != wantMessageType {
		t.Fatalf("challenge MESSAGE_TYPE for %s = %d, want %d", xpub, messageType, wantMessageType)
	}
	if got := lchStringField(t, response, lchSlotErrorCode); got != wantErrorCode {
		t.Fatalf("challenge ERROR_CODE for %s = %q, want %q", xpub, got, wantErrorCode)
	}
}

func buildChallengeRequestFrameForXpub(requestID, moduleID, xpub string) []byte {
	builder := flatbuffers.NewBuilder(512)
	requestIDOffset := builder.CreateString(requestID)
	moduleIDOffset := builder.CreateString(moduleID)
	moduleVersionOffset := builder.CreateString("1.0.0")
	requesterPeerIDOffset := builder.CreateString("requester.orbpro.test")
	requesterXPubOffset := builder.CreateString(xpub)
	requesterSigningPubKeyOffset := builder.CreateByteVector([]byte{
		11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26,
		27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42,
	})
	requesterEphemeralPubKeyOffset := builder.CreateByteVector([]byte{
		42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 32, 31, 30, 29, 28, 27,
		26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11,
	})
	requestedDomainOffset := builder.CreateString("localhost")
	providerPeerIDOffset := builder.CreateString("provider.orbpro.test")

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
