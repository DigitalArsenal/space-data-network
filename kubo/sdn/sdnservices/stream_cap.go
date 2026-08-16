package sdnservices

// Generic byte-stream connector — capability codes 20 "tls" / 22 "websocket" /
// 23 "tcp" (manifest.go capabilityNames), task sdn-stream-connector, Hermes
// ruling 2026-08-16 (graph/findings/airspace-tracks-program.md).
//
// ONE shared handler serves all three capability names under the "stream"
// hostcall prefix (capPrefixFromName), the same one-handler/many-capabilities
// shape as the storage_* family. The host is TRANSPORT ONLY: TCP dial, TLS
// dial, websocket dial + message framing. Every protocol/application concern
// — URL allowlists, feed auth, decoding, filtering, reconnect — lives in the
// guest module (WASM-not-Go boundary; host reconnect = application logic,
// rejected by ruling).
//
// Operations:
//
//	stream.open  — {"kind":"tcp"|"tls"|"websocket",
//	                "addr":"host:port" (tcp/tls) | "url":"ws(s)://..." (websocket),
//	                "headers":{...} (websocket only),
//	                "timeout_ms":30000, "max_frame_bytes":1048576}
//	               → {"handle":"s1","kind":"tcp"}
//	stream.send  — {"handle":"s1","data":"...","encoding":"utf8"|"base64"} → true
//	stream.close — {"handle":"s1"} → true
//
// Inbound delivery is HOST-PUSH via the proven pubsub pattern
// (drainSubscription → InvokeMethod): every event on a handle is delivered as
// InvokeMethod("on_stream_frame", json) with envelope
//
//	{"handle":"s1","event":"opened"|"frame"|"closed"|"error",
//	 "data":"<base64>","encoding":"base64","seq":N,"dropped":D,"reason":"..."}
//
// seq is 0-based per handle; dropped is the CUMULATIVE per-handle count of
// frames discarded by backpressure (drop-oldest on a bounded queue) — surfaced
// so loss is never silent. "closed" and "error" are terminal: the handle is
// freed and never delivers again.
//
// Fail closed: tcp/tls/websocket are modulert sensitiveCapabilities, so a
// bridge only receives a grant after the capability-policy gate approves the
// module's content hash; every call ALSO re-checks the grant for the exact
// kind in use (wss requires "websocket" alone — "tls" means raw TLS sockets).

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ipfs/kubo/sdn/modulert"
)

// Application-blind host limits (Hermes ruling: approved as proposed; no
// bytes/sec throttle in v1 — drop-oldest with a surfaced counter is the valve).
const (
	streamMaxHandles         = 8
	streamDefaultFrameBytes  = 1 << 20  // 1 MiB
	streamMaxFrameBytes      = 16 << 20 // absolute host ceiling
	streamTCPReadBufferBytes = 64 << 10 // read-chunk cap for tcp/tls "frames"
	streamQueueDepth         = 256
	streamIdleTimeout        = 5 * time.Minute
	streamDefaultTimeoutMs   = 30000
	streamInboundMethod      = "on_stream_frame"
)

// NewStreamCapFactory returns the BridgeCapFactory registered under the
// "tcp", "tls" and "websocket" capability names.
func NewStreamCapFactory() modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		h := newStreamCapHandler(
			func(capName string) bool { return bridge != nil && bridge.HasCapability(capName) },
			func(ctx context.Context, method string, payload []byte) error {
				if mod == nil {
					return fmt.Errorf("stream inbound delivery requires a module instance")
				}
				_, err := mod.InvokeMethod(ctx, method, payload)
				return err
			},
			func() context.Context {
				if mod != nil {
					return mod.Context()
				}
				return context.Background()
			},
		)
		return h.handle
	}
}

// streamConn is the minimal transport surface shared by net.Conn (tcp/tls)
// and *websocket.Conn.
type streamConn interface {
	readFrame(maxFrame int, idle time.Duration) ([]byte, error)
	writeFrame(data []byte) error
	close() error
}

type netStreamConn struct {
	conn net.Conn
	buf  []byte
}

func (c *netStreamConn) readFrame(maxFrame int, idle time.Duration) ([]byte, error) {
	if c.buf == nil {
		size := maxFrame
		if size > streamTCPReadBufferBytes {
			size = streamTCPReadBufferBytes
		}
		c.buf = make([]byte, size)
	}
	c.conn.SetReadDeadline(time.Now().Add(idle)) //nolint:errcheck
	n, err := c.conn.Read(c.buf)
	if n > 0 {
		out := make([]byte, n)
		copy(out, c.buf[:n])
		return out, err
	}
	return nil, err
}

func (c *netStreamConn) writeFrame(data []byte) error {
	_, err := c.conn.Write(data)
	return err
}

func (c *netStreamConn) close() error { return c.conn.Close() }

type wsStreamConn struct {
	conn *websocket.Conn
}

func (c *wsStreamConn) readFrame(maxFrame int, idle time.Duration) ([]byte, error) {
	c.conn.SetReadLimit(int64(maxFrame))
	c.conn.SetReadDeadline(time.Now().Add(idle)) //nolint:errcheck
	_, data, err := c.conn.ReadMessage()
	return data, err
}

func (c *wsStreamConn) writeFrame(data []byte) error {
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (c *wsStreamConn) close() error { return c.conn.Close() }

type streamEvent struct {
	event  string // "opened" | "frame" | "closed" | "error"
	data   []byte
	reason string
}

type streamHandle struct {
	id       string
	kind     string
	conn     streamConn
	maxFrame int

	sendMu sync.Mutex // websocket writes are not concurrency-safe

	queueMu  sync.Mutex
	queue    []streamEvent // bounded ring, drop-oldest (never drops terminal)
	notify   chan struct{}
	dropped  uint64 // cumulative, surfaced on every delivered event
	terminal bool

	closeOnce sync.Once
}

// enqueue appends an event under backpressure policy: when the bounded queue
// is full, the OLDEST non-terminal event is discarded and counted (Hermes:
// the counter must be surfaced or it's silent loss). Terminal events are
// always admitted.
func (sh *streamHandle) enqueue(ev streamEvent) {
	terminal := ev.event == "closed" || ev.event == "error"
	sh.queueMu.Lock()
	if sh.terminal {
		sh.queueMu.Unlock()
		return // already terminated — nothing may follow
	}
	if len(sh.queue) >= streamQueueDepth && !terminal {
		sh.queue = sh.queue[1:]
		sh.dropped++
	}
	sh.queue = append(sh.queue, ev)
	if terminal {
		sh.terminal = true
	}
	sh.queueMu.Unlock()
	select {
	case sh.notify <- struct{}{}:
	default:
	}
}

func (sh *streamHandle) closeConn() {
	sh.closeOnce.Do(func() { sh.conn.close() }) //nolint:errcheck
}

type streamCapHandler struct {
	has     func(capName string) bool
	invoke  func(ctx context.Context, method string, payload []byte) error
	modCtx  func() context.Context
	mu      sync.Mutex
	handles map[string]*streamHandle
	nextID  uint64
}

func newStreamCapHandler(
	has func(string) bool,
	invoke func(context.Context, string, []byte) error,
	modCtx func() context.Context,
) *streamCapHandler {
	return &streamCapHandler{
		has:     has,
		invoke:  invoke,
		modCtx:  modCtx,
		handles: make(map[string]*streamHandle),
	}
}

func (h *streamCapHandler) handle(operation string, payload []byte) ([]byte, error) {
	switch operation {
	case "stream.open":
		return h.open(payload), nil
	case "stream.send":
		return h.send(payload), nil
	case "stream.close":
		return h.closeHandle(payload), nil
	default:
		return errCapJSON(fmt.Sprintf("unknown stream operation: %s", operation)), nil
	}
}

func (h *streamCapHandler) open(payload []byte) []byte {
	var req struct {
		Kind          string            `json:"kind"`
		URL           string            `json:"url"`
		Addr          string            `json:"addr"`
		Headers       map[string]string `json:"headers"`
		TimeoutMs     int               `json:"timeout_ms"`
		MaxFrameBytes int               `json:"max_frame_bytes"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return errCapJSON("invalid stream.open payload: " + err.Error())
	}
	switch req.Kind {
	case "tcp", "tls", "websocket":
	default:
		return errCapJSON(`stream.open kind must be "tcp", "tls" or "websocket"`)
	}
	// POLICY (fail closed, defense in depth): the grant for the EXACT kind is
	// required. wss:// needs "websocket" alone; "tls" is raw TLS sockets only.
	if !h.has(req.Kind) {
		return errCapJSON(fmt.Sprintf("stream.open kind %q requires the %q capability grant", req.Kind, req.Kind))
	}
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = streamDefaultTimeoutMs
	}
	maxFrame := streamDefaultFrameBytes
	if req.MaxFrameBytes > 0 {
		maxFrame = req.MaxFrameBytes
	}
	if maxFrame > streamMaxFrameBytes {
		return errCapJSON(fmt.Sprintf("max_frame_bytes exceeds the host ceiling of %d bytes", streamMaxFrameBytes))
	}

	h.mu.Lock()
	if len(h.handles) >= streamMaxHandles {
		h.mu.Unlock()
		return errCapJSON(fmt.Sprintf("stream handle limit reached (%d concurrent per module)", streamMaxHandles))
	}
	h.nextID++
	id := fmt.Sprintf("s%d", h.nextID)
	h.mu.Unlock()

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	var conn streamConn
	var err error
	switch req.Kind {
	case "tcp":
		var c net.Conn
		c, err = net.DialTimeout("tcp", req.Addr, timeout)
		if err == nil {
			conn = &netStreamConn{conn: c}
		}
	case "tls":
		host, _, splitErr := net.SplitHostPort(req.Addr)
		if splitErr != nil {
			return errCapJSON("stream.open tls addr must be host:port: " + splitErr.Error())
		}
		d := &net.Dialer{Timeout: timeout}
		var c net.Conn
		c, err = tls.DialWithDialer(d, "tcp", req.Addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err == nil {
			conn = &netStreamConn{conn: c}
		}
	case "websocket":
		if req.URL == "" {
			return errCapJSON("stream.open websocket requires url")
		}
		if u, parseErr := url.Parse(req.URL); parseErr != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
			return errCapJSON("stream.open websocket url must be ws:// or wss://")
		}
		hdr := http.Header{}
		for k, v := range req.Headers {
			hdr.Set(k, v)
		}
		dialer := &websocket.Dialer{HandshakeTimeout: timeout}
		var c *websocket.Conn
		c, _, err = dialer.Dial(req.URL, hdr) //nolint:bodyclose
		if err == nil {
			conn = &wsStreamConn{conn: c}
		}
	}
	if err != nil {
		return errCapJSON(fmt.Sprintf("stream.open %s dial failed: %s", req.Kind, err.Error()))
	}

	sh := &streamHandle{
		id:       id,
		kind:     req.Kind,
		conn:     conn,
		maxFrame: maxFrame,
		notify:   make(chan struct{}, 1),
	}
	h.mu.Lock()
	h.handles[id] = sh
	h.mu.Unlock()

	sh.enqueue(streamEvent{event: "opened"})
	go h.readLoop(sh)
	go h.deliverLoop(sh)

	return okCapJSON(map[string]string{"handle": id, "kind": req.Kind})
}

func (h *streamCapHandler) send(payload []byte) []byte {
	var req struct {
		Handle   string `json:"handle"`
		Data     string `json:"data"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return errCapJSON("invalid stream.send payload: " + err.Error())
	}
	h.mu.Lock()
	sh := h.handles[req.Handle]
	h.mu.Unlock()
	if sh == nil {
		return errCapJSON(fmt.Sprintf("unknown stream handle: %q", req.Handle))
	}
	if !h.has(sh.kind) {
		return errCapJSON(fmt.Sprintf("stream.send requires the %q capability grant", sh.kind))
	}
	var data []byte
	if req.Encoding == "base64" {
		data = decodeBase64Cap(req.Data)
		if data == nil && req.Data != "" {
			return errCapJSON("stream.send data is not valid base64")
		}
	} else {
		data = []byte(req.Data)
	}
	if len(data) > sh.maxFrame {
		return errCapJSON(fmt.Sprintf("stream.send frame exceeds the %d-byte limit for this handle", sh.maxFrame))
	}
	sh.sendMu.Lock()
	err := sh.conn.writeFrame(data)
	sh.sendMu.Unlock()
	if err != nil {
		return errCapJSON("stream.send failed: " + err.Error())
	}
	return okCapJSON(true)
}

func (h *streamCapHandler) closeHandle(payload []byte) []byte {
	var req struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return errCapJSON("invalid stream.close payload: " + err.Error())
	}
	h.mu.Lock()
	sh := h.handles[req.Handle]
	h.mu.Unlock()
	if sh == nil {
		return errCapJSON(fmt.Sprintf("unknown stream handle: %q", req.Handle))
	}
	sh.enqueue(streamEvent{event: "closed", reason: "closed by module"})
	sh.closeConn()
	return okCapJSON(true)
}

// readLoop pulls transport frames and enqueues them under drop-oldest
// backpressure, terminating with exactly one "closed" or "error" event.
func (h *streamCapHandler) readLoop(sh *streamHandle) {
	for {
		data, err := sh.conn.readFrame(sh.maxFrame, streamIdleTimeout)
		if len(data) > 0 {
			sh.enqueue(streamEvent{event: "frame", data: data})
		}
		if err != nil {
			switch {
			case isStreamIdleTimeout(err):
				sh.enqueue(streamEvent{event: "closed", reason: "idle"})
			case isStreamEOF(err):
				sh.enqueue(streamEvent{event: "closed", reason: "eof"})
			default:
				sh.enqueue(streamEvent{event: "error", reason: err.Error()})
			}
			sh.closeConn()
			return
		}
	}
}

// deliverLoop drains the handle's queue into the guest's declared inbound
// method — the same host-push contract as pubsub's drainSubscription. It stops
// after the terminal event or when the module's lifecycle context ends.
func (h *streamCapHandler) deliverLoop(sh *streamHandle) {
	ctx := h.modCtx()
	var seq uint64
	for {
		sh.queueMu.Lock()
		var ev *streamEvent
		if len(sh.queue) > 0 {
			e := sh.queue[0]
			sh.queue = sh.queue[1:]
			ev = &e
		}
		dropped := sh.dropped
		terminal := sh.terminal && len(sh.queue) == 0 && ev == nil
		sh.queueMu.Unlock()

		if ev == nil {
			if terminal {
				h.forget(sh)
				return
			}
			select {
			case <-ctx.Done():
				sh.closeConn()
				h.forget(sh)
				return
			case <-sh.notify:
			}
			continue
		}

		env := map[string]interface{}{
			"handle":   sh.id,
			"event":    ev.event,
			"encoding": "base64",
			"seq":      seq,
			"dropped":  dropped,
		}
		if ev.data != nil {
			env["data"] = base64.StdEncoding.EncodeToString(ev.data)
		}
		if ev.reason != "" {
			env["reason"] = ev.reason
		}
		payload, _ := json.Marshal(env)
		seq++
		// Best-effort, like on_pubsub_message: a guest without the method
		// simply never sees inbound frames.
		h.invoke(ctx, streamInboundMethod, payload) //nolint:errcheck

		if ev.event == "closed" || ev.event == "error" {
			h.forget(sh)
			return
		}
	}
}

func (h *streamCapHandler) forget(sh *streamHandle) {
	h.mu.Lock()
	if h.handles[sh.id] == sh {
		delete(h.handles, sh.id)
	}
	h.mu.Unlock()
}

// openHandleCount is a test seam.
func (h *streamCapHandler) openHandleCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.handles)
}

func isStreamIdleTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func isStreamEOF(err error) bool {
	if err == nil {
		return false
	}
	if err.Error() == "EOF" {
		return true
	}
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived)
}
