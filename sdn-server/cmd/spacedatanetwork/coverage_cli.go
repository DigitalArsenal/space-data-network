package main

// The CLI coverage the owner asked for: "there needs to be CLI methods for
// everything we are doing in addition to Kubo, including the data stuff like
// pub/sub and per-node per-standard tables, etc."
//
// CONNECTORS ONLY. Every command here drives an endpoint the daemon already
// serves. Nothing computes, stores, or decides anything the HTTP surface does
// not already do — the CLI is the missing wire, exactly as `key export` was.
//
// Every Admin-gated command goes through adminClient, which signs in as the
// node's root account through the real challenge/verify admit point (§19). None
// of them bypass the auth wall.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// accounts — local login accounts and the node root wallet
// ---------------------------------------------------------------------------

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List, add, modify, and remove accounts that can log in to this node",
	Long: `List and manage accounts that can log in to the local node (plus the node's own root wallet).

Network peers are managed separately; 'accounts' covers only local login accounts
and the node root wallet itself.`,
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List login accounts and the node root wallet",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := client.get(cmd.Context(), "/api/accounts", &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var (
	accountsAddXPub          string
	accountsAddSigningPubKey string
	accountsAddTrust         string
	accountsAddName          string
)

var accountsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a login account to this node",
	Long: `Enroll a login account on this node (POST /api/auth/users).

Provide --xpub and/or --signing-pubkey (64-char hex Ed25519 public key).
When --xpub is omitted, the account row key defaults to pubkey:<signing-pubkey>.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		xpub := strings.TrimSpace(accountsAddXPub)
		sigKey := strings.TrimSpace(accountsAddSigningPubKey)
		if xpub == "" && sigKey == "" {
			return fmt.Errorf("at least one of --xpub or --signing-pubkey is required")
		}
		if xpub == "" {
			xpub = "pubkey:" + strings.ToLower(sigKey)
		}
		trust := strings.TrimSpace(accountsAddTrust)
		if trust == "" {
			trust = "standard"
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{
			"xpub":        xpub,
			"trust_level": trust,
		}
		if name := strings.TrimSpace(accountsAddName); name != "" {
			body["name"] = name
		}
		if sigKey != "" {
			body["signing_pubkey_hex"] = sigKey
		}
		var out map[string]any
		if err := client.postJSON(cmd.Context(), "/api/auth/users", body, &out, ""); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var (
	accountsModifyXPub          string
	accountsModifyLevel         string
	accountsModifyName          string
	accountsModifySigningPubKey string
)

func resolveAccountTarget(ctx context.Context, client *adminClient, selector string) (*accountEntry, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("account identifier (--xpub or positional name / xpub / signing key) is required")
	}
	var resp struct {
		Accounts []accountEntry `json:"accounts"`
	}
	if err := client.get(ctx, "/api/accounts", &resp); err != nil {
		return nil, err
	}
	lower := strings.ToLower(selector)
	var matches []accountEntry
	for _, a := range resp.Accounts {
		if a.XPub == selector ||
			strings.EqualFold(a.SigningPubKeyHex, lower) ||
			strings.EqualFold(strings.TrimSpace(a.Name), selector) ||
			(a.PeerID != "" && a.PeerID == selector) {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		// Allow direct xpub addressing even if not returned (e.g. server decides 404).
		return &accountEntry{XPub: selector, Kind: accountKindOperator}, nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("ambiguous account %q matched %d rows; pass the exact --xpub", selector, len(matches))
	}
	if matches[0].Kind == accountKindRoot {
		return nil, fmt.Errorf("the node root wallet cannot be modified or removed")
	}
	return &matches[0], nil
}

var accountsModifyCmd = &cobra.Command{
	Use:   "modify [xpub | name | signing-key]",
	Short: "Modify a login account's trust level, name, or signing key",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		selector := strings.TrimSpace(accountsModifyXPub)
		if selector == "" && len(args) == 1 {
			selector = strings.TrimSpace(args[0])
		}
		if selector == "" {
			return fmt.Errorf("specify the account via --xpub or a positional argument")
		}
		level := strings.TrimSpace(accountsModifyLevel)
		name := strings.TrimSpace(accountsModifyName)
		sigKey := strings.TrimSpace(accountsModifySigningPubKey)
		if level == "" && name == "" && sigKey == "" {
			return fmt.Errorf("specify at least one of --level, --name, or --signing-pubkey to modify")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		target, err := resolveAccountTarget(cmd.Context(), client, selector)
		if err != nil {
			return err
		}
		if level == "" {
			if target.TrustLevel.String() != "" {
				level = target.TrustLevel.String()
			} else {
				return fmt.Errorf("--level is required when the existing trust level cannot be resolved")
			}
		}
		body := map[string]any{
			"xpub":        target.XPub,
			"trust_level": level,
		}
		if name != "" {
			body["name"] = name
		}
		if sigKey != "" {
			body["signing_pubkey_hex"] = sigKey
		}
		var out map[string]any
		if err := client.do(cmd.Context(), "PUT", "/api/auth/users/"+url.PathEscape(target.XPub), body, &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var accountsRemoveXPub string

var accountsRemoveCmd = &cobra.Command{
	Use:   "remove [xpub | name | signing-key]",
	Short: "Remove a login account from this node",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		selector := strings.TrimSpace(accountsRemoveXPub)
		if selector == "" && len(args) == 1 {
			selector = strings.TrimSpace(args[0])
		}
		if selector == "" {
			return fmt.Errorf("specify the account via --xpub or a positional argument")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		target, err := resolveAccountTarget(cmd.Context(), client, selector)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := client.do(cmd.Context(), "DELETE", "/api/auth/users/"+url.PathEscape(target.XPub), nil, &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

// ---------------------------------------------------------------------------
// identity set / keys — node EPM edit (§6, §18)
// ---------------------------------------------------------------------------

var identitySetFile string

var identitySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace the node's EPM profile from a JSON file (Admin)",
	Long: `Replace the node's EPM profile.

⚠ PUT /api/node/epm is a WHOLE-PROFILE REPLACE: every key you omit is a DELETE,
not "leave unchanged". Read the current profile first, edit it, and send it back
whole:

  spacedatanetwork identity export --format json > profile.json
  # edit, keeping every field you want to keep
  spacedatanetwork identity set --file profile.json

photo_data_url bites hardest — it lives only in the stored profile, so omitting
it silently drops the node's photo.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(identitySetFile) == "" {
			return fmt.Errorf("--file is required (a JSON epm.Profile)")
		}
		payload, err := os.ReadFile(identitySetFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", identitySetFile, err)
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var profile epm.Profile
		if err := json.Unmarshal(payload, &profile); err != nil {
			return fmt.Errorf("parse %s as an epm.Profile: %w", identitySetFile, err)
		}
		wire, err := epm.EncodeProfileEPM(&profile)
		if err != nil {
			return fmt.Errorf("encode EPM: %w", err)
		}
		if err := client.putRaw(cmd.Context(), "/api/node/epm", epm.EPMContentType, wire); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "profile replaced; EPM re-signed and republished")
		return nil
	},
}

var identityKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Show the node's derivation-path key slots and their GEN KEY proposals",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := client.get(cmd.Context(), "/api/node/epm/keys", &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var identityGenKeySlot string

var identityGenKeyCmd = &cobra.Command{
	Use:   "gen-key",
	Short: "Propose the next derivation path for a key slot (does NOT save)",
	Long: `Propose the next derivation path for a key slot.

This is the GEN KEY action. It returns a PATH, never key material: for the
xpub-derivable slots the public key is a pure function of xpub + path, and the
private key never leaves the node's seed.

It does NOT apply the proposal. Save it deliberately with 'identity set', so a
single command can never silently republish the node's identity.

Only the secp256k1 signing/encryption slots are rotatable. The ed25519
record-signing key is not offered: it is not xpub-derivable, and rotating it is
a signing-identity change rather than a path relabel.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(identityGenKeySlot) == "" {
			return fmt.Errorf("--slot is required (signing|encryption)")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := client.postJSON(cmd.Context(), "/api/node/epm/keys",
			map[string]any{"slot": identityGenKeySlot}, &out, ""); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

// ---------------------------------------------------------------------------
// pubsub — the node's topics (distinct from `channels`, which is private streams)
// ---------------------------------------------------------------------------

var pubsubCmd = &cobra.Command{
	Use:   "pubsub",
	Short: "Inspect and use the node's pubsub topics",
	Long: `Inspect and use the node's pubsub topics.

This is a DIFFERENT surface from 'channels', which manages private channel
streams. Live topic names carry .fbs suffixes; they are wire identifiers and are
not renamed.`,
}

var pubsubTopicsCmd = &cobra.Command{
	Use:   "topics",
	Short: "List the node's pubsub topics",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out any
		if err := client.get(cmd.Context(), "/api/v1/pubsub/topics", &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var pubsubMessagesTopic string

var pubsubMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Read recent pubsub messages",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		path := "/api/v1/pubsub/messages"
		if t := strings.TrimSpace(pubsubMessagesTopic); t != "" {
			path += "?topic=" + t
		}
		var out any
		if err := client.get(cmd.Context(), path, &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var (
	pubsubPublishTopic string
	pubsubPublishFile  string
)

var pubsubPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish a message to a pubsub topic",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(pubsubPublishTopic) == "" {
			return fmt.Errorf("--topic is required")
		}
		if strings.TrimSpace(pubsubPublishFile) == "" {
			return fmt.Errorf("--file is required (the message payload)")
		}
		payload, err := os.ReadFile(pubsubPublishFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", pubsubPublishFile, err)
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out any
		if err := client.postJSON(cmd.Context(), "/api/v1/pubsub/publish",
			map[string]any{"topic": pubsubPublishTopic, "data": string(payload)}, &out, ""); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

// ---------------------------------------------------------------------------
// tables — per-node per-standard FlatSQL tables (the owner named this first)
// ---------------------------------------------------------------------------

var tablesCmd = &cobra.Command{
	Use:   "tables",
	Short: "List and query the node's per-provider, per-standard data tables",
	Long: `List and query the node's per-provider, per-standard data tables.

'tables list' reports the datastores the node holds — one per provider per SDS
type. 'tables query' runs a query through the unified data endpoint, which is
the same surface the dashboard uses.`,
}

var tablesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the node's datastores (per provider, per standard)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out any
		if err := client.get(cmd.Context(), "/api/v1/data/datastores", &out); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var (
	tablesQuerySQL      string
	tablesQueryProvider string
	tablesQuerySchema   string
	tablesQueryLimit    int
)

var tablesQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Query a per-provider, per-standard table",
	RunE: func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		if s := strings.TrimSpace(tablesQuerySQL); s != "" {
			body["query"] = s
		}
		if p := strings.TrimSpace(tablesQueryProvider); p != "" {
			body["provider"] = p
		}
		if s := strings.TrimSpace(tablesQuerySchema); s != "" {
			body["schema"] = s
		}
		if tablesQueryLimit > 0 {
			body["limit"] = tablesQueryLimit
		}
		if len(body) == 0 {
			return fmt.Errorf("one of --sql, --provider or --schema is required")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out any
		if err := client.postJSON(cmd.Context(), "/api/v1/data/query", body, &out, ""); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

// jsonRaw carries an already-encoded JSON document through the client without
// re-encoding it (which would double-encode it as a string).
type jsonRaw []byte

func (r jsonRaw) MarshalJSON() ([]byte, error) { return r, nil }

func init() {
	accountsAddCmd.Flags().StringVar(&accountsAddXPub, "xpub", "", "account xpub (optional if --signing-pubkey is provided)")
	accountsAddCmd.Flags().StringVar(&accountsAddSigningPubKey, "signing-pubkey", "", "Ed25519 signing public key (64-char hex)")
	accountsAddCmd.Flags().StringVar(&accountsAddTrust, "level", "standard", "trust level (unknown|marginal|standard|full|admin)")
	accountsAddCmd.Flags().StringVar(&accountsAddName, "name", "", "display name")

	accountsModifyCmd.Flags().StringVar(&accountsModifyXPub, "xpub", "", "account xpub, name, or signing public key")
	accountsModifyCmd.Flags().StringVar(&accountsModifyLevel, "level", "", "new trust level (unknown|marginal|standard|full|admin)")
	accountsModifyCmd.Flags().StringVar(&accountsModifyName, "name", "", "new display name")
	accountsModifyCmd.Flags().StringVar(&accountsModifySigningPubKey, "signing-pubkey", "", "new Ed25519 signing public key (64-char hex)")

	accountsRemoveCmd.Flags().StringVar(&accountsRemoveXPub, "xpub", "", "account xpub, name, or signing public key")

	identitySetCmd.Flags().StringVar(&identitySetFile, "file", "", "JSON epm.Profile to write (WHOLE-PROFILE REPLACE)")
	identityGenKeyCmd.Flags().StringVar(&identityGenKeySlot, "slot", "", "key slot: signing|encryption")

	pubsubMessagesCmd.Flags().StringVar(&pubsubMessagesTopic, "topic", "", "filter by topic")
	pubsubPublishCmd.Flags().StringVar(&pubsubPublishTopic, "topic", "", "topic to publish to")
	pubsubPublishCmd.Flags().StringVar(&pubsubPublishFile, "file", "", "payload file")

	tablesQueryCmd.Flags().StringVar(&tablesQuerySQL, "sql", "", "query to run")
	tablesQueryCmd.Flags().StringVar(&tablesQueryProvider, "provider", "", "provider id")
	tablesQueryCmd.Flags().StringVar(&tablesQuerySchema, "schema", "", "SDS schema (e.g. OMM.fbs)")
	tablesQueryCmd.Flags().IntVar(&tablesQueryLimit, "limit", 0, "max rows")

	accountsCmd.AddCommand(accountsListCmd, accountsAddCmd, accountsModifyCmd, accountsRemoveCmd)
	identityCmd.AddCommand(identitySetCmd, identityKeysCmd, identityGenKeyCmd)
	pubsubCmd.AddCommand(pubsubTopicsCmd, pubsubMessagesCmd, pubsubPublishCmd)
	tablesCmd.AddCommand(tablesListCmd, tablesQueryCmd)

	// Every Admin-gated command gets the remote/non-root override, uniformly.
	for _, c := range []*cobra.Command{
		accountsListCmd, accountsAddCmd, accountsModifyCmd, accountsRemoveCmd,
		identitySetCmd, identityKeysCmd, identityGenKeyCmd,
		pubsubTopicsCmd, pubsubMessagesCmd, pubsubPublishCmd,
		tablesListCmd, tablesQueryCmd,
	} {
		addSessionTokenFlag(c)
	}

	rootCmd.AddCommand(accountsCmd)
	rootCmd.AddCommand(pubsubCmd)
	rootCmd.AddCommand(tablesCmd)
}
