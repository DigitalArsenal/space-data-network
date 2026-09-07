package peers

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

type blockedRegistryPersistence struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockedRegistryPersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	return map[peer.ID]*TrustedPeer{}, map[string]*PeerGroup{}, nil
}
func (p *blockedRegistryPersistence) Save(map[peer.ID]*TrustedPeer, map[string]*PeerGroup) error {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return errors.New("storage unavailable")
}

// Reproduces the live CelesTrak lock chain: AddPeer -> Save -> stalled store,
// followed by InterceptUpgraded -> RecordConnection -> registry mutex.
func TestPeerStreamsSurviveBlockedRegistryPersistence(t *testing.T) {
	persistence := &blockedRegistryPersistence{entered: make(chan struct{}), release: make(chan struct{})}
	registry := NewRegistry(false, persistence)
	defer registry.StopStatsWriter()
	server, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.ConnectionGater(NewTrustedConnectionGater(registry)))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Unblock the save before host cleanup, including on test failure.
	var released sync.Once
	release := func() { released.Do(func() { close(persistence.release) }) }
	defer release()
	saved := make(chan error, 1)
	go func() { saved <- registry.AddPeer(&TrustedPeer{ID: client.ID(), TrustLevel: Trusted}) }()
	select {
	case <-persistence.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("save did not start")
	}
	const wire = "/sdn/test-registry-persistence/1.0.0"
	server.SetStreamHandler(wire, func(s network.Stream) { defer s.Close(); _, _ = s.Write([]byte("ok")) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.NewStream(ctx, server.ID(), wire)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Second))
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "ok" {
		t.Fatalf("stream result %q: %v", data, err)
	}
	select {
	case err := <-saved:
		t.Fatalf("save returned before storage completed: %v", err)
	default:
	}
	if registry.GetTrustLevel(client.ID()) != Trusted {
		t.Fatal("live trust was lost")
	}
	release()
	if err := <-saved; !errors.Is(err, ErrNotPersisted) {
		t.Fatalf("lost persistence failure: %v", err)
	}
}
