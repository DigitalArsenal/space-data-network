package node

import (
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/readbudget"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"path/filepath"
	"testing"
	"time"
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

func TestDatasetCatchupDiscoveryDoesNotStarveRateLimitedSchemas(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{validator: validator}
	seen := map[string]bool{}
	refused := 0
	// Each pass models a refilled provider burst, as the catch-up pass runs
	// it: the first refusal ends the pass and the next starts there. Use the
	// public listing budget and all registered schemas, including those
	// beyond its 256-read burst: every schema is listed within the passes the
	// burst needs.
	passes := (len(validator.Schemas()) + 255) / 256
	for pass := 0; pass < passes; pass++ {
		budget := readbudget.New(time.Hour, 256)
		schemas, err := n.datasetCatchupSchemas("provider")
		if err != nil {
			t.Fatal(err)
		}
		n.noteDatasetCatchupRefused("provider", "")
		for _, schema := range schemas {
			if budget.Allow("requester") != 0 {
				refused++
				n.noteDatasetCatchupRefused("provider", schema)
				break
			}
			seen[schema] = true
		}
	}
	if refused == 0 {
		t.Fatal("fixture did not exhaust the provider budget")
	}
	if len(seen) != len(validator.Schemas()) {
		t.Fatalf("listed %d of %d schemas across %d passes", len(seen), len(validator.Schemas()), passes)
	}
}

func TestDatasetCatchupSchemaReadErrorPolicy(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		policy    config.TrustedImportConfig
		wantError bool
	}{
		{name: "allow all", policy: config.TrustedImportConfig{NewLanes: "allow"}},
		{name: "wildcard allow", policy: config.TrustedImportConfig{Allow: []config.LaneSelector{{Schema: "*"}}}},
		{name: "scoped held discovery fails closed", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Peers.TrustedImport = tc.policy
			n := &Node{validator: validator, store: store, config: cfg}
			schemas, err := n.datasetCatchupSchemas("provider")
			if tc.wantError {
				if err == nil {
					t.Fatal("unreadable held-schema state must be reported")
				}
			} else if err != nil || len(schemas) != len(validator.Schemas()) {
				t.Fatalf("allow-all discovery: schemas=%d want=%d err=%v", len(schemas), len(validator.Schemas()), err)
			}
		})
	}
}
