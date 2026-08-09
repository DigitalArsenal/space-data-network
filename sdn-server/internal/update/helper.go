package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HelperPlanOptions struct {
	Paths            Paths
	SourceExecutable string
	UpdateID         string
	AdminURL         string
	Token            string
	RestartArgv      []string
	// HealthTimeout bounds the post-restart health wait. Zero keeps the
	// helper's 60s default — which fits an 18s-boot box like the VM but NOT a
	// store-heavy node whose boot replays the record catalog for minutes
	// (host-02 measured >5m on 2026-08-01; the 60s gate rolled back a healthy
	// update). "Takes as long as it takes" is an owner rule; the gate must be
	// able to say it too.
	HealthTimeout time.Duration
	// AllowRollback carries the operator's explicit acceptance of a declared
	// source-lineage rollback into the helper process, which re-verifies the
	// staged payload from scratch in its own process and would otherwise
	// refuse what `update install --allow-rollback` just accepted.
	AllowRollback bool
}

type HelperPlan struct {
	Executable string
	Args       []string
}

func PrepareHelperPlan(opts HelperPlanOptions) (*HelperPlan, error) {
	if strings.TrimSpace(opts.Paths.Root) == "" {
		return nil, errors.New("helper bundle root is required")
	}
	if strings.TrimSpace(opts.SourceExecutable) == "" {
		return nil, errors.New("helper source executable is required")
	}
	if strings.TrimSpace(opts.UpdateID) == "" {
		return nil, errors.New("helper update id is required")
	}
	sourceInfo, err := os.Stat(opts.SourceExecutable)
	if err != nil {
		return nil, fmt.Errorf("stat helper source executable: %w", err)
	}
	helperDir := filepath.Join(opts.Paths.Updates, "helper")
	if err := os.RemoveAll(helperDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		return nil, err
	}
	helperPath := filepath.Join(helperDir, filepath.Base(opts.SourceExecutable))
	if err := copyFile(opts.SourceExecutable, helperPath, sourceInfo.Mode().Perm()|0o700); err != nil {
		return nil, err
	}

	args := []string{
		"update",
		"helper-apply",
		"--bundle-root", opts.Paths.Root,
		"--update-id", opts.UpdateID,
	}
	if strings.TrimSpace(opts.AdminURL) != "" && strings.TrimSpace(opts.Token) != "" {
		args = append(args, "--admin-url", opts.AdminURL, "--token", opts.Token)
	}
	if opts.AllowRollback {
		args = append(args, "--allow-rollback")
	}
	if opts.HealthTimeout > 0 {
		args = append(args, "--health-timeout", opts.HealthTimeout.String())
	}
	if len(opts.RestartArgv) == 0 && (strings.TrimSpace(opts.AdminURL) == "" || strings.TrimSpace(opts.Token) == "") {
		args = append(args, "--no-restart")
	} else if len(opts.RestartArgv) > 0 {
		restartJSON, err := json.Marshal(opts.RestartArgv)
		if err != nil {
			return nil, err
		}
		args = append(args, "--restart-argv-json", string(restartJSON))
	}
	return &HelperPlan{Executable: helperPath, Args: args}, nil
}

func copyFile(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read helper source executable: %w", err)
	}
	if err := os.WriteFile(target, data, mode); err != nil {
		return fmt.Errorf("write helper executable: %w", err)
	}
	return nil
}
