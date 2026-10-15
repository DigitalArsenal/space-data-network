package node

// SEC-031 regressions for the dataset feed-head admission gate.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	n := &Node{peerRegistry: registry}
	t.Setenv(allowUnsignedFeedHeadsDevEnv, "")
	now := time.Now().UTC()

	// The audit attack: an untrusted author's self-signed head, relayed by
	// the trusted peer.
	forged := testFeedHead(math.MaxInt64, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	forged = signedFeedHead(t, forged, attacker.priv)
	if _, _, err := n.admitDatasetFeedHead(context.Background(), feedHeadMessage(t, attacker.id, trusted.id, forged), "OMM.fbs"); err == nil || !strings.Contains(err.Error(), "not a trusted peer") {
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
	n := &Node{peerRegistry: registry}
	now := time.Now().UTC()
	for name, ann := range map[string]sdnpubsub.DatasetFeedHeadAnnouncement{
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
	now := time.Now().UTC().Truncate(time.Second)

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
	for _, pub := range []storage.DatasetShardPublication{
		row("batch-good", 2, now.Add(-time.Hour)),
		row("batch-forged-seq", math.MaxInt64, now.Add(-2*time.Hour)),
		row("batch-forged-time", 1, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)),
	} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := RepairImplausibleDatasetFeedPublications(store, storage.DatasetPublicationQueryProfile, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("dry run found %d implausible rows, want 2: %+v", len(plan), plan)
	}
	if _, err := RepairImplausibleDatasetFeedPublications(store, storage.DatasetPublicationQueryProfile, now, true); err != nil {
		t.Fatal(err)
	}
	left, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: "OMM.fbs", QueryProfile: storage.DatasetPublicationQueryProfile})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].BatchID != "batch-good" {
		t.Fatalf("after repair: %+v, want only batch-good", left)
	}
}

func feedHeadTestHost(t *testing.T) host.Host {
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
	return h
}

// TestForwardedForgedFeedHeadCannotRollBackLane is the audit's end-to-end
// repro over real libp2p hosts and the node's StrictSign gossipsub: X
// (untrusted, connected only to the trusted provider P) publishes a
// self-signed head naming P's OLD batch with a MaxInt64 sequence. The victim V
// gates every delivery through admitDatasetFeedHead before materializing, as
// handleDatasetFeedHeadSubscription does once wired; the lane must keep the
// genuine newest batch.
func TestForwardedForgedFeedHeadCannotRollBackLane(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ph, vh, xh := feedHeadTestHost(t), feedHeadTestHost(t), feedHeadTestHost(t)
	ph.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(pStore).HandleStream)

	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	pubA := sec031Publish(t, pStore, ph.ID().String(), "batch-A-old", 1001, 1, base)
	pubB := sec031Publish(t, pStore, ph.ID().String(), "batch-B-new", 1002, 2, base.Add(time.Hour))

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: ph.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	n := &Node{
		host: vh, store: vStore, peerRegistry: registry, ctx: ctx,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmp, "v", "v-storage")},
			Peers: config.PeersConfig{TrustedImport: config.TrustedImportConfig{
				Allow: []config.LaneSelector{{Schema: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp"}},
			}},
		},
	}
	n.laneRetention = func(string, string, string) string { return channels.RetentionReplaceCurrent }
	if err := vh.Connect(ctx, peer.AddrInfo{ID: ph.ID(), Addrs: ph.Addrs()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []storage.DatasetShardPublication{pubA, pubB} {
		if _, err := n.materializeDatasetFeedHeadAnnouncement(ctx, sec031Ann(p), ph.ID()); err != nil {
			t.Fatalf("genuine import %s: %v", p.BatchID, err)
		}
		n.wg.Wait()
	}
	if sec031Count(t, vStore, pubB.BatchID) == 0 {
		t.Fatal("setup: the genuine newest batch is not on the victim")
	}

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
	if err := xh.Connect(ctx, peer.AddrInfo{ID: ph.ID(), Addrs: ph.Addrs()}); err != nil {
		t.Fatal(err)
	}
	topicName := sdnpubsub.DatasetFeedHeadTopic("OMM.fbs")
	pt, err := pps.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	psub, err := pt.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, err := psub.Next(ctx); err != nil {
				return
			}
		}
	}()
	vt, err := vps.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	vsub, err := vt.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	refused := make(chan error, 4)
	go func() {
		for {
			msg, err := vsub.Next(ctx)
			if err != nil {
				return
			}
			ann, author, err := n.admitDatasetFeedHead(ctx, msg, "OMM.fbs")
			if err != nil {
				if !errors.Is(err, errOwnDatasetFeedHead) {
					refused <- err
				}
				continue
			}
			_, _ = n.materializeDatasetFeedHeadAnnouncement(ctx, ann, author)
		}
	}()
	xt, err := xps.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // mesh formation

	forged := sec031Ann(pubA)
	forged.FeedSequence = math.MaxInt64
	forged.PublishedAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	forged.FeedHead = "attacker-head"
	_, xPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdnpubsub.SignDatasetFeedHead(&forged, xPriv); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := xt.Publish(ctx, payload); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-refused:
		if !strings.Contains(err.Error(), "not a trusted peer") {
			t.Fatalf("forged head refused for the wrong reason: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the forged head never reached the victim's gate")
	}
	time.Sleep(500 * time.Millisecond)
	if got := sec031Count(t, vStore, pubB.BatchID); got == 0 {
		t.Fatalf("the genuine newest batch was evicted: lane=%s", sec031Lane(t, vStore))
	}
	if rows, _ := vStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: "OMM.fbs", QueryProfile: storage.DatasetPublicationQueryProfile}); len(rows) > 0 {
		for _, row := range rows {
			if row.FeedSequence == math.MaxInt64 || row.FeedHead == "attacker-head" {
				t.Fatalf("the forged head reached the publication ledger: %+v", row)
			}
		}
	}
}

func sec031Publish(t *testing.T, st *storage.FlatSQLStore, producer string, batch string, norad uint32, seq int64, at time.Time) storage.DatasetShardPublication {
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
		QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256, FeedSequence: seq, FeedHead: "head-" + batch, PublishedAt: at,
	}
	if err := st.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatalf("upsert %s: %v", batch, err)
	}
	return pub
}

func sec031Ann(p storage.DatasetShardPublication) sdnpubsub.DatasetFeedHeadAnnouncement {
	return sdnpubsub.DatasetFeedHeadAnnouncement{
		MessageType: sdnpubsub.DatasetFeedHeadMessageType, Schema: p.SchemaName, ProviderID: p.ProviderID, SourceName: p.SourceName,
		BatchID: p.BatchID, QueryProfile: p.QueryProfile, Limit: p.Limit, FeedSequence: p.FeedSequence, FeedHead: p.FeedHead,
		RecordCount: p.RecordCount, ByteCount: p.ByteCount, ShardCID: p.ShardCID, IndexCID: p.IndexCID, PublishedAt: p.PublishedAt,
	}
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
