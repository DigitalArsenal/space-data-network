package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// TestRemoteRecordsRelayResetCancelsProducerStream proves over two real libp2p
// hosts that the remote-records relay keeps its write side open while awaiting
// the reply, so cancelling the HTTP request delivers a stream reset (not an
// early io.EOF) to the producer's read-chunk handler (stack issue 1093).
func TestRemoteRecordsRelayResetCancelsProducerStream(t *testing.T) {
	producer, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	relayHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer relayHost.Close()

	if err := relayHost.Connect(context.Background(), peer.AddrInfo{ID: producer.ID(), Addrs: producer.Addrs()}); err != nil {
		t.Fatal(err)
	}

	gotRequest := make(chan struct{}, 1)
	readResult := make(chan error, 1)
	producer.SetStreamHandler(libp2pprotocol.ID(syncwire.ProtocolID(syncwire.OpReadChunk)), func(s network.Stream) {
		defer s.Close()
		if _, err := syncwire.ReadRequest(s); err != nil {
			readResult <- err
			return
		}
		gotRequest <- struct{}{}
		// Watch the open stream like protocol.watchStream does: a half-close
		// returns io.EOF immediately, while keeping write open blocks until
		// the requester resets the stream (non-EOF error).
		var buf [16]byte
		_, err := s.Read(buf[:])
		readResult <- err
	})

	h := &CoreAPIHandler{h2pHost: relayHost}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/data/remote/{peerID}/{op}", h.handleRemoteRecords)

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := syncwire.BuildQRP(syncwire.QRPFields{
		Kind:       syncwire.QRPKindRequest,
		SchemaName: "OMM.fbs",
		Limit:      10,
	})
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/remote/"+producer.ID().String()+"/read-chunk", bytes.NewReader(body)).WithContext(reqCtx)
	httpReq.Header.Set("Content-Type", StreamContentType)

	done := make(chan int, 1)
	go func() {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httpReq)
		done <- res.Code
	}()

	select {
	case <-gotRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("producer never received the read-chunk request")
	}

	// While the HTTP request is alive, the producer must NOT see io.EOF.
	select {
	case err := <-readResult:
		t.Fatalf("producer read ended before requester cancelled (half-closed stream): %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Cancelling the HTTP request resets the libp2p stream immediately.
	cancel()

	select {
	case err := <-readResult:
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("producer saw %v after cancel, want non-EOF stream reset", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("producer never observed stream reset after requester cancelled")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay handler did not return after cancel")
	}
}
