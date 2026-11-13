package epm

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// One E2E: real identify observations, public projection, snapshot pagination,
// private-only exclusion and flood refusal over authenticated libp2p streams.
func TestPublicPeerDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	newHost := func(public string) host.Host {
		opts := []libp2p.Option{libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.UserAgent("spacedatanetwork/directory-e2e"), libp2p.DisableRelay()}
		if public != "" {
			opts = append(opts, libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
				return append(addrs, ma.StringCast(public), ma.StringCast("/ip4/10.1.2.3/tcp/443/tls/ws"), ma.StringCast("/dns4/secret.local/tcp/443/tls/ws"))
			}))
		}
		h, err := libp2p.New(opts...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		h.SetStreamHandler(EPMExchangeProtocolID, func(s network.Stream) { _ = s.Close() })
		return h
	}
	a, b, c, private, reader := newHost(""), newHost("/ip4/8.8.8.8/tcp/443/tls/ws"), newHost("/ip4/1.1.1.1/tcp/443/tls/ws"), newHost(""), newHost("")
	registry := peers.NewRegistry(false, nil)
	RegisterPeerDirectory(a, registry)
	for _, other := range []host.Host{b, c, private, reader} {
		// Only loopback is dialed; the public addresses are real identify claims.
		if err := a.Connect(ctx, peer.AddrInfo{ID: other.ID(), Addrs: other.Network().ListenAddresses()}); err != nil {
			t.Fatal(err)
		}
		conn := a.Network().ConnsToPeer(other.ID())[0]
		select {
		case <-a.(interface{ IDService() identify.IDService }).IDService().IdentifyWait(conn):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	request := func(cursor string) ([]byte, error) {
		s, err := reader.NewStream(ctx, a.ID(), PeerDirectoryProtocolID)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		if err = syncwire.Request(s, syncwire.QRPFields{Limit: 1, Cursor: cursor}); err != nil {
			return nil, err
		}
		return syncwire.ReadFrame(s, directoryPageBytes-4)
	}
	first, err := request("")
	if err != nil {
		t.Fatal(err)
	}
	q, err := syncwire.ParseQRP(first)
	if err != nil {
		t.Fatal(err)
	}
	if int8(q.KIND()) != syncwire.QRPKindPage || q.RECORD_COUNT() != 1 || q.TOTAL_COUNT() != 2 || string(q.PRODUCER_PEER_ID()) != a.ID().String() || q.AS_OF_MS() <= 0 {
		t.Fatalf("wrong directory page: kind=%d rows=%d total=%d", q.KIND(), q.RECORD_COUNT(), q.TOTAL_COUNT())
	}
	cursor := string(q.NEXT_CURSOR())
	stamp := q.AS_OF_MS()
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	if path := os.Getenv("SDN_DIRECTORY_GOLDEN"); path != "" {
		if err := os.WriteFile(path, first, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A later connection change cannot alter the snapshot already being paged.
	if err := a.Network().ClosePeer(b.ID()); err != nil {
		t.Fatal(err)
	}
	if err := a.Network().ClosePeer(c.ID()); err != nil {
		t.Fatal(err)
	}
	second, err := request(cursor)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, frame := range [][]byte{first, second} {
		page, err := syncwire.ParseQRP(frame)
		if err != nil {
			t.Fatal(err)
		}
		if page.AS_OF_MS() != stamp {
			t.Fatal("snapshot changed")
		}
		cols, rows := syncwire.Rows(page)
		for _, row := range rows {
			id := row[cols["PEER_ID"]].Text
			if id != b.ID().String() && id != c.ID().String() {
				t.Fatalf("unexpected peer %s", id)
			}
			if seen[id] {
				t.Fatal("duplicate peer")
			}
			seen[id] = true
			if row[cols["CONNECTED"]].Int != 1 || row[cols["EVIDENCE"]].Text != "connected" {
				t.Fatal("snapshot lost connected evidence")
			}
			if row[cols["SDN_PROTOCOL_0"]].Text != string(EPMExchangeProtocolID) {
				t.Fatal("missing identify protocol")
			}
		}
		for _, privateValue := range []string{"127.0.0.1", "10.1.2.3", "secret.local", "TRUST_LEVEL", "PIN_NOTE"} {
			if bytes.Contains(frame, []byte(privateValue)) {
				t.Fatalf("private value exposed: %s", privateValue)
			}
		}
	}
	last, _ := syncwire.ParseQRP(second)
	if string(last.NEXT_CURSOR()) != "" || len(seen) != 2 {
		t.Fatal("pagination did not finish")
	}
	for i := 2; i < directoryReadsPerMinute; i++ {
		frame, err := request(cursor)
		if err != nil {
			t.Fatal(err)
		}
		q, _ := syncwire.ParseQRP(frame)
		if int8(q.KIND()) != syncwire.QRPKindPage {
			t.Fatalf("early refusal at read %d", i+1)
		}
	}
	flood, err := request(cursor)
	if err != nil {
		t.Fatal(err)
	}
	refusal, err := syncwire.ParseQRP(flood)
	if err != nil {
		t.Fatal(err)
	}
	if int8(refusal.KIND()) != syncwire.QRPKindError || string(refusal.ERROR_CODE()) != "rate_limited" {
		t.Fatal("flood was not refused")
	}
}
