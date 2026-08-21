package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
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
	helperApplyTrigger         string
	helperApplyAdminCA         string
	helperApplySignalKeyID     string
	helperApplyAllowRollback   bool
	// helperApplyDaemonUnit / helperApplyDaemonRestartPolicy are the supervisor
	// facts the DAEMON resolved at launch time (hostsvc.Probe inside
	// LaunchSelfUpgrade) and handed to the helper as explicit args. The helper
	// cross-checks them against the shutdown response before Apply and refuses
	// on conflict (ops-update-lane-restart-policy-preflight).
	helperApplyDaemonUnit          string
	helperApplyDaemonRestartPolicy string
	updateInstallHealthTimeout     time.Duration
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
		loopback := helperLoopbackTransport(helperApplyAdminCA, helperApplyAdminURL)
		var daemonShutdown *daemonShutdownInfo
		if strings.TrimSpace(helperApplyAdminURL) != "" && strings.TrimSpace(helperApplyToken) != "" {
			info, err := requestDaemonUpdateShutdown(daemonLoopbackClientWith(10*time.Second, loopback), helperApplyAdminURL, helperApplyBundleRoot, helperApplyToken)
			if err != nil {
				// A REFUSAL is not an outage: the daemon stayed up because it
				// could not resolve its own unit and Restart= policy, and
				// applying under an unplannable supervisor is exactly how a box
				// stays down (live incident 2026-08-08). Same token, same launch:
				// a corrected retry is safe. The helper aborts BEFORE any swap.
				var refused *daemonShutdownRefusedError
				if errors.As(err, &refused) {
					return refused
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "daemon_shutdown=unavailable error=%q\n", err.Error())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon_shutdown=requested")
				daemonShutdown = info
				if len(restartArgv) == 0 {
					restartArgv = info.RestartArgv
				}
				if info.Supervised {
					fmt.Fprintf(cmd.OutOrStdout(), "daemon_supervisor=resolved unit=%s restart=%s\n", info.Unit, info.RestartPolicy)
				}
				time.Sleep(2 * time.Second)
			}
		}
		paths := update.PathsFor(helperApplyBundleRoot)
		// SUPERVISOR-PLAN GATES (ops-update-lane-restart-policy-preflight).
		// Every check runs BEFORE Apply touches the bundle, so a refusal costs
		// nothing but an error and a retry needs no fresh launch.
		supervisorPlan, err := resolveHelperSupervisorPlan(cmd.Context(), daemonShutdown)
		if err != nil {
			return err
		}
		result, err := update.Apply(paths, update.ApplyOptions{
			UpdateID:      helperApplyUpdateID,
			AllowRollback: helperApplyAllowRollback,
			Trigger:       strings.TrimSpace(helperApplyTrigger),
			SignalKeyID:   strings.TrimSpace(helperApplySignalKeyID),
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
			Paths:         paths,
			RestartArgv:   restartArgv,
			Unit:          supervisorPlan.Unit,
			RestartPolicy: supervisorPlan.RestartPolicy,
			AdminURL:      helperApplyAdminURL,
			NoRestart:     helperApplyNoRestart,
			// The SAME anchored transport, captured above while the daemon was
			// still running: a probe that cannot verify the daemon's own
			// certificate reports "unhealthy" for a perfectly healthy daemon
			// and rolls a good update back.
			Client:        daemonLoopbackClientWith(5*time.Second, loopback),
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
	updateHelperApplyCmd.Flags().StringVar(&helperApplyAdminCA, "admin-ca", "",
		"path of the certificate the daemon serves, handed over by the daemon itself; used as the TLS anchor for the loopback shutdown handshake and health gate")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyTrigger, "trigger", "", "what caused this apply (\"signal\" for a pushed update signal); recorded in the deploy ledger")
	updateHelperApplyCmd.Flags().StringVar(&helperApplySignalKeyID, "signal-key-id", "", "signing key of the signal that triggered this apply; recorded in the deploy ledger")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyDaemonUnit, "daemon-unit", "", "systemd unit this daemon runs under, resolved by the daemon at launch and handed to the helper; cross-checked against the shutdown response and refused on mismatch")
	updateHelperApplyCmd.Flags().StringVar(&helperApplyDaemonRestartPolicy, "daemon-restart-policy", "", "Restart= policy of the daemon's resolved unit, carried from launch time so the helper can refuse an unknown policy before any exit is requested")
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
	// Unit is the resolved systemd unit the DAEMON runs under, taken from the
	// daemon's own shutdown response (requestDaemonUpdateShutdown) and
	// cross-checked against the --daemon-unit launch facts. When it is
	// non-empty the daemon IS supervised and the only legal restart is an
	// explicit `systemctl restart` of THAT unit: never a direct spawn, which
	// leaves a live, unsupervised replacement outside the unit's cgroup while
	// the unit loops "activating" against the store's single-writer lock (six
	// occurrences, graph task sdn-update-helper-supervisor-mode), and never a
	// bare wait on the supervisor's Restart= policy, which is a STOP under
	// on-failure and no (live incident 2026-08-08) and an activating lock-loop
	// under always.
	Unit string
	// RestartPolicy is the resolved unit's Restart= setting verbatim, carried
	// for the record and for the rollback line. The restart plan does not
	// depend on it: every known policy gets the explicit unit restart, and an
	// unknown policy was refused before Apply began.
	RestartPolicy string
	AdminURL      string
	NoRestart     bool
	Out           io.Writer
	Err           io.Writer
	Client        *http.Client
	HealthTimeout time.Duration
	StartDaemon   func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error)
	WaitHealth    func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error
	Rollback      func(paths update.Paths) (*update.RollbackResult, error)
	// RestartUnit starts the daemon's resolved systemd unit explicitly. Nil
	// uses the systemctl restart against hostsvc.SystemctlPath; the seam
	// exists so the supervised flow's sequencing is testable on hosts that
	// have no systemd to talk to.
	RestartUnit func(unit string) error
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
	if opts.NoRestart {
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

	// SUPERVISED: the daemon runs under a resolved systemd unit, so the restart
	// is an explicit unit restart — never a bare wait on Restart=, never a
	// direct spawn.
	if strings.TrimSpace(opts.Unit) != "" {
		restartUnit := opts.RestartUnit
		if restartUnit == nil {
			restartUnit = restartDaemonUnit
		}
		return helperPostApplyRestartUnit(ctx, opts, restartUnit, waitHealth, rollback, client, out, errOut, timeout)
	}

	if len(opts.RestartArgv) == 0 {
		fmt.Fprintln(out, "restart=manual")
		fmt.Fprintln(out, "next=restart the SDN daemon to run the new version")
		return nil
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

// helperPostApplyRestartUnit is the supervised branch of
// helperPostApplyRestart: it restarts the daemon by EXPLICITLY asking systemd
// to restart the resolved unit (opts.Unit), then health-waits, and on failure
// rolls the bundle back and restarts the unit again. It NEVER calls
// StartDaemon and it never relies on the supervisor's Restart= policy to
// resurrect a clean exit — under Restart=on-failure or Restart=no a clean
// daemon exit is a STOP and the box stays down (live incident 2026-08-08),
// and under always a clean exit is what feeds the "activating" lock-loop
// against the store's single-writer lock. Every known policy gets the
// explicit restart; the refused unknown policy never reaches Apply.
func helperPostApplyRestartUnit(
	ctx context.Context,
	opts helperPostApplyOptions,
	restartUnit func(unit string) error,
	waitHealth func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error,
	rollback func(paths update.Paths) (*update.RollbackResult, error),
	client *http.Client,
	out, errOut io.Writer,
	timeout time.Duration,
) error {
	unit := strings.TrimSpace(opts.Unit)
	fmt.Fprintf(out, "restart=supervised unit=%s policy=%s\n", unit, opts.RestartPolicy)
	fmt.Fprintln(out, "next=restarting the resolved systemd unit explicitly; the helper does not direct-spawn a supervised daemon")
	if err := restartUnit(unit); err != nil {
		fmt.Fprintf(errOut, "restart_daemon_unit=error error=%q\n", err.Error())
		return supervisedRestartRollbackAndRetry(ctx, opts, restartUnit, waitHealth, rollback, client, out, errOut, timeout,
			fmt.Sprintf("restarting unit %s failed", unit))
	}
	fmt.Fprintf(out, "restart=requested unit=%s\n", unit)
	if strings.TrimSpace(opts.AdminURL) == "" {
		return nil
	}
	if err := waitHealth(ctx, client, opts.AdminURL, timeout); err == nil {
		fmt.Fprintln(out, "daemon_health=healthy")
		return nil
	} else {
		fmt.Fprintf(errOut, "daemon_health=unhealthy error=%q\n", err.Error())
		return supervisedRestartRollbackAndRetry(ctx, opts, restartUnit, waitHealth, rollback, client, out, errOut, timeout,
			"daemon health failed after update")
	}
}

// supervisedRestartRollbackAndRetry is the rollback leg of the unit-restart
// flow: roll the bundle back to the previous slot, start the resolved unit
// again (the binary is now the one that was serving before), wait for health,
// and return an error naming the failure and the restored version.
func supervisedRestartRollbackAndRetry(
	ctx context.Context,
	opts helperPostApplyOptions,
	restartUnit func(unit string) error,
	waitHealth func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error,
	rollback func(paths update.Paths) (*update.RollbackResult, error),
	client *http.Client,
	out, errOut io.Writer,
	timeout time.Duration,
	cause string,
) error {
	rollbackResult, rollbackErr := rollback(opts.Paths)
	if rollbackErr != nil {
		return fmt.Errorf("%s and rollback failed: %v: %w", cause, rollbackErr, rollbackErr)
	}
	fmt.Fprintf(out, "rollback=applied restored_version=%s failed_path=%s\n",
		rollbackResult.RestoredVersion, rollbackResult.FailedPath)
	if err := restartUnit(strings.TrimSpace(opts.Unit)); err != nil {
		return fmt.Errorf("%s; rolled back to %s but restarting unit %s failed: %w",
			cause, rollbackResult.RestoredVersion, strings.TrimSpace(opts.Unit), err)
	}
	fmt.Fprintf(out, "restart=requested unit=%s restored_version=%s\n", strings.TrimSpace(opts.Unit), rollbackResult.RestoredVersion)
	if restoredHealthErr := waitHealth(ctx, client, opts.AdminURL, timeout); restoredHealthErr != nil {
		return fmt.Errorf("%s; rolled back to %s but the restored daemon is unhealthy: %w",
			cause, rollbackResult.RestoredVersion, restoredHealthErr)
	}
	fmt.Fprintln(out, "daemon_health=healthy")
	return fmt.Errorf("%s; rolled back to %s", cause, rollbackResult.RestoredVersion)
}

// restartDaemonUnit asks systemd to restart the resolved unit explicitly and
// returns once the request is ACCEPTED. --no-block means systemd enqueues the
// job and returns immediately: the unit comes up outside this helper's
// lifetime, which is exactly the point, because the helper must not die with
// the unit's teardown. The unit name comes from hostsvc.Probe (unitFromCgroup
// admits only ".service" units), so the suffix check here is belt and braces
// against a hand-assembled launch arg ever reaching systemctl.
func restartDaemonUnit(unit string) error {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return errors.New("restart daemon unit: unit is empty")
	}
	if !strings.HasSuffix(unit, ".service") {
		return fmt.Errorf("restart daemon unit %q: not a service unit; only units resolved by hostsvc.Probe are restarted", unit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hostsvc.SystemctlPath, "restart", "--no-block", unit)
	// A deliberately EMPTY environment, matching hostsvc.Control: systemctl
	// must resolve the unit exactly as the manager sees it, not through a host
	// environment that could shadow XDG_RUNTIME_DIR or SYSTEMD_UNIT_PATH.
	cmd.Env = []string{}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart daemon unit %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	return nil
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

// daemonShutdownInfo carries everything the daemon's shutdown response
// resolved about itself: its restart argv, whether it was supervised by
// systemd, and — when supervised — the exact unit it runs under and that
// unit's Restart= policy. The helper's restart plan is built from these facts
// and the --daemon-unit/--daemon-restart-policy launch facts, and nothing
// else.
type daemonShutdownInfo struct {
	RestartArgv   []string
	Supervised    bool
	Unit          string
	RestartPolicy string
}

// daemonShutdownRefusedError is a REFUSAL, distinct from an outage: the
// daemon answered (non-202) and stayed up, explaining why it will not shut
// down. A refusal means "do not apply": proceeding would proceed with no
// restart plan. A transport error by contrast means the daemon's answer was
// never heard, which the lane has always treated as "unavailable".
type daemonShutdownRefusedError struct {
	HTTPStatus int
	Detail     string
}

func (e *daemonShutdownRefusedError) Error() string {
	return fmt.Sprintf("daemon update shutdown refused (%s): %s", http.StatusText(e.HTTPStatus), e.Detail)
}

// requestDaemonUpdateShutdown asks the live daemon to shut down for an
// update-apply and reports everything it resolved about itself: its restart
// argv, whether IT was supervised by systemd (INVOCATION_ID set in its own
// environment — the daemon is the only party that can answer this reliably;
// the helper's own environment says nothing about how the daemon it is
// restarting was started), and when supervised the exact unit and its
// Restart= policy. A non-202 answer is returned as *daemonShutdownRefusedError;
// only transport/parse failures return as plain errors ("unavailable").
func requestDaemonUpdateShutdown(client *http.Client, rawAdminURL string, bundleRoot string, token string) (*daemonShutdownInfo, error) {
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
		return nil, &daemonShutdownRefusedError{
			HTTPStatus: resp.StatusCode,
			Detail:     strings.TrimSpace(string(data)),
		}
	}
	var result struct {
		RestartArgv   []string `json:"restartArgv"`
		Supervised    bool     `json:"supervised"`
		Unit          string   `json:"unit"`
		RestartPolicy string   `json:"restartPolicy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &daemonShutdownInfo{
		RestartArgv:   result.RestartArgv,
		Supervised:    result.Supervised,
		Unit:          result.Unit,
		RestartPolicy: result.RestartPolicy,
	}, nil
}

// helperSupervisorPlan is the post-shutdown restart plan: the resolved unit
// to restart EXPLICITLY, if this daemon is supervised, plus its Restart=
// policy for the record. Empty Unit means "not supervised — direct-spawn
// path".
type helperSupervisorPlan struct {
	Unit          string
	RestartPolicy string
}

// resolveHelperSupervisorPlan validates the restart plan the helper is about
// to execute and returns the resolved unit to restart. It is called AFTER the
// shutdown handshake and BEFORE any apply (ops-update-lane-restart-policy-
// preflight). The gates, in order:
//
//  1. validateSupervisorPlan — a daemon that reports itself supervised but
//     whose response resolved no unit or no Restart= policy is a REFUSAL, not
//     a plan: under Restart=on-failure/no a clean exit is a STOP and the box
//     stays down (live incident 2026-08-08). A launched supervised helper
//     that finds the daemon NOT supervised (response lost or nothing heard)
//     falls back to the launch facts the daemon resolved at launch time; when
//     even those are missing the launcher's claim that the daemon is
//     supervised is stale and refused.
//  2. assertResolvedUnitStable — the unit the daemon resolved at launch
//     (--daemon-unit) must equal the unit its shutdown response resolved
//     seconds later; a mismatch means the unit was renamed or recreated while
//     this helper ran, and restarting the launch-time name would hit nothing.
//  3. assertHelperEscapedDaemonUnit — this helper's own cgroup must NOT
//     resolve back to the daemon's unit: a helper inside the daemon's unit
//     (setsid fallback, mis-scoped systemd-run) is SIGTERM'd by the unit
//     teardown the moment the daemon exits — mid-swap, tearing the bundle.
func resolveHelperSupervisorPlan(ctx context.Context, shutdown *daemonShutdownInfo) (helperSupervisorPlan, error) {
	launchUnit := strings.TrimSpace(helperApplyDaemonUnit)
	launchPolicy := strings.TrimSpace(helperApplyDaemonRestartPolicy)
	if shutdown == nil {
		// Nothing heard from the daemon at all. Launch-time facts carry the
		// plan when they exist — the daemon resolved them while it could still
		// refuse — but the helper-escape gate still runs: a helper that has
		// not escaped is refused regardless of who resolved what.
		if launchUnit == "" {
			return helperSupervisorPlan{}, nil
		}
		if launchPolicy == "" {
			return helperSupervisorPlan{}, fmt.Errorf(
				"supervisor plan incomplete: this helper was launched for unit %q but no Restart= policy was carried; a daemon exit under an unknown policy cannot be planned (a clean exit is a STOP under on-failure or no). Verify the service definition and relaunch", launchUnit)
		}
		if err := assertHelperEscapedDaemonUnit(ctx, launchUnit); err != nil {
			return helperSupervisorPlan{}, err
		}
		return helperSupervisorPlan{Unit: launchUnit, RestartPolicy: launchPolicy}, nil
	}
	if !shutdown.Supervised {
		if launchUnit != "" {
			return helperSupervisorPlan{}, fmt.Errorf(
				"supervisor plan conflict: the daemon reports it is NOT supervised (unit=%q restart=%q) but this helper was launched with --daemon-unit %q; the launch facts are stale — refusing the apply", shutdown.Unit, shutdown.RestartPolicy, launchUnit)
		}
		return helperSupervisorPlan{}, nil
	}
	if strings.TrimSpace(shutdown.Unit) == "" || strings.TrimSpace(shutdown.RestartPolicy) == "" {
		return helperSupervisorPlan{}, fmt.Errorf(
			"update refused: the daemon reports it is supervised by systemd but resolved no owning unit or Restart= policy (unit=%q restart=%q); a clean daemon exit is a STOP under Restart=on-failure/no and no explicit restart can be planned. Verify the service definition (systemctl status, systemctl show), fix it, and retry — the daemon is still up and untouched, and the launch token is unconsumed", shutdown.Unit, shutdown.RestartPolicy)
	}
	unit := strings.TrimSpace(shutdown.Unit)
	if launchUnit != "" && launchUnit != unit {
		return helperSupervisorPlan{}, fmt.Errorf(
			"resolved unit is stale: the daemon resolved %q at launch but %q at shutdown — the unit was renamed or recreated while this helper ran; refusing the apply", launchUnit, unit)
	}
	if err := assertHelperEscapedDaemonUnit(ctx, unit); err != nil {
		return helperSupervisorPlan{}, err
	}
	return helperSupervisorPlan{Unit: unit, RestartPolicy: strings.TrimSpace(shutdown.RestartPolicy)}, nil
}

// probeHost resolves THIS helper process's own supervisor state. A package
// var so the helper-escape gate is testable on hosts (macOS, CI, containers)
// that have no systemd to probe; the production value is hostsvc.Probe.
var probeHost = func(ctx context.Context) hostsvc.State { return hostsvc.Probe(ctx) }

// assertHelperEscapedDaemonUnit proves this helper process does not live in
// the daemon's own unit cgroup (helper escape, ops-update-lane-restart-policy-
// preflight). probeHost resolves the helper's OWN unit from its own cgroup;
// resolving the daemon's unit — or failing to resolve any unit at all — is a
// refusal, fail-closed.
func assertHelperEscapedDaemonUnit(ctx context.Context, daemonUnit string) error {
	daemonUnit = strings.TrimSpace(daemonUnit)
	state := probeHost(ctx)
	own := strings.TrimSpace(state.Unit)
	if own == "" || own == daemonUnit {
		return fmt.Errorf(
			"helper cgroup check failed: this helper resolves its own unit as %q and the daemon's unit is %q — the helper has NOT escaped the daemon's unit cgroup, and the unit teardown that follows the daemon's exit would SIGTERM it mid-swap, tearing the bundle. Launch the helper outside the daemon unit (systemd-run), then retry; no files were changed", own, daemonUnit)
	}
	return nil
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
