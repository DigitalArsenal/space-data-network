package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
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
		fmt.Fprintf(out, "update_feed_base_url=%s\n", manifest.Update.FeedBaseURL)
		fmt.Fprintf(out, "update_pubsub_topic=%s\n", manifest.Update.PubsubTopic)
		fmt.Fprintf(out, "updater_module=%s\n", manifest.Update.UpdaterModule)
		fmt.Fprintf(out, "updater_wasm=%s\n", manifest.Update.UpdaterWASM)
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
		if providerCandidate, err := fetchProviderUpdateCandidate(&http.Client{Timeout: 20 * time.Second}, *manifest, currentUpdateSequence(), providerUpdateFilter{}); err != nil {
			fmt.Fprintf(out, "provider_update_check=error error=%q\n", err.Error())
		} else {
			fmt.Fprintf(out, "provider_update=%s status=available version=%s sequence=%d channel=%s manifest_url=%s carrier_url=%s\n",
				providerCandidate.UpdateID,
				providerCandidate.Version,
				providerCandidate.Sequence,
				providerCandidate.Channel,
				providerCandidate.ManifestURL,
				providerCandidate.CarrierURL)
			available = true
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

var (
	updateInstallID      string
	updateInstallVersion string
	updateInstallDryRun  bool
	updateInstallDirect  bool
)

var updateInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Fetch, verify, stage, and install the newest signed SDN CLI bundle update",
	Long: "Fetches the SDN-owned CLI bundle update feed, downloads the selected signed " +
		"manifest and inert update.wasm carrier, verifies the payload against the bundle " +
		"trust roots, stages it, and applies it to the current self-contained bundle.",
	RunE: func(cmd *cobra.Command, args []string) error {
		layout := bundle.ResolveCurrent()
		if layout.Root == "" {
			return errors.New("current executable is not running from a self-contained SDN bundle")
		}
		manifest, err := loadCurrentBundleManifest()
		if err != nil {
			return err
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
		candidate, err := fetchProviderUpdateCandidate(&http.Client{Timeout: 5 * time.Minute}, *manifest, state.Sequence, providerUpdateFilter{
			UpdateID: updateInstallID,
			Version:  updateInstallVersion,
		})
		if err != nil {
			return err
		}
		manifestBytes, err := readHTTPSURL(&http.Client{Timeout: 5 * time.Minute}, candidate.ManifestURL, 64<<20)
		if err != nil {
			return fmt.Errorf("read provider update manifest: %w", err)
		}
		wasmBytes, err := readHTTPSURL(&http.Client{Timeout: 5 * time.Minute}, candidate.CarrierURL, 2<<30)
		if err != nil {
			return fmt.Errorf("read provider update carrier: %w", err)
		}
		staged, err := update.Stage(paths, manifestBytes, wasmBytes, update.HostVerifyOptions(roots, state.Sequence, time.Now()))
		if err != nil {
			return err
		}
		if !updateInstallDryRun && !updateInstallDirect && shouldDelegateInstallToHelper() {
			token, err := generateUpdateControlToken()
			if err != nil {
				return err
			}
			if err := update.WriteControlToken(paths, token); err != nil {
				return err
			}
			helperPlan, err := prepareUpdateHelper(paths, staged.UpdateID, token)
			if err != nil {
				return err
			}
			helperCmd := exec.Command(helperPlan.Executable, helperPlan.Args...)
			helperCmd.Stdout = cmd.OutOrStdout()
			helperCmd.Stderr = cmd.ErrOrStderr()
			if err := helperCmd.Start(); err != nil {
				return fmt.Errorf("start update helper: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "staged_update=%s\n", staged.UpdateID)
			fmt.Fprintf(out, "helper_started=%s\n", helperPlan.Executable)
			fmt.Fprintf(out, "helper_pid=%d\n", helperCmd.Process.Pid)
			fmt.Fprintln(out, "next=the helper will apply the update after this command exits")
			return nil
		}
		result, err := update.Apply(paths, update.ApplyOptions{
			UpdateID: staged.UpdateID,
			DryRun:   updateInstallDryRun,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if result.DryRun {
			fmt.Fprintf(out, "would_install_update=%s version=%s sequence=%d channel=%s\n",
				result.UpdateID, result.Version, result.Sequence, result.Channel)
			return nil
		}
		fmt.Fprintf(out, "installed_update=%s\n", result.UpdateID)
		fmt.Fprintf(out, "version=%s\n", result.Version)
		fmt.Fprintf(out, "sequence=%d\n", result.Sequence)
		fmt.Fprintf(out, "rollback_path=%s\n", result.RollbackPath)
		fmt.Fprintln(out, "next=restart the SDN daemon to run the new version")
		return nil
	},
}

var (
	helperApplyBundleRoot      string
	helperApplyUpdateID        string
	helperApplyAdminURL        string
	helperApplyToken           string
	helperApplyNoRestart       bool
	helperApplyRestartArgvJSON string
)

var updateHelperApplyCmd = &cobra.Command{
	Use:    "helper-apply",
	Hidden: true,
	Short:  "Apply a staged update from a copied helper executable",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(helperApplyBundleRoot) == "" {
			return errors.New("--bundle-root is required")
		}
		if strings.TrimSpace(helperApplyUpdateID) == "" {
			return errors.New("--update-id is required")
		}
		var restartArgv []string
		if strings.TrimSpace(helperApplyRestartArgvJSON) != "" {
			if err := json.Unmarshal([]byte(helperApplyRestartArgvJSON), &restartArgv); err != nil {
				return fmt.Errorf("parse restart argv: %w", err)
			}
		}
		if strings.TrimSpace(helperApplyAdminURL) != "" && strings.TrimSpace(helperApplyToken) != "" {
			daemonArgv, err := requestDaemonUpdateShutdown(&http.Client{Timeout: 10 * time.Second}, helperApplyAdminURL, helperApplyBundleRoot, helperApplyToken)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "daemon_shutdown=unavailable error=%q\n", err.Error())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon_shutdown=requested")
				if len(restartArgv) == 0 {
					restartArgv = daemonArgv
				}
				time.Sleep(2 * time.Second)
			}
		}
		result, err := update.Apply(update.PathsFor(helperApplyBundleRoot), update.ApplyOptions{
			UpdateID: helperApplyUpdateID,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "applied_update=%s\n", result.UpdateID)
		fmt.Fprintf(out, "version=%s\n", result.Version)
		fmt.Fprintf(out, "sequence=%d\n", result.Sequence)
		fmt.Fprintf(out, "rollback_path=%s\n", result.RollbackPath)
		if helperApplyNoRestart || len(restartArgv) == 0 {
			fmt.Fprintln(out, "restart=manual")
			fmt.Fprintln(out, "next=restart the SDN daemon to run the new version")
			return nil
		}
		restartCmd := exec.Command(restartArgv[0], restartArgv[1:]...)
		restartCmd.Stdout = cmd.OutOrStdout()
		restartCmd.Stderr = cmd.ErrOrStderr()
		if err := restartCmd.Start(); err != nil {
			return fmt.Errorf("restart daemon: %w", err)
		}
		fmt.Fprintf(out, "restart=started pid=%d\n", restartCmd.Process.Pid)
		return nil
	},
}

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
	Schema    string               `json:"schema"`
	Version   string               `json:"version"`
	Channel   string               `json:"channel"`
	Signature string               `json:"signature"`
	Update    bundleUpdateMetadata `json:"update"`
}

type bundleUpdateMetadata struct {
	FeedBaseURL   string `json:"feedBaseUrl"`
	PubsubTopic   string `json:"pubsubTopic"`
	UpdaterModule string `json:"updaterModule"`
	UpdaterWASM   string `json:"updaterWasm"`
}

type providerUpdateFilter struct {
	UpdateID string
	Version  string
}

func init() {
	updateStageCmd.Flags().StringVar(&updateStageManifest, "manifest", "", "path or HTTPS URL of the signed update manifest.json")
	updateStageCmd.Flags().StringVar(&updateStageCarrier, "carrier", "", "path or HTTPS URL of the update.wasm carrier")
	updateInstallCmd.Flags().StringVar(&updateInstallID, "update-id", "", "provider update id to install (default: highest verified sequence)")
	updateInstallCmd.Flags().StringVar(&updateInstallVersion, "version", "", "provider update version to install")
	updateInstallCmd.Flags().BoolVar(&updateInstallDryRun, "dry-run", false, "verify and report without swapping files")
	updateInstallCmd.Flags().BoolVar(&updateInstallDirect, "direct", false, "apply in the current process instead of using the helper")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyBundleRoot, "bundle-root", "", "bundle root to update")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyUpdateID, "update-id", "", "staged update id to apply")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyAdminURL, "admin-url", "", "local daemon admin URL")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyToken, "token", "", "one-time daemon update control token")
	updateHelperApplyCmd.Flags().BoolVar(&helperApplyNoRestart, "no-restart", false, "do not restart the daemon after apply")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyRestartArgvJSON, "restart-argv-json", "", "JSON array argv to restart after apply")
	updateApplyCmd.Flags().StringVar(&updateApplyID, "update-id", "", "staged update id to apply (default: highest verified sequence)")
	updateApplyCmd.Flags().BoolVar(&updateApplyDryRun, "dry-run", false, "verify and report without swapping files")
	updateCmd.AddCommand(updateCheckCmd)
	updateCmd.AddCommand(updateStageCmd)
	updateCmd.AddCommand(updateInstallCmd)
	updateCmd.AddCommand(updateHelperApplyCmd)
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

func currentUpdateSequence() int64 {
	layout := bundle.ResolveCurrent()
	if layout.Root == "" {
		return 0
	}
	paths := update.PathsFor(layout.Root)
	state, err := update.LoadState(paths)
	if err != nil || state == nil {
		return 0
	}
	return state.Sequence
}

func providerFeedIndexURL(baseURL string, channel string, platform string, arch string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("SDN update feed base URL must use HTTPS")
	}
	for name, value := range map[string]string{
		"channel":  channel,
		"platform": platform,
		"arch":     arch,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, `/\`) {
			return "", fmt.Errorf("invalid update feed %s", name)
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/cli-bundle/" + channel + "/" + platform + "/" + arch + "/index.json"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func fetchProviderUpdateCandidate(client *http.Client, manifest bundleManifest, currentSequence int64, filter providerUpdateFilter) (*update.ProviderFeedUpdate, error) {
	indexURL, err := providerFeedIndexURL(manifest.Update.FeedBaseURL, manifest.Channel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	raw, err := readHTTPSURL(client, indexURL, 16<<20)
	if err != nil {
		return nil, fmt.Errorf("fetch update provider index: %w", err)
	}
	feed, err := update.ParseProviderFeed(raw)
	if err != nil {
		return nil, err
	}
	return feed.Select(update.ProviderFeedSelection{
		UpdateID:        strings.TrimSpace(filter.UpdateID),
		Version:         strings.TrimSpace(filter.Version),
		Channel:         manifest.Channel,
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		Kind:            "cli-bundle",
		CurrentSequence: currentSequence,
	})
}

func shouldDelegateInstallToHelper() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return false
	}
	return localDaemonAvailable(adminURL(cfg))
}

func localDaemonAvailable(rawAdminURL string) bool {
	infoURL, err := adminEndpointURL(rawAdminURL, "/api/node/info")
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get(infoURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func prepareUpdateHelper(paths update.Paths, updateID string, token string) (*update.HelperPlan, error) {
	source, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		source = resolved
	}
	cfg, _ := config.Load(configPath)
	return update.PrepareHelperPlan(update.HelperPlanOptions{
		Paths:            paths,
		SourceExecutable: source,
		UpdateID:         updateID,
		AdminURL:         adminURL(cfg),
		Token:            token,
		RestartArgv:      nil,
	})
}

func generateUpdateControlToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func requestDaemonUpdateShutdown(client *http.Client, rawAdminURL string, bundleRoot string, token string) ([]string, error) {
	shutdownURL, err := adminEndpointURL(rawAdminURL, "/api/v1/admin/update/shutdown")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{
		"token":      token,
		"bundleRoot": bundleRoot,
	})
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(shutdownURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("daemon update shutdown rejected: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		RestartArgv []string `json:"restartArgv"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.RestartArgv, nil
}

func adminEndpointURL(rawAdminURL string, endpointPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawAdminURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid admin URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpointPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func readHTTPSURL(client *http.Client, source string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("maximum update download size must be positive")
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("update provider URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Get(source)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", source, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("update provider response exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func readLocalOrHTTP(source string) ([]byte, error) {
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") {
		return readHTTPSURL(&http.Client{Timeout: 5 * time.Minute}, source, 2<<30)
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
	manifest.Update.FeedBaseURL = strings.TrimSpace(manifest.Update.FeedBaseURL)
	manifest.Update.PubsubTopic = strings.TrimSpace(manifest.Update.PubsubTopic)
	manifest.Update.UpdaterModule = strings.TrimSpace(manifest.Update.UpdaterModule)
	manifest.Update.UpdaterWASM = strings.TrimSpace(manifest.Update.UpdaterWASM)
	if manifest.Version == "" {
		return nil, errors.New("bundle manifest missing version")
	}
	if manifest.Channel == "" {
		return nil, errors.New("bundle manifest missing channel")
	}
	if manifest.Signature == "" {
		return nil, errors.New("bundle manifest missing signature")
	}
	if manifest.Update.FeedBaseURL == "" {
		return nil, errors.New("bundle manifest missing update feed base URL")
	}
	if manifest.Update.PubsubTopic == "" {
		return nil, errors.New("bundle manifest missing update pubsub topic")
	}
	if manifest.Update.UpdaterModule == "" {
		return nil, errors.New("bundle manifest missing updater module")
	}
	if manifest.Update.UpdaterWASM == "" {
		return nil, errors.New("bundle manifest missing updater wasm")
	}
	return &manifest, nil
}
