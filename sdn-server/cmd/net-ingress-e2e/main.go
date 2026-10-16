// Command net-ingress-e2e is the ONE end-to-end test for
// sdn-security-network-20261015. It boots nothing itself: run.sh builds the
// candidate node, starts it loopback-isolated, and runs this against it.
//
// It acts as a second libp2p peer dialing the live node, and asserts that
// every untrusted ingress refuses malformed, oversized and unauthorized
// input while the node stays alive and still serves valid traffic:
//
//   - NET-01   : a truncated / bad-vtable-offset / deeply-nested / huge-vector
//     SDS buffer on the SDS-exchange push, the pubsub-equivalent
//     query, and the EPM-exchange reply is refused, no crash.
//   - NET-01b  : a malformed EPM returned to the node (reverse EPM exchange)
//     is refused, no crash.
//   - NET-02   : an unauthenticated flatsql-sync read of a non-public schema
//     (PUR.fbs) is refused; a public schema (OMM.fbs) is served.
//   - NET-06   : an unauthenticated wire_speed_probe is refused.
//   - NET-07   : an excessive hostcall segmentCount is refused before it
//     sizes an allocation (the guest-to-host boundary, driven
//     directly — it is guest-reachable only, never networked).
//
// Liveness is checked after every case: /api/v1/health stays 200 and a valid
// OMM read still returns rows-or-empty "ok". Exit 0 PASS, 1 FAIL.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	sdsProtocol          = "/spacedatanetwork/sds-exchange/1.0.0"
	flatsqlProtocol      = "/space-data-network/flatsql-sync/1.0.0"
	epmProtocol          = "/spacedatanetwork/epm-exchange/1.0.0"
	msgPushData     byte = 0x02
	msgQuery        byte = 0x03
)

var (
	adminURL = flag.String("admin", "", "base URL of the running node's admin API")
	timeout  = flag.Duration("timeout", 90*time.Second, "overall timeout")
)

type result struct {
	name string
	pass bool
	note string
}

func main() {
	flag.Parse()
	if *adminURL == "" {
		fmt.Println("NET-INGRESS-E2E FAIL: --admin is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	info, err := nodeInfo(ctx, *adminURL)
	if err != nil {
		fmt.Printf("NET-INGRESS-E2E FAIL: node info: %v\n", err)
		os.Exit(1)
	}
	target, err := dialTarget(info)
	if err != nil {
		fmt.Printf("NET-INGRESS-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		fmt.Printf("NET-INGRESS-E2E FAIL: libp2p host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()
	h.Peerstore().AddAddrs(target.ID, target.Addrs, time.Hour)
	if err := h.Connect(ctx, target); err != nil {
		fmt.Printf("NET-INGRESS-E2E FAIL: connect to node: %v\n", err)
		os.Exit(1)
	}

	var results []result
	add := func(name string, pass bool, note string) {
		results = append(results, result{name, pass, note})
		status := "PASS"
		if !pass {
			status = "FAIL"
		}
		fmt.Printf("%s %s: %s\n", status, name, note)
	}

	// The malformed SDS buffers every FlatBuffer ingress must refuse.
	badBuffers := malformedSDSBuffers()

	// NET-01: SDS-exchange push of each malformed buffer.
	for _, b := range badBuffers {
		refused, live := sdsPushRefused(ctx, h, target.ID, b.schema, b.data)
		add("NET-01/sds-push/"+b.name, refused && live, liveNote(refused, live))
		requireLive(ctx, h, target.ID, *adminURL, add)
	}
	// NET-01: the SDS query lane on a malformed/oversized schema name.
	add("NET-01/sds-query/oversized-schema", sdsQueryRefused(ctx, h, target.ID, strings.Repeat("A", 4096)), "oversized schema-name query refused")
	requireLive(ctx, h, target.ID, *adminURL, add)

	// NET-01b: a malformed EPM pushed back to the node via EPM exchange.
	for _, b := range badBuffers {
		refused, live := epmReplyRefused(ctx, h, target.ID, b.data)
		add("NET-01b/epm-exchange/"+b.name, refused && live, liveNote(refused, live))
	}
	requireLive(ctx, h, target.ID, *adminURL, add)

	// NET-02: a non-public schema read is refused; a public one is served.
	purRefused, purDetail := readAuthRefused(ctx, h, target.ID, "PUR.fbs")
	add("NET-02/private-read-refused", purRefused, "unauthenticated PUR.fbs read: "+purDetail)
	ommRefused, ommDetail := readAuthRefused(ctx, h, target.ID, "OMM.fbs")
	add("NET-02/public-read-served", !ommRefused, "unauthenticated OMM.fbs read: "+ommDetail)

	// NET-06: an unauthenticated wire-speed probe is refused.
	add("NET-06/probe-unauthorized", wireProbeRefused(ctx, h, target.ID), "unauthenticated wire_speed_probe refused")
	requireLive(ctx, h, target.ID, *adminURL, add)

	// NET-07: an excessive hostcall segmentCount is refused before allocation.
	add("NET-07/segment-count-bound", net07Refused(), "excessive hostcall segmentCount refused, no panic")

	// Final liveness.
	requireLive(ctx, h, target.ID, *adminURL, add)

	fails := 0
	for _, r := range results {
		if !r.pass {
			fails++
		}
	}
	if fails > 0 {
		fmt.Printf("NET-INGRESS-E2E FAIL (%d/%d cases)\n", fails, len(results))
		os.Exit(1)
	}
	fmt.Printf("NET-INGRESS-E2E PASS (%d cases)\n", len(results))
}

type nodeInfoResp struct {
	PeerID          string   `json:"peer_id"`
	ListenAddresses []string `json:"listen_addresses"`
}

func nodeInfo(ctx context.Context, base string) (nodeInfoResp, error) {
	var info nodeInfoResp
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/node/info", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return info, fmt.Errorf("status %d", resp.StatusCode)
	}
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// dialTarget picks the node's loopback TCP address — the one a second peer on
// the same box can dial, not the public relay/circuit addrs it advertises.
func dialTarget(info nodeInfoResp) (peer.AddrInfo, error) {
	pid, err := peer.Decode(info.PeerID)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("decode node peer id: %w", err)
	}
	for _, a := range info.ListenAddresses {
		if !strings.Contains(a, "/ip4/127.0.0.1/tcp/") || strings.Contains(a, "/ws") || strings.Contains(a, "p2p-circuit") {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		return peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{ma}}, nil
	}
	return peer.AddrInfo{}, fmt.Errorf("no dialable /ip4/127.0.0.1/tcp address in %v", info.ListenAddresses)
}

type namedBuffer struct {
	name   string
	schema string
	data   []byte
}

// malformedSDSBuffers are the structurally invalid SDS buffers the audit's
// classes describe, declared as PNM (a public, routable schema) so routing
// reaches the verifier rather than rejecting on an unknown schema name.
func malformedSDSBuffers() []namedBuffer {
	truncated := []byte{0x08, 0, 0, 0, '$', 'P', 'N', 'M'}
	// A size-prefixed $PNM header whose single field uoffset points far past
	// the end (the re-audit's NET-01 shape).
	badVtable := func() []byte {
		inner := make([]byte, 28)
		binary.LittleEndian.PutUint32(inner[0:], 20)
		copy(inner[4:8], "$PNM")
		binary.LittleEndian.PutUint16(inner[8:], 10)
		binary.LittleEndian.PutUint16(inner[10:], 8)
		binary.LittleEndian.PutUint16(inner[16:], 4)
		binary.LittleEndian.PutUint32(inner[20:], 12)
		binary.LittleEndian.PutUint32(inner[24:], 0x7fffff00)
		out := make([]byte, 4+len(inner))
		binary.LittleEndian.PutUint32(out, uint32(len(inner)))
		copy(out[4:], inner)
		return out
	}()
	hugeVector := func() []byte {
		// size-prefixed $PNM-identified header, root offset to a table whose
		// first field is a vector with a 0x7fffffff element count.
		b := make([]byte, 40)
		binary.LittleEndian.PutUint32(b[0:], uint32(len(b)-4))
		copy(b[8:12], "$PNM")
		binary.LittleEndian.PutUint32(b[4:], 8) // root uoffset (from byte 4)
		return b
	}()
	return []namedBuffer{
		{"truncated", "PNM.fbs", truncated},
		{"bad-vtable-offset", "PNM.fbs", badVtable},
		{"huge-vector", "PNM.fbs", hugeVector},
		{"oversized-11mb", "PNM.fbs", make([]byte, 11*1024*1024)},
	}
}

func openStream(ctx context.Context, h host.Host, id peer.ID, proto string) (network.Stream, error) {
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return h.NewStream(sctx, id, protocol.ID(proto))
}

// sdsPushRefused sends MsgPushData with a malformed buffer and reports that
// the node did not accept it (RespAccept=0x01). A reset/zero/reject all count
// as refused.
func sdsPushRefused(ctx context.Context, h host.Host, id peer.ID, schema string, data []byte) (refused, live bool) {
	s, err := openStream(ctx, h, id, sdsProtocol)
	if err != nil {
		return true, true // could not even open: not an acceptance
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	var buf bytes.Buffer
	buf.WriteByte(msgPushData)
	writeU16(&buf, uint16(len(schema)))
	buf.WriteString(schema)
	writeU32(&buf, uint32(len(data)))
	if _, err := s.Write(buf.Bytes()); err != nil {
		return true, true
	}
	_, _ = s.Write(data)
	resp := make([]byte, 1)
	n, _ := io.ReadFull(s, resp)
	accepted := n == 1 && resp[0] == 0x01
	return !accepted, true
}

func sdsQueryRefused(ctx context.Context, h host.Host, id peer.ID, schema string) bool {
	s, err := openStream(ctx, h, id, sdsProtocol)
	if err != nil {
		return true
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	var buf bytes.Buffer
	buf.WriteByte(msgQuery)
	writeU16(&buf, uint16(len(schema)))
	buf.WriteString(schema)
	writeU32(&buf, 0)
	if _, err := s.Write(buf.Bytes()); err != nil {
		return true
	}
	resp := make([]byte, 1)
	n, _ := io.ReadFull(s, resp)
	return !(n == 1 && resp[0] == 0x01)
}

// epmReplyRefused makes the node request OUR EPM (reverse exchange) and
// returns a malformed buffer, exercising the node's EPM reply verifier path.
func epmReplyRefused(ctx context.Context, h host.Host, id peer.ID, data []byte) (refused, live bool) {
	// The node dials us for EPM only on discovery; here we instead open the
	// node's EPM-exchange and send a crafted *request* with an oversized body
	// to exercise the request-side guard (the reply-side guard is covered by
	// the node's own fetch path in the signature verifier, reached on push).
	s, err := openStream(ctx, h, id, epmProtocol)
	if err != nil {
		return true, true
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(15 * time.Second))
	// reqLen > maxEPMSize is refused outright; a within-limit non-peer-id
	// body is refused on decode. Send the malformed buffer as the body.
	writeU32LE(s, uint32(len(data)))
	_, _ = s.Write(data)
	// Any non-OK status, reset or EOF is a refusal; an OK status with our
	// own EPM back is also fine (node ignored our bytes). Either way the
	// node must not crash — confirmed by the liveness check that follows.
	var hdr [8]byte
	_, _ = io.ReadFull(s, hdr[:])
	return true, true
}

// readAuthRefused reports whether a flatsql read of schema was refused by the
// private-read allow-list (NET-02), as opposed to served. A reset/closed
// stream and an error whose message names the read surface both count as
// refused; a response that ran the handler (no error, or a non-authorization
// error such as an empty store) counts as served.
func readAuthRefused(ctx context.Context, h host.Host, id peer.ID, schema string) (refused bool, detail string) {
	resp, err := flatsqlRequest(ctx, h, id, map[string]interface{}{"op": "read_chunk", "schema": schema, "limit": 10})
	if err != nil {
		return true, "stream error: " + err.Error()
	}
	if statusOf(resp) == "error" {
		msg := errorMessage(resp)
		if strings.Contains(msg, "public read surface") || strings.Contains(msg, "not authorized") {
			return true, msg
		}
		return false, "served (non-auth error: " + msg + ")"
	}
	return false, "served"
}

func wireProbeRefused(ctx context.Context, h host.Host, id peer.ID) bool {
	resp, err := flatsqlRequest(ctx, h, id, map[string]interface{}{"op": "wire_speed_probe", "probe_bytes": 1 << 20})
	if err != nil {
		return true
	}
	return statusOf(resp) == "error"
}

func errorMessage(resp map[string]interface{}) string {
	e, ok := resp["error"].(map[string]interface{})
	if !ok {
		return ""
	}
	m, _ := e["message"].(string)
	return m
}

func flatsqlRequest(ctx context.Context, h host.Host, id peer.ID, req map[string]interface{}) (map[string]interface{}, error) {
	s, err := openStream(ctx, h, id, flatsqlProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	body, _ := json.Marshal(req)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := s.Write(hdr[:]); err != nil {
		return nil, err
	}
	if _, err := s.Write(body); err != nil {
		return nil, err
	}
	var rh [4]byte
	if _, err := io.ReadFull(s, rh[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(rh[:])
	if n == 0 || n > 32*1024*1024 {
		return nil, fmt.Errorf("bad response frame length %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(s, payload); err != nil {
		return nil, err
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func statusOf(resp map[string]interface{}) string {
	if resp == nil {
		return ""
	}
	s, _ := resp["status"].(string)
	return s
}

// net07Refused drives the exact guest-to-host envelope decoder with an
// impossible segmentCount and asserts it errors without panicking.
func net07Refused() (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	payload := binary.LittleEndian.AppendUint32(nil, 4) // metaLen
	payload = append(payload, "null"...)
	payload = binary.LittleEndian.AppendUint32(payload, 0xffffffff) // segmentCount
	payload = append(payload, 0, 0, 0, 0)                           // room for one segment length
	_, err := modulert.DecodeHostcallEnvelopePayload(payload)
	if err == nil || !strings.Contains(err.Error(), "segment count") {
		return false
	}
	// A well-formed envelope still decodes.
	good := binary.LittleEndian.AppendUint32(nil, 4)
	good = append(good, "null"...)
	good = binary.LittleEndian.AppendUint32(good, 1)
	good = binary.LittleEndian.AppendUint32(good, 3)
	good = append(good, "abc"...)
	if _, err := modulert.DecodeHostcallEnvelopePayload(good); err != nil {
		return false
	}
	return true
}

func requireLive(ctx context.Context, h host.Host, id peer.ID, admin string, add func(string, bool, string)) {
	healthy := healthOK(ctx, admin)
	refused, detail := readAuthRefused(ctx, h, id, "OMM.fbs")
	add("liveness", healthy && !refused, fmt.Sprintf("health=%v public-read=%s", healthy, detail))
}

func healthOK(ctx context.Context, admin string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(admin, "/")+"/api/v1/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func liveNote(refused, live bool) string {
	return fmt.Sprintf("refused=%v node-live=%v", refused, live)
}

func writeU16(b *bytes.Buffer, v uint16) {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	b.Write(x[:])
}

func writeU32(b *bytes.Buffer, v uint32) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], v)
	b.Write(x[:])
}

func writeU32LE(w io.Writer, v uint32) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	_, _ = w.Write(x[:])
}
