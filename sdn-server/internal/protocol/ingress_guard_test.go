package protocol

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// net01PNM is the re-audit's NET-01 announcement: a size-prefixed $PNM whose
// root header passes the envelope check and whose CID uoffset points 2 GiB
// past the end of its 32 bytes.
func net01PNM() []byte {
	inner := make([]byte, 28)
	binary.LittleEndian.PutUint32(inner[0:], 20)
	copy(inner[4:8], "$PNM")
	binary.LittleEndian.PutUint16(inner[8:], 10)
	binary.LittleEndian.PutUint16(inner[10:], 8)
	binary.LittleEndian.PutUint16(inner[16:], 4)
	binary.LittleEndian.PutUint32(inner[20:], 12)
	binary.LittleEndian.PutUint32(inner[24:], 0x7fffff00)
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

func newPubSubTestHandler(t *testing.T) *SDSExchangeHandler {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := NewSDSExchangeHandler(newFlatSQLSyncTestStore(t), validator)
	h.SetPushAuthorizer(AllowAnyPush)
	return h
}

const attacker = peer.ID("attacker-peer-id-bytes")

// NET-01: the malformed announcement never reaches the announcement handler,
// on the aggregate PNM topic or any per-schema topic.
func TestNET01MalformedPNMIsRefusedBeforeTheAnnouncementHandler(t *testing.T) {
	h := newPubSubTestHandler(t)
	called := false
	h.SetPubSubPNMHandler(func(context.Context, string, []byte, peer.ID) error {
		called = true
		return nil
	})
	for _, topic := range []string{"PNM.fbs", "OMM.fbs"} {
		stored, err := h.HandlePubSubMessageStored(topic, net01PNM(), attacker)
		if err == nil || stored {
			t.Fatalf("%s: malformed PNM accepted (stored=%v err=%v)", topic, stored, err)
		}
		if !errors.Is(err, fbverify.ErrInvalid) {
			t.Fatalf("%s: err = %v, want the structural verifier's refusal", topic, err)
		}
	}
	if called {
		t.Fatal("the announcement handler saw the malformed PNM")
	}
}

// A well-formed but BARE $PNM passes Validate, yet every announcement
// consumer reads GetSizePrefixedRootAsPNM, which would take the root offset
// from the identifier bytes: the lane requires the size-prefixed form.
func TestNET01BarePNMIsRefusedOnTheAnnouncementLane(t *testing.T) {
	h := newPubSubTestHandler(t)
	h.SetPubSubPNMHandler(func(context.Context, string, []byte, peer.ID) error {
		t.Fatal("the announcement handler saw a bare PNM")
		return nil
	})
	bare := sds.NewPNMBuilder().Build()[4:]
	if _, err := h.HandlePubSubMessageStored("PNM.fbs", bare, attacker); err == nil {
		t.Fatal("bare PNM accepted on the announcement lane")
	}
}

// The backstop: if anything downstream of validation ever panics, the
// message is dropped with ErrMalformedMessage and the gossip loop lives.
func TestNET01PanicInTheAnnouncementHandlerIsContained(t *testing.T) {
	h := newPubSubTestHandler(t)
	h.SetPubSubPNMHandler(func(context.Context, string, []byte, peer.ID) error {
		var b []byte
		_ = b[7] // the shape of an unverified accessor
		return nil
	})
	stored, err := h.HandlePubSubMessageStored("PNM.fbs", sds.NewPNMBuilder().Build(), attacker)
	if !errors.Is(err, ErrMalformedMessage) || stored {
		t.Fatalf("stored=%v err=%v, want ErrMalformedMessage", stored, err)
	}
}

func TestRecoverStreamContainsHandlerPanics(t *testing.T) {
	func() {
		defer recoverStream("test", nil)
		panic("malformed")
	}()
}

func TestWireSpeedProbeBudget(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	b := newWireSpeedProbeBudget()
	b.now = func() time.Time { return clock }
	a, c, d := peer.ID("a"), peer.ID("c"), peer.ID("d")

	releaseA, err := b.acquire(a, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.acquire(a, 1<<20); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second concurrent probe from one peer: %v", err)
	}
	releaseC, err := b.acquire(c, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.acquire(d, 1<<20); err == nil || !strings.Contains(err.Error(), "probes already running") {
		t.Fatalf("node-wide cap: %v", err)
	}
	releaseA()
	releaseC()

	// Drain a's bucket, then it must wait for the refill.
	for spent := int64(1 << 20); spent+maxWireSpeedProbeBytes <= wireSpeedProbePeerBurstBytes; spent += maxWireSpeedProbeBytes {
		release, err := b.acquire(a, maxWireSpeedProbeBytes)
		if err != nil {
			t.Fatalf("within budget: %v", err)
		}
		release()
	}
	if _, err := b.acquire(a, maxWireSpeedProbeBytes); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("over budget: %v", err)
	}
	clock = clock.Add(time.Duration(maxWireSpeedProbeBytes/wireSpeedProbePeerRefillBytesPerSecond+1) * time.Second)
	release, err := b.acquire(a, maxWireSpeedProbeBytes)
	if err != nil {
		t.Fatalf("after refill: %v", err)
	}
	release()

	// Idle, refilled peers are forgotten so the table stays bounded.
	clock = clock.Add(time.Hour)
	b.sweepLocked(clock)
	if len(b.peers) != 0 {
		t.Fatalf("%d idle peers kept", len(b.peers))
	}
}
