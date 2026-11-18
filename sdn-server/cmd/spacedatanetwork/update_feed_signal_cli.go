package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spf13/cobra"
)

func signalCommandRoots(path string) (update.TrustedRoots, error) {
	if path != "" {
		return update.ReadTrustRootsFile(path)
	}
	for directory, err := os.Getwd(); err == nil; directory = filepath.Dir(directory) {
		repository := filepath.Join(directory, "deployment", "release", "fleet-trust-roots.json")
		if _, err := os.Stat(repository); err == nil {
			return update.ReadTrustRootsFile(repository)
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root))
}

func newSignSignalsCommand() *cobra.Command {
	var keyFile, keyEnv, dir, rootsPath string
	var verifyOnly bool
	var rolloutWindow int64
	cmd := &cobra.Command{Use: "sign-signals", Short: "Sign or verify every feed signal with the update roots", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			roots, err := signalCommandRoots(rootsPath)
			if err != nil {
				return err
			}
			if !verifyOnly {
				if (keyFile == "") == (keyEnv == "") {
					return errors.New("provide exactly one of --key-file or --key-env")
				}
				var raw []byte
				if keyFile != "" {
					raw, err = os.ReadFile(keyFile)
				} else {
					raw = []byte(os.Getenv(keyEnv))
				}
				if err != nil {
					return err
				}
				key, err := update.ParseSigningKeyPEM(raw)
				if err != nil {
					return err
				}
				var window *int64
				if rolloutWindow >= 0 {
					window = &rolloutWindow
				}
				if err := update.SignFeedSignals(dir, key, roots, window); err != nil {
					return err
				}
			}
			paths, err := update.VerifyFeedSignalDir(dir, roots)
			if err != nil {
				return err
			}
			for _, path := range paths {
				fmt.Fprintf(cmd.OutOrStdout(), "verified=%s\n", path)
			}
			return nil
		}}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "PKCS8 Ed25519 private key PEM")
	cmd.Flags().StringVar(&keyEnv, "key-env", "", "environment variable containing the signing PEM")
	cmd.Flags().StringVar(&dir, "feed-dir", "", "update feed directory")
	cmd.Flags().StringVar(&rootsPath, "roots", "", "trusted update roots file")
	cmd.Flags().BoolVar(&verifyOnly, "verify-only", false, "verify existing signals without signing")
	cmd.Flags().Int64Var(&rolloutWindow, "rollout-window-seconds", -1, "sign this rollout window into every signal (0 acts at once; unset leaves each node's update.rollout_window_seconds)")
	_ = cmd.MarkFlagRequired("feed-dir")
	return cmd
}

func sendFeedSignals(cmd *cobra.Command) error {
	if updateSignalVersion == "" {
		return errors.New("--from-feed requires --version")
	}
	if updateSignalTopic != "" || updateSignalDistribution != "" || updateSignalDryRun {
		return errors.New("--from-feed does not accept topic, distribution, or dry-run overrides")
	}
	files, err := update.FeedSignalFiles(strings.TrimSpace(os.Getenv("SDN_UPDATE_FEED_DIR")), updateSignalChannel, updateSignalVersion)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("version is absent or withdrawn")
	}
	root, err := os.OpenRoot(os.Getenv("SDN_UPDATE_FEED_DIR"))
	if err != nil {
		return err
	}
	defer root.Close()
	client, err := newSignalClient(cmd)
	if err != nil {
		return err
	}
	var failures []error
	for _, file := range files {
		raw, err := root.ReadFile(file.Path)
		if err == nil {
			signal, parseErr := update.ParseSignal(raw)
			if parseErr != nil {
				err = parseErr
			} else {
				published, publishedErr := time.Parse(time.RFC3339, signal.PublishedAt)
				if publishedErr != nil {
					err = fmt.Errorf("parse signal published_at: %w", publishedErr)
				}
				expires := published.Add(update.SignalDefaultTTL)
				if err == nil && signal.ExpiresAt != "" {
					expires, err = time.Parse(time.RFC3339, signal.ExpiresAt)
				}
				if err == nil && !time.Now().Before(expires) {
					err = errors.New("release signals were signed at build time and are valid for seven days; the pipeline must sign them again")
				}
			}
		}
		if err == nil {
			request, requestErr := client.newRequest(cmd.Context(), http.MethodPost, "/api/v1/admin/updates/signal/signed", bytes.NewReader(raw))
			err = requestErr
			if err == nil {
				response, sendErr := client.http.Do(request)
				err = sendErr
				if err == nil {
					body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
					response.Body.Close()
					err = readErr
					if response.StatusCode != http.StatusOK {
						err = fmt.Errorf("signed signal refused: %s: %s", response.Status, body)
					}
				}
			}
		}
		target := file.Entry.Target.Platform + "/" + file.Entry.Target.Arch
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "target=%s refused=%s\n", target, err)
			failures = append(failures, err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "target=%s sent=%s\n", target, file.Entry.UpdateID)
		}
	}
	return errors.Join(failures...)
}

func newWithdrawCommand() *cobra.Command {
	var channel, version, kind string
	cmd := &cobra.Command{Use: "withdraw", Short: "Withdraw a version from the served update feed", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			archive, err := update.WithdrawFeedVersion(os.Getenv("SDN_UPDATE_FEED_DIR"), kind, channel, version)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "withdrawn=%s kind=%s channel=%s archive=%s\n", version, kind, channel, archive)
			return nil
		}}
	cmd.Flags().StringVar(&kind, "kind", "cli-bundle", "update kind")
	cmd.Flags().StringVar(&channel, "channel", "", "feed channel")
	cmd.Flags().StringVar(&version, "version", "", "release version")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func init() {
	updateSignalCmd.Flags().BoolVar(&updateSignalFromFeed, "from-feed", false, "publish the pipeline-signed signals from SDN_UPDATE_FEED_DIR")
	updateCmd.AddCommand(newSignSignalsCommand(), newWithdrawCommand())
}

var updateSignalFromFeed bool
