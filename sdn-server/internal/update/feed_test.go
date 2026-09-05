package update

import (
	"strings"
	"testing"
)

func TestProviderFeedSelectsHighestCompatibleCLIBundleUpdate(t *testing.T) {
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.index.v1",
		"generated_at": "2026-06-22T00:00:00Z",
		"feed_base_url": "https://sdn.spaceaware.io/updates",
		"updates": [
			{
				"update_id": "cli-bundle-beta-linux-amd64-1.0.4",
				"version": "1.0.4",
				"sequence": 104,
				"channel": "beta",
				"target": {"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
				"manifest_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.4/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.4/update.wasm"
			},
			{
				"update_id": "cli-bundle-beta-linux-amd64-1.0.5",
				"version": "1.0.5",
				"sequence": 105,
				"channel": "beta",
				"target": {"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
				"manifest_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/update.wasm"
			},
			{
				"update_id": "desktop-beta-linux-amd64-1.0.6",
				"version": "1.0.6",
				"sequence": 106,
				"channel": "beta",
				"target": {"platform": "linux", "arch": "amd64", "kind": "desktop-app"},
				"manifest_url": "https://sdn.spaceaware.io/updates/desktop/beta/linux/amd64/1.0.6/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/desktop/beta/linux/amd64/1.0.6/update.wasm"
			}
		]
	}`)

	feed, err := ParseProviderFeed(raw)
	if err != nil {
		t.Fatalf("ParseProviderFeed returned error: %v", err)
	}
	selected, err := feed.Select(ProviderFeedSelection{
		Channel:         "beta",
		Platform:        "linux",
		Arch:            "amd64",
		Kind:            "cli-bundle",
		CurrentSequence: 104,
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if selected.UpdateID != "cli-bundle-beta-linux-amd64-1.0.5" {
		t.Fatalf("selected update = %s, want cli-bundle-beta-linux-amd64-1.0.5", selected.UpdateID)
	}
}

func TestProviderFeedRejectsInsecurePayloadURLs(t *testing.T) {
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.index.v1",
		"generated_at": "2026-06-22T00:00:00Z",
		"feed_base_url": "https://sdn.spaceaware.io/updates",
		"updates": [
			{
				"update_id": "cli-bundle-beta-linux-amd64-1.0.5",
				"version": "1.0.5",
				"sequence": 105,
				"channel": "beta",
				"target": {"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
				"manifest_url": "http://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/update.wasm"
			}
		]
	}`)

	_, err := ParseProviderFeed(raw)
	if err == nil || !strings.Contains(err.Error(), "manifest_url must use HTTPS") {
		t.Fatalf("ParseProviderFeed error = %v, want manifest_url HTTPS rejection", err)
	}
}

func TestProviderFeedRejectsTargetMismatchWhenSelectingExplicitUpdate(t *testing.T) {
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.index.v1",
		"generated_at": "2026-06-22T00:00:00Z",
		"feed_base_url": "https://sdn.spaceaware.io/updates",
		"updates": [
			{
				"update_id": "cli-bundle-beta-darwin-amd64-1.0.5",
				"version": "1.0.5",
				"sequence": 105,
				"channel": "beta",
				"target": {"platform": "darwin", "arch": "amd64", "kind": "cli-bundle"},
				"manifest_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/darwin/amd64/1.0.5/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/darwin/amd64/1.0.5/update.wasm"
			}
		]
	}`)

	feed, err := ParseProviderFeed(raw)
	if err != nil {
		t.Fatalf("ParseProviderFeed returned error: %v", err)
	}
	_, err = feed.Select(ProviderFeedSelection{
		UpdateID:        "cli-bundle-beta-darwin-amd64-1.0.5",
		Channel:         "beta",
		Platform:        "linux",
		Arch:            "amd64",
		Kind:            "cli-bundle",
		CurrentSequence: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "no compatible update") {
		t.Fatalf("Select error = %v, want target mismatch rejection", err)
	}
}

func TestProviderFeedReportsNoNewUpdateAtCurrentSequence(t *testing.T) {
	raw := []byte(`{
		"schema": "org.spacedatanetwork.update.index.v1",
		"generated_at": "2026-06-22T00:00:00Z",
		"feed_base_url": "https://sdn.spaceaware.io/updates",
		"updates": [
			{
				"update_id": "cli-bundle-beta-linux-amd64-1.0.5",
				"version": "1.0.5",
				"sequence": 105,
				"channel": "beta",
				"target": {"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
				"manifest_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/manifest.json",
				"carrier_url": "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/1.0.5/update.wasm"
			}
		]
	}`)

	feed, err := ParseProviderFeed(raw)
	if err != nil {
		t.Fatalf("ParseProviderFeed returned error: %v", err)
	}
	_, err = feed.Select(ProviderFeedSelection{
		Channel:         "beta",
		Platform:        "linux",
		Arch:            "amd64",
		Kind:            "cli-bundle",
		CurrentSequence: 105,
	})
	if err == nil || !strings.Contains(err.Error(), "no compatible update") {
		t.Fatalf("Select error = %v, want no compatible update", err)
	}
}

func TestProviderFeedChecksManifestChannelAndTarget(t *testing.T) {
	sequence := int64(42)
	manifest := &Manifest{Sequence: &sequence, Channel: "beta", Target: ManifestTarget{Platform: "windows", Arch: "amd64", Kind: "cli-bundle"}}
	entry := ProviderFeedUpdate{Sequence: 42, Channel: "BETA", Target: ManifestTarget{Platform: "win32", Arch: "x64", Kind: "cli-bundle"}}
	if err := entry.AssertMatchesPayload(manifest, 0); err != nil {
		t.Fatalf("compatible platform aliases: %v", err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"channel":          func(m *Manifest) { m.Channel = "stable" },
		"platform":         func(m *Manifest) { m.Target.Platform = "linux" },
		"architecture":     func(m *Manifest) { m.Target.Arch = "arm64" },
		"kind":             func(m *Manifest) { m.Target.Kind = "desktop-app" },
		"missing sequence": func(m *Manifest) { m.Sequence = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *manifest
			mutate(&changed)
			if err := entry.AssertMatchesPayload(&changed, 0); err == nil {
				t.Fatal("feed/manifest disagreement was accepted")
			}
		})
	}
}
