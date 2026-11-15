package update

import (
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// A pointer (a signal, a feed index entry) is followed only when it lies under
// the bundle's own update.feedBaseUrl.
func TestFeedBaseContainsOnlyPathsUnderTheBundleFeed(t *testing.T) {
	base, err := ParseFeedBase("https://sdn.spaceaware.io/updates")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url  string
		want string // "" means contained
	}{
		{url: "https://sdn.spaceaware.io/updates/cli-bundle/release/linux/amd64/1.1.2/manifest.json"},
		{url: "https://SDN.spaceaware.io/updates/cli-bundle/release/linux/amd64/1.1.2/update.wasm"},
		{url: "https://sdn.spaceaware.io:443/updates/x/manifest.json"},
		{url: "https://attacker.example/updates/x/manifest.json", want: "another host"},
		{url: "https://sdn.spaceaware.io.attacker.example/updates/x/manifest.json", want: "another host"},
		{url: "https://sdn.spaceaware.io:8443/updates/x/manifest.json", want: "another host"},
		{url: "http://sdn.spaceaware.io/updates/x/manifest.json", want: "another scheme"},
		{url: "https://user@sdn.spaceaware.io/updates/x/manifest.json", want: "credentials"},
		{url: "https://sdn.spaceaware.io/other/x/manifest.json", want: "not below"},
		{url: "https://sdn.spaceaware.io/updatesx/manifest.json", want: "not below"},
		{url: "https://sdn.spaceaware.io/updates", want: "not below"},
		{url: "https://sdn.spaceaware.io/updates/../admin/x.json", want: "clean"},
		{url: "https://sdn.spaceaware.io/updates//x/manifest.json", want: "clean"},
		{url: "https://sdn.spaceaware.io/updates/%2e%2e/admin/x.json", want: "percent-encoded"},
		{url: "https://sdn.spaceaware.io/updates/a%2Fb/manifest.json", want: "percent-encoded"},
		{url: "https://sdn.spaceaware.io/updates/x/manifest.json?redirect=https://attacker.example", want: "query"},
		{url: "https://sdn.spaceaware.io/updates/x/manifest.json#frag", want: "fragment"},
	} {
		err := base.Contains(tc.url, "manifest_url")
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v, want contained", tc.url, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want refused (%s)", tc.url, err, tc.want)
		}
	}
	for _, bad := range []string{"", "http://sdn.spaceaware.io/updates", "https:///updates", "https://u:p@sdn.spaceaware.io/updates", "https://sdn.spaceaware.io/updates?x=1"} {
		if _, err := ParseFeedBase(bad); err == nil {
			t.Errorf("ParseFeedBase(%q) accepted a feed base that is not a plain HTTPS URL", bad)
		}
	}
}

// A validly signed signal whose manifest or carrier lies off the receiving
// bundle's feed is refused by Verify, and its own feed_base_url cannot move
// the feed.
func TestSignalOffTheBundleFeedIsRefused(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)
	const feed = "https://sdn.spaceaware.io/updates"
	for name, mutate := range map[string]func(*Signal){
		"manifest on another host": func(s *Signal) { s.ManifestURL = "https://attacker.example/updates/m.json" },
		"carrier on another host":  func(s *Signal) { s.CarrierURL = "https://attacker.example/updates/u.wasm" },
		"its own feed_base_url names the other host": func(s *Signal) {
			s.FeedBaseURL = "https://attacker.example/updates"
			s.ManifestURL = "https://attacker.example/updates/m.json"
			s.CarrierURL = "https://attacker.example/updates/u.wasm"
		},
		"outside the feed path": func(s *Signal) { s.ManifestURL = "https://sdn.spaceaware.io/api/v1/m.json" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := signTestSignal(t, priv, keyID, func(s *Signal) {
				s.FeedBaseURL = feed
				s.ManifestURL = feed + "/cli-bundle/release/m.json"
				s.CarrierURL = feed + "/cli-bundle/release/u.wasm"
				mutate(s)
			}, sigdomain.DomainUpdateSignalV1)
			signal, err := ParseSignal(raw)
			if err != nil {
				t.Fatal(err)
			}
			opts := hostOpts(roots)
			opts.FeedBaseURL = feed
			if err := signal.Verify(opts); err == nil || !strings.Contains(err.Error(), "is not under this bundle's update feed") {
				t.Fatalf("Verify = %v, want refused as off the bundle's feed", err)
			}
		})
	}
	// The control: on the feed, the same signal verifies.
	raw := signTestSignal(t, priv, keyID, func(s *Signal) {
		s.FeedBaseURL = feed
		s.ManifestURL = feed + "/cli-bundle/release/m.json"
		s.CarrierURL = feed + "/cli-bundle/release/u.wasm"
	}, sigdomain.DomainUpdateSignalV1)
	signal, err := ParseSignal(raw)
	if err != nil {
		t.Fatal(err)
	}
	opts := hostOpts(roots)
	opts.FeedBaseURL = feed
	if err := signal.Verify(opts); err != nil {
		t.Fatalf("a signal on the bundle's feed: %v", err)
	}
}
