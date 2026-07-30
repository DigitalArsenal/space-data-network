package node

import (
	"bytes"
	"testing"

	lcf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
)

// Regression cover for sdn-module-delivery-grant-sig-broken.
//
// The licensing guest signs grants through the host keyslot.sign oracle and so
// can never derive the provider's public key itself; it publishes whatever the
// host puts in LCF.PROVIDER_SIGNING_KEY.PUBLIC_KEY as the grant's
// GRANT_VERIFIER_PUBKEY, and that is the key every browser client verifies the
// grant signature against. The host omitted the field from 2026-07-09 (module
// commit 54bd4be removed keyslot.get) until this fix, so every grant carried 32
// zero bytes and every browser failed with "licensing grant provider signature
// verification failed" — while the Go delivery client, which does not verify
// provider signatures, saw nothing wrong.

func testLicensingNodeContext(t *testing.T) (*modulert.NodeContext, ed25519.PublicKey) {
	t.Helper()

	signingSeed := bytes.Repeat([]byte{0x11}, ed25519.SeedSize)
	wrappingKey := bytes.Repeat([]byte{0x22}, 32)
	return &modulert.NodeContext{
		PeerID: "provider.orbpro.test",
		KeySlots: map[string][]byte{
			providerSigningSlotID:  signingSeed,
			providerWrappingSlotID: wrappingKey,
		},
		KeySlotAlgorithms: map[string]string{
			providerSigningSlotID:  modulert.KeySlotAlgorithmEd25519,
			providerWrappingSlotID: modulert.KeySlotAlgorithmX25519,
		},
	}, ed25519.NewKeyFromSeed(signingSeed).Public().(ed25519.PublicKey)
}

func TestLicensingRuntimeConfigFramePublishesProviderSigningPublicKey(t *testing.T) {
	t.Parallel()

	nodeCtx, wantPublicKey := testLicensingNodeContext(t)

	frame, gotPublicKey, err := buildLicensingRuntimeConfigFrame(nodeCtx)
	if err != nil {
		t.Fatalf("buildLicensingRuntimeConfigFrame() failed: %v", err)
	}
	if !bytes.Equal(gotPublicKey, wantPublicKey) {
		t.Fatalf("returned provider signing public key = %x, want %x", gotPublicKey, wantPublicKey)
	}

	signingKey := lcf.GetRootAsLCF(frame, 0).PROVIDER_SIGNING_KEY(nil)
	if signingKey == nil {
		t.Fatal("LCF omits PROVIDER_SIGNING_KEY")
	}
	published := signingKey.PUBLIC_KEYBytes()
	if len(published) == 0 {
		t.Fatal("LCF PROVIDER_SIGNING_KEY.PUBLIC_KEY is absent — the guest would stamp 32 zero bytes into every grant and no client could verify one")
	}
	if !bytes.Equal(published, wantPublicKey) {
		t.Fatalf("LCF PROVIDER_SIGNING_KEY.PUBLIC_KEY = %x, want %x", published, wantPublicKey)
	}

	// The published key must be the public half of the SAME slot keyslot.sign
	// uses. Deriving it from anywhere else ships a self-consistent lie: every
	// host check passes and every client verify fails.
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(nodeCtx.KeySlots[providerSigningSlotID]), []byte("grant-bytes"))
	if !ed25519.Verify(published, []byte("grant-bytes"), signature) {
		t.Fatal("published verifier key does not verify a signature made with the provider signing slot")
	}

	// The wrapping key's public half is deliberately NOT published: the guest
	// learns it from the requester's ECDH envelope, and a key reference should
	// assert nothing it does not need to.
	if wrappingKey := lcf.GetRootAsLCF(frame, 0).PROVIDER_WRAPPING_KEY(nil); wrappingKey != nil {
		if got := wrappingKey.PUBLIC_KEYBytes(); len(got) != 0 {
			t.Fatalf("LCF PROVIDER_WRAPPING_KEY.PUBLIC_KEY = %x, want absent", got)
		}
	}
}

func TestLicensingRuntimeConfigFrameFailsClosedWithoutAUsableSigningSlot(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*modulert.NodeContext){
		"undeclared algorithm": func(nodeCtx *modulert.NodeContext) {
			delete(nodeCtx.KeySlotAlgorithms, providerSigningSlotID)
		},
		"slot declared for the wrong algorithm": func(nodeCtx *modulert.NodeContext) {
			nodeCtx.KeySlotAlgorithms[providerSigningSlotID] = modulert.KeySlotAlgorithmX25519
		},
	} {
		t.Run(name, func(t *testing.T) {
			nodeCtx, _ := testLicensingNodeContext(t)
			mutate(nodeCtx)

			// Refusing to configure is the honest outcome: a licensing runtime
			// that cannot state its own verifier key would otherwise come up
			// healthy, list every module, and issue grants nobody can verify.
			if _, _, err := buildLicensingRuntimeConfigFrame(nodeCtx); err == nil {
				t.Fatal("buildLicensingRuntimeConfigFrame() succeeded without a usable provider signing slot; want refusal")
			}
		})
	}
}

func TestVerifyLicensingStatusVerifierKeyRejectsAnUnverifiableRuntime(t *testing.T) {
	t.Parallel()

	nodeCtx, wantPublicKey := testLicensingNodeContext(t)
	otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x33}, ed25519.SeedSize)).Public().(ed25519.PublicKey)

	if err := verifyLicensingStatusVerifierKey(licensingStatusFrame(t, nodeCtx, wantPublicKey), wantPublicKey); err != nil {
		t.Fatalf("verifyLicensingStatusVerifierKey() rejected a matching status: %v", err)
	}

	// The exact live failure: a guest that kept 32 zero bytes reports them, and
	// every grant it issues is unverifiable. This must fail the bootstrap.
	if err := verifyLicensingStatusVerifierKey(
		licensingStatusFrame(t, nodeCtx, make([]byte, ed25519.PublicKeySize)),
		wantPublicKey,
	); err == nil {
		t.Fatal("verifyLicensingStatusVerifierKey() admitted an all-zero verifier key")
	}
	if err := verifyLicensingStatusVerifierKey(licensingStatusFrame(t, nodeCtx, nil), wantPublicKey); err == nil {
		t.Fatal("verifyLicensingStatusVerifierKey() admitted a status with no verifier key")
	}
	if err := verifyLicensingStatusVerifierKey(licensingStatusFrame(t, nodeCtx, otherKey), wantPublicKey); err == nil {
		t.Fatal("verifyLicensingStatusVerifierKey() admitted a verifier key the host never sent")
	}
}

// licensingStatusFrame builds the $LCF status a licensing guest returns from
// server_configure_runtime, reporting publicKey as the verifier key it retained
// (key_server.cpp build_lcf_status_bytes echoes g_provider_signing_public here).
func licensingStatusFrame(t *testing.T, nodeCtx *modulert.NodeContext, publicKey []byte) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	providerPeerIDOffset := builder.CreateString(nodeCtx.PeerID)
	signingKeyRefOffset := buildKeyReferenceFrame(
		builder,
		providerSigningKeyID,
		providerSigningSlotID,
		keyReferenceRoleProviderSigning,
		keyReferenceAlgorithmEd25519Seed,
		1,
		publicKey,
	)

	lcf.LCFStart(builder)
	lcf.LCFAddMESSAGE_TYPE(builder, 1)
	lcf.LCFAddROLE(builder, licensingConfigRoleProvider)
	lcf.LCFAddPROVIDER_PEER_ID(builder, providerPeerIDOffset)
	lcf.LCFAddPROVIDER_SIGNING_KEY(builder, signingKeyRefOffset)
	root := lcf.LCFEnd(builder)
	lcf.FinishLCFBuffer(builder, root)
	return builder.FinishedBytes()
}
