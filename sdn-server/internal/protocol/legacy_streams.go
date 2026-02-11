package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

const (
	legacyReadDeadline = 3 * time.Second
	legacyMaxReadBytes = 4096
)

type idExchangeResponse struct {
	PeerID    string `json:"peer_id"`
	Protocol  string `json:"protocol"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

// HandleLegacyIDExchange supports the historical ID exchange test protocol used
// by older browser integration scripts.
func HandleLegacyIDExchange(s network.Stream) {
	defer s.Close()

	_ = s.SetReadDeadline(time.Now().Add(legacyReadDeadline))
	buf := make([]byte, legacyMaxReadBytes)
	n, err := s.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) && !isTimeoutError(err) {
		log.Debugf("Legacy ID exchange read failed from %s: %v", s.Conn().RemotePeer().ShortString(), err)
		return
	}

	msg := "hello"
	if n > 0 {
		trimmed := bytes.TrimSpace(buf[:n])
		if len(trimmed) > 0 {
			msg = string(trimmed)
		}
	}

	resp := idExchangeResponse{
		PeerID:    s.Conn().LocalPeer().String(),
		Protocol:  string(IDExchangeProtoID),
		Message:   fmt.Sprintf("ack:%s", msg),
		Timestamp: time.Now().Unix(),
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		log.Debugf("Legacy ID exchange marshal failed: %v", err)
		return
	}

	if _, err := s.Write(payload); err != nil {
		log.Debugf("Legacy ID exchange write failed to %s: %v", s.Conn().RemotePeer().ShortString(), err)
	}
}

// HandleLegacyChat echoes incoming line-based chat traffic for compatibility
// with the historical chat protocol smoke tests.
func HandleLegacyChat(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(io.LimitReader(s, legacyMaxReadBytes))
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		log.Debugf("Legacy chat read failed from %s: %v", s.Conn().RemotePeer().ShortString(), err)
		return
	}

	line = string(bytes.TrimSpace([]byte(line)))
	if line == "" {
		line = "hello"
	}

	if _, err := s.Write([]byte("echo:" + line + "\n")); err != nil {
		log.Debugf("Legacy chat write failed to %s: %v", s.Conn().RemotePeer().ShortString(), err)
	}
}

func isTimeoutError(err error) bool {
	type timeout interface {
		Timeout() bool
	}
	t, ok := err.(timeout)
	return ok && t.Timeout()
}
