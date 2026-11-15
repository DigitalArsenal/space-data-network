package node

import (
	"context"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// A VALIDLY SIGNED signal that points anywhere but this bundle's own feed
// (newSignalHarness: https://feed.example/updates) is refused before a single
// byte is fetched. Before this, a signal's manifest_url and carrier_url only
// had to be HTTPS on any host — so whoever holds a trusted key (and the root
// every bundle up to v1.1.1 trusted is the SEC-001 key, whose seed is public)
// could make every install contact a host of their choosing.
func TestSignalPointingOffTheBundleFeedIsRefusedBeforeAnyFetch(t *testing.T) {
	for name, mutate := range map[string]func(*update.Signal){
		"another host": func(s *update.Signal) {
			s.ManifestURL = "https://attacker.example/updates/m.json"
			s.CarrierURL = "https://attacker.example/updates/u.wasm"
		},
		"its own feed_base_url names another host": func(s *update.Signal) {
			s.FeedBaseURL = "https://attacker.example/updates"
			s.ManifestURL = "https://attacker.example/updates/m.json"
			s.CarrierURL = "https://attacker.example/updates/u.wasm"
		},
		"only the carrier on another host": func(s *update.Signal) {
			s.CarrierURL = "https://attacker.example/updates/u.wasm"
		},
		"another port on the feed host": func(s *update.Signal) {
			s.ManifestURL = "https://feed.example:8443/updates/m.json"
		},
		"outside the feed path": func(s *update.Signal) {
			s.ManifestURL = "https://feed.example/api/v1/admin/m.json"
		},
		"dot segments out of the feed path": func(s *update.Signal) {
			s.ManifestURL = "https://feed.example/updates/../api/m.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSignalHarness(t)
			h.sub.handle(context.Background(), h.sign(t, mutate))
			if got := h.transport.requests.Load(); got != 0 {
				t.Fatalf("a signed signal pointing off the bundle's feed caused %d fetch(es); it must be refused before any", got)
			}
			if h.launches.Load() != 0 {
				t.Fatal("nothing may be launched for a signal pointing off the bundle's feed")
			}
		})
	}
	// The control: the same harness on its own feed goes to fetch.
	h := newSignalHarness(t)
	h.sub.handle(context.Background(), h.sign(t, nil))
	if h.transport.requests.Load() == 0 {
		t.Fatal("a signal on the bundle's own feed must reach the fetch")
	}
}
