package sdnservices

// Tests for the generic byte-stream connector (task sdn-stream-connector).
// The handler is exercised directly (test-seam constructor) against REAL local
// TCP and websocket servers — computable-outcome tests only: gating, delivery
// order/content, backpressure accounting, terminal semantics, limits.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ipfs/kubo/sdn/modulert"
)

type streamTestSink struct {
	mu     sync.Mutex
	events []map[string]interface{}
	notify chan struct{}
}

func newStreamTestSink() *streamTestSink {
	return &streamTestSink{notify: make(chan struct{}, 64)}
}

func (s *streamTestSink) invoke(_ context.Context, method string, payload []byte) error {
	if method != streamInboundMethod {
		return fmt.Errorf("unexpected inbound method %q", method)
	}
	var env map[string]interface{}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, env)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitFor blocks until pred(all events so far) is true or the deadline hits.
func (s *streamTestSink) waitFor(t *testing.T, what string, pred func([]map[string]interface{}) bool) []map[string]interface{} {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		evs := append([]map[string]interface{}(nil), s.events...)
		s.mu.Unlock()
		if pred(evs) {
			return evs
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s; events: %v", what, evs)
		case <-s.notify:
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func grantAll(string) bool  { return true }
func grantNone(string) bool { return false }

func newTestStreamHandler(has func(string) bool, sink *streamTestSink) *streamCapHandler {
	return newStreamCapHandler(has, sink.invoke, context.Background)
}

func mustCall(t *testing.T, h *streamCapHandler, op string, req interface{}) map[string]interface{} {
	t.Helper()
	payload, _ := json.Marshal(req)
	out, err := h.handle(op, payload)
	if err != nil {
		t.Fatalf("%s returned transport error: %v", op, err)
	}
	var env map[string]interface{}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("%s returned non-JSON envelope: %v", op, err)
	}
	return env
}

func capErrMessage(env map[string]interface{}) string {
	if e, ok := env["error"].(map[string]interface{}); ok {
		return fmt.Sprintf("%v", e["message"])
	}
	return ""
}

// startTCPEcho starts a TCP server that echoes every read back to the client
// and returns its address.
func startTCPEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestStreamOpenFailsClosedWithoutGrant(t *testing.T) {
	sink := newStreamTestSink()
	h := newTestStreamHandler(grantNone, sink)
	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": "127.0.0.1:1"})
	if env["ok"] == true {
		t.Fatal("stream.open succeeded without the tcp grant")
	}
	if msg := capErrMessage(env); !strings.Contains(msg, `"tcp" capability grant`) {
		t.Fatalf("refusal must name the missing grant, got %q", msg)
	}
}

func TestStreamUnknownOperationRefused(t *testing.T) {
	h := newTestStreamHandler(grantAll, newStreamTestSink())
	env := mustCall(t, h, "stream.tcp_open", map[string]interface{}{})
	if env["ok"] == true {
		t.Fatal("unknown operation must be refused")
	}
}

func TestStreamTCPOpenSendEchoClose(t *testing.T) {
	addr := startTCPEcho(t)
	sink := newStreamTestSink()
	h := newTestStreamHandler(grantAll, sink)

	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": addr})
	if env["ok"] != true {
		t.Fatalf("stream.open failed: %v", env)
	}
	handle := env["result"].(map[string]interface{})["handle"].(string)

	// "opened" arrives first, seq 0.
	evs := sink.waitFor(t, "opened", func(e []map[string]interface{}) bool { return len(e) >= 1 })
	if evs[0]["event"] != "opened" || evs[0]["seq"].(float64) != 0 || evs[0]["handle"] != handle {
		t.Fatalf("first event must be opened seq=0 on %s, got %v", handle, evs[0])
	}

	if env := mustCall(t, h, "stream.send", map[string]interface{}{"handle": handle, "data": "AIVDM,test", "encoding": "utf8"}); env["ok"] != true {
		t.Fatalf("stream.send failed: %v", env)
	}
	evs = sink.waitFor(t, "echo frame", func(e []map[string]interface{}) bool {
		for _, ev := range e {
			if ev["event"] == "frame" {
				return true
			}
		}
		return false
	})
	var frame map[string]interface{}
	for _, ev := range evs {
		if ev["event"] == "frame" {
			frame = ev
			break
		}
	}
	raw, err := base64.StdEncoding.DecodeString(frame["data"].(string))
	if err != nil || string(raw) != "AIVDM,test" {
		t.Fatalf("frame payload mismatch: %q err=%v", raw, err)
	}
	if frame["encoding"] != "base64" || frame["dropped"].(float64) != 0 {
		t.Fatalf("frame envelope mismatch: %v", frame)
	}

	if env := mustCall(t, h, "stream.close", map[string]interface{}{"handle": handle}); env["ok"] != true {
		t.Fatalf("stream.close failed: %v", env)
	}
	sink.waitFor(t, "terminal closed", func(e []map[string]interface{}) bool {
		if len(e) == 0 {
			return false
		}
		last := e[len(e)-1]
		return last["event"] == "closed" && last["reason"] == "closed by module"
	})
	// Handle is freed after the terminal event.
	deadline := time.Now().Add(2 * time.Second)
	for h.openHandleCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("handle not freed after terminal event")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if env := mustCall(t, h, "stream.send", map[string]interface{}{"handle": handle, "data": "x"}); env["ok"] == true {
		t.Fatal("send on a freed handle must be refused")
	}
}

func TestStreamWebsocketEcho(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var sawHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("X-Feed-Auth")
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	sink := newStreamTestSink()
	h := newTestStreamHandler(grantAll, sink)
	env := mustCall(t, h, "stream.open", map[string]interface{}{
		"kind": "websocket", "url": wsURL,
		"headers": map[string]string{"X-Feed-Auth": "module-owned-token"},
	})
	if env["ok"] != true {
		t.Fatalf("websocket open failed: %v", env)
	}
	handle := env["result"].(map[string]interface{})["handle"].(string)
	if sawHeader != "module-owned-token" {
		t.Fatalf("open headers not forwarded, got %q", sawHeader)
	}

	msg := []byte{0x00, 0x01, 0xFE, 0xFF} // binary survives round-trip
	mustCall(t, h, "stream.send", map[string]interface{}{
		"handle": handle, "data": base64.StdEncoding.EncodeToString(msg), "encoding": "base64",
	})
	evs := sink.waitFor(t, "ws echo frame", func(e []map[string]interface{}) bool {
		for _, ev := range e {
			if ev["event"] == "frame" {
				return true
			}
		}
		return false
	})
	for _, ev := range evs {
		if ev["event"] == "frame" {
			raw, _ := base64.StdEncoding.DecodeString(ev["data"].(string))
			if string(raw) != string(msg) {
				t.Fatalf("binary payload corrupted: %x", raw)
			}
		}
	}
	mustCall(t, h, "stream.close", map[string]interface{}{"handle": handle})
}

func TestStreamServerCloseDeliversTerminal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("last words")) //nolint:errcheck
		conn.Close()
	}()

	sink := newStreamTestSink()
	h := newTestStreamHandler(grantAll, sink)
	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": ln.Addr().String()})
	if env["ok"] != true {
		t.Fatalf("open failed: %v", env)
	}
	evs := sink.waitFor(t, "eof terminal", func(e []map[string]interface{}) bool {
		if len(e) == 0 {
			return false
		}
		last := e[len(e)-1]
		return last["event"] == "closed" && last["reason"] == "eof"
	})
	// The pre-close frame must still have been delivered, in order.
	foundFrame := false
	for _, ev := range evs {
		if ev["event"] == "frame" {
			raw, _ := base64.StdEncoding.DecodeString(ev["data"].(string))
			if string(raw) == "last words" {
				foundFrame = true
			}
		}
	}
	if !foundFrame {
		t.Fatalf("frame before eof was lost: %v", evs)
	}
}

func TestStreamHandleLimit(t *testing.T) {
	addr := startTCPEcho(t)
	sink := newStreamTestSink()
	h := newTestStreamHandler(grantAll, sink)
	for i := 0; i < streamMaxHandles; i++ {
		env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": addr})
		if env["ok"] != true {
			t.Fatalf("open %d failed: %v", i, env)
		}
	}
	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": addr})
	if env["ok"] == true {
		t.Fatalf("open beyond the %d-handle limit must be refused", streamMaxHandles)
	}
	if msg := capErrMessage(env); !strings.Contains(msg, "handle limit") {
		t.Fatalf("refusal must name the limit, got %q", msg)
	}
}

func TestStreamOversizeSendRefused(t *testing.T) {
	addr := startTCPEcho(t)
	sink := newStreamTestSink()
	h := newTestStreamHandler(grantAll, sink)
	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": addr, "max_frame_bytes": 16})
	handle := env["result"].(map[string]interface{})["handle"].(string)
	env = mustCall(t, h, "stream.send", map[string]interface{}{"handle": handle, "data": strings.Repeat("a", 17)})
	if env["ok"] == true {
		t.Fatal("oversize send must be refused, never truncated")
	}
	if msg := capErrMessage(env); !strings.Contains(msg, "16-byte limit") {
		t.Fatalf("refusal must name the limit, got %q", msg)
	}
}

func TestStreamMaxFrameCeilingRefusedAtOpen(t *testing.T) {
	h := newTestStreamHandler(grantAll, newStreamTestSink())
	env := mustCall(t, h, "stream.open", map[string]interface{}{
		"kind": "tcp", "addr": "127.0.0.1:1", "max_frame_bytes": streamMaxFrameBytes + 1,
	})
	if env["ok"] == true {
		t.Fatal("max_frame_bytes above the host ceiling must be refused")
	}
}

// TestStreamBackpressureDropOldestCounted floods the queue while delivery is
// stalled and verifies drop-oldest with a surfaced cumulative counter — the
// Hermes "never silent loss" requirement.
func TestStreamBackpressureDropOldestCounted(t *testing.T) {
	sh := &streamHandle{id: "s1", kind: "tcp", maxFrame: streamDefaultFrameBytes, notify: make(chan struct{}, 1)}
	total := streamQueueDepth + 40
	for i := 0; i < total; i++ {
		sh.enqueue(streamEvent{event: "frame", data: []byte(fmt.Sprintf("%d", i))})
	}
	sh.queueMu.Lock()
	defer sh.queueMu.Unlock()
	if len(sh.queue) != streamQueueDepth {
		t.Fatalf("queue depth = %d, want %d", len(sh.queue), streamQueueDepth)
	}
	if sh.dropped != uint64(total-streamQueueDepth) {
		t.Fatalf("dropped = %d, want %d (cumulative, counted)", sh.dropped, total-streamQueueDepth)
	}
	// Oldest were dropped: head of queue is frame #40.
	if string(sh.queue[0].data) != "40" {
		t.Fatalf("drop-oldest violated: head = %s", sh.queue[0].data)
	}
	// A terminal event is always admitted even on a full queue.
	sh.queueMu.Unlock()
	sh.enqueue(streamEvent{event: "closed", reason: "eof"})
	sh.queueMu.Lock()
	if !sh.terminal || sh.queue[len(sh.queue)-1].event != "closed" {
		t.Fatal("terminal event must always be admitted")
	}
	// Nothing may follow a terminal event.
	sh.queueMu.Unlock()
	sh.enqueue(streamEvent{event: "frame", data: []byte("late")})
	sh.queueMu.Lock()
	if sh.queue[len(sh.queue)-1].event != "closed" {
		t.Fatal("events after terminal must be discarded")
	}
}

func TestStreamKindGatingIsExact(t *testing.T) {
	// websocket grant alone must NOT open tcp (and vice versa) — per-kind
	// re-check, wss needs websocket only.
	onlyWS := func(c string) bool { return c == "websocket" }
	h := newTestStreamHandler(onlyWS, newStreamTestSink())
	env := mustCall(t, h, "stream.open", map[string]interface{}{"kind": "tcp", "addr": "127.0.0.1:1"})
	if env["ok"] == true {
		t.Fatal("websocket grant must not satisfy kind tcp")
	}
}

// TestStreamCapThroughRealBridge exercises the factory through a REAL
// HostBridge grant set — the same HasCapability path a loaded WASM module's
// hostcalls traverse — against a live local websocket server. (Full guest
// wasm e2e lives with the module-SDK op-surface task; see
// tests/modules/stream-echo.)
func TestStreamCapThroughRealBridge(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	factory := NewStreamCapFactory()

	// Grant absent -> fail closed through the real bridge.
	denied := factory(nil, modulert.NewHostBridge(&modulert.NodeContext{}, []string{"http"}))
	out, err := denied("stream.open", []byte(`{"kind":"websocket","url":"`+wsURL+`"}`))
	if err != nil {
		t.Fatalf("handler transport error: %v", err)
	}
	var env map[string]interface{}
	json.Unmarshal(out, &env) //nolint:errcheck
	if env["ok"] == true {
		t.Fatal("bridge without the websocket grant opened a stream")
	}

	// Grant present -> open + send + close succeed (delivery is exercised by
	// the sink-level tests; a nil module makes inbound push a no-op).
	h := factory(nil, modulert.NewHostBridge(&modulert.NodeContext{}, []string{"websocket"}))
	out, _ = h("stream.open", []byte(`{"kind":"websocket","url":"`+wsURL+`"}`))
	env = map[string]interface{}{}
	json.Unmarshal(out, &env) //nolint:errcheck
	if env["ok"] != true {
		t.Fatalf("granted open failed: %s", out)
	}
	handle := env["result"].(map[string]interface{})["handle"].(string)
	out, _ = h("stream.send", []byte(`{"handle":"`+handle+`","data":"ping"}`))
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("send failed: %s", out)
	}
	out, _ = h("stream.close", []byte(`{"handle":"`+handle+`"}`))
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("close failed: %s", out)
	}
}
