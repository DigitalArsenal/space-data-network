package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestPlanManagedKubo(t *testing.T) {
	dir := t.TempDir()
	kuboBin := filepath.Join(dir, "runtime", "kubo", "ipfs")
	if err := os.MkdirAll(filepath.Dir(kuboBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kuboBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	layout := bundle.Layout{Root: dir, KuboBinary: kuboBin}
	data := filepath.Join(dir, "data")

	// A bundle with Kubo and the default API URL: managed, under the data
	// dir, on 127.0.0.1:5002 — off the admin listener's 5001.
	cfg := config.Default()
	plan, reason := planManagedKubo(cfg, layout, data)
	if plan == nil {
		t.Fatalf("bundle Kubo not managed: %s", reason)
	}
	if plan.Binary != kuboBin || plan.RepoPath != filepath.Join(data, "kubo") || plan.APIAddr != "127.0.0.1:5002" {
		t.Fatalf("plan = %+v", plan)
	}

	// An operator-run Kubo named in the config is left alone.
	cfg.Admin.IPFSAPIURL = "http://127.0.0.1:5999"
	if plan, reason := planManagedKubo(cfg, layout, data); plan != nil {
		t.Fatalf("operator Kubo overridden: %+v", plan)
	} else if reason == "" {
		t.Fatal("no reason given for leaving the operator's Kubo alone")
	}

	// No bundle, no binary: not managed, and the reason says why.
	cfg = config.Default()
	if plan, reason := planManagedKubo(cfg, bundle.Layout{}, data); plan != nil || reason == "" {
		t.Fatalf("bare binary without Kubo: plan=%+v reason=%q", plan, reason)
	}

	// An existing operator repository named by asset_pins.kubo_repo_path is
	// used; a path that does not exist (the config default names a
	// production volume) is not, and the binary can come from SDN_KUBO_BINARY.
	existing := filepath.Join(dir, "elsewhere", "repo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "config"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	cfg.AssetPins.KuboRepoPath = existing
	t.Setenv("SDN_KUBO_BINARY", kuboBin)
	plan, _ = planManagedKubo(cfg, bundle.Layout{}, data)
	if plan == nil || plan.RepoPath != existing || plan.Binary != kuboBin {
		t.Fatalf("explicit paths ignored: %+v", plan)
	}
	cfg.AssetPins.KuboRepoPath = "/mnt/volume_nyc3_01/ipfs"
	plan, _ = planManagedKubo(cfg, bundle.Layout{}, data)
	if plan == nil || plan.RepoPath != filepath.Join(data, "kubo") {
		t.Fatalf("a non-existent production default repo path was honoured: %+v", plan)
	}
}
