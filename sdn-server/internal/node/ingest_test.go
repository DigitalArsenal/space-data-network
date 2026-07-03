package node

import (
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestBuildIngestRunnerConfigMapsYAMLOntoRunnerConfig(t *testing.T) {
	t.Setenv("SPACETRACK_IDENTITY", "st-user")
	t.Setenv("SPACETRACK_PASSWORD", "st-pass")
	t.Setenv("UDL_USERNAME", "")
	t.Setenv("UDL_PASSWORD", "")
	t.Setenv("SDN_DATASET_PUBLISH_URL", "")

	cfg := config.Default()
	cfg.Storage.Path = "/var/lib/spacedatanetwork/data"
	cfg.Ingest = config.IngestConfig{
		Enabled:              true,
		CelestrakInterval:    "3h",
		SatcatInterval:       "24h",
		SpaceWeatherInterval: "6h",
		SpaceTrackEnabled:    true,
		HTTPTimeout:          "900s",
		MinFreeDiskGB:        2,
		DatasetPublishURL:    "http://127.0.0.1:5001/api/v1/admin/dataset-updates/publish",
	}

	out, err := buildIngestRunnerConfig(cfg)
	if err != nil {
		t.Fatalf("buildIngestRunnerConfig: %v", err)
	}
	if out.StoragePath != "/var/lib/spacedatanetwork/data" {
		t.Fatalf("StoragePath = %q", out.StoragePath)
	}
	if out.RawPath != "/var/lib/spacedatanetwork/raw" {
		t.Fatalf("RawPath = %q, want default <storage-parent>/raw", out.RawPath)
	}
	if out.CelestrakInterval != 3*time.Hour || out.SatcatInterval != 24*time.Hour || out.SpaceWeatherInterval != 6*time.Hour {
		t.Fatalf("intervals = %s/%s/%s", out.CelestrakInterval, out.SatcatInterval, out.SpaceWeatherInterval)
	}
	if !out.SpaceTrackEnabled || out.SpaceTrackIdentity != "st-user" || out.SpaceTrackPassword != "st-pass" {
		t.Fatalf("Space-Track credentials not sourced from env: %+v", out.SpaceTrackEnabled)
	}
	if out.HTTPTimeout != 900*time.Second {
		t.Fatalf("HTTPTimeout = %s", out.HTTPTimeout)
	}
	if out.MinFreeDiskBytes != 2*1024*1024*1024 {
		t.Fatalf("MinFreeDiskBytes = %d", out.MinFreeDiskBytes)
	}
	if out.DatasetPublishURL != "http://127.0.0.1:5001/api/v1/admin/dataset-updates/publish" {
		t.Fatalf("DatasetPublishURL = %q", out.DatasetPublishURL)
	}
	if out.Once {
		t.Fatalf("in-daemon ingest must run periodic workers, not once mode")
	}
}

func TestBuildIngestRunnerConfigRejectsBadDurations(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Path = "/tmp/store"
	cfg.Ingest = config.IngestConfig{
		Enabled:           true,
		CelestrakInterval: "three hours",
	}
	_, err := buildIngestRunnerConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "ingest.celestrak_interval") {
		t.Fatalf("err = %v, want celestrak_interval parse error", err)
	}
}
