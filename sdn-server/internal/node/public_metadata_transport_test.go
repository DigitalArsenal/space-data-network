package node

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestPublicMetadataTransport(t *testing.T) {
	a, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var calls atomic.Int32
	payload := `{"opaque":"signed module publication"}`
	registerPublicMetadataTransport(b, http.NewServeMux(), "/.well-known/sdn/modules.pmm", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("forwarded credentials")
		}
		io.WriteString(w, payload)
	}))
	mux := http.NewServeMux()
	registerPublicMetadataTransport(a, mux, "/.well-known/sdn/modules.pmm", http.NotFoundHandler())
	path := "/.well-known/sdn/peers/" + b.ID().String() + "/modules.pmm"
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Cookie", "test=do-not-forward")
		r.Header.Set("Authorization", "test-do-not-forward")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", path); w.Code != 503 {
		t.Fatalf("disconnected: %d", w.Code)
	}
	if w := request("GET", "/.well-known/sdn/peers/invalid/modules.pmm"); w.Code != 400 {
		t.Fatalf("invalid: %d", w.Code)
	}
	if err := a.Connect(context.Background(), peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := request("GET", path)
		if w.Code != 200 || w.Body.String() != payload || w.Header().Get("X-SDN-Remote-Peer") != b.ID().String() {
			t.Fatalf("relay: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache calls: %d", calls.Load())
	}
	for _, method := range []string{"POST", "HEAD"} {
		if w := request(method, path); w.Code != 405 {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
}

func TestPublicMetadataResponseBound(t *testing.T) {
	w := &boundedMetadataResponse{header: make(http.Header)}
	if _, err := w.Write([]byte(strings.Repeat("x", publicMetadataLimit))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil || !w.overflow || w.body.Len() != publicMetadataLimit {
		t.Fatal("metadata limit not enforced")
	}
}
