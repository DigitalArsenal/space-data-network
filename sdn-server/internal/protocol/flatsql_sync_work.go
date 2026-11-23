package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// The work a follower's requests cost this node. A lane's statement, a
// followed page, a batch's list, a fetch by CID and a read-chunk scan read
// the store; each runs under the context of its stream (streamWatch), so on
// a format 5 store the engine's read ends when the peer resets the stream or
// the connection goes, and under a bound shorter than the follower's wait,
// so a follower is answered (busy) rather than left to give up and ask
// again.
//
// Slots: one peer runs at most followWorkPerPeer such reads at once. Peers
// the node has no trust relation with (followTrust, readAuthorizer: the
// notion followSlots uses) share followWorkStrangers slots; a trusted peer
// takes any free slot, and followWorkTrustedReserve slots more are open to
// trusted peers alone, so throwaway peer IDs that keep every stranger slot
// busy never keep a trusted follower waiting. In all at most
// followWorkStrangers + followWorkTrustedReserve reads of other peers run at
// once: 3 on a host of 2 or more cores, 2 on a host of 1 core, where every
// read shares the one core with the node's own work.
//
// The store engine (FlatSQL 3.12.14) serves reads on 4 interactive lanes, 1
// bulk lane and 1 sandbox lane on any host, whatever its cores (the cores
// set only its writer threads: one fewer than the cores, at least 1, at most
// 8). Every read here runs on an interactive lane, so peers hold at most 3
// of the 4 (2 on a 1-core host) and none of the bulk lane: the node's own
// reads (its publications' window probes and fingerprint scans, its API)
// always have an interactive lane and the bulk lane.
//
// A read waits for a slot: a statement up to followStatementSlotWait, a
// page, a list page, a fetch or a scan within its bound; then it is answered
// busy.
const (
	followWorkPerPeer        = 1
	followWorkTrustedReserve = 1
)

// followWorkStrangers is the slots peers without a trust relation share on
// a host of cores cores.
func followWorkStrangers(cores int) int {
	if cores < 2 {
		return 1
	}
	return 2
}

var (
	// followStatementBound bounds the work of one lane statement (the wait
	// for a slot included); the follower waits followStatementWait
	// (internal/node, 5 minutes) for it.
	followStatementBound = time.Minute
	// followStatementSlotWait bounds a statement's wait for a slot. A
	// follower's page of a batch's list on a keep-all lane of millions of
	// records took 10 to 16 s on a copy of a production store; a statement
	// waits past one such page of its own peer instead of being refused at
	// once and asked again 30 s or more later.
	followStatementSlotWait = 20 * time.Second
	// followPageBound bounds the work of one followed page (the wait for a
	// slot and for new records after a quiet interval included), one page
	// of a batch's list, one fetch by CID and one scan. The follower waits
	// followReadWait (internal/node, 5 minutes) for each; a follower of
	// 1.1.13 or 1.1.14 waits 2 minutes, so the bound is well inside that and
	// the producer's busy reaches it before it gives up.
	followPageBound = 90 * time.Second
)

// followWorkFault is nil in every release build. A build with the tag
// sdn_e2e_follow_faults sets it (follow_faults_e2e.go): a test lane's
// statement is a slow store read, for the publication real-daemon tests.
var followWorkFault func(ctx context.Context, sourceName string) error

// errFollowWorkBusy refuses a read past the work limits.
var errFollowWorkBusy = fmt.Errorf("%w: store reads for followers are at their limit; ask again later", syncwire.ErrBusy)

// errFollowWorkBound answers a read that passed its bound.
var errFollowWorkBound = fmt.Errorf("%w: the read did not finish within its bound; ask again later", syncwire.ErrBusy)

// followWork counts the store reads served to other peers: per peer, of
// peers without a trust relation, and in total.
type followWork struct {
	strangerSlots, totalSlots int

	mu        sync.Mutex
	total     int
	strangers int
	byPeer    map[peer.ID]int
	free      chan struct{} // closed and replaced at each release
}

func newFollowWork(cores int) *followWork {
	strangers := followWorkStrangers(cores)
	return &followWork{strangerSlots: strangers, totalSlots: strangers + followWorkTrustedReserve, byPeer: map[peer.ID]int{},
		free: make(chan struct{})}
}

// acquire takes a slot for p, waiting for one until ctx ends; a ctx that has
// ended gets busy at once. The returned release gives the slot back.
func (f *followWork) acquire(ctx context.Context, p peer.ID, trusted bool) (func(), error) {
	for {
		f.mu.Lock()
		if f.total < f.totalSlots && f.byPeer[p] < followWorkPerPeer && (trusted || f.strangers < f.strangerSlots) {
			f.total++
			f.byPeer[p]++
			if !trusted {
				f.strangers++
			}
			f.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { f.release(p, trusted) }) }, nil
		}
		free := f.free
		f.mu.Unlock()
		if ctx.Err() != nil {
			return nil, errFollowWorkBusy
		}
		select {
		case <-free:
		case <-ctx.Done():
			return nil, errFollowWorkBusy
		}
	}
}

func (f *followWork) release(p peer.ID, trusted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total--
	if !trusted {
		f.strangers--
	}
	if f.byPeer[p]--; f.byPeer[p] <= 0 {
		delete(f.byPeer, p)
	}
	close(f.free)
	f.free = make(chan struct{})
}

// running is the reads served at once.
func (f *followWork) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// followTrusted reports a peer this node has a trust relation with, as
// followed streams and store reads for followers count it.
func (h *FlatSQLSyncHandler) followTrusted(p peer.ID) bool {
	return authorizedPeer(h.followTrust, p) || authorizedPeer(h.readAuthorizer, p)
}

// boundedWork runs read under a slot of p's, waited for up to slotWait, and
// a context that ends at bound (the wait included), with the stream's
// context; a read past its bound answers errFollowWorkBound. The slot is
// held until read returns: on a format 5 store every engine read under it
// ends when its context ends, on the other store formats a read runs to its
// end and the bound is checked then.
func (h *FlatSQLSyncHandler) boundedWork(stream context.Context, p peer.ID, slotWait, bound time.Duration, read func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(stream, bound)
	defer cancel()
	waitCtx, waitCancel := context.WithTimeout(ctx, min(slotWait, bound))
	release, err := h.followWork.acquire(waitCtx, p, h.followTrusted(p))
	waitCancel()
	if err != nil {
		if stream.Err() != nil {
			return context.Cause(stream)
		}
		if ctx.Err() != nil {
			return errFollowWorkBound
		}
		return err
	}
	defer release()
	err = read(ctx)
	if err != nil && stream.Err() == nil && ctx.Err() != nil {
		return errFollowWorkBound
	}
	return err
}

// errStreamReset is the cause of a watched stream's context when the peer
// reset the stream or its connection ended.
var errStreamReset = errors.New("the peer reset the stream")

// streamWatch reads a stream for its handler from the time the request is
// decoded, so that the handler's context ends as soon as the peer resets the
// stream or the connection closes, while the handler is reading the store;
// what the peer sends (a followed stream's grants) is handed to the handler
// as it reads. The stream's own read deadline is cleared; the handler's read
// waits until the deadline it sets on the watch.
type streamWatch struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu       sync.Mutex
	buf      []byte
	err      error         // what the stream's read ended with (io.EOF: the peer closed its side)
	ready    chan struct{} // closed and replaced when buf or err changes
	deadline time.Time
}

// streamWatchBuffer bounds what a peer may send ahead of the handler (a
// grant is under 200 bytes).
const streamWatchBuffer = 64 << 10

// halfClosedPoll is how often the watch of a stream whose peer closed its
// side looks whether the connection has closed.
const halfClosedPoll = 500 * time.Millisecond

// watchable is what watchStream needs of a stream.
type watchable interface {
	io.Reader
	SetReadDeadline(time.Time) error
	Conn() network.Conn
}

// watchStream starts reading s for its handler. The returned watch's context
// ends with parent, or with errStreamReset once the peer resets s or the
// connection closes, including after the peer closed its side (io.EOF): a
// peer that closes its side after its request (sdn-js, syncwire.Call) still
// reads the answer, and once its connection closes nobody does. A reset
// after the peer closed its side is not seen on the read side (yamux goes on
// answering io.EOF; the reset ends only the write side), so such a read ends
// at its bound or with the connection. stop ends the watch's context (the
// handler is done) and sets the stream's read deadline in the past, so the
// watch's read of s returns and its goroutine ends then; the handler's
// caller closing s (a close ends the stream's read side) ends it too.
func watchStream(parent context.Context, s watchable) (*streamWatch, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &streamWatch{ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	_ = s.SetReadDeadline(time.Time{})
	go func() {
		p := make([]byte, 4096)
		for {
			n, err := s.Read(p)
			w.mu.Lock()
			if n > 0 {
				if len(w.buf)+n > streamWatchBuffer {
					err = fmt.Errorf("%w: the peer sent more than %d bytes ahead of its pages", syncwire.ErrFollowClosed, streamWatchBuffer)
				} else {
					w.buf = append(w.buf, p[:n]...)
				}
			}
			if err != nil {
				w.err = err
			}
			close(w.ready)
			w.ready = make(chan struct{})
			w.mu.Unlock()
			if err == io.EOF {
				break
			}
			if err != nil {
				// A closed connection wraps io.EOF in a reset (yamux:
				// "stream reset: connection closed: EOF"), so the test
				// above is for io.EOF itself, not errors.Is. A reset, a
				// closed connection or a peer past its buffer: nobody
				// reads what this handler would send.
				cancel(errStreamReset)
				return
			}
		}
		// The peer closed its side and reads the answer: watch its
		// connection until the handler is done.
		conn := s.Conn()
		tick := time.NewTicker(halfClosedPoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if conn == nil || conn.IsClosed() {
					cancel(errStreamReset)
					return
				}
			}
		}
	}()
	return w, func() {
		cancel(context.Canceled)
		_ = s.SetReadDeadline(time.Unix(1, 0))
	}
}

// SetReadDeadline sets when the handler's next Read gives up waiting.
func (w *streamWatch) SetReadDeadline(t time.Time) error {
	w.mu.Lock()
	w.deadline = t
	w.mu.Unlock()
	return nil
}

// Read hands the handler what the peer sent, waiting for it until the
// deadline set with SetReadDeadline.
func (w *streamWatch) Read(p []byte) (int, error) {
	for {
		w.mu.Lock()
		if len(w.buf) > 0 {
			n := copy(p, w.buf)
			w.buf = w.buf[n:]
			w.mu.Unlock()
			return n, nil
		}
		if w.err != nil {
			err := w.err
			w.mu.Unlock()
			return 0, err
		}
		ready, deadline := w.ready, w.deadline
		w.mu.Unlock()
		if deadline.IsZero() {
			<-ready
			continue
		}
		d := time.Until(deadline)
		if d <= 0 {
			return 0, fmt.Errorf("read: %w", errWatchDeadline)
		}
		t := time.NewTimer(d)
		select {
		case <-ready:
			t.Stop()
		case <-t.C:
			return 0, fmt.Errorf("read: %w", errWatchDeadline)
		}
	}
}

// errWatchDeadline is a watched read past its deadline.
var errWatchDeadline = errors.New("i/o deadline reached")

// watchedStream is a followed stream's two halves: reads from the watch,
// writes and write deadlines to the stream.
type watchedStream struct {
	*streamWatch
	w interface {
		io.Writer
		SetWriteDeadline(time.Time) error
	}
}

func (s watchedStream) Write(p []byte) (int, error)        { return s.w.Write(p) }
func (s watchedStream) SetWriteDeadline(t time.Time) error { return s.w.SetWriteDeadline(t) }

// workCores is the cores followWork is sized for.
var workCores = runtime.NumCPU()
