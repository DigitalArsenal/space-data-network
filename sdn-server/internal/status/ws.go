package status

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	logging "github.com/ipfs/go-log/v2"
)

var wsLog = logging.Logger("sdn-status-ws")

const (
	// broadcastInterval is how often every connected client receives a fresh
	// NodeStatusSet frame.
	broadcastInterval = 5 * time.Second
	writeWait         = 10 * time.Second
	pongWait          = 60 * time.Second
	pingPeriod        = (pongWait * 9) / 10
	// clientSendBuffer bounds per-client backpressure; a client that cannot
	// keep up is dropped rather than stalling the broadcast.
	clientSendBuffer = 8
)

// SnapshotFunc returns the current serialized NodeStatusSet ($NST, size-
// prefixed). Returning nil skips that push.
type SnapshotFunc func() []byte

// FrameSourceFunc returns a pre-built frame and the generation it was built
// at. ok is false while the source has nothing yet. The Broadcaster pushes the
// frame as its own binary message when the generation moves, so the socket
// carries several INDEPENDENT frames; a client tells them apart by the
// FlatBuffer file identifier ($NST for node status, $NDS for dashboard stats),
// which sits at bytes 8..12 of a size-prefixed buffer.
type FrameSourceFunc func() (frame []byte, generation uint64, ok bool)

// frameSource is one auxiliary frame lane and the generation last pushed.
type frameSource struct {
	name string
	get  FrameSourceFunc
	// lastGen is touched only from refresh (single-flighted by b.building).
	lastGen atomic.Uint64
	// lastFrame is what a newly connected client is sent immediately.
	lastFrame atomic.Pointer[[]byte]
}

// Broadcaster is the public, read-only /ws/status hub: it upgrades incoming
// connections, pushes the current status frame immediately, then fans out a
// fresh frame to every client on a 5s ticker. There is no authentication and
// no publish path — client messages are read only to service pong/close.
type Broadcaster struct {
	snapshot       SnapshotFunc
	allowedOrigins []string
	upgrader       websocket.Upgrader

	// lastFrame is the most recent frame the builder produced, served to new
	// clients immediately. The builder reads live subsystems — the peer
	// registry, the EPM service, the record store — and ANY of them can be slow
	// while the node is busy ingesting. Building on the connect path made that
	// slowness fatal: the client completed its upgrade and then sat receiving
	// nothing, which is exactly what the dashboard showed
	// ("Connecting to the node status feed…", forever, for everyone).
	//
	// A status feed that stops answering while the node is busy is worthless,
	// because "is it working?" is asked precisely then. So the frame is built
	// OFF the hot path and cached: a slow node costs freshness, never the feed.
	lastFrame atomic.Pointer[[]byte]
	// building guards against overlapping builds when one takes longer than the
	// tick: a slow build must not queue up more of itself.
	building atomic.Bool

	// sources are ADDITIONAL frame lanes pushed on the same socket alongside
	// the $NST frame. Set before Start; never mutated afterwards.
	sources []*frameSource

	mu      sync.Mutex
	clients map[*wsClient]struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
	// done is closed exactly once (guarded by client-set membership) to tell
	// writePump to stop. send is never closed, so a concurrent broadcast can
	// safely drop into it without a send-on-closed-channel panic.
	done chan struct{}
}

// NewBroadcaster builds a Broadcaster. snapshot supplies the frame to push;
// allowedOrigins is the extra cross-origin allowlist beyond the always-allowed
// same-origin and loopback/dev origins.
func NewBroadcaster(snapshot SnapshotFunc, allowedOrigins []string) *Broadcaster {
	b := &Broadcaster{
		snapshot:       snapshot,
		allowedOrigins: append([]string(nil), allowedOrigins...),
		clients:        make(map[*wsClient]struct{}),
		stop:           make(chan struct{}),
	}
	b.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     b.checkOrigin,
	}
	return b
}

// AddFrameSource registers an additional frame lane pushed on this socket.
// Call before Start. The lane's frame is sent on connect and whenever its
// generation changes; the $NST frame keeps flowing exactly as before.
func (b *Broadcaster) AddFrameSource(name string, get FrameSourceFunc) {
	if get == nil {
		return
	}
	b.sources = append(b.sources, &frameSource{name: name, get: get})
}

// Start launches the 5s broadcast loop. Idempotent.
func (b *Broadcaster) Start() {
	b.startOnce.Do(func() {
		// Warm the cache immediately so the first client to arrive — which, on
		// a node that boots straight into an ingest, may arrive before the
		// first tick — has something real to receive.
		go b.refresh()
		go b.run()
	})
}

// refresh builds one frame and caches it, then fans it out. It is the ONLY
// caller of the snapshot function, and it never runs on a connection's
// goroutine or inside the ticker loop.
func (b *Broadcaster) refresh() {
	if !b.building.CompareAndSwap(false, true) {
		// A previous build is still running against a busy subsystem. Skipping
		// is correct: clients keep receiving the cached frame, and one slow
		// build cannot pile up behind another.
		return
	}
	defer b.building.Store(false)

	if b.snapshot != nil {
		if frame := b.snapshot(); frame != nil {
			b.lastFrame.Store(&frame)
			b.broadcast(frame)
		}
	}
	b.pushSources()
}

// pushSources fans out each auxiliary lane whose generation moved since the
// last push. An unchanged lane sends nothing: an idle node must not spend
// bandwidth restating the same numbers.
func (b *Broadcaster) pushSources() {
	for _, src := range b.sources {
		frame, gen, ok := src.get()
		if !ok || len(frame) == 0 {
			continue
		}
		src.lastFrame.Store(&frame)
		if src.lastGen.Swap(gen) == gen {
			continue
		}
		b.broadcast(frame)
	}
}

// cachedFrame returns the last built frame, or nil before the first build.
func (b *Broadcaster) cachedFrame() []byte {
	if p := b.lastFrame.Load(); p != nil {
		return *p
	}
	return nil
}

// Stop halts the broadcast loop and closes all clients. Idempotent.
func (b *Broadcaster) Stop() {
	b.stopOnce.Do(func() {
		close(b.stop)
	})
}

func (b *Broadcaster) run() {
	ticker := time.NewTicker(broadcastInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			b.closeAll()
			return
		case <-ticker.C:
			// Off the loop's goroutine: a build that blocks on a busy store
			// must not stop the loop from servicing stop/closeAll either.
			go b.refresh()
		}
	}
}

func (b *Broadcaster) broadcast(frame []byte) {
	b.mu.Lock()
	clients := make([]*wsClient, 0, len(b.clients))
	for c := range b.clients {
		clients = append(clients, c)
	}
	b.mu.Unlock()

	for _, c := range clients {
		select {
		case c.send <- frame:
		default:
			// Slow client: drop it rather than block the whole broadcast.
			b.removeClient(c)
		}
	}
}

func (b *Broadcaster) closeAll() {
	b.mu.Lock()
	clients := make([]*wsClient, 0, len(b.clients))
	for c := range b.clients {
		clients = append(clients, c)
	}
	b.clients = make(map[*wsClient]struct{})
	b.mu.Unlock()
	for _, c := range clients {
		close(c.done)
	}
}

func (b *Broadcaster) addClient(c *wsClient) {
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
}

func (b *Broadcaster) removeClient(c *wsClient) {
	b.mu.Lock()
	if _, ok := b.clients[c]; ok {
		delete(b.clients, c)
		close(c.done)
	}
	b.mu.Unlock()
}

// ServeHTTP upgrades the request and serves the read-only status stream.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote the error response.
		return
	}
	c := &wsClient{conn: conn, send: make(chan []byte, clientSendBuffer), done: make(chan struct{})}
	b.addClient(c)

	// Immediate push from the CACHE so a fresh connection does not wait a full
	// tick — and, more importantly, never waits on a subsystem this connection
	// has no business waiting for. If the cache is still cold, the client gets
	// the next built frame; a refresh is kicked off here so that arrives at the
	// earliest possible moment rather than at the next tick.
	if frame := b.cachedFrame(); frame != nil {
		select {
		case c.send <- frame:
		default:
		}
	} else {
		go b.refresh()
	}

	// Auxiliary lanes are sent to the new client regardless of generation: it
	// has seen nothing yet, and waiting for the numbers to change would leave
	// a freshly opened dashboard blank on a quiet node.
	for _, src := range b.sources {
		frame := src.cachedFrame()
		if frame == nil {
			if f, _, ok := src.get(); ok {
				frame = f
			}
		}
		if len(frame) == 0 {
			continue
		}
		select {
		case c.send <- frame:
		default:
		}
	}

	go b.writePump(c)
	b.readPump(c) // blocks until the client disconnects
}

// readPump services pong/close control frames and discards any client data.
// The status feed is one-directional, so there is nothing to act on; reading
// is required only to detect disconnects and keep the pong deadline fresh.
func (b *Broadcaster) readPump(c *wsClient) {
	defer func() {
		b.removeClient(c)
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(512)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (b *Broadcaster) writePump(c *wsClient) {
	ping := time.NewTicker(pingPeriod)
	defer func() {
		ping.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case <-c.done:
			// Hub dropped this client; send a courtesy close and exit.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case frame := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// checkOrigin permits same-origin browser handshakes (matching internal/api/
// ws.go), any loopback/dev origin (localhost / 127.0.0.1 / ::1 on any port,
// http or https), and any origin on the configured allowlist. Non-browser
// clients (no Origin header) are always allowed, matching gorilla's own
// documented secure default.
func (b *Broadcaster) checkOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if isLoopbackHost(u.Hostname()) && (u.Scheme == "http" || u.Scheme == "https") {
		return true
	}
	for _, allowed := range b.allowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func (s *frameSource) cachedFrame() []byte {
	if p := s.lastFrame.Load(); p != nil {
		return *p
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
