package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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

		// WHETHER THIS BOX IS LISTENING. "Why did that box not upgrade?" is the
		// first question a push lane creates, and until now `update check` could
		// not answer it: an install with the signal lane switched off looks
		// exactly like a publisher that never signalled. Report the lane's
		// posture and the reverse targets beside the feed facts, so one command
		// answers both halves.
		layout := bundle.ResolveCurrent()
		if cfg, _, err := config.LoadResolved(configPath); err == nil && cfg != nil {
			topic := strings.TrimSpace(cfg.Update.Topic)
			if topic == "" {
				topic = strings.TrimSpace(manifest.Update.PubsubTopic)
			}
			if topic == "" {
				topic = update.SignalTopic(manifest.Channel)
			}
			fmt.Fprintf(out, "update_signal_enabled=%t\n", cfg.Update.Enabled)
			fmt.Fprintf(out, "update_signal_topic=%s\n", topic)
			fmt.Fprintf(out, "update_health_timeout=%s\n", cfg.Update.HealthTimeout())
		}
		if layout.Root != "" {
			if inventory, err := update.Inventory(update.PathsFor(layout.Root)); err == nil {
				fmt.Fprintf(out, "rollback_slots=%d/%d\n", len(inventory.Slots), inventory.Limit)
				for _, slot := range inventory.Missing {
					fmt.Fprintf(out, "rollback_slot_missing=%s\n", slot.UpdateID)
				}
			}
		}

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
			helperPlan, err := prepareUpdateHelper(paths, staged.UpdateID, token, resolveUpdateStoreRoot(updateInstallStoreRoot, cmd.ErrOrStderr()))
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
		storeRoot := resolveUpdateStoreRoot(updateInstallStoreRoot, cmd.ErrOrStderr())
		result, err := applyUnlessDaemonRuns(storeRoot, updateInstallDryRun, func() (*update.ApplyResult, error) {
			return update.Apply(paths, update.ApplyOptions{
				UpdateID:      staged.UpdateID,
				DryRun:        updateInstallDryRun,
				AllowRollback: updateInstallAllowRollback,
				StoreRoot:     storeRoot,
			})
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
	helperApplyTrigger         string
	helperApplyAdminCA         string
	helperApplySignalKeyID     string
	helperApplyAllowRollback   bool
	helperApplyStoreRoot       string
	updateInstallHealthTimeout time.Duration
	updateInstallStoreRoot     string
	updateApplyStoreRoot       string
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
		// gate.
		loopback := helperLoopbackTransport(helperApplyAdminCA, helperApplyAdminURL)
		return runHelperApply(cmd.Context(), helperApplyOptions{
			Paths:         update.PathsFor(helperApplyBundleRoot),
			UpdateID:      strings.TrimSpace(helperApplyUpdateID),
			AdminURL:      strings.TrimSpace(helperApplyAdminURL),
			Token:         strings.TrimSpace(helperApplyToken),
			StoreRoot:     resolveUpdateStoreRoot(helperApplyStoreRoot, cmd.ErrOrStderr()),
			RestartArgv:   restartArgv,
			NoRestart:     helperApplyNoRestart,
			AllowRollback: helperApplyAllowRollback,
			Trigger:       helperApplyTrigger,
			SignalKeyID:   helperApplySignalKeyID,
			HealthTimeout: helperApplyHealthTimeout,
			// The SAME anchored transport for the shutdown handshake and the
			// health gate: a probe that cannot verify the daemon's own
			// certificate reports "unhealthy" for a perfectly healthy daemon
			// and rolls a good update back.
			Client: daemonLoopbackClientWith(10*time.Second, loopback),
			Out:    cmd.OutOrStdout(),
			Err:    cmd.ErrOrStderr(),
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
		storeRoot := resolveUpdateStoreRoot(updateApplyStoreRoot, cmd.ErrOrStderr())
		apply := func() (*update.ApplyResult, error) {
			return update.Apply(paths, update.ApplyOptions{
				UpdateID:  strings.TrimSpace(updateApplyID),
				DryRun:    updateApplyDryRun,
				StoreRoot: storeRoot,
			})
		}
		result, err := applyUnlessDaemonRuns(storeRoot, updateApplyDryRun, apply)
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
	updateHelperApplyCmd.Flags().StringVar(&helperApplyAdminCA, "admin-ca", "",
		"path of the certificate the daemon serves, handed over by the daemon itself; used as the TLS anchor for the loopback shutdown handshake and health gate")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyTrigger, "trigger", "", "what caused this apply (\"signal\" for a pushed update signal); recorded in the deploy ledger")
	updateHelperApplyCmd.Flags().StringVar(&helperApplySignalKeyID, "signal-key-id", "", "signing key of the signal that triggered this apply; recorded in the deploy ledger")
	updateApplyCmd.Flags().StringVar(&updateApplyID, "update-id", "", "staged update id to apply (default: highest verified sequence)")
	updateApplyCmd.Flags().BoolVar(&updateApplyDryRun, "dry-run", false, "verify and report without swapping files")
	updateApplyCmd.Flags().StringVar(&updateApplyStoreRoot, "store-root", "", storeRootFlagUsage)
	updateInstallCmd.Flags().StringVar(&updateInstallStoreRoot, "store-root", "", storeRootFlagUsage)
	updateHelperApplyCmd.Flags().StringVar(&helperApplyStoreRoot, "store-root", "", storeRootFlagUsage)
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
	return daemonLoopbackClientWith(timeout, daemonLoopbackTransport())
}

// daemonLoopbackClientWith builds the client, and exists ONLY to keep a nil
// transport out of http.Client.Transport.
//
// CAUGHT LIVE, 2026-08-09, by the first signal-driven self-upgrade. Transport is
// an INTERFACE field; assigning a typed nil (*http.Transport)(nil) to it yields
// a non-nil interface holding a nil pointer, so net/http skips its own nil check
// and dereferences it. The helper panicked in requestDaemonUpdateShutdown before
// it asked the daemon to stop:
//
//	net/http.(*Transport).alternateRoundTripper(0x0)
//
// The lane failed SAFE — the crash preceded the shutdown request and the swap,
// so the daemon stayed up and healthy and the box was untouched — but it failed.
// A nil transport must mean "use the default", which is what an absent field
// means and what the code did before the transport was hoisted out of the
// client constructor.
func daemonLoopbackClientWith(timeout time.Duration, transport *http.Transport) *http.Client {
	client := &http.Client{Timeout: timeout}
	if transport != nil {
		client.Transport = transport
	}
	return client
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
// helperLoopbackTransport builds the helper's anchored transport, preferring the
// certificate path THE DAEMON HANDED IT over re-deriving one from config.
//
// FOUND LIVE, 2026-08-09, on the second signal-driven self-upgrade. Inside the
// transient systemd unit the config resolver did not reach the running-daemon
// tier at all: systemd-run inherits the MANAGER's environment, and this box
// carries SDN_CONFIG=/etc/space-data-network/config.yaml there — a different
// config from the sidecar's, resolved through the SDN_CONFIG tier, which
// deliberately does NOT satisfy IsOwnDaemonConfig (a config file must not get to
// choose the client's trust anchor). So no anchor was found, the handshake went
// out on system roots, and it failed:
//
//	daemon_shutdown=unavailable ... x509: certificate signed by unknown authority
//
// The swap still happened and the daemon was never stopped, so the box sat with
// new bytes on disk and the old process serving, waiting for a human — the
// precise outcome this lane exists to abolish.
//
// Re-deriving the anchor was the wrong shape. The daemon KNOWS which certificate
// it serves; it is reading it off its own live config to serve TLS. Handing that
// path to the helper it is spawning removes an ambient-environment dependency
// from the middle of a deploy, and it is strictly stronger than re-derivation:
// the anchor now comes from the running daemon's own configuration rather than
// from whatever config file the helper's environment happens to point at.
func helperLoopbackTransport(certPath, adminURL string) *http.Transport {
	certPath = strings.TrimSpace(certPath)
	if certPath == "" {
		return daemonLoopbackTransport()
	}
	pem, err := os.ReadFile(certPath)
	if err != nil {
		return daemonLoopbackTransport()
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return daemonLoopbackTransport()
	}
	tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(strings.TrimSpace(adminURL), "/"), "https://"), "http://")
	if name := serverNameForCert(certPath, ExpectedCertHostFor(host)); name != "" {
		tlsCfg.ServerName = name
	}
	return &http.Transport{TLSClientConfig: tlsCfg}
}

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

func prepareUpdateHelper(paths update.Paths, updateID string, token string, storeRoot string) (*update.HelperPlan, error) {
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
		StoreRoot:        storeRoot,
	})
}

const storeRootFlagUsage = "the record store the daemon opens (default: storage.path of the resolved config); " +
	"the store-format guard refuses a build whose binary does not open its on-disk format"

// resolveUpdateStoreRoot names the record store the store-format guard checks:
// the explicit flag, else storage.path of the config the CLI resolves (the
// running daemon's, when one is up). When neither is available the guard
// cannot check anything, and says so on errOut rather than refusing: an
// operator's forward update must not fail because a config was unreadable.
func resolveUpdateStoreRoot(explicit string, errOut io.Writer) string {
	if root := strings.TrimSpace(explicit); root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			return abs
		}
		return root
	}
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Storage.Path) == "" {
		reason := "the resolved config names no storage.path"
		if err != nil {
			reason = err.Error()
		}
		if errOut != nil {
			fmt.Fprintf(errOut, "store_format_guard=unchecked reason=%q config=%q\n", reason, res.Path)
		}
		return ""
	}
	root := strings.TrimSpace(cfg.Storage.Path)
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// THE SWAP, AS THE HELPER RUNS IT (expert review PLAT-01 / SD-5).
//
// The helper used to ask the daemon to shut down, sleep two seconds, extract
// the bundle and rename it into place, then accept the first "status ok" it
// heard. Every step of that raced something: the extraction ran while the node
// was down; the sleep raced a shutdown that 15 of 22 times took 90 s; the
// renames raced a supervisor respawning the old binary; and the health gate
// could not tell the build it had installed from the one it had replaced. On
// an unhealthy result under a supervisor it renamed the old files back and
// reported "rolled back" while the bad build — possibly mid-migration — went
// on running. The sequence is now:
//
//  1. guard and PREPARE while the daemon still serves: verify the staged
//     payload, run the store-format guard, extract into updates/incoming/ and
//     validate it (update.Prepare). Nothing has stopped yet.
//  2. STOP: the update shutdown handshake returns the daemon's pid, and the
//     helper waits for that pid to exit — bounded, then escalated through the
//     supervisor connector (or SIGTERM), then SIGKILL (update.StopLadder).
//  3. LOCK: take the store's single-writer lock, so no daemon — the old one, or
//     one a supervisor respawned — can have the store open during the swap.
//  4. SWAP: renames only (update.ApplyPrepared, which re-runs the guard now
//     that nothing can write the store). Then release the lock.
//  5. RESTART and GATE: the daemon must be healthy AND report, on /api/v1/id,
//     the bundle version and executable sha256 of the bundle now on disk.
//  6. REVERT on a failed gate: refuse up front if the store-format guard
//     already refuses the previous slot (alert, leave the current build
//     running); else stop the failed build (through the supervisor when there
//     is one, so its restart policy cannot respawn it mid-swap), lock, roll
//     back (the guard runs again inside), and restart the restored build
//     through the supervisor. A revert the guard refuses after the stop
//     restarts the current build and alerts: it never half-reverts.
//
// Every step is a deploy-ledger line (internal/update/deployledger.go).

// helperApplyOptions is everything `update helper-apply` acts on, after flag
// parsing.
type helperApplyOptions struct {
	Paths         update.Paths
	UpdateID      string
	AdminURL      string
	Token         string
	StoreRoot     string
	RestartArgv   []string
	NoRestart     bool
	AllowRollback bool
	Trigger       string
	SignalKeyID   string
	HealthTimeout time.Duration
	Stop          update.StopBounds
	// Client reaches the daemon over loopback for the shutdown handshake and
	// the health gate. Build it while the daemon is still up: see
	// daemonLoopbackTransport.
	Client *http.Client
	Out    io.Writer
	Err    io.Writer
	// Supervisor returns the connector to the supervisor that runs the daemon
	// at pid, or nil when none is proven. Nil uses hostsvc (systemd).
	Supervisor func(ctx context.Context, pid int) daemonSupervisor
}

// daemonSupervisor is the helper's connector to the supervisor that runs the
// daemon: systemd through hostsvc. A Stop sticks — the unit's Restart= policy
// does not undo it — until Start.
type daemonSupervisor interface {
	// Name says which supervisor and unit, for the ledger and the output.
	Name() string
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
	// MainPID is the daemon process the supervisor runs now; 0 for none.
	MainPID(ctx context.Context) int
}

type hostsvcSupervisor struct{ state hostsvc.State }

func (s hostsvcSupervisor) Name() string { return "systemd unit " + s.state.Unit }

func (s hostsvcSupervisor) Stop(ctx context.Context) error {
	return hostsvc.Control(ctx, s.state, hostsvc.ActionStop)
}

func (s hostsvcSupervisor) Start(ctx context.Context) error {
	return hostsvc.Control(ctx, s.state, hostsvc.ActionRestart)
}

func (s hostsvcSupervisor) MainPID(ctx context.Context) int {
	return hostsvc.Refresh(ctx, s.state).MainPID
}

// hostsvcSupervisorFor proves the systemd unit that runs pid (hostsvc.ProbePID)
// or reports none.
func hostsvcSupervisorFor(ctx context.Context, pid int) daemonSupervisor {
	state := hostsvc.ProbePID(ctx, pid)
	if !state.Detected {
		return nil
	}
	return hostsvcSupervisor{state: state}
}

// servingDaemon is the daemon the helper stopped for the swap.
type servingDaemon struct {
	PID         int
	RestartArgv []string
	Supervised  bool
	// Supervisor is the proven connector, nil when there is none.
	Supervisor daemonSupervisor
	// StoppedBySupervisor is true when the stop went through the connector:
	// it sticks, so the unit must be started again through it.
	StoppedBySupervisor bool
}

// runHelperApply is `update helper-apply`.
func runHelperApply(ctx context.Context, o helperApplyOptions) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Err == nil {
		o.Err = io.Discard
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Supervisor == nil {
		o.Supervisor = hostsvcSupervisorFor
	}
	if o.HealthTimeout <= 0 {
		o.HealthTimeout = 60 * time.Second
	}

	// THE STORE-FORMAT GUARD, before the daemon is asked to stop: an update
	// whose binary cannot open the store on disk is refused while the node
	// keeps serving.
	if err := update.CheckStagedStoreFormat(o.Paths, o.UpdateID, o.StoreRoot); err != nil {
		var refusal *update.StoreFormatRefusal
		if errors.As(err, &refusal) {
			fmt.Fprintf(o.Err, "store_format_guard=refused update_id=%s slot_max_store_format=%d store_format=%d store_root=%s\n",
				o.UpdateID, refusal.SlotMaxStoreFormat, refusal.Store.Format, refusal.Store.Root)
		}
		return err
	}
	prepared, err := update.Prepare(o.Paths, update.ApplyOptions{
		UpdateID:      o.UpdateID,
		AllowRollback: o.AllowRollback,
		StoreRoot:     o.StoreRoot,
	})
	if err != nil {
		return err
	}
	defer prepared.Discard()
	version := prepared.Candidate.Result.Version
	fmt.Fprintf(o.Out, "prepared_update=%s version=%s\n", o.UpdateID, version)

	daemon, err := o.stopServingDaemon(ctx, version)
	if err != nil {
		return err
	}
	restartArgv := o.RestartArgv
	if len(restartArgv) == 0 {
		restartArgv = daemon.RestartArgv
	}

	var result *update.ApplyResult
	err = o.underStoreLock(o.UpdateID, version, func() error {
		var applyErr error
		result, applyErr = update.ApplyPrepared(o.Paths, prepared, update.ApplyOptions{
			AllowRollback: o.AllowRollback,
			Trigger:       strings.TrimSpace(o.Trigger),
			SignalKeyID:   strings.TrimSpace(o.SignalKeyID),
			StoreRoot:     o.StoreRoot,
		})
		return applyErr
	})
	if err != nil {
		// Nothing was swapped (or the swap restored itself): the build on disk
		// is the build that was running. Bring it back the way it ran rather
		// than leave the box dark.
		if daemon.PID > 0 && !o.NoRestart && len(restartArgv) > 0 {
			if _, restartErr := o.startBuildOnDisk(ctx, daemon, daemon.StoppedBySupervisor, restartArgv, o.UpdateID, version,
				"the swap did not happen; restarting the build that was running"); restartErr != nil {
				fmt.Fprintf(o.Err, "restart=failed error=%q\n", restartErr.Error())
			}
		}
		return err
	}
	fmt.Fprintf(o.Out, "applied_update=%s\n", result.UpdateID)
	fmt.Fprintf(o.Out, "version=%s\n", result.Version)
	fmt.Fprintf(o.Out, "sequence=%d\n", result.Sequence)
	fmt.Fprintf(o.Out, "rollback_path=%s\n", result.RollbackPath)

	if o.NoRestart || len(restartArgv) == 0 {
		fmt.Fprintln(o.Out, "restart=manual")
		fmt.Fprintln(o.Out, "next=restart the SDN daemon to run the new version")
		return nil
	}
	child, err := o.startBuildOnDisk(ctx, daemon, daemon.StoppedBySupervisor, restartArgv, result.UpdateID, result.Version,
		"starting the build just installed")
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}
	if strings.TrimSpace(o.AdminURL) == "" {
		return nil
	}
	// A declared rollback may install a build from before the identity
	// report; every other update installs one that reports it.
	requireIdentity := !declaresRollback(prepared.Candidate.Manifest)
	gateErr := o.gate(ctx, result.UpdateID, result.Version, requireIdentity)
	if gateErr == nil {
		return nil
	}
	return o.revert(ctx, daemon, child, restartArgv, result, gateErr)
}

// declaresRollback reports a manifest that goes BACK: a declared
// source-lineage rollback, or a signed sequence rollback.
func declaresRollback(m *update.Manifest) bool {
	if m == nil {
		return false
	}
	if m.Rollback != nil {
		return true
	}
	return m.Provenance != nil && m.Provenance.Lineage == update.LineageRollback
}

// stopServingDaemon asks the daemon to shut down and waits for its pid to
// exit (update.StopLadder). It returns an error, with nothing swapped, when
// the daemon answered but would not stop, or did not exit through every rung.
func (o helperApplyOptions) stopServingDaemon(ctx context.Context, version string) (servingDaemon, error) {
	var daemon servingDaemon
	if strings.TrimSpace(o.AdminURL) == "" || strings.TrimSpace(o.Token) == "" {
		fmt.Fprintln(o.Out, "daemon_shutdown=skipped reason=no-admin-url")
		return daemon, nil
	}
	if err := o.record("stop-requested", 0, version,
		"update shutdown handshake: asking the daemon at "+o.AdminURL+" to exit for this update"); err != nil {
		return daemon, err
	}
	answer, err := o.requestShutdown(o.Token)
	if err != nil {
		if isNotListening(err) {
			// Nothing is serving there. Whether a daemon still runs from this
			// bundle is for the store lock to say.
			fmt.Fprintf(o.Err, "daemon_shutdown=unavailable reason=not-listening error=%q\n", err.Error())
			return daemon, o.record("stop-skipped", 0, version,
				"no daemon is listening at "+o.AdminURL+" ("+err.Error()+"); the store lock decides whether one still runs")
		}
		// A daemon answered and did not stop. Swapping now is swapping under a
		// live process: refuse, and leave it serving.
		fmt.Fprintf(o.Err, "daemon_shutdown=refused error=%q\n", err.Error())
		_ = o.record("stop-failed", 0, version, "the daemon did not accept the update shutdown ("+err.Error()+"); it is still serving and nothing was swapped")
		return daemon, fmt.Errorf("the daemon did not accept the update shutdown, so nothing was swapped: %w", err)
	}
	daemon = servingDaemon{PID: answer.PID, RestartArgv: answer.RestartArgv, Supervised: answer.Supervised}
	fmt.Fprintf(o.Out, "daemon_shutdown=requested pid=%d supervised=%t\n", daemon.PID, daemon.Supervised)
	if daemon.Supervised {
		daemon.Supervisor = o.Supervisor(ctx, daemon.PID)
		if daemon.Supervisor != nil {
			fmt.Fprintf(o.Out, "supervisor=%q\n", daemon.Supervisor.Name())
		} else {
			fmt.Fprintln(o.Out, "supervisor=unproven")
		}
	}
	ladder := update.StopLadder{
		Paths:    o.Paths,
		UpdateID: o.UpdateID,
		Version:  version,
		PID:      daemon.PID,
		How:      "update shutdown handshake",
		Bounds:   o.Stop,
		Out:      o.Out,
	}
	if sup := daemon.Supervisor; sup != nil {
		ladder.Escalate = func() error { return sup.Stop(ctx) }
		ladder.EscalateHow = "supervisor stop of " + sup.Name()
	}
	stopped, err := ladder.Run(ctx)
	if err != nil {
		return daemon, err
	}
	daemon.StoppedBySupervisor = stopped.Escalated && daemon.Supervisor != nil
	return daemon, nil
}

// requestShutdown makes the update shutdown handshake with token.
func (o helperApplyOptions) requestShutdown(token string) (*update.ControlShutdown, error) {
	shutdownURL, err := adminEndpointURL(o.AdminURL, update.ControlShutdownPath)
	if err != nil {
		return nil, err
	}
	return update.RequestShutdown(o.Client, shutdownURL, o.Paths.Root, token)
}

// isNotListening reports a handshake that never reached a daemon: nothing is
// listening at the admin URL.
func isNotListening(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// storeLockWait bounds the helper's retries for the store lock once the
// daemon's pid has exited (the kernel releases the lock with the process).
const storeLockWait = 10 * time.Second

// underStoreLock runs swap holding the store's single-writer lock, so no
// daemon can have the store open while the bundle is renamed under it.
func (o helperApplyOptions) underStoreLock(updateID, version string, swap func() error) error {
	release, checked, err := lockUpdateStore(o.StoreRoot)
	deadline := time.Now().Add(storeLockWait)
	for err != nil && errors.Is(err, storage.ErrStoreLocked) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		release, checked, err = lockUpdateStore(o.StoreRoot)
	}
	if err != nil {
		fmt.Fprintf(o.Err, "store_lock=failed root=%s error=%q\n", o.StoreRoot, err.Error())
		_ = o.record("store-lock-failed", 0, version, "the store's single-writer lock is held, so a daemon still has the store open: nothing was swapped ("+err.Error()+")")
		return fmt.Errorf("take the store lock before swapping: %w", err)
	}
	defer release()
	if !checked {
		fmt.Fprintln(o.Err, "store_lock=unchecked reason=no-store")
		if err := o.record("store-unchecked", 0, version, "no record store at "+orNone(o.StoreRoot)+": the swap is not covered by the store lock"); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(o.Out, "store_lock=held root=%s\n", o.StoreRoot)
		if err := o.record("store-locked", 0, version, "took the single-writer lock of the store at "+o.StoreRoot+": no daemon can open it while the bundle is swapped"); err != nil {
			return err
		}
	}
	return swap()
}

// lockUpdateStore takes the store's single-writer lock. checked is false when
// there is no store to protect (no root resolved, or none on disk yet).
func lockUpdateStore(storeRoot string) (release func(), checked bool, err error) {
	root := strings.TrimSpace(storeRoot)
	if root == "" {
		return func() {}, false, nil
	}
	if _, statErr := os.Stat(root); errors.Is(statErr, os.ErrNotExist) {
		return func() {}, false, nil
	}
	lock, err := storage.LockStoreForMaintenance(root)
	if err != nil {
		return nil, true, err
	}
	return func() { _ = lock.Release() }, true, nil
}

// withStoreLock is the operator verbs' version (update apply, install
// --direct, rollback): the same lock, refused at once when a daemon holds it.
func withStoreLock(storeRoot string, fn func() error) error {
	release, _, err := lockUpdateStore(storeRoot)
	if err != nil {
		if errors.Is(err, storage.ErrStoreLocked) {
			return fmt.Errorf("the record store at %s is open, so a daemon is running from it, and nothing is swapped under a running daemon: stop it first, or use `spacedatanetwork update install`, whose helper stops it: %w", storeRoot, err)
		}
		return fmt.Errorf("take the store lock before swapping: %w", err)
	}
	defer release()
	return fn()
}

// applyUnlessDaemonRuns runs an in-process apply (update apply, install
// without the helper) under the store lock, so it refuses instead of swapping
// a bundle out from under a running daemon. A dry run swaps nothing and needs
// no lock.
func applyUnlessDaemonRuns(storeRoot string, dryRun bool, apply func() (*update.ApplyResult, error)) (*update.ApplyResult, error) {
	if dryRun {
		return apply()
	}
	var result *update.ApplyResult
	err := withStoreLock(storeRoot, func() error {
		var applyErr error
		result, applyErr = apply()
		return applyErr
	})
	return result, err
}

// startBuildOnDisk starts the build now on disk the way the stopped daemon
// ran: through the supervisor connector when the stop went through it (the
// stop sticks), by the supervisor's own restart policy when the daemon merely
// exited under it, or by spawning the restart argv. It returns the spawned
// process, if it spawned one.
func (o helperApplyOptions) startBuildOnDisk(ctx context.Context, daemon servingDaemon, viaSupervisor bool, argv []string, updateID, version, why string) (helperStartedProcess, error) {
	switch {
	case viaSupervisor && daemon.Supervisor != nil:
		o.recordAfter("restart", 0, version, why+": start of "+daemon.Supervisor.Name()+" through the supervisor connector")
		if err := daemon.Supervisor.Start(ctx); err != nil {
			return nil, err
		}
		fmt.Fprintf(o.Out, "restart=supervisor unit=%q\n", daemon.Supervisor.Name())
		return nil, nil
	case daemon.Supervised:
		o.recordAfter("restart", 0, version, why+": the supervisor's restart policy brings the daemon back under its own unit")
		fmt.Fprintln(o.Out, "restart=supervised")
		fmt.Fprintln(o.Out, "next=the supervising init restarts the daemon under its own unit; not direct-spawning")
		return nil, nil
	default:
		process, err := startHelperDaemonProcess(argv, o.Out, o.Err)
		if err != nil {
			return nil, err
		}
		o.recordAfter("restart", process.PID(), version, why+": spawned the daemon's restart argv")
		fmt.Fprintf(o.Out, "restart=started pid=%d\n", process.PID())
		return process, nil
	}
}

// gate waits for the restarted daemon to be healthy AND to be the build on
// disk (update.BundleIdentity).
func (o helperApplyOptions) gate(ctx context.Context, updateID, version string, requireIdentity bool) error {
	expected, err := update.BundleIdentity(o.Paths.Root)
	if err == nil {
		var identity update.Identity
		identity, err = waitForDaemonGate(ctx, o.Client, o.AdminURL, expected, requireIdentity, o.HealthTimeout)
		if err == nil {
			fmt.Fprintln(o.Out, "daemon_health=healthy")
			if identity.Reported() {
				fmt.Fprintf(o.Out, "daemon_identity=verified %s\n", identity)
			} else {
				fmt.Fprintln(o.Out, "daemon_identity=unreported")
			}
			o.recordAfter("health-passed", 0, version, "healthy, and the daemon that answered is the build on disk ("+identity.String()+")")
			return nil
		}
	}
	fmt.Fprintf(o.Err, "daemon_health=unhealthy error=%q\n", err.Error())
	o.recordAfter("health-failed", 0, version, err.Error())
	return err
}

// revert reverses an update whose build failed the gate. child is the process
// the helper spawned for it, if it spawned one.
func (o helperApplyOptions) revert(ctx context.Context, daemon servingDaemon, child helperStartedProcess, argv []string, result *update.ApplyResult, gateErr error) error {
	// Can this box be reverted at all? Asked BEFORE stopping anything: a revert
	// the store-format guard refuses, or one with no slot to restore, would
	// take the running build down for nothing.
	if err := update.CheckRollbackStoreFormat(o.Paths, o.StoreRoot); err != nil {
		return o.revertRefused(result, gateErr, err)
	}
	stoppedViaSupervisor, err := o.stopFailedBuild(ctx, daemon, child, result)
	if err != nil {
		return o.revertRefused(result, gateErr, fmt.Errorf("the build that failed could not be stopped: %w", err))
	}
	var restored *update.RollbackResult
	err = o.underStoreLock(result.UpdateID, result.Version, func() error {
		var rollbackErr error
		restored, rollbackErr = update.RollbackLast(o.Paths, update.RollbackOptions{
			Reason:    "daemon health failed after update: " + gateErr.Error(),
			StoreRoot: o.StoreRoot,
		})
		return rollbackErr
	})
	if err != nil {
		// Nothing was swapped: the build that failed its gate is still the
		// build on disk. Bring it back rather than leave the box dark.
		if _, restartErr := o.startBuildOnDisk(ctx, daemon, stoppedViaSupervisor, argv, result.UpdateID, result.Version,
			"the revert did not happen; restarting the current build"); restartErr != nil {
			fmt.Fprintf(o.Err, "restart=failed error=%q\n", restartErr.Error())
		}
		return o.revertRefused(result, gateErr, err)
	}
	fmt.Fprintf(o.Out, "rollback=applied restored_version=%s failed_path=%s\n", restored.RestoredVersion, restored.FailedPath)
	if _, err := o.startBuildOnDisk(ctx, daemon, stoppedViaSupervisor, argv, restored.RestoredUpdateID, restored.RestoredVersion,
		"starting the restored build"); err != nil {
		return fmt.Errorf("daemon health failed after update; rolled back to %s but restart failed: %w", restored.RestoredVersion, err)
	}
	// The restored build may predate the identity report; when it reports
	// one, it must be the build on disk.
	if err := o.gate(ctx, restored.RestoredUpdateID, restored.RestoredVersion, false); err != nil {
		return fmt.Errorf("daemon health failed after update; rolled back to %s but the restored daemon is unhealthy: %w", restored.RestoredVersion, err)
	}
	return fmt.Errorf("daemon health failed after update; rolled back to %s", restored.RestoredVersion)
}

// stopFailedBuild stops the build that failed the gate before anything is
// renamed back: through the supervisor connector when there is one (its stop
// sticks, so the restart policy cannot respawn the failed build mid-swap),
// else through the update shutdown handshake, escalating to signals. It
// reports whether the stop went through the supervisor.
func (o helperApplyOptions) stopFailedBuild(ctx context.Context, daemon servingDaemon, child helperStartedProcess, result *update.ApplyResult) (bool, error) {
	ladder := update.StopLadder{
		Paths:    o.Paths,
		UpdateID: result.UpdateID,
		Version:  result.Version,
		Bounds:   o.Stop,
		Out:      o.Out,
	}
	if sup := daemon.Supervisor; daemon.Supervised && sup != nil {
		ladder.PID = sup.MainPID(ctx)
		ladder.Request = func() error { return sup.Stop(ctx) }
		ladder.How = "supervisor stop of " + sup.Name()
		_, err := ladder.Run(ctx)
		return true, err
	}
	token, err := generateUpdateControlToken()
	if err != nil {
		return false, err
	}
	if err := update.WriteControlToken(o.Paths, token); err != nil {
		return false, err
	}
	ladder.How = "update shutdown handshake"
	if child != nil {
		// This helper started it: wait on the process itself.
		ladder.PID = child.PID()
		ladder.Exited = child.Done()
		ladder.Request = func() error {
			_, err := o.requestShutdown(token)
			return err
		}
		_, err := ladder.Run(ctx)
		return false, err
	}
	// Supervised, with no connector proven: the daemon names its own pid.
	if err := o.record("stop-requested", 0, result.Version, "update shutdown handshake: asking the daemon that failed its health gate to exit"); err != nil {
		return false, err
	}
	answer, err := o.requestShutdown(token)
	if err != nil {
		if isNotListening(err) {
			// Not serving: there is nothing to wait for, and the store lock
			// decides whether anything still runs.
			return false, nil
		}
		return false, err
	}
	ladder.PID = answer.PID
	_, err = ladder.Run(ctx)
	return false, err
}

// revertRefused alerts that the update could not be reverted and leaves the
// current build running: an operator has to decide what happens next.
func (o helperApplyOptions) revertRefused(result *update.ApplyResult, gateErr, cause error) error {
	reason := fmt.Sprintf("update %s failed its health gate (%v) and was NOT reverted: %s. The current build is left running; reverting it needs an operator.",
		result.UpdateID, gateErr, strings.TrimRight(cause.Error(), "."))
	fmt.Fprintf(o.Err, "revert=refused update_id=%s\n", result.UpdateID)
	o.recordAfter("revert-refused", 0, result.Version, reason)
	// The OPS ALERT lane (internal/ops): the registry logs the transition with
	// its stable prefix into this helper's journal, and the hook puts it on
	// the helper's own output.
	alerts := ops.NewRegistry()
	alerts.OnTransition(func(alert ops.Alert, active bool) {
		if active {
			fmt.Fprintf(o.Err, "ops_alert=raised kind=%s subject=%s severity=%s error=%q\n", alert.Kind, alert.Subject, alert.Severity, alert.LastError)
		}
	})
	alerts.Raise(ops.KindUpdateFailed, result.UpdateID, ops.SeverityError, reason)
	var refusal *update.StoreFormatRefusal
	if errors.As(cause, &refusal) {
		return fmt.Errorf("daemon health failed after update; the revert was refused and the current build is left running: %v: %w", gateErr, cause)
	}
	return fmt.Errorf("daemon health failed after update and rollback failed: %v: %w", gateErr, cause)
}

// record writes one step of the swap to the deploy ledger. Before the swap a
// step that cannot be recorded does not happen; the caller returns the error.
func (o helperApplyOptions) record(action string, pid int, version, reason string) error {
	return update.RecordDeployLedgerEntry(o.Paths, update.DeployLedgerEntry{
		Action:      action,
		UpdateID:    o.UpdateID,
		Version:     version,
		DaemonPID:   pid,
		Reason:      reason,
		Trigger:     strings.TrimSpace(o.Trigger),
		SignalKeyID: strings.TrimSpace(o.SignalKeyID),
	})
}

// recordAfter is record for the steps after the swap, which happen either way:
// a ledger that cannot be written is reported, not obeyed.
func (o helperApplyOptions) recordAfter(action string, pid int, version, reason string) {
	if err := o.record(action, pid, version, reason); err != nil {
		fmt.Fprintf(o.Err, "deploy_ledger=unwritten action=%s error=%q\n", action, err.Error())
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none resolved)"
	}
	return s
}

type helperStartedProcess interface {
	PID() int
	// Done is closed once the process has exited and been reaped.
	Done() <-chan struct{}
}

type helperExecProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p helperExecProcess) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p helperExecProcess) Done() <-chan struct{} { return p.done }

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
	// Reap it: a revert waits on this process's exit, and an unreaped child
	// would sit as a zombie that never "exits".
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return helperExecProcess{cmd: cmd, done: done}, nil
}

// waitForDaemonGate waits until the daemon is healthy and reports the
// expected identity, and returns the identity it reported.
func waitForDaemonGate(ctx context.Context, client *http.Client, rawAdminURL string, expected update.ExpectedIdentity, requireIdentity bool, timeout time.Duration) (update.Identity, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		identity, err := probeDaemonGate(ctx, client, rawAdminURL, expected, requireIdentity)
		if err == nil {
			return identity, nil
		}
		if time.Now().After(deadline) {
			return update.Identity{}, err
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return update.Identity{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func probeDaemonGate(ctx context.Context, client *http.Client, rawAdminURL string, expected update.ExpectedIdentity, requireIdentity bool) (update.Identity, error) {
	if err := probeDaemonHealth(ctx, client, rawAdminURL); err != nil {
		return update.Identity{}, err
	}
	identity, err := probeDaemonIdentity(ctx, client, rawAdminURL)
	if err != nil {
		return update.Identity{}, err
	}
	if err := expected.Check(identity, requireIdentity); err != nil {
		return update.Identity{}, err
	}
	return identity, nil
}

// probeDaemonIdentity reads which build is answering: /api/v1/id's
// bundle_version and build_sha256.
func probeDaemonIdentity(ctx context.Context, client *http.Client, rawAdminURL string) (update.Identity, error) {
	idURL, err := adminEndpointURL(rawAdminURL, "/api/v1/id")
	if err != nil {
		return update.Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, idURL, nil)
	if err != nil {
		return update.Identity{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return update.Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return update.Identity{}, fmt.Errorf("daemon identity rejected: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var identity update.Identity
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&identity); err != nil {
		return update.Identity{}, fmt.Errorf("decode daemon identity: %w", err)
	}
	return identity, nil
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
