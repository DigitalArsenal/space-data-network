package main

import (
	"bytes"
	"context"
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
	// updateStageAllowRollback mirrors --allow-rollback on `update install`
	// for the manual staging path, so the gate cannot be sidestepped by
	// staging first and applying second.
	updateStageAllowRollback bool
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
		stageOpts := update.HostVerifyOptions(roots, state.Sequence, time.Now())
		stageOpts.AllowRollback = updateStageAllowRollback
		staged, err := update.Stage(paths, manifestBytes, wasmBytes, stageOpts)
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
	// updateInstallAllowRollback accepts a manifest the PUBLISHER marked as a
	// deliberate rollback (provenance.lineage=rollback). Without it, such a
	// manifest is refused: a rollback and an accidental regression are
	// byte-identical, and only an operator can tell them apart.
	updateInstallAllowRollback bool
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
		// The index entry we selected on and the signed manifest we resolved to
		// must describe the same artifact. Stage() then verifies the artifact
		// itself against the manifest's signature, hashes and size.
		if parsed, err := update.ParseManifest(manifestBytes); err != nil {
			return err
		} else if err := candidate.AssertMatchesPayload(parsed, len(wasmBytes)); err != nil {
			return err
		}
		installOpts := update.HostVerifyOptions(roots, state.Sequence, time.Now())
		installOpts.AllowRollback = updateInstallAllowRollback
		staged, err := update.Stage(paths, manifestBytes, wasmBytes, installOpts)
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
			UpdateID:      staged.UpdateID,
			DryRun:        updateInstallDryRun,
			AllowRollback: updateInstallAllowRollback,
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
	helperApplyHealthTimeout   time.Duration
	helperApplyAllowRollback   bool
	updateInstallHealthTimeout time.Duration
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
		// Resolved ONCE, here, while the daemon is still up — see
		// daemonLoopbackTransport for why this cannot be deferred to the health
		// gate below.
		loopback := daemonLoopbackTransport()
		var daemonSupervised bool
		if strings.TrimSpace(helperApplyAdminURL) != "" && strings.TrimSpace(helperApplyToken) != "" {
			daemonArgv, supervised, err := requestDaemonUpdateShutdown(&http.Client{Timeout: 10 * time.Second, Transport: loopback}, helperApplyAdminURL, helperApplyBundleRoot, helperApplyToken)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "daemon_shutdown=unavailable error=%q\n", err.Error())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon_shutdown=requested")
				if len(restartArgv) == 0 {
					restartArgv = daemonArgv
				}
				daemonSupervised = supervised
				time.Sleep(2 * time.Second)
			}
		}
		result, err := update.Apply(update.PathsFor(helperApplyBundleRoot), update.ApplyOptions{
			UpdateID:      helperApplyUpdateID,
			AllowRollback: helperApplyAllowRollback,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "applied_update=%s\n", result.UpdateID)
		fmt.Fprintf(out, "version=%s\n", result.Version)
		fmt.Fprintf(out, "sequence=%d\n", result.Sequence)
		fmt.Fprintf(out, "rollback_path=%s\n", result.RollbackPath)
		return helperPostApplyRestart(cmd.Context(), helperPostApplyOptions{
			Paths:       update.PathsFor(helperApplyBundleRoot),
			RestartArgv: restartArgv,
			Supervised:  daemonSupervised,
			AdminURL:    helperApplyAdminURL,
			NoRestart:   helperApplyNoRestart,
			// The SAME anchored transport, captured above while the daemon was
			// still running: a probe that cannot verify the daemon's own
			// certificate reports "unhealthy" for a perfectly healthy daemon
			// and rolls a good update back.
			Client:        &http.Client{Timeout: 5 * time.Second, Transport: loopback},
			Out:           out,
			Err:           cmd.ErrOrStderr(),
			HealthTimeout: helperApplyHealthTimeout,
		})
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
	updateStageCmd.Flags().BoolVar(&updateStageAllowRollback, "allow-rollback", false, "accept an update the publisher marked as a deliberate source-lineage rollback")
	updateInstallCmd.Flags().StringVar(&updateInstallID, "update-id", "", "provider update id to install (default: highest verified sequence)")
	updateInstallCmd.Flags().StringVar(&updateInstallVersion, "version", "", "provider update version to install")
	updateInstallCmd.Flags().BoolVar(&updateInstallDryRun, "dry-run", false, "verify and report without swapping files")
	updateInstallCmd.Flags().BoolVar(&updateInstallDirect, "direct", false, "apply in the current process instead of using the helper")
	updateInstallCmd.Flags().BoolVar(&updateInstallAllowRollback, "allow-rollback", false, "accept an update the publisher marked as a deliberate source-lineage rollback")
	updateInstallCmd.Flags().DurationVar(&updateInstallHealthTimeout, "health-timeout", 0, "how long the helper waits for daemon health after restart (0 = 60s default; store-heavy nodes whose boot replays the catalog need minutes)")
	updateHelperApplyCmd.Flags().DurationVar(&helperApplyHealthTimeout, "health-timeout", 0, "post-restart daemon health wait (0 = 60s default)")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyBundleRoot, "bundle-root", "", "bundle root to update")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyUpdateID, "update-id", "", "staged update id to apply")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyAdminURL, "admin-url", "", "local daemon admin URL")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyToken, "token", "", "one-time daemon update control token")
	updateHelperApplyCmd.Flags().BoolVar(&helperApplyNoRestart, "no-restart", false, "do not restart the daemon after apply")
	updateHelperApplyCmd.Flags().BoolVar(&helperApplyAllowRollback, "allow-rollback", false, "accept an update the publisher marked as a deliberate source-lineage rollback")
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
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return false
	}
	return localDaemonAvailable(adminURL(cfg))
}

// daemonLoopbackHTTPClient reaches THIS box's own daemon with the daemon's own
// certificate as the trust anchor.
//
// THIS IS THE DEFECT THAT MADE UNATTENDED SELF-UPGRADE IMPOSSIBLE. The helper
// path used a bare http.Client, i.e. system roots. host-01's admin listener
// binds 0.0.0.0:443 and serves an ORIGIN certificate for sdn.spaceaware.io: no
// system root vouches for it, and it carries no 127.0.0.1 SAN, so a loopback
// dial fails twice over — once on the anchor, once on the name. Measured
// 2026-08-09: `curl https://127.0.0.1/api/v1/data/health` on host-01 returns
// "SSL certificate problem: self-signed certificate", while the same request
// with -k returns 200.
//
// The consequences were silent and exactly wrong. localDaemonAvailable() saw
// the failure and concluded NO DAEMON IS RUNNING, so `update install` skipped
// the helper entirely and applied in-process — no shutdown handshake, no
// post-restart health gate, no automatic rollback. The daemon then had to be
// restarted by hand, which is why every host-01 roll in the record reads
// "install-while-up ... then systemctl restart --no-block". The lane reported
// success; a human finished the job.
//
// adminClient has solved this since 2026-07-28 (daemonTLSConfig +
// serverNameForCert): anchor to the certificate the daemon's own config
// declares, and present a name that certificate covers. The dial still goes to
// loopback and verification still happens — only the anchor changes.
// InsecureSkipVerify is never set here.
func daemonLoopbackHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: daemonLoopbackTransport()}
}

// daemonLoopbackTransport builds the anchored transport, and MUST be called
// while the daemon is still running.
//
// config.LoadResolved("") prefers the RUNNING DAEMON tier — it reads the config
// path off the live process's command line — and only that tier (or a system
// location) satisfies Resolution.IsOwnDaemonConfig, which is the precondition
// for trusting the certificate that config declares. An explicit -c path
// deliberately does NOT qualify, because a config file can point anywhere and
// must not get to choose the CLI's trust anchor.
//
// The consequence for the helper is a sequencing rule, not a flag: once it has
// asked the daemon to shut down there is no running process to resolve from, so
// a transport built at that point may silently fall back to system roots and
// then report a perfectly healthy daemon as unhealthy — rolling a good update
// back. Build it once, up front, and reuse it for both the shutdown handshake
// and the post-restart health gate.
func daemonLoopbackTransport() *http.Transport {
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil || cfg == nil {
		return nil
	}
	tlsCfg, certPath, err := daemonTLSConfig(cfg, res)
	if err != nil || tlsCfg == nil {
		return nil
	}
	base := strings.TrimRight(adminURL(cfg), "/")
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if name := serverNameForCert(certPath, ExpectedCertHostFor(host)); name != "" {
		tlsCfg.ServerName = name
	}
	return &http.Transport{TLSClientConfig: tlsCfg}
}

func localDaemonAvailable(rawAdminURL string) bool {
	infoURL, err := adminEndpointURL(rawAdminURL, "/api/node/info")
	if err != nil {
		return false
	}
	client := daemonLoopbackHTTPClient(750 * time.Millisecond)
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
	cfg, _ := mustLoadResolved(configPath)
	return update.PrepareHelperPlan(update.HelperPlanOptions{
		Paths:            paths,
		SourceExecutable: source,
		UpdateID:         updateID,
		AdminURL:         adminURL(cfg),
		Token:            token,
		RestartArgv:      nil,
		HealthTimeout:    updateInstallHealthTimeout,
		AllowRollback:    updateInstallAllowRollback,
	})
}

type helperStartedProcess interface {
	PID() int
	Kill() error
}

type helperExecProcess struct {
	cmd *exec.Cmd
}

func (p helperExecProcess) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p helperExecProcess) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

type helperPostApplyOptions struct {
	Paths       update.Paths
	RestartArgv []string
	// Supervised, when true, means the DAEMON we are restarting reported it
	// was started by systemd (INVOCATION_ID in its own environment — see
	// requestDaemonUpdateShutdown). Direct-spawning RestartArgv in that case
	// leaves a live, unsupervised replacement outside the unit's cgroup
	// while the unit's own Restart= policy loops "activating" against the
	// store's single-writer lock (six occurrences on lane boxes, graph task
	// sdn-update-helper-supervisor-mode). When Supervised is true this
	// function never calls StartDaemon: the daemon's own shutdown already
	// exits the supervised process, the supervisor's Restart= policy brings
	// it back under the SAME unit, and this only health-waits for that.
	Supervised    bool
	AdminURL      string
	NoRestart     bool
	Out           io.Writer
	Err           io.Writer
	Client        *http.Client
	HealthTimeout time.Duration
	StartDaemon   func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error)
	WaitHealth    func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error
	Rollback      func(paths update.Paths) (*update.RollbackResult, error)
}

func helperPostApplyRestart(ctx context.Context, opts helperPostApplyOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	errOut := opts.Err
	if errOut == nil {
		errOut = io.Discard
	}
	if opts.NoRestart || len(opts.RestartArgv) == 0 {
		fmt.Fprintln(out, "restart=manual")
		fmt.Fprintln(out, "next=restart the SDN daemon to run the new version")
		return nil
	}

	start := opts.StartDaemon
	if start == nil {
		start = startHelperDaemonProcess
	}
	waitHealth := opts.WaitHealth
	if waitHealth == nil {
		waitHealth = waitForDaemonHealth
	}
	rollback := opts.Rollback
	if rollback == nil {
		rollback = func(paths update.Paths) (*update.RollbackResult, error) {
			return update.RollbackLast(paths, update.RollbackOptions{Reason: "daemon health failed after update"})
		}
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	timeout := opts.HealthTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	if opts.Supervised {
		return helperPostApplyRestartSupervised(ctx, opts, waitHealth, rollback, client, out, errOut, timeout)
	}

	process, err := start(opts.RestartArgv, out, errOut)
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}
	fmt.Fprintf(out, "restart=started pid=%d\n", process.PID())
	if strings.TrimSpace(opts.AdminURL) == "" {
		return nil
	}
	if err := waitHealth(ctx, client, opts.AdminURL, timeout); err == nil {
		fmt.Fprintln(out, "daemon_health=healthy")
		return nil
	} else {
		fmt.Fprintf(errOut, "daemon_health=unhealthy error=%q\n", err.Error())
		if killErr := process.Kill(); killErr != nil {
			fmt.Fprintf(errOut, "failed_daemon_stop=error error=%q\n", killErr.Error())
		} else {
			fmt.Fprintln(errOut, "failed_daemon=stopped")
		}
		rollbackResult, rollbackErr := rollback(opts.Paths)
		if rollbackErr != nil {
			return fmt.Errorf("daemon health failed after update and rollback failed: %v: %w", err, rollbackErr)
		}
		fmt.Fprintf(out, "rollback=applied restored_version=%s failed_path=%s\n",
			rollbackResult.RestoredVersion, rollbackResult.FailedPath)
		restoredProcess, restartErr := start(opts.RestartArgv, out, errOut)
		if restartErr != nil {
			return fmt.Errorf("daemon health failed after update; rolled back to %s but restart failed: %w",
				rollbackResult.RestoredVersion, restartErr)
		}
		fmt.Fprintf(out, "restart=started pid=%d\n", restoredProcess.PID())
		if restoredHealthErr := waitHealth(ctx, client, opts.AdminURL, timeout); restoredHealthErr != nil {
			return fmt.Errorf("daemon health failed after update; rolled back to %s but restored daemon is unhealthy: %w",
				rollbackResult.RestoredVersion, restoredHealthErr)
		}
		fmt.Fprintln(out, "daemon_health=healthy")
		return fmt.Errorf("daemon health failed after update; rolled back to %s", rollbackResult.RestoredVersion)
	}
}

// helperPostApplyRestartSupervised is the opts.Supervised branch of
// helperPostApplyRestart: it NEVER calls StartDaemon. The daemon we asked to
// shut down (requestDaemonUpdateShutdown) was itself started by systemd, so
// its own exit is what the unit's Restart= policy is watching for — the
// supervisor brings it back under the SAME unit/cgroup on its own. All this
// does is wait for that to happen, and if it does not, roll back the bundle
// and wait again (the supervisor keeps retrying on whatever binary is on
// disk, so a rollback is enough — no second process to spawn here either).
func helperPostApplyRestartSupervised(
	ctx context.Context,
	opts helperPostApplyOptions,
	waitHealth func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error,
	rollback func(paths update.Paths) (*update.RollbackResult, error),
	client *http.Client,
	out, errOut io.Writer,
	timeout time.Duration,
) error {
	fmt.Fprintln(out, "restart=supervised")
	fmt.Fprintln(out, "next=the supervising init restarts the daemon under its own unit; not direct-spawning")
	if strings.TrimSpace(opts.AdminURL) == "" {
		return nil
	}
	if err := waitHealth(ctx, client, opts.AdminURL, timeout); err == nil {
		fmt.Fprintln(out, "daemon_health=healthy")
		return nil
	} else {
		fmt.Fprintf(errOut, "daemon_health=unhealthy error=%q\n", err.Error())
		rollbackResult, rollbackErr := rollback(opts.Paths)
		if rollbackErr != nil {
			return fmt.Errorf("daemon health failed after update and rollback failed: %v: %w", err, rollbackErr)
		}
		fmt.Fprintf(out, "rollback=applied restored_version=%s failed_path=%s\n",
			rollbackResult.RestoredVersion, rollbackResult.FailedPath)
		if restoredHealthErr := waitHealth(ctx, client, opts.AdminURL, timeout); restoredHealthErr != nil {
			return fmt.Errorf("daemon health failed after update; rolled back to %s but supervised restart is still unhealthy: %w",
				rollbackResult.RestoredVersion, restoredHealthErr)
		}
		fmt.Fprintln(out, "daemon_health=healthy")
		return fmt.Errorf("daemon health failed after update; rolled back to %s", rollbackResult.RestoredVersion)
	}
}

func startHelperDaemonProcess(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("restart argv is empty")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return helperExecProcess{cmd: cmd}, nil
}

func waitForDaemonHealth(ctx context.Context, client *http.Client, rawAdminURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = probeDaemonHealth(ctx, client, rawAdminURL)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func probeDaemonHealth(ctx context.Context, client *http.Client, rawAdminURL string) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	healthURL, err := adminEndpointURL(rawAdminURL, "/api/v1/data/health")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("daemon health rejected: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var health daemonHealthPayload
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return fmt.Errorf("decode daemon health: %w", err)
	}
	if !health.ok() {
		return errors.New("daemon reported unhealthy")
	}
	return nil
}

func generateUpdateControlToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// requestDaemonUpdateShutdown asks the live daemon to shut down for an
// update-apply and reports its restart argv plus whether IT was supervised
// by systemd (INVOCATION_ID set in its own environment — the daemon is the
// only party that can answer this reliably; the helper's own environment
// says nothing about how the daemon it is restarting was started).
func requestDaemonUpdateShutdown(client *http.Client, rawAdminURL string, bundleRoot string, token string) (restartArgv []string, supervised bool, err error) {
	shutdownURL, err := adminEndpointURL(rawAdminURL, "/api/v1/admin/update/shutdown")
	if err != nil {
		return nil, false, err
	}
	body, err := json.Marshal(map[string]string{
		"token":      token,
		"bundleRoot": bundleRoot,
	})
	if err != nil {
		return nil, false, err
	}
	resp, err := client.Post(shutdownURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, false, fmt.Errorf("daemon update shutdown rejected: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		RestartArgv []string `json:"restartArgv"`
		Supervised  bool     `json:"supervised"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, err
	}
	return result.RestartArgv, result.Supervised, nil
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
