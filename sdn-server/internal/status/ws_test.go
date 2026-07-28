package status

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

func TestWSFirstFrameIsNSTBinary(t *testing.T) {
	frame := buildFixtureSet(t)
	b := NewBroadcaster(func() []byte { return frame }, nil)
	b.Start()
	defer b.Stop()

	srv := httptest.NewServer(http.HandlerFunc(b.ServeHTTP))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("first frame type = %d, want BinaryMessage(%d)", msgType, websocket.BinaryMessage)
	}
	if !nst.SizePrefixedNodeStatusSetBufferHasIdentifier(data) {
		t.Fatalf("first frame missing $NST size-prefixed identifier")
	}
	set := nst.GetSizePrefixedRootAsNodeStatusSet(data, 0)
	if set.NodesLength() != 2 {
		t.Errorf("decoded NodesLength = %d, want 2", set.NodesLength())
	}
	var self nst.NodeStatus
	if !set.Nodes(&self, 0) || !self.IsSelf() {
		t.Error("decoded self node missing or IS_SELF false")
	}
}

func TestWSBroadcastTickDelivers(t *testing.T) {
	frame := buildFixtureSet(t)
	// Broadcaster with a very short interval so the test does not wait 5s.
	b := NewBroadcaster(func() []byte { return frame }, nil)
	b.Start()
	defer b.Stop()

	srv := httptest.NewServer(http.HandlerFunc(b.ServeHTTP))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Read the immediate frame, then at least one ticker-driven frame.
	_ = conn.SetReadDeadline(time.Now().Add(broadcastInterval + 3*time.Second))
	for i := 0; i < 2; i++ {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("frame %d type = %d, want binary", i, mt)
		}
		if !nst.SizePrefixedNodeStatusSetBufferHasIdentifier(data) {
			t.Fatalf("frame %d missing $NST identifier", i)
		}
	}
}

func TestCheckOrigin(t *testing.T) {
	b := NewBroadcaster(nil, []string{"https://sdn.spaceaware.io"})
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"no origin (non-browser)", "", "node.example:5001", true},
		{"same origin", "https://node.example:5001", "node.example:5001", true},
		{"loopback http any port", "http://localhost:8080", "node.example:5001", true},
		{"loopback ip", "http://127.0.0.1:3000", "node.example:5001", true},
		{"allowlisted", "https://sdn.spaceaware.io", "node.example:5001", true},
		{"cross-origin rejected", "https://evil.example", "node.example:5001", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws/status", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := b.checkOrigin(r); got != tc.want {
				t.Errorf("checkOrigin(origin=%q, host=%q) = %v, want %v", tc.origin, tc.host, got, tc.want)
			}
		})
	}
}

// The dashboard went dark for every visitor on 2026-07-28 with this exact
// shape: clients completed the WebSocket upgrade and then received NOTHING.
// The cause was building the frame ON the connect path — the builder reads the
// peer registry, the EPM service and the record store, and the store was held
// for minutes by a catalog ingest, so every connection blocked before its
// writer ever started.
//
// A status feed is asked "is this node working?" precisely when the node is
// busy. It must answer from what it last knew rather than not at all.
func TestWSServesCachedFrameWhileTheBuilderIsBlocked(t *testing.T) {
	frame := buildFixtureSet(t)

	release := make(chan struct{})
	var calls int32
	b := NewBroadcaster(func() []byte {
		if atomic.AddInt32(&calls, 1) == 1 {
			return frame // the warm-up build succeeds and fills the cache
		}
		<-release // every later build blocks, as a busy store would
		return frame
	}, nil)
	b.Start()
	defer func() { close(release); b.Stop() }()

	// Let the warm-up build land, then let a tick start a build that blocks.
	deadline := time.Now().Add(3 * time.Second)
	for b.cachedFrame() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.cachedFrame() == nil {
		t.Fatal("broadcaster never warmed its cache")
	}
	go b.refresh() // this one blocks in the builder and never returns

	srv := httptest.NewServer(http.HandlerFunc(b.ServeHTTP))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("client received no frame while the builder was blocked: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("frame type = %d, want BinaryMessage(%d)", msgType, websocket.BinaryMessage)
	}
	if !nst.SizePrefixedNodeStatusSetBufferHasIdentifier(data) {
		t.Fatal("cached frame is not a $NST frame")
	}
}

// A build that outlives its tick must not queue more of itself behind it.
func TestWSRefreshDoesNotOverlap(t *testing.T) {
	release := make(chan struct{})
	var concurrent, maxConcurrent int32
	b := NewBroadcaster(func() []byte {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			prev := atomic.LoadInt32(&maxConcurrent)
			if n <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&concurrent, -1)
		return nil
	}, nil)

	for i := 0; i < 5; i++ {
		go b.refresh()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	time.Sleep(200 * time.Millisecond)

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Fatalf("concurrent builds = %d, want 1 — a slow build must not pile up", got)
	}
}
