package pubsub

import (
	"encoding/binary"
	"testing"

	ps "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
)

// oobPNM is a size-prefixed $PNM whose every field offset points 2 GiB past
// its end (security re-audit NET-01).
func oobPNM() []byte {
	const slots = 9
	vtableSize := 4 + 2*slots
	vtable, table := 8, 8+vtableSize
	inner := make([]byte, table+12)
	binary.LittleEndian.PutUint32(inner[0:], uint32(table))
	copy(inner[4:8], "$PNM")
	binary.LittleEndian.PutUint16(inner[vtable:], uint16(vtableSize))
	binary.LittleEndian.PutUint16(inner[vtable+2:], 12)
	for i := 0; i < slots; i++ {
		binary.LittleEndian.PutUint16(inner[vtable+4+2*i:], 4)
	}
	binary.LittleEndian.PutUint32(inner[table:], uint32(table-vtable))
	binary.LittleEndian.PutUint32(inner[table+4:], 0x7fffff00)
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

// A tip reaches the queue's accessors after only the configured verifier ran:
// with the production verifier or a lax one, a malformed PNM is dropped and
// the queue's goroutine survives.
func TestTipQueueDropsMalformedPNM(t *testing.T) {
	for name, verifier := range map[string]PNMVerifier{
		"production verifier": channels.VerifySignedPNMEnvelope,
		"lax verifier":        func([]byte) (channels.PNMTrustEvidence, error) { return channels.PNMTrustEvidence{}, nil },
	} {
		tq := NewTipQueue(nil)
		tq.SetPNMVerifier(verifier)
		msg := &ps.Message{Message: &pb.Message{Data: oobPNM()}}
		if tq.handleMessage(msg) {
			t.Fatalf("%s: malformed PNM accepted", name)
		}
	}
}
