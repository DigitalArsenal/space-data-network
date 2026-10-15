package node

// SEC-031 regressions for the dataset feed-head admission gate.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type feedHeadPeer struct {
	id   peer.ID
	priv ed25519.PrivateKey
}

func newFeedHeadPeer(t *testing.T) feedHeadPeer {
	t.Helper()
	priv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	return feedHeadPeer{id: id, priv: ed25519.PrivateKey(raw)}
}

func feedHeadMessage(t *testing.T, author, relay peer.ID, ann sdnpubsub.DatasetFeedHeadAnnouncement) *pubsub.Message {
	t.Helper()
	data, err := json.Marshal(ann)
	if err != nil {
		t.Fatal(err)
	}
	return &pubsub.Message{Message: &pb.Message{From: []byte(author), Data: data}, ReceivedFrom: relay}
}

func testFeedHead(seq int64, at time.Time) sdnpubsub.DatasetFeedHeadAnnouncement {
	return sdnpubsub.DatasetFeedHeadAnnouncement{
		MessageType: sdnpubsub.DatasetFeedHeadMessageType, Schema: "OMM.fbs",
		ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-1",
		QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, FeedSequence: seq,
		FeedHead: "head-1", ShardCID: "bafyshard", IndexCID: "bafyindex", PublishedAt: at,
	}
}

func signedFeedHead(t *testing.T, ann sdnpubsub.DatasetFeedHeadAnnouncement, key ed25519.PrivateKey) sdnpubsub.DatasetFeedHeadAnnouncement {
	t.Helper()
	if err := sdnpubsub.SignDatasetFeedHead(&ann, key); err != nil {
		t.Fatal(err)
	}
	return ann
}

func TestAdmitDatasetFeedHeadAuthorizesTheAuthorNotTheRelay(t *testing.T) {
	trusted, attacker := newFeedHeadPeer(t), newFeedHeadPeer(t)
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: trusted.id, TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	n := &Node{peerRegistry: registry, store: newFeedHeadTestStore(t)}
	t.Setenv(allowUnsignedFeedHeadsDevEnv, "")
	now := time.Now().UTC()

	// The audit attack: an untrusted author's self-signed head, relayed by
	// the trusted peer.
	forged := testFeedHead(math.MaxInt64, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	forged = signedFeedHead(t, forged, attacker.priv)
	if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, attacker.id, trusted.id, forged), "OMM.fbs"); err == nil {
		t.Fatal("the audit's forged head, relayed by a trusted peer, was admitted")
	}
	// The same attack with plausible ranking is refused for its author.
	plausible := signedFeedHead(t, testFeedHead(2, now), attacker.priv)
	if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, attacker.id, trusted.id, plausible), "OMM.fbs"); err == nil || !strings.Contains(err.Error(), "not a trusted peer") {
		t.Fatalf("untrusted author relayed by a trusted peer: err = %v, want refusal", err)
	}

	// A head that claims the trusted peer as author but is signed with the
	// key embedded in the message, not the author's key.
	_, embeddedKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	selfSigned := signedFeedHead(t, testFeedHead(2, now), embeddedKey)
	if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, trusted.id, trusted.id, selfSigned), "OMM.fbs"); err == nil || !strings.Contains(err.Error(), "not signed by a key bound to its author") {
		t.Fatalf("head signed by its embedded key: err = %v, want refusal", err)
	}

	// Unsigned heads are refused by default.
	if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, trusted.id, trusted.id, testFeedHead(2, now)), "OMM.fbs"); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned head: err = %v, want refusal", err)
	}

	// The author's own genuine head is admitted and authorized as the author.
	genuine := signedFeedHead(t, testFeedHead(2, now), trusted.priv)
	ann, author, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, trusted.id, attacker.id, genuine), "OMM.fbs")
	if err != nil {
		t.Fatalf("genuine head: %v", err)
	}
	if author != trusted.id || ann.FeedHead != genuine.FeedHead {
		t.Fatalf("admitted as %s, want the author %s (never the relay)", author, trusted.id)
	}
}

func TestAdmitDatasetFeedHeadRefusesForgedRanking(t *testing.T) {
	trusted := newFeedHeadPeer(t)
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: trusted.id, TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	n := &Node{peerRegistry: registry, store: newFeedHeadTestStore(t)}
	now := time.Now().UTC()
	without := func(mutate func(*sdnpubsub.DatasetFeedHeadAnnouncement)) sdnpubsub.DatasetFeedHeadAnnouncement {
		a := testFeedHead(2, now)
		mutate(&a)
		return a
	}
	for name, ann := range map[string]sdnpubsub.DatasetFeedHeadAnnouncement{
		// The verifier's S2: an empty lane field skipped every lane check.
		"empty batch_id, sequence 2^24": without(func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.BatchID, a.FeedSequence = "", sdnpubsub.DatasetFeedHeadMaxSequence
		}),
		"empty provider, source and batch": without(func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.ProviderID, a.SourceName, a.BatchID = "", "", ""
		}),
		"empty source_name":      without(func(a *sdnpubsub.DatasetFeedHeadAnnouncement) { a.SourceName = "" }),
		"MaxInt64 sequence":      testFeedHead(math.MaxInt64, now),
		"zero sequence":          testFeedHead(0, now),
		"future published_at":    testFeedHead(2, now.Add(time.Hour)),
		"year 2099 published_at": testFeedHead(2, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)),
		"missing published_at":   testFeedHead(2, time.Time{}),
		"sequence past the cap":  testFeedHead(sdnpubsub.DatasetFeedHeadMaxSequence+1, now),
		"schema for another topic": func() sdnpubsub.DatasetFeedHeadAnnouncement {
			a := testFeedHead(2, now)
			a.Schema = "CAT.fbs"
			return a
		}(),
	} {
		signed := signedFeedHead(t, ann, trusted.priv)
		if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, trusted.id, trusted.id, signed), "OMM.fbs"); err == nil {
			t.Fatalf("%s: admitted", name)
		}
	}
}

func TestAdmitDatasetFeedHeadChecksTheRecordedLane(t *testing.T) {
	trusted := newFeedHeadPeer(t)
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: trusted.id, TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	n := &Node{peerRegistry: registry, store: store}
	now := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)

	recorded := storage.DatasetShardPublication{
		SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-1",
		QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, RecordCount: 1, ByteCount: 1,
		ShardCID: "bafyshard", IndexCID: "bafyindex", ShardSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64),
		QuerySHA256: strings.Repeat("c", 64), ResultSHA256: strings.Repeat("d", 64), FeedSequence: 3, PublishedAt: now,
	}
	if err := store.UpsertDatasetShardPublication(recorded); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: "OMM.fbs", QueryProfile: storage.DatasetPublicationQueryProfile})
	if err != nil || len(rows) != 1 {
		t.Fatalf("recorded rows = %v, %v", rows, err)
	}
	recorded = rows[0]

	admit := func(ann sdnpubsub.DatasetFeedHeadAnnouncement) error {
		_, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, trusted.id, trusted.id, signedFeedHead(t, ann, trusted.priv)), "OMM.fbs")
		return err
	}
	// The recorded window re-announced as recorded: fine (idempotent).
	same := testFeedHead(recorded.FeedSequence, now)
	same.FeedHead = recorded.FeedHead
	if err := admit(same); err != nil {
		t.Fatalf("re-announcing the recorded window: %v", err)
	}
	// The recorded window with a new sequence: a rewrite.
	rewrite := testFeedHead(recorded.FeedSequence+5, now)
	rewrite.FeedHead = recorded.FeedHead
	if err := admit(rewrite); err == nil || !strings.Contains(err.Error(), "rewrites recorded window") {
		t.Fatalf("rewriting a recorded window: err = %v", err)
	}
	// A new window may not leap far past the batch's highest sequence.
	leap := testFeedHead(recorded.FeedSequence+datasetFeedHeadMaxSequenceStep+1, now)
	leap.Offset = 10
	if err := admit(leap); err == nil || !strings.Contains(err.Error(), "leaps past") {
		t.Fatalf("sequence leap: err = %v", err)
	}
	next := testFeedHead(recorded.FeedSequence+1, now)
	next.Offset, next.FeedHead = 10, "head-2"
	if err := admit(next); err != nil {
		t.Fatalf("the next window: %v", err)
	}

	// The verifier's S1: once a newer batch of the lane has begun, a head
	// that extends the older batch with a later published_at is refused,
	// however modest its sequence. A late-arriving window published before
	// the newer batch began is still taken.
	newer := recorded
	newer.BatchID, newer.FeedSequence, newer.FeedHead = "batch-2", 0, ""
	newer.ShardCID, newer.IndexCID = "bafyshard2", "bafyindex2"
	newer.PublishedAt = now.Add(time.Hour)
	if err := store.UpsertDatasetShardPublication(newer); err != nil {
		t.Fatal(err)
	}
	rollback := testFeedHead(recorded.FeedSequence+2, now.Add(90*time.Minute))
	rollback.Offset, rollback.FeedHead = 20, "head-rollback"
	if err := admit(rollback); err == nil || !strings.Contains(err.Error(), "after the newer batch batch-2 began") {
		t.Fatalf("extending a superseded batch: err = %v", err)
	}
	late := testFeedHead(recorded.FeedSequence+2, now.Add(30*time.Minute))
	late.Offset, late.FeedHead = 20, "head-late"
	if err := admit(late); err != nil {
		t.Fatalf("a window of the older batch published before the newer batch began: %v", err)
	}
	// A batch never seen here is not refused for being older: retention
	// ranks it below the newer batch by its publish time.
	older := testFeedHead(1, now.Add(-time.Hour))
	older.BatchID, older.FeedHead = "batch-0", "head-0"
	if err := admit(older); err != nil {
		t.Fatalf("an older, unseen batch: %v", err)
	}
}

func TestRepairImplausibleDatasetFeedPublications(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	row := func(batch string, seq int64, at time.Time) storage.DatasetShardPublication {
		return storage.DatasetShardPublication{
			SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: batch,
			QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, RecordCount: 1, ByteCount: 1,
			ShardCID: "bafy" + batch, IndexCID: "bafyidx" + batch, ShardSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64),
			QuerySHA256: strings.Repeat("c", 64), ResultSHA256: strings.Repeat("d", 64), FeedSequence: seq, PublishedAt: at,
		}
	}
	window := func(pub storage.DatasetShardPublication, offset int) storage.DatasetShardPublication {
		pub.Offset = offset
		return pub
	}
	for _, pub := range []storage.DatasetShardPublication{
		row("batch-good", 2, now.Add(-time.Hour)),
		row("batch-forged-seq", math.MaxInt64, now.Add(-2*time.Hour)),
		row("batch-forged-time", 1, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)),
		// The verifier's S1 residue: an old batch extended, with a
		// plausible sequence, after the newer batch-good began.
		row("batch-old", 1, now.Add(-3*time.Hour)),
		window(row("batch-old", 1+datasetFeedHeadMaxSequenceStep, now.Add(-time.Minute)), 10),
		// A sequence leap inside one batch.
		window(row("batch-good", 2+datasetFeedHeadMaxSequenceStep+1, now.Add(-time.Hour)), 10),
	} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := RepairImplausibleDatasetFeedPublications(store, storage.DatasetPublicationQueryProfile, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 4 {
		t.Fatalf("dry run found %d implausible rows, want 4: %+v", len(plan), plan)
	}
	if _, err := RepairImplausibleDatasetFeedPublications(store, storage.DatasetPublicationQueryProfile, now, true); err != nil {
		t.Fatal(err)
	}
	left, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: "OMM.fbs", QueryProfile: storage.DatasetPublicationQueryProfile})
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]int{}
	for _, row := range left {
		kept[fmt.Sprintf("%s@%d", row.BatchID, row.Offset)]++
	}
	if len(left) != 2 || kept["batch-good@0"] != 1 || kept["batch-old@0"] != 1 {
		t.Fatalf("after repair: %v, want batch-good@0 and batch-old@0 only", kept)
	}
}

func newFeedHeadTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func feedHeadTestHostWithKey(t *testing.T) (host.Host, ed25519.PrivateKey) {
	t.Helper()
	priv, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.Identity(priv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	raw, err := priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	return h, ed25519.PrivateKey(raw)
}

// producerPublish stores one batch on a producer's store and records its
// publication row the way the producer does; it returns the stored row (with
// the feed sequence and head the store computed).
func producerPublish(t *testing.T, st *storage.FlatSQLStore, producer, batch string, norad uint32, at time.Time) storage.DatasetShardPublication {
	t.Helper()
	tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: batch, ContentKeyID: "public"}
	rec := sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectID("1998-067A").WithObjectName(batch).Build()
	if _, err := st.StoreWithSourceTags("OMM.fbs", rec, producer, nil, tags); err != nil {
		t.Fatalf("store %s: %v", batch, err)
	}
	export, err := st.ExportDatasetWindow(filepath.Join(st.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: batch, Limit: 10, AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("export %s: %v", batch, err)
	}
	pub := storage.DatasetShardPublication{
		SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: batch,
		QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, RecordCount: export.RecordCount, ByteCount: export.ShardBytes,
		ShardCID: export.ShardCID, IndexCID: export.IndexCID, ShardSHA256: export.ShardSHA256, IndexSHA256: export.IndexSHA256,
		QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256, PublishedAt: at,
	}
	if err := st.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("upsert %s: %v", batch, err)
	}
	stored, found, err := st.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName: pub.SchemaName, ProviderID: pub.ProviderID, SourceName: pub.SourceName, BatchID: batch,
		QueryProfile: pub.QueryProfile, Offset: 0, Limit: pub.Limit,
	})
	if err != nil || !found {
		t.Fatalf("read back %s: found=%v err=%v", batch, found, err)
	}
	return stored
}

// producerFeedHead is the head a producer announces for a stored row (the
// fields internal/api announceDatasetFeedHead sends), signed with its key.
func producerFeedHead(t *testing.T, p storage.DatasetShardPublication, key ed25519.PrivateKey) sdnpubsub.DatasetFeedHeadAnnouncement {
	t.Helper()
	ann := sdnpubsub.DatasetFeedHeadAnnouncement{
		MessageType: sdnpubsub.DatasetFeedHeadMessageType,
		Schema:      p.SchemaName, ProviderID: p.ProviderID, SourceName: p.SourceName, BatchID: p.BatchID,
		QueryProfile: p.QueryProfile, Offset: p.Offset, Limit: p.Limit, FeedSequence: p.FeedSequence,
		PreviousHead: p.PreviousHead, FeedHead: p.FeedHead, RecordCount: p.RecordCount, ByteCount: p.ByteCount,
		ShardCID: p.ShardCID, IndexCID: p.IndexCID, ManifestCID: p.ManifestCID, PNMCID: p.PNMCID, PublishedAt: p.PublishedAt,
	}
	if key != nil {
		if err := sdnpubsub.SignDatasetFeedHead(&ann, key); err != nil {
			t.Fatal(err)
		}
	}
	return ann
}

type feedHeadResult struct {
	imported int
	err      error
}

// feedHeadNet is the audit's topology over real libp2p hosts and the node's
// StrictSign gossipsub: P is the trusted producer, V the victim node (trusts
// only P) and X an untrusted attacker connected only to P. With relayOnly, V
// is connected only to R, a second trusted node serving the same content,
// and never to the author P. With liveHandler, V runs the node's own
// handleDatasetFeedHeadSubscription instead of reporting each head's result.
type feedHeadNet struct {
	ctx      context.Context
	pStore   *storage.FlatSQLStore
	vStore   *storage.FlatSQLStore
	pHost    host.Host
	pKey     ed25519.PrivateKey
	node     *Node
	pTopic   *pubsub.Topic
	xTopic   *pubsub.Topic
	results  chan feedHeadResult
	baseTime time.Time
	// live: the node's handler goroutine holds node.wg for the test's
	// lifetime, so retention is awaited by polling alone.
	live bool
}

type feedHeadNetOptions struct{ relayOnly, liveHandler bool }

func newFeedHeadNet(t *testing.T, opts feedHeadNetOptions) *feedHeadNet {
	t.Helper()
	t.Setenv(allowUnsignedFeedHeadsDevEnv, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	tmp := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	pStore, err := storage.NewFlatSQLStore(filepath.Join(tmp, "p", "p-db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pStore.Close() })
	vStore, err := storage.NewFlatSQLStore(filepath.Join(tmp, "v", "v-db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vStore.Close() })

	ph, pKey := feedHeadTestHostWithKey(t)
	vh, _ := feedHeadTestHostWithKey(t)
	xh, _ := feedHeadTestHostWithKey(t)
	ph.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(pStore).HandleStream)

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: ph.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	node := &Node{
		host: vh, store: vStore, peerRegistry: registry, ctx: ctx,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmp, "v", "v-storage")},
			Peers: config.PeersConfig{TrustedImport: config.TrustedImportConfig{
				Allow: []config.LaneSelector{{Schema: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp"}},
			}},
		},
	}
	node.laneRetention = func(string, string, string) string { return channels.RetentionReplaceCurrent }

	pps, err := newGossipSub(ctx, ph)
	if err != nil {
		t.Fatal(err)
	}
	vps, err := newGossipSub(ctx, vh)
	if err != nil {
		t.Fatal(err)
	}
	xps, err := newGossipSub(ctx, xh)
	if err != nil {
		t.Fatal(err)
	}
	topicName := sdnpubsub.DatasetFeedHeadTopic("OMM.fbs")
	drain := func(ps *pubsub.PubSub) *pubsub.Topic {
		topic, err := ps.Join(topicName)
		if err != nil {
			t.Fatal(err)
		}
		sub, err := topic.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				if _, err := sub.Next(ctx); err != nil {
					return
				}
			}
		}()
		return topic
	}
	pTopic := drain(pps)
	xTopic := drain(xps)
	if err := xh.Connect(ctx, peer.AddrInfo{ID: ph.ID(), Addrs: ph.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if opts.relayOnly {
		rh, _ := feedHeadTestHostWithKey(t)
		rh.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(pStore).HandleStream)
		rps, err := newGossipSub(ctx, rh)
		if err != nil {
			t.Fatal(err)
		}
		drain(rps)
		if err := registry.AddPeer(&peers.TrustedPeer{ID: rh.ID(), TrustLevel: peers.Trusted}); err != nil {
			t.Fatal(err)
		}
		if err := rh.Connect(ctx, peer.AddrInfo{ID: ph.ID(), Addrs: ph.Addrs()}); err != nil {
			t.Fatal(err)
		}
		if err := vh.Connect(ctx, peer.AddrInfo{ID: rh.ID(), Addrs: rh.Addrs()}); err != nil {
			t.Fatal(err)
		}
	} else if err := vh.Connect(ctx, peer.AddrInfo{ID: ph.ID(), Addrs: ph.Addrs()}); err != nil {
		t.Fatal(err)
	}

	vTopic, err := vps.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	vSub, err := vTopic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan feedHeadResult, 16)
	if opts.liveHandler {
		node.wg.Add(1)
		go node.handleDatasetFeedHeadSubscription(vSub, "OMM.fbs")
	} else {
		go func() {
			for {
				msg, err := vSub.Next(ctx)
				if err != nil {
					return
				}
				imported, err := node.handleDatasetFeedHeadMessage(ctx, msg, "OMM.fbs")
				results <- feedHeadResult{imported: imported, err: err}
			}
		}()
	}
	time.Sleep(2 * time.Second) // mesh formation
	return &feedHeadNet{ctx: ctx, pStore: pStore, vStore: vStore, pHost: ph, pKey: pKey, node: node, pTopic: pTopic, xTopic: xTopic, results: results,
		baseTime: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second), live: opts.liveHandler}
}

func (f *feedHeadNet) publish(t *testing.T, topic *pubsub.Topic, ann sdnpubsub.DatasetFeedHeadAnnouncement) feedHeadResult {
	t.Helper()
	payload, err := json.Marshal(ann)
	if err != nil {
		t.Fatal(err)
	}
	if err := topic.Publish(f.ctx, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-f.results:
		return result
	case <-time.After(30 * time.Second):
		t.Fatalf("feed head %s never reached the victim", ann.FeedHead)
	}
	return feedHeadResult{}
}

// waitForLane waits for the lane's retention to settle on current: current
// is the lane's newest servable batch and every older batch's records are
// evicted (ReplaceCurrent).
func (f *feedHeadNet) waitForLane(t *testing.T, current string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if !f.live {
			f.node.wg.Wait()
		}
		cur, older, ok, err := f.vStore.OlderLaneBatches("OMM.fbs", "space-data-network-02", "celestrak-gp")
		if err == nil && ok && cur == current {
			evicted := true
			for _, batch := range older {
				if sec031Count(t, f.vStore, batch) != 0 {
					evicted = false
				}
			}
			if evicted && sec031Count(t, f.vStore, current) > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("lane = %s, want current %s with every older batch evicted", sec031Lane(t, f.vStore), current)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestGossipedFeedHeadsAreAdmittedOnTheirAuthorsSignature is the audit's
// SEC-031 repro and the verifier's bypass variants (S1, S2) over real
// libp2p hosts, each head travelling by gossip into the node's feed-head
// receive path (handleDatasetFeedHeadMessage). Genuine producer heads are
// imported; every forgery is refused before anything is fetched or stored;
// the lane keeps its genuine newest batch and still takes the next one.
func TestGossipedFeedHeadsAreAdmittedOnTheirAuthorsSignature(t *testing.T) {
	f := newFeedHeadNet(t, feedHeadNetOptions{})
	producer := f.pHost.ID().String()
	pubA := producerPublish(t, f.pStore, producer, "batch-A-old", 1001, f.baseTime)
	pubB := producerPublish(t, f.pStore, producer, "batch-B-new", 1002, f.baseTime.Add(time.Hour))

	// Genuine history arrives by gossip and is imported.
	for _, p := range []storage.DatasetShardPublication{pubA, pubB} {
		if r := f.publish(t, f.pTopic, producerFeedHead(t, p, f.pKey)); r.err != nil || r.imported == 0 {
			t.Fatalf("genuine head %s: imported=%d err=%v", p.BatchID, r.imported, r.err)
		}
	}
	f.waitForLane(t, pubB.BatchID)

	_, xKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	forge := func(from storage.DatasetShardPublication, key ed25519.PrivateKey, mutate func(*sdnpubsub.DatasetFeedHeadAnnouncement)) sdnpubsub.DatasetFeedHeadAnnouncement {
		ann := producerFeedHead(t, from, nil)
		mutate(&ann)
		if key != nil {
			if err := sdnpubsub.SignDatasetFeedHead(&ann, key); err != nil {
				t.Fatal(err)
			}
		}
		return ann
	}
	attacks := []struct {
		name  string
		topic *pubsub.Topic
		ann   sdnpubsub.DatasetFeedHeadAnnouncement
		want  string
	}{
		{"V0 audit repro: untrusted X, MaxInt64 sequence, year 2099", f.xTopic, forge(pubA, xKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.FeedSequence, a.PublishedAt, a.FeedHead = math.MaxInt64, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "attacker-head"
		}), "invalid dataset feed-head announcement"},
		{"V0b untrusted X, plausible ranking", f.xTopic, forge(pubA, xKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = 2, now, 10, "attacker-head-2"
		}), "not a trusted peer"},
		{"V0c untrusted X claiming P's key in the message", f.xTopic, forge(pubA, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = 2, now, 10, "attacker-head-3"
		}), "not a trusted peer"},
		{"V1 trusted P extends its superseded batch A", f.pTopic, forge(pubA, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = 1+datasetFeedHeadMaxSequenceStep, now, 10, "p-forged-head"
		}), "after the newer batch batch-B-new began"},
		{"V2 trusted P, empty lane fields", f.pTopic, forge(pubA, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.ProviderID, a.SourceName, a.BatchID = "", "", ""
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = sdnpubsub.DatasetFeedHeadMaxSequence, now, 20, "p-forged-head2"
		}), "names no"},
		{"V2b trusted P, empty batch_id only", f.pTopic, forge(pubA, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.BatchID = ""
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = sdnpubsub.DatasetFeedHeadMaxSequence, now, 40, "p-forged-head2b"
		}), "names no batch_id"},
		{"V3 trusted P, unsigned", f.pTopic, forge(pubA, nil, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) {
			a.FeedSequence, a.PublishedAt, a.Offset, a.FeedHead = 99, now, 30, "p-unsigned"
		}), "unsigned"},
	}
	for _, attack := range attacks {
		r := f.publish(t, attack.topic, attack.ann)
		if r.err == nil || r.imported != 0 {
			t.Fatalf("%s: imported=%d err=%v, want refusal", attack.name, r.imported, r.err)
		}
		if !strings.Contains(r.err.Error(), attack.want) {
			t.Fatalf("%s: refused for the wrong reason: %v (want %q)", attack.name, r.err, attack.want)
		}
		f.waitForLane(t, pubB.BatchID)
	}

	// A trusted author's head whose commitment does not match the content
	// it names: refused before anything is imported.
	pubC := producerPublish(t, f.pStore, producer, "batch-C-newest", 1003, f.baseTime.Add(2*time.Hour))
	tampered := forge(pubC, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) { a.FeedHead = "not-the-content" })
	if r := f.publish(t, f.pTopic, tampered); r.err == nil || !errors.Is(r.err, errFeedHeadContentMismatch) {
		t.Fatalf("head not committing to its content: imported=%d err=%v", r.imported, r.err)
	}
	if got := sec031Count(t, f.vStore, pubC.BatchID); got != 0 {
		t.Fatalf("records of a refused head were imported: %d", got)
	}
	wrongCount := forge(pubC, f.pKey, func(a *sdnpubsub.DatasetFeedHeadAnnouncement) { a.RecordCount = 7 })
	if r := f.publish(t, f.pTopic, wrongCount); r.err == nil || !errors.Is(r.err, errFeedHeadContentMismatch) {
		t.Fatalf("head with a false record count: imported=%d err=%v", r.imported, r.err)
	}

	// Nothing forged reached the ledger, and the genuine next batch is taken.
	rows, err := f.vStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: "OMM.fbs", QueryProfile: storage.DatasetPublicationQueryProfile})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Offset != 0 || row.FeedSequence > 2 {
			t.Fatalf("a forged head reached the publication ledger: %+v", row)
		}
	}
	if r := f.publish(t, f.pTopic, producerFeedHead(t, pubC, f.pKey)); r.err != nil || r.imported == 0 {
		t.Fatalf("genuine head %s: imported=%d err=%v", pubC.BatchID, r.imported, r.err)
	}
	f.waitForLane(t, pubC.BatchID)
	if got := sec031Count(t, f.vStore, pubC.BatchID); got != 1 {
		t.Fatalf("batch C records = %d, want 1", got)
	}
}

// TestGossipedFeedHeadIsFetchedFromATrustedRelayWhenTheAuthorIsUnreachable:
// the bytes are bound to the author's signed head, so a trusted relay may
// serve them when the author cannot be dialled.
func TestGossipedFeedHeadIsFetchedFromATrustedRelayWhenTheAuthorIsUnreachable(t *testing.T) {
	f := newFeedHeadNet(t, feedHeadNetOptions{relayOnly: true})
	pub := producerPublish(t, f.pStore, f.pHost.ID().String(), "batch-relayed", 2001, f.baseTime)
	r := f.publish(t, f.pTopic, producerFeedHead(t, pub, f.pKey))
	if r.err != nil || r.imported != 1 {
		t.Fatalf("relayed genuine head: imported=%d err=%v", r.imported, r.err)
	}
	if got := f.node.host.Network().Connectedness(f.pHost.ID()); got == network.Connected {
		t.Fatalf("the victim reached the author directly (%v); this test needs the relay to serve", got)
	}
	f.waitForLane(t, pub.BatchID)
}

func sec031Count(t *testing.T, st *storage.FlatSQLStore, batch string) int {
	t.Helper()
	recs, err := st.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: batch, Limit: 10})
	if err != nil {
		t.Fatalf("query %s: %v", batch, err)
	}
	return len(recs)
}

func sec031Lane(t *testing.T, st *storage.FlatSQLStore) string {
	t.Helper()
	cur, older, ok, err := st.OlderLaneBatches("OMM.fbs", "space-data-network-02", "celestrak-gp")
	if err != nil {
		t.Fatalf("OlderLaneBatches: %v", err)
	}
	b, _ := json.Marshal(map[string]interface{}{"current": cur, "older": older, "ok": ok})
	return string(b)
}
