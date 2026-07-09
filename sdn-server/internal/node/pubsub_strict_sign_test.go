package node

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

// TestNewGossipSubEnforcesStrictSign is a regression lock for the
// StrictSign message-signing invariant relied on across gossipsub loops
// (dataset publication announcements, discovery, PNM provenance, etc): a
// receiver built via newGossipSub — the exact call node.go uses to
// construct n.pubsub — must reject messages that were not signed by their
// publisher, and must accept properly signed ones.
//
// If a future change ever weakens newGossipSub (e.g. adding
// pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign) or
// WithNoSigning()), the "unsigned message rejected" half of this test
// starts failing because the receiver would begin accepting spoofable,
// unsigned gossip.
func TestNewGossipSubEnforcesStrictSign(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const topicName = "strict-sign-regression-test"

	receiverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("receiver host: %v", err)
	}
	defer receiverHost.Close()

	// Production code path: this is exactly what internal/node/node.go
	// calls to build n.pubsub.
	receiverPS, err := newGossipSub(ctx, receiverHost)
	if err != nil {
		t.Fatalf("newGossipSub: %v", err)
	}
	receiverTopic, err := receiverPS.Join(topicName)
	if err != nil {
		t.Fatalf("receiver join: %v", err)
	}
	sub, err := receiverTopic.Subscribe()
	if err != nil {
		t.Fatalf("receiver subscribe: %v", err)
	}

	// --- Unsigned publisher: simulates what happens if StrictSign were
	// ever dropped from a peer's outgoing message policy (or a hostile
	// peer that refuses to sign). The receiver above still runs with
	// production defaults and must drop this peer's messages.
	unsignedHost := connectPeer(t, ctx, receiverHost)
	defer unsignedHost.Close()
	unsignedPS, err := pubsub.NewGossipSub(ctx, unsignedHost, pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign))
	if err != nil {
		t.Fatalf("unsigned pubsub: %v", err)
	}
	unsignedTopic, err := unsignedPS.Join(topicName)
	if err != nil {
		t.Fatalf("unsigned join: %v", err)
	}
	waitForTopicPeer(t, unsignedTopic, receiverHost.ID())

	if err := unsignedTopic.Publish(ctx, []byte("unsigned-payload-must-be-dropped")); err != nil {
		t.Fatalf("unsigned publish: %v", err)
	}

	unsignedCtx, unsignedCancel := context.WithTimeout(ctx, 4*time.Second)
	defer unsignedCancel()
	if msg, err := sub.Next(unsignedCtx); err == nil {
		t.Fatalf("receiver accepted an unsigned message (StrictSign appears to be disabled): %q", string(msg.Data))
	}

	// --- Signed publisher (positive control, same production code path
	// as the receiver): proves the topology/mesh actually works and that
	// the unsigned message above was rejected because of the signing
	// policy, not because messages never reach the receiver at all.
	signedHost := connectPeer(t, ctx, receiverHost)
	defer signedHost.Close()
	signedPS, err := newGossipSub(ctx, signedHost)
	if err != nil {
		t.Fatalf("signed pubsub: %v", err)
	}
	signedTopic, err := signedPS.Join(topicName)
	if err != nil {
		t.Fatalf("signed join: %v", err)
	}
	waitForTopicPeer(t, signedTopic, receiverHost.ID())

	want := []byte("signed-payload-must-be-delivered")
	if err := signedTopic.Publish(ctx, want); err != nil {
		t.Fatalf("signed publish: %v", err)
	}

	signedCtx, signedCancel := context.WithTimeout(ctx, 10*time.Second)
	defer signedCancel()
	msg, err := sub.Next(signedCtx)
	if err != nil {
		t.Fatalf("receiver never got the signed control message: %v", err)
	}
	if !bytes.Equal(msg.Data, want) {
		t.Fatalf("signed message payload = %q, want %q", msg.Data, want)
	}
}

// connectPeer creates a new libp2p host and connects it to target.
func connectPeer(t *testing.T, ctx context.Context, target host.Host) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("peer host: %v", err)
	}
	h.Peerstore().AddAddrs(target.ID(), target.Addrs(), peerstore.PermanentAddrTTL)
	if err := h.Connect(ctx, peer.AddrInfo{ID: target.ID(), Addrs: target.Addrs()}); err != nil {
		t.Fatalf("connect to target: %v", err)
	}
	return h
}

// waitForTopicPeer polls until target shows up as a topic peer, i.e. the
// gossipsub mesh/fanout is ready to deliver a Publish from this topic to
// target. This avoids relying on a fixed sleep to outlast heartbeat timing.
func waitForTopicPeer(t *testing.T, topic *pubsub.Topic, target peer.ID) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range topic.ListPeers() {
			if p == target {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear as a topic peer", target)
}
