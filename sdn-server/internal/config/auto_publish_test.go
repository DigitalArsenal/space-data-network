package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// publishing.auto_publish is the seam that turns "a batch landed" into "the
// network can see it" (sdn-rfb-publish-to-consumer-node). Two properties are
// load-bearing: it parses exactly as an operator writes it, and it is EMPTY by
// default — a node never republishes a source nobody asked it to.

func TestDefaultConfigPublishesNoLaneAutomatically(t *testing.T) {
	if lanes := Default().Publishing.AutoPublish; len(lanes) != 0 {
		t.Fatalf("Default().Publishing.AutoPublish = %+v, want empty (absence of config is not permission to republish)", lanes)
	}
}

// The producer's SHIPPED config is the thing that actually decides whether the
// RF catalogue reaches the consumer node. A config that no longer parses, or
// that quietly loses the lane, reproduces the original defect exactly — so the
// checked-in file is loaded here rather than trusted.
func TestShippedCelesTrakConfigCarriesTheRFBAutoPublishLane(t *testing.T) {
	configPath := filepath.Join("..", "..", "..", "deployment", "celestrak", "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Skipf("deployment config not present in this checkout: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("shipped celestrak config does not load: %v", err)
	}
	for _, lane := range cfg.Publishing.AutoPublish {
		if lane.Schema == "RFB.fbs" && lane.SourceName == "satnogs-db" {
			return
		}
	}
	t.Fatalf("shipped celestrak config declares no RFB.fbs/satnogs-db auto_publish lane: %+v", cfg.Publishing.AutoPublish)
}

// The cellular $TBS lane (sdn-tbs-feed-sync-for-cache-lane, 2026-08-21) is
// the producer-side trigger of the host-02 -> host-01 topology hop: the
// aggregate cache on the consumer node serves from ITS OWN store, and this
// shipped lane is what turns a landed cell-tower ingest batch into a dataset
// publication the consumer's schema-generic feed-head subscription
// materializes. A shipped config that loses the lane reproduces the exact
// "store fills locally, peers never see it" defect the RFB lane fixed — so
// the checked-in file is loaded here rather than trusted.
//
// The lane must name the source the box ACTUALLY ingests. Verified live
// 2026-08-26: host-02 runs the cell-tower ingest flow as provider
// "mls-archive" / source "mls-final-full-cell-export", while this file
// declared only "cell-tower-bulk" — the module DEFAULT, matching nothing that
// box produces. The lane existed, was armed at boot, and published nothing;
// host-01's cellular tiles read records:0 the whole time. A lane whose
// narrowing matches no live source is indistinguishable from no lane at all,
// so both spellings are pinned here.
func TestShippedCelesTrakConfigCarriesTheTBSAutoPublishLane(t *testing.T) {
	configPath := filepath.Join("..", "..", "..", "deployment", "celestrak", "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Skipf("deployment config not present in this checkout: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("shipped celestrak config does not load: %v", err)
	}
	wanted := map[string]bool{
		"mls-final-full-cell-export": false,
		"cell-tower-bulk":            false,
	}
	for _, lane := range cfg.Publishing.AutoPublish {
		if lane.Schema != "TBS.fbs" {
			continue
		}
		if _, ok := wanted[lane.SourceName]; !ok {
			continue
		}
		wanted[lane.SourceName] = true
		// A publication is scoped to ONE batch id and ObserveIngest DISCARDS
		// a batch that lands inside the lane window instead of deferring it,
		// so a whole-payload interval on a PAGED source loses chunks. This is
		// the arithmetic, not a preference.
		if lane.MinInterval > time.Minute {
			t.Fatalf("TBS.fbs/%s min_interval %s is longer than the paging tick: batches inside the window are dropped, not deferred",
				lane.SourceName, lane.MinInterval)
		}
	}
	for source, found := range wanted {
		if !found {
			t.Fatalf("shipped celestrak config declares no TBS.fbs/%s auto_publish lane: %+v", source, cfg.Publishing.AutoPublish)
		}
	}
}

func TestLoadAutoPublishLanesFromYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	yamlDoc := "publishing:\n" +
		"  auto_publish:\n" +
		"    - schema: RFB.fbs\n" +
		"      provider_id: space-data-network-02\n" +
		"      source_name: satnogs-db\n" +
		"      min_interval: 30m\n" +
		"    - schema: OMM.fbs\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	lanes := cfg.Publishing.AutoPublish
	if len(lanes) != 2 {
		t.Fatalf("auto_publish lanes = %d, want 2", len(lanes))
	}
	if lanes[0].Schema != "RFB.fbs" ||
		lanes[0].ProviderID != "space-data-network-02" ||
		lanes[0].SourceName != "satnogs-db" ||
		lanes[0].MinInterval != 30*time.Minute {
		t.Fatalf("first lane = %+v, want the SatNOGS RF lane", lanes[0])
	}
	if lanes[1].Schema != "OMM.fbs" || lanes[1].ProviderID != "" || lanes[1].SourceName != "" {
		t.Fatalf("second lane = %+v, want a schema-only lane", lanes[1])
	}
	if lanes[1].MinInterval != 0 {
		t.Fatalf("unset min_interval = %s, want 0 (the runtime default applies)", lanes[1].MinInterval)
	}
}
