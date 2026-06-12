// Command wan-latency-bench measures publish->receive GossipSub latency
// between two hosts, mirroring the loopback methodology in
// docs/benchmarks-latency.md (internal/stress/latency_bench_test.go) over a
// real network path.
//
// Run the subscriber on one host, then the publisher on the other:
//
//	wan-latency-bench -mode sub -listen /ip4/0.0.0.0/tcp/49001
//	wan-latency-bench -mode pub -connect /ip4/<sub-ip>/tcp/49001/p2p/<sub-peer-id>
//
// The publisher embeds its send timestamp (unix nanos, big endian) in each
// payload; the subscriber computes latency against its own clock on
// delivery. Cross-host results therefore include any clock offset between
// the two hosts — run both directions and report NTP sync state alongside.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const warmupMarker = 1

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

func main() {
	mode := flag.String("mode", "", "pub or sub")
	listen := flag.String("listen", "/ip4/0.0.0.0/tcp/49001", "listen multiaddr (sub)")
	connect := flag.String("connect", "", "subscriber multiaddr incl /p2p/ (pub)")
	topic := flag.String("topic", "/spacedatanetwork/sds/BENCH.fbs", "gossipsub topic")
	count := flag.Int("count", 1000, "messages to publish/expect")
	size := flag.Int("size", 512, "payload size in bytes (>= 9)")
	gap := flag.Duration("gap", 2*time.Millisecond, "inter-publish gap")
	scenario := flag.String("scenario", "wan", "label for the results row")
	flag.Parse()

	if *size < 9 {
		fmt.Fprintln(os.Stderr, "payload size must be >= 9")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	h, err := libp2p.New(libp2p.ListenAddrStrings(*listen))
	if err != nil {
		fmt.Fprintf(os.Stderr, "libp2p host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gossipsub: %v\n", err)
		os.Exit(1)
	}
	tp, err := ps.Join(*topic)
	if err != nil {
		fmt.Fprintf(os.Stderr, "join: %v\n", err)
		os.Exit(1)
	}

	switch *mode {
	case "sub":
		runSubscriber(ctx, h, tp, *count, *scenario, *size)
	case "pub":
		runPublisher(ctx, h, tp, *connect, *count, *size, *gap)
	default:
		fmt.Fprintln(os.Stderr, "-mode must be pub or sub")
		os.Exit(1)
	}
}

func runSubscriber(ctx context.Context, h interface {
	ID() peer.ID
	Addrs() []multiaddr.Multiaddr
}, tp *pubsub.Topic, count int, scenario string, size int) {
	sub, err := tp.Subscribe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe: %v\n", err)
		os.Exit(1)
	}

	for _, a := range h.Addrs() {
		fmt.Printf("LISTEN %s/p2p/%s\n", a, h.ID())
	}
	fmt.Println("READY")

	latencies := make([]time.Duration, 0, count)
	var firstRecv, lastRecv time.Time
	idle := time.NewTimer(10 * time.Minute)
	msgs := make(chan *pubsub.Message, 256)
	go func() {
		for {
			m, err := sub.Next(ctx)
			if err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

collect:
	for len(latencies) < count {
		select {
		case m, ok := <-msgs:
			if !ok {
				break collect
			}
			now := time.Now()
			if len(m.Data) < 9 || m.Data[8] == warmupMarker {
				// Warm-up traffic keeps the idle timer alive but is excluded.
				idle.Reset(30 * time.Second)
				continue
			}
			sent := time.Unix(0, int64(binary.BigEndian.Uint64(m.Data[:8])))
			latencies = append(latencies, now.Sub(sent))
			if firstRecv.IsZero() {
				firstRecv = now
			}
			lastRecv = now
			idle.Reset(30 * time.Second)
		case <-idle.C:
			break collect
		case <-ctx.Done():
			break collect
		}
	}

	delivered := len(latencies)
	rate := float64(delivered) / float64(count)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	elapsed := lastRecv.Sub(firstRecv)
	fmt.Printf("\n| Scenario | Delivered | Payload | p50 | p95 | p99 | max |\n")
	fmt.Printf("|---|---|---|---|---|---|---|\n")
	fmt.Printf("| %s | %d/%d (%.1f%%) | %dB | %s | %s | %s | %s |\n",
		scenario, delivered, count, rate*100, size,
		percentile(latencies, 0.50), percentile(latencies, 0.95),
		percentile(latencies, 0.99), percentile(latencies, 0.999))
	fmt.Printf("RECV_WINDOW %s\n", elapsed)
	for _, d := range latencies {
		fmt.Printf("LATENCY_NS %d\n", d.Nanoseconds())
	}
	if rate < 0.99 {
		fmt.Println("RESULT FAIL delivery-rate")
		os.Exit(2)
	}
	fmt.Println("RESULT OK")
}

func runPublisher(ctx context.Context, h interface {
	Connect(context.Context, peer.AddrInfo) error
}, tp *pubsub.Topic, connect string, count, size int, gap time.Duration) {
	if connect == "" {
		fmt.Fprintln(os.Stderr, "-connect is required in pub mode")
		os.Exit(1)
	}
	ma, err := multiaddr.NewMultiaddr(connect)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect addr: %v\n", err)
		os.Exit(1)
	}
	ai, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect addr info: %v\n", err)
		os.Exit(1)
	}
	if err := h.Connect(ctx, *ai); err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(30 * time.Second)
	for len(tp.ListPeers()) == 0 {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "gossipsub mesh did not form")
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}

	payload := make([]byte, size)

	// Warm-up: early publishes can drop while heartbeats settle; send marked
	// messages for a fixed window, the subscriber excludes them.
	payload[8] = warmupMarker
	warmupEnd := time.Now().Add(3 * time.Second)
	for time.Now().Before(warmupEnd) {
		binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().UnixNano()))
		if err := tp.Publish(ctx, payload); err != nil {
			fmt.Fprintf(os.Stderr, "warm-up publish: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}

	payload[8] = 0
	start := time.Now()
	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().UnixNano()))
		if err := tp.Publish(ctx, payload); err != nil {
			fmt.Fprintf(os.Stderr, "publish %d: %v\n", i, err)
			os.Exit(1)
		}
		time.Sleep(gap)
	}
	fmt.Printf("PUBLISHED %d in %s\n", count, time.Since(start))
	// Give the last messages time to flush before tearing the host down.
	time.Sleep(2 * time.Second)
	fmt.Println("RESULT OK")
}
