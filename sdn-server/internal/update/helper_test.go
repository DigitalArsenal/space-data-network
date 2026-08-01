package update

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPrepareHelperPlanCopiesExecutableOutsideReplaceableBundlePaths(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := PrepareHelperPlan(HelperPlanOptions{
		Paths:            PathsFor(root),
		SourceExecutable: source,
		UpdateID:         "cli-bundle-beta-linux-amd64-1.0.5",
		AdminURL:         "http://127.0.0.1:5001/",
		Token:            "token-123",
		RestartArgv:      []string{source, "daemon", "--config", "/tmp/sdn-config.yaml"},
	})
	if err != nil {
		t.Fatalf("PrepareHelperPlan returned error: %v", err)
	}

	wantHelper := filepath.Join(root, "updates", "helper", "spacedatanetwork")
	if plan.Executable != wantHelper {
		t.Fatalf("helper executable = %q, want %q", plan.Executable, wantHelper)
	}
	if _, err := os.Stat(wantHelper); err != nil {
		t.Fatalf("helper executable was not copied: %v", err)
	}
	if slices.Contains(plan.Args, "--bundle-root") == false || slices.Contains(plan.Args, root) == false {
		t.Fatalf("helper args do not include bundle root: %#v", plan.Args)
	}
	if slices.Contains(plan.Args, "--update-id") == false || slices.Contains(plan.Args, "cli-bundle-beta-linux-amd64-1.0.5") == false {
		t.Fatalf("helper args do not include update id: %#v", plan.Args)
	}
	if slices.Contains(plan.Args, "--restart-argv-json") == false {
		t.Fatalf("helper args do not include restart argv json: %#v", plan.Args)
	}
}

func TestPrepareHelperPlanAllowsNoRestartWhenArgsUnavailable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "runtime", "sdn", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := PrepareHelperPlan(HelperPlanOptions{
		Paths:            PathsFor(root),
		SourceExecutable: source,
		UpdateID:         "cli-bundle-beta-linux-amd64-1.0.5",
	})
	if err != nil {
		t.Fatalf("PrepareHelperPlan returned error: %v", err)
	}
	if !slices.Contains(plan.Args, "--no-restart") {
		t.Fatalf("helper args should report no restart when restart argv is unavailable: %#v", plan.Args)
	}
}

func TestPrepareHelperPlanLetsDaemonProvideRestartArgs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "runtime", "sdn", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := PrepareHelperPlan(HelperPlanOptions{
		Paths:            PathsFor(root),
		SourceExecutable: source,
		UpdateID:         "cli-bundle-beta-linux-amd64-1.0.5",
		AdminURL:         "http://127.0.0.1:5001/",
		Token:            "token-123",
	})
	if err != nil {
		t.Fatalf("PrepareHelperPlan returned error: %v", err)
	}
	if slices.Contains(plan.Args, "--no-restart") {
		t.Fatalf("helper args should allow restart argv from daemon response: %#v", plan.Args)
	}
	if !slices.Contains(plan.Args, "--admin-url") || !slices.Contains(plan.Args, "--token") {
		t.Fatalf("helper args should include daemon control details: %#v", plan.Args)
	}
}

// Regression for the 2026-08-01 host-02 false rollback: the 60s default
// health gate cannot fit a store-heavy node whose boot replays the record
// catalog for minutes, so the install lane must be able to carry a longer
// budget through to the helper.
func TestPrepareHelperPlanForwardsHealthTimeout(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "runtime", "sdn", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := PrepareHelperPlan(HelperPlanOptions{
		Paths:            PathsFor(root),
		SourceExecutable: source,
		UpdateID:         "cli-bundle-beta-linux-amd64-1.0.7",
		AdminURL:         "http://127.0.0.1:5001/",
		Token:            "token-123",
		HealthTimeout:    20 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PrepareHelperPlan returned error: %v", err)
	}
	idx := slices.Index(plan.Args, "--health-timeout")
	if idx < 0 || idx+1 >= len(plan.Args) || plan.Args[idx+1] != "20m0s" {
		t.Fatalf("helper args must carry the health timeout: %#v", plan.Args)
	}

	// Zero keeps the default: the flag must be ABSENT, not "0s", so old
	// helpers never see an unknown value.
	plan, err = PrepareHelperPlan(HelperPlanOptions{
		Paths:            PathsFor(root),
		SourceExecutable: source,
		UpdateID:         "cli-bundle-beta-linux-amd64-1.0.7",
		AdminURL:         "http://127.0.0.1:5001/",
		Token:            "token-123",
	})
	if err != nil {
		t.Fatalf("PrepareHelperPlan returned error: %v", err)
	}
	if slices.Contains(plan.Args, "--health-timeout") {
		t.Fatalf("zero health timeout must omit the flag: %#v", plan.Args)
	}
}
