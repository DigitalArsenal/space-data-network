package node

import (
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// net01MalformedPNM is the re-audit's NET-01 announcement: a size-prefixed
// $PNM whose root header passes the envelope check and whose CID uoffset
// points 2 GiB past the end of its 32 bytes.
func net01MalformedPNM() []byte {
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
}

// TestNET01UntrustedMalformedPNMDoesNotCrashTheNode is the security
// re-audit's NET-01 repro (zz_secaudit_pnm_panic_test.go) as a regression:
// an untrusted peer, directly connected, publishes malformed announcements
// on an open schema topic that the victim's real subscription loop handles.
// On origin/main the first one panics the process (slice bounds out of range
// in PNM.CID, node.go materializeDatasetPublicationPNMLane); now each is
// refused and the loop keeps running.
func TestNET01UntrustedMalformedPNMDoesNotCrashTheNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	victimHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer victimHost.Close()
	attacker, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	victimPS, err := newGossipSub(ctx, victimHost)
	if err != nil {
		t.Fatal(err)
	}
	attackerPS, err := pubsub.NewGossipSub(ctx, attacker)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n := &Node{host: victimHost, store: store, peerRegistry: peers.NewRegistry(true, nil),
		config: &config.Config{}, ctx: ctx, protocol: protocol.NewSDSExchangeHandler(store, validator)}
	n.protocol.SetPushAuthorizer(n.mayAcceptPush)
	n.protocol.SetPubSubPNMHandler(n.handleDatasetPublicationPNM)

	topics := []string{"OMM.fbs", "PNM.fbs"}
	attackerTopics := map[string]*pubsub.Topic{}
	for _, schema := range topics {
		name := "/spacedatanetwork/sds/" + schema
		vt, err := victimPS.Join(name)
		if err != nil {
			t.Fatal(err)
		}
		vsub, err := vt.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		n.wg.Add(1)
		go n.handleSubscription(vsub, schema) // exactly as subscribeToSchemas does
		at, err := attackerPS.Join(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := at.Subscribe(); err != nil {
			t.Fatal(err)
		}
		attackerTopics[schema] = at
	}
	if err := attacker.Connect(ctx, peer.AddrInfo{ID: victimHost.ID(), Addrs: victimHost.Addrs()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, schema := range topics {
		// The publisher must know the victim subscribes, or the message goes
		// nowhere and the test proves nothing.
		for len(victimPS.ListPeers("/spacedatanetwork/sds/"+schema)) == 0 || len(attackerPS.ListPeers("/spacedatanetwork/sds/"+schema)) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("attacker and victim never saw each other on %s", schema)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// Let the first heartbeat graft the mesh (the re-audit's repro waits
	// the same way); a publish before it can be dropped on the floor.
	time.Sleep(1500 * time.Millisecond)
	malformed := [][]byte{
		net01MalformedPNM(),
		// A well-formed BARE $PNM: the announcement lane reads every PNM
		// size-prefixed, so this one is malformed there too.
		sds.NewPNMBuilder().Build()[4:],
	}
	for _, schema := range topics {
		for _, msg := range malformed {
			if err := attackerTopics[schema].Publish(ctx, msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Give the victim's loops time to handle all four. A panic in any of
	// them aborts this test binary.
	time.Sleep(3 * time.Second)
	if got := n.protocol.DroppedNoStore(); got != 0 {
		t.Fatalf("store unexpectedly absent (%d drops)", got)
	}
	// Every malformed message must actually have reached the handler, or
	// surviving them proves nothing.
	seen := map[string]int{}
	for _, obs := range protocol.DefaultTopicActivity.Snapshot(time.Now()) {
		seen[obs.Topic] = len(obs.MessageTimestamps)
	}
	for _, schema := range topics {
		if got := seen["/spacedatanetwork/sds/"+schema]; got < len(malformed) {
			t.Fatalf("victim handled %d of %d messages on %s", got, len(malformed), schema)
		}
	}
	rows, err := store.QueryAll("PNM.fbs", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("an untrusted malformed PNM was stored (%d rows)", len(rows))
	}
}

// net01bMalformedEPM is a size-prefixed $EPM whose every root slot holds one
// uoffset 2 GiB past its end: it passes EPMBufferHasIdentifier and the
// envelope check, and any string, vector or table accessor on it panics.
func net01bMalformedEPM() []byte {
	const slots = 22
	vtableSize := 4 + 2*slots
	vtable := 8
	table := vtable + vtableSize
	table += (4 - table%4) % 4
	inner := make([]byte, table+12)
	binary.LittleEndian.PutUint32(inner[0:], uint32(table))
	copy(inner[4:8], "$EPM")
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

// TestNET01bMalformedEPMOnConnectDoesNotCrashTheNode is NET-01b on the
// connect-time lane: the node asks every bootstrap, mDNS, DHT and trusted
// peer for its EPM on a bare goroutine (requestConnectedPeerEPM). A peer
// that answers with a malformed EPM panicked VerifyEPMSignature there on
// origin/main, which ends the process; now the EPM is refused and nothing
// of it is indexed.
func TestNET01bMalformedEPMOnConnectDoesNotCrashTheNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	victimHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer victimHost.Close()
	attacker, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	evil := net01bMalformedEPM()
	served := make(chan struct{}, 1)
	attacker.SetStreamHandler(epm.EPMExchangeProtocolID, func(s network.Stream) {
		defer s.Close()
		var req [4]byte
		_, _ = io.ReadFull(s, req[:])
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(evil)))
		_, _ = s.Write(hdr[:])
		_, _ = s.Write(evil)
		served <- struct{}{}
	})
	if err := victimHost.Connect(ctx, peer.AddrInfo{ID: attacker.ID(), Addrs: attacker.Addrs()}); err != nil {
		t.Fatal(err)
	}
	registry := peers.NewRegistry(false, nil)
	n := &Node{host: victimHost, ctx: ctx, peerRegistry: registry, config: &config.Config{}}
	n.requestConnectedPeerEPM(attacker.ID(), "bootstrap")
	select {
	case <-served:
	case <-time.After(15 * time.Second):
		t.Fatal("the node never asked the attacker for its EPM; the test proves nothing")
	}
	// The victim's goroutine reads and indexes the answer after it is
	// served. A panic there aborts this test binary.
	time.Sleep(2 * time.Second)
	if tp, err := registry.GetPeer(attacker.ID()); err == nil && len(tp.EPMData) != 0 {
		t.Fatal("the malformed EPM was indexed")
	}
}
