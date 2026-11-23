package protocol

// flatsql-sync 2.0.0: chunked, resumable FlatSQL record and publication reads
// over any libp2p stream transport (TCP, WebSocket, WebRTC, WebTransport).
//
// One protocol ID per op (syncwire.ProtocolID). The request is exactly one
// size-prefixed $QRP{KIND=Request} frame; every reply begins with a $QRP
// Page or Error frame (u32 little-endian length prefix). read-chunk replies
// exactly as POST /api/v1/data/scan does: runs of [$QRP Page][RECORD_COUNT
// record frames]. Publication ops answer SEGMENT_COLUMNS rows; byte ops
// follow their header with the raw shard or index bytes it counts.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/readbudget"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	defaultWireSpeedProbeBytes    int64 = 64 * 1024 * 1024
	maxWireSpeedProbeBytes        int64 = 512 * 1024 * 1024
	wireSpeedProbeChunkBytes            = 1024 * 1024
	publishedShardCopyBufferBytes       = 1024 * 1024
)

// FlatSQLSyncHandler serves the flatsql-sync 2.0.0 ops.
type FlatSQLSyncHandler struct {
	store *storage.FlatSQLStore
	// readAuthorizer admits a peer to schemas outside the anonymous read
	// surface, to other registered datastores, to the datastore listing and
	// to the wire-speed probe (NET-02, NET-06). Unset: nobody.
	readAuthorizer   PeerAuthorizer
	probes           *wireSpeedProbeBudget
	publicationReads *readbudget.Budget
	// follows counts followed read-chunk streams per lane (FollowStats).
	follows *followStats
	// followSlots limits the followed streams served at once.
	followSlots *followSlots
	// followTrust names the peers this node has a trust relation with; a
	// followed stream of any other peer gets the anonymous limits. Unset:
	// every peer is anonymous.
	followTrust PeerAuthorizer
	// followWork limits the store reads served to followers at once
	// (flatsql_sync_work.go).
	followWork *followWork
}

// syncRequest is one decoded $QRP request.
type syncRequest struct {
	op           string
	q            *QRP.QRP
	schema       string
	providerID   string
	sourceName   string
	batchID      string
	queryProfile string
	datastoreKey string
	cid          string
	cursor       string
	limit        int
	maxBytes     uint64
	shardCIDs    []string
}

// NewFlatSQLSyncHandler creates a FlatSQL sync stream handler.
//
// As constructed it serves only the anonymous read surface: the schemas
// sds.IsPublicReadSchema names, from the node's own datastore. Everything
// else needs SetPrivateReadAuthorizer.
func NewFlatSQLSyncHandler(store *storage.FlatSQLStore) *FlatSQLSyncHandler {
	return &FlatSQLSyncHandler{store: store, probes: newWireSpeedProbeBudget(), publicationReads: newPublicationListBudget(), follows: newFollowStats(), followSlots: newFollowSlots(),
		followWork: newFollowWork()}
}

// SetPrivateReadAuthorizer installs the check that admits a peer to private
// schemas, other datastores, list-datastores and wire-speed-probe. It returns
// h so registration can chain it.
func (h *FlatSQLSyncHandler) SetPrivateReadAuthorizer(fn PeerAuthorizer) *FlatSQLSyncHandler {
	h.readAuthorizer = fn
	return h
}

// SetFollowTrust installs the check naming the peers this node has a trust
// relation with (followed streams: the trusted limits). It returns h so
// registration can chain it.
func (h *FlatSQLSyncHandler) SetFollowTrust(fn PeerAuthorizer) *FlatSQLSyncHandler {
	h.followTrust = fn
	return h
}

// StreamHandlerSetter is what Register needs of a libp2p host.
type StreamHandlerSetter interface {
	SetStreamHandler(pid libp2pprotocol.ID, handler network.StreamHandler)
}

// Register installs one stream handler per op on host.
func (h *FlatSQLSyncHandler) Register(host StreamHandlerSetter) {
	for _, op := range syncwire.Ops {
		host.SetStreamHandler(libp2pprotocol.ID(syncwire.ProtocolID(op)), h.streamHandler(op))
	}
}

func (h *FlatSQLSyncHandler) streamHandler(op string) network.StreamHandler {
	return func(s network.Stream) { h.HandleStream(op, s) }
}

// authorize applies the read surface to one request before any op runs. For
// a peer without private-read authority it rewrites the request's schema to
// the canonical public name it checked, so the name checked is the name read.
func (h *FlatSQLSyncHandler) authorize(peerID peer.ID, req *syncRequest) error {
	authorized := authorizedPeer(h.readAuthorizer, peerID)
	switch req.op {
	case syncwire.OpListDatastores, syncwire.OpWireSpeedProbe:
		if !authorized {
			return fmt.Errorf("%w: %s needs private-read authority", syncwire.ErrNotAuthorized, req.op)
		}
		return nil
	}
	if authorized {
		return nil
	}
	if req.datastoreKey != "" {
		return fmt.Errorf("%w: DATASTORE_KEY needs private-read authority", syncwire.ErrNotAuthorized)
	}
	if publishedLaneOps[req.op] {
		// The dataset-publication lane serves only what this node holds as a
		// dataset publication: shards and indexes announced on open topics,
		// whose manifests are pinned publicly. Its handlers look every byte
		// up in the publication index, never in a raw table, so it is open
		// to any peer for any well-formed schema name (trusted catch-up
		// replicates every standard over it). Raw tables stay behind the
		// public-read allow-list below.
		if req.schema == "" || sds.ValidateSchemaName(req.schema) != nil {
			return fmt.Errorf("%w (%s)", ErrReadNotAuthorized, strconv.QuoteToASCII(truncateName(req.schema)))
		}
		return nil
	}
	canonical, ok := publicReadSchema(req.schema)
	if !ok {
		return fmt.Errorf("%w (%s)", ErrReadNotAuthorized, strconv.QuoteToASCII(truncateName(req.schema)))
	}
	req.schema = canonical
	return nil
}

// publishedLaneOps are the flatsql-sync ops that read only the node's
// dataset publications (see FlatSQLSyncHandler.authorize).
var publishedLaneOps = map[string]bool{
	syncwire.OpListPublished:  true,
	syncwire.OpReadShard:      true,
	syncwire.OpReadIndex:      true,
	syncwire.OpReadShardBatch: true,
}

func truncateName(name string) string {
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

// HandleStream answers one request of op.
func (h *FlatSQLSyncHandler) HandleStream(op string, s network.Stream) {
	defer s.Close()
	defer recoverStream("flatsql-sync/"+op, s)
	_ = s.SetReadDeadline(time.Now().Add(DefaultReadTimeout))
	if op == syncwire.OpListPublished {
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		if retry := h.publicationReads.Allow(streamPeer(s)); retry > 0 {
			_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusBusy, "rate_limited", "Publication read limit reached", 429, retry))
			return
		}
	}

	req, err := decodeSyncRequest(op, s)
	if err != nil {
		log.Warnf("flatsql-sync: refused %s request from %s: %s", op, streamPeer(s).ShortString(), boundedRefusalReason(err.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(err))
		return
	}
	peerID := streamPeer(s)
	if err := h.authorize(peerID, &req); err != nil {
		log.Warnf("flatsql-sync: refused %s from %s: %s", op, peerID.ShortString(), boundedRefusalReason(err.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(err))
		return
	}

	var requestErr error
	switch op {
	case syncwire.OpReadChunk:
		// The read runs under the stream's context: a peer that resets
		// the stream ends the store read it asked for.
		began := time.Now()
		watch, stop := watchStream(context.Background(), s)
		requestErr = h.handleReadChunk(watch.ctx, watchedStream{streamWatch: watch, w: s}, peerID, req)
		reset := errors.Is(context.Cause(watch.ctx), errStreamReset)
		stop()
		if reset {
			lane := req.providerID + "/" + req.sourceName
			if req.batchID != "" {
				lane += " batch " + req.batchID
			}
			log.Infof("flatsql-sync: %s %s %s from %s: the peer reset the stream after %s; its store read stopped (%v)", op, req.schema,
				lane, peerID.ShortString(), time.Since(began).Round(time.Millisecond), requestErr)
			return
		}
	case syncwire.OpOpenManifest:
		requestErr = h.handleOpenManifest(s, req)
	case syncwire.OpListPublished:
		requestErr = h.handleListPublished(s, req)
	case syncwire.OpReadShard, syncwire.OpReadIndex:
		requestErr = h.handleReadPublishedBytes(s, req)
	case syncwire.OpReadShardBatch:
		requestErr = h.handleReadShardBatch(s, req)
	case syncwire.OpWireSpeedProbe:
		requestErr = h.handleWireSpeedProbe(s, peerID, req)
	case syncwire.OpListDatastores:
		requestErr = h.handleListDatastores(s)
	default:
		requestErr = fmt.Errorf("unsupported sync op %q", truncateName(op))
	}
	if requestErr != nil {
		log.Debugf("flatsql-sync: %s from %s failed: %s", op, peerID.ShortString(), boundedRefusalReason(requestErr.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(requestErr))
	}
}

// decodeSyncRequest reads the one $QRP request and its FILTERS. A filter the
// op does not apply is refused, never ignored.
func decodeSyncRequest(op string, r io.Reader) (syncRequest, error) {
	q, err := syncwire.ReadRequest(r)
	if err != nil {
		return syncRequest{}, err
	}
	req := syncRequest{
		op:         op,
		q:          q,
		schema:     syncwire.NormalizeSchema(string(q.SCHEMA_NAME())),
		providerID: strings.TrimSpace(string(q.PROVIDER_ID())),
		sourceName: strings.TrimSpace(string(q.SOURCE_NAME())),
		batchID:    strings.TrimSpace(string(q.BATCH_ID())),
		cid:        strings.TrimSpace(string(q.CID())),
		cursor:     strings.TrimSpace(string(q.CURSOR())),
		limit:      int(q.LIMIT()),
		maxBytes:   q.MAX_BYTES(),
	}
	for _, field := range syncwire.FilterFields(q) {
		switch field {
		case syncwire.FilterQueryProfile, syncwire.FilterDatastoreKey:
		case syncwire.FilterShardCID:
			if op != syncwire.OpReadShardBatch {
				return syncRequest{}, fmt.Errorf("%s takes no %s filter", op, field)
			}
		case syncwire.FilterProjection, syncwire.FilterCID:
			if op != syncwire.OpReadChunk {
				return syncRequest{}, fmt.Errorf("%s takes no %s filter", op, field)
			}
		default:
			return syncRequest{}, fmt.Errorf("unsupported filter %q", truncateName(field))
		}
	}
	req.queryProfile, _ = syncwire.Filter(q, syncwire.FilterQueryProfile, syncwire.QRPFilterEq)
	req.queryProfile = strings.TrimSpace(req.queryProfile)
	req.datastoreKey, _ = syncwire.Filter(q, syncwire.FilterDatastoreKey, syncwire.QRPFilterEq)
	req.datastoreKey = strings.TrimSpace(req.datastoreKey)
	if cids, ok := syncwire.Filter(q, syncwire.FilterShardCID, syncwire.QRPFilterIn); ok {
		req.shardCIDs = cleanPublishedShardCIDs(strings.Split(cids, ","))
	}
	return req, nil
}

// publicationQueryProfile is the request's query profile, defaulting to the
// dataset-publication profile.
func (req syncRequest) publicationQueryProfile() string {
	if req.queryProfile != "" {
		return req.queryProfile
	}
	return storage.DatasetPublicationQueryProfile
}

func (req syncRequest) publicationQuery() storage.DatasetShardPublicationQuery {
	return storage.DatasetShardPublicationQuery{
		SchemaName:   req.schema,
		ProviderID:   req.providerID,
		SourceName:   req.sourceName,
		BatchID:      req.batchID,
		QueryProfile: req.publicationQueryProfile(),
	}
}

func (h *FlatSQLSyncHandler) handleReadChunk(ctx context.Context, w io.Writer, peerID peer.ID, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	query, err := syncwire.ScanQuery(req.q)
	if err != nil {
		return err
	}
	query.Schema = req.schema // the name authorize checked
	query.QueryProfile = req.queryProfile
	projection, listed := syncwire.Filter(req.q, syncwire.FilterProjection, syncwire.QRPFilterEq)
	named, fetched := syncwire.Filter(req.q, syncwire.FilterCID, syncwire.QRPFilterIn)
	if listed && projection == syncwire.ProjectionStatement {
		if req.q.FOLLOW() || fetched || req.batchID != "" || req.providerID == "" || req.sourceName == "" || req.cursor != "" {
			return errors.New("a lane's statement is one unfollowed read-chunk naming the lane and no batch, with PROJECTION STATEMENT alone")
		}
		began := time.Now()
		var frame []byte
		err := h.boundedWork(ctx, peerID, false, followStatementBound, func(read context.Context) error {
			if followWorkFault != nil {
				if err := followWorkFault(read, query.SourceName); err != nil {
					return err
				}
			}
			var err error
			frame, err = laneStatementPage(activeStore, query)
			return err
		})
		if err == nil {
			_, err = w.Write(frame)
		}
		logFollowerRead("statement", peerID, req, began, err)
		return err
	}
	if listed || fetched {
		if req.q.FOLLOW() || listed == fetched || req.batchID == "" || req.providerID == "" || req.sourceName == "" {
			return errors.New("a batch's list or its records by CID is one unfollowed read-chunk naming the lane and the batch, with one of PROJECTION and CID")
		}
		if listed && projection != syncwire.ProjectionCID {
			return fmt.Errorf("unsupported PROJECTION %q", truncateName(projection))
		}
		began := time.Now()
		what := "records by CID"
		if listed {
			what = "batch list page"
		}
		var frame []byte
		var records []*storage.Record
		var asked int
		// A list page and a fetch are pages: under the stream's context
		// and the page bound.
		err := h.boundedWork(ctx, peerID, true, followPageBound, func(read context.Context) error {
			var err error
			if listed {
				frame, err = batchCIDPage(read, activeStore, req)
			} else {
				records, asked, err = batchRecords(read, activeStore, req, named)
			}
			return err
		})
		if err == nil {
			if listed {
				_, err = w.Write(frame)
			} else {
				err = syncwire.WriteFollowPage(w, activeStore, &datasync.Page{Schema: req.schema, Limit: asked}, records, syncwire.FollowStatement{})
			}
		}
		logFollowerRead(what, peerID, req, began, err)
		return err
	}
	if req.q.FOLLOW() {
		rw, ok := w.(io.ReadWriter)
		if !ok {
			return errors.New("a followed read needs a two-way stream")
		}
		logFollowerRead("followed stream opened", peerID, req, time.Now(), nil)
		return h.followReadChunk(ctx, rw, activeStore, query, peerID, req)
	}
	var page *datasync.Page
	var records []*storage.Record
	if err := h.boundedWork(ctx, peerID, true, followPageBound, func(read context.Context) error {
		var err error
		page, records, err = datasync.ScanPageContext(read, activeStore, query, datasync.MaxSyncChunkLimit)
		return err
	}); err != nil {
		return err
	}
	return syncwire.WriteScanPage(w, activeStore, page, records)
}

// logFollowerRead logs one read a follower asked of a lane (its statement, a
// page of a batch's list, records by CID, a followed stream), with what it
// took: these are the reads a follower costs the producer.
func logFollowerRead(what string, peerID peer.ID, req syncRequest, began time.Time, err error) {
	lane := fmt.Sprintf("%s %s/%s", req.schema, truncateName(req.providerID), truncateName(req.sourceName))
	if req.batchID != "" {
		lane += " batch " + truncateName(req.batchID)
	}
	if err != nil {
		log.Infof("flatsql-sync: %s of %s for %s failed after %s: %s", what, lane, peerID.ShortString(), time.Since(began).Round(time.Millisecond),
			boundedRefusalReason(err.Error()))
		return
	}
	log.Infof("flatsql-sync: %s of %s for %s in %s", what, lane, peerID.ShortString(), time.Since(began).Round(time.Millisecond))
}

// followQuiet is how long a followed read waits for new records before it
// answers a grant with an empty page. It waits on the store's write signal
// for the standard, not by reading again; a store that does not signal is
// read again once a quiet interval.
var (
	followQuiet = 10 * time.Second
	// followGrantWait bounds the wait for a trusted subscriber's next
	// grant; followAnonGrantWait an anonymous one's.
	followGrantWait     = 30 * time.Minute
	followAnonGrantWait = 2 * time.Minute
	// followAnonLifetime ends an anonymous subscriber's stream; it opens
	// another at its resume cursor.
	followAnonLifetime = 15 * time.Minute
)

// Followed streams this node serves at once: per trusted peer, per peer
// without a trust relation (anonymous), for all anonymous peers together and
// in total. A stream past any is refused busy, so throwaway peer IDs hold at
// most followAnonStreamsTotal of the followStreamsTotal slots, and one
// anonymous peer at most an eighth of them: eight peers fill the anonymous
// pool, and a subscriber the producer trusts is served outside it (16
// streams, one per lane). A subscriber follows one stream per lane: a node
// following more than eight lanes of one producer is listed by that producer
// as trusted.
const (
	followStreamsPerPeer     = 16
	followAnonStreamsPerPeer = 8
	followAnonStreamsTotal   = 64
	followStreamsTotal       = 256
)

// errFollowLifetime ends an anonymous followed stream at its lifetime.
var errFollowLifetime = fmt.Errorf("%w: an anonymous followed stream's lifetime is over; open another at the resume cursor", syncwire.ErrBusy)

// batchCIDPage is one page of a batch's list of CIDs. CURSOR and
// NEXT_CURSOR are the sequence after which the page reads.
func batchCIDPage(ctx context.Context, store *storage.FlatSQLStore, req syncRequest) ([]byte, error) {
	var after int64
	if req.cursor != "" {
		var err error
		if after, err = strconv.ParseInt(req.cursor, 10, 64); err != nil || after < 0 {
			return nil, fmt.Errorf("a batch list CURSOR %q is not a node-issued position", truncateName(req.cursor))
		}
	}
	cids, last, more, err := store.LaneBatchCIDPage(ctx, req.schema, req.providerID, req.sourceName, req.batchID, after, syncwire.FollowCIDPageLimit)
	if err != nil {
		return nil, err
	}
	next := ""
	if more {
		next = strconv.FormatInt(last, 10)
	}
	return syncwire.CIDPage(syncwire.QRPFields{SchemaName: req.schema, ProviderID: req.providerID, SourceName: req.sourceName,
		BatchID: req.batchID, Cursor: req.cursor, NextCursor: next, Limit: syncwire.FollowCIDPageLimit, GeneratedAtMs: time.Now().UnixMilli()}, cids), nil
}

// laneStatementPage is a lane's statement (syncwire.LaneStatement): its
// current batch and that batch's records and bytes, the lane's records and
// bytes, and a resume cursor at the type's newest sequence for the followed
// read of the lane (the query a subscriber's followed request names: the
// lane, no batch, no profile), so a follow opened there sends only what is
// stored after it.
//
// On a format 5 store every figure comes from maintained counters (the type
// summary and the lane counters), never from reading the lane's records, so
// a statement costs the lane's batches, not its records: the batch's and
// the lane's records and bytes are the sums of their tag instances' counters
// (storage.LaneTotals; a record in two of the lane's batches counts in each).
func laneStatementPage(store *storage.FlatSQLStore, query datasync.QueryRequest) ([]byte, error) {
	follow := datasync.QueryRequest{Schema: query.Schema, ProviderID: query.ProviderID, SourceName: query.SourceName}
	boundary, err := store.RecordSequenceHead(datasync.FilterFromRequest(follow, 0, 0))
	if err != nil {
		return nil, err
	}
	stmt := laneStatement(store, follow)
	st := syncwire.LaneStatement{FollowStatement: stmt, ResumeCursor: datasync.ResumeCursorAt(store, follow, boundary)}
	totals := func(batch string) (int64, int64, error) {
		records, bytes, err := store.LaneTotals(follow.Schema, follow.ProviderID, follow.SourceName, batch)
		if !errors.Is(err, storage.ErrFollowLaneUnsupported) {
			return records, bytes, err
		}
		q := follow
		q.BatchID = batch
		records, head, err := store.RawRecordSnapshot(datasync.FilterFromRequest(q, 0, 0))
		return records, head.TotalBytes, err
	}
	if stmt.BatchID != "" {
		if st.Records, st.BatchBytes, err = totals(stmt.BatchID); err != nil {
			return nil, err
		}
	}
	if st.LaneRecords, st.LaneBytes, err = totals(""); err != nil {
		return nil, err
	}
	return syncwire.StatementPage(syncwire.QRPFields{SchemaName: follow.Schema, ProviderID: follow.ProviderID, SourceName: follow.SourceName,
		GeneratedAtMs: time.Now().UnixMilli()}, st), nil
}

// batchRecords is the records of a batch named by CID for one followed page
// (no statement), and the CIDs named; records past the page's limits, and
// CIDs the batch does not hold, are left out.
func batchRecords(ctx context.Context, store *storage.FlatSQLStore, req syncRequest, named string) ([]*storage.Record, int, error) {
	var cids []string
	for _, c := range strings.Split(named, ",") {
		if c = strings.TrimSpace(c); c != "" {
			cids = append(cids, c)
		}
	}
	if len(cids) == 0 || len(cids) > syncwire.FollowFetchMaxCIDs {
		return nil, 0, fmt.Errorf("a fetch by CID names 1 to %d CIDs, not %d", syncwire.FollowFetchMaxCIDs, len(cids))
	}
	records, err := store.LaneRecordsByCID(ctx, req.schema, req.providerID, req.sourceName, req.batchID, cids)
	if err != nil {
		return nil, 0, err
	}
	return records[:syncwire.FollowPageCut(records, syncwire.FollowMaxRecords)], len(cids), nil
}

type deadlineSetter interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// followReadChunk serves a followed read-chunk (syncwire/follow.go): one
// page for the opening request and one for each grant. Every page ends with
// a resume cursor at its last record (or, once everything stored has been
// sent, at the scan's boundary), and the next page reads from it: positions
// are tokens of the store's scan generation, so after a rebuild the
// subscriber's saved position expires and it reads the lane again instead of
// skipping. The peer's read permission is checked again before each page:
// once it is gone the stream ends.
func (h *FlatSQLSyncHandler) followReadChunk(ctx context.Context, s io.ReadWriter, store *storage.FlatSQLStore, query datasync.QueryRequest, peerID peer.ID, req syncRequest) error {
	trusted := authorizedPeer(h.followTrust, peerID) || authorizedPeer(h.readAuthorizer, peerID)
	if !h.followSlots.acquire(peerID, trusted) {
		return errFollowBusy
	}
	defer h.followSlots.release(peerID, trusted)
	if query.Cursor == "" && query.ResumeCursor == "" {
		// A follow from the lane's beginning reads forward from sequence 0
		// as a resume does: a followed page carries no count (TOTAL_COUNT
		// is the stated batch's records), and counting the lane reads
		// every record of it.
		query.ResumeCursor = datasync.ResumeCursorAt(store, query, 0)
	}
	grantWait, ends := followGrantWait, time.Time{}
	if !trusted {
		grantWait, ends = followAnonGrantWait, time.Now().Add(followAnonLifetime)
	}
	deadlines, _ := s.(deadlineSetter)
	if query.Limit <= 0 || query.Limit > syncwire.FollowMaxRecords {
		query.Limit = syncwire.FollowMaxRecords
	}
	lane := followLaneKey(query)
	h.follows.open(lane)
	defer h.follows.close(lane)
	caughtUp := false
	for first := true; ; first = false {
		if !first {
			if !ends.IsZero() && time.Now().After(ends) {
				return errFollowLifetime
			}
			if deadlines != nil {
				_ = deadlines.SetReadDeadline(time.Now().Add(grantWait))
			}
			if _, err := syncwire.ReadRequest(s); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return nil
				}
				return fmt.Errorf("%w: %v", syncwire.ErrFollowClosed, err)
			}
			again := req
			if err := h.authorize(peerID, &again); err != nil {
				return err
			}
		}
		var page *datasync.Page
		var records []*storage.Record
		if err := h.followPage(ctx, peerID, store, query, first, caughtUp, &page, &records); err != nil {
			return err
		}
		cut := syncwire.FollowPageCut(records, query.Limit)
		more := page.NextCursor != "" || cut < len(records)
		records = records[:cut]
		if more {
			page.ResumeCursor = datasync.ResumeCursorAt(store, query, records[len(records)-1].RowID)
		}
		page.Cursor, page.NextCursor = "", ""
		if deadlines != nil {
			_ = deadlines.SetWriteDeadline(time.Now().Add(DefaultReadTimeout))
		}
		if err := syncwire.WriteFollowPage(s, store, page, records, laneStatement(store, query)); err != nil {
			return err
		}
		var recordBytes int64
		for _, r := range records {
			recordBytes += int64(syncwire.FramePrefixLength + len(r.Data))
		}
		h.follows.served(lane, recordBytes, len(records))
		query.TotalCount, query.SnapshotID, query.Head = 0, "", ""
		query.Cursor, query.ResumeCursor = "", page.ResumeCursor
		caughtUp = !more
	}
}

// followPage reads a followed stream's next page into page and records:
// under a work slot (waited for within the bound), with the stream's
// context and within followPageBound. A
// stream caught up waits for new records for followQuiet, without a slot,
// and then answers an empty page.
func (h *FlatSQLSyncHandler) followPage(stream context.Context, peerID peer.ID, store *storage.FlatSQLStore, query datasync.QueryRequest, first, caughtUp bool,
	page **datasync.Page, records *[]*storage.Record) error {
	bound := time.Now().Add(followPageBound)
	quiet := time.NewTimer(followQuiet)
	defer quiet.Stop()
	for {
		written := store.WriteSignal(query.Schema)
		if err := h.boundedWork(stream, peerID, true, time.Until(bound), func(read context.Context) error {
			var err error
			*page, *records, err = datasync.ScanPageContext(read, store, query, syncwire.FollowMaxRecords)
			return err
		}); err != nil {
			return err
		}
		if !caughtUp || len(*records) > 0 || (*page).NextCursor != "" {
			return nil
		}
		select {
		case <-written:
		case <-quiet.C:
			return nil
		case <-stream.Done():
			return context.Cause(stream)
		}
	}
}

// laneStatement is this node's statement of a followed lane: its current
// batch and that batch's records (none for a lane not named in full, or a
// store that does not state batches).
func laneStatement(store *storage.FlatSQLStore, query datasync.QueryRequest) syncwire.FollowStatement {
	if query.ProviderID == "" || query.SourceName == "" {
		return syncwire.FollowStatement{}
	}
	if _, retracted, err := store.DatasetPublicationLaneRetractedAt(storage.DatasetPublicationLane{SchemaName: syncwire.NormalizeSchema(query.Schema), ProviderID: query.ProviderID, SourceName: query.SourceName}); err != nil {
		log.Debugf("flatsql-sync: retraction of %s %s/%s unread: %v", query.Schema, query.ProviderID, query.SourceName, err)
	} else if retracted {
		return syncwire.FollowStatement{Retracted: true}
	}
	batch, records, err := store.LaneCurrentBatch(query.Schema, query.ProviderID, query.SourceName)
	if err != nil {
		if !errors.Is(err, storage.ErrFollowLaneUnsupported) {
			log.Debugf("flatsql-sync: no statement of %s %s/%s: %v", query.Schema, query.ProviderID, query.SourceName, err)
		}
		return syncwire.FollowStatement{}
	}
	return syncwire.FollowStatement{BatchID: batch, Records: records}
}

// errFollowBusy refuses a followed read past the stream limits.
var errFollowBusy = fmt.Errorf("%w: followed stream limit reached", syncwire.ErrBusy)

// followSlots counts the followed streams being served, per peer, for
// anonymous peers and in total.
type followSlots struct {
	mu     sync.Mutex
	total  int
	anon   int
	byPeer map[peer.ID]int
}

func newFollowSlots() *followSlots { return &followSlots{byPeer: map[peer.ID]int{}} }

func (f *followSlots) acquire(p peer.ID, trusted bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.total >= followStreamsTotal || f.byPeer[p] >= followStreamsPerPeer {
		return false
	}
	if !trusted && (f.anon >= followAnonStreamsTotal || f.byPeer[p] >= followAnonStreamsPerPeer) {
		return false
	}
	f.total++
	f.byPeer[p]++
	if !trusted {
		f.anon++
	}
	return true
}

func (f *followSlots) release(p peer.ID, trusted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total--
	if !trusted {
		f.anon--
	}
	if f.byPeer[p]--; f.byPeer[p] <= 0 {
		delete(f.byPeer, p)
	}
}

// FollowStats is what this node has served to followers, per lane.
func (h *FlatSQLSyncHandler) FollowStats() map[FollowLane]FollowStat {
	return h.follows.Snapshot()
}

func (h *FlatSQLSyncHandler) handleOpenManifest(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := datasync.OpenManifest(activeStore, datasync.QueryRequest{
		Schema:         req.schema,
		ProviderID:     req.providerID,
		SourceName:     req.sourceName,
		BatchID:        req.batchID,
		ProducerPeerID: strings.TrimSpace(string(req.q.PRODUCER_PEER_ID())),
		PeerID:         strings.TrimSpace(string(req.q.ORIGIN_ID())),
		Search:         strings.TrimSpace(string(req.q.SEARCH())),
		QueryProfile:   req.queryProfile,
		Limit:          req.limit,
	}, datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	segs := make([]syncwire.Segment, 0, len(m.Segments)+len(m.ArtifactBundles))
	for _, seg := range m.Segments {
		segs = append(segs, syncwire.Segment{
			SchemaName: m.Schema, ProviderID: m.ProviderID, SourceName: m.SourceName, BatchID: m.BatchID,
			QueryProfile: m.QueryProfile, Role: syncwire.RoleSegment,
			SegmentIndex: int64(seg.Index), Cursor: seg.Cursor, NextCursor: seg.NextCursor,
			RecordCount: int64(seg.RowCount), ByteCount: seg.ByteCount, ChunkHash: seg.ChunkHash,
			ShardCID: seg.CID, IndexCID: seg.IndexCID, ManifestCID: seg.ManifestCID, PNMCID: seg.PNMCID,
			ShardSHA256: seg.ShardSHA256, IndexSHA256: seg.IndexSHA256, QuerySHA256: seg.QuerySHA256,
			FeedSequence: seg.FeedSequence, PreviousHead: seg.PreviousHead, FeedHead: seg.FeedHead,
		})
	}
	for _, bundle := range m.ArtifactBundles {
		// A bundle row: SHARD_CID = the bundle CID, SHARD_SHA256 = its
		// bytes' hash, SEGMENT_INDEX = first segment covered, LIMIT =
		// segments covered.
		segs = append(segs, syncwire.Segment{
			SchemaName: m.Schema, ProviderID: m.ProviderID, SourceName: m.SourceName, BatchID: m.BatchID,
			QueryProfile: m.QueryProfile, Role: bundle.Role,
			SegmentIndex: int64(bundle.SegmentStart), Limit: int64(bundle.SegmentCount),
			ShardCID: bundle.CID, ByteCount: bundle.ByteCount, ShardSHA256: bundle.SHA256,
		})
	}
	_, err = w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:     m.Schema,
		ProviderID:     m.ProviderID,
		SourceName:     m.SourceName,
		BatchID:        m.BatchID,
		ProducerPeerID: m.ProducerPeerID,
		TotalCount:     uint64(max(m.TotalCount, 0)),
		Etag:           m.ManifestID,
		Cursor:         m.SnapshotID,
		GeneratedAtMs:  time.Now().UnixMilli(),
	}, segs))
	return err
}

func (h *FlatSQLSyncHandler) handleListPublished(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	if req.schema == "" {
		return fmt.Errorf("published shard schema is required")
	}
	offset := 0
	if req.cursor != "" {
		if offset, err = strconv.Atoi(req.cursor); err != nil || offset < 0 {
			return fmt.Errorf("list-published CURSOR %q is not a node-issued offset", truncateName(req.cursor))
		}
	}
	limit := req.limit
	if limit <= 0 || limit > publicationListRows {
		limit = publicationListRows
	}
	selected, total, err := activeStore.ListDatasetShardPublicationsPage(req.publicationQuery(), offset, limit)
	if err != nil {
		return err
	}
	offset = min(offset, total)
	completion, err := activeStore.DatasetPublicationCompletions(selected)
	if err != nil {
		return err
	}
	segs := make([]syncwire.Segment, len(selected))
	for i, pub := range selected {
		segs[i] = syncwire.SegmentFromPublication(pub)
		segs[i].Availability, segs[i].AvailabilityReason = activeStore.DatasetPublicationAvailability(pub)
		segs[i].AvailabilityCheckedAtMs = time.Now().UnixMilli()
		segs[i].BatchCompletion = completion[i].State
		segs[i].BatchRevision = completion[i].Revision
		segs[i].BatchTotalRecords = completion[i].TotalRecords
	}
	for {
		next := ""
		if offset+len(segs) < total {
			next = strconv.Itoa(offset + len(segs))
		}
		page := syncwire.SegmentPage(syncwire.QRPFields{
			SchemaName: req.schema, ProviderID: req.providerID, SourceName: req.sourceName, BatchID: req.batchID,
			Limit: uint32(len(segs)), Cursor: strconv.Itoa(offset), NextCursor: next,
			TotalCount: uint64(total), GeneratedAtMs: time.Now().UnixMilli(),
		}, segs)
		if len(page) <= publicationListBytes {
			_, err = w.Write(page)
			return err
		}
		if len(segs) <= 1 {
			return fmt.Errorf("publication metadata exceeds page byte limit")
		}
		segs = segs[:len(segs)/2]
	}
}

// publishedAsset is one publication's shard or index file, or, when the file
// is gone and the node's IPFS repository holds the content in full, that
// content (cid set, path empty).
type publishedAsset struct {
	segment syncwire.Segment
	path    string
	cid     string
	size    int64
	store   *storage.FlatSQLStore
}

// open reads the asset from its file or from the repository.
func (a publishedAsset) open() (io.ReadSeekCloser, error) {
	if a.path != "" {
		return os.Open(a.path)
	}
	file, _, err := a.store.OpenPublicationContent(a.cid)
	return file, err
}

// heldAsset is the asset of cid served from the repository when its file at
// path is missing; ok is false when the repository does not hold it in full
// (or, with want > 0, at another size).
func heldAsset(store *storage.FlatSQLStore, cid string, want int64) (int64, bool) {
	size, held := store.PublicationContentHeld(cid)
	if !held || (want > 0 && size != want) {
		return 0, false
	}
	return size, true
}

// publishedShard resolves a shard CID of the request's lane.
func publishedShard(store *storage.FlatSQLStore, req syncRequest, cid string) (publishedAsset, error) {
	if req.schema == "" {
		return publishedAsset{}, fmt.Errorf("published shard schema is required")
	}
	pub, found, err := store.FindDatasetShardPublicationByCID(req.publicationQuery(), cid)
	if err != nil {
		return publishedAsset{}, err
	}
	if !found {
		return publishedAsset{}, fmt.Errorf("published shard was not found for %s", cid)
	}
	path, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		return publishedAsset{}, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		if size, held := heldAsset(store, cid, pub.ByteCount); held {
			seg := syncwire.SegmentFromPublication(pub)
			seg.Role, seg.ByteCount = syncwire.RoleShard, size
			return publishedAsset{segment: seg, cid: cid, size: size, store: store}, nil
		}
	}
	if err != nil {
		return publishedAsset{}, fmt.Errorf("stat published shard %s: %w", cid, err)
	}
	if pub.ByteCount > 0 && info.Size() != pub.ByteCount {
		return publishedAsset{}, fmt.Errorf("published shard %s size = %d, want %d", cid, info.Size(), pub.ByteCount)
	}
	seg := syncwire.SegmentFromPublication(pub)
	seg.Role, seg.ByteCount = syncwire.RoleShard, info.Size()
	return publishedAsset{segment: seg, path: path, size: info.Size(), store: store}, nil
}

// publishedIndex resolves an index CID of the request's lane.
func publishedIndex(store *storage.FlatSQLStore, req syncRequest, cid string) (publishedAsset, error) {
	if req.schema == "" {
		return publishedAsset{}, fmt.Errorf("published index schema is required")
	}
	publications, err := store.ListDatasetShardPublications(req.publicationQuery())
	if err != nil {
		return publishedAsset{}, err
	}
	for _, pub := range publications {
		if pub.IndexCID != cid {
			continue
		}
		path, err := store.DatasetPublicationIndexPath(pub)
		if err != nil {
			return publishedAsset{}, err
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			if size, held := heldAsset(store, cid, 0); held {
				seg := syncwire.SegmentFromPublication(pub)
				seg.Role, seg.ByteCount = syncwire.RoleIndex, size
				return publishedAsset{segment: seg, cid: cid, size: size, store: store}, nil
			}
		}
		if err != nil {
			return publishedAsset{}, fmt.Errorf("stat published index %s: %w", cid, err)
		}
		seg := syncwire.SegmentFromPublication(pub)
		seg.Role, seg.ByteCount = syncwire.RoleIndex, info.Size()
		return publishedAsset{segment: seg, path: path, size: info.Size(), store: store}, nil
	}
	return publishedAsset{}, fmt.Errorf("published index was not found for %s", cid)
}

// handleReadPublishedBytes serves read-shard and read-index: one Page with
// one row (BYTE_OFFSET, BYTE_LENGTH, BYTE_COUNT = whole file), then
// BYTE_LENGTH raw bytes from BYTE_OFFSET.
func (h *FlatSQLSyncHandler) handleReadPublishedBytes(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	if req.cid == "" {
		return fmt.Errorf("%s CID is required", req.op)
	}
	resolve := publishedShard
	if req.op == syncwire.OpReadIndex {
		resolve = publishedIndex
	}
	asset, err := resolve(activeStore, req, req.cid)
	if err != nil {
		return err
	}
	var requestedOffset int64
	if req.cursor != "" {
		if requestedOffset, err = strconv.ParseInt(req.cursor, 10, 64); err != nil {
			return fmt.Errorf("%s CURSOR %q is not a byte offset", req.op, truncateName(req.cursor))
		}
	}
	if req.maxBytes > uint64(asset.size) {
		return fmt.Errorf("%s %s byte range: length %d exceeds size %d", req.op, req.cid, req.maxBytes, asset.size)
	}
	byteOffset, byteLength, err := normalizePublishedShardByteRange(asset.size, requestedOffset, int64(req.maxBytes))
	if err != nil {
		return fmt.Errorf("%s %s byte range: %w", req.op, req.cid, err)
	}
	asset.segment.ByteOffset, asset.segment.ByteLength = byteOffset, byteLength

	file, err := asset.open()
	if err != nil {
		return fmt.Errorf("open %s %s: %w", asset.segment.Role, req.cid, err)
	}
	defer file.Close()
	if byteOffset > 0 {
		if _, err := file.Seek(byteOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s %s to %d: %w", asset.segment.Role, req.cid, byteOffset, err)
		}
	}
	if _, err := w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:    asset.segment.SchemaName,
		CID:           req.cid,
		GeneratedAtMs: time.Now().UnixMilli(),
	}, []syncwire.Segment{asset.segment})); err != nil {
		return err
	}
	_, err = io.CopyBuffer(w, io.LimitReader(file, byteLength), make([]byte, publishedShardCopyBufferBytes))
	return err
}

// handleReadShardBatch serves read-shard-batch: one Page with one row per
// shard in request order, then each shard's BYTE_COUNT bytes concatenated.
func (h *FlatSQLSyncHandler) handleReadShardBatch(w io.Writer, req syncRequest) error {
	if len(req.shardCIDs) > datasync.MaxSyncChunkLimit {
		return fmt.Errorf("published shard batch limit is %d", datasync.MaxSyncChunkLimit)
	}
	if len(req.shardCIDs) == 0 {
		return fmt.Errorf("read-shard-batch takes FILTERS [{SHARD_CID In \"a,b,c\"}]")
	}
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	assets := make([]publishedAsset, 0, len(req.shardCIDs))
	segs := make([]syncwire.Segment, 0, len(req.shardCIDs))
	var total int64
	for _, cid := range req.shardCIDs {
		asset, err := publishedShard(activeStore, req, cid)
		if err != nil {
			return err
		}
		asset.segment.ByteLength = asset.size
		assets = append(assets, asset)
		segs = append(segs, asset.segment)
		total += asset.size
	}
	if _, err := w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:    req.schema,
		ProviderID:    req.providerID,
		SourceName:    req.sourceName,
		BatchID:       req.batchID,
		MaxBytes:      uint64(total),
		GeneratedAtMs: time.Now().UnixMilli(),
	}, segs)); err != nil {
		return err
	}
	buffer := make([]byte, publishedShardCopyBufferBytes)
	for _, asset := range assets {
		file, err := asset.open()
		if err != nil {
			return fmt.Errorf("open published shard %s: %w", asset.segment.ShardCID, err)
		}
		_, copyErr := io.CopyBuffer(w, io.LimitReader(file, asset.size), buffer)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func cleanPublishedShardCIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		cid := strings.TrimSpace(value)
		if cid == "" {
			continue
		}
		if _, exists := seen[cid]; exists {
			continue
		}
		seen[cid] = struct{}{}
		out = append(out, cid)
	}
	return out
}

func normalizePublishedShardByteRange(totalBytes int64, requestedOffset int64, requestedLength int64) (int64, int64, error) {
	if totalBytes < 0 {
		return 0, 0, fmt.Errorf("total byte count is negative")
	}
	if requestedOffset < 0 {
		return 0, 0, fmt.Errorf("offset must be non-negative")
	}
	if requestedLength < 0 {
		return 0, 0, fmt.Errorf("length must be non-negative")
	}
	if requestedOffset > totalBytes {
		return 0, 0, fmt.Errorf("offset %d exceeds shard size %d", requestedOffset, totalBytes)
	}
	length := requestedLength
	if length == 0 {
		length = totalBytes - requestedOffset
	}
	if requestedOffset+length > totalBytes {
		return 0, 0, fmt.Errorf("range %d+%d exceeds shard size %d", requestedOffset, length, totalBytes)
	}
	return requestedOffset, length, nil
}

// DatastoreColumns is the COLUMNS of a list-datastores page.
var DatastoreColumns = []syncwire.QRPColumnField{
	{Name: "DATASTORE_KEY", Type: "string"},
	{Name: "SCHEMA_NAME", Type: "string"},
	{Name: "PROVIDER_ID", Type: "string"},
	{Name: "SOURCE_NAME", Type: "string"},
	{Name: "BATCH_ID", Type: "string"},
	{Name: "QUERY_PROFILE", Type: "string"},
	{Name: "PRODUCER_PEER_ID", Type: "string"},
	{Name: "PRODUCER_PUBLIC_KEY", Type: "string"},
	{Name: "UPDATED_AT_MS", Type: "int64"},
}

func (h *FlatSQLSyncHandler) handleListDatastores(w io.Writer) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	entries, err := h.store.ListDatastoreIdentities()
	if err != nil {
		return err
	}
	rows := make([][]syncwire.QRPCell, 0, len(entries))
	for _, entry := range entries {
		id := entry.Identity
		updated := entry.UpdatedAt * 1000 // registry stores Unix seconds
		rows = append(rows, []syncwire.QRPCell{
			syncwire.TextCell(entry.Key), syncwire.TextCell(id.SchemaName), syncwire.TextCell(id.ProviderID),
			syncwire.TextCell(id.SourceName), syncwire.TextCell(id.BatchHead), syncwire.TextCell(id.QueryProfile),
			syncwire.TextCell(id.SourcePeerID), syncwire.TextCell(id.SourcePublicKey), syncwire.IntCell(updated),
		})
	}
	_, err = w.Write(syncwire.BuildQRP(syncwire.QRPFields{
		Kind:          syncwire.QRPKindPage,
		Status:        syncwire.QRPStatusOk,
		TotalCount:    uint64(len(rows)),
		Columns:       DatastoreColumns,
		Rows:          rows,
		GeneratedAtMs: time.Now().UnixMilli(),
	}))
	return err
}

func (h *FlatSQLSyncHandler) storeForRequest(req syncRequest) (*storage.FlatSQLStore, func(), error) {
	if h.store == nil {
		return nil, func() {}, fmt.Errorf("FlatSQL store is unavailable")
	}
	if req.datastoreKey == "" {
		return h.store, func() {}, nil
	}
	store, err := h.store.OpenRegisteredDatastore(req.datastoreKey)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() { _ = store.Close() }, nil
}

func (h *FlatSQLSyncHandler) handleWireSpeedProbe(w io.Writer, peerID peer.ID, req syncRequest) error {
	probeBytes := normalizeWireSpeedProbeBytes(int64(min(req.maxBytes, uint64(maxWireSpeedProbeBytes))))
	if h.probes == nil {
		return errors.New("wire-speed-probe: probe budget unavailable")
	}
	release, err := h.probes.acquire(peerID, probeBytes)
	if err != nil {
		return err
	}
	defer release()
	// A reader that stops consuming must not hold a node-wide probe slot:
	// every write (the header, then each chunk) must make progress within
	// DefaultHandlerTimeout. A slow but live reader keeps going.
	if err := extendProbeWriteDeadline(w); err != nil {
		return err
	}
	if _, err := w.Write(syncwire.BuildQRP(syncwire.QRPFields{
		Kind:          syncwire.QRPKindPage,
		Status:        syncwire.QRPStatusOk,
		MaxBytes:      uint64(probeBytes),
		GeneratedAtMs: time.Now().UnixMilli(),
	})); err != nil {
		return err
	}
	return writeWireSpeedProbePayload(w, probeBytes)
}

func normalizeWireSpeedProbeBytes(requested int64) int64 {
	if requested <= 0 {
		return defaultWireSpeedProbeBytes
	}
	if requested > maxWireSpeedProbeBytes {
		return maxWireSpeedProbeBytes
	}
	return requested
}

// extendProbeWriteDeadline gives the next probe write DefaultHandlerTimeout
// to complete, when the writer is a stream that supports deadlines.
func extendProbeWriteDeadline(writer io.Writer) error {
	stream, ok := writer.(interface{ SetWriteDeadline(time.Time) error })
	if !ok {
		return nil
	}
	if err := stream.SetWriteDeadline(time.Now().Add(DefaultHandlerTimeout)); err != nil {
		return fmt.Errorf("wire-speed-probe: set write deadline: %w", err)
	}
	return nil
}

func writeWireSpeedProbePayload(writer io.Writer, totalBytes int64) error {
	chunk := make([]byte, wireSpeedProbeChunkBytes)
	var state uint32 = 0x9e3779b9
	for index := range chunk {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		chunk[index] = byte(state)
	}
	remaining := totalBytes
	for remaining > 0 {
		size := int64(len(chunk))
		if remaining < size {
			size = remaining
		}
		if err := extendProbeWriteDeadline(writer); err != nil {
			return err
		}
		if _, err := writer.Write(chunk[:size]); err != nil {
			return err
		}
		remaining -= size
	}
	return nil
}
