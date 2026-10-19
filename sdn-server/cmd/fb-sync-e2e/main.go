// fb-sync-e2e proves the flatsql-sync 2.0.0 / pubsub / ws wire is
// FlatBuffers only, end to end, between two live libp2p nodes on loopback:
// a provider (real FlatSQL store, the production FlatSQLSyncHandler with no
// private-read authority, gossipsub with the production structure
// validators, the production CoreAPIHandler pubsub routes and /ws handler on
// a real HTTP listener) and a subscriber (its own FlatSQL store and
// gossipsub).
//
//	(a) a lane syncs over read-chunk; every received record frame is
//	    byte-identical (sha256) to the bytes the provider stored, in order;
//	(b) a published segment lists over list-published and reads over
//	    read-shard / read-index byte-identical to the provider's files;
//	(c) a signed feed head crosses gossipsub on the 2.0.0 topic and
//	    verifies; a tampered copy is refused;
//	(d) POST /api/v1/pubsub/publish with a size-prefixed OMM frame arrives
//	    at the subscriber over gossipsub byte-identical, and /ws delivers a
//	    published frame verbatim;
//	(e) a dial of /space-data-network/flatsql-sync/1.0.0 fails negotiation;
//	(f) no sync response frame, pubsub message or ws frame is JSON.
//
//	fb-sync-e2e [--work DIR] [--records N]
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	schema   = "OMM.fbs"
	provider = "fb-sync-e2e"
)

var lanes = []string{"celestrak-gp", "spacetrack-gp"}

type result struct {
	name   string
	ok     bool
	detail string
}

var results []result

func check(name string, ok bool, format string, args ...any) {
	results = append(results, result{name, ok, fmt.Sprintf(format, args...)})
}

// frameAudit is assertion (f): every frame observed on a sync stream, a
// pubsub message or a ws message.
type frameAudit struct {
	frames, braceFirst int
	bad                []string
}

// observe records one complete size-prefixed frame. It must be a
// FlatBuffer (exact length prefix, a known file identifier); JSON text fails
// both. A first byte of '{' is only the low byte of a 123-byte length
// prefix; it is counted and allowed only for such a verified frame.
func (a *frameAudit) observe(where string, frame []byte, validator *sds.Validator) {
	a.frames++
	if len(frame) < 12 || int(binary.LittleEndian.Uint32(frame)) != len(frame)-4 {
		a.bad = append(a.bad, where+": not one size-prefixed frame")
		return
	}
	if _, known := validator.SchemaForIdentifier(syncwire.FrameIdentifier(frame)); !known {
		a.bad = append(a.bad, fmt.Sprintf("%s: unknown identifier %q", where, syncwire.FrameIdentifier(frame)))
		return
	}
	if frame[0] == '{' {
		a.braceFirst++
	}
}

func main() {
	work := flag.String("work", "", "work directory (default: a temp dir, removed after)")
	records := flag.Int("records", 2500, "records to seed on the provider lane")
	jsClient := flag.String("js-client", "", "sdn-js directory with a built dist: also read the provider with the sdn-js client (js-client.mjs)")
	flag.Parse()
	err := run(*work, *records, *jsClient)
	failed := 0
	for _, r := range results {
		status := "PASS"
		if !r.ok {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s %s: %s\n", status, r.name, r.detail)
	}
	if err != nil {
		fmt.Printf("FAIL harness: %v\n", err)
		failed++
	}
	if failed > 0 || len(results) == 0 {
		fmt.Printf("FB-SYNC-E2E FAIL (%d)\n", failed)
		os.Exit(1)
	}
	fmt.Println("FB-SYNC-E2E PASS")
}

// ommRecord builds the i-th OMM record (bare root buffer, as stored).
func ommRecord(i int, sizePrefixed bool) []byte {
	b := flatbuffers.NewBuilder(256)
	name := b.CreateString(fmt.Sprintf("FB-SYNC-%06d", i))
	objectID := b.CreateString(fmt.Sprintf("2026-%03dA", i%1000))
	epoch := b.CreateString("2026-10-18T12:00:00Z")
	OMM.OMMStart(b)
	OMM.OMMAddNORAD_CAT_ID(b, uint32(90000+i))
	OMM.OMMAddOBJECT_NAME(b, name)
	OMM.OMMAddOBJECT_ID(b, objectID)
	OMM.OMMAddEPOCH(b, epoch)
	OMM.OMMAddMEAN_MOTION(b, 15.5+float64(i%100)/1000)
	end := OMM.OMMEnd(b)
	if sizePrefixed {
		OMM.FinishSizePrefixedOMMBuffer(b, end)
	} else {
		OMM.FinishOMMBuffer(b, end)
	}
	return append([]byte(nil), b.FinishedBytes()...)
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func run(work string, n int, jsClient string) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if work == "" {
		if work, err = os.MkdirTemp("", "fb-sync-e2e-"); err != nil {
			return err
		}
		defer os.RemoveAll(work)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(work, "provider"), validator)
	if err != nil {
		return fmt.Errorf("provider store: %w", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(work, "subscriber"), validator)
	if err != nil {
		return fmt.Errorf("subscriber store: %w", err)
	}
	defer subscriberStore.Close()

	// Seed: n OMM records over two lanes, in insert (rowid) order.
	var stored [][]byte
	for start := 0; start < n; start += 500 {
		batch := make([][]byte, 0, 500)
		for i := start; i < min(start+500, n); i++ {
			batch = append(batch, ommRecord(i, false))
		}
		tags := storage.SourceTags{ProviderID: provider, SourceName: lanes[(start/500)%len(lanes)], BatchID: "b" + strconv.Itoa(start/500), ProducerPeerID: "fb-sync-e2e"}
		if _, err := providerStore.StoreBatchWithSourceTags(schema, batch, "fb-sync-e2e", nil, tags); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		stored = append(stored, batch...)
	}
	// One published segment: a FlatSQL shard + index of 64 records.
	export, pub, err := publishSegment(providerStore, stored[:64])
	if err != nil {
		return err
	}

	providerHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		return err
	}
	defer providerHost.Close()
	subscriberHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		return err
	}
	defer subscriberHost.Close()
	// Production surface: no private-read authority, so read-chunk goes
	// through the public-read allow-list and publication ops through the
	// publication index.
	protocol.NewFlatSQLSyncHandler(providerStore).Register(providerHost)
	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	audit := &frameAudit{}

	checkReadChunk(ctx, subscriberHost, providerHost.ID(), stored, validator, audit)
	checkPublishedSegment(ctx, subscriberHost, providerHost.ID(), export, pub, validator, audit)
	if jsClient != "" {
		checkJSClient(ctx, jsClient, providerHost, stored, export, pub)
	}
	checkLegacyDialRefused(ctx, subscriberHost, providerHost.ID())

	// Gossipsub on both nodes, with the production topic validators.
	providerPS, err := pubsub.NewGossipSub(ctx, providerHost)
	if err != nil {
		return err
	}
	subscriberPS, err := pubsub.NewGossipSub(ctx, subscriberHost)
	if err != nil {
		return err
	}
	providerGossip, subscriberGossip := newGossip(providerPS, providerHost.ID()), newGossip(subscriberPS, subscriberHost.ID())
	checkFeedHead(ctx, providerGossip, subscriberGossip, pub, validator, audit)
	if err := checkHTTPPubSubAndWS(ctx, providerHost, providerPS, providerGossip, subscriberGossip, providerStore, validator, audit); err != nil {
		return err
	}

	check("f/no-json-frames", len(audit.bad) == 0 && audit.frames > 0,
		"%d frames observed, %d not FlatBuffers-only %v (%d start with 0x7b as the low byte of a 123-byte length prefix)",
		audit.frames, len(audit.bad), audit.bad, audit.braceFirst)
	return nil
}

func publishSegment(store *storage.FlatSQLStore, data [][]byte) (*storage.DatasetExport, storage.DatasetShardPublication, error) {
	tags := storage.SourceTags{ProviderID: provider, SourceName: "published-lane", BatchID: "pub-1"}
	records := make([]storage.DatasetExportRecord, len(data))
	for i, d := range data {
		records[i] = storage.DatasetExportRecord{CID: sum(d), Data: d, SourceTags: tags}
	}
	export, err := storage.ExportDatasetRecords(filepath.Join(store.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{
		SchemaName: schema, ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
		Limit: len(records), AllowLargeResultSet: true, OrderByCID: true,
	}, records)
	if err != nil {
		return nil, storage.DatasetShardPublication{}, fmt.Errorf("export segment: %w", err)
	}
	pub := storage.DatasetShardPublication{
		SchemaName: schema, ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile, Limit: export.RecordCount, RecordCount: export.RecordCount,
		ByteCount: export.ShardBytes, ShardCID: export.ShardCID, IndexCID: export.IndexCID, ManifestCID: "bafy-fb-sync-e2e-manifest",
		ShardSHA256: export.ShardSHA256, IndexSHA256: export.IndexSHA256, QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256,
		FeedSequence: 1, FeedHead: "fb-sync-e2e-head-1", PublishedAt: time.Now().UTC().Truncate(time.Second), // the store keeps seconds
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		return nil, pub, fmt.Errorf("register segment: %w", err)
	}
	return export, pub, nil
}

// (a) read-chunk: page the whole lane set; record frames must equal the
// stored bytes, in order.
func checkReadChunk(ctx context.Context, h host.Host, target peer.ID, stored [][]byte, validator *sds.Validator, audit *frameAudit) {
	var got [][]byte
	runs, pages := 0, 0
	cursor := ""
	var failure error
	for pages < 100 {
		stream, page, err := syncwire.Call(ctx, h, target, syncwire.OpReadChunk, syncwire.QRPFields{SchemaName: "OMM", Cursor: cursor, Limit: 1000})
		if err != nil {
			failure = err
			break
		}
		pages++
		next := ""
		for page != nil {
			audit.observe("read-chunk header", pageBytes(page), validator)
			runs++
			next = string(page.NEXT_CURSOR())
			for i := 0; i < int(page.RECORD_COUNT()); i++ {
				frame, err := syncwire.ReadFrame(stream, syncwire.MaxFrameBytes)
				if err != nil {
					failure = err
					break
				}
				audit.observe("read-chunk record", frame, validator)
				got = append(got, frame[4:])
			}
			page = nil
			if failure == nil {
				if p, _, err := syncwire.ReadPage(stream); err == nil {
					page = p
				} else if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "EOF") {
					failure = err
				}
			}
		}
		_ = stream.Close()
		if failure != nil || next == "" {
			break
		}
		cursor = next
	}
	identical := failure == nil && len(got) == len(stored)
	mismatch := -1
	for i := 0; identical && i < len(got); i++ {
		if sum(got[i]) != sum(stored[i]) {
			identical, mismatch = false, i
		}
	}
	check("a/read-chunk-byte-identical", identical,
		"%d/%d record frames sha256-identical in order over %s (%d pages, %d lane runs, mismatch=%d, err=%v)",
		len(got), len(stored), syncwire.ProtocolID(syncwire.OpReadChunk), pages, runs, mismatch, failure)
}

// pageBytes re-reads the frame a parsed $QRP came from.
func pageBytes(q interface{ Table() flatbuffers.Table }) []byte {
	t := q.Table()
	return t.Bytes
}

// (b) list-published + read-shard + read-index.
func checkPublishedSegment(ctx context.Context, h host.Host, target peer.ID, export *storage.DatasetExport, pub storage.DatasetShardPublication, validator *sds.Validator, audit *frameAudit) {
	lane := syncwire.QRPFields{SchemaName: schema, ProviderID: pub.ProviderID, SourceName: pub.SourceName, BatchID: pub.BatchID,
		Filters: []syncwire.QRPFilterField{{Field: syncwire.FilterQueryProfile, Op: syncwire.QRPFilterEq, Value: pub.QueryProfile}}}
	listReq := lane
	listReq.Limit = 10
	stream, page, err := syncwire.Call(ctx, h, target, syncwire.OpListPublished, listReq)
	var segs []syncwire.Segment
	if err == nil {
		audit.observe("list-published", pageBytes(page), validator)
		_ = stream.Close()
		segs, err = syncwire.Segments(page)
	}
	listed := err == nil && len(segs) == 1 && segs[0].ShardCID == pub.ShardCID && segs[0].IndexCID == pub.IndexCID &&
		segs[0].ShardSHA256 == pub.ShardSHA256 && segs[0].RecordCount == int64(pub.RecordCount) && segs[0].PublishedAtMs == pub.PublishedAt.UnixMilli() &&
		page.TOTAL_COUNT() == 1
	check("b/list-published-segment-row", listed, "%d SEGMENT row(s), TOTAL_COUNT=%d, shard %s (err=%v)", len(segs), totalOf(page), pub.ShardCID, err)

	read := func(op, cid, path string, wantSHA string) {
		req := lane
		req.CID = cid
		stream, page, err := syncwire.Call(ctx, h, target, op, req)
		if err != nil {
			check("b/"+op+"-byte-identical", false, "%v", err)
			return
		}
		defer stream.Close()
		audit.observe(op+" header", pageBytes(page), validator)
		rows, err := syncwire.Segments(page)
		if err != nil || len(rows) != 1 {
			check("b/"+op+"-byte-identical", false, "rows=%d err=%v", len(rows), err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(stream, rows[0].ByteLength))
		want, _ := os.ReadFile(path)
		ok := err == nil && int64(len(body)) == rows[0].ByteCount && bytes.Equal(body, want) && sum(body) == wantSHA
		check("b/"+op+"-byte-identical", ok, "%d bytes, sha256 %s (provider file %s, BYTE_COUNT=%d, err=%v)",
			len(body), short(sum(body)), short(wantSHA), rows[0].ByteCount, err)
	}
	read(syncwire.OpReadShard, pub.ShardCID, export.ShardPath, export.ShardSHA256)
	read(syncwire.OpReadIndex, pub.IndexCID, export.IndexPath, export.IndexSHA256)
}

func totalOf(q interface{ TOTAL_COUNT() uint64 }) uint64 {
	if q == nil {
		return 0
	}
	return q.TOTAL_COUNT()
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// (e) 1.0.0 is registered nowhere.
func checkLegacyDialRefused(ctx context.Context, h host.Host, target peer.ID) {
	stream, err := h.NewStream(ctx, target, libp2pprotocol.ID("/space-data-network/flatsql-sync/1.0.0"))
	if err == nil {
		_ = stream.Reset()
	}
	check("e/flatsql-sync-1.0.0-refused", err != nil && strings.Contains(err.Error(), "protocol"),
		"dial /space-data-network/flatsql-sync/1.0.0: %v", err)
}

// gossip is a minimal topic publisher over one gossipsub instance, joining
// each topic once with the production structure validator.
type gossip struct {
	ps     *pubsub.PubSub
	id     peer.ID
	topics map[string]*pubsub.Topic
}

func newGossip(ps *pubsub.PubSub, id peer.ID) *gossip {
	return &gossip{ps: ps, id: id, topics: map[string]*pubsub.Topic{}}
}

func (g *gossip) topic(name string) (*pubsub.Topic, error) {
	if t := g.topics[name]; t != nil {
		return t, nil
	}
	if err := sdnpubsub.RegisterTopicStructureValidator(g.ps, name); err != nil {
		return nil, err
	}
	t, err := g.ps.Join(name)
	if err != nil {
		return nil, err
	}
	g.topics[name] = t
	return t, nil
}

func (g *gossip) PublishToTopic(ctx context.Context, name string, data []byte) error {
	t, err := g.topic(name)
	if err != nil {
		return err
	}
	return t.Publish(ctx, data)
}

func (g *gossip) subscribe(name string) (*pubsub.Subscription, error) {
	t, err := g.topic(name)
	if err != nil {
		return nil, err
	}
	return t.Subscribe()
}

// waitMesh waits until the publisher sees the subscriber on topic.
func waitMesh(ctx context.Context, g *gossip, name string, want peer.ID) error {
	t, err := g.topic(name)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		for _, p := range t.ListPeers() {
			if p == want {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("topic %s: peer %s never joined", name, want.ShortString())
}

func next(ctx context.Context, sub *pubsub.Subscription) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	msg, err := sub.Next(ctx)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// (c) signed feed head over gossipsub; a tampered copy is refused.
func checkFeedHead(ctx context.Context, providerGossip, subscriberGossip *gossip, pub storage.DatasetShardPublication, validator *sds.Validator, audit *frameAudit) {
	topic := sdnpubsub.DatasetFeedHeadTopic(schema)
	sub, err := subscriberGossip.subscribe(topic)
	if err == nil {
		defer sub.Cancel()
		err = waitMesh(ctx, providerGossip, topic, subscriberGossip.id)
	}
	if err != nil {
		check("c/feed-head-signed-verifies", false, "%v", err)
		return
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	if err := sdnpubsub.PublishSignedDatasetFeedHead(ctx, providerGossip, syncwire.SegmentFromPublication(pub), key); err != nil {
		check("c/feed-head-signed-verifies", false, "publish: %v", err)
		return
	}
	data, err := next(ctx, sub)
	if err != nil {
		check("c/feed-head-signed-verifies", false, "receive: %v", err)
		return
	}
	audit.observe("feed-head gossip", data, validator)
	ann, err := sdnpubsub.ParseDatasetFeedHeadAnnouncement(data)
	if err == nil {
		err = sdnpubsub.VerifyDatasetFeedHead(ann, key.Public().(ed25519.PublicKey))
	}
	check("c/feed-head-signed-verifies", err == nil && ann.ShardCID == pub.ShardCID && len(ann.Signature) == ed25519.SignatureSize,
		"%d-byte $QRP Page on %s, shard %s, Ed25519 over the canonical frame verifies against the provider key (err=%v)", len(data), topic, ann.ShardCID, err)

	// Tamper: change one byte inside the FEED_HEAD text cell.
	tampered := bytes.Replace(append([]byte(nil), data...), []byte(pub.FeedHead), []byte(strings.Replace(pub.FeedHead, "head-1", "head-9", 1)), 1)
	bad, perr := sdnpubsub.ParseDatasetFeedHeadAnnouncement(tampered)
	verr := errors.New("not parsed")
	if perr == nil {
		verr = sdnpubsub.VerifyDatasetFeedHead(bad, nil)
	}
	check("c/feed-head-tampered-refused", !bytes.Equal(tampered, data) && perr == nil && verr != nil,
		"FEED_HEAD %q -> %q: %v", pub.FeedHead, bad.FeedHead, verr)
}

// (d) HTTP pubsub publish -> gossipsub, /messages, /topics, and /ws.
func checkHTTPPubSubAndWS(ctx context.Context, providerHost host.Host, providerPS *pubsub.PubSub, providerGossip, subscriberGossip *gossip, store *storage.FlatSQLStore, validator *sds.Validator, audit *frameAudit) error {
	core := api.NewCoreAPIHandler(providerHost.ID(), providerHost, providerPS, providerGossip, store, validator, nil, nil, nil)
	mux := http.NewServeMux()
	core.RegisterRoutes(mux)
	mux.Handle("/ws", api.NewWSHandler(providerGossip, validator))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	base := "http://" + listener.Addr().String()

	topic := sdnpubsub.TopicName(schema)
	sub, err := subscriberGossip.subscribe(topic)
	if err != nil {
		return err
	}
	defer sub.Cancel()
	if err := waitMesh(ctx, providerGossip, topic, subscriberGossip.id); err != nil {
		return err
	}

	// POST /api/v1/pubsub/publish: one size-prefixed OMM frame.
	frame := ommRecord(777777, true)
	resp, err := http.Post(base+"/api/v1/pubsub/publish", api.StreamContentType, bytes.NewReader(frame))
	if err != nil {
		return err
	}
	reply, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	audit.observe("publish reply", reply, validator)
	replyOK := false
	if q, err := syncwire.ParseQRP(reply); err == nil {
		replyOK = int8(q.KIND()) == syncwire.QRPKindPage && string(q.SCHEMA_NAME()) == schema && q.RECORD_COUNT() == 1
	}
	got, err := next(ctx, sub)
	if err == nil {
		audit.observe("pubsub message", got, validator)
	}
	check("d/http-publish-arrives-byte-identical", resp.StatusCode == http.StatusOK && replyOK && err == nil && bytes.Equal(got, frame),
		"HTTP %d, $QRP Page reply=%t, gossip message %d bytes sha256 %s == published %s (err=%v)",
		resp.StatusCode, replyOK, len(got), short(sum(got)), short(sum(frame)), err)

	// JSON is refused outright.
	resp, err = http.Post(base+"/api/v1/pubsub/publish", "text/plain", strings.NewReader(`{"schema":"OMM.fbs","data":"AAAA"}`))
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		audit.observe("publish json refusal", body, validator)
		check("d/http-publish-json-refused", resp.StatusCode == http.StatusUnsupportedMediaType, "HTTP %d, one $QRP Error frame", resp.StatusCode)
	}

	// The provider subscribes to its schema topic, as a node does; the
	// topic listing reports subscribed topics.
	own, err := providerGossip.subscribe(topic)
	if err != nil {
		return err
	}
	defer own.Cancel()

	// GET /messages and /topics: $QRP Page + frames.
	for _, path := range []string{"/api/v1/pubsub/messages?schema=OMM.fbs&limit=5", "/api/v1/pubsub/topics"} {
		resp, err := http.Get(base + path)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		frames, err := syncwire.SplitFrames(body)
		for _, f := range frames {
			audit.observe(path, f, validator)
		}
		ok := err == nil && len(frames) > 0 && resp.StatusCode == http.StatusOK
		if ok {
			q, perr := syncwire.ParseQRP(frames[0])
			ok = perr == nil && int8(q.KIND()) == syncwire.QRPKindPage
			if strings.Contains(path, "messages") {
				ok = ok && int(q.RECORD_COUNT()) == len(frames)-1 && len(frames) == 6
			} else {
				ok = ok && len(syncwire.Tables(q)) > 0
			}
		}
		check("d/http-"+strings.Split(strings.TrimPrefix(path, "/api/v1/pubsub/"), "?")[0]+"-flatbuffers", ok, "HTTP %d, %d frames", resp.StatusCode, len(frames))
	}

	// /ws: subscribe, publish from a second connection, receive verbatim;
	// a text frame is refused with a $QRP Error.
	wsURL := "ws://" + listener.Addr().String() + "/ws"
	subscriber, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer subscriber.Close()
	publisher, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer publisher.Close()
	_ = subscriber.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := subscriber.WriteMessage(websocket.BinaryMessage, dssSubscribe(schema)); err != nil {
		return err
	}
	_, ack, err := subscriber.ReadMessage()
	if err != nil {
		return err
	}
	audit.observe("ws ack", ack, validator)
	acked := DSS.SizePrefixedDSSBufferHasIdentifier(ack) && DSS.GetSizePrefixedRootAsDSS(ack, 0).SUBSCRIBED()
	wsFrame := ommRecord(888888, true)
	if err := publisher.WriteMessage(websocket.BinaryMessage, wsFrame); err != nil {
		return err
	}
	kind, delivered, err := subscriber.ReadMessage()
	if err == nil {
		audit.observe("ws message", delivered, validator)
	}
	check("d/ws-publish-delivered-verbatim", acked && err == nil && kind == websocket.BinaryMessage && bytes.Equal(delivered, wsFrame),
		"$DSS ack SUBSCRIBED=%t, binary=%t, %d bytes identical=%t (err=%v)", acked, kind == websocket.BinaryMessage, len(delivered), bytes.Equal(delivered, wsFrame), err)

	_ = publisher.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := publisher.WriteMessage(websocket.TextMessage, []byte(`{"type":"subscribe","schema":"OMM.fbs"}`)); err != nil {
		return err
	}
	kind, refusal, err := publisher.ReadMessage()
	refused := false
	if err == nil {
		audit.observe("ws refusal", refusal, validator)
		if q, perr := syncwire.ParseQRP(refusal); perr == nil {
			refused = int8(q.KIND()) == syncwire.QRPKindError && string(q.ERROR_CODE()) == "text_frame_refused"
		}
	}
	check("d/ws-text-frame-refused", kind == websocket.BinaryMessage && refused, "reply is one $QRP Error text_frame_refused=%t (err=%v)", refused, err)
	return nil
}

func dssSubscribe(schema string) []byte {
	b := flatbuffers.NewBuilder(64)
	name := b.CreateString(schema)
	DSS.DSSStart(b)
	DSS.DSSAddSCHEMA_NAME(b, name)
	DSS.DSSAddREQUESTED_ACTION(b, DSS.EnumValuesdssAction["Subscribe"])
	DSS.FinishSizePrefixedDSSBuffer(b, DSS.DSSEnd(b))
	return b.FinishedBytes()
}

// (g) the sdn-js client (js-client.mjs, the built dist) reads the same
// provider over libp2p TCP: every read-chunk run stream must equal the
// stored records with their u32le size prefixes, in order, and the
// list-published row and read-shard bytes must equal the publication.
func checkJSClient(ctx context.Context, sdnJS string, provider host.Host, stored [][]byte, export *storage.DatasetExport, pub storage.DatasetShardPublication) {
	script, err := filepath.Abs(filepath.Join("sdn-server", "cmd", "fb-sync-e2e", "js-client.mjs"))
	if err != nil {
		check("g/js-client", false, "%v", err)
		return
	}
	addr := provider.Addrs()[0].String() + "/p2p/" + provider.ID().String()
	cmd := exec.CommandContext(ctx, "node", script, sdnJS, addr, provider.ID().String(), pub.ProviderID, pub.SourceName, pub.BatchID)
	cmd.Dir = sdnJS
	out, runErr := cmd.CombinedOutput()
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			got[k] = v
		}
	}
	if runErr != nil || got["error"] != "" {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		check("g/js-client", false, "node js-client.mjs: %v: %q", runErr, tail)
		return
	}
	frames := sha256.New()
	var prefix [4]byte
	for _, record := range stored {
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(record)))
		frames.Write(prefix[:])
		frames.Write(record)
	}
	wantFrames := hex.EncodeToString(frames.Sum(nil))
	check("g/js-read-chunk-byte-identical", got["read-chunk-records"] == strconv.Itoa(len(stored)) && got["read-chunk-frames-sha256"] == wantFrames,
		"sdn-js read %s/%d records in %s pages, %s lane runs; size-prefixed frames sha256 %s (provider %s)",
		got["read-chunk-records"], len(stored), got["read-chunk-pages"], got["read-chunk-runs"], short(got["read-chunk-frames-sha256"]), short(wantFrames))
	check("g/js-list-published-segment-row", got["list-published-total"] == "1" && got["list-published-rows"] == "1" &&
		got["list-published-shard-cid"] == pub.ShardCID && got["list-published-shard-sha256"] == pub.ShardSHA256 &&
		got["list-published-at-ms"] == strconv.FormatInt(pub.PublishedAt.UnixMilli(), 10),
		"sdn-js decoded TOTAL_COUNT=%s, %s row(s), SHARD_CID %s, PUBLISHED_AT_MS %s",
		got["list-published-total"], got["list-published-rows"], got["list-published-shard-cid"], got["list-published-at-ms"])
	check("g/js-read-shard-byte-identical", got["read-shard-bytes"] == strconv.FormatInt(export.ShardBytes, 10) && got["read-shard-sha256"] == export.ShardSHA256,
		"sdn-js read %s bytes, sha256 %s (provider file %d bytes, %s)",
		got["read-shard-bytes"], short(got["read-shard-sha256"]), export.ShardBytes, short(export.ShardSHA256))
}
