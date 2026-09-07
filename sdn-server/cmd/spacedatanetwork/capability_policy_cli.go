package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

type capabilityApprovalRequest struct {
	ModuleHash      string `json:"module_hash"`
	Capability      string `json:"capability"`
	PluginID        string `json:"plugin_id,omitempty"`
	SignerPubKeyHex string `json:"signer_pubkey_hex,omitempty"`
	Note            string `json:"note,omitempty"`
}

func init() {
	pluginsCmd.AddCommand(newCapabilityPolicyCommand(newAdminClient))
}

func newCapabilityPolicyCommand(connect func(*cobra.Command) (*adminClient, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "capabilities", Short: "Inspect and approve module capabilities through the authenticated native policy API"}
	var listHash string
	list := &cobra.Command{
		Use: "list", Short: "List persisted capability approvals", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/api/modules/capabilities"
			if listHash != "" {
				if err := validateCapabilityHash(listHash); err != nil {
					return err
				}
				path += "?module_hash=" + url.QueryEscape(strings.ToLower(listHash))
			}
			client, err := connect(cmd)
			if err != nil {
				return err
			}
			var result json.RawMessage
			if err := client.get(cmd.Context(), path, &result); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	list.Flags().StringVar(&listHash, "module-hash", "", "filter by portable module SHA-256")
	var approval capabilityApprovalRequest
	approve := &cobra.Command{
		Use: "approve", Short: "Approve one capability for one portable module hash", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateCapabilityHash(approval.ModuleHash); err != nil {
				return err
			}
			approval.ModuleHash = strings.ToLower(approval.ModuleHash)
			approval.Capability = strings.TrimSpace(approval.Capability)
			if approval.Capability == "" {
				return fmt.Errorf("--capability is required")
			}
			if approval.SignerPubKeyHex != "" {
				if _, err := modulePublicKey(approval.SignerPubKeyHex); err != nil {
					return err
				}
				approval.SignerPubKeyHex = strings.ToLower(strings.TrimSpace(approval.SignerPubKeyHex))
			}
			client, err := connect(cmd)
			if err != nil {
				return err
			}
			var result json.RawMessage
			if err := client.postJSON(cmd.Context(), "/api/modules/capabilities/approve", approval, &result, ""); err != nil {
				return err
			}
			var recorded capabilityApprovalRequest
			if err := json.Unmarshal(result, &recorded); err != nil {
				return err
			}
			if recorded.ModuleHash != approval.ModuleHash || recorded.Capability != approval.Capability ||
				(approval.PluginID != "" && recorded.PluginID != approval.PluginID) ||
				(approval.SignerPubKeyHex != "" && recorded.SignerPubKeyHex != approval.SignerPubKeyHex) {
				return fmt.Errorf("native policy response does not confirm the requested approval")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	approve.Flags().StringVar(&approval.ModuleHash, "module-hash", "", "portable module SHA-256")
	approve.Flags().StringVar(&approval.Capability, "capability", "", "one exact capability to approve")
	approve.Flags().StringVar(&approval.PluginID, "plugin-id", "", "module identity for the policy record")
	approve.Flags().StringVar(&approval.SignerPubKeyHex, "signer-public-key", "", "publisher Ed25519 public key in hex")
	approve.Flags().StringVar(&approval.Note, "note", "", "reason recorded with the approval")
	addSessionTokenFlag(list)
	addSessionTokenFlag(approve)
	cmd.AddCommand(list, approve)
	return cmd
}

func validateCapabilityHash(value string) error {
	hash, err := hex.DecodeString(value)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("--module-hash must be a 32-byte SHA-256 in hex")
	}
	return nil
}
