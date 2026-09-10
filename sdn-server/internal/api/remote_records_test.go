package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

func TestRemoteRecordsConnector(t *testing.T) {
	local, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	remote, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if err := local.Connect(context.Background(), peer.AddrInfo{ID: remote.ID(), Addrs: remote.Addrs()}); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 1)
	response := []byte{0, 0, 0, 2, '{', '}', 4, 3, 2, 1}
	remote.SetStreamHandler(protocol.FlatSQLSyncProtocolID, func(s network.Stream) {
		defer s.Close()
		body, _ := io.ReadAll(s)
		received <- body
		_, _ = s.Write(response)
	})
	h := &CoreAPIHandler{h2pHost: local}
	// An established connection remains usable when identify clears stale advertised addresses.
	local.Peerstore().ClearAddrs(remote.ID())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/data/remote/{peerID}", h.handleRemoteRecords)
	frame := func(payload string) []byte {
		body := []byte(payload)
		result := make([]byte, len(body)+4)
		binary.BigEndian.PutUint32(result, uint32(len(body)))
		copy(result[4:], body)
		return result
	}
	body := frame(`{"op":"read_chunk","schema":"CAT.fbs","source_name":"satcat","cursor":"opaque","limit":100}`)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/data/remote/"+remote.ID().String(), bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), response) {
		t.Fatalf("response %d %q", w.Code, w.Body.Bytes())
	}
	if !bytes.Equal(<-received, body) {
		t.Fatal("connector changed the request")
	}
	if w.Header().Get("X-SDN-Remote-Peer") != remote.ID().String() {
		t.Fatal("wrong remote identity")
	}
	for _, payload := range []string{
		`{"op":"list_published_shards","publication_limit":1000,"publication_offset":0}`,
		`{"op":"read_published_shard","cid":"bafyknownpublication","byte_offset":0,"byte_length":4194304}`,
	} {
		requestBody := frame(payload)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, r.URL.String(), bytes.NewReader(requestBody)))
		if w.Code != http.StatusOK || !bytes.Equal(<-received, requestBody) {
			t.Fatalf("read operation failed: %d", w.Code)
		}
	}
	for _, payload := range []string{`{"op":"list_published_shards","publication_limit":0}`, `{"op":"read_published_shard","cid":"bafyknownpublication","byte_length":4194305}`, `{"op":"read_published_shard","cid":"bafyknownpublication","byte_length":0}`, `{"op":"ack_progress","limit":100}`, `{"op":"read_chunk","limit":1001}`, `{"op":"read_chunk","limit":0}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, r.URL.String(), bytes.NewReader(frame(payload))))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe request %s: %d", payload, w.Code)
		}
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/data/remote/invalid", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatal("invalid peer accepted")
	}
	remote.SetStreamHandler(protocol.FlatSQLSyncProtocolID, func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(io.Discard, s)
		_, _ = s.Write(make([]byte, remoteRecordsMaxResponse+1))
	})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, r.URL.String(), bytes.NewReader(body)))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("oversized response accepted: %d", w.Code)
	}
	var message map[string]any
	if json.Unmarshal(w.Body.Bytes(), &message) != nil {
		t.Fatal("invalid error body")
	}
}
