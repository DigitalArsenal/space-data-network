package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check, stage, and apply signed SDN bundle updates",
}

var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the bundled update manifest and staged updates",
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := loadCurrentBundleManifest()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "version=%s\n", manifest.Version)
		fmt.Fprintf(out, "channel=%s\n", manifest.Channel)
		fmt.Fprintln(out, "update_check_scope=bundled_manifest")

		staged, available := scanStagedForCheck()
		for _, candidate := range staged {
			if candidate.Err != nil {
				fmt.Fprintf(out, "staged_update=%s status=rejected error=%q\n", candidate.UpdateID, candidate.Err.Error())
				continue
			}
			fmt.Fprintf(out, "staged_update=%s status=verified version=%s sequence=%d channel=%s\n",
				candidate.UpdateID, candidate.Result.Version, candidate.Result.Sequence, candidate.Result.Channel)
		}
		fmt.Fprintf(out, "updates_available=%t\n", available)
		return nil
	},
}

var (
	updateStageManifest string
	updateStageCarrier  string
)

var updateStageCmd = &cobra.Command{
	Use:   "stage",
	Short: "Verify a signed update payload and stage it for apply",
	Long: "Downloads (or reads) a signed org.spacedatanetwork.update.v1 manifest and its " +
		"inert update.wasm carrier, verifies signature, target, expiration, sequence, and " +
		"hashes against the bundle trust store, and stages the payload under updates/staged/.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(updateStageManifest) == "" || strings.TrimSpace(updateStageCarrier) == "" {
			return errors.New("--manifest and --carrier are required")
		}
		layout := bundle.ResolveCurrent()
		if layout.Root == "" {
			return errors.New("current executable is not running from a self-contained SDN bundle")
		}
		paths := update.PathsFor(layout.Root)
		roots, err := update.LoadTrustRoots(paths)
		if err != nil {
			return err
		}
		state, err := update.LoadState(paths)
		if err != nil {
			return err
		}
		manifestBytes, err := readLocalOrHTTP(updateStageManifest)
		if err != nil {
			return fmt.Errorf("read update manifest: %w", err)
		}
		wasmBytes, err := readLocalOrHTTP(updateStageCarrier)
		if err != nil {
			return fmt.Errorf("read update carrier: %w", err)
		}
		staged, err := update.Stage(paths, manifestBytes, wasmBytes, update.HostVerifyOptions(roots, state.Sequence, time.Now()))
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "staged_update=%s\n", staged.UpdateID)
		fmt.Fprintf(out, "version=%s\n", staged.Result.Version)
		fmt.Fprintf(out, "sequence=%d\n", staged.Result.Sequence)
		fmt.Fprintf(out, "channel=%s\n", staged.Result.Channel)
		fmt.Fprintf(out, "staged_path=%s\n", staged.Dir)
		fmt.Fprintln(out, "next=run `spacedatanetwork update apply` to install")
		return nil
	},
}

var (
	updateApplyID     string
	updateApplyDryRun bool
)

var updateApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a staged signed SDN bundle update",
	Long: "Re-verifies a staged update payload, extracts the bundle, checks every artifact " +
		"checksum, and atomically swaps the bundle contents. The previous payload is kept " +
		"under updates/rollback/<update-id>/ and restored automatically if the swap fails. " +
		"Stop the SDN daemon before applying.",
	RunE: func(cmd *cobra.Command, args []string) error {
		layout := bundle.ResolveCurrent()
		if layout.Root == "" {
			return errors.New("current executable is not running from a self-contained SDN bundle")
		}
		paths := update.PathsFor(layout.Root)
		result, err := update.Apply(paths, update.ApplyOptions{
			UpdateID: strings.TrimSpace(updateApplyID),
			DryRun:   updateApplyDryRun,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if result.DryRun {
			fmt.Fprintf(out, "would_apply=%s version=%s sequence=%d channel=%s\n",
				result.UpdateID, result.Version, result.Sequence, result.Channel)
			return nil
		}
		fmt.Fprintf(out, "applied_update=%s\n", result.UpdateID)
		fmt.Fprintf(out, "version=%s\n", result.Version)
		fmt.Fprintf(out, "sequence=%d\n", result.Sequence)
		fmt.Fprintf(out, "rollback_path=%s\n", result.RollbackPath)
		fmt.Fprintln(out, "next=restart the SDN daemon to run the new version")
		return nil
	},
}

type bundleManifest struct {
	Schema    string `json:"schema"`
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	Signature string `json:"signature"`
}

func init() {
	updateStageCmd.Flags().StringVar(&updateStageManifest, "manifest", "", "path or HTTPS URL of the signed update manifest.json")
	updateStageCmd.Flags().StringVar(&updateStageCarrier, "carrier", "", "path or HTTPS URL of the update.wasm carrier")
	updateApplyCmd.Flags().StringVar(&updateApplyID, "update-id", "", "staged update id to apply (default: highest verified sequence)")
	updateApplyCmd.Flags().BoolVar(&updateApplyDryRun, "dry-run", false, "verify and report without swapping files")
	updateCmd.AddCommand(updateCheckCmd)
	updateCmd.AddCommand(updateStageCmd)
	updateCmd.AddCommand(updateApplyCmd)
	rootCmd.AddCommand(updateCmd)
}

// scanStagedForCheck reports staged updates for `update check`. Missing
// trust roots or state are reported as rejection reasons rather than
// failing the whole check.
func scanStagedForCheck() ([]update.StagedUpdate, bool) {
	layout := bundle.ResolveCurrent()
	if layout.Root == "" {
		return nil, false
	}
	paths := update.PathsFor(layout.Root)
	roots, err := update.LoadTrustRoots(paths)
	if err != nil {
		roots = update.TrustedRoots{}
	}
	state, err := update.LoadState(paths)
	if err != nil {
		state = &update.State{}
	}
	staged, err := update.ScanStaged(paths, update.HostVerifyOptions(roots, state.Sequence, time.Now()))
	if err != nil {
		return nil, false
	}
	available := false
	for _, candidate := range staged {
		if candidate.Err == nil {
			available = true
		}
	}
	return staged, available
}

func readLocalOrHTTP(source string) ([]byte, error) {
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") {
		if parsed.Scheme == "http" {
			return nil, errors.New("update payloads must be fetched over HTTPS")
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: %s", source, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 2<<30))
	}
	return os.ReadFile(source)
}

func loadCurrentBundleManifest() (*bundleManifest, error) {
	layout := bundle.ResolveCurrent()
	if layout.ManifestPath == "" {
		return nil, errors.New("current executable is not running from a self-contained SDN bundle")
	}
	return loadBundleManifest(layout.ManifestPath)
}

func loadBundleManifest(path string) (*bundleManifest, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest bundleManifest
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "org.spacedatanetwork.bundle.v1" {
		return nil, fmt.Errorf("unsupported bundle manifest schema: %s", manifest.Schema)
	}
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Channel = strings.TrimSpace(manifest.Channel)
	manifest.Signature = strings.TrimSpace(manifest.Signature)
	if manifest.Version == "" {
		return nil, errors.New("bundle manifest missing version")
	}
	if manifest.Channel == "" {
		return nil, errors.New("bundle manifest missing channel")
	}
	if manifest.Signature == "" {
		return nil, errors.New("bundle manifest missing signature")
	}
	return &manifest, nil
}
