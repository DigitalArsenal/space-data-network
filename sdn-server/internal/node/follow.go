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
// followed lane: the lane, the producer (PROVIDER_PEER_ID), the cursor to
// resume at (CURSOR, a resume cursor the producer's scan generation
// verifies), where the subscription starts (SYNC_FILTER "start=current" or
// "start=beginning"), when it was subscribed (LAST_SYNC_STARTED_AT, Unix ms)
// and what has been received. Every subscription is in subscriptionsFileName.
// followFileName, the file nodes up to 1.1.9 read and write, holds only the
// subscriptions that start at the lane's beginning: 1.1.9 follows each record
// of that file from its cursor, and from the lane's beginning when it has
// none, so a subscription bounded to the producer's current batch is never
// in it, and a node rolled back to 1.1.9 does not read such a lane from its
// beginning. At load, a record of followFileName that subscriptionsFileName
// lacks (a 1.1.9 follow) is a subscription that starts at the beginning; for
// one both files hold, followFileName's cursor is the newer (1.1.9 ran last);
// a lane only subscriptionsFileName holds is kept as it holds it. Writes go
// to followFileName first, so a crash between the two writes cannot leave
// an unsubscribed lane in followFileName alone. An unsubscribe made under
// 1.1.9 (during a rollback) removes the lane from followFileName only, so
// the lane comes back with the next 1.2.0 start. A file that does not read
// at start is moved aside, never overwritten: no lane runs and no
// subscription changes until a readable copy is in place (alert
// subscriptions_unreadable).
//
// The first read is bounded by the lane's rule. A subscription starts at the
// producer's current batch (FollowStartCurrent) or at the lane's beginning
// (FollowStartBeginning); by default a replace-current lane starts at the
// current batch and a keep-all or archive-all lane at the beginning, and a
// subscription forced past a producer that does not state the lane starts at
// the current batch unless the beginning is asked for. To start at the
// current batch the node first reads the producer's statement of the lane
// (syncwire.LaneStatement: the current batch, the batch's and the lane's
// size, and the lane's present position as a resume cursor), saves that
// position as its cursor, and opens the followed stream there: the stream's
// first page is empty, and the current batch is then held as above (its list
// read, its records fetched by CID). Nothing older is sent. Before a
// subscription starts the node checks the first read (the batch, or the
// lane) against its free disk and its record ceiling (FollowPlan); a refusal
// names both sizes and can be overridden (FollowOptions.Force).
//
// No path reads a lane from its beginning that the operator did not
// subscribe to from its beginning. A producer that refuses the saved cursor
// (its scan generation turned: a rebuilt index, a store re-derived from its
// journals, a wiped store) gets the subscription started again at its
// current batch, with the alert follow_cursor_refused. For a lane whose
// history this node keeps (subscribed from its beginning, or a keep-all or
// archive-all lane) the records stored between the last position held and
// the current batch are not read: that is kept with the subscription
// (FollowStatus.Unread) until the operator subscribes from the beginning
// again or acknowledges it.
//
// While a lane's stream is connected its $PNM publications are not imported
// (their records arrive on the stream), but only once the batch the producer
// states is held as current: a lane whose current batch cannot be held gets
// it through its publications.
//
// The running guard: before each page is stored, and before each fetch by
// CID (for the most bytes one page can hold), the node reserves the page's
// store bytes and records in one reservation shared by every lane, and
// checks that the free disk less the reservation stays above
// followDiskReserve and the records held and reserved within its record
// ceiling. A page that does not fit stops the lane (alert follow_stopped, the
// cursor kept) and the lane is checked again every followStoppedWait. Force
// skips only the check before the subscription.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
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

// followFileName is the file of the subscriptions that start at the lane's
// beginning, in the form 1.1.9 reads; subscriptionsFileName every
// subscription. Both are in the directory that holds storage.path.
const (
	followFileName        = "follows.dss"
	subscriptionsFileName = "subscriptions.dss"
)

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
)

// followStoppedWait is the wait before a lane the running guard stopped is
// checked again.
const followStoppedWait = 5 * time.Minute

// followHeldRefresh is how old the count of records held may be before the
// running guard reads it from the store again.
const followHeldRefresh = 30 * time.Second

// errFollowNotAdmitted marks a producer this node does not trust;
// followNotAdmittedWait is the wait before its trust is checked again.
var errFollowNotAdmitted = errors.New("not admitted")

const followNotAdmittedWait = 2 * time.Minute

// errFollowStopped marks a page the running guard did not store.
var errFollowStopped = errors.New("stopped before the store passes its limit")

// followStoreExpansion is the store bytes a record byte takes (5.0 to 5.3
// measured, stream-790 Part A); followDiskReserve the free disk a first
// read leaves.
const (
	followStoreExpansion = 5
	followDiskReserve    = 1 << 30
)

// followStatementWait bounds one read of a producer's statement: long
// enough for a producer before 1.1.13, whose statement reads every record of
// the lane twice and goes on after the reader gives up, to answer once
// instead of being asked again (a producer from 1.1.13 answers from its
// counters).
const followStatementWait = 5 * time.Minute

// The wait before a failed followed stream is opened again: it doubles from
// followBackoffMin after each connection that delivered no page, up to
// followBackoffMax, each wait drawn within followBackoffJitter of it, and
// starts again at followBackoffMin after a connection that delivered a page.
// A busy refusal is waited at least its retry hint.
var (
	followBackoffMin    = time.Second
	followBackoffMax    = 20 * time.Minute
	followBackoffJitter = 0.2
)

// followBackoffWait is the wait drawn for backoff.
func followBackoffWait(backoff time.Duration) time.Duration {
	f := 1 + followBackoffJitter*(2*rand.Float64()-1)
	return time.Duration(float64(backoff) * f)
}

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
	// Rule is the retention rule the subscription sets on the lane ("": the
	// lane's rule as it is); it decides the default start.
	Rule string
	// AcknowledgeUnread clears what a refused cursor left unread
	// (FollowStatus.Unread) on the existing subscription; it subscribes
	// nothing.
	AcknowledgeUnread bool
	// Publications is what the node keeps of the lane's publications:
	// FollowPublicationsKeep (FollowStatus.KeepPublications),
	// FollowPublicationsStream, or "" (a new subscription streams; an
	// existing one keeps what it has). On a lane already followed it changes
	// that alone.
	Publications string
}

// What a subscription keeps of its lane's publications (FollowOptions.Publications).
const (
	// FollowPublicationsStream: the records arrive on the followed stream,
	// and while the stream holds the producer's current batch the lane's
	// publications are not fetched.
	FollowPublicationsStream = "stream"
	// FollowPublicationsKeep: every publication of the lane is also fetched
	// and imported, so this node holds its manifest, shard and index and
	// serves them (a gateway host whose readers fetch the producer's
	// publications from it); the lane's retention rule decides which
	// publications stay.
	FollowPublicationsKeep = "keep"
)

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
	// ReservedRecords and ReservedBytes are what the followed lanes are
	// storing now (the running guard's reservation; ReservedBytes are store
	// bytes, not counted in FreeBytes).
	ReservedRecords, ReservedBytes int64
	// Refusal is why the first read does not fit ("" when it does).
	Refusal string
}

// FollowStatus is one followed lane as this node has received it.
type FollowStatus struct {
	FollowKey
	// Start is where the subscription starts: FollowStartCurrent (it waits
	// for the producer's statement while it has no cursor) or
	// FollowStartBeginning.
	Start string
	// Stopped is why the running guard stopped the lane ("" while it runs).
	Stopped string
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
	// Retracted is true once the producer stated the lane retracted: this
	// node dropped the records it held of the lane from that producer.
	Retracted bool
	// Unread is when the producer refused the saved cursor of a lane whose
	// history this node keeps (a keep-all or archive-all lane, or one
	// subscribed from its beginning): the records the producer stored
	// between the last position held and its current batch then were not
	// read. It is kept with the subscription until the operator subscribes
	// to the lane again from its beginning or acknowledges it.
	Unread time.Time
	// FromBeginning marks a subscription made from the lane's beginning that
	// a refused cursor started again at the current batch: it stays in
	// followFileName while Unread is set.
	FromBeginning bool
	// KeepPublications is set when the subscription keeps the lane's
	// publications (FollowPublicationsKeep): each is fetched and imported
	// although the stream carries its records.
	KeepPublications bool
	// Refused is why this build does not run the subscription ("" when it
	// does): its SYNC_FILTER names a field this build does not know (a later
	// version wrote it). The lane is not followed and its publications are
	// not imported; the record is written back as it was read (filter, and
	// followFileName membership as loaded), so the later version finds it
	// again.
	Refused string
	// filter is the SYNC_FILTER of a refused subscription as read;
	// inOldFile that followFileName held it at load.
	filter    string
	inOldFile bool
}

// inFollowFile reports a subscription followFileName holds: one that starts
// at the lane's beginning, or one made so whose refused cursor is not
// acknowledged. A refused subscription stays where it was loaded from.
func (st FollowStatus) inFollowFile() bool {
	if st.Refused != "" {
		return st.inOldFile
	}
	return st.Start == FollowStartBeginning || (!st.Unread.IsZero() && st.FromBeginning)
}

// UnreadText says what a refused cursor left unread ("" when nothing).
func (st FollowStatus) UnreadText() string {
	if st.Unread.IsZero() {
		return ""
	}
	return fmt.Sprintf("the producer refused the saved cursor at %s; the records it stored between the last position held and its current batch at that time are not read (to read the whole lane: sync subscribe --start beginning; to accept the gap: sync subscribe --acknowledge-unread)",
		st.Unread.UTC().Format(time.RFC3339))
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
	// batchHeld is true once the batch the producer states is held as
	// current, and false again when holding a stated batch fails: until it
	// is true the lane's $PNM publications are imported although its stream
	// is connected (Node.followStreaming).
	batchHeld bool
	// settled is true once the lane's stream has had an outcome since the
	// lane started: a stated batch held or not held, the stream refused as
	// not authorized, or the lane stopped by the running guard. Until then,
	// for followPublicationWait from started, the lane's $PNM publications
	// wait (Node.followPublication); deferred marks one that waited, so the
	// stored-$PNM catch-up runs again once the lane settles.
	settled  bool
	started  time.Time
	deferred bool
}

// followPublicationWait bounds the wait of a lane's publications for its
// stream to settle after the lane starts: a lane whose stream does not
// connect in that time imports its publications.
const followPublicationWait = 5 * time.Minute

// settledLocked marks e's stream settled; the caller holds m.mu.
func (m *followManager) settledLocked(e *followEntry) {
	e.settled = true
	if e.deferred {
		e.deferred = false
		m.nudgeCatchup()
	}
}

// nudgeCatchup asks the stored-$PNM catch-up to run now.
func (m *followManager) nudgeCatchup() {
	select {
	case m.catchupNudge <- struct{}{}:
	default:
	}
}

// keepPublicationsStarted is a lane that now keeps its publications: the
// publications this node passed over (their lane not subscribed, or
// streamed) are read again by the stored-$PNM catch-up, now.
func (m *followManager) keepPublicationsStarted() {
	if m.n != nil {
		m.n.forgetDeclinedDatasetPNMs()
	}
	m.nudgeCatchup()
}

// What the $PNM path does with a publication of a subscribed lane
// (Node.followPublication).
const (
	// followPublicationImport: the publication is imported.
	followPublicationImport = iota
	// followPublicationStreamed: the stream is connected and holds the
	// batch the producer states as current; it carries the records.
	followPublicationStreamed
	// followPublicationDeferred: the lane started less than
	// followPublicationWait ago and its stream has not settled; the
	// publication is left for the stored-$PNM catch-up.
	followPublicationDeferred
)

type followRate struct {
	at    time.Time
	bytes int64
}

const followRateWindow = 10 * time.Second

// followManager runs the node's followed lanes.
type followManager struct {
	n *Node
	// path is followFileName's path, subsPath subscriptionsFileName's.
	path, subsPath string
	mu             sync.Mutex
	subs           map[FollowKey]*followEntry
	// saveMu serializes writes of the files.
	saveMu sync.Mutex
	// heldMu guards the running guard's count of the records this node
	// holds (read from the store at heldAt, plus what follows stored since),
	// and the records and store bytes every lane has been granted and not
	// yet stored (reserveRecords, reserveBytes): one reservation for all
	// lanes, so lanes storing together stay within the free disk reserve and
	// the record ceiling.
	heldMu         sync.Mutex
	held           int64
	heldAt         time.Time
	reserveRecords int64
	reserveBytes   int64
	// catchupNudge asks the stored-$PNM catch-up to run now (a lane whose
	// publications waited for its stream settled).
	catchupNudge chan struct{}
	// blocked is why the subscription files are not loaded ("" once they
	// are): while it is set nothing writes them and no lane runs.
	blocked string
	// peered is the producers peerProducer has seen (guarded by mu): true
	// for one this node added to Kubo's peering service, false for one that
	// was peered already (the repository's Peering.Peers), which it never
	// removes.
	peered map[peer.ID]bool
}

// peerProducer keeps a followed lane's producer in Kubo's peering service
// when this node has no content routing (its DHT off). Bitswap's broadcast
// control asks only peers that served this node before, LAN peers and
// peered peers for a block, so without it the node's own fetches by CID
// (a kept publication's shard) and its gateway's never reach the producer
// (host-01 and host-02, 2026-11-21). A node with a DHT finds the producer
// as a provider and needs none of this.
func (m *followManager) peerProducer(pid peer.ID) {
	if m.n == nil || m.n.kubo == nil || m.n.host == nil || m.n.dhtParticipation() != dhtParticipationOff {
		return
	}
	m.mu.Lock()
	if m.peered == nil {
		m.peered = map[peer.ID]bool{}
	}
	_, seen := m.peered[pid]
	if !seen {
		m.peered[pid] = false
	}
	m.mu.Unlock()
	if seen {
		return
	}
	if m.n.kubo.Peer(peer.AddrInfo{ID: pid, Addrs: m.n.host.Peerstore().Addrs(pid)}) {
		m.mu.Lock()
		m.peered[pid] = true
		m.mu.Unlock()
		log.Infof("Followed producer %s peered in Kubo: this node's DHT is off, and Bitswap asks only peered, LAN and earlier serving peers for a block", pid)
	}
}

// unpeerProducers removes from Kubo's peering service each producer in
// stopped that peerProducer added and no remaining subscription follows.
func (m *followManager) unpeerProducers(stopped []*followEntry) {
	if m.n == nil || m.n.kubo == nil {
		return
	}
	var drop []peer.ID
	m.mu.Lock()
	for _, e := range stopped {
		pid, err := peer.Decode(e.status.Producer)
		if err != nil || !m.peered[pid] {
			continue
		}
		still := false
		for k := range m.subs {
			if k.Producer == e.status.Producer {
				still = true
				break
			}
		}
		if !still {
			delete(m.peered, pid)
			drop = append(drop, pid)
		}
	}
	m.mu.Unlock()
	for _, pid := range drop {
		m.n.kubo.Unpeer(pid)
		log.Infof("Producer %s removed from Kubo's peering: no lane is followed from it", pid)
	}
}

// storeDir is where the followed records are stored (storage.path), whose
// free disk the size check and the running guard read; the directory of the
// subscription files when the node has no storage path.
func (m *followManager) storeDir() string {
	if m.n.config != nil && m.n.config.Storage.Path != "" {
		return m.n.config.Storage.Path
	}
	return filepath.Dir(m.path)
}

func newFollowManager(n *Node, dir string) *followManager {
	return &followManager{n: n, path: filepath.Join(dir, followFileName), subsPath: filepath.Join(dir, subscriptionsFileName),
		subs: map[FollowKey]*followEntry{}, catchupNudge: make(chan struct{}, 1)}
}

// errFollowFileDecode marks a subscriptions file whose bytes do not decode
// (as opposed to a read that failed: permission, I/O, a directory).
var errFollowFileDecode = errors.New("does not decode as subscriptions")

// readFollowFile reads one subscriptions file (none: no records). A record
// with no start in SYNC_FILTER starts at the beginning (followFileName's
// records, as 1.1.9 wrote them). A record whose SYNC_FILTER names a field
// this build does not know is returned refused (FollowStatus.Refused), not
// as an error. Bytes that do not decode are errFollowFileDecode; a read that
// fails is its own error.
func readFollowFile(path string) ([]FollowStatus, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		return nil, fmt.Errorf("%s %w: %w", path, errFollowFileDecode, err)
	}
	var out []FollowStatus
	for _, frame := range frames {
		if !DSS.SizePrefixedDSSBufferHasIdentifier(frame) {
			return nil, fmt.Errorf("%s %w: a frame that is not a $DSS record", path, errFollowFileDecode)
		}
		d := DSS.GetSizePrefixedRootAsDSS(frame, 0)
		st := FollowStatus{FollowKey: normalizeFollowKey(FollowKey{Schema: string(d.SCHEMA_NAME()), ProviderID: string(d.PROVIDER_ID()),
			SourceName: string(d.SOURCE_NAME()), Producer: string(d.PROVIDER_PEER_ID())}),
			Start:         FollowStartBeginning,
			Cursor:        string(d.CURSOR()),
			ReceivedBytes: int64(d.DOWNLOADED_BYTES()), StoredRecords: int64(d.LOCAL_ROWS()), ReceivedRecords: int64(d.SYNCED_ROWS())}
		if st.Schema == "" || st.Producer == "" || !d.SUBSCRIBED() {
			continue
		}
		var unknown *followFilterUnknownError
		if err := parseFollowFilter(string(d.SYNC_FILTER()), &st); errors.As(err, &unknown) {
			st.Refused = fmt.Sprintf("this build does not run the subscription: its SYNC_FILTER (in %s) names %s, which this build does not know (a later version wrote it); the lane is not followed and its publications are not imported, and the record is kept as it is",
				filepath.Base(path), unknown.names())
			st.filter = string(d.SYNC_FILTER())
		} else if err != nil {
			return nil, fmt.Errorf("%s %w: subscription %s %s/%s from %s: %w", path, errFollowFileDecode, st.Schema, st.ProviderID, st.SourceName, st.Producer, err)
		}
		if ms := d.LAST_SYNC_STARTED_AT(); ms > 0 {
			st.Since = time.UnixMilli(int64(min(ms, uint64(1<<62))))
		}
		out = append(out, st)
	}
	return out, nil
}

// Keys of a subscription's SYNC_FILTER (a URL query): where it starts, when a
// refused cursor left records unread, that it was made from the lane's
// beginning, and that it keeps the lane's publications. A build before
// 1.1.11 does not know "publications" and holds such a subscription without
// running it (FollowStatus.Refused).
const (
	followFilterStart        = "start"
	followFilterUnread       = "unread"
	followFilterSubscribed   = "subscribed"
	followFilterPublications = "publications"
)

// followFilterUnknownError is a SYNC_FILTER naming fields this build does not
// know.
type followFilterUnknownError struct{ keys []string }

func (e *followFilterUnknownError) names() string {
	quoted := make([]string, len(e.keys))
	for i, k := range e.keys {
		quoted[i] = strconv.Quote(k)
	}
	return strings.Join(quoted, ", ")
}

func (e *followFilterUnknownError) Error() string {
	return "SYNC_FILTER names " + e.names() + ", not a subscription field of this build"
}

// parseFollowFilter reads a subscription's SYNC_FILTER into st ("" starts at
// the beginning: followFileName's records, as 1.1.9 wrote them). Fields this
// build does not know are a *followFilterUnknownError (after the known ones
// are read); a known field that does not read is an error.
func parseFollowFilter(filter string, st *FollowStatus) error {
	if filter == "" {
		return nil
	}
	q, err := url.ParseQuery(filter)
	if err != nil {
		return fmt.Errorf("SYNC_FILTER %q: %w", filter, err)
	}
	var unknown []string
	for k := range q {
		switch k {
		case followFilterStart, followFilterUnread, followFilterSubscribed, followFilterPublications:
		default:
			unknown = append(unknown, k)
		}
	}
	for k, v := range q {
		if slices.Contains(unknown, k) {
			continue
		}
		if len(v) != 1 {
			return fmt.Errorf("SYNC_FILTER %q names %s %d times", filter, k, len(v))
		}
		switch k {
		case followFilterStart:
			switch v[0] {
			case FollowStartBeginning, FollowStartCurrent:
				st.Start = v[0]
			default:
				return fmt.Errorf("SYNC_FILTER %q: start %q is not a start", filter, v[0])
			}
		case followFilterUnread:
			at, err := time.Parse(time.RFC3339Nano, v[0])
			if err != nil {
				return fmt.Errorf("SYNC_FILTER %q: unread: %w", filter, err)
			}
			st.Unread = at
		case followFilterSubscribed:
			if v[0] != FollowStartBeginning {
				return fmt.Errorf("SYNC_FILTER %q: subscribed %q is not %q", filter, v[0], FollowStartBeginning)
			}
			st.FromBeginning = true
		case followFilterPublications:
			if v[0] != FollowPublicationsKeep {
				return fmt.Errorf("SYNC_FILTER %q: publications %q is not %q", filter, v[0], FollowPublicationsKeep)
			}
			st.KeepPublications = true
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &followFilterUnknownError{keys: unknown}
	}
	if _, ok := q[followFilterStart]; !ok {
		return fmt.Errorf("SYNC_FILTER %q names no start", filter)
	}
	return nil
}

// followFilter is a subscription's SYNC_FILTER (a refused one's as read).
func followFilter(st FollowStatus) string {
	if st.Refused != "" {
		return st.filter
	}
	q := url.Values{followFilterStart: {st.Start}}
	if st.KeepPublications {
		q.Set(followFilterPublications, FollowPublicationsKeep)
	}
	if !st.Unread.IsZero() {
		q.Set(followFilterUnread, st.Unread.UTC().Format(time.RFC3339Nano))
		if st.FromBeginning {
			q.Set(followFilterSubscribed, FollowStartBeginning)
		}
	}
	return q.Encode()
}

// followUnreadableSuffix is the suffix a subscriptions file that does not
// read is moved aside to (followed by the UTC time).
const followUnreadableSuffix = ".unreadable-"

// readFollowFileOrSetAside reads one subscriptions file. One whose bytes do
// not decode is moved aside (never overwritten) and named in the error; one
// whose read fails (permission, I/O, a directory in its place) is left where
// it is, named with the error, and read again at the next start. With none
// at path, a file moved aside earlier is still an error: the subscriptions
// it held are not "none".
func readFollowFileOrSetAside(path string) ([]FollowStatus, error) {
	subs, err := readFollowFile(path)
	if err == nil {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			if aside, _ := filepath.Glob(path + followUnreadableSuffix + "*"); len(aside) > 0 {
				sort.Strings(aside)
				return nil, fmt.Errorf("%s is missing and %s holds the subscriptions it held when it did not read; put a readable copy at %s, or remove %s to start with none",
					path, aside[len(aside)-1], path, strings.Join(aside, ", "))
			}
		}
		return subs, nil
	}
	if !errors.Is(err, errFollowFileDecode) {
		return nil, fmt.Errorf("%s did not read (%w); it is left in place and read again at the next start", path, err)
	}
	aside := path + followUnreadableSuffix + time.Now().UTC().Format("20060102T150405.000000000Z")
	if renameErr := os.Rename(path, aside); renameErr != nil {
		return nil, fmt.Errorf("%w; not moved aside: %v", err, renameErr)
	}
	return nil, fmt.Errorf("%w; moved to %s: put a readable copy at %s, or remove %s to start with none", err, aside, path, aside)
}

// load reads the subscriptions (see the package comment for the two files)
// and starts each lane. A file that does not read loads nothing: one whose
// bytes do not decode is moved aside, one whose read fails is left in place;
// the alert subscriptions_unreadable names it, no lane runs and nothing
// writes the files until a restart finds them readable. A subscription whose
// SYNC_FILTER names a field this build does not know is not run (alert
// subscription_refused naming the lane and the field) and is kept as read;
// the other lanes run.
func (m *followManager) load() error {
	subs, err := readFollowFileOrSetAside(m.subsPath)
	var old []FollowStatus
	if err == nil {
		old, err = readFollowFileOrSetAside(m.path)
	}
	if err != nil {
		m.mu.Lock()
		m.blocked = err.Error()
		m.mu.Unlock()
		m.n.alerts.Raise(ops.KindSubscriptionsUnreadable, filepath.Base(m.subsPath), ops.SeverityError,
			"the subscriptions are not loaded, so this node imports no lane and changes no subscription: "+err.Error())
		return err
	}
	index := map[FollowKey]int{}
	for i, st := range subs {
		index[st.FollowKey] = i
	}
	for _, st := range old {
		st.inOldFile = true
		i, held := index[st.FollowKey]
		if !held {
			index[st.FollowKey] = len(subs)
			subs = append(subs, st)
			continue
		}
		subs[i].inOldFile = true
		if subs[i].inFollowFile() && st.Cursor != "" {
			subs[i].Cursor = st.Cursor
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range subs {
		if _, held := m.subs[st.FollowKey]; held {
			continue
		}
		if st.Refused != "" {
			m.keepRefusedLocked(st)
			log.Warnf("Subscription %s %s/%s from %s: %s", st.Schema, st.ProviderID, st.SourceName, st.Producer, st.Refused)
			m.n.alerts.Raise(ops.KindSubscriptionRefused, followAlertSubject(st.FollowKey), ops.SeverityError, st.Refused)
			continue
		}
		m.startLocked(st)
		if text := st.UnreadText(); text != "" {
			m.n.alerts.Raise(ops.KindFollowCursorRefused, followAlertSubject(st.FollowKey), ops.SeverityWarning, text)
		}
	}
	return nil
}

// errBlockedLocked is why a change is refused while the subscription files
// are not loaded (nil once they are); the caller holds m.mu.
func (m *followManager) errBlockedLocked() error {
	if m.blocked == "" {
		return nil
	}
	return fmt.Errorf("the subscription files did not load, so no subscription changes until they do: %s", m.blocked)
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
	if opts.Rule != "" {
		plan.Rule = opts.Rule
	}
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
		if opts.Start == "" {
			// Without the statement the lane's size is unknown: only an
			// explicit --start beginning reads it from its beginning.
			plan.Start = FollowStartCurrent
		}
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
	plan.ReservedRecords, plan.ReservedBytes = m.reserved()
	if free, err := storage.FreeDiskBytes(m.storeDir()); err == nil {
		// What the followed lanes are storing is not free.
		plan.FreeBytes = int64(min(free, uint64(1<<62))) - plan.ReservedBytes
	} else {
		return plan, fmt.Errorf("free disk under %s: %w", m.storeDir(), err)
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
	case plan.HeldRecords+plan.ReservedRecords+plan.FirstReadRecords > plan.RecordCeiling:
		plan.Refusal = fmt.Sprintf("the first read is %d records, %d bytes; this node holds %d records, its followed lanes are storing %d, and its ceiling is %d (subscriptions.record_ceiling)",
			plan.FirstReadRecords, plan.FirstReadBytes, plan.HeldRecords, plan.ReservedRecords, plan.RecordCeiling)
	}
	return plan, nil
}

// Subscribe starts following a lane: the plan first, refused unless it fits
// or opts.Force. A lane already followed from the producer is left as it is,
// except that opts.AcknowledgeUnread clears what a refused cursor left
// unread, and a subscription from the lane's beginning replaces one with
// records unread (the whole lane is read again).
func (m *followManager) Subscribe(ctx context.Context, key FollowKey, opts FollowOptions) (FollowPlan, error) {
	key = normalizeFollowKey(key)
	m.mu.Lock()
	if err := m.errBlockedLocked(); err != nil {
		m.mu.Unlock()
		return FollowPlan{FollowKey: key}, err
	}
	e, held := m.subs[key]
	var st FollowStatus
	if held {
		st = e.status
	}
	m.mu.Unlock()
	if held && st.Refused != "" {
		return FollowPlan{FollowKey: key}, fmt.Errorf("this node holds that subscription and does not run it (%s); sync unsubscribe it first to subscribe again", st.Refused)
	}
	switch opts.Publications {
	case "", FollowPublicationsKeep, FollowPublicationsStream:
	default:
		return FollowPlan{FollowKey: key}, fmt.Errorf("publications %q is not %q or %q", opts.Publications, FollowPublicationsKeep, FollowPublicationsStream)
	}
	if held && opts.Publications != "" && !opts.AcknowledgeUnread && !(!st.Unread.IsZero() && opts.Start == FollowStartBeginning) {
		// The lane is followed: only what it keeps of its publications
		// changes; the stream and its cursor are left as they are.
		keep := opts.Publications == FollowPublicationsKeep
		m.mu.Lock()
		changed := e.status.KeepPublications != keep
		e.status.KeepPublications = keep
		m.mu.Unlock()
		if !changed {
			return FollowPlan{FollowKey: key, Start: st.Start, Rule: opts.Rule}, nil
		}
		log.Infof("Followed lane %s %s/%s from %s: publications %s", key.Schema, key.ProviderID, key.SourceName, key.Producer,
			map[bool]string{true: "kept (each is fetched and imported, so this node serves its files)", false: "not fetched while the stream holds the current batch"}[keep])
		err := m.save()
		if keep {
			m.keepPublicationsStarted()
		}
		return FollowPlan{FollowKey: key, Start: st.Start, Rule: opts.Rule}, err
	}
	if opts.AcknowledgeUnread {
		if !held {
			return FollowPlan{FollowKey: key}, errors.New("this node does not subscribe to that lane from that producer")
		}
		m.mu.Lock()
		was := e.status.Unread
		e.status.Unread, e.status.FromBeginning = time.Time{}, false
		m.mu.Unlock()
		if !was.IsZero() {
			m.n.alerts.Clear(ops.KindFollowCursorRefused, followAlertSubject(key))
			log.Infof("Followed lane %s %s/%s from %s: the records left unread by the cursor refused at %s are acknowledged", key.Schema, key.ProviderID,
				key.SourceName, key.Producer, was.UTC().Format(time.RFC3339))
		}
		return FollowPlan{FollowKey: key, Start: st.Start, Rule: opts.Rule}, m.save()
	}
	replace := held && !st.Unread.IsZero() && opts.Start == FollowStartBeginning
	if held && !replace {
		return FollowPlan{FollowKey: key, Start: st.Start, Rule: opts.Rule}, nil
	}
	plan, err := m.Plan(ctx, key, opts)
	if err != nil {
		return plan, err
	}
	if plan.Refusal != "" && !opts.Force {
		return plan, errors.New(plan.Refusal)
	}
	if replace {
		// The lane is read again from its beginning: the old subscription
		// stops first, then the new one starts (below).
		m.mu.Lock()
		if cur := m.subs[key]; cur == e {
			delete(m.subs, key)
		}
		m.mu.Unlock()
		e.cancel()
		<-e.done
		m.n.alerts.Clear(ops.KindFollowCursorRefused, followAlertSubject(key))
		log.Infof("Followed lane %s %s/%s from %s: subscribed again from the lane's beginning; the records left unread at %s are read", key.Schema,
			key.ProviderID, key.SourceName, key.Producer, st.Unread.UTC().Format(time.RFC3339))
	}
	if plan.StatementError != "" && plan.Start == FollowStartCurrent {
		// Forced: the follow reads the statement when the producer gives it.
		log.Warnf("Subscribed to %s %s/%s from %s without its statement (%s); it starts at the producer's current batch once the producer states it",
			key.Schema, key.ProviderID, key.SourceName, key.Producer, plan.StatementError)
	}
	keep := opts.Publications == FollowPublicationsKeep || (replace && opts.Publications == "" && st.KeepPublications)
	st = FollowStatus{FollowKey: key, Since: time.Now().UTC(), Start: plan.Start, KeepPublications: keep}
	if plan.Start == FollowStartCurrent && plan.StatementError == "" {
		st.Cursor = plan.Statement.ResumeCursor
	}
	log.Infof("Subscribed to %s %s/%s from %s: starts at the %s; first read %d records, %d bytes (rule %s)%s", key.Schema, key.ProviderID, key.SourceName,
		key.Producer, map[string]string{FollowStartCurrent: "producer's current batch " + plan.Statement.BatchID, FollowStartBeginning: "lane's beginning"}[plan.Start],
		plan.FirstReadRecords, plan.FirstReadBytes, plan.Rule, map[bool]string{true: "; forced past: " + plan.Refusal}[plan.Refusal != ""])
	m.mu.Lock()
	if _, held := m.subs[key]; !held {
		m.startLocked(st)
	}
	m.mu.Unlock()
	err = m.save()
	if keep {
		m.keepPublicationsStarted()
	}
	return plan, err
}

// adopt starts a subscription without a plan (a lane converted from the
// removed import ledger): it starts at the producer's current batch.
func (m *followManager) adopt(key FollowKey) bool {
	key = normalizeFollowKey(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.subs[key]; held || m.blocked != "" {
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
	if err := m.errBlockedLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
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
		// What was raised about the lane goes with it.
		subject := followAlertSubject(e.status.FollowKey)
		for _, kind := range []string{ops.KindFollowCursorRefused, ops.KindFollowStopped, ops.KindFollowRefused, ops.KindSubscriptionRefused} {
			m.n.alerts.Clear(kind, subject)
		}
	}
	m.unpeerProducers(stopped)
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
		return fmt.Errorf("%w: producer %s is not a trusted peer of this node", errFollowNotAdmitted, pid)
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
	e, ok := n.follows.subs[key]
	return ok && e.status.Refused == ""
}

// followKeepsPublications reports whether this node's subscription to the
// lane from the peer keeps its publications (FollowPublicationsKeep).
func (n *Node) followKeepsPublications(from peer.ID, schema, providerID, sourceName string) bool {
	if n == nil || n.follows == nil {
		return false
	}
	key := normalizeFollowKey(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: from.String()})
	n.follows.mu.Lock()
	defer n.follows.mu.Unlock()
	e, ok := n.follows.subs[key]
	return ok && e.status.Refused == "" && e.status.KeepPublications
}

// followPublication is what the $PNM path does with a publication of the
// subscribed lane from the peer. A lane whose followed stream is connected
// and holds the batch the producer states as current is streamed: the stream
// carries the publication's records. A lane whose current batch is not held
// (its list read or its fetches by CID fail, so it gets only the records
// stored after its cursor), or whose stream the producer refuses, imports
// it. A lane started less than followPublicationWait ago whose stream has not
// settled yet (after a restart: the stored-$PNM catch-up runs before most
// streams have held their batch again) leaves it to the catch-up, which runs
// again as soon as the lane settles: a batch already held is not imported a
// second time, and a lane that cannot hold it still gets it.
func (n *Node) followPublication(from peer.ID, schema, providerID, sourceName string) int {
	if n == nil || n.follows == nil {
		return followPublicationImport
	}
	key := normalizeFollowKey(FollowKey{Schema: schema, ProviderID: providerID, SourceName: sourceName, Producer: from.String()})
	n.follows.mu.Lock()
	defer n.follows.mu.Unlock()
	e, ok := n.follows.subs[key]
	switch {
	case !ok:
		return followPublicationImport
	case e.status.Connected && e.batchHeld:
		return followPublicationStreamed
	case !e.settled && time.Since(e.started) < followPublicationWait:
		e.deferred = true
		return followPublicationDeferred
	}
	return followPublicationImport
}

// errGossipRecordNoLane is why a record gossiped on a schema topic is not
// stored.
var errGossipRecordNoLane = errors.New("a record gossiped on a schema topic names no lane (no provider, no source), so no subscription admits it; this node imports a lane only through its subscription (the followed stream and the lane's signed $PNM publications)")

// gossipRecordAdmitted is the subscription gate of records gossiped on schema
// topics (protocol.GossipRecordAdmitter). Such a record carries no lane, so
// no subscription names it, and none is stored.
//
// What uses that path, and what keeps working: the node itself gossips no
// record there. Its identity ($EPM) and its dataset publications travel as
// signed $PNM announcements, which are not this path: a trusted peer's $PNM
// is still stored and handled (the identity read, the catalog, and the
// import of a subscribed lane's publication). The log head ($PLHD) has no
// publisher. What did use the path is an outside publisher (POST
// /api/v1/pubsub/publish, the sdn-js publish, a module's pubsub.publish) whose
// records a node trusting it, or one with network.accept_push_from: any,
// stored with no lane; that ingest is gone: records enter a node through its
// own API or a subscription.
func (n *Node) gossipRecordAdmitted(from peer.ID, schema string) error {
	return errGossipRecordNoLane
}

// followTrusted reports a peer this node has a trust relation with: its
// followed streams get the trusted limits (protocol.FlatSQLSyncHandler).
func (n *Node) followTrusted(id peer.ID) bool {
	return n != nil && n.peerRegistry != nil && n.peerRegistry.IsTrusted(id)
}

// keepRefusedLocked holds a subscription this build does not run
// (FollowStatus.Refused): listed, written back as read, not followed; the
// caller holds m.mu.
func (m *followManager) keepRefusedLocked(st FollowStatus) {
	st.LastError = st.Refused
	done := make(chan struct{})
	close(done)
	m.subs[st.FollowKey] = &followEntry{status: st, cancel: func() {}, done: done, started: time.Now(), settled: true}
}

// startLocked starts one lane; the caller holds m.mu.
func (m *followManager) startLocked(st FollowStatus) {
	ctx, cancel := context.WithCancel(m.n.ctx)
	e := &followEntry{status: st, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	m.subs[st.FollowKey] = e
	m.n.wg.Add(1)
	go func() {
		defer m.n.wg.Done()
		defer close(e.done)
		m.run(ctx, e)
	}()
}

// run follows one lane until ctx ends, reconnecting after a failure. The
// wait before a reconnect doubles from followBackoffMin up to
// followBackoffMax (with jitter) and starts again after a connection that
// delivered a page: a producer that cannot answer a lane is not asked again
// every minute and a half for as long as it cannot, and the lane's $PNM
// publications import meanwhile.
func (m *followManager) run(ctx context.Context, e *followEntry) {
	if followStartDelay != nil {
		m.mu.Lock()
		source := e.status.SourceName
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(followStartDelay(source)):
		}
	}
	backoff := followBackoffMin
	for ctx.Err() == nil {
		pages, err := m.followGuarded(ctx, e)
		if ctx.Err() != nil {
			return
		}
		if pages > 0 {
			backoff = followBackoffMin
		}
		wait := followBackoffWait(backoff)
		var remote *syncwire.RemoteError
		if errors.As(err, &remote) && remote.Code == "busy" {
			wait = max(wait, time.Duration(remote.RetryAfterMs)*time.Millisecond)
		}
		stopped := errors.Is(err, errFollowStopped)
		if stopped {
			wait = followStoppedWait
		}
		if errors.Is(err, errFollowNotAdmitted) {
			// Trust is asked again later, not at every backoff.
			wait = followNotAdmittedWait
		}
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
		if stopped {
			e.status.Stopped = err.Error()
		}
		if stopped || followNotAuthorized(err) {
			// The stream holds no batch: the lane's publications carry it.
			m.settledLocked(e)
		}
		key := e.status.FollowKey
		m.mu.Unlock()
		if followRefusal(err) {
			m.n.alerts.Raise(ops.KindFollowRefused, followAlertSubject(key), ops.SeverityError, err.Error())
		}
		if stopped {
			m.n.alerts.Raise(ops.KindFollowStopped, followAlertSubject(key), ops.SeverityError, err.Error())
		}
		log.Warnf("Followed lane %s %s/%s from %s: %.1024q; reconnecting in %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, fmt.Sprint(err), wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, followBackoffMax)
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
		e.status.Cursor = stmt.ResumeCursor
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
	m.peerProducer(remote)
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
				// The producer no longer accepts the saved cursor: start
				// again at its current batch, never at the lane's beginning.
				m.restartAtCurrent(e, remoteErr)
			}
			_ = stream.Reset()
			return pages, err
		}
		if err := m.n.followAdmitted(remote, st.FollowKey); err != nil {
			_ = stream.Reset()
			return pages, fmt.Errorf("%v; the stream ends", err)
		}
		transfer := time.Since(began)
		// A page with records: the next page is granted before this one is
		// stored, so the producer reads and sends it while this node writes
		// (two pages at most in flight; the cursor is still saved only after
		// the store returns, so a restart resumes before the granted page).
		// A quiet page is granted after the current-batch step below.
		ahead := len(runs) > 0
		if ahead {
			if err := syncwire.Grant(stream, st.Schema); err != nil {
				_ = stream.Reset()
				return pages, fmt.Errorf("grant: %w", err)
			}
		}
		release, err := m.reserve(runsRecords(runs), received)
		if err != nil {
			_ = stream.Reset()
			return pages, err
		}
		res, storeErr := m.storeReserved(release, func() (storedRuns, error) { return m.storeRuns(ctx, st, remote, runs, "", nil) })
		m.mu.Lock()
		if e.status.Stopped != "" {
			e.status.Stopped = ""
			m.n.alerts.Clear(ops.KindFollowStopped, followAlertSubject(st.FollowKey))
		}
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
				e.batchHeld = false
				m.settledLocked(e)
				m.mu.Unlock()
				if errors.Is(err, errFollowStopped) {
					_ = stream.Reset()
					return pages, err
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
		if !ahead {
			if err := syncwire.Grant(stream, st.Schema); err != nil {
				_ = stream.Reset()
				return pages, fmt.Errorf("grant: %w", err)
			}
		}
	}
}

// followStoreFault and followStartDelay are nil in every release build. A
// build with the tag sdn_e2e_follow_faults sets them (follow_faults_e2e.go):
// the store of a test lane panics, and a test lane's first stream opens late,
// for the publication real-daemon tests.
var (
	followStoreFault func(sourceName string)
	followStartDelay func(sourceName string) time.Duration
)

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
		if followStoreFault != nil {
			followStoreFault(st.SourceName)
		}
		tags := storage.SourceTags{ProviderID: st.ProviderID, SourceName: st.SourceName, BatchID: string(h.BATCH_ID()), ProducerPeerID: remote.String()}
		if batch != "" {
			tags.BatchID = batch
		}
		began = time.Now()
		n, err := m.n.store.StoreBatchWithSourceTags(schema, data, remote.String(), nil, tags)
		res.store += time.Since(began)
		res.records += len(data)
		res.stored += n
		m.addHeld(int64(n))
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
	markHeld := func() {
		m.mu.Lock()
		e.batchHeld = true
		m.settledLocked(e)
		m.mu.Unlock()
	}
	if stmt.Retracted {
		if err := m.dropRetracted(st, e, remote); err != nil {
			return err
		}
		markHeld()
		return nil
	}
	if stmt.BatchID == "" {
		markHeld()
		return nil
	}
	counts, err := m.n.store.FollowLaneBatchCounts(st.Schema, st.ProviderID, st.SourceName, remote.String())
	if errors.Is(err, storage.ErrFollowLaneUnsupported) {
		markHeld()
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
		e.current, e.held, e.batchHeld = stmt, own, true
		m.settledLocked(e)
		m.mu.Unlock()
		return nil
	case e.current == stmt && own == e.held:
		e.batchHeld = true
		m.settledLocked(e)
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
	e.current, e.held, e.rule, e.batchHeld = stmt, counts[stmt.BatchID], rule, true
	m.settledLocked(e)
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
			// The records' bytes are not known before they arrive: the
			// fetch reserves the most one page of them can hold.
			release, err := m.reserve(len(pending), syncwire.FollowMaxPageBytes)
			if err != nil {
				return got, fetched, err
			}
			var stored []string
			_, err = m.storeReserved(release, func() (storedRuns, error) {
				var err error
				stored, err = m.fetchByCID(ctx, remote, st, batch, pending)
				return storedRuns{}, err
			})
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

// restartAtCurrent starts a subscription whose cursor the producer refused
// again at the producer's current batch (see the package comment).
//
// For a lane whose history this node keeps (subscribed from its beginning,
// or a keep-all or archive-all lane) the records stored between the last
// position held and the current batch are not read: that is kept with the
// subscription (FollowStatus.Unread, in both files' SYNC_FILTER and shown by
// sync subscriptions) until the operator subscribes from the beginning again
// or acknowledges it, and a subscription made from the beginning stays in
// followFileName meanwhile.
func (m *followManager) restartAtCurrent(e *followEntry, refused *syncwire.RemoteError) {
	m.mu.Lock()
	key := e.status.FollowKey
	m.mu.Unlock()
	rule := m.n.laneRetentionRule(key.Schema, key.ProviderID, key.SourceName)
	m.mu.Lock()
	was := e.status.Start
	keeps := was == FollowStartBeginning || !e.status.Unread.IsZero() || rule == channels.RetentionKeepAll || rule == channels.RetentionArchiveAll
	if keeps && e.status.Unread.IsZero() {
		e.status.Unread = time.Now().UTC()
		e.status.FromBeginning = was == FollowStartBeginning
	}
	e.status.Start, e.status.Cursor = FollowStartCurrent, ""
	st := e.status
	m.mu.Unlock()
	if err := m.save(); err != nil {
		log.Errorf("Followed lane %s %s/%s from %s: start at the current batch not saved: %v", key.Schema, key.ProviderID, key.SourceName, key.Producer, err)
	}
	msg := fmt.Sprintf("the producer refused the saved cursor (%s); the lane starts again at the producer's current batch", refused.Code)
	if text := st.UnreadText(); text != "" {
		msg += "; " + text
	}
	log.Warnf("Followed lane %s %s/%s from %s: %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, msg)
	m.n.alerts.Raise(ops.KindFollowCursorRefused, followAlertSubject(key), ops.SeverityWarning, msg)
}

func runsRecords(runs []syncwire.FollowRun) int {
	n := 0
	for _, r := range runs {
		n += len(r.Records)
	}
	return n
}

// heldRecords is the records this node holds, read from the store at most
// every followHeldRefresh and counted on from what follows stored since.
func (m *followManager) heldRecords() (int64, error) {
	m.heldMu.Lock()
	defer m.heldMu.Unlock()
	if !m.heldAt.IsZero() && time.Since(m.heldAt) < followHeldRefresh {
		return m.held, nil
	}
	summary, err := m.n.store.DataSummary()
	if err != nil {
		return 0, err
	}
	var held int64
	for _, s := range summary.Schemas {
		held += s.Count
	}
	m.held, m.heldAt = held, time.Now()
	return held, nil
}

func (m *followManager) addHeld(n int64) {
	m.heldMu.Lock()
	m.held += n
	m.heldMu.Unlock()
}

// recordCeiling is subscriptions.record_ceiling.
func (m *followManager) recordCeiling() int64 {
	if m.n.config != nil {
		return m.n.config.Subscriptions.EffectiveRecordCeiling()
	}
	return config.DefaultSubscriptionRecordCeiling
}

// reserve is the running guard: it reserves records more records of bytes
// record bytes (their store bytes, bytes times followStoreExpansion) for one
// lane to store, when the free disk less every lane's reservations stays
// above followDiskReserve and the records held and reserved within the
// record ceiling. The caller calls the release it returns once the store has
// returned (the records stored are then counted as held, addHeld). A
// refusal is errFollowStopped.
func (m *followManager) reserve(records int, bytes int64) (func(), error) {
	if records <= 0 {
		return func() {}, nil
	}
	dir := m.storeDir()
	free, freeErr := storage.FreeDiskBytes(dir)
	held, err := m.heldRecords()
	if err != nil {
		return nil, fmt.Errorf("records held here: %w", err)
	}
	ceiling := m.recordCeiling()
	storeBytes := bytes * followStoreExpansion
	m.heldMu.Lock()
	defer m.heldMu.Unlock()
	if freeErr == nil {
		need := m.reserveBytes + storeBytes + followDiskReserve
		if int64(min(free, uint64(1<<62))) < need {
			return nil, fmt.Errorf("%w: storing %d more records (%d bytes) needs about %d bytes of free disk under %s (1 GiB to spare, and %d bytes the followed lanes are storing), and %d are free",
				errFollowStopped, records, bytes, need, dir, m.reserveBytes, free)
		}
	}
	if held+m.reserveRecords+int64(records) > ceiling {
		return nil, fmt.Errorf("%w: this node holds %d records, its followed lanes are storing %d, and %d more would pass its ceiling of %d (subscriptions.record_ceiling)",
			errFollowStopped, held, m.reserveRecords, records, ceiling)
	}
	m.reserveBytes += storeBytes
	m.reserveRecords += int64(records)
	var once sync.Once
	return func() {
		once.Do(func() {
			m.heldMu.Lock()
			m.reserveBytes -= storeBytes
			m.reserveRecords -= int64(records)
			m.heldMu.Unlock()
		})
	}, nil
}

// publicationStopped reports a publication of a subscribed lane that the
// running guard did not import (alert follow_stopped, the lane stopped); the
// stored-$PNM catch-up tries it again.
func (m *followManager) publicationStopped(key FollowKey, fileID string, err error) {
	if m == nil || key.Schema == "" {
		return
	}
	key = normalizeFollowKey(key)
	text := fmt.Sprintf("publication %s not imported: %v; the stored-$PNM catch-up tries it again", fileID, err)
	m.mu.Lock()
	if e := m.subs[key]; e != nil {
		e.status.Stopped = text
	}
	m.mu.Unlock()
	log.Warnf("Subscribed lane %s %s/%s from %s: %s", key.Schema, key.ProviderID, key.SourceName, key.Producer, text)
	m.n.alerts.Raise(ops.KindFollowStopped, followAlertSubject(key), ops.SeverityError, text)
}

// publicationImported counts the records a publication of a subscribed lane
// stored (for the running guard's count of records held) and clears a stop
// of the lane: the guard let it through.
func (m *followManager) publicationImported(key FollowKey, records int) {
	if m == nil || key.Schema == "" {
		return
	}
	m.addHeld(int64(records))
	key = normalizeFollowKey(key)
	m.mu.Lock()
	if e := m.subs[key]; e != nil {
		e.status.Stopped = ""
	}
	m.mu.Unlock()
	m.n.alerts.Clear(ops.KindFollowStopped, followAlertSubject(key))
}

// storeReserved runs store, the store a reservation was granted for, and
// releases the reservation when store returns or panics: the reservation is
// shared by every lane, so one held past its store (a panic recovered by
// followGuarded, retried at each reconnect) would stop every lane.
func (m *followManager) storeReserved(release func(), store func() (storedRuns, error)) (storedRuns, error) {
	defer release()
	return store()
}

// reserved is the records and store bytes the followed lanes are storing.
func (m *followManager) reserved() (records, bytes int64) {
	m.heldMu.Lock()
	defer m.heldMu.Unlock()
	return m.reserveRecords, m.reserveBytes
}

// reservedBytes is the store bytes the followed lanes are storing.
func (m *followManager) reservedBytes() int64 {
	_, bytes := m.reserved()
	return bytes
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

// save writes the subscriptions followFileName holds (inFollowFile) to it,
// then every subscription to subscriptionsFileName, each file atomically.
// That order is what keeps a crash between the two writes harmless: a lane
// unsubscribed is gone from followFileName first, so it cannot come back
// from there as a subscription from the beginning that subscriptionsFileName
// lacks; a lane both files held and only followFileName lost is kept as
// subscriptionsFileName holds it (load). Nothing is written while the files
// did not load.
func (m *followManager) save() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	err := m.errBlockedLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	var all, beginning bytes.Buffer
	for _, st := range m.Statuses() {
		all.Write(encodeFollowDSS(st, true))
		if st.inFollowFile() {
			beginning.Write(encodeFollowDSS(st, false))
		}
	}
	if err := writeFileAtomic(m.path, beginning.Bytes()); err != nil {
		return err
	}
	return writeFileAtomic(m.subsPath, all.Bytes())
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
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
	return os.Rename(tmp, path)
}

// encodeFollowDSS is one followed lane as a size-prefixed $DSS record; with
// start, SYNC_FILTER names where it starts.
func encodeFollowDSS(st FollowStatus, start bool) []byte {
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
	if start {
		pending = b.CreateString(followFilter(st))
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
