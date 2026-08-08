package node

import (
	"context"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// The licensing challenge has TWO time bounds and only one of them is a
// security control. MAX_CLOCK_SKEW_MS bounds the disagreement between the
// requester's clock and the provider's; CHALLENGE_TTL_MS bounds the life of a
// server-minted nonce. The node used to publish 5 s for the first one, which
// turned a clock check into an end-to-end TRANSPORT budget: the browser stamps
// REQUESTED_AT before it encodes and before it dials, and re-sends the same
// stamped bytes to each candidate multiaddr in turn, so dial + handshake +
// every failed candidate is charged to a window named for clock drift. Live on
// host-01 that refused 24 of 160 challenge requests as invalid_timestamp while
// the nonce leg passed 132 of 132.
//
// These tests lock the shape of the fix: the transport-sized bound is generous,
// the freshness bound is untouched, and the skew check is still a real bound
// rather than a disabled one.

func TestLicensingConfigFrameBudgetsTransportNotJustClockDrift(t *testing.T) {
	t.Parallel()

	frame, err := buildLicensingRuntimeConfigFrame(&modulert.NodeContext{
		PeerID:   "provider.orbpro.test",
		KeySlots: testLicensingKeySlots(),
	})
	if err != nil {
		t.Fatalf("buildLicensingRuntimeConfigFrame: %v", err)
	}

	skew := lcfUint64Field(t, frame, lcfSlotMaxClockSkewMS)
	ttl := lcfUint64Field(t, frame, lcfSlotChallengeTTLMS)

	// 5_000 is the value that caused the outage. Naming it here means the
	// regression cannot come back silently under a different constant name.
	if skew == 5_000 {
		t.Fatalf("MAX_CLOCK_SKEW_MS is back to the 5s transport budget that refused live grants as invalid_timestamp")
	}
	if skew != licensingMaxClockSkewMS {
		t.Fatalf("MAX_CLOCK_SKEW_MS = %d, want %d", skew, licensingMaxClockSkewMS)
	}
	// The point of the number is that a browser's whole candidate walk —
	// discovery, dial, handshake, retries across every advertised multiaddr —
	// fits inside it with room to spare. Anything at or below ~30 s is back to
	// budgeting transport.
	if skew < 60_000 {
		t.Fatalf("MAX_CLOCK_SKEW_MS = %d is small enough to charge dial+retry latency to a clock check", skew)
	}

	// Freshness lives here and must NOT have moved: it is anchored to a
	// server-minted nonce and the provider's own clock, which is exactly why
	// widening the skew window costs nothing.
	if ttl != licensingChallengeTTLMS {
		t.Fatalf("CHALLENGE_TTL_MS = %d, want %d — the freshness bound must not move with the skew bound", ttl, licensingChallengeTTLMS)
	}
	if ttl != 30_000 {
		t.Fatalf("CHALLENGE_TTL_MS = %d, want the unchanged 30s nonce lifetime", ttl)
	}
}

// TestStaleStampedRequestStillGetsAChallenge is the behavioural half, run
// against the REAL licensing key server. A request stamped 60 s before it
// arrives is exactly the shape of the live failure — a browser that stamped,
// then spent its budget walking dead candidate addresses — and it must now be
// answered with a challenge. The second leg proves the check is still ARMED:
// an hour-stale stamp is still refused, so this is a corrected bound and not a
// removed one.
func TestStaleStampedRequestStillGetsAChallenge(t *testing.T) {
	t.Parallel()

	reg := writeGrantablePluginRegistry(
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

	for _, tc := range []struct {
		name        string
		stampedAgo  time.Duration
		wantRefusal bool
	}{
		{name: "60s stale — the live failure", stampedAgo: 60 * time.Second, wantRefusal: false},
		{name: "5m stale — inside the corrected window", stampedAgo: 4 * time.Minute, wantRefusal: false},
		{name: "1h stale — the bound is still armed", stampedAgo: time.Hour, wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := mod.InvokeMethod(
				context.Background(),
				"server_handle_message",
				buildChallengeRequestFrameStampedAt(
					"req-skew-"+tc.name,
					"com.orbpro.sgp4",
					uint64(time.Now().Add(-tc.stampedAgo).UnixMilli()),
				),
			)
			if err != nil {
				t.Fatalf("InvokeMethod(server_handle_message) failed: %v", err)
			}
			if !flatbuffers.BufferHasIdentifier(response, "$LCH") {
				t.Fatalf("expected $LCH response, got %d bytes", len(response))
			}

			messageType, _ := decodeChallengeHeader(t, response)
			errorCode := lchStringField(t, response, lchSlotErrorCode)

			if tc.wantRefusal {
				if messageType != licensingChallengeMessageTypeError {
					t.Fatalf("a %v-stale stamp got MESSAGE_TYPE=%d (want Error) — the skew bound is disabled, not corrected", tc.stampedAgo, messageType)
				}
				return
			}
			if errorCode == "invalid_timestamp" {
				t.Fatalf("a %v-stale stamp was still refused invalid_timestamp — the transport budget defect is not fixed", tc.stampedAgo)
			}
			if messageType != licensingChallengeMessageTypeResponse {
				t.Fatalf("a %v-stale stamp got MESSAGE_TYPE=%d error_code=%q, want a challenge Response", tc.stampedAgo, messageType, errorCode)
			}
		})
	}
}

const (
	licensingChallengeMessageTypeResponse = 1
	licensingChallengeMessageTypeError    = 2

	// FlatBuffers vtable slot offsets: field index i sits at 4 + 2*i.
	lcfSlotMaxClockSkewMS = 20 // LCF field 8
	lcfSlotChallengeTTLMS = 22 // LCF field 9
	lchSlotErrorCode      = 34 // LCH field 15
)

// writeGrantablePluginRegistry is writeTestPluginRegistry plus the operator
// grant policy the module needs in order to be PUBLISHED to the key server at
// all.
//
// 23680234 made the admit point fail closed: the default policy is `allowlist`,
// an allowlist policy with an empty list publishes NOTHING, and a module that
// was never published answers every challenge with module_not_found. That is
// correct behaviour and it is why any test about what the key server does with
// a request must first make the module grantable — otherwise the test measures
// the admit point and reports it as a challenge outcome.
func writeGrantablePluginRegistry(t *testing.T, entries ...license.PluginCatalogEntry) *license.PluginRegistry {
	t.Helper()

	return writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyOpen},
		entries...,
	)
}

func testLicensingKeySlots() map[string][]byte {
	signing := make([]byte, 32)
	wrapping := make([]byte, 32)
	for i := range signing {
		signing[i] = byte(i + 1)
		wrapping[i] = byte(32 - i)
	}
	return map[string][]byte{
		providerSigningSlotID:  signing,
		providerWrappingSlotID: wrapping,
	}
}

// buildChallengeRequestFrameStampedAt is buildChallengeRequestFrame with the
// one field these tests are about held under the caller's control. It is a
// separate builder on purpose: the shared helper is used by tests that care
// about other properties and must keep stamping "now".
func buildChallengeRequestFrameStampedAt(requestID, moduleID string, requestedAtMs uint64) []byte {
	builder := flatbuffers.NewBuilder(512)

	requestIDOffset := builder.CreateString(requestID)
	moduleIDOffset := builder.CreateString(moduleID)
	moduleVersionOffset := builder.CreateString("1.0.0")
	requesterPeerIDOffset := builder.CreateString("requester.orbpro.test")
	requesterXPubOffset := builder.CreateString("xpub-test-requester")
	signingPub := make([]byte, 32)
	ephemeralPub := make([]byte, 32)
	for i := range signingPub {
		signingPub[i] = byte(i + 11)
		ephemeralPub[i] = byte(42 - i)
	}
	requesterSigningPubKeyOffset := builder.CreateByteVector(signingPub)
	requesterEphemeralPubKeyOffset := builder.CreateByteVector(ephemeralPub)
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
	builder.PrependUint64Slot(11, requestedAtMs, 0)
	builder.PrependUOffsetTSlot(14, providerPeerIDOffset, 0)
	root := builder.EndObject()
	builder.FinishWithFileIdentifier(root, []byte("$LCH"))
	return builder.FinishedBytes()
}

func lcfUint64Field(t *testing.T, frame []byte, slot flatbuffers.VOffsetT) uint64 {
	t.Helper()
	table := &flatbuffers.Table{Bytes: frame, Pos: flatbuffers.GetUOffsetT(frame)}
	offset := table.Offset(slot)
	if offset == 0 {
		t.Fatalf("LCF frame carries no field at vtable slot %d", slot)
	}
	return table.GetUint64(flatbuffers.UOffsetT(offset) + table.Pos)
}

func lchStringField(t *testing.T, frame []byte, slot flatbuffers.VOffsetT) string {
	t.Helper()
	table := &flatbuffers.Table{Bytes: frame, Pos: flatbuffers.GetUOffsetT(frame)}
	offset := table.Offset(slot)
	if offset == 0 {
		return ""
	}
	return string(table.ByteVector(flatbuffers.UOffsetT(offset) + table.Pos))
}
