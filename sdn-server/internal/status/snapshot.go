package status

import (
	"bytes"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// A DASHBOARD READ MUST NEVER RUN A STORE QUERY.
//
// boundedread.go bounds how long a request waits on the store; this bounds it
// to zero. Every lane here is rebuilt on its own background goroutine and the
// finished frame is held in RAM, so the request path does nothing but hand back
// bytes it already has. That is the kubo webui bar: the page paints instantly
// or it is broken, and "the node is mid-ingest" is not an excuse a user accepts.
//
// The lanes are named so one cache can serve every push/pull surface that wants
// a pre-built binary frame; "stats" is the first.

// LaneConfig declares one background-refreshed frame.
type LaneConfig struct {
	// Name is the lookup key, e.g. "stats".
	Name string
	// Build assembles the frame. It runs ONLY on the lane goroutine, never on
	// a request. An error leaves the previous good frame standing.
	Build func() ([]byte, error)
	// Interval between refreshes. Jittered by up to 10% so several lanes do
	// not converge on the same tick.
	Interval time.Duration
}

// Snapshot is one lane's current state.
type Snapshot struct {
	// Frame is the last successfully built frame; nil before the first build.
	Frame []byte
	// Generation increments every time Frame changes. 0 = never built. It is
	// the ETag the HTTP surface serves.
	Generation uint64
	// BuiltAt is when Frame was produced.
	BuiltAt time.Time
	// Err is the most recent build error, kept alongside the good frame so an
	// operator can see the lane is failing while clients keep being served.
	Err error
	// ErrAt is when Err was recorded.
	ErrAt time.Time
	// Restored is true while Frame is the frame this lane persisted on a
	// PREVIOUS boot — real data, from before the restart, not yet replaced by
	// a build of this boot. The frame's own AS_OF says how old it is. See
	// snapshot_persist.go.
	Restored bool
}

type lane struct {
	cfg LaneConfig

	mu   sync.RWMutex
	snap Snapshot
}

// SnapshotCache holds a named set of background-refreshed frames.
type SnapshotCache struct {
	lanes map[string]*lane

	// persistDir backs every lane's latest frame with a file so a restart does
	// not blank the dashboard. "" = RAM-only, the original behavior. Set with
	// SetPersistDir before Start; see snapshot_persist.go.
	persistDir string

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
}

// ErrNoLane is returned for an unknown lane name.
var ErrNoLane = errors.New("status: no such snapshot lane")

// NewSnapshotCache builds a cache over the given lanes. Lanes with no name or
// no build function are ignored.
func NewSnapshotCache(cfgs ...LaneConfig) *SnapshotCache {
	c := &SnapshotCache{lanes: make(map[string]*lane, len(cfgs)), stop: make(chan struct{})}
	for _, cfg := range cfgs {
		if cfg.Name == "" || cfg.Build == nil {
			continue
		}
		if cfg.Interval <= 0 {
			cfg.Interval = 5 * time.Second
		}
		c.lanes[cfg.Name] = &lane{cfg: cfg}
	}
	return c
}

// Start builds every lane once, then keeps them refreshed. Idempotent.
func (c *SnapshotCache) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		// Restore BEFORE any lane goroutine runs: the whole point is that the
		// first request through the door after a restart is answered, not told
		// the snapshot is cold while a 60-minute hydration holds the store.
		c.loadPersisted()
		for _, l := range c.lanes {
			c.wg.Add(1)
			go c.run(l)
		}
	})
}

// Stop halts every lane. Idempotent; safe before Start.
func (c *SnapshotCache) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.stop) })
	c.wg.Wait()
}

func (c *SnapshotCache) run(l *lane) {
	defer c.wg.Done()
	// Warm immediately: a node that boots straight into an ingest must still
	// have a frame for the first client through the door.
	c.build(l)
	for {
		select {
		case <-c.stop:
			return
		case <-time.After(jitter(l.cfg.Interval)):
			c.build(l)
		}
	}
}

// Refresh rebuilds one lane now, on the caller's goroutine. Callers are
// background workers only — never an HTTP handler.
func (c *SnapshotCache) Refresh(name string) error {
	if c == nil {
		return ErrNoLane
	}
	l, ok := c.lanes[name]
	if !ok {
		return ErrNoLane
	}
	return c.build(l)
}

func (c *SnapshotCache) build(l *lane) error {
	frame, err := l.cfg.Build()

	l.mu.Lock()
	if err != nil {
		l.snap.Err, l.snap.ErrAt = err, time.Now()
		l.mu.Unlock()
		return err
	}
	l.snap.Err, l.snap.ErrAt = nil, time.Time{}
	unchanged := l.snap.Generation != 0 && bytes.Equal(l.snap.Frame, frame)
	if unchanged {
		// Nothing moved: hold the generation so ETags and the ws push stay
		// quiet while the node is idle. A restored frame that this boot's build
		// reproduced byte for byte is no longer "from the previous boot" — this
		// boot just confirmed it.
		l.snap.Restored = false
		l.mu.Unlock()
		return nil
	}
	l.snap.Frame = frame
	l.snap.Generation++
	l.snap.BuiltAt = time.Now()
	l.snap.Restored = false
	snap := l.snap
	l.mu.Unlock()

	// Write-behind on the lane goroutine, outside the lock: a slow disk must
	// never delay a Frame() read. See snapshot_persist.go.
	c.persistLane(l.cfg.Name, snap)
	return nil
}

// Frame returns the lane's current snapshot. The second result is false for an
// unknown lane. It never blocks on a build.
func (c *SnapshotCache) Frame(name string) (Snapshot, bool) {
	if c == nil {
		return Snapshot{}, false
	}
	l, ok := c.lanes[name]
	if !ok {
		return Snapshot{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.snap, true
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	spread := int64(d / 10)
	if spread <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(2*spread)-spread)
}
