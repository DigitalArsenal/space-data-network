package node

import (
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"testing"
)

func TestDatasetCatchupSchemaDiscoveryRespectsImportPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allow, deny []config.LaneSelector
		held, want  bool
	}{
		{name: "unlisted"},
		{name: "held", held: true, want: true},
		{name: "allowed source", allow: []config.LaneSelector{{Schema: "OMM.fbs", ProviderID: "provider", SourceName: "feed"}}, want: true},
		{name: "other schema", allow: []config.LaneSelector{{Schema: "CAT.fbs"}}},
		{name: "other peer", allow: []config.LaneSelector{{Peer: "other", Schema: "OMM.fbs"}}},
		{name: "wildcard", allow: []config.LaneSelector{{Schema: "*"}}, want: true},
		{name: "whole schema denied", held: true, deny: []config.LaneSelector{{Schema: "OMM.fbs"}}},
		{name: "one source denied", held: true, deny: []config.LaneSelector{{Schema: "OMM.fbs", SourceName: "blocked"}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.TrustedImportConfig{Allow: tc.allow, Deny: tc.deny}
			if got := mayDiscoverTrustedSchema(cfg, "peer", "OMM.fbs", tc.held); got != tc.want {
				t.Fatalf("discovery = %v, want %v", got, tc.want)
			}
		})
	}
}
