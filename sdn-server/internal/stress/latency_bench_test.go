package stress

// End-to-end GossipSub publish->receive latency benchmark. Unlike the
// tagged stress suite this runs in the normal test build (skipped with
// -short) so latency evidence stays fresh in CI. Run directly with:
//
//	go test ./internal/stress -run TestPubsubLatencyBench -v
//
// Results are summarized as a markdown block; see docs/benchmarks-latency.md.

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	latencyBenchTopic    = "/spacedatanetwork/sds/OMM.fbs"
	latencyBenchMessages = 1000
	// Typical serialized OMM FlatBuffer is a few hundred bytes.
	latencyBenchPayloadSize = 512
	latencyBenchSendGap     = 2 * time.Millisecond
)

func newLatencyBenchHost(t *testing.T) (host.Host, *pubsub.PubSub) {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	ps, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		t.Fatalf("gossipsub: %v", err)
	}
	return h, ps
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(p * float64(len(sorted)-1))
	return sorted[index]
}

func TestPubsubLatencyBench(t *testing.T) {
	if testing.Short() {
		t.Skip("latency benchmark skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	publisherHost, publisherPS := newLatencyBenchHost(t)
	subscriberHost, subscriberPS := newLatencyBenchHost(t)

	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: publisherHost.ID(), Addrs: publisherHost.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	pubTopic, err := publisherPS.Join(latencyBenchTopic)
	if err != nil {
		t.Fatalf("publisher join: %v", err)
	}
	subTopic, err := subscriberPS.Join(latencyBenchTopic)
	if err != nil {
		t.Fatalf("subscriber join: %v", err)
	}
	subscription, err := subTopic.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Let the gossipsub meshes connect before measuring.
	deadline := time.Now().Add(10 * time.Second)
	for len(pubTopic.ListPeers()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("gossipsub mesh did not form")
		}
		time.Sleep(20 * time.Millisecond)
	}

	received := make(chan time.Duration, latencyBenchMessages)
	warmupReceived := make(chan struct{}, 8)
	go func() {
		for {
			msg, err := subscription.Next(ctx)
			if err != nil {
				return
			}
			now := time.Now()
			if len(msg.Data) < 9 {
				continue
			}
			if msg.Data[8] == 1 { // warm-up marker
				select {
				case warmupReceived <- struct{}{}:
				default:
				}
				continue
			}
			sentNanos := int64(binary.BigEndian.Uint64(msg.Data[:8]))
			received <- now.Sub(time.Unix(0, sentNanos))
		}
	}()

	// Warm-up: the first publishes after mesh formation can be dropped while
	// gossipsub heartbeats settle; they are marked and excluded.
	payload := make([]byte, latencyBenchPayloadSize)
	payload[8] = 1
	warmupDeadline := time.After(10 * time.Second)
	for delivered := false; !delivered; {
		binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().UnixNano()))
		if err := pubTopic.Publish(ctx, payload); err != nil {
			t.Fatalf("warm-up publish: %v", err)
		}
		select {
		case <-warmupReceived:
			delivered = true
		case <-time.After(200 * time.Millisecond):
		case <-warmupDeadline:
			t.Fatal("warm-up message never delivered")
		}
	}

	payload[8] = 0
	for i := 0; i < latencyBenchMessages; i++ {
		binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().UnixNano()))
		if err := pubTopic.Publish(ctx, payload); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		time.Sleep(latencyBenchSendGap)
	}

	latencies := make([]time.Duration, 0, latencyBenchMessages)
	collectDeadline := time.After(15 * time.Second)
collect:
	for len(latencies) < latencyBenchMessages {
		select {
		case d := <-received:
			latencies = append(latencies, d)
		case <-collectDeadline:
			break collect
		}
	}
	deliveryRate := float64(len(latencies)) / float64(latencyBenchMessages)
	if deliveryRate < 0.99 {
		t.Fatalf("delivery rate %.2f%% below 99%% (%d/%d)", deliveryRate*100, len(latencies), latencyBenchMessages)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 0.50)
	p95 := percentile(latencies, 0.95)
	p99 := percentile(latencies, 0.99)
	max := latencies[len(latencies)-1]

	fmt.Printf("\n| Scenario | Delivered | Payload | p50 | p95 | p99 | max |\n")
	fmt.Printf("|---|---|---|---|---|---|---|\n")
	fmt.Printf("| direct 2-node GossipSub (localhost TCP) | %d/%d (%.1f%%) | %dB | %s | %s | %s | %s |\n\n",
		len(latencies), latencyBenchMessages, deliveryRate*100, latencyBenchPayloadSize, p50, p95, p99, max)

	// Generous functional bound: the sub-second tactical-latency claim must
	// hold with two orders of magnitude to spare on loopback.
	if p99 > time.Second {
		t.Fatalf("p99 publish->receive latency %s exceeds 1s on loopback", p99)
	}
}
