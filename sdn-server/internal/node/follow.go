package node

// Followed lanes: a subscription names a lane (standard, provider, source)
// and the producer peer to follow. The node opens a followed read-chunk
// stream to the producer (syncwire/follow.go), stores each page's records,
// and only after the store has returned saves the page's cursor and grants
// the next page. A restart resumes at the saved cursor; a record sent again
// is the same content address and is stored once.
//
// Trust: a lane is followed only from a peer this node trusts, for a lane
// its import policy admits (the decision trusted catch-up takes), checked
// when the subscription is made, at every connection and at every page.
// Records are stored under the identity this node can verify: the
// authenticated peer of the stream is their origin and their producer.
// Record frames carry no signature, so a run whose header names another peer
// as its origin or producer is refused, and so is the page. Every $QRP frame
// is structurally verified before it is read (syncwire.ParseQRP), and a
// panic in a followed stream ends that stream with an error and an alert
// (follow_refused), never the process.
//
// The current batch: every followed page's end header carries the producer's
// statement of the lane, its current batch and that batch's records. Records
// a new batch carries over unchanged from the last one keep their content
// address, so the producer tags them to the new batch in place and never
// sends them again by sequence. So at a quiet page (an empty page: the
// producer has sent everything it stores) whose statement differs from the
// batch this node holds as current, the node reads the producer's list of
// the batch's CIDs (paged, bounded by the stated count), tags the records it
// already holds to the batch, fetches the others by CID, and only once it
// holds every record of the list makes the batch current: a half-received
// batch never is. It then removes the records of the batch it holds from
// that producer that the list no longer names, and applies the lane's rule
// with the producer's batch as the kept one (replace-current removes what
// only the producer's other batches hold; another producer's records of the
// lane stay). A list read and its fetches run at most once per
// followSyncEvery per lane.
//
// Subscriptions are kept on disk as size-prefixed $DSS records, one per
// followed lane (followFileName): the lane, the producer (PROVIDER_PEER_ID),
// the cursor to resume at (CURSOR, a resume cursor the producer's scan
// generation verifies) and what has been received.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// followFileName is the followed lanes' file in the storage directory.
const followFileName = "follows.dss"

// followPageLimit is the records a followed page holds.
const followPageLimit = syncwire.FollowMaxRecords

// followReadWait bounds the wait for one followed page; the producer sends
// an empty page after a quiet interval, so a longer silence is a dead stream.
const followReadWait = 2 * time.Minute

// followSyncEvery is the shortest interval between two reads of a producer's
// list of its current batch, per lane.
const followSyncEvery = 30 * time.Second

// followMaxBatchRecords bounds the producer's current batch this node reads
// a list of and holds.
const followMaxBatchRecords = 1 << 20

// errFollowRefused marks what the producer sent that this node refuses.
var errFollowRefused = errors.New("refused")

// FollowKey names one followed lane.
type FollowKey struct {
	Schema, ProviderID, SourceName, Producer string
}

// FollowStatus is one followed lane as this node has received it.
type FollowStatus struct {
	FollowKey
	Cursor                string
	ReceivedBytes         int64
	ReceivedRecords       int64
	StoredRecords         int64
	RemovedRecords        int64
	Pages                 int64
	TransferMs            int64
	VerifyMs              int64
	StoreMs               int64
	ReceiveBytesPerSecond int64
	LastReceivedAt        time.Time
	Connected             bool
	LastError             string
	// Retracted is true once the producer stated the lane retracted: this
	// node dropped the records it held of the lane from that producer.
	Retracted bool
}

type followEntry struct {
	status  FollowStatus
	samples []followRate
	cancel  context.CancelFunc
	done    chan struct{}
	// current is the producer's statement this node holds as its current
	// batch, held the records of it this node held then; lastSync is when
	// the producer's list was last read.
	current  syncwire.FollowStatement
	held     int64
	lastSync time.Time
}

type followRate struct {
	at    time.Time
	bytes int64
}

const followRateWindow = 10 * time.Second

// followManager runs the node's followed lanes.
type followManager struct {
	n    *Node
	path string
	mu   sync.Mutex
	subs map[FollowKey]*followEntry
	// saveMu serializes writes of the file.
	saveMu sync.Mutex
}

func newFollowManager(n *Node, dir string) *followManager {
	return &followManager{n: n, path: filepath.Join(dir, followFileName), subs: map[FollowKey]*followEntry{}}
}

// load reads the followed lanes' file and starts each lane.
func (m *followManager) load() error {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", m.path, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, frame := range frames {
		if !DSS.SizePrefixedDSSBufferHasIdentifier(frame) {
			return fmt.Errorf("%s: a frame that is not a $DSS record", m.path)
		}
		d := DSS.GetSizePrefixedRootAsDSS(frame, 0)
		st := FollowStatus{FollowKey: FollowKey{Schema: syncwire.NormalizeSchema(string(d.SCHEMA_NAME())), ProviderID: string(d.PROVIDER_ID()),
			SourceName: string(d.SOURCE_NAME()), Producer: string(d.PROVIDER_PEER_ID())},
			Cursor:        string(d.CURSOR()),
			ReceivedBytes: int64(d.DOWNLOADED_BYTES()), StoredRecords: int64(d.LOCAL_ROWS()), ReceivedRecords: int64(d.SYNCED_ROWS())}
		if st.Schema == "" || st.Producer == "" || !d.SUBSCRIBED() {
			continue
		}
		if _, held := m.subs[st.FollowKey]; !held {
			m.startLocked(st)
		}
	}
	return nil
}

// Follow starts following a lane from a producer (on) or stops it (off).
// Stopping with an empty producer stops every producer of the lane; with an
// empty provider and source, every followed lane of the standard.
func (m *followManager) Follow(key FollowKey, on bool) error {
	key.Schema = syncwire.NormalizeSchema(key.Schema)
	key.ProviderID, key.SourceName, key.Producer = strings.TrimSpace(key.ProviderID), strings.TrimSpace(key.SourceName), strings.TrimSpace(key.Producer)
	if key.Schema == "" {
		return errors.New("a followed lane names a standard")
	}
	if on {
		if key.ProviderID == "" || key.SourceName == "" {
			return errors.New("a followed lane names a standard, a provider and a source")
		}
		pid, err := peer.Decode(key.Producer)
		if err != nil {
			return fmt.Errorf("producer peer id: %w", err)
		}
		if err := m.n.followAdmitted(pid, key); err != nil {
			return err
		}
		m.mu.Lock()
		if _, held := m.subs[key]; !held {
			m.startLocked(FollowStatus{FollowKey: key})
		}
		m.mu.Unlock()
		return m.save()
	}
	if (key.ProviderID == "") != (key.SourceName == "") {
		return errors.New("stopping a followed lane names both a provider and a source, or neither (every lane of the standard)")
	}
	var stopped []*followEntry
	m.mu.Lock()
	for k, e := range m.subs {
		if k.Schema != key.Schema {
			continue
		}
		if key.ProviderID != "" && (k.ProviderID != key.ProviderID || k.SourceName != key.SourceName) {
			continue
		}
		if key.Producer != "" && k.Producer != key.Producer {
			continue
		}
		stopped = append(stopped, e)
		delete(m.subs, k)
	}
	m.mu.Unlock()
	for _, e := range stopped {
		e.cancel()
		<-e.done
	}
	return m.save()
}

// followAdmitted is the trust decision for following key from pid: a peer
// this node trusts, not itself, for a lane its import policy admits (the
// check trusted catch-up applies to a publication).
func (n *Node) followAdmitted(pid peer.ID, key FollowKey) error {
	if n.host != nil && pid == n.host.ID() {
		return errors.New("a node does not follow its own lanes")
	}
	if n.peerRegistry == nil || !n.peerRegistry.IsTrusted(pid) {
		return fmt.Errorf("producer %s is not a trusted peer of this node", pid)
	}
	if !n.trustedPeerLaneImportAllowed(pid, key.Schema, key.ProviderID, key.SourceName, "") {
		return fmt.Errorf("this node's import policy does not admit %s %s/%s from %s", key.Schema, key.ProviderID, key.SourceName, pid)
	}
	return nil
}

// followTrusted reports a peer this node has a trust relation with: its
// followed streams get the trusted limits (protocol.FlatSQLSyncHandler).
func (n *Node) followTrusted(id peer.ID) bool {
	return n != nil && n.peerRegistry != nil && n.peerRegistry.IsTrusted(id)
}

// startLocked starts one lane; the caller holds m.mu.
func (m *followManager) startLocked(st FollowStatus) {
	ctx, cancel := context.WithCancel(m.n.ctx)
	e := &followEntry{status: st, cancel: cancel, done: make(chan struct{})}
	m.subs[st.FollowKey] = e
	m.n.wg.Add(1)
	go func() {
		defer m.n.wg.Done()
		defer close(e.done)
		m.run(ctx, e)
	}()
}

// run follows one lane until ctx ends, reconnecting after a failure. The
// wait before a reconnect doubles up to 30 s and starts again at 1 s after a
// connection that delivered a page.
func (m *followManager) run(ctx context.Context, e *followEntry) {
	backoff := time.Second
	for ctx.Err() == nil {
		pages, err := m.followGuarded(ctx, e)
		if ctx.Err() != nil {
			return
		}
		if pages > 0 {
			backoff = time.Second
		}
		m.mu.Lock()
		e.status.Connected = false
		if err != nil {
			e.status.LastError = err.Error()
		}
		key := e.status.FollowKey
		m.mu.Unlock()
		if followRefusal(err) {
			m.n.alerts.Raise(ops.KindFollowRefused, followAlertSubject(key), ops.SeverityError, err.Error())
		}
		log.Warnf("Followed lane %s %s/%s from %s: %.1024q; reconnecting in %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, fmt.Sprint(err), backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// followGuarded is one followed stream: a panic in it (bytes from the
// producer reaching code that should have refused them first) ends that
// stream with an error, logged with its stack.
func (m *followManager) followGuarded(ctx context.Context, e *followEntry) (pages int, err error) {
	m.mu.Lock()
	key := e.status.FollowKey
	m.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: the followed stream panicked: %v", errFollowRefused, r)
			log.Errorf("Followed lane %s %s/%s from %s: %v\n%s", key.Schema, key.ProviderID, key.SourceName, key.Producer, err, debug.Stack())
		}
	}()
	return m.followOnce(ctx, e)
}

// followRefusal reports an error that is the producer's fault: a malformed
// frame, a page past its limits, a run naming another peer, a panic.
func followRefusal(err error) bool {
	return errors.Is(err, errFollowRefused) || errors.Is(err, syncwire.ErrMalformedQRP) || errors.Is(err, syncwire.ErrFollowPageLimit)
}

func followAlertSubject(key FollowKey) string {
	return key.Schema + " " + key.ProviderID + "/" + key.SourceName + " from " + shortPeerID(key.Producer)
}

// followOnce runs one followed stream until it fails or ctx ends; it
// returns the pages it stored.
func (m *followManager) followOnce(ctx context.Context, e *followEntry) (int, error) {
	m.mu.Lock()
	st := e.status
	m.mu.Unlock()
	pid, err := peer.Decode(st.Producer)
	if err != nil {
		return 0, err
	}
	if err := m.n.followAdmitted(pid, st.FollowKey); err != nil {
		return 0, err
	}
	stream, err := m.n.host.NewStream(ctx, pid, libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)))
	if err != nil {
		return 0, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	remote := stream.Conn().RemotePeer()
	req := syncwire.QRPFields{SchemaName: st.Schema, ProviderID: st.ProviderID, SourceName: st.SourceName, Limit: followPageLimit, Follow: true,
		ResumeCursor: st.Cursor}
	if err := syncwire.Request(stream, req); err != nil {
		_ = stream.Reset()
		return 0, fmt.Errorf("write request: %w", err)
	}
	log.Infof("Followed lane %s %s/%s from %s: stream open", st.Schema, st.ProviderID, st.SourceName, st.Producer)
	pages := 0
	for {
		_ = stream.SetReadDeadline(time.Now().Add(followReadWait))
		began := time.Now()
		runs, end, received, err := syncwire.ReadFollowPage(stream, followPageLimit)
		if err != nil {
			var remoteErr *syncwire.RemoteError
			if errors.As(err, &remoteErr) && strings.Contains(remoteErr.Code, "cursor") {
				// The producer no longer accepts the saved cursor: read the
				// lane again from its start; records already held are
				// stored once.
				m.resetCursor(e)
			}
			_ = stream.Reset()
			return pages, err
		}
		if err := m.n.followAdmitted(remote, st.FollowKey); err != nil {
			_ = stream.Reset()
			return pages, fmt.Errorf("%v; the stream ends", err)
		}
		transfer := time.Since(began)
		res, storeErr := m.storeRuns(ctx, st, remote, runs, "", nil)
		m.mu.Lock()
		e.status.ReceivedBytes += received
		e.status.ReceivedRecords += int64(res.records)
		e.status.StoredRecords += int64(res.stored)
		e.status.TransferMs += transfer.Milliseconds()
		e.status.VerifyMs += res.verify.Milliseconds()
		e.status.StoreMs += res.store.Milliseconds()
		m.mu.Unlock()
		if storeErr != nil {
			_ = stream.Reset()
			return pages, storeErr
		}
		pages++
		m.mu.Lock()
		e.status.Connected, e.status.LastError = true, ""
		moved := e.status.Cursor != string(end.RESUME_CURSOR())
		e.status.Cursor = string(end.RESUME_CURSOR())
		e.status.Pages++
		now := time.Now()
		if res.records > 0 {
			e.status.LastReceivedAt = now
		}
		e.samples = append(e.samples, followRate{at: now, bytes: received})
		m.mu.Unlock()
		if res.records > 0 || moved {
			if err := m.save(); err != nil {
				_ = stream.Reset()
				return pages, fmt.Errorf("save cursor: %w", err)
			}
		}
		if res.records == 0 {
			// The producer has sent everything it stores and has been
			// quiet: hold its current batch as it states it. A failure is
			// the lane's error until a later quiet page succeeds; the
			// stream goes on.
			if err := m.holdCurrent(ctx, st, e, remote, syncwire.StatementOf(end)); err != nil {
				if ctx.Err() != nil {
					return pages, ctx.Err()
				}
				m.mu.Lock()
				e.status.LastError = err.Error()
				m.mu.Unlock()
				if followRefusal(err) {
					m.n.alerts.Raise(ops.KindFollowRefused, followAlertSubject(st.FollowKey), ops.SeverityError, err.Error())
				}
				log.Warnf("Followed lane %s %s/%s from %s: %.1024q", st.Schema, st.ProviderID, st.SourceName, remote, err.Error())
			}
		}
		if err := syncwire.Grant(stream, st.Schema); err != nil {
			_ = stream.Reset()
			return pages, fmt.Errorf("grant: %w", err)
		}
	}
}

// storedRuns is what storeRuns stored.
type storedRuns struct {
	verify, store   time.Duration
	records, stored int
	cids            []string
}

// storeRuns verifies each record of a page's runs against its standard and
// stores each run's records under the lane's tags and the run's batch (or
// batch, when set), with the stream's peer as their origin and producer.
// With want set, every record must be one of want's CIDs. It returns when the
// store has returned, with what it stored before any failure.
func (m *followManager) storeRuns(ctx context.Context, st FollowStatus, remote peer.ID, runs []syncwire.FollowRun, batch string, want map[string]bool) (storedRuns, error) {
	var res storedRuns
	for _, run := range runs {
		h := run.Header
		schema := syncwire.NormalizeSchema(string(h.SCHEMA_NAME()))
		if schema != st.Schema || string(h.PROVIDER_ID()) != st.ProviderID || string(h.SOURCE_NAME()) != st.SourceName {
			return res, fmt.Errorf("%w: the producer sent a run of %s %s/%s on a stream of %s %s/%s", errFollowRefused,
				schema, h.PROVIDER_ID(), h.SOURCE_NAME(), st.Schema, st.ProviderID, st.SourceName)
		}
		for _, claim := range []struct{ field, value string }{{"ORIGIN_ID", string(h.ORIGIN_ID())}, {"PRODUCER_PEER_ID", string(h.PRODUCER_PEER_ID())}} {
			if claimed, err := peer.Decode(strings.TrimSpace(claim.value)); err == nil && claimed != remote {
				return res, fmt.Errorf("%w: the producer %s sent %d records whose %s names another peer, %s; unsigned records are taken only as the stream peer's own",
					errFollowRefused, remote, len(run.Records), claim.field, claimed)
			}
		}
		began := time.Now()
		data := make([][]byte, 0, len(run.Records))
		for _, frame := range run.Records {
			body := frame[syncwire.FramePrefixLength:]
			if want != nil {
				cid := storage.ComputeCID(body)
				if !want[cid] {
					return res, fmt.Errorf("%w: the producer sent a record (%s) it was not asked for", errFollowRefused, cid)
				}
				res.cids = append(res.cids, cid)
			}
			if m.n.validator != nil {
				if err := m.n.validator.Validate(ctx, schema, body); err != nil {
					return res, fmt.Errorf("%w: a record of %s fails its standard: %v", errFollowRefused, schema, err)
				}
			}
			data = append(data, body)
		}
		res.verify += time.Since(began)
		tags := storage.SourceTags{ProviderID: st.ProviderID, SourceName: st.SourceName, BatchID: string(h.BATCH_ID()), ProducerPeerID: remote.String()}
		if batch != "" {
			tags.BatchID = batch
		}
		began = time.Now()
		n, err := m.n.store.StoreBatchWithSourceTags(schema, data, remote.String(), nil, tags)
		res.store += time.Since(began)
		res.records += len(data)
		res.stored += n
		if err != nil {
			return res, fmt.Errorf("store %d records of %s: %w", len(data), schema, err)
		}
	}
	return res, nil
}

// holdCurrent brings the producer's current batch, as stmt states it, into
// this node (see the package comment). It returns nil when there is nothing
// to do, or when the last list read was less than followSyncEvery ago.
func (m *followManager) holdCurrent(ctx context.Context, st FollowStatus, e *followEntry, remote peer.ID, stmt syncwire.FollowStatement) error {
	if stmt.Retracted {
		return m.dropRetracted(st, e, remote)
	}
	if stmt.BatchID == "" {
		return nil
	}
	counts, err := m.n.store.FollowLaneBatchCounts(st.Schema, st.ProviderID, st.SourceName, remote.String())
	if errors.Is(err, storage.ErrFollowLaneUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	own := counts[stmt.BatchID]
	m.mu.Lock()
	switch {
	case e.current.BatchID == stmt.BatchID && own == stmt.Records:
		// The current batch grew by records that arrived in sequence.
		e.current, e.held = stmt, own
		m.mu.Unlock()
		return nil
	case e.current == stmt && own == e.held:
		m.mu.Unlock()
		return nil
	case time.Since(e.lastSync) < followSyncEvery:
		m.mu.Unlock()
		return nil
	}
	e.lastSync = time.Now()
	m.mu.Unlock()

	began := time.Now()
	if stmt.Records > followMaxBatchRecords {
		return fmt.Errorf("%w: the producer states %d records in batch %s, more than the %d this node holds of a followed batch", errFollowRefused,
			stmt.Records, stmt.BatchID, followMaxBatchRecords)
	}
	list, err := m.producerBatchList(ctx, remote, st, stmt)
	if err != nil {
		return fmt.Errorf("read the producer's list of batch %s: %w", stmt.BatchID, err)
	}
	held, err := m.n.store.FollowLaneBatchCIDs(st.Schema, st.ProviderID, st.SourceName, stmt.BatchID, remote.String())
	if err != nil {
		return err
	}
	slices.Sort(held)
	held = slices.Compact(held)
	missing, extra := sortedDifference(list, held), sortedDifference(held, list)
	got, fetched, err := m.holdRecords(ctx, st, remote, stmt.BatchID, missing)
	if err != nil {
		return fmt.Errorf("hold the records of batch %s: %w", stmt.BatchID, err)
	}
	if got < len(missing) {
		return fmt.Errorf("this node holds %d of the %d records the producer lists in batch %s; the batch is not current",
			len(list)-len(missing)+got, len(list), stmt.BatchID)
	}
	rule := m.n.laneRetentionRule(st.Schema, st.ProviderID, st.SourceName)
	gone := func(batch string) bool { return batch == stmt.BatchID }
	if rule == channels.RetentionReplaceCurrent {
		gone = func(string) bool { return true }
	}
	removed, err := m.n.store.RemoveFollowLaneRecords(st.Schema, st.ProviderID, st.SourceName, remote.String(), gone, extra)
	if err != nil {
		return fmt.Errorf("remove the records batch %s no longer holds: %w", stmt.BatchID, err)
	}
	var superseded int64
	if rule == channels.RetentionReplaceCurrent && !m.n.laneProducedHere(st.Schema, st.ProviderID, st.SourceName) {
		m.n.datasetSupersedeMu.Lock()
		superseded, err = m.n.store.SupersedeFollowLane(st.Schema, st.ProviderID, st.SourceName, remote.String(), stmt.BatchID)
		m.n.datasetSupersedeMu.Unlock()
		if err != nil {
			return fmt.Errorf("keep batch %s as the current set: %w", stmt.BatchID, err)
		}
	}
	counts, err = m.n.store.FollowLaneBatchCounts(st.Schema, st.ProviderID, st.SourceName, remote.String())
	if err != nil {
		return err
	}
	m.mu.Lock()
	e.current, e.held = stmt, counts[stmt.BatchID]
	e.status.RemovedRecords += removed + superseded
	m.mu.Unlock()
	m.n.alerts.Clear(ops.KindFollowRefused, followAlertSubject(st.FollowKey))
	log.Infof("Followed lane %s %s/%s from %s: batch %s is current (%d records listed, %d tagged from held records, %d fetched by CID, %d removed that the batch no longer holds, %d removed with the superseded batches; rule %s) in %s",
		st.Schema, st.ProviderID, st.SourceName, remote, stmt.BatchID, len(list), got-fetched, fetched, removed, superseded, rule,
		time.Since(began).Round(time.Millisecond))
	return nil
}

// dropRetracted removes every record of the lane this node holds from the
// producer, because the producer states the lane retracted. A record also
// held under another lane or producer stays (RemoveFollowLaneRecords). What
// the producer served to others, and any copy this node already exported or
// forwarded, is not this node's to recall.
func (m *followManager) dropRetracted(st FollowStatus, e *followEntry, remote peer.ID) error {
	counts, err := m.n.store.FollowLaneBatchCounts(st.Schema, st.ProviderID, st.SourceName, remote.String())
	if errors.Is(err, storage.ErrFollowLaneUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	var removed int64
	for batch := range counts {
		cids, err := m.n.store.FollowLaneBatchCIDs(st.Schema, st.ProviderID, st.SourceName, batch, remote.String())
		if err != nil {
			return err
		}
		n, err := m.n.store.RemoveFollowLaneRecords(st.Schema, st.ProviderID, st.SourceName, remote.String(), func(string) bool { return true }, cids)
		removed += n
		if err != nil {
			return fmt.Errorf("remove the records of the retracted lane: %w", err)
		}
	}
	m.mu.Lock()
	e.current, e.held = syncwire.FollowStatement{Retracted: true}, 0
	e.status.Retracted = true
	e.status.RemovedRecords += removed
	e.status.LastError = ""
	m.mu.Unlock()
	if removed > 0 {
		log.Infof("Followed lane %s %s/%s from %s: the producer retracted the lane; %d records removed", st.Schema, st.ProviderID, st.SourceName, remote, removed)
	}
	return nil
}

// sortedDifference is the strings of a (sorted, distinct) that b (sorted,
// distinct) lacks.
func sortedDifference(a, b []string) []string {
	var out []string
	j := 0
	for _, s := range a {
		for j < len(b) && b[j] < s {
			j++
		}
		if j >= len(b) || b[j] != s {
			out = append(out, s)
		}
	}
	return out
}

// producerBatchList is the producer's list of the CIDs of its batch (sorted,
// distinct), read in pages; a list longer than the statement allows (its
// count and one page more, at most followMaxBatchRecords) is refused.
func (m *followManager) producerBatchList(ctx context.Context, remote peer.ID, st FollowStatus, stmt syncwire.FollowStatement) ([]string, error) {
	bound := min(stmt.Records+syncwire.FollowCIDPageLimit, followMaxBatchRecords)
	var list []string
	cursor := ""
	for {
		stream, err := m.producerRead(ctx, remote, st, stmt.BatchID, cursor, syncwire.FollowCIDPageLimit,
			syncwire.QRPFilterField{Field: syncwire.FilterProjection, Op: syncwire.QRPFilterEq, Value: syncwire.ProjectionCID})
		if err != nil {
			return nil, err
		}
		cids, next, err := syncwire.ReadCIDPage(stream)
		_ = stream.Close()
		if err != nil {
			return nil, err
		}
		list = append(list, cids...)
		if int64(len(list)) > bound {
			return nil, fmt.Errorf("%w: the producer lists more than %d records in batch %s, which it states holds %d", errFollowRefused,
				bound, stmt.BatchID, stmt.Records)
		}
		if next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("%w: the producer's list of batch %s does not advance", errFollowRefused, stmt.BatchID)
		}
		cursor = next
	}
	slices.Sort(list)
	return slices.Compact(list), nil
}

// holdRecords gives the records of cids batch's tag from remote: those this
// node holds from their stored bytes, the others fetched from remote by CID.
// It returns how many it holds so and how many of those it fetched.
func (m *followManager) holdRecords(ctx context.Context, st FollowStatus, remote peer.ID, batch string, cids []string) (int, int, error) {
	tags := storage.SourceTags{ProviderID: st.ProviderID, SourceName: st.SourceName, BatchID: batch, ProducerPeerID: remote.String()}
	got := 0
	var absent []string
	for start := 0; start < len(cids); start += 1024 {
		part := cids[start:min(start+1024, len(cids))]
		bytesOf, err := m.n.store.HeldRecordBytes(st.Schema, part)
		if err != nil {
			return got, 0, err
		}
		data := make([][]byte, 0, len(bytesOf))
		for _, c := range part {
			if b, ok := bytesOf[c]; ok {
				data = append(data, b)
			} else {
				absent = append(absent, c)
			}
		}
		if len(data) == 0 {
			continue
		}
		if _, err := m.n.store.StoreBatchWithSourceTags(st.Schema, data, remote.String(), nil, tags); err != nil {
			return got, 0, fmt.Errorf("tag %d held records to batch %s: %w", len(data), batch, err)
		}
		got += len(data)
	}
	fetched := 0
	for start := 0; start < len(absent); start += syncwire.FollowFetchMaxCIDs {
		pending := absent[start:min(start+syncwire.FollowFetchMaxCIDs, len(absent))]
		for len(pending) > 0 {
			stored, err := m.fetchByCID(ctx, remote, st, batch, pending)
			if err != nil {
				return got, fetched, err
			}
			if len(stored) == 0 {
				return got, fetched, nil
			}
			got += len(stored)
			fetched += len(stored)
			done := make(map[string]bool, len(stored))
			for _, c := range stored {
				done[c] = true
			}
			rest := pending[:0:0]
			for _, c := range pending {
				if !done[c] {
					rest = append(rest, c)
				}
			}
			pending = rest
		}
	}
	return got, fetched, nil
}

// fetchByCID reads records of batch named by CID from remote (one followed
// page, under the page's limits) and stores them with the batch's tag. It
// returns the CIDs stored.
func (m *followManager) fetchByCID(ctx context.Context, remote peer.ID, st FollowStatus, batch string, cids []string) ([]string, error) {
	stream, err := m.producerRead(ctx, remote, st, batch, "", len(cids),
		syncwire.QRPFilterField{Field: syncwire.FilterCID, Op: syncwire.QRPFilterIn, Value: strings.Join(cids, ",")})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	runs, _, _, err := syncwire.ReadFollowPage(stream, len(cids))
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(cids))
	for _, c := range cids {
		want[c] = true
	}
	res, err := m.storeRuns(ctx, st, remote, runs, batch, want)
	m.mu.Lock()
	if e := m.subs[st.FollowKey]; e != nil {
		e.status.ReceivedRecords += int64(res.records)
		e.status.StoredRecords += int64(res.stored)
	}
	m.mu.Unlock()
	return res.cids, err
}

// producerRead opens one (unfollowed) read-chunk of a batch of the lane.
func (m *followManager) producerRead(ctx context.Context, remote peer.ID, st FollowStatus, batch, cursor string, limit int, filters ...syncwire.QRPFilterField) (network.Stream, error) {
	stream, err := m.n.host.NewStream(ctx, remote, libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	_ = stream.SetDeadline(time.Now().Add(followReadWait))
	if err := syncwire.Request(stream, syncwire.QRPFields{SchemaName: st.Schema, ProviderID: st.ProviderID, SourceName: st.SourceName,
		BatchID: batch, Cursor: cursor, Limit: uint32(limit), Filters: filters}); err != nil {
		_ = stream.Reset()
		return nil, fmt.Errorf("write request: %w", err)
	}
	return stream, nil
}

func (m *followManager) resetCursor(e *followEntry) {
	m.mu.Lock()
	e.status.Cursor = ""
	m.mu.Unlock()
	_ = m.save()
}

// Statuses is every followed lane's status, sorted by lane.
func (m *followManager) Statuses() []FollowStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]FollowStatus, 0, len(m.subs))
	for _, e := range m.subs {
		keep := 0
		for keep < len(e.samples) && now.Sub(e.samples[keep].at) > followRateWindow {
			keep++
		}
		e.samples = e.samples[keep:]
		var b int64
		for _, s := range e.samples {
			b += s.bytes
		}
		st := e.status
		st.ReceiveBytesPerSecond = int64(float64(b) / followRateWindow.Seconds())
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].FollowKey, out[j].FollowKey
		return a.Schema+"\x00"+a.ProviderID+"\x00"+a.SourceName+"\x00"+a.Producer < b.Schema+"\x00"+b.ProviderID+"\x00"+b.SourceName+"\x00"+b.Producer
	})
	return out
}

// save writes every followed lane as one $DSS record, atomically.
func (m *followManager) save() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	var buf bytes.Buffer
	for _, st := range m.Statuses() {
		buf.Write(encodeFollowDSS(st))
	}
	tmp := m.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// encodeFollowDSS is one followed lane as a size-prefixed $DSS record.
func encodeFollowDSS(st FollowStatus) []byte {
	b := flatbuffers.NewBuilder(512)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	schema := str(strings.TrimSuffix(st.Schema, ".fbs"))
	provider := str(st.ProviderID)
	source := str(st.SourceName)
	producer := str(st.Producer)
	cursor := str(st.Cursor)
	protocolID := str(syncwire.ProtocolID(syncwire.OpReadChunk) + " follow")
	errText := str(st.LastError)
	lastAt := ""
	if !st.LastReceivedAt.IsZero() {
		lastAt = st.LastReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	last := str(lastAt)
	DSS.DSSStart(b)
	if st.Connected {
		DSS.DSSAddSTATUS(b, DSS.EnumValuesdssSyncState["SYNCING"])
	}
	DSS.DSSAddSYNCED_ROWS(b, uint64(st.ReceivedRecords))
	DSS.DSSAddLOCAL_ROWS(b, uint64(st.StoredRecords))
	DSS.DSSAddDOWNLOADED_BYTES(b, uint64(st.ReceivedBytes))
	DSS.DSSAddDOWNLOAD_SPEED_BYTES_PER_SECOND(b, uint64(st.ReceiveBytesPerSecond))
	DSS.DSSAddNETWORK_TRANSFER_MS(b, uint64(st.TransferMs))
	DSS.DSSAddVERIFICATION_MS(b, uint64(st.VerifyMs))
	DSS.DSSAddFLATSQL_MATERIALIZATION_MS(b, uint64(st.StoreMs))
	if producer != 0 {
		DSS.DSSAddPROVIDER_PEER_ID(b, producer)
	}
	if cursor != 0 {
		DSS.DSSAddCURSOR(b, cursor)
	}
	DSS.DSSAddSYNC_PROTOCOL(b, protocolID)
	if last != 0 {
		DSS.DSSAddLAST_SYNCED_AT(b, last)
	}
	if errText != 0 {
		DSS.DSSAddERROR(b, errText)
	}
	if schema != 0 {
		DSS.DSSAddSCHEMA_NAME(b, schema)
	}
	if provider != 0 {
		DSS.DSSAddPROVIDER_ID(b, provider)
	}
	if source != 0 {
		DSS.DSSAddSOURCE_NAME(b, source)
	}
	DSS.DSSAddSUBSCRIBED(b, true)
	DSS.FinishSizePrefixedDSSBuffer(b, DSS.DSSEnd(b))
	return b.FinishedBytes()
}

// Follow starts (on) or stops following a lane from a producer peer.
func (n *Node) Follow(schema, providerID, sourceName, producer string, on bool) error {
	if n.follows == nil {
		return errors.New("this node has no store to follow lanes into")
	}
	return n.follows.Follow(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: producer}, on)
}

// FollowStatuses is the lanes this node follows, as received.
func (n *Node) FollowStatuses() []FollowStatus {
	if n.follows == nil {
		return nil
	}
	return n.follows.Statuses()
}

// ServedFollows is what this node has served to its followers, per lane.
func (n *Node) ServedFollows() map[protocol.FollowLane]protocol.FollowStat {
	if n.flatsqlSync == nil {
		return nil
	}
	return n.flatsqlSync.FollowStats()
}
