package node

import (
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// These tests deliberately use the REAL go-libp2p BasicConnMgr rather than a
// fake. It is entirely in-process — no swarm, no listener, no network — and a
// fake would only prove that the controller calls the methods a fake records,
// not that it gets go-libp2p's actual Protect/Unprotect/UpsertTag semantics
// right. The multi-tag protection survival property in
// TestTrustDemotionKeepsConfigProtection is exactly the kind of thing a fake
// would have let through.

func testPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("derive peer id: %v", err)
	}
	return id
}

func testConnMgr(t *testing.T) *connmgr.BasicConnMgr {
	t.Helper()
	cm, err := connmgr.NewConnManager(4, 8)
	if err != nil {
		t.Fatalf("new conn manager: %v", err)
	}
	t.Cleanup(func() { _ = cm.Close() })
	return cm
}

func enabledPolicy() admissionPolicy {
	return resolveAdmissionPolicy(config.NetworkConfig{MaxConns: 1000})
}

// --- policy resolution ------------------------------------------------------

func TestResolveAdmissionPolicyDefaults(t *testing.T) {
	p := resolveAdmissionPolicy(config.Default().Network)

	if !p.Enabled {
		t.Fatal("policy must be enabled by default: the zero value of the config struct is the intended policy")
	}
	if p.Ceiling != 1000 {
		t.Fatalf("ceiling = %d, want the configured max_connections 1000", p.Ceiling)
	}
	if p.HighWater != 872 {
		t.Fatalf("high water = %d, want 1000 - 128 reserved headroom = 872", p.HighWater)
	}
	if p.LowWater != 654 {
		t.Fatalf("low water = %d, want 75%% of 872 = 654", p.LowWater)
	}
	if p.Headroom != 128 {
		t.Fatalf("headroom = %d, want 128", p.Headroom)
	}
	if p.GracePeriod != 30*time.Second {
		t.Fatalf("grace = %s, want 30s", p.GracePeriod)
	}
	if p.SilencePeriod != 10*time.Second {
		t.Fatalf("silence = %s, want 10s", p.SilencePeriod)
	}
	if p.ProtectTrustLevel != peers.Trusted {
		t.Fatalf("protect level = %s, want Trusted", p.ProtectTrustLevel)
	}
	if len(p.Notes) != 0 {
		t.Fatalf("a default config must resolve without adjustments, got %v", p.Notes)
	}
}

// TestResolveAdmissionPolicyHoldsTheBandInvariant is the whole point of the
// resolver: go-libp2p does NOT validate its own watermarks, so every path
// through this function must produce 0 < low <= high <= ceiling.
func TestResolveAdmissionPolicyHoldsTheBandInvariant(t *testing.T) {
	ceilings := []int{-5, 0, 1, 2, 3, 4, 8, 16, 64, 100, 999, 1000, 1152, 2560, 65535}
	for _, ceiling := range ceilings {
		for _, ac := range []config.PeerAdmissionConfig{
			{},
			{ReservedHeadroom: 128},
			{ReservedHeadroom: 1_000_000},
			{ReservedHeadroom: -1},
			{HighWater: 1_000_000},
			{LowWater: 1_000_000},
			{HighWater: 10, LowWater: 5000},
			{HighWater: 1, LowWater: 1},
		} {
			p := resolveAdmissionPolicy(config.NetworkConfig{MaxConns: ceiling, Admission: ac})
			if p.LowWater < 1 {
				t.Fatalf("ceiling=%d ac=%+v: low water %d must be positive", ceiling, ac, p.LowWater)
			}
			if p.LowWater > p.HighWater {
				t.Fatalf("ceiling=%d ac=%+v: low %d > high %d — this inversion silently disables trimming in go-libp2p",
					ceiling, ac, p.LowWater, p.HighWater)
			}
			if p.HighWater > p.Ceiling {
				t.Fatalf("ceiling=%d ac=%+v: high %d above ceiling %d", ceiling, ac, p.HighWater, p.Ceiling)
			}
			if p.Ceiling < 1 {
				t.Fatalf("ceiling=%d: resolved ceiling %d must be positive", ceiling, p.Ceiling)
			}
			if p.SilencePeriod <= 0 {
				t.Fatalf("ceiling=%d ac=%+v: silence period %s must be positive — connmgr rejects it otherwise",
					ceiling, ac, p.SilencePeriod)
			}
		}
	}
}

// TestResolveAdmissionPolicyFixesTheShippedWiring pins the two concrete
// misconfigurations the policy replaces, measured against real fleet configs.
func TestResolveAdmissionPolicyFixesTheShippedWiring(t *testing.T) {
	t.Run("default ceiling had no band at all", func(t *testing.T) {
		// Shipped: NewConnManager(1000, 1000) — low == high, so the node lives
		// permanently AT its watermark, which is the observed host-01 state.
		p := resolveAdmissionPolicy(config.NetworkConfig{MaxConns: 1000})
		if p.LowWater >= p.HighWater {
			t.Fatalf("low %d must sit strictly below high %d so a trim buys time", p.LowWater, p.HighWater)
		}
		if p.HighWater >= p.Ceiling {
			t.Fatalf("high %d must sit strictly below the ceiling %d so pinned peers keep headroom", p.HighWater, p.Ceiling)
		}
	})

	t.Run("celestrak.eth ceiling was inverted", func(t *testing.T) {
		// Shipped: NewConnManager(1000, 64) on a max_connections: 64 node.
		// go-libp2p accepts it and then never trims, because getConnsToClose
		// returns early whenever connCount <= lowWater.
		p := resolveAdmissionPolicy(config.NetworkConfig{MaxConns: 64})
		if p.LowWater > p.HighWater {
			t.Fatalf("low %d > high %d on a 64-connection node", p.LowWater, p.HighWater)
		}
		if p.HighWater > 64 {
			t.Fatalf("high %d exceeds the configured ceiling 64", p.HighWater)
		}
		if p.Headroom < 1 {
			t.Fatalf("a small node still needs headroom, got %d", p.Headroom)
		}
		// The reserved default (128) is twice this node's whole ceiling and
		// must have been clamped rather than driving the high water negative.
		if p.Headroom > 16 {
			t.Fatalf("headroom %d exceeds a quarter of the ceiling", p.Headroom)
		}
	})
}

// TestResolvedPolicyIsAcceptedByConnMgr closes the loop: the resolved numbers
// must actually construct a go-libp2p connection manager. WithSilencePeriod
// rejects a non-positive period, and a config typo must never stop the node
// booting.
func TestResolvedPolicyIsAcceptedByConnMgr(t *testing.T) {
	cases := []config.NetworkConfig{
		config.Default().Network,
		{MaxConns: 64},
		{MaxConns: 0},
		{MaxConns: 1, Admission: config.PeerAdmissionConfig{ReservedHeadroom: 99999}},
		{MaxConns: 500, Admission: config.PeerAdmissionConfig{GracePeriod: "not-a-duration", SilencePeriod: "0s"}},
		{MaxConns: 500, Admission: config.PeerAdmissionConfig{SilencePeriod: "-1m", GracePeriod: "-1m"}},
		{MaxConns: 500, Admission: config.PeerAdmissionConfig{Disabled: true}},
	}
	for i, cfg := range cases {
		p := resolveAdmissionPolicy(cfg)
		cm, err := connmgr.NewConnManager(p.LowWater, p.HighWater,
			connmgr.WithGracePeriod(p.GracePeriod),
			connmgr.WithSilencePeriod(p.SilencePeriod))
		if err != nil {
			t.Fatalf("case %d (%+v): resolved policy rejected by connmgr: %v", i, cfg, err)
		}
		_ = cm.Close()
	}
}

func TestResolveAdmissionPolicyClampsAndExplains(t *testing.T) {
	p := resolveAdmissionPolicy(config.NetworkConfig{
		MaxConns: 100,
		Admission: config.PeerAdmissionConfig{
			HighWater:         5000,
			LowWater:          4000,
			ReservedHeadroom:  90,
			GracePeriod:       "nonsense",
			SilencePeriod:     "0s",
			ProtectTrustLevel: "archangel",
		},
	})

	if p.HighWater != 100 || p.LowWater != 100 {
		t.Fatalf("high/low = %d/%d, want both clamped to the ceiling 100", p.HighWater, p.LowWater)
	}
	if p.GracePeriod != defaultAdmissionGracePeriod {
		t.Fatalf("grace = %s, want the default after an unparseable value", p.GracePeriod)
	}
	if p.SilencePeriod != defaultAdmissionSilencePeriod {
		t.Fatalf("silence = %s, want the default after a non-positive value", p.SilencePeriod)
	}
	if p.ProtectTrustLevel != peers.Trusted {
		t.Fatalf("protect level = %s, want the Trusted default after an unknown level", p.ProtectTrustLevel)
	}

	joined := strings.Join(p.Notes, " | ")
	for _, want := range []string{"high_water", "reserved_headroom", "grace_period", "silence_period", "protect_trust_level"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes must explain the %s substitution, got %q", want, joined)
		}
	}
	if !strings.Contains(p.Summary(), "ADJUSTED") {
		t.Fatalf("the boot log line must surface adjustments, got %q", p.Summary())
	}
}

func TestResolveAdmissionPolicyExplicitWatermarksWin(t *testing.T) {
	p := resolveAdmissionPolicy(config.NetworkConfig{
		MaxConns:  2000,
		Admission: config.PeerAdmissionConfig{HighWater: 1500, LowWater: 900, ProtectTrustLevel: "admin"},
	})
	if p.HighWater != 1500 || p.LowWater != 900 {
		t.Fatalf("high/low = %d/%d, want the configured 1500/900", p.HighWater, p.LowWater)
	}
	if p.Headroom != 500 {
		t.Fatalf("headroom = %d, want ceiling 2000 - high 1500", p.Headroom)
	}
	if p.ProtectTrustLevel != peers.Admin {
		t.Fatalf("protect level = %s, want Admin", p.ProtectTrustLevel)
	}
	if len(p.Notes) != 0 {
		t.Fatalf("valid explicit values need no adjustment, got %v", p.Notes)
	}
}

func TestResolveAdmissionPolicyDisabledIsASafeEscapeHatch(t *testing.T) {
	p := resolveAdmissionPolicy(config.NetworkConfig{
		MaxConns:  1000,
		Admission: config.PeerAdmissionConfig{Disabled: true},
	})
	if p.Enabled {
		t.Fatal("disabled must disable")
	}
	if p.Headroom != 0 {
		t.Fatalf("headroom = %d, want 0 when disabled", p.Headroom)
	}
	if p.LowWater > p.HighWater || p.LowWater < 1 {
		t.Fatalf("even disabled, the watermarks must stay valid: %d..%d", p.LowWater, p.HighWater)
	}
	if !strings.Contains(p.Summary(), "DISABLED") {
		t.Fatalf("the boot log must say the node is running without an admission policy, got %q", p.Summary())
	}
	if !strings.Contains(p.Summary(), "NOT protected") {
		t.Fatalf("the boot log must name the consequence, got %q", p.Summary())
	}
}

func TestAdmissionPolicySummaryStatesTheActivePolicy(t *testing.T) {
	s := enabledPolicy().Summary()
	for _, want := range []string{"PEER ADMISSION POLICY", "ceiling=1000", "654..872", "headroom=128", "grace=30s"} {
		if !strings.Contains(s, want) {
			t.Fatalf("boot log line must contain %q, got %q", want, s)
		}
	}
}

// --- SDN protocol reputation ------------------------------------------------

func TestIsSDNProtocol(t *testing.T) {
	// Served: what this host registers stream handlers for. It is a kubo fork,
	// so it serves the public DHT protocol too — which is precisely what the
	// flood speaks.
	served := map[coreprotocol.ID]struct{}{
		"/ipfs/kad/1.0.0":                        {},
		"/ipfs/id/1.0.0":                         {},
		"/libp2p/circuit/relay/0.2.0/hop":        {},
		"/meshsub/1.1.0":                         {},
		"/spacedatanetwork/sds-exchange/1.0.0":   {},
		"/space-data-network/flatsql-sync/1.0.0": {},
		"/acme-module/telemetry/1.0.0":           {},
	}

	cases := []struct {
		id   coreprotocol.ID
		want bool
		why  string
	}{
		{"/spacedatanetwork/sds-exchange/1.0.0", true, "SDS exchange is ours"},
		{"/spacedatanetwork/epm-exchange/1.0.0", true, "EPM exchange is ours even when unserved in this table"},
		{"/space-data-network/flatsql-sync/1.0.0", true, "the FlatSQL sync lane is ours"},
		{"/space-data-network/id-exchange/1.0.0", true, "the legacy prefix is still ours"},
		{"/sdn/updates/v1/beta", true, "the update lane is ours"},
		{"/acme-module/telemetry/1.0.0", true, "a module-registered protocol we serve counts, which is why `served` exists"},
		{"/ipfs/kad/1.0.0", false, "the public DHT is served but is exactly the churn this policy is about"},
		{"/ipfs/id/1.0.0", false, "identify is libp2p commons"},
		{"/libp2p/circuit/relay/0.2.0/hop", false, "relay is libp2p commons"},
		{"/meshsub/1.1.0", false, "gossipsub is libp2p commons"},
		{"/some/random/thing", false, "a protocol we do not serve tells us nothing"},
		{"", false, "empty is not a protocol"},
	}
	for _, tc := range cases {
		if got := isSDNProtocol(tc.id, served); got != tc.want {
			t.Errorf("isSDNProtocol(%q) = %v, want %v: %s", tc.id, got, tc.want, tc.why)
		}
	}
}

func TestSDNPeerTagValue(t *testing.T) {
	served := map[coreprotocol.ID]struct{}{"/ipfs/kad/1.0.0": {}}

	// The measured flood: a peer that speaks only public DHT and identify.
	churn := []coreprotocol.ID{"/ipfs/kad/1.0.0", "/ipfs/id/1.0.0", "/ipfs/ping/1.0.0"}
	if v := sdnPeerTagValue(churn, served); v != 0 {
		t.Fatalf("anonymous DHT churn tagged %d, want 0 so connmgr trims it first", v)
	}

	// One SDN protocol anywhere in the advertised set is proof enough.
	ours := append(append([]coreprotocol.ID{}, churn...), "/spacedatanetwork/epm-exchange/1.0.0")
	if v := sdnPeerTagValue(ours, served); v != admissionSDNPeerTagValue {
		t.Fatalf("SDN peer tagged %d, want %d", v, admissionSDNPeerTagValue)
	}

	if v := sdnPeerTagValue(nil, served); v != 0 {
		t.Fatalf("a peer that advertised nothing tagged %d, want 0", v)
	}
}

// --- controller: protection -------------------------------------------------

func TestProtectFleetProtectsEverySource(t *testing.T) {
	cm := testConnMgr(t)
	registry := peers.NewRegistry(false, nil)
	c := newPeerAdmissionController(enabledPolicy(), cm, registry)

	bootstrapPeer := testPeerID(t)
	trustedPeer := testPeerID(t)
	pinnedPeer := testPeerID(t)
	registryPeer := testPeerID(t)
	strangerPeer := testPeerID(t)

	if err := registry.AddPeer(&peers.TrustedPeer{ID: registryPeer, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add registry peer: %v", err)
	}
	if err := registry.AddPeer(&peers.TrustedPeer{ID: strangerPeer, TrustLevel: peers.Standard}); err != nil {
		t.Fatalf("add stranger: %v", err)
	}

	c.ProtectFleet(
		[]string{
			fmt.Sprintf("/ip4/167.172.219.213/tcp/4001/p2p/%s", bootstrapPeer),
			"  ", // blank line in a config must not panic
			"/ip4/1.2.3.4/tcp/4001/p2p/not-a-peer-id", // malformed must be skipped, not fatal
		},
		[]string{fmt.Sprintf("/ip4/10.0.0.2/tcp/4001/p2p/%s", trustedPeer)},
		[]peers.Pin{{PeerID: pinnedPeer.String()}, {PeerID: "garbage"}},
	)

	for name, tc := range map[string]struct {
		id  peer.ID
		tag string
	}{
		"bootstrap peer (the fleet)": {bootstrapPeer, admissionTagBootstrap},
		"config trusted peer":        {trustedPeer, admissionTagConfigTrusted},
		"operator pin":               {pinnedPeer, admissionTagPinned},
		"registry Trusted peer":      {registryPeer, admissionTagRegistryTrust},
	} {
		if !cm.IsProtected(tc.id, tc.tag) {
			t.Errorf("%s must be protected under %q — a trim must never be able to evict it", name, tc.tag)
		}
	}

	if cm.IsProtected(strangerPeer, "") {
		t.Error("a Standard-trust peer must NOT be protected: protection is for the fleet, not for everyone we have met")
	}
	if got := c.Stats().ProtectedPeers; got != 4 {
		t.Errorf("protected peer count = %d, want 4", got)
	}
}

func TestTrustPromotionProtectsAndDemotionReleases(t *testing.T) {
	cm := testConnMgr(t)
	c := newPeerAdmissionController(enabledPolicy(), cm, peers.NewRegistry(false, nil))
	id := testPeerID(t)

	c.HandleTrustChange(id, peers.Standard, peers.Trusted)
	if !cm.IsProtected(id, admissionTagRegistryTrust) {
		t.Fatal("promotion to Trusted must protect the peer")
	}
	if got := c.Stats().ProtectedPeers; got != 1 {
		t.Fatalf("protected count = %d, want 1", got)
	}

	c.HandleTrustChange(id, peers.Trusted, peers.Standard)
	if cm.IsProtected(id, "") {
		t.Fatal("demotion below the protect level must release the protection")
	}
	if got := c.Stats().ProtectedPeers; got != 0 {
		t.Fatalf("protected count = %d, want 0 after demotion", got)
	}

	// Admin is above Trusted and must also protect.
	c.HandleTrustChange(id, peers.Standard, peers.Admin)
	if !cm.IsProtected(id, admissionTagRegistryTrust) {
		t.Fatal("Admin is above the protect level and must protect")
	}
}

// TestTrustDemotionKeepsConfigProtection is why the protection tags are
// distinct: an operator lowering a peer's registry trust must not silently
// strip the protection the OPERATOR'S OWN CONFIG FILE granted it.
func TestTrustDemotionKeepsConfigProtection(t *testing.T) {
	cm := testConnMgr(t)
	c := newPeerAdmissionController(enabledPolicy(), cm, peers.NewRegistry(false, nil))
	id := testPeerID(t)

	c.ProtectFleet(nil, []string{fmt.Sprintf("/ip4/10.0.0.2/tcp/4001/p2p/%s", id)}, nil)
	c.HandleTrustChange(id, peers.Standard, peers.Trusted)
	c.HandleTrustChange(id, peers.Trusted, peers.Untrusted)

	if !cm.IsProtected(id, admissionTagConfigTrusted) {
		t.Fatal("a config-trusted peer must stay protected through a registry trust demotion")
	}
	if got := c.Stats().ProtectedPeers; got != 1 {
		t.Fatalf("protected count = %d, want 1 (the peer is still protected by its config tag)", got)
	}
}

// --- controller: reputation tagging ----------------------------------------

func TestObserveIdentifiedTagsOnlySDNPeers(t *testing.T) {
	cm := testConnMgr(t)
	c := newPeerAdmissionController(enabledPolicy(), cm, nil)
	served := map[coreprotocol.ID]struct{}{"/ipfs/kad/1.0.0": {}}

	sdnPeer := testPeerID(t)
	churnPeer := testPeerID(t)

	c.observeIdentified(sdnPeer, []coreprotocol.ID{"/spacedatanetwork/sds-exchange/1.0.0"}, served)
	c.observeIdentified(churnPeer, []coreprotocol.ID{"/ipfs/kad/1.0.0"}, served)

	info := cm.GetTagInfo(sdnPeer)
	if info == nil || info.Tags[admissionTagSDNPeer] != admissionSDNPeerTagValue {
		t.Fatalf("SDN peer tag info = %+v, want %s=%d", info, admissionTagSDNPeer, admissionSDNPeerTagValue)
	}
	if churnInfo := cm.GetTagInfo(churnPeer); churnInfo != nil && churnInfo.Tags[admissionTagSDNPeer] != 0 {
		t.Fatalf("anonymous churn must carry no SDN value, got %+v", churnInfo.Tags)
	}

	stats := c.Stats()
	if stats.IdentifiedPeers != 2 || stats.SDNTaggedPeers != 1 || stats.AnonymousPeers != 1 {
		t.Fatalf("stats = %+v, want 2 identified / 1 tagged / 1 anonymous", stats)
	}
}

// TestReIdentifyDoesNotStackValue: TagPeer would ADD on every reconnect, which
// would let a churning peer out-rank the fleet simply by reconnecting. UpsertTag
// must hold the value flat.
func TestReIdentifyDoesNotStackValue(t *testing.T) {
	cm := testConnMgr(t)
	c := newPeerAdmissionController(enabledPolicy(), cm, nil)
	id := testPeerID(t)

	for i := 0; i < 25; i++ {
		c.observeIdentified(id, []coreprotocol.ID{"/sdn/updates/v1/beta"}, nil)
	}

	info := cm.GetTagInfo(id)
	if info == nil || info.Value != admissionSDNPeerTagValue {
		t.Fatalf("value after 25 re-identifies = %+v, want a flat %d", info, admissionSDNPeerTagValue)
	}
	if got := c.Stats().SDNTaggedPeers; got != 1 {
		t.Fatalf("tagged peer count = %d, want 1 — this is a peer count, not a call count", got)
	}
}

func TestSDNPeersOutrankChurnInTheTrimOrder(t *testing.T) {
	cm := testConnMgr(t)
	c := newPeerAdmissionController(enabledPolicy(), cm, nil)
	sdnPeer, churnPeer := testPeerID(t), testPeerID(t)

	c.observeIdentified(sdnPeer, []coreprotocol.ID{"/spacedatanetwork/epm-exchange/1.0.0"}, nil)
	c.observeIdentified(churnPeer, []coreprotocol.ID{"/ipfs/kad/1.0.0"}, nil)

	sdnValue, churnValue := 0, 0
	if info := cm.GetTagInfo(sdnPeer); info != nil {
		sdnValue = info.Value
	}
	if info := cm.GetTagInfo(churnPeer); info != nil {
		churnValue = info.Value
	}
	// BasicConnMgr.getConnsToClose sorts candidates ASCENDING by value and
	// closes from the front, so a strictly greater value is what keeps an SDN
	// peer alive through a trim.
	if !(sdnValue > churnValue) {
		t.Fatalf("SDN peer value %d must exceed churn value %d, or the trim order is arbitrary", sdnValue, churnValue)
	}
}

// --- controller: disabled and nil safety -----------------------------------

func TestDisabledControllerIsInert(t *testing.T) {
	cm := testConnMgr(t)
	disabled := resolveAdmissionPolicy(config.NetworkConfig{
		MaxConns:  1000,
		Admission: config.PeerAdmissionConfig{Disabled: true},
	})
	c := newPeerAdmissionController(disabled, cm, peers.NewRegistry(false, nil))
	id := testPeerID(t)

	c.ProtectFleet([]string{fmt.Sprintf("/ip4/10.0.0.2/tcp/4001/p2p/%s", id)}, nil, nil)
	c.HandleTrustChange(id, peers.Standard, peers.Trusted)
	c.observeIdentified(id, []coreprotocol.ID{"/spacedatanetwork/sds-exchange/1.0.0"}, nil)

	if cm.IsProtected(id, "") {
		t.Error("a disabled policy must not protect anything")
	}
	if info := cm.GetTagInfo(id); info != nil && info.Value != 0 {
		t.Errorf("a disabled policy must not tag anything, got %+v", info)
	}
	if s := c.Stats(); s.Enabled || s.ProtectedPeers != 0 || s.SDNTaggedPeers != 0 {
		t.Errorf("stats = %+v, want an inert policy", s)
	}
}

func TestNilControllerIsSafe(t *testing.T) {
	var c *peerAdmissionController
	id := testPeerID(t)

	// Every one of these runs on a node whose host failed to build.
	c.ProtectFleet([]string{id.String()}, nil, nil)
	c.HandleTrustChange(id, peers.Standard, peers.Trusted)
	c.observeIdentified(id, []coreprotocol.ID{"/sdn/x"}, nil)
	if s := c.Stats(); s.Enabled {
		t.Fatalf("nil controller stats = %+v, want the zero value", s)
	}

	var n *Node
	if s := n.PeerAdmission(); s.Enabled {
		t.Fatalf("nil node stats = %+v, want the zero value", s)
	}
}
