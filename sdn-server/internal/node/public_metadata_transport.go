package node

// This connector carries only the explicitly supplied public HTTP resource.
// It does not interpret its schema, grant access to artifacts, or forward cookies.
import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const publicMetadataProtocol protocol.ID = "/space-data-network/public-metadata/1.0.0"
const publicMetadataLimit = 1 << 20

type boundedMetadataResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *boundedMetadataResponse) Header() http.Header { return w.header }
func (w *boundedMetadataResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedMetadataResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(p) > publicMetadataLimit {
		w.overflow = true
		return 0, fmt.Errorf("public metadata exceeds limit")
	}
	return w.body.Write(p)
}

type publicMetadataEntry struct {
	body []byte
	at   time.Time
}

func registerPublicMetadataTransport(h host.Host, mux *http.ServeMux, path string, handler http.Handler) {
	if h == nil || mux == nil {
		return
	}
	slots := make(chan struct{}, 8)
	h.SetStreamHandler(publicMetadataProtocol, func(stream network.Stream) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			_ = stream.Reset()
			return
		}
		defer stream.Close()
		_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
		req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(stream, 8192)))
		if err != nil || req.Method != http.MethodGet || req.URL.RequestURI() != path || req.ContentLength > 0 || len(req.TransferEncoding) != 0 {
			_ = stream.Reset()
			return
		}
		// Construct a fresh request; never pass remote authorization headers through.
		local, _ := http.NewRequest(http.MethodGet, path, nil)
		local.Header.Set("Accept", "application/json")
		w := &boundedMetadataResponse{header: make(http.Header)}
		handler.ServeHTTP(w, local)
		if w.overflow {
			_ = stream.Reset()
			return
		}
		status := w.status
		if status == 0 {
			status = http.StatusOK
		}
		response := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(w.body.Bytes())), ContentLength: int64(w.body.Len())}
		response.Header.Set("Content-Type", "application/json")
		_ = response.Write(stream)
	})
	var mu sync.Mutex
	cache := make(map[peer.ID]publicMetadataEntry)
	mux.HandleFunc("GET /.well-known/sdn/peers/{peer}/modules.pmm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=30")
		id, err := peer.Decode(r.PathValue("peer"))
		if err != nil {
			http.Error(w, "Invalid peer ID", http.StatusBadRequest)
			return
		}
		mu.Lock()
		entry, cached := cache[id]
		mu.Unlock()
		if cached && time.Since(entry.at) < time.Minute {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-SDN-Remote-Peer", id.String())
			_, _ = w.Write(entry.body)
			return
		}
		// This is a bounded read of a known connection, never a public dial proxy.
		if h.Network().Connectedness(id) != network.Connected {
			http.Error(w, "Provider is not connected", http.StatusServiceUnavailable)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Metadata requests are busy", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		stream, err := h.NewStream(ctx, id, publicMetadataProtocol)
		if err != nil {
			http.Error(w, "Provider metadata is unavailable", http.StatusBadGateway)
			return
		}
		defer stream.Close()
		stopReset := context.AfterFunc(ctx, func() { _ = stream.Reset() })
		defer stopReset()
		_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
		request, _ := http.NewRequest(http.MethodGet, "http://sdn-peer"+path, nil)
		request.Header.Set("Accept", "application/json")
		if err = request.Write(stream); err != nil {
			_ = stream.Reset()
			http.Error(w, "Provider metadata request failed", http.StatusBadGateway)
			return
		}
		response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(stream, publicMetadataLimit+8192)), request)
		if err != nil {
			_ = stream.Reset()
			http.Error(w, "Provider metadata response failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, publicMetadataLimit+1))
		if err != nil || len(body) > publicMetadataLimit {
			_ = stream.Reset()
			http.Error(w, "Provider metadata exceeds limits", http.StatusBadGateway)
			return
		}
		if response.StatusCode != http.StatusOK {
			http.Error(w, "Provider has no available module manifest", http.StatusBadGateway)
			return
		}
		mu.Lock()
		if len(cache) >= 32 {
			for key := range cache {
				delete(cache, key)
				break
			}
		}
		cache[id] = publicMetadataEntry{body: body, at: time.Now()}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-SDN-Remote-Peer", id.String())
		w.Header().Set("Cache-Control", "private, max-age=30")
		_, _ = w.Write(body)
	})
}
