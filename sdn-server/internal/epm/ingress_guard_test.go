package epm

import (
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// craftOOBBuffer returns a size-prefixed FlatBuffer carrying ident whose root
// table's every vtable slot points at one uoffset 2 GiB past the end. It
// passes <T>BufferHasIdentifier and the node's envelope check; any string,
// vector or table accessor on it panics.
func craftOOBBuffer(ident string, slots int) []byte {
	vtableSize := 4 + 2*slots
	vtable := 8
	table := vtable + vtableSize
	if table%4 != 0 {
		table += 4 - table%4
	}
	inner := make([]byte, table+12)
	binary.LittleEndian.PutUint32(inner[0:], uint32(table))
	copy(inner[4:8], ident)
	binary.LittleEndian.PutUint16(inner[vtable:], uint16(vtableSize))
	binary.LittleEndian.PutUint16(inner[vtable+2:], 12)
	for i := 0; i < slots; i++ {
		binary.LittleEndian.PutUint16(inner[vtable+4+2*i:], 4)
	}
	binary.LittleEndian.PutUint32(inner[table:], uint32(table-vtable))
	binary.LittleEndian.PutUint32(inner[table+4:], 0x7fffff00)
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

// NET-01b (security re-audit, 2026-10-15): a peer that advertises the
// epm-exchange protocol answers the node's EPM request with a malformed EPM.
// On origin/main VerifyEPMSignature's accessors panic on it and the process
// dies; now the EPM is refused and nothing is stored.
func TestNET01bMalformedEPMFromExchangePeerIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	victim, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	attacker, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	evil := craftOOBBuffer("$EPM", 22)
	attacker.SetStreamHandler(EPMExchangeProtocolID, func(s network.Stream) {
		defer s.Close()
		var req [4]byte
		_, _ = io.ReadFull(s, req[:])
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(evil)))
		_, _ = s.Write(hdr[:])
		_, _ = s.Write(evil)
	})
	if err := victim.Connect(ctx, peer.AddrInfo{ID: attacker.ID(), Addrs: attacker.Addrs()}); err != nil {
		t.Fatal(err)
	}
	registry := peers.NewRegistry(false, nil)
	svc := NewService(nil, registry, victim.ID(), "", t.TempDir())
	if err := svc.RequestPeerEPM(ctx, victim, attacker.ID()); err != nil {
		t.Logf("RequestPeerEPM: %v", err)
	}
	if tp, err := registry.GetPeer(attacker.ID()); err == nil && len(tp.EPMData) != 0 {
		t.Fatal("the malformed EPM was stored")
	}
}

// The same shape on the EPM announcement lane.
func TestNET01bMalformedEPMAnnouncementIsDropped(t *testing.T) {
	svc := NewService(nil, peers.NewRegistry(false, nil), "", "", t.TempDir())
	svc.HandlePNMEPM(craftOOBBuffer("$PNM", 9), "attacker")
}

func TestVerifyEPMBuffer(t *testing.T) {
	good := sds.NewEPMBuilder().WithLegalName("Org").WithEmail("a@b").
		WithAddress("1 Main", "Denver", "CO", "80202", "US").WithKeys("02ab", "03cd").
		WithMultiAddrs([]string{"/ip4/1.2.3.4/tcp/1"}).Build()
	if err := VerifyEPMBuffer(good); err != nil {
		t.Fatalf("well-formed EPM: %v", err)
	}
	if err := VerifyEPMBuffer(craftOOBBuffer("$EPM", 22)); err == nil {
		t.Fatal("malformed EPM verified")
	}
	if err := VerifyEPMBuffer(good[4:]); err == nil {
		t.Fatal("bare EPM verified as size-prefixed")
	}
	if err := VerifyEPMBuffer(nil); err == nil {
		t.Fatal("empty EPM verified")
	}
}
