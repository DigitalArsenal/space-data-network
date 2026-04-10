package caps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	defaultProtocolRequestTimeout = 15 * time.Second
	maxProtocolResponseBytes      = 1024 * 1024
)

// NewProtocolCapFactory returns a CapFactory for the "protocol_dial" capability.
// It allows modules to open outbound libp2p streams through the hosting node.
func NewProtocolCapFactory() modulert.CapFactory {
	return func(mod *modulert.Module) modulert.CapHandler {
		return newProtocolCapHandler(mod.RuntimeHost())
	}
}

func newProtocolCapHandler(runtimeHost libp2phost.Host) modulert.CapHandler {
	return func(operation string, payload []byte) ([]byte, error) {
		if operation != "protocol.request" {
			return errCapJSON(fmt.Sprintf("unknown protocol operation: %s", operation)), nil
		}
		if runtimeHost == nil {
			return errCapJSON("protocol host is not available"), nil
		}

		var request struct {
			Target        string `json:"target"`
			ProtocolID    string `json:"protocolId"`
			PayloadBase64 string `json:"payloadBase64"`
			TimeoutMs     int    `json:"timeoutMs"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return errCapJSON("invalid protocol request payload: " + err.Error()), nil
		}
		request.Target = strings.TrimSpace(request.Target)
		request.ProtocolID = strings.TrimSpace(request.ProtocolID)
		if request.Target == "" {
			return errCapJSON("missing target"), nil
		}
		if request.ProtocolID == "" {
			return errCapJSON("missing protocolId"), nil
		}

		requestPayload, err := decodeProtocolRequestPayload(request.PayloadBase64)
		if err != nil {
			return errCapJSON("invalid payloadBase64: " + err.Error()), nil
		}

		timeout := defaultProtocolRequestTimeout
		if request.TimeoutMs > 0 {
			timeout = time.Duration(request.TimeoutMs) * time.Millisecond
		}

		targetInfo, err := resolveProtocolTarget(runtimeHost, request.Target)
		if err != nil {
			return errCapJSON(err.Error()), nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if len(targetInfo.Addrs) > 0 {
			if err := runtimeHost.Connect(ctx, *targetInfo); err != nil {
				return errCapJSON("connect target: " + err.Error()), nil
			}
		}

		stream, err := runtimeHost.NewStream(ctx, targetInfo.ID, protocol.ID(request.ProtocolID))
		if err != nil {
			return errCapJSON("open stream: " + err.Error()), nil
		}
		defer stream.Close()

		_ = stream.SetDeadline(time.Now().Add(timeout))
		if len(requestPayload) > 0 {
			if _, err := stream.Write(requestPayload); err != nil {
				return errCapJSON("write request payload: " + err.Error()), nil
			}
		}
		if err := stream.CloseWrite(); err != nil {
			return errCapJSON("close write side: " + err.Error()), nil
		}

		responsePayload, err := io.ReadAll(io.LimitReader(stream, maxProtocolResponseBytes))
		if err != nil {
			return errCapJSON("read response payload: " + err.Error()), nil
		}
		return okCapRaw(responsePayload), nil
	}
}

func decodeProtocolRequestPayload(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(raw)
}

func resolveProtocolTarget(runtimeHost libp2phost.Host, rawTarget string) (*peer.AddrInfo, error) {
	if runtimeHost == nil {
		return nil, fmt.Errorf("protocol host is not available")
	}

	if strings.HasPrefix(rawTarget, "/") {
		addr, err := multiaddr.NewMultiaddr(rawTarget)
		if err != nil {
			return nil, fmt.Errorf("invalid target multiaddr: %w", err)
		}
		info, err := peer.AddrInfoFromP2pAddr(addr)
		if err == nil {
			return info, nil
		}

		infos, infosErr := peer.AddrInfosFromP2pAddrs(addr)
		if infosErr != nil || len(infos) == 0 {
			return nil, fmt.Errorf("invalid target multiaddr: %w", err)
		}
		parsedInfo := infos[0]
		return &parsedInfo, nil
	}

	peerID, err := peer.Decode(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid target peer id: %w", err)
	}

	return &peer.AddrInfo{
		ID:    peerID,
		Addrs: runtimeHost.Peerstore().Addrs(peerID),
	}, nil
}
