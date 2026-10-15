package main

// `spacedatanetwork update distribution get|submit` — the OFFLINE signing lane
// of SEC-001 (deployment/identity/ROTATION.md step 4).
//
// `update distribute` submits an unsigned release and waits. With the fleet's
// update root kept offline, the owner then:
//
//	update distribution get <id> --document manifest --out manifest.json   (on the coordinator)
//	sign-update-manifest.mjs --kind manifest --document manifest.json ...   (on the offline machine)
//	update distribution submit <id> --document manifest --signed manifest.signed.json
//
// and the same with --document signal once the coordinator has built the
// signal. The coordinator accepts a signed document only if it verifies
// against its own update roots and covers exactly the waiting one; nothing
// here signs anything.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	updateDistributionDocument string
	updateDistributionOut      string
	updateDistributionSigned   string
	updateDistributionNodeURL  string
)

// distributionWaitingState is the state a distribution is in while each
// document waits for its signature (internal/api/update_distribution.go).
var distributionWaitingState = map[string]string{
	"manifest": "awaiting-manifest-signature",
	"signal":   "awaiting-signal-signature",
}

func distributionDocumentKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if _, ok := distributionWaitingState[kind]; !ok {
		return "", errors.New(`--document is "manifest" or "signal"`)
	}
	return kind, nil
}

// waitingDocument is the unsigned document a distribution waits on, refusing
// one that is not waiting (already signed, or the other document's turn).
func waitingDocument(view map[string]json.RawMessage, kind string) (json.RawMessage, error) {
	var state string
	_ = json.Unmarshal(view["state"], &state)
	if want := distributionWaitingState[kind]; state != want {
		return nil, fmt.Errorf("the distribution is %q, not waiting for its %s (%q)", state, kind, want)
	}
	doc := view[kind]
	if len(doc) == 0 || string(doc) == "null" {
		return nil, fmt.Errorf("the distribution carries no %s", kind)
	}
	return doc, nil
}

func newDistributionClient(cmd *cobra.Command) (*adminClient, error) {
	updateSignManifestNodeURL = updateDistributionNodeURL
	return newUpdateSigningClient(cmd)
}

var updateDistributionCmd = &cobra.Command{
	Use:   "distribution",
	Short: "Read a waiting release document, or submit one signed offline",
}

var updateDistributionGetCmd = &cobra.Command{
	Use:   "get <distribution-id>",
	Short: "Write the unsigned manifest or signal a distribution waits on, to sign offline",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind, err := distributionDocumentKind(updateDistributionDocument)
		if err != nil {
			return err
		}
		if strings.TrimSpace(updateDistributionOut) == "" {
			return errors.New("--out is required")
		}
		client, err := newDistributionClient(cmd)
		if err != nil {
			return err
		}
		var view map[string]json.RawMessage
		if err := client.get(context.Background(), "/api/v1/admin/updates/distributions/"+args[0], &view); err != nil {
			return err
		}
		doc, err := waitingDocument(view, kind)
		if err != nil {
			return err
		}
		if err := os.WriteFile(updateDistributionOut, append(append([]byte(nil), doc...), '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", updateDistributionOut, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", kind, updateDistributionOut)
		return nil
	},
}

var updateDistributionSubmitCmd = &cobra.Command{
	Use:   "submit <distribution-id>",
	Short: "Submit a manifest or signal signed offline with the fleet update root",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind, err := distributionDocumentKind(updateDistributionDocument)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(updateDistributionSigned)
		if err != nil {
			return fmt.Errorf("read --signed: %w", err)
		}
		if !json.Valid(raw) {
			return fmt.Errorf("%s is not JSON", updateDistributionSigned)
		}
		client, err := newDistributionClient(cmd)
		if err != nil {
			return err
		}
		var view map[string]json.RawMessage
		if err := client.postJSON(context.Background(), "/api/v1/admin/updates/distributions/"+args[0]+"/"+kind, json.RawMessage(raw), &view, ""); err != nil {
			return err
		}
		var state, keyID string
		_ = json.Unmarshal(view["state"], &state)
		_ = json.Unmarshal(view["key_id"], &keyID)
		fmt.Fprintf(cmd.OutOrStdout(), "accepted %s: state=%s key_id=%s\n", kind, state, keyID)
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{updateDistributionGetCmd, updateDistributionSubmitCmd} {
		c.Flags().StringVar(&updateDistributionDocument, "document", "manifest", `"manifest" or "signal"`)
		c.Flags().StringVar(&updateDistributionNodeURL, "node-url", "", "explicit base URL of the coordinator node (default: derived from the config bind address)")
		c.Flags().String("session-token", "", "session token for the admin API (default: $SDN_SESSION_TOKEN; omit to sign in with the node's root key)")
	}
	updateDistributionGetCmd.Flags().StringVar(&updateDistributionOut, "out", "", "where to write the unsigned document")
	updateDistributionSubmitCmd.Flags().StringVar(&updateDistributionSigned, "signed", "", "the document signed offline (sign-update-manifest.mjs --kind ... --document ...)")
	updateDistributionCmd.AddCommand(updateDistributionGetCmd, updateDistributionSubmitCmd)
	updateCmd.AddCommand(updateDistributionCmd)
}
