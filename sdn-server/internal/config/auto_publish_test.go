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
