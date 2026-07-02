package trust

import (
	"context"
	"crypto/ed25519"
	"sort"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestTrustEventEncodeDecodeVerify(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	change := StatusChange{
		Evaluator: "eve", Subject: "dave",
		OldScore: 0.3, NewScore: 0.7,
		OldTrusted: false, NewTrusted: true,
		AtMs: 424242,
	}
	env, err := EncodeTrustEvent(change, priv)
	if err != nil {
		t.Fatal(err)
	}
	evt, pub, err := DecodeTrustEvent(env)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Evaluator != "eve" || evt.Subject != "dave" || !evt.NewTrusted || evt.OldTrusted || evt.AtMs != 424242 {
		t.Fatalf("decoded event mismatch: %+v", evt)
	}
	wantPub := priv.Public().(ed25519.PublicKey)
	if string(pub) != string(wantPub) {
		t.Fatal("sender pub mismatch")
	}
	// Tampering the body breaks the signature.
	bad := append([]byte(nil), env...)
	bad[6] ^= 0x01
	if _, _, err := DecodeTrustEvent(bad); err == nil {
		t.Fatal("tampered event accepted")
	}
	// Truncation rejected.
	if _, _, err := DecodeTrustEvent(env[:len(env)-3]); err == nil {
		t.Fatal("truncated event accepted")
	}
}

func TestFanOutReachesExactlyTheNeighborhood(t *testing.T) {
	s := newFixtureService(t)
	_, priv, _ := ed25519.GenerateKey(nil)

	got := map[string][][]byte{}
	pub := &EventPublisher{
		SenderPriv: priv,
		Publish: func(topic string, data []byte) error {
			got[topic] = append(got[topic], data)
			return nil
		},
	}

	change := StatusChange{Evaluator: "eve", Subject: "dave", NewTrusted: true, AtMs: 1}
	delivered, err := pub.FanOut(s, []StatusChange{change})
	if err != nil {
		t.Fatal(err)
	}

	// dave's neighborhood (both directions, unbounded) + dave himself.
	wantMembers := []string{"alice", "bob", "carol", "dave", "eve", "stranger"}
	wantTopics := make([]string, 0, len(wantMembers))
	for _, m := range wantMembers {
		wantTopics = append(wantTopics, TrustTopic(m))
	}
	gotTopics := make([]string, 0, len(got))
	for topic := range got {
		gotTopics = append(gotTopics, topic)
	}
	sort.Strings(gotTopics)
	sort.Strings(wantTopics)
	if len(gotTopics) != len(wantTopics) {
		t.Fatalf("delivered to %v, want %v", gotTopics, wantTopics)
	}
	for i := range gotTopics {
		if gotTopics[i] != wantTopics[i] {
			t.Fatalf("delivered to %v, want %v", gotTopics, wantTopics)
		}
	}
	if delivered != len(wantTopics) {
		t.Fatalf("delivered = %d, want %d", delivered, len(wantTopics))
	}
	// Every payload decodes + verifies to the same event.
	for topic, envs := range got {
		for _, env := range envs {
			evt, _, err := DecodeTrustEvent(env)
			if err != nil {
				t.Fatalf("topic %s: %v", topic, err)
			}
			if evt.Subject != "dave" || !evt.NewTrusted {
				t.Fatalf("topic %s: wrong event %+v", topic, evt)
			}
		}
	}

	// Depth bound shrinks the audience: depth 1 = direct trusters/trustees.
	got = map[string][][]byte{}
	pub.MaxDepth = 1
	if _, err := pub.FanOut(s, []StatusChange{change}); err != nil {
		t.Fatal(err)
	}
	// dave depth-1: trusters alice, bob, carol, stranger (+dave). eve is 2 hops.
	if _, ok := got[TrustTopic("eve")]; ok {
		t.Fatal("depth-1 fan-out reached eve (2 hops away)")
	}
	if _, ok := got[TrustTopic("alice")]; !ok {
		t.Fatal("depth-1 fan-out missed alice (direct truster)")
	}
}

// TestTrustEventsOverGossipsub proves the full WS11 pipeline live: a funds
// mutation flips a subject's status, and the flip event lands (signed,
// verifiable) on a neighborhood member's real gossipsub trust topic.
func TestTrustEventsOverGossipsub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hA, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer hA.Close()
	hB, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer hB.Close()
	psA, err := pubsub.NewGossipSub(ctx, hA)
	if err != nil {
		t.Fatal(err)
	}
	psB, err := pubsub.NewGossipSub(ctx, hB)
	if err != nil {
		t.Fatal(err)
	}
	if err := hB.Connect(ctx, peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}); err != nil {
		t.Fatal(err)
	}

	// The member (alice's node) subscribes to HER trust topic.
	aliceTopicB, err := psB.Join(TrustTopic("alice"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := aliceTopicB.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	aliceTopicA, err := psA.Join(TrustTopic("alice"))
	if err != nil {
		t.Fatal(err)
	}

	// Wait for mesh.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(aliceTopicA.ListPeers()) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if len(aliceTopicA.ListPeers()) == 0 {
		t.Fatal("gossipsub mesh did not form")
	}

	// Trust service on the publisher node: pin the threshold, flip dave.
	svc := NewService(fixtureGraph(t), fixtureFunds())
	svc.nowMs = func() int64 { return 999 }
	svc.TrackEvaluator("eve")
	st, _ := svc.Status("eve", "dave")
	svc.Evaluator().Config.TrustThreshold = st.Score + 0.01
	svc2 := NewService(fixtureGraph(t), fixtureFunds())
	svc2.nowMs = func() int64 { return 999 }
	svc2.Evaluator().Config.TrustThreshold = st.Score + 0.01
	svc2.TrackEvaluator("eve")

	changes := svc2.UpdateFunds("dave", []FundHolding{{Type: FundStablecoin, Location: "0xdave", Amount: 10_000_000}})
	if len(changes) == 0 {
		t.Fatal("no flip produced")
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	topics := map[string]*pubsub.Topic{TrustTopic("alice"): aliceTopicA}
	pub := &EventPublisher{
		SenderPriv: priv,
		Publish: func(topic string, data []byte) error {
			tp, ok := topics[topic]
			if !ok {
				return nil // only alice's topic is wired in this test
			}
			return tp.Publish(ctx, data)
		},
	}
	if _, err := pub.FanOut(svc2, changes); err != nil {
		t.Fatal(err)
	}

	// Alice's node receives + verifies the flip event.
	msg, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("member receive: %v", err)
	}
	evt, senderPub, err := DecodeTrustEvent(msg.Data)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Subject != "dave" || !evt.NewTrusted || evt.OldTrusted || evt.AtMs != 999 {
		t.Fatalf("wrong event on the wire: %+v", evt)
	}
	if string(senderPub) != string(priv.Public().(ed25519.PublicKey)) {
		t.Fatal("sender attribution mismatch")
	}
}
