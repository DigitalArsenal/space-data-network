package node

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// Coordinator D2 (multi-body round 2): a trusted peer's NEW lane is not
// imported unless listed; a held lane keeps importing; deny always wins.
func TestTrustedImportLanePolicy(t *testing.T) {
	held := func() bool { return true }
	fresh := func() bool { return false }
	const host01 = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	def := config.Default().Peers.TrustedImport
	cases := []struct {
		name     string
		cfg      config.TrustedImportConfig
		schema   string
		provider string
		source   string
		held     func() bool
		want     bool
		why      trustedImportDecision
	}{
		{"asteroids new on host-02", def, "SBD.fbs", "lowell-astorb", "astorb-numbered", fresh, false, trustedImportNew},
		{"kernels new on host-02", def, "PNM.fbs", "naif", "ssb-kernels", fresh, false, trustedImportNew},
		{"catalog already held keeps syncing", def, "OMM.fbs", "space-data-network-02", "celestrak-gp", held, true, trustedImportHeld},
		{"listed new lane", config.TrustedImportConfig{Allow: []config.LaneSelector{{Schema: "SBD.fbs", ProviderID: "lowell-astorb"}}}, "SBD.fbs", "lowell-astorb", "astorb-numbered", fresh, true, trustedImportAllowed},
		{"listed for another peer only", config.TrustedImportConfig{Allow: []config.LaneSelector{{Peer: "16Uiu2HAmOther", ProviderID: "lowell-astorb"}}}, "SBD.fbs", "lowell-astorb", "astorb-numbered", fresh, false, trustedImportNew},
		{"deny wins over held", config.TrustedImportConfig{Deny: []config.LaneSelector{{ProviderID: "naif"}}}, "PNM.fbs", "naif", "ssb-kernels", held, false, trustedImportDenied},
		{"deny wins over allow", config.TrustedImportConfig{Allow: []config.LaneSelector{{ProviderID: "*"}}, Deny: []config.LaneSelector{{Schema: "SBD.fbs", ProviderID: "lowell-astorb"}}}, "SBD.fbs", "lowell-astorb", "astorb-numbered", fresh, false, trustedImportDenied},
		{"new_lanes allow restores the old rule", config.TrustedImportConfig{NewLanes: "allow"}, "PNM.fbs", "usgs-astrogeology", "body-imagery", fresh, true, trustedImportNewLanes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := trustedImportLanePolicy(c.cfg, host01, c.schema, c.provider, c.source, c.held)
			if got != c.want || why != c.why {
				t.Fatalf("got %v (%s), want %v (%s)", got, why, c.want, c.why)
			}
		})
	}
}

// A node with no config of its own (Default) refuses new lanes.
func TestTrustedImportDefaultIsDenyNew(t *testing.T) {
	if config.Default().Peers.TrustedImport.NewLanesAllowed() {
		t.Fatal("default must deny new lanes from trusted peers")
	}
}
