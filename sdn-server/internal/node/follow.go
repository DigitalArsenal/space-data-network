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
// when the subscription is made, at every connection and, for the peer's
// trust, at every page. Records are stored under the identity this node can
// verify: the authenticated peer of the stream is their origin and their
// producer. Record frames carry no signature, so a run whose header names
// another peer as its origin or producer is refused, and so is the page.
//
// Retention: the lane's rule (keep-all, replace-current, archive-all) is
// applied as an imported publication applies it, with the newest batch
// received as the imported one, once the producer has sent everything it
// stores and has been quiet for an interval (an empty page): a batch arriving
// is never superseded by a half-received one.
//
// Removals at the producer: at such a quiet page (at most once per
// followReconcileEvery, and at the first one of each connection) the node
// compares, for each batch of the lane it stored from the producer, its count
// with the producer's (one read-chunk asking for the batch's TOTAL_COUNT).
// When the producer holds fewer, the node reads the CIDs of the producer's
// batch (a read-chunk of the batch, hashed as it arrives, nothing stored) and
// removes the records of its batch that the producer no longer holds (a
// record also held under any other tag stays). After a removal it reads the
// lane again from its start, so it holds what the producer holds, and the
// lane's rule applies again at the next quiet page.
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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// followFileName is the followed lanes' file in the storage directory.
const followFileName = "follows.dss"

// followPageLimit is the records a followed page holds at most.
const followPageLimit = syncwire.FollowMaxRecords

// followReadWait bounds the wait for one followed page; the producer sends
// an empty page after a quiet interval, so a longer silence is a dead stream.
const followReadWait = 2 * time.Minute

// followReconcileEvery is the shortest interval between two comparisons of
// a lane's batches with the producer's.
const followReconcileEvery = 30 * time.Second

// errFollowReread ends a followed stream so the lane is read again from its
// start.
var errFollowReread = errors.New("records were removed at the producer; reading the lane again from its start")

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
}

type followEntry struct {
	status  FollowStatus
	samples []followRate
	cancel  context.CancelFunc
	done    chan struct{}
	// newestBatch is the batch of the newest record stored since the lane's
	// rule was last applied ("" when none).
	newestBatch   string
	lastReconcile time.Time
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
		pages, err := m.followOnce(ctx, e)
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
		log.Warnf("Followed lane %s %s/%s from %s: %v; reconnecting in %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
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
	reconciled := false
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
		if !m.n.peerRegistry.IsTrusted(remote) {
			_ = stream.Reset()
			return pages, fmt.Errorf("producer %s is no longer a trusted peer of this node", remote)
		}
		transfer := time.Since(began)
		verify, store, records, stored, newest, storeErr := m.storePage(ctx, st, remote, runs)
		m.mu.Lock()
		e.status.ReceivedBytes += received
		e.status.ReceivedRecords += int64(records)
		e.status.StoredRecords += int64(stored)
		e.status.TransferMs += transfer.Milliseconds()
		e.status.VerifyMs += verify.Milliseconds()
		e.status.StoreMs += store.Milliseconds()
		if newest != "" {
			e.newestBatch = newest
		}
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
		if records > 0 {
			e.status.LastReceivedAt = now
		}
		e.samples = append(e.samples, followRate{at: now, bytes: received})
		m.mu.Unlock()
		if records == 0 {
			// The producer has sent everything it stores and has been
			// quiet: the lane's rule, then its removals.
			m.applyRetention(st, e)
			if !reconciled || m.reconcileDue(e) {
				reconciled = true
				removed, err := m.reconcile(ctx, st, remote)
				m.mu.Lock()
				e.lastReconcile = time.Now()
				e.status.RemovedRecords += removed
				m.mu.Unlock()
				if err != nil {
					_ = stream.Reset()
					return pages, fmt.Errorf("compare the lane with the producer's: %w", err)
				}
				if removed > 0 {
					m.resetCursor(e)
					_ = stream.Reset()
					return pages, errFollowReread
				}
			}
		}
		if records > 0 || moved {
			if err := m.save(); err != nil {
				_ = stream.Reset()
				return pages, fmt.Errorf("save cursor: %w", err)
			}
		}
		if err := syncwire.Grant(stream, st.Schema); err != nil {
			_ = stream.Reset()
			return pages, fmt.Errorf("grant: %w", err)
		}
	}
}

// storePage verifies each record of a page against its standard and stores
// each run's records under the run's lane tags, with the stream's peer as
// their origin and producer. It returns when the store has returned, with
// what it stored before any failure, and the batch of the newest record.
func (m *followManager) storePage(ctx context.Context, st FollowStatus, remote peer.ID, runs []syncwire.FollowRun) (verify, store time.Duration, records, stored int, newest string, err error) {
	for _, run := range runs {
		h := run.Header
		schema := syncwire.NormalizeSchema(string(h.SCHEMA_NAME()))
		if schema != st.Schema || string(h.PROVIDER_ID()) != st.ProviderID || string(h.SOURCE_NAME()) != st.SourceName {
			return verify, store, records, stored, newest, fmt.Errorf("the producer sent a run of %s %s/%s on a stream of %s %s/%s",
				schema, h.PROVIDER_ID(), h.SOURCE_NAME(), st.Schema, st.ProviderID, st.SourceName)
		}
		for _, claim := range []struct{ field, value string }{{"ORIGIN_ID", string(h.ORIGIN_ID())}, {"PRODUCER_PEER_ID", string(h.PRODUCER_PEER_ID())}} {
			if claimed, err := peer.Decode(strings.TrimSpace(claim.value)); err == nil && claimed != remote {
				return verify, store, records, stored, newest, fmt.Errorf("the producer %s sent %d records whose %s names another peer, %s; unsigned records are taken only as the stream peer's own: refused",
					remote, len(run.Records), claim.field, claimed)
			}
		}
		began := time.Now()
		data := make([][]byte, 0, len(run.Records))
		for _, frame := range run.Records {
			body := frame[syncwire.FramePrefixLength:]
			if m.n.validator != nil {
				if err := m.n.validator.Validate(ctx, schema, body); err != nil {
					return verify, store, records, stored, newest, fmt.Errorf("record of %s fails its standard: %w", schema, err)
				}
			}
			data = append(data, body)
		}
		verify += time.Since(began)
		tags := storage.SourceTags{ProviderID: st.ProviderID, SourceName: st.SourceName, BatchID: string(h.BATCH_ID()), ProducerPeerID: remote.String()}
		began = time.Now()
		n, err := m.n.store.StoreBatchWithSourceTags(schema, data, remote.String(), nil, tags)
		store += time.Since(began)
		records += len(data)
		stored += n
		if err != nil {
			return verify, store, records, stored, newest, fmt.Errorf("store %d records of %s: %w", len(data), schema, err)
		}
		newest = tags.BatchID
	}
	return verify, store, records, stored, newest, nil
}

// applyRetention applies the lane's rule with the newest batch received
// since it was last applied as the imported one.
func (m *followManager) applyRetention(st FollowStatus, e *followEntry) {
	m.mu.Lock()
	batch := e.newestBatch
	e.newestBatch = ""
	m.mu.Unlock()
	if batch == "" {
		return
	}
	m.n.applyLaneRetention(st.Schema, st.ProviderID, st.SourceName, storage.RetainCandidate{BatchID: batch, PublishedAt: time.Now().UTC()}, nil)
}

func (m *followManager) reconcileDue(e *followEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Since(e.lastReconcile) >= followReconcileEvery
}

// reconcile removes the records of the lane this node stored from remote
// that remote no longer holds; it returns the records removed.
func (m *followManager) reconcile(ctx context.Context, st FollowStatus, remote peer.ID) (int64, error) {
	held, err := m.n.store.FollowLaneBatchCounts(st.Schema, st.ProviderID, st.SourceName, remote.String())
	if errors.Is(err, storage.ErrFollowLaneUnsupported) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	batches := make([]string, 0, len(held))
	for b := range held {
		if b != "" {
			batches = append(batches, b)
		}
	}
	sort.Strings(batches)
	var removed int64
	for _, batch := range batches {
		own := held[batch]
		theirs, err := m.producerBatchCount(ctx, remote, st, batch)
		if err != nil {
			return removed, err
		}
		if theirs >= own {
			continue
		}
		keep := map[string]bool{}
		if theirs > 0 {
			if keep, err = m.producerBatchCIDs(ctx, remote, st, batch); err != nil {
				return removed, err
			}
		}
		cids, err := m.n.store.FollowLaneBatchCIDs(st.Schema, st.ProviderID, st.SourceName, batch, remote.String())
		if err != nil {
			return removed, err
		}
		var gone []string
		for _, c := range cids {
			if !keep[c] {
				gone = append(gone, c)
			}
		}
		n, err := m.n.store.RemoveFollowLaneRecords(st.Schema, st.ProviderID, st.SourceName, batch, remote.String(), gone)
		removed += n
		log.Infof("Followed lane %s %s/%s from %s: the producer holds %d records of batch %s, this node %d; removed %d it no longer holds",
			st.Schema, st.ProviderID, st.SourceName, remote, theirs, batch, own, n)
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// producerRead opens one (unfollowed) read-chunk of a batch of the lane.
func (m *followManager) producerRead(ctx context.Context, remote peer.ID, st FollowStatus, batch, cursor string, limit int) (network.Stream, error) {
	stream, err := m.n.host.NewStream(ctx, remote, libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	_ = stream.SetDeadline(time.Now().Add(followReadWait))
	if err := syncwire.Request(stream, syncwire.QRPFields{SchemaName: st.Schema, ProviderID: st.ProviderID, SourceName: st.SourceName,
		BatchID: batch, Cursor: cursor, Limit: uint32(limit)}); err != nil {
		_ = stream.Reset()
		return nil, fmt.Errorf("write request: %w", err)
	}
	return stream, nil
}

// producerBatchCount is the producer's TOTAL_COUNT of one batch of the lane.
func (m *followManager) producerBatchCount(ctx context.Context, remote peer.ID, st FollowStatus, batch string) (int64, error) {
	stream, err := m.producerRead(ctx, remote, st, batch, "", 1)
	if err != nil {
		return 0, err
	}
	defer stream.Reset()
	header, _, err := syncwire.ReadPage(stream)
	if err != nil {
		return 0, fmt.Errorf("count batch %s at the producer: %w", batch, err)
	}
	return int64(header.TOTAL_COUNT()), nil
}

// producerBatchCIDs is the CIDs of one batch of the lane at the producer,
// hashed from its records as they arrive (nothing is stored or kept).
func (m *followManager) producerBatchCIDs(ctx context.Context, remote peer.ID, st FollowStatus, batch string) (map[string]bool, error) {
	cids := map[string]bool{}
	cursor := ""
	for {
		stream, err := m.producerRead(ctx, remote, st, batch, cursor, followPageLimit)
		if err != nil {
			return nil, err
		}
		next, err := readScanCIDs(stream, cids)
		stream.Close()
		if err != nil {
			return nil, fmt.Errorf("read batch %s at the producer: %w", batch, err)
		}
		if next == "" {
			return cids, nil
		}
		cursor = next
	}
}

// readScanCIDs reads one scan page to its end, adding each record's CID to
// cids, and returns the page's NEXT_CURSOR.
func readScanCIDs(r io.Reader, cids map[string]bool) (string, error) {
	next := ""
	read := 0
	for {
		header, _, err := syncwire.ReadPage(r)
		if errors.Is(err, io.EOF) {
			return next, nil
		}
		if err != nil {
			return "", err
		}
		next = string(header.NEXT_CURSOR())
		count := int(header.RECORD_COUNT())
		if count == 0 {
			continue
		}
		if read += count; read > followPageLimit {
			return "", fmt.Errorf("%w: more than %d records in a page", syncwire.ErrFollowPageLimit, followPageLimit)
		}
		for i := 0; i < count; i++ {
			frame, err := syncwire.ReadFrame(r, syncwire.MaxFrameBytes)
			if err != nil {
				return "", err
			}
			cids[storage.ComputeCID(frame[syncwire.FramePrefixLength:])] = true
		}
	}
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
