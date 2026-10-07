package main

// `spacedatanetwork update seal` — encrypts an update payload for the nodes
// that will install it (G2), before its manifest is signed.
//
// The publisher builds the bundle and the unsigned manifest, reads each target
// node's sealed-transport key from that node's /api/node/info, and runs this on
// the publisher host next to `update sign-manifest`. It encrypts the bundle
// once under a fresh content key, wraps that key for each node, records the
// wrapped keys in the manifest's envelope and the ciphertext carrier's hash in
// wasm.hash, and writes the carrier to publish as update.wasm. The signature
// that follows covers the envelope, so the recipient set is signed too.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
)

var (
	updateSealManifest    string
	updateSealBundle      string
	updateSealRecipients  string
	updateSealOutManifest string
	updateSealOutCarrier  string
)

// sealRecipient is one --recipients row: a node, as its /api/node/info
// advertises its sealed transport.
type sealRecipient struct {
	Name          string `json:"name"`
	PeerID        string `json:"peer_id"`
	EncryptionKey string `json:"encryption_key"`
	Fingerprint   string `json:"fingerprint"`
}

var updateSealCmd = &cobra.Command{
	Use:   "seal",
	Short: "Encrypt an update payload for the nodes that install it",
	Long: "Encrypts --bundle once under a fresh content key, wraps the key for every node in --recipients " +
		"(name, peer_id, encryption_key hex, fingerprint, as each node's /api/node/info reports them), records " +
		"the envelope in the unsigned --manifest and writes it to --out-manifest, and writes the ciphertext " +
		"carrier to --out-carrier. Sign the result with `update sign-manifest`.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, f := range []struct{ value, name string }{
			{updateSealManifest, "--manifest"}, {updateSealBundle, "--bundle"}, {updateSealRecipients, "--recipients"},
			{updateSealOutManifest, "--out-manifest"}, {updateSealOutCarrier, "--out-carrier"},
		} {
			if strings.TrimSpace(f.value) == "" {
				return fmt.Errorf("%s is required", f.name)
			}
		}
		manifestBytes, err := os.ReadFile(updateSealManifest)
		if err != nil {
			return err
		}
		bundle, err := os.ReadFile(updateSealBundle)
		if err != nil {
			return err
		}
		rowsBytes, err := os.ReadFile(updateSealRecipients)
		if err != nil {
			return err
		}
		var rows []sealRecipient
		if err := json.Unmarshal(rowsBytes, &rows); err != nil {
			return fmt.Errorf("read --recipients: %w", err)
		}
		if len(rows) == 0 {
			return errors.New("--recipients names no node")
		}
		recipients := make([]update.EnvelopeRecipient, 0, len(rows))
		for _, row := range rows {
			pub, err := hex.DecodeString(strings.TrimSpace(row.EncryptionKey))
			if err != nil || len(pub) != 33 {
				return fmt.Errorf("recipient %s: encryption_key is not a compressed secp256k1 key in hex", row.Name)
			}
			// The key must be the one the fingerprint names: a row that mixes
			// two nodes' fields would seal for one and address the other.
			if got := walletderive.Fingerprint(pub); got != row.Fingerprint {
				return fmt.Errorf("recipient %s: fingerprint %s does not match its key (%s)", row.Name, row.Fingerprint, got)
			}
			recipients = append(recipients, update.EnvelopeRecipient{KeyID: []byte(row.Fingerprint), PublicKey: pub, KeyExchange: ecies.Secp256k1})
		}

		decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
		decoder.UseNumber()
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			return fmt.Errorf("read --manifest: %w", err)
		}
		carrier, err := update.SealPayload(doc, bundle, recipients)
		if err != nil {
			return err
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(updateSealOutManifest, append(out, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(updateSealOutCarrier, carrier, 0o644); err != nil {
			return err
		}
		names := make([]string, len(rows))
		for i, row := range rows {
			names[i] = row.Name
		}
		fmt.Fprintf(cmd.OutOrStdout(), "sealed=%s\nrecipients=%s\ncarrier_bytes=%d\n", doc["update_id"], strings.Join(names, ","), len(carrier))
		return nil
	},
}

func init() {
	updateSealCmd.Flags().StringVar(&updateSealManifest, "manifest", "", "unsigned update manifest describing the plaintext bundle")
	updateSealCmd.Flags().StringVar(&updateSealBundle, "bundle", "", "the plaintext bundle archive")
	updateSealCmd.Flags().StringVar(&updateSealRecipients, "recipients", "", "JSON list of recipient nodes")
	updateSealCmd.Flags().StringVar(&updateSealOutManifest, "out-manifest", "", "where to write the sealed, still unsigned manifest")
	updateSealCmd.Flags().StringVar(&updateSealOutCarrier, "out-carrier", "", "where to write the ciphertext carrier (update.wasm)")
	updateCmd.AddCommand(updateSealCmd)
}
