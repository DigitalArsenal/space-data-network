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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// accounts — the unified ACCOUNTS surface (§16)
// ---------------------------------------------------------------------------

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List and manage accounts (network peers and login operators are one thing)",
	Long: `List and manage accounts.

A node running somewhere and an account that logs in are the same kind of thing:
a PRR-shaped identity carrying a trust level (§16). 'accounts list' shows the
merged view; trust changes go to the peer or operator record explicitly, because
a merged row has two underlying records and guessing which one to edit would be
wrong.`,
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accounts (peers, operators, and identities that are both)",
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
	accountsAddPeerID    string
	accountsAddPublicKey string
	accountsAddVCardFile string
	accountsAddTrust     string
	accountsAddName      string
)

var accountsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a peer account by peer ID, public key, or vCard",
	Long: `Add a peer account.

Identity may be given as --peer-id or --public-key (a marshalled libp2p key, a
raw 32-byte ed25519 key, or a raw 33-byte compressed secp256k1 key, in hex or
base64). Supplying both is allowed only if they agree — a mismatch is refused
rather than resolved to either.

An xpub is NOT accepted: it identifies a wallet account, not a libp2p host.
Derive the peer ID from it client-side and pass --peer-id.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if accountsAddPeerID == "" && accountsAddPublicKey == "" && accountsAddVCardFile == "" {
			return fmt.Errorf("one of --peer-id, --public-key or --vcard is required")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}

		// A bare vCard goes to the dedicated import endpoint, which derives the
		// peer's EPM from the card when it can (§8).
		if accountsAddVCardFile != "" && accountsAddPeerID == "" && accountsAddPublicKey == "" {
			card, rerr := os.ReadFile(accountsAddVCardFile)
			if rerr != nil {
				return fmt.Errorf("read %s: %w", accountsAddVCardFile, rerr)
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "POST", "/api/peers/import/vcard", string(card), &out); err != nil {
				return err
			}
			return printJSON(cmd, out)
		}

		body := map[string]any{"trust_level": accountsAddTrust}
		if accountsAddPeerID != "" {
			body["peer_id"] = accountsAddPeerID
		}
		if accountsAddPublicKey != "" {
			body["public_key"] = accountsAddPublicKey
		}
		if accountsAddName != "" {
			body["name"] = accountsAddName
		}
		if accountsAddVCardFile != "" {
			card, rerr := os.ReadFile(accountsAddVCardFile)
			if rerr != nil {
				return fmt.Errorf("read %s: %w", accountsAddVCardFile, rerr)
			}
			body["vcard"] = string(card)
		}
		var out map[string]any
		if err := client.postJSON(cmd.Context(), "/api/peers", body, &out, ""); err != nil {
			return err
		}
		return printJSON(cmd, out)
	},
}

var (
	accountsTrustPeerID string
	accountsTrustXPub   string
	accountsTrustLevel  string
	accountsTrustName   string
)

var accountsTrustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Set the trust level of a peer (--peer-id) or an operator (--xpub)",
	Long: `Set a trust level.

A merged account row has TWO underlying records — a peer entry and an operator
entry — so the target is explicit rather than inferred:

  --peer-id <id>   sets peer trust      (PUT /api/peers/<id>/trust)
  --xpub <xpub>    sets operator trust  (PUT /api/auth/users/<xpub>)

Operator levels are capped at 'admin': 'ultimate' is reserved for the node's own
identity, and 'never' as an operator lockout is not supported.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if (accountsTrustPeerID == "") == (accountsTrustXPub == "") {
			return fmt.Errorf("exactly one of --peer-id or --xpub is required (a merged account has two records; say which)")
		}
		if strings.TrimSpace(accountsTrustLevel) == "" {
			return fmt.Errorf("--level is required (never|unknown|marginal|standard|full|admin|ultimate)")
		}
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var out map[string]any
		if accountsTrustPeerID != "" {
			body := map[string]any{"trust_level": accountsTrustLevel}
			if err := client.do(cmd.Context(), "PUT", "/api/peers/"+accountsTrustPeerID+"/trust", body, &out); err != nil {
				return err
			}
		} else {
			body := map[string]any{"xpub": accountsTrustXPub, "trust_level": accountsTrustLevel, "name": accountsTrustName}
			if err := client.do(cmd.Context(), "PUT", "/api/auth/users/"+accountsTrustXPub, body, &out); err != nil {
				return err
			}
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
	accountsAddCmd.Flags().StringVar(&accountsAddPeerID, "peer-id", "", "libp2p peer ID")
	accountsAddCmd.Flags().StringVar(&accountsAddPublicKey, "public-key", "", "public key (hex or base64)")
	accountsAddCmd.Flags().StringVar(&accountsAddVCardFile, "vcard", "", "path to a vCard file")
	accountsAddCmd.Flags().StringVar(&accountsAddTrust, "level", "standard", "trust level")
	accountsAddCmd.Flags().StringVar(&accountsAddName, "name", "", "display name")

	accountsTrustCmd.Flags().StringVar(&accountsTrustPeerID, "peer-id", "", "set PEER trust for this peer ID")
	accountsTrustCmd.Flags().StringVar(&accountsTrustXPub, "xpub", "", "set OPERATOR trust for this xpub")
	accountsTrustCmd.Flags().StringVar(&accountsTrustLevel, "level", "", "trust level")
	accountsTrustCmd.Flags().StringVar(&accountsTrustName, "name", "", "operator display name (with --xpub)")

	identitySetCmd.Flags().StringVar(&identitySetFile, "file", "", "JSON epm.Profile to write (WHOLE-PROFILE REPLACE)")
	identityGenKeyCmd.Flags().StringVar(&identityGenKeySlot, "slot", "", "key slot: signing|encryption")

	pubsubMessagesCmd.Flags().StringVar(&pubsubMessagesTopic, "topic", "", "filter by topic")
	pubsubPublishCmd.Flags().StringVar(&pubsubPublishTopic, "topic", "", "topic to publish to")
	pubsubPublishCmd.Flags().StringVar(&pubsubPublishFile, "file", "", "payload file")

	tablesQueryCmd.Flags().StringVar(&tablesQuerySQL, "sql", "", "query to run")
	tablesQueryCmd.Flags().StringVar(&tablesQueryProvider, "provider", "", "provider id")
	tablesQueryCmd.Flags().StringVar(&tablesQuerySchema, "schema", "", "SDS schema (e.g. OMM.fbs)")
	tablesQueryCmd.Flags().IntVar(&tablesQueryLimit, "limit", 0, "max rows")

	accountsCmd.AddCommand(accountsListCmd, accountsAddCmd, accountsTrustCmd)
	identityCmd.AddCommand(identitySetCmd, identityKeysCmd, identityGenKeyCmd)
	pubsubCmd.AddCommand(pubsubTopicsCmd, pubsubMessagesCmd, pubsubPublishCmd)
	tablesCmd.AddCommand(tablesListCmd, tablesQueryCmd)

	// Every Admin-gated command gets the remote/non-root override, uniformly.
	for _, c := range []*cobra.Command{
		accountsListCmd, accountsAddCmd, accountsTrustCmd,
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
