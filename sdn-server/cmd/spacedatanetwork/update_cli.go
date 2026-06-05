package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check and apply signed SDN bundle updates",
}

var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check the bundled update manifest",
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest, err := loadCurrentBundleManifest()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", manifest.Version)
		fmt.Fprintf(cmd.OutOrStdout(), "channel=%s\n", manifest.Channel)
		fmt.Fprintf(cmd.OutOrStdout(), "update_feed_base_url=%s\n", manifest.Update.FeedBaseURL)
		fmt.Fprintf(cmd.OutOrStdout(), "update_pubsub_topic=%s\n", manifest.Update.PubsubTopic)
		fmt.Fprintf(cmd.OutOrStdout(), "updater_module=%s\n", manifest.Update.UpdaterModule)
		fmt.Fprintf(cmd.OutOrStdout(), "updater_wasm=%s\n", manifest.Update.UpdaterWASM)
		fmt.Fprintln(cmd.OutOrStdout(), "update_check_scope=bundled_manifest")
		fmt.Fprintln(cmd.OutOrStdout(), "updates_available=false")
		return nil
	},
}

var updateApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a staged signed SDN bundle update",
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("no staged update is available")
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

func init() {
	updateCmd.AddCommand(updateCheckCmd)
	updateCmd.AddCommand(updateApplyCmd)
	rootCmd.AddCommand(updateCmd)
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
