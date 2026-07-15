package channels_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
)

// --- topic-scheme unit test -------------------------------------------------

// TestTopicScheme locks the per-(provider, standard) wire topic grammar: the
// exact string, and — critically — that two sources of the SAME standard map to
// DISTINCT topics (this is what makes per-provider isolation possible).
func TestTopicScheme(t *testing.T) {
	// FormatChannelID grammar (ported subset).
	id, err := channels.FormatChannelID(channels.ChannelIDInput{SourceID: "celestrak-gp", StandardCode: "OMM"})
	if err != nil {
		t.Fatalf("FormatChannelID: %v", err)
	}
	if id != "celestrak-gp-OMM" {
		t.Fatalf("channel id = %q, want celestrak-gp-OMM", id)
	}

	// Wire topic for (celestrak-gp, OMM).
	top, err := channels.WireTopic("celestrak-gp", "OMM")
	if err != nil {
		t.Fatalf("WireTopic: %v", err)
	}
	want := "/spacedatanetwork/channels/OMM/celestrak-gp"
	if top != want {
		t.Fatalf("topic = %q, want %q", top, want)
	}

	// Two sources of the SAME standard => distinct topics.
	topA, _ := channels.WireTopic("celestrak-gp", "OMM")
	topB, _ := channels.WireTopic("provider-two", "OMM")
	if topA == topB {
		t.Fatalf("two sources of OMM produced the same topic %q", topA)
	}
	if topB != "/spacedatanetwork/channels/OMM/provider-two" {
		t.Fatalf("second source topic = %q", topB)
	}

	// Same source, different standard => distinct topics (isolation on standard).
	topOMM, _ := channels.WireTopic("celestrak-gp", "OMM")
	topCDM, _ := channels.WireTopic("celestrak-gp", "CDM")
	if topOMM == topCDM {
		t.Fatalf("two standards of the same source produced the same topic %q", topOMM)
	}

	// Standard prefix is the shared root under which a standard's sources live.
	pfx, err := channels.StandardTopicPrefix("OMM")
	if err != nil {
		t.Fatalf("StandardTopicPrefix: %v", err)
	}
	if pfx != "/spacedatanetwork/channels/OMM/" {
		t.Fatalf("prefix = %q", pfx)
	}

	// Bad standard codes are rejected.
	if _, err := channels.WireTopic("celestrak-gp", "omm"); err == nil {
		t.Fatalf("lowercase standard should be rejected")
	}
	if _, err := channels.WireTopic("", "OMM"); err == nil {
		t.Fatalf("empty source should be rejected")
	}
}

// --- two-node fan-out test --------------------------------------------------

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

// buildOMM produces one OMM record WITHOUT its 4-byte size prefix — the
// canonical single-FlatBuffer form the store content-addresses and fans out.
func buildOMM(t *testing.T, norad uint32, name string) []byte {
	t.Helper()
	sized := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch("2026-05-10T00:00:00Z").
		WithEpochTimestamp(float64(time.Now().Unix())).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return sized[4:]
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

// newNode makes a real libp2p host with a gossipsub instance whose wire
// message-id is the record CID (channels.MessageIDFn).
func newNode(ctx context.Context, t *testing.T) (host.Host, *pubsub.PubSub) {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithMessageIdFn(channels.MessageIDFn))
	if err != nil {
		t.Fatalf("NewGossipSub: %v", err)
	}
	return h, ps
}

// TestChannelFanoutAcrossNodes proves Phase 4 end-to-end: storing a real OMM
// record on node A through the store+channels fan-out streams the exact record
// bytes to node B, which is subscribed to the (celestrak-gp, OMM) channel over
// real gossipsub; a DIFFERENT (source,standard) channel receives nothing.
func TestChannelFanoutAcrossNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hostA, psA := newNode(ctx, t)
	hostB, psB := newNode(ctx, t)

	// Connect the two hosts.
	if err := hostB.Connect(ctx, peer.AddrInfo{ID: hostA.ID(), Addrs: hostA.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	chA := channels.New(psA)
	chB := channels.New(psB)

	const (
		source = "celestrak-gp"
		std    = "OMM"
	)

	// Node A's store fans every newly stored record out to its channel.
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	storeA, err := sdnstore.Open(sdnstore.Config{
		Blockstore:     blockstore.NewBlockstore(mds),
		Datastore:      mds,
		Schemas:        ommSchemas(),
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		OnStore:        chA.Publisher(),
	})
	if err != nil {
		t.Fatalf("sdnstore.Open: %v", err)
	}
	defer storeA.Close()

	// Node B subscribes to the exact (celestrak-gp, OMM) channel...
	subB, err := chB.Subscribe(std, source)
	if err != nil {
		t.Fatalf("subscribe B (celestrak-gp, OMM): %v", err)
	}
	defer subB.Cancel()

	// ...and to two DIFFERENT channels that must NOT receive the record:
	// a different source of the same standard, and a different standard.
	subOtherSource, err := chB.Subscribe(std, "provider-x")
	if err != nil {
		t.Fatalf("subscribe B (provider-x, OMM): %v", err)
	}
	defer subOtherSource.Cancel()
	subOtherStd, err := chB.Subscribe("CDM", source)
	if err != nil {
		t.Fatalf("subscribe B (celestrak-gp, CDM): %v", err)
	}
	defer subOtherStd.Cancel()

	// Node A joins (subscribes to) the same channel so a real GRAFTed mesh
	// forms; drain-discard its own copies.
	subA, err := chA.Subscribe(std, source)
	if err != nil {
		t.Fatalf("subscribe A (celestrak-gp, OMM): %v", err)
	}
	defer subA.Cancel()
	go func() {
		for {
			if _, err := subA.Next(ctx); err != nil {
				return
			}
		}
	}()

	// Wait for the (celestrak-gp, OMM) mesh to include the peer.
	wireTopic, _ := channels.WireTopic(source, std)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(psA.ListPeers(wireTopic)) >= 1 && len(psB.ListPeers(wireTopic)) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Store real OMM records on A; each store fans out to the channel. Retry
	// with a FRESH record per attempt (byte-identical re-store is idempotent
	// and would not re-fire the fan-out) to absorb gossipsub mesh warmup.
	var stored []byte
	var got *pubsub.Message
	for attempt := 0; attempt < 8 && got == nil; attempt++ {
		rec := buildOMM(t, uint32(25544+attempt), fmt.Sprintf("ISS-%d", attempt))
		if _, err := storeA.Store(ctx, source, std, rec); err != nil {
			t.Fatalf("store on A (attempt %d): %v", attempt, err)
		}
		recvCtx, recvCancel := context.WithTimeout(ctx, 3*time.Second)
		msg, err := subB.Next(recvCtx)
		recvCancel()
		if err == nil {
			stored = rec
			got = msg
			break
		}
	}
	if got == nil {
		t.Fatalf("node B never received the record on the (celestrak-gp, OMM) channel")
	}

	// The received bytes are EXACTLY the stored record.
	if !bytes.Equal(got.Data, stored) {
		t.Fatalf("received %d bytes, want the stored %d-byte record (mismatch)", len(got.Data), len(stored))
	}

	// The gossipsub message id carries the record CID (MessageIDFn).
	wantCID, err := channels.CIDOf(stored)
	if err != nil {
		t.Fatalf("CIDOf: %v", err)
	}
	if got.ID != wantCID.String() {
		t.Fatalf("message id = %q, want record CID %q", got.ID, wantCID.String())
	}

	// Isolation: neither the different-source nor the different-standard channel
	// received the record.
	assertSilent := func(name string, sub *channels.Subscription) {
		c, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		if msg, err := sub.Next(c); err == nil {
			t.Fatalf("isolation broken: %s channel received %d bytes", name, len(msg.Data))
		}
	}
	assertSilent("(provider-x, OMM)", subOtherSource)
	assertSilent("(celestrak-gp, CDM)", subOtherStd)
}
