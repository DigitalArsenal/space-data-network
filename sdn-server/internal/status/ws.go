package status

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// Broadcaster is the public, read-only /ws/status hub: it upgrades incoming
// connections, pushes the current status frame immediately, then fans out a
// fresh frame to every client on a 5s ticker. There is no authentication and
// no publish path — client messages are read only to service pong/close.
type Broadcaster struct {
	snapshot       SnapshotFunc
	allowedOrigins []string
	upgrader       websocket.Upgrader

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

// Start launches the 5s broadcast loop. Idempotent.
func (b *Broadcaster) Start() {
	b.startOnce.Do(func() {
		go b.run()
	})
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
			if b.snapshot == nil {
				continue
			}
			frame := b.snapshot()
			if frame == nil {
				continue
			}
			b.broadcast(frame)
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

	// Immediate push so a fresh connection does not wait a full tick.
	if b.snapshot != nil {
		if frame := b.snapshot(); frame != nil {
			select {
			case c.send <- frame:
			default:
			}
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
