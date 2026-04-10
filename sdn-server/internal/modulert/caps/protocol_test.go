package caps

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
)

func TestProtocolCapHandlerRequestsLibp2pStreams(t *testing.T) {
	server, err := libp2p.New()
	if err != nil {
		t.Fatalf("create server host: %v", err)
	}
	defer server.Close()

	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("create client host: %v", err)
	}
	defer client.Close()

	const protocolID = protocol.ID("/sdn/test/protocol-request/1.0.0")
	server.SetStreamHandler(protocolID, func(stream network.Stream) {
		defer stream.Close()

		requestBytes, err := io.ReadAll(stream)
		if err != nil {
			t.Errorf("read request bytes: %v", err)
			return
		}

		if _, err := stream.Write(append([]byte("echo:"), requestBytes...)); err != nil {
			t.Errorf("write response bytes: %v", err)
		}
	})

	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), peerstore.PermanentAddrTTL)

	handler := newProtocolCapHandler(client)
	payload := []byte(`{"target":"` + server.ID().String() + `","protocolId":"` + string(protocolID) + `","payloadBase64":"aGVsbG8="}`)

	responseEnvelope, err := handler("protocol.request", payload)
	if err != nil {
		t.Fatalf("protocol.request returned error: %v", err)
	}

	var response struct {
		Ok     bool `json:"ok"`
		Result struct {
			Type   string `json:"__type"`
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !response.Ok {
		t.Fatalf("expected ok response, got: %s", string(responseEnvelope))
	}
	if response.Result.Type != "bytes" {
		t.Fatalf("expected bytes envelope, got %q", response.Result.Type)
	}

	responseBytes, err := base64.StdEncoding.DecodeString(response.Result.Base64)
	if err != nil {
		t.Fatalf("decode response base64: %v", err)
	}
	if string(responseBytes) != "echo:hello" {
		t.Fatalf("unexpected response bytes: %q", string(responseBytes))
	}
}
