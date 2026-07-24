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

	cfg := config.Default()
	cfg.Storage.Path = "/var/lib/spacedatanetwork/data"
	cfg.Ingest = config.IngestConfig{
		Enabled:                true,
		SpaceTrackEnabled:      true,
		SpaceTrackPollInterval: "45m",
		HTTPTimeout:            "90s",
		MinFreeDiskGB:          2,
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
	if !out.SpaceTrackEnabled || out.SpaceTrackIdentity != "st-user" || out.SpaceTrackPassword != "st-pass" {
		t.Fatalf("Space-Track credentials not sourced from env: %+v", out.SpaceTrackEnabled)
	}
	if out.SpaceTrackPollInterval != 45*time.Minute {
		t.Fatalf("SpaceTrackPollInterval = %s", out.SpaceTrackPollInterval)
	}
	if out.HTTPTimeout != 90*time.Second {
		t.Fatalf("HTTPTimeout = %s", out.HTTPTimeout)
	}
	if out.MinFreeDiskBytes != 2*1024*1024*1024 {
		t.Fatalf("MinFreeDiskBytes = %d", out.MinFreeDiskBytes)
	}
	if out.Once {
		t.Fatalf("in-daemon ingest must run periodic workers, not once mode")
	}
}

func TestBuildIngestRunnerConfigRejectsBadDurations(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Path = "/tmp/store"
	cfg.Ingest = config.IngestConfig{
		Enabled:                true,
		SpaceTrackPollInterval: "thirty minutes",
	}
	_, err := buildIngestRunnerConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "ingest.spacetrack_poll_interval") {
		t.Fatalf("err = %v, want spacetrack_poll_interval parse error", err)
	}
}
