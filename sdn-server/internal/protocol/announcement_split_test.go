package protocol

import (
	"context"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A PNM announcement on pubsub does two unrelated things: it may WRITE a row
// into the record store, and it is DISPATCHED to the node's dataset-catalog
// handler. Only the first lands on the anonymous public data plane; only the
// first is what peer trust gates.
//
// The two were coupled once — 74c82735c gated the whole branch on mayPush and
// so switched off untrusted catalog discovery as a side effect, which nothing
// noticed for a week because no test held the two properties together. This
// test does. It fails if the store write ever escapes the trust gate (the
// release blocker 74c82735c closed) AND it fails if the handler dispatch is
// ever put back behind it (the discovery feature that gating cost).
func TestUntrustedAnnouncementIsDispatchedButNeverStored(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	pnmBytes := buildProtocolTestPNM(t, "bafyuntrusteddiscovery", "provider:OMM.fbs:current")
	stranger := peer.ID("12D3KooWUntrustedAnnouncer")

	handler := NewSDSExchangeHandler(store, validator)
	handler.SetPushAuthorizer(func(peer.ID) bool { return false })
	var dispatched []peer.ID
	handler.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		dispatched = append(dispatched, from)
		return nil
	})

	stored, err := handler.HandlePubSubMessageStored("PNM.fbs", pnmBytes, stranger)
	if err != nil {
		t.Fatalf("an untrusted announcement must be handled, not errored: %v", err)
	}
	// HANDLED is not STORED. A caller that counts ingest must read this bool,
	// not the nil error: the activity ring reported untrusted announcements as
	// stored records for as long as the two were conflated.
	if stored {
		t.Fatal("an untrusted announcement reported itself as a stored record")
	}

	// HALF ONE — the gate 74c82735c installed. A peer this node has not
	// promoted may not put a row in the store, because the store is served
	// back anonymously, unioned across every producer table.
	if got := countStored(t, store, "PNM.fbs"); got != 0 {
		t.Fatalf("untrusted peer stored %d PNM records; the store write must stay behind mayPush", got)
	}

	// HALF TWO — the feature that gate cost. Metadata discovery is the whole
	// point of the announcement: see a peer's dataset inventory, THEN decide
	// whether to trust it. The handler's own downstream checks
	// (materializeDatasetPublicationPNM's IsTrusted, and
	// cacheDatasetPublicationMetadata's signature-verified, metadata-only,
	// bounded fetch) are what keep this safe, not this branch.
	if len(dispatched) != 1 || dispatched[0] != stranger {
		t.Fatalf("untrusted announcement dispatched %v, want exactly one dispatch from %s", dispatched, stranger)
	}

	// And a trusted peer still gets BOTH, or "stored" and "dispatched" would
	// be indistinguishable from the feature simply being off.
	trusted := NewSDSExchangeHandler(store, validator)
	trusted.SetPushAuthorizer(func(id peer.ID) bool { return id == stranger })
	var trustedDispatched int
	trusted.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		trustedDispatched++
		return nil
	})
	storedTrusted, err := trusted.HandlePubSubMessageStored("PNM.fbs", pnmBytes, stranger)
	if err != nil {
		t.Fatalf("HandlePubSubMessage for a trusted peer failed: %v", err)
	}
	if !storedTrusted {
		t.Fatal("a trusted announcement did not report itself as stored")
	}
	if got := countStored(t, store, "PNM.fbs"); got != 1 {
		t.Fatalf("trusted peer stored %d PNM records, want 1", got)
	}
	if trustedDispatched != 1 {
		t.Fatalf("trusted announcement dispatched %d times, want 1", trustedDispatched)
	}
}

// The announcer picks the manifest CID that cacheDatasetPublicationMetadata
// then fetches, so dispatching an untrusted announcement spends this node's
// bandwidth at the sender's choosing. Bounded is not the same as free: the
// lane carries its own budget, far tighter than the general per-peer message
// limit, which is sized for record traffic.
func TestUntrustedAnnouncementDispatchIsRateLimitedPerPeer(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	handler := NewSDSExchangeHandler(store, validator)
	handler.SetPushAuthorizer(func(peer.ID) bool { return false })
	dispatched := map[peer.ID]int{}
	handler.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		dispatched[from]++
		return nil
	})

	flooder := peer.ID("12D3KooWAnnouncementFlooder")
	const attempts = untrustedAnnouncementBurst + 24
	for i := 0; i < attempts; i++ {
		// A distinct CID each time: repeating one is deduplicated downstream,
		// so the interesting flood is the one that is not.
		pnm := buildProtocolTestPNM(t, fmt.Sprintf("bafyflood%03d", i), "provider:OMM.fbs:current")
		if err := handler.HandlePubSubMessage("PNM.fbs", pnm, flooder); err != nil {
			t.Fatalf("rate limiting must drop, not error: %v", err)
		}
	}
	if got := dispatched[flooder]; got > untrustedAnnouncementBurst {
		t.Fatalf("flooder had %d announcements dispatched from %d attempts; burst is %d", got, attempts, untrustedAnnouncementBurst)
	}
	if dispatched[flooder] == 0 {
		t.Fatal("rate limiting starved the lane entirely; the budget must admit the burst")
	}

	// The budget is per peer. One noisy announcer must not silence another.
	quiet := peer.ID("12D3KooWQuietAnnouncer")
	pnm := buildProtocolTestPNM(t, "bafyquietcatalog", "provider:OMM.fbs:current")
	if err := handler.HandlePubSubMessage("PNM.fbs", pnm, quiet); err != nil {
		t.Fatalf("HandlePubSubMessage failed: %v", err)
	}
	if dispatched[quiet] != 1 {
		t.Fatalf("a second peer's announcement was dispatched %d times, want 1", dispatched[quiet])
	}

	// A TRUSTED peer is not on this budget at all: its announcements are
	// stored, and the store write is the thing the general rate limiter and
	// the trust gate already govern.
	trusted := NewSDSExchangeHandler(store, validator)
	trusted.SetPushAuthorizer(AllowAnyPush)
	trustedDispatched := 0
	trusted.SetPubSubPNMHandler(func(ctx context.Context, schema string, data []byte, from peer.ID) error {
		trustedDispatched++
		return nil
	})
	for i := 0; i < attempts; i++ {
		pnm := buildProtocolTestPNM(t, fmt.Sprintf("bafytrusted%03d", i), "provider:OMM.fbs:current")
		if err := trusted.HandlePubSubMessage("PNM.fbs", pnm, flooder); err != nil {
			t.Fatalf("HandlePubSubMessage failed: %v", err)
		}
	}
	if trustedDispatched != attempts {
		t.Fatalf("trusted announcements dispatched %d times, want %d", trustedDispatched, attempts)
	}
}

// A handler built without a limiter (a bare struct literal, as some tests do)
// must not lose the feature the budget protects. The budget fails OPEN; the
// trust gate above it fails CLOSED. Those directions are deliberately opposite
// and this pins them so a future refactor cannot quietly swap them.
func TestUntrustedAnnouncementLimiterFailsOpenAndTrustGateFailsClosed(t *testing.T) {
	var limiter *untrustedAnnouncementLimiter
	if !limiter.Allow(peer.ID("12D3KooWAnyone")) {
		t.Fatal("a nil announcement budget must allow; it is a DoS control, not a trust gate")
	}
	if (&SDSExchangeHandler{}).mayPush(peer.ID("12D3KooWAnyone")) {
		t.Fatal("an unwired push authorizer must deny")
	}
	if NewSDSExchangeHandler(nil, nil).untrustedAnnouncements == nil {
		t.Fatal("the constructor must install the budget unconditionally, not from config")
	}
}
