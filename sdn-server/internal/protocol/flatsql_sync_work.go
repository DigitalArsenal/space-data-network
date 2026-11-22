package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// The work a follower's requests cost this node. A lane's statement, a
// followed page and a batch's list read the store; each runs under the
// context of its stream (streamWatch), so the engine's read ends when the
// peer resets the stream or the connection goes, and under a bound shorter
// than the follower's wait, so a follower is answered (busy) rather than
// left to give up and ask again. At most followWorkPerPeer of one peer's and
// followWorkTotal of all peers' such reads run at once; a statement past
// them is refused busy at once (a follower backs off), and a page, a list or
// a scan waits for a slot within its bound. The store engine serves reads on
// 4 interactive lanes and 1 bulk lane: with at most 2 of the interactive
// lanes and none of the bulk lane taken by remote peers, a node's own reads
// (its publications' window probes and fingerprint scans, its API) always
// have lanes of their own. Writes (ingest) run on the engine's writer
// threads, apart from every read.
const (
	followWorkPerPeer = 2
	followWorkTotal   = 2
)

var (
	// followStatementBound bounds the work of one lane statement; the
	// follower waits followStatementWait (internal/node, 5 minutes) for it.
	followStatementBound = time.Minute
	// followPageBound bounds the work of one followed page, the wait for
	// new records after a quiet interval included; the follower waits
	// followReadWait (internal/node, 5 minutes) for a page.
	followPageBound = 2 * time.Minute
)

// followWorkFault is nil in every release build. A build with the tag
// sdn_e2e_follow_faults sets it (follow_faults_e2e.go): a test lane's
// statement is a slow store read, for the publication real-daemon tests.
var followWorkFault func(ctx context.Context, sourceName string) error

// errFollowWorkBusy refuses a read past the work limits.
var errFollowWorkBusy = fmt.Errorf("%w: store reads for followers are at their limit; ask again later", syncwire.ErrBusy)

// errFollowWorkBound answers a read that passed its bound.
var errFollowWorkBound = fmt.Errorf("%w: the read did not finish within its bound; ask again later", syncwire.ErrBusy)

// followWork counts the store reads served to followers, per peer and in
// total.
type followWork struct {
	mu     sync.Mutex
	total  int
	byPeer map[peer.ID]int
	free   chan struct{} // closed and replaced at each release
}

func newFollowWork() *followWork {
	return &followWork{byPeer: map[peer.ID]int{}, free: make(chan struct{})}
}

// acquire takes a slot for p, waiting for one until ctx ends (a ctx already
// ended or without a deadline is not waited on: busy at once). The returned
// release gives it back.
func (f *followWork) acquire(ctx context.Context, p peer.ID, wait bool) (func(), error) {
	for {
		f.mu.Lock()
		if f.total < followWorkTotal && f.byPeer[p] < followWorkPerPeer {
			f.total++
			f.byPeer[p]++
			f.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { f.release(p) }) }, nil
		}
		free := f.free
		f.mu.Unlock()
		if !wait {
			return nil, errFollowWorkBusy
		}
		select {
		case <-free:
		case <-ctx.Done():
			return nil, errFollowWorkBusy
		}
	}
}

func (f *followWork) release(p peer.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total--
	if f.byPeer[p]--; f.byPeer[p] <= 0 {
		delete(f.byPeer, p)
	}
	close(f.free)
	f.free = make(chan struct{})
}

// running is the reads served at once (tests read it through FollowWork).
func (f *followWork) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// boundedWork runs read under a slot of p's and a context that ends at
// bound, with the stream's context; a read past its bound answers
// errFollowWorkBound.
func (h *FlatSQLSyncHandler) boundedWork(stream context.Context, p peer.ID, wait bool, bound time.Duration, read func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(stream, bound)
	defer cancel()
	release, err := h.followWork.acquire(ctx, p, wait)
	if err != nil {
		if stream.Err() != nil {
			return context.Cause(stream)
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
// stream, while the handler is reading the store; what the peer sends (a
// followed stream's grants) is handed to the handler as it reads. The
// stream's own read deadline is cleared; the handler's read waits until the
// deadline it sets on the watch.
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

type readDeadliner interface {
	io.Reader
	SetReadDeadline(time.Time) error
}

// watchStream starts reading s for its handler. The returned watch's context
// ends with parent, or with errStreamReset once the peer resets s or the
// connection ends. stop ends the watch's context (the handler is done).
func watchStream(parent context.Context, s readDeadliner) (*streamWatch, func()) {
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
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// A reset, a closed connection or a peer past its
					// buffer: nobody reads what this handler would send.
					cancel(errStreamReset)
				}
				return
			}
		}
	}()
	return w, func() { cancel(context.Canceled) }
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
