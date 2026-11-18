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
// generation verifies), when it was subscribed (LAST_SYNC_STARTED_AT, Unix
// ms) and what has been received.
//
// The first read is bounded by the lane's rule. A subscription starts at the
// producer's current batch (FollowStartCurrent) or at the lane's beginning
// (FollowStartBeginning); by default a replace-current lane starts at the
// current batch and a keep-all or archive-all lane at the beginning. To start
// at the current batch the node first reads the producer's statement of the
// lane (syncwire.LaneStatement: the current batch, the batch's and the
// lane's size, and the lane's present position as a resume cursor), saves
// that position as its cursor, and opens the followed stream there: the
// stream's first page is empty, and the current batch is then held as above
// (its list read, its records fetched by CID). Nothing older is sent. Until
// the cursor is saved the record carries SYNC_FILTER "start=current", so a
// restart still starts at the current batch. Before a subscription starts
// the node checks the first read (the batch, or the lane) against its free
// disk and its record ceiling (FollowPlan); a refusal names both sizes and
// can be overridden (FollowOptions.Force).

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
	"github.com/spacedatanetwork/sdn-server/internal/config"
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

// Start modes of a subscription (FollowOptions.Start).
const (
	FollowStartCurrent   = "current"
	FollowStartBeginning = "beginning"
	// followStartPending is the $DSS SYNC_FILTER of a subscription that
	// starts at the current batch and has no cursor yet.
	followStartPending = "start=current"
)

// followStoreExpansion is the store bytes a record byte takes (5.0 to 5.3
// measured, stream-790 Part A); followDiskReserve the free disk a first
// read leaves.
const (
	followStoreExpansion = 5
	followDiskReserve    = 1 << 30
)

// followStatementWait bounds one read of a producer's statement.
const followStatementWait = 60 * time.Second

// FollowKey names one followed lane.
type FollowKey struct {
	Schema, ProviderID, SourceName, Producer string
}

// FollowOptions are how a subscription starts.
type FollowOptions struct {
	// Start is FollowStartCurrent, FollowStartBeginning, or "" (by the
	// lane's rule: replace-current starts at the current batch).
	Start string
	// Force subscribes although the first read does not fit or the
	// producer does not state the lane.
	Force bool
}

// FollowPlan is a subscription's first read and whether this node takes it.
type FollowPlan struct {
	FollowKey
	Start, Rule string
	// Statement is the producer's; StatementError why there is none.
	Statement      syncwire.LaneStatement
	StatementError string
	// FirstReadRecords and FirstReadBytes are the current batch (start
	// current) or the lane (start beginning).
	FirstReadRecords, FirstReadBytes int64
	FreeBytes, NeedBytes             int64
	HeldRecords, RecordCeiling       int64
	// Refusal is why the first read does not fit ("" when it does).
	Refusal string
}

// FollowStatus is one followed lane as this node has received it.
type FollowStatus struct {
	FollowKey
	// Start is FollowStartCurrent while the subscription waits to start at
	// the producer's current batch (no cursor yet).
	Start string
	// Since is when the lane was subscribed.
	Since                 time.Time
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
	// CurrentBatch is the batch this node holds as the producer's current
	// one; Stated the producer's latest statement on a followed page; Lag
	// the records of the stated batch this node does not hold as current.
	CurrentBatch string
	Stated       syncwire.FollowStatement
	Lag          int64
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
	// stated is the statement of the latest followed page.
	stated syncwire.FollowStatement
	// rule is the lane's rule as last applied to the current batch.
	rule string
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
		if st.Cursor == "" && string(d.SYNC_FILTER()) == followStartPending {
			st.Start = FollowStartCurrent
		}
		if ms := d.LAST_SYNC_STARTED_AT(); ms > 0 {
			st.Since = time.UnixMilli(int64(min(ms, uint64(1<<62))))
		}
		if _, held := m.subs[st.FollowKey]; !held {
			m.startLocked(st)
		}
	}
	return nil
}

func normalizeFollowKey(key FollowKey) FollowKey {
	key.Schema = syncwire.NormalizeSchema(key.Schema)
	key.ProviderID, key.SourceName, key.Producer = strings.TrimSpace(key.ProviderID), strings.TrimSpace(key.SourceName), strings.TrimSpace(key.Producer)
	return key
}

// checkSubscription is the lane and producer of a new subscription.
func (m *followManager) checkSubscription(key FollowKey) (peer.ID, error) {
	if key.Schema == "" || key.ProviderID == "" || key.SourceName == "" {
		return "", errors.New("a followed lane names a standard, a provider and a source")
	}
	pid, err := peer.Decode(key.Producer)
	if err != nil {
		return "", fmt.Errorf("producer peer id: %w", err)
	}
	return pid, m.n.followAdmitted(pid, key)
}

// Plan is a subscription's first read: the producer's statement, read now,
// and this node's free disk and record ceiling.
func (m *followManager) Plan(ctx context.Context, key FollowKey, opts FollowOptions) (FollowPlan, error) {
	key = normalizeFollowKey(key)
	pid, err := m.checkSubscription(key)
	if err != nil {
		return FollowPlan{}, err
	}
	plan := FollowPlan{FollowKey: key, Start: opts.Start, Rule: m.n.laneRetentionRule(key.Schema, key.ProviderID, key.SourceName)}
	switch plan.Start {
	case "":
		plan.Start = FollowStartBeginning
		if plan.Rule == channels.RetentionReplaceCurrent {
			plan.Start = FollowStartCurrent
		}
	case FollowStartCurrent, FollowStartBeginning:
	default:
		return FollowPlan{}, fmt.Errorf("a subscription starts at %q or %q, not %q", FollowStartCurrent, FollowStartBeginning, opts.Start)
	}
	plan.Statement, err = m.readStatement(ctx, pid, key)
	if err != nil {
		plan.StatementError = err.Error()
		plan.Refusal = fmt.Sprintf("the producer did not state the lane (%v); a producer before 1.2.0 does not, so its first read cannot be bounded or sized", err)
		if followNotAuthorized(err) {
			plan.Refusal = fmt.Sprintf("the producer did not state the lane (%v): it serves this standard's stream only to peers it grants private reads; with --force the subscription admits the lane's signed publications ($PNM)", err)
		}
		return plan, nil
	}
	plan.FirstReadRecords, plan.FirstReadBytes = plan.Statement.Records, plan.Statement.BatchBytes
	if plan.Start == FollowStartBeginning {
		plan.FirstReadRecords, plan.FirstReadBytes = plan.Statement.LaneRecords, plan.Statement.LaneBytes
	}
	plan.NeedBytes = plan.FirstReadBytes*followStoreExpansion + followDiskReserve
	plan.RecordCeiling = config.DefaultSubscriptionRecordCeiling
	if m.n.config != nil {
		plan.RecordCeiling = m.n.config.Subscriptions.EffectiveRecordCeiling()
	}
	if free, err := storage.FreeDiskBytes(filepath.Dir(m.path)); err == nil {
		plan.FreeBytes = int64(min(free, uint64(1<<62)))
	} else {
		return plan, fmt.Errorf("free disk under %s: %w", filepath.Dir(m.path), err)
	}
	if summary, err := m.n.store.DataSummary(); err == nil {
		for _, s := range summary.Schemas {
			plan.HeldRecords += s.Count
		}
	} else {
		return plan, fmt.Errorf("records held here: %w", err)
	}
	switch {
	case plan.FreeBytes < plan.NeedBytes:
		plan.Refusal = fmt.Sprintf("the first read is %d records, %d bytes; it needs about %d bytes of free disk (%d per record byte and 1 GiB to spare), and %d are free",
			plan.FirstReadRecords, plan.FirstReadBytes, plan.NeedBytes, followStoreExpansion, plan.FreeBytes)
	case plan.HeldRecords+plan.FirstReadRecords > plan.RecordCeiling:
		plan.Refusal = fmt.Sprintf("the first read is %d records, %d bytes; this node holds %d records and its ceiling is %d (subscriptions.record_ceiling)",
			plan.FirstReadRecords, plan.FirstReadBytes, plan.HeldRecords, plan.RecordCeiling)
	}
	return plan, nil
}

// Subscribe starts following a lane: the plan first, refused unless it fits
// or opts.Force; a lane already followed from the producer is left as it is.
func (m *followManager) Subscribe(ctx context.Context, key FollowKey, opts FollowOptions) (FollowPlan, error) {
	key = normalizeFollowKey(key)
	m.mu.Lock()
	e, held := m.subs[key]
	var st FollowStatus
	if held {
		st = e.status
	}
	m.mu.Unlock()
	if held {
		return FollowPlan{FollowKey: key, Start: st.Start}, nil
	}
	plan, err := m.Plan(ctx, key, opts)
	if err != nil {
		return plan, err
	}
	if plan.Refusal != "" && !opts.Force {
		return plan, errors.New(plan.Refusal)
	}
	if plan.StatementError != "" && plan.Start == FollowStartCurrent {
		// Forced: the follow reads the statement when the producer gives it.
		log.Warnf("Subscribed to %s %s/%s from %s without its statement (%s); it starts at the producer's current batch once the producer states it",
			key.Schema, key.ProviderID, key.SourceName, key.Producer, plan.StatementError)
	}
	st = FollowStatus{FollowKey: key, Since: time.Now().UTC()}
	if plan.Start == FollowStartCurrent {
		st.Start = FollowStartCurrent
		if plan.StatementError == "" {
			st.Start, st.Cursor = "", plan.Statement.ResumeCursor
		}
	}
	log.Infof("Subscribed to %s %s/%s from %s: starts at the %s; first read %d records, %d bytes (rule %s)%s", key.Schema, key.ProviderID, key.SourceName,
		key.Producer, map[string]string{FollowStartCurrent: "producer's current batch " + plan.Statement.BatchID, FollowStartBeginning: "lane's beginning"}[plan.Start],
		plan.FirstReadRecords, plan.FirstReadBytes, plan.Rule, map[bool]string{true: "; forced past: " + plan.Refusal}[plan.Refusal != ""])
	m.mu.Lock()
	if _, held := m.subs[key]; !held {
		m.startLocked(st)
	}
	m.mu.Unlock()
	return plan, m.save()
}

// adopt starts a subscription without a plan (a lane converted from the
// removed import ledger): it starts at the producer's current batch.
func (m *followManager) adopt(key FollowKey) bool {
	key = normalizeFollowKey(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.subs[key]; held {
		return false
	}
	m.startLocked(FollowStatus{FollowKey: key, Start: FollowStartCurrent, Since: time.Now().UTC()})
	return true
}

// readStatement reads the producer's statement of the lane.
func (m *followManager) readStatement(ctx context.Context, remote peer.ID, key FollowKey) (syncwire.LaneStatement, error) {
	ctx, cancel := context.WithTimeout(ctx, followStatementWait)
	defer cancel()
	stream, err := m.producerRead(ctx, remote, FollowStatus{FollowKey: key}, "", "", 1,
		syncwire.QRPFilterField{Field: syncwire.FilterProjection, Op: syncwire.QRPFilterEq, Value: syncwire.ProjectionStatement})
	if err != nil {
		return syncwire.LaneStatement{}, err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	return syncwire.ReadStatementPage(stream)
}

// Follow stops following a lane (on false; Subscribe starts one). Stopping
// with an empty producer stops every producer of the lane; with an empty
// provider and source, every followed lane of the standard.
func (m *followManager) Follow(key FollowKey, on bool) error {
	key = normalizeFollowKey(key)
	if key.Schema == "" {
		return errors.New("a followed lane names a standard")
	}
	if on {
		return errors.New("a lane is followed by Subscribe")
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
// this node trusts, not itself. The subscription itself is what admits the
// lane.
func (n *Node) followAdmitted(pid peer.ID, key FollowKey) error {
	if n.host != nil && pid == n.host.ID() {
		return errors.New("a node does not follow its own lanes")
	}
	if n.peerRegistry == nil || !n.peerRegistry.IsTrusted(pid) {
		return fmt.Errorf("producer %s is not a trusted peer of this node", pid)
	}
	return nil
}

// unsubscribedLaneLog remembers the lanes already reported as not imported.
var unsubscribedLaneLog sync.Map

// subscribedFrom reports whether this node subscribes to the lane from the
// peer: the one gate of every import (the followed stream and the $PNM
// path).
func (n *Node) subscribedFrom(from peer.ID, schema, providerID, sourceName string) bool {
	if n == nil || n.follows == nil {
		return false
	}
	key := normalizeFollowKey(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: from.String()})
	n.follows.mu.Lock()
	defer n.follows.mu.Unlock()
	_, ok := n.follows.subs[key]
	return ok
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
		wait := backoff
		if followNotAuthorized(err) {
			// The producer serves this standard's stream only to peers it
			// grants private reads. The subscription still admits the lane's
			// signed $PNM publications; the stream is asked again later.
			wait = followNotAuthorizedWait
			err = fmt.Errorf("%w; the producer serves this lane's stream only to peers it grants private reads, so the lane imports through its publications ($PNM) until it does", err)
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
		log.Warnf("Followed lane %s %s/%s from %s: %.1024q; reconnecting in %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, fmt.Sprint(err), wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
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

// followNotAuthorizedWait is the wait before a followed stream the producer
// refused as not authorized is asked again.
const followNotAuthorizedWait = 10 * time.Minute

// followNotAuthorized reports a producer that does not serve the stream to
// this node (a standard off its public read surface, and no private-read
// grant).
func followNotAuthorized(err error) bool {
	var remote *syncwire.RemoteError
	return errors.As(err, &remote) && remote.Code == "not_authorized"
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
	if st.Start == FollowStartCurrent && st.Cursor == "" {
		// The subscription starts at the producer's current batch: its
		// statement gives the present position to follow from.
		stmt, err := m.readStatement(ctx, pid, st.FollowKey)
		if err != nil {
			return 0, fmt.Errorf("read the producer's statement of the lane, to start at its current batch (a producer before 1.2.0 gives none; this lane starts once it does): %w", err)
		}
		m.mu.Lock()
		e.status.Start, e.status.Cursor = "", stmt.ResumeCursor
		st = e.status
		m.mu.Unlock()
		if err := m.save(); err != nil {
			return 0, fmt.Errorf("save cursor: %w", err)
		}
		log.Infof("Followed lane %s %s/%s from %s: starts at the producer's current batch %s; first read bounded to %d records, %d bytes (the lane holds %d records, %d bytes)",
			st.Schema, st.ProviderID, st.SourceName, st.Producer, stmt.BatchID, stmt.Records, stmt.BatchBytes, stmt.LaneRecords, stmt.LaneBytes)
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
		if stated := syncwire.StatementOf(end); stated.BatchID != "" {
			e.stated = stated
		}
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
			// A rule changed since the current batch was held applies now.
			m.n.datasetSupersedeMu.Lock()
			m.applyRules(false)
			m.n.datasetSupersedeMu.Unlock()
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
	e.current, e.held, e.rule = stmt, counts[stmt.BatchID], rule
	e.status.RemovedRecords += removed + superseded
	m.mu.Unlock()
	m.n.alerts.Clear(ops.KindFollowRefused, followAlertSubject(st.FollowKey))
	log.Infof("Followed lane %s %s/%s from %s: batch %s is current (%d records listed, %d tagged from held records, %d fetched by CID, %d removed that the batch no longer holds, %d removed with the superseded batches; rule %s) in %s",
		st.Schema, st.ProviderID, st.SourceName, remote, stmt.BatchID, len(list), got-fetched, fetched, removed, superseded, rule,
		time.Since(began).Round(time.Millisecond))
	return nil
}

// applyRules applies each followed lane's rule to the batch it holds as the
// producer's current one, for the lanes whose rule changed since it was
// last applied (all of them when every is set): replace-current removes
// what only the producer's other batches hold. A lane whose current batch is
// not known yet (before its first quiet page) gets its rule when the batch
// is held. The caller holds n.datasetSupersedeMu. It returns the lanes it
// checked.
func (m *followManager) applyRules(every bool) int {
	m.mu.Lock()
	type due struct {
		e     *followEntry
		key   FollowKey
		batch string
	}
	var lanes []due
	for k, e := range m.subs {
		if e.current.BatchID != "" {
			lanes = append(lanes, due{e, k, e.current.BatchID})
		}
	}
	m.mu.Unlock()
	checked := 0
	for _, l := range lanes {
		rule := m.n.laneRetentionRule(l.key.Schema, l.key.ProviderID, l.key.SourceName)
		m.mu.Lock()
		changed := l.e.rule != rule
		m.mu.Unlock()
		if !every && !changed {
			continue
		}
		checked++
		var removed int64
		if rule == channels.RetentionReplaceCurrent && !m.n.laneProducedHere(l.key.Schema, l.key.ProviderID, l.key.SourceName) {
			var err error
			removed, err = m.n.store.SupersedeFollowLane(l.key.Schema, l.key.ProviderID, l.key.SourceName, l.key.Producer, l.batch)
			if err != nil {
				log.Warnf("Followed lane %s %s/%s from %s: rule %s not applied: %v", l.key.Schema, l.key.ProviderID, l.key.SourceName, l.key.Producer, rule, err)
				continue
			}
		}
		m.mu.Lock()
		l.e.rule = rule
		l.e.status.RemovedRecords += removed
		m.mu.Unlock()
		if removed > 0 || changed {
			log.Infof("Followed lane %s %s/%s from %s: rule %s applied to current batch %s; %d records removed", l.key.Schema, l.key.ProviderID,
				l.key.SourceName, l.key.Producer, rule, l.batch, removed)
		}
	}
	return checked
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
	runs, _, received, err := syncwire.ReadFollowPage(stream, len(cids))
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
		e.status.ReceivedBytes += received
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
		st.CurrentBatch, st.Stated = e.current.BatchID, e.stated
		switch {
		case e.stated.BatchID == "":
		case e.stated.BatchID == e.current.BatchID:
			st.Lag = max(e.stated.Records-e.held, 0)
		default:
			st.Lag = e.stated.Records
		}
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
	pending := flatbuffers.UOffsetT(0)
	if st.Start == FollowStartCurrent && st.Cursor == "" {
		pending = b.CreateString(followStartPending)
	}
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
	if pending != 0 {
		DSS.DSSAddSYNC_FILTER(b, pending)
	}
	if !st.Since.IsZero() {
		DSS.DSSAddLAST_SYNC_STARTED_AT(b, uint64(max(st.Since.UnixMilli(), 0)))
	}
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

// errNoFollows is a node without a store to follow lanes into.
var errNoFollows = errors.New("this node has no store to follow lanes into")

// Unfollow stops following a lane (see followManager.Follow for the empty
// fields).
func (n *Node) Unfollow(schema, providerID, sourceName, producer string) error {
	if n.follows == nil {
		return errNoFollows
	}
	return n.follows.Follow(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: producer}, false)
}

// Subscribe follows a lane from a producer after checking its first read.
func (n *Node) Subscribe(ctx context.Context, key FollowKey, opts FollowOptions) (FollowPlan, error) {
	if n.follows == nil {
		return FollowPlan{}, errNoFollows
	}
	return n.follows.Subscribe(ctx, key, opts)
}

// PlanSubscription is a subscription's first read, without subscribing.
func (n *Node) PlanSubscription(ctx context.Context, key FollowKey, opts FollowOptions) (FollowPlan, error) {
	if n.follows == nil {
		return FollowPlan{}, errNoFollows
	}
	return n.follows.Plan(ctx, key, opts)
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
