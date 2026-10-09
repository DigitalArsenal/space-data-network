package main

// `spacedatanetwork update distribute` — hand a release to the distribution key.
//
// Owner 2026-10-09: releases are signed with the distribution key, entered each
// time in the updater module's page, never saved. This command submits the
// unsigned manifest to the coordinator node (api/update_distribution.go), says
// where to sign it, and waits until the key has signed it. `update signal
// --distribution <id>` then holds the release's signal for the same key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

var (
	updateDistributeIn      string
	updateDistributeOut     string
	updateDistributeNodeURL string
	updateDistributeWait    time.Duration
)

type distributionView struct {
	ID             string          `json:"id"`
	State          string          `json:"state"`
	UpdateID       string          `json:"update_id"`
	Version        string          `json:"version"`
	Target         string          `json:"target"`
	KeyID          string          `json:"key_id"`
	Topic          string          `json:"topic"`
	SignedManifest json.RawMessage `json:"signed_manifest"`
}

var updateDistributeCmd = &cobra.Command{
	Use:   "distribute",
	Short: "Submit an unsigned release and wait for the distribution key to sign it",
	Long: "Submits an unsigned update manifest to this node's distribution queue. Open Updater in " +
		"the dashboard, enter the distribution key and sign the release; the signed manifest is " +
		"written to --out. The key is entered for every release and never saved.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(updateDistributeIn) == "" || strings.TrimSpace(updateDistributeOut) == "" {
			return errors.New("--manifest and --out are required")
		}
		raw, err := os.ReadFile(updateDistributeIn)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
		var doc map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&doc); err != nil {
			return fmt.Errorf("parse manifest: %w", err)
		}
		signing, _ := doc["signing"].(map[string]any)
		if signing == nil {
			return errors.New("manifest has no signing block")
		}
		// The distribution key names itself when it signs.
		signing["statement_domain"] = sigdomain.DomainUpdateManifestV1
		delete(signing, "key_id")
		delete(signing, "public_key")
		delete(signing, "signature")

		updateSignManifestNodeURL = updateDistributeNodeURL
		client, err := newUpdateSigningClient(cmd)
		if err != nil {
			return err
		}
		var submitted distributionView
		if err := client.postJSON(context.Background(), "/api/v1/admin/updates/distributions", doc, &submitted, ""); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "distribution=%s\n", submitted.ID)
		fmt.Fprintf(out, "next=open Updater in this node's dashboard, enter the distribution key and sign %s (%s)\n", submitted.Version, submitted.Target)
		signed, err := waitForDistribution(client, submitted.ID, "manifest-signed", updateDistributeWait, out)
		if err != nil {
			return err
		}
		if dir := filepath.Dir(updateDistributeOut); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create output directory: %w", err)
			}
		}
		if err := os.WriteFile(updateDistributeOut, append([]byte(signed.SignedManifest), '\n'), 0o644); err != nil {
			return fmt.Errorf("write signed manifest: %w", err)
		}
		fmt.Fprintf(out, "signed_manifest=%s\n", updateDistributeOut)
		fmt.Fprintf(out, "key_id=%s\n", signed.KeyID)
		return nil
	},
}

// waitForDistribution polls the distribution until it reaches state. A state
// past it counts: a signal can follow a manifest signature within one poll.
func waitForDistribution(client *adminClient, id, state string, wait time.Duration, out io.Writer) (*distributionView, error) {
	order := map[string]int{"awaiting-manifest-signature": 0, "manifest-signed": 1, "awaiting-signal-signature": 2, "signalled": 3}
	deadline := time.Now().Add(wait)
	for {
		var current distributionView
		if err := client.get(context.Background(), "/api/v1/admin/updates/distributions/"+id, &current); err != nil {
			return nil, err
		}
		if order[current.State] >= order[state] {
			return &current, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("distribution %s is still %s after %s: nobody entered the distribution key", id, current.State, wait)
		}
		time.Sleep(3 * time.Second)
	}
}

func init() {
	updateDistributeCmd.Flags().StringVar(&updateDistributeIn, "manifest", "", "path of the unsigned (sealed) manifest.json")
	updateDistributeCmd.Flags().StringVar(&updateDistributeOut, "out", "", "path to write the signed manifest.json")
	updateDistributeCmd.Flags().StringVar(&updateDistributeNodeURL, "node-url", "", "explicit base URL of the coordinator node (default: derived from the config bind address)")
	updateDistributeCmd.Flags().DurationVar(&updateDistributeWait, "wait", 30*time.Minute, "how long to wait for the distribution key")
	updateDistributeCmd.Flags().String("session-token", "", "session token for the admin API (default: $SDN_SESSION_TOKEN; omit to sign in with the node's root key)")
	updateCmd.AddCommand(updateDistributeCmd)
}
