package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

func main() {
	var peerID string
	var addrList repeatedFlag
	var probeBytes int64
	var timeout time.Duration
	flag.StringVar(&peerID, "peer", "", "target libp2p peer ID")
	flag.Var(&addrList, "addr", "target multiaddr; may be repeated")
	flag.Int64Var(&probeBytes, "probe-bytes", 64*1024*1024, "wire-speed probe bytes")
	flag.DurationVar(&timeout, "timeout", 2*time.Minute, "dial and stream timeout")
	flag.Parse()

	if peerID == "" || len(addrList) == 0 {
		fmt.Fprintln(os.Stderr, "--peer and at least one --addr are required")
		os.Exit(2)
	}
	if err := run(peerID, addrList, probeBytes, timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(peerID string, addrs []string, probeBytes int64, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	target, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("parse peer ID: %w", err)
	}
	infos := make([]peer.AddrInfo, 0, len(addrs))
	for _, raw := range addrs {
		ma, err := multiaddr.NewMultiaddr(raw)
		if err != nil {
			return fmt.Errorf("parse addr %s: %w", raw, err)
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return fmt.Errorf("parse peer addr %s: %w", raw, err)
		}
		infos = append(infos, *info)
	}

	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		return fmt.Errorf("create libp2p host: %w", err)
	}
	defer h.Close()

	for _, info := range infos {
		if info.ID == "" {
			info.ID = target
		}
		h.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.TempAddrTTL)
		if err := h.Connect(ctx, info); err == nil {
			target = info.ID
			break
		}
	}

	stream, err := h.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return fmt.Errorf("open FlatSQL sync stream: %w", err)
	}
	defer stream.Close()

	if err := writeJSONFrame(stream, map[string]any{
		"op":          "wire_speed_probe",
		"probe_bytes": probeBytes,
	}); err != nil {
		return fmt.Errorf("write probe request: %w", err)
	}

	var header struct {
		Op           string `json:"op"`
		Status       string `json:"status"`
		SyncProtocol string `json:"sync_protocol"`
		ProbeBytes   int64  `json:"probe_bytes"`
		PayloadBytes int64  `json:"payload_bytes"`
		Error        struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := readJSONFrame(stream, &header); err != nil {
		return fmt.Errorf("read probe header: %w", err)
	}
	if header.Status == "error" {
		return fmt.Errorf("remote probe error: %s", header.Error.Message)
	}
	if header.SyncProtocol != "" && header.SyncProtocol != protocol.FlatSQLSyncProtocolID {
		return fmt.Errorf("unexpected sync protocol %s", header.SyncProtocol)
	}
	payloadBytes := header.PayloadBytes
	if payloadBytes <= 0 {
		payloadBytes = header.ProbeBytes
	}

	started := time.Now()
	written, err := io.CopyN(io.Discard, stream, payloadBytes)
	elapsed := time.Since(started)
	if err != nil {
		return fmt.Errorf("read probe payload: %w", err)
	}
	bytesPerSecond := float64(written) / elapsed.Seconds()
	result := map[string]any{
		"peer":               target.String(),
		"probeBytes":         header.ProbeBytes,
		"payloadBytes":       written,
		"elapsedMs":          float64(elapsed.Microseconds()) / 1000,
		"bytesPerSecond":     int64(bytesPerSecond),
		"mebibytesPerSec":    bytesPerSecond / 1024 / 1024,
		"syncProtocol":       protocol.FlatSQLSyncProtocolID,
		"client":             "go-libp2p",
		"remoteHttpFallback": false,
		"sshFallback":        false,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func writeJSONFrame(writer io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func readJSONFrame(reader io.Reader, target any) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[:])
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

type repeatedFlag []string

func (flag *repeatedFlag) String() string {
	return fmt.Sprint([]string(*flag))
}

func (flag *repeatedFlag) Set(value string) error {
	*flag = append(*flag, value)
	return nil
}
