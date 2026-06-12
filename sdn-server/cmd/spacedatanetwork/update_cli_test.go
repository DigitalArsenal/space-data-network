package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBundleManifestAcceptsSignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature",
		"update": {
			"feedBaseUrl": "https://updates.spacedatanetwork.org",
			"pubsubTopic": "/sdn/updates/v1/beta",
			"updaterModule": "org.spacedatanetwork.updater",
			"updaterWasm": "runtime/modules/org.spacedatanetwork.updater.wasm"
		}
	}`)

	manifest, err := loadBundleManifest(path)
	if err != nil {
		t.Fatalf("loadBundleManifest returned error: %v", err)
	}
	if manifest.Version != "1.2.3" {
		t.Fatalf("Version = %q, want 1.2.3", manifest.Version)
	}
	if manifest.Channel != "beta" {
		t.Fatalf("Channel = %q, want beta", manifest.Channel)
	}
	if manifest.Update.FeedBaseURL != "https://updates.spacedatanetwork.org" ||
		manifest.Update.PubsubTopic != "/sdn/updates/v1/beta" ||
		manifest.Update.UpdaterModule != "org.spacedatanetwork.updater" ||
		manifest.Update.UpdaterWASM != "runtime/modules/org.spacedatanetwork.updater.wasm" {
		t.Fatalf("Update metadata = %#v", manifest.Update)
	}
}

func TestLoadBundleManifestRejectsMissingUpdateMetadata(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without update metadata")
	}
}

func TestLoadBundleManifestRejectsUnsignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted an unsigned manifest")
	}
}

func TestLoadBundleManifestRejectsMissingVersion(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without version")
	}
}

func TestLoadBundleManifestRejectsWhitespaceRequiredFields(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": " ",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest with whitespace-only version")
	}
}

func writeBundleManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
