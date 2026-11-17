package node

// Followed lanes: a subscription names a lane (standard, provider, source)
// and the producer peer to follow. The node opens a followed read-chunk
// stream to the producer (syncwire/follow.go), stores each page's records,
// and only after the store has returned saves the page's cursor and grants
// the next page. A restart resumes at the saved cursor; a record sent again
// is the same content address and is stored once.
//
// Subscriptions are kept on disk as size-prefixed $DSS records, one per
// followed lane (followFileName): the lane, the producer (PROVIDER_PEER_ID),
// the cursor to resume at (CURSOR: the scan's resume cursor; NEXT_CURSOR: a
// continuation within a page run) and what has been received.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// followFileName is the followed lanes' file in the storage directory.
const followFileName = "follows.dss"

// followPageLimit is the records a followed page holds at most.
const followPageLimit = 4096

// followReadWait bounds the wait for one followed page; the producer sends
// an empty page after a quiet interval, so a longer silence is a dead stream.
const followReadWait = 2 * time.Minute

// FollowKey names one followed lane.
type FollowKey struct {
	Schema, ProviderID, SourceName, Producer string
}

// FollowStatus is one followed lane as this node has received it.
type FollowStatus struct {
	FollowKey
	Cursor, NextCursor    string
	ReceivedBytes         int64
	ReceivedRecords       int64
	StoredRecords         int64
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
	for _, frame := range frames {
		if !DSS.SizePrefixedDSSBufferHasIdentifier(frame) {
			return fmt.Errorf("%s: a frame that is not a $DSS record", m.path)
		}
		d := DSS.GetSizePrefixedRootAsDSS(frame, 0)
		st := FollowStatus{FollowKey: FollowKey{Schema: syncwire.NormalizeSchema(string(d.SCHEMA_NAME())), ProviderID: string(d.PROVIDER_ID()),
			SourceName: string(d.SOURCE_NAME()), Producer: string(d.PROVIDER_PEER_ID())},
			Cursor: string(d.CURSOR()), NextCursor: string(d.NEXT_CURSOR()),
			ReceivedBytes: int64(d.DOWNLOADED_BYTES()), StoredRecords: int64(d.LOCAL_ROWS()), ReceivedRecords: int64(d.SYNCED_ROWS())}
		if st.Schema == "" || st.Producer == "" || !d.SUBSCRIBED() {
			continue
		}
		m.start(st)
	}
	return nil
}

// Follow starts following a lane from a producer (on) or stops it (off).
// Stopping with an empty producer stops every producer of the lane.
func (m *followManager) Follow(key FollowKey, on bool) error {
	key.Schema = syncwire.NormalizeSchema(key.Schema)
	key.ProviderID, key.SourceName, key.Producer = strings.TrimSpace(key.ProviderID), strings.TrimSpace(key.SourceName), strings.TrimSpace(key.Producer)
	if key.Schema == "" || key.ProviderID == "" || key.SourceName == "" {
		return errors.New("a followed lane names a standard, a provider and a source")
	}
	if on {
		if _, err := peer.Decode(key.Producer); err != nil {
			return fmt.Errorf("producer peer id: %w", err)
		}
		m.mu.Lock()
		_, held := m.subs[key]
		m.mu.Unlock()
		if !held {
			m.start(FollowStatus{FollowKey: key})
		}
		return m.save()
	}
	var stopped []*followEntry
	m.mu.Lock()
	for k, e := range m.subs {
		if k.Schema == key.Schema && k.ProviderID == key.ProviderID && k.SourceName == key.SourceName && (key.Producer == "" || k.Producer == key.Producer) {
			stopped = append(stopped, e)
			delete(m.subs, k)
		}
	}
	m.mu.Unlock()
	for _, e := range stopped {
		e.cancel()
		<-e.done
	}
	return m.save()
}

func (m *followManager) start(st FollowStatus) {
	ctx, cancel := context.WithCancel(m.n.ctx)
	e := &followEntry{status: st, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.subs[st.FollowKey] = e
	m.mu.Unlock()
	m.n.wg.Add(1)
	go func() {
		defer m.n.wg.Done()
		defer close(e.done)
		m.run(ctx, e)
	}()
}

// run follows one lane until ctx ends, reconnecting after a failure.
func (m *followManager) run(ctx context.Context, e *followEntry) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := m.followOnce(ctx, e)
		if ctx.Err() != nil {
			return
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

// followOnce runs one followed stream until it fails or ctx ends.
func (m *followManager) followOnce(ctx context.Context, e *followEntry) error {
	m.mu.Lock()
	st := e.status
	m.mu.Unlock()
	pid, err := peer.Decode(st.Producer)
	if err != nil {
		return err
	}
	stream, err := m.n.host.NewStream(ctx, pid, libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)))
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	req := syncwire.QRPFields{SchemaName: st.Schema, ProviderID: st.ProviderID, SourceName: st.SourceName, Limit: followPageLimit, Follow: true}
	if st.NextCursor != "" {
		req.Cursor = st.NextCursor
	} else {
		req.ResumeCursor = st.Cursor
	}
	if err := syncwire.Request(stream, req); err != nil {
		_ = stream.Reset()
		return fmt.Errorf("write request: %w", err)
	}
	log.Infof("Followed lane %s %s/%s from %s: stream open", st.Schema, st.ProviderID, st.SourceName, st.Producer)
	for {
		_ = stream.SetReadDeadline(time.Now().Add(followReadWait))
		began := time.Now()
		runs, end, received, err := syncwire.ReadFollowPage(stream)
		if err != nil {
			var remote *syncwire.RemoteError
			if errors.As(err, &remote) && strings.Contains(remote.Code, "cursor") {
				// The producer no longer accepts the saved cursor: read the
				// lane again from its start; records already held are
				// stored once.
				m.resetCursor(e)
			}
			_ = stream.Reset()
			return err
		}
		transfer := time.Since(began)
		verify, store, records, stored, err := m.storePage(ctx, st, runs)
		if err != nil {
			_ = stream.Reset()
			return err
		}
		m.mu.Lock()
		e.status.Connected, e.status.LastError = true, ""
		if next := string(end.NEXT_CURSOR()); next != "" {
			e.status.NextCursor, e.status.Cursor = next, ""
		} else {
			e.status.NextCursor, e.status.Cursor = "", string(end.RESUME_CURSOR())
		}
		e.status.Pages++
		e.status.ReceivedBytes += received
		e.status.ReceivedRecords += int64(records)
		e.status.StoredRecords += int64(stored)
		e.status.TransferMs += transfer.Milliseconds()
		e.status.VerifyMs += verify.Milliseconds()
		e.status.StoreMs += store.Milliseconds()
		now := time.Now()
		if records > 0 {
			e.status.LastReceivedAt = now
		}
		e.samples = append(e.samples, followRate{at: now, bytes: received})
		m.mu.Unlock()
		if records > 0 || end.RESUME_CURSOR() != nil {
			if err := m.save(); err != nil {
				_ = stream.Reset()
				return fmt.Errorf("save cursor: %w", err)
			}
		}
		if err := syncwire.Grant(stream, st.Schema); err != nil {
			_ = stream.Reset()
			return fmt.Errorf("grant: %w", err)
		}
	}
}

// storePage verifies each record of a page against its standard and stores
// each run's records under the run's lane tags. It returns when the store
// has returned.
func (m *followManager) storePage(ctx context.Context, st FollowStatus, runs []syncwire.FollowRun) (verify, store time.Duration, records, stored int, err error) {
	for _, run := range runs {
		h := run.Header
		schema := syncwire.NormalizeSchema(string(h.SCHEMA_NAME()))
		if schema != st.Schema || string(h.PROVIDER_ID()) != st.ProviderID || string(h.SOURCE_NAME()) != st.SourceName {
			return verify, store, records, stored, fmt.Errorf("the producer sent a run of %s %s/%s on a stream of %s %s/%s",
				schema, h.PROVIDER_ID(), h.SOURCE_NAME(), st.Schema, st.ProviderID, st.SourceName)
		}
		began := time.Now()
		data := make([][]byte, 0, len(run.Records))
		for _, frame := range run.Records {
			body := frame[syncwire.FramePrefixLength:]
			if m.n.validator != nil {
				if err := m.n.validator.Validate(ctx, schema, body); err != nil {
					return verify, store, records, stored, fmt.Errorf("record of %s fails its standard: %w", schema, err)
				}
			}
			data = append(data, body)
		}
		verify += time.Since(began)
		origin := strings.TrimSpace(string(h.ORIGIN_ID()))
		if origin == "" {
			origin = st.Producer
		}
		tags := storage.SourceTags{ProviderID: st.ProviderID, SourceName: st.SourceName, BatchID: string(h.BATCH_ID()), ProducerPeerID: string(h.PRODUCER_PEER_ID())}
		began = time.Now()
		n, err := m.n.store.StoreBatchWithSourceTags(schema, data, origin, nil, tags)
		store += time.Since(began)
		records += len(data)
		stored += n
		if err != nil {
			return verify, store, records, stored, fmt.Errorf("store %d records of %s: %w", len(data), schema, err)
		}
	}
	return verify, store, records, stored, nil
}

func (m *followManager) resetCursor(e *followEntry) {
	m.mu.Lock()
	e.status.Cursor, e.status.NextCursor = "", ""
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
	next := str(st.NextCursor)
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
	if next != 0 {
		DSS.DSSAddNEXT_CURSOR(b, next)
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
