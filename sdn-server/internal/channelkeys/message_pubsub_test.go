package channelkeys

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestEncryptedChatOverGossipsub proves WS9.2 end-to-end in Go: two real
// libp2p hosts joined to the channel's gossipsub chat topic exchange an
// encrypted message — the member decrypts + verifies the sender; a non-member
// (no content key) sees only ciphertext.
func TestEncryptedChatOverGossipsub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newHost := func() (host.Host, *pubsub.PubSub) {
		h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		if err != nil {
			t.Fatalf("libp2p.New: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		ps, err := pubsub.NewGossipSub(ctx, h)
		if err != nil {
			t.Fatalf("NewGossipSub: %v", err)
		}
		return h, ps
	}

	senderHost, senderPS := newHost()
	memberHost, memberPS := newHost()
	outsiderHost, outsiderPS := newHost()

	// Fully connect the mesh.
	for _, h := range []host.Host{memberHost, outsiderHost} {
		if err := h.Connect(ctx, peer.AddrInfo{ID: senderHost.ID(), Addrs: senderHost.Addrs()}); err != nil {
			t.Fatalf("connect: %v", err)
		}
	}

	// Channel with alice (the receiving member); the sender knows the key too.
	ch, err := New("gossip-room")
	if err != nil {
		t.Fatal(err)
	}
	alice := x25519Party(t, "alice")
	_ = ch.AddMember(alice.member())
	envs, err := ch.WrapForMembers()
	if err != nil {
		t.Fatal(err)
	}
	// Alice recovers the channel key from her wrapped envelope (the 9.1 flow).
	aliceKey, err := UnwrapForMember(alice.priv, envs[0].ENC, envs[0].KMF, ch.Context())
	if err != nil {
		t.Fatal(err)
	}

	topicName := ChatTopic(ch.ID())
	senderTopic, err := senderPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	memberTopic, err := memberPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	outsiderTopic, err := outsiderPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	memberSub, err := memberTopic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	outsiderSub, err := outsiderTopic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}

	// Let the gossipsub mesh form.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(senderTopic.ListPeers()) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := len(senderTopic.ListPeers()); got < 2 {
		t.Fatalf("mesh did not form: sender sees %d peers", got)
	}

	// Sender encrypts under the channel key and publishes.
	_, senderSignPriv, _ := ed25519.GenerateKey(nil)
	plaintext := []byte("encrypted chat over real gossipsub")
	envelope, err := EncryptMessage(ch.ContentKey(), senderSignPriv, ch.Context(), ch.Epoch(), plaintext, EncryptOptions{TimestampMs: 42})
	if err != nil {
		t.Fatal(err)
	}
	if err := senderTopic.Publish(ctx, envelope); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Member receives + decrypts with the key recovered from her 9.1 envelope.
	memberMsg, err := memberSub.Next(ctx)
	if err != nil {
		t.Fatalf("member receive: %v", err)
	}
	got, err := DecryptMessage(aliceKey, memberMsg.Data, ch.Context())
	if err != nil {
		t.Fatalf("member decrypt: %v", err)
	}
	if !bytes.Equal(got.Plaintext, plaintext) {
		t.Fatal("member plaintext mismatch")
	}
	wantPub := senderSignPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(got.SenderPublicKey, wantPub) {
		t.Fatal("sender attribution mismatch")
	}

	// Outsider receives the SAME bytes but holds no channel key: the envelope
	// must not contain the plaintext, and decrypting with any other key fails.
	outMsg, err := outsiderSub.Next(ctx)
	if err != nil {
		t.Fatalf("outsider receive: %v", err)
	}
	if bytes.Contains(outMsg.Data, plaintext) {
		t.Fatal("wire bytes leak plaintext to non-members")
	}
	wrongKey := make([]byte, 32)
	if _, err := DecryptMessage(wrongKey, outMsg.Data, ch.Context()); err == nil {
		t.Fatal("non-member decrypted the message")
	}
}
