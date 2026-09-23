package main

// `spacedatanetwork admin init` — enroll a node admin from the server itself,
// so a remote node can be administered from the browser dashboard with the
// same credentials. The wizard lives in internal/admincli; this file is the
// wiring: the node's own keys, the HD wallet module, and where the admin row
// lands (the running daemon's user API, or auth.db when the daemon is down).

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/admincli"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sealed"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Enroll and manage the admins who sign in to this node's dashboard",
}

var (
	adminInitName        string
	adminInitSource      string
	adminInitUsername    string
	adminInitPublicKey   string
	adminInitSecretStdin bool
	adminInitYes         bool
)

var adminInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Print this node's keys and enroll an admin",
	Long: `Print this node's public keys, then enroll an admin who signs in from the
browser dashboard.

The admin's identity comes from one of:
  password   a username and password; sign in with the same pair
  mnemonic   a BIP-39 recovery phrase; sign in with the same phrase
  pubkey     the sign-in public key of a wallet made elsewhere

Passwords and phrases are read without echo and never stored: only the derived
public sign-in key is. With the daemon running the admin is added through its
API; with the daemon stopped it is written to the auth database directly.

Non-interactive use:
  spacedatanetwork admin init --name Ops --source pubkey --pubkey <hex> --yes
  printf '%s\n' "$PASSWORD" | spacedatanetwork admin init --name Ops \
      --source password --username ops --secret-stdin --yes`,
	RunE: runAdminInit,
}

var adminAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Enroll another admin (same sources and flags as init)",
	RunE:  runAdminInit,
}

var adminListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the accounts that can sign in to this node, admins first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withAdminStore(cmd, func(ctx context.Context, store admincli.ManageStore) error {
			accounts, err := store.List(ctx)
			if err != nil {
				return err
			}
			admincli.PrintAccounts(os.Stdout, accounts)
			return nil
		})
	},
}

var adminManageYes bool

var adminRemoveCmd = &cobra.Command{
	Use:   "remove <name | sign-in key | account>",
	Short: "Remove an account (the last admin cannot be removed)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withAdminStore(cmd, func(ctx context.Context, store admincli.ManageStore) error {
			return admincli.Remove(ctx, adminTerminal(), store, args[0], adminManageYes)
		})
	},
}

var adminSetTrustCmd = &cobra.Command{
	Use:   "set-trust <name | sign-in key | account> <unknown|marginal|standard|full|admin>",
	Short: "Change an account's trust (the last admin cannot be demoted)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withAdminStore(cmd, func(ctx context.Context, store admincli.ManageStore) error {
			return admincli.SetTrust(ctx, adminTerminal(), store, args[0], args[1], adminManageYes)
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{adminRemoveCmd, adminSetTrustCmd} {
		c.Flags().BoolVar(&adminManageYes, "yes", false, "skip the confirmation")
	}
	for _, c := range []*cobra.Command{adminListCmd, adminRemoveCmd, adminSetTrustCmd} {
		addSessionTokenFlag(c)
		adminCmd.AddCommand(c)
	}
}

func init() {
	f := adminInitCmd.Flags()
	f.StringVar(&adminInitName, "name", "", "admin display name")
	f.StringVar(&adminInitSource, "source", "", "identity source: password, mnemonic or pubkey")
	f.StringVar(&adminInitUsername, "username", "", "username (password source)")
	f.StringVar(&adminInitPublicKey, "pubkey", "", "64-hex Ed25519 sign-in public key (pubkey source)")
	f.BoolVar(&adminInitSecretStdin, "secret-stdin", false, "read the password or recovery phrase as one line of stdin")
	f.BoolVar(&adminInitYes, "yes", false, "enroll without the final confirmation")
	adminAddCmd.Flags().AddFlagSet(f)
	addSessionTokenFlag(adminInitCmd)
	addSessionTokenFlag(adminAddCmd)
	adminCmd.AddCommand(adminInitCmd, adminAddCmd)
	rootCmd.AddCommand(adminCmd)
}

func runAdminInit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	source, err := admincli.ParseSource(adminInitSource)
	if err != nil {
		return err
	}
	var (
		cfg  *config.Config
		keys admincli.ServerKeys
	)
	if remoteRequested() {
		// The node being administered is the remote one: show its keys, as
		// checked against --remote-fingerprint.
		node, err := sealed.ReadNodeKeys(ctx, &http.Client{Timeout: 30 * time.Second}, remoteURL, remoteFingerprint)
		if err != nil {
			return err
		}
		keys = admincli.ServerKeys{
			PeerID:            strings.TrimRight(remoteURL, "/"),
			SigningPubKeyHex:  hex.EncodeToString(node.SigningKey),
			SigningKeyPath:    "remote",
			EncryptionPubHex:  hex.EncodeToString(node.EncryptionKey),
			EncryptionKeyPath: "remote",
		}
	} else {
		cfg, _, err = config.LoadResolved(configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if keys, err = adminServerKeys(ctx, cfg); err != nil {
			return err
		}
	}

	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	var store admincli.Store
	if remoteRequested() {
		client, err := newRemoteAdminClient(cmd)
		if err != nil {
			return err
		}
		store = daemonAdminStore{client: client}
	} else {
		local, closeStore, err := adminStore(cmd, cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		store = local
	}

	_, err = admincli.Init(ctx, admincli.Options{
		Name:            adminInitName,
		Source:          source,
		Username:        adminInitUsername,
		PublicKey:       adminInitPublicKey,
		SecretFromStdin: adminInitSecretStdin,
		Yes:             adminInitYes,
	}, adminTerminal(), keys, walletderive.HDWallet{HDWalletModule: hw}, store)
	return err
}

// adminTerminal is the operator's console: prompts on stderr, and hidden
// input when stdin is a terminal.
func adminTerminal() admincli.Terminal {
	terminal := admincli.Terminal{In: bufio.NewReader(os.Stdin), Out: os.Stderr}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		terminal.ReadSecret = func(prompt string) ([]byte, error) {
			fmt.Fprint(os.Stderr, prompt)
			secret, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return secret, err
		}
	}
	return terminal
}

// withAdminStore runs fn against the daemon, or auth.db when it is down.
func withAdminStore(cmd *cobra.Command, fn func(context.Context, admincli.ManageStore) error) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if remoteRequested() {
		client, err := newRemoteAdminClient(cmd)
		if err != nil {
			return err
		}
		return fn(ctx, daemonAdminStore{client: client})
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	store, closeStore, err := adminStore(cmd, cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	return fn(ctx, store)
}

// adminServerKeys derives the node's public keys the way show-identity does,
// including the encryption key the node advertises on its card.
func adminServerKeys(ctx context.Context, cfg *config.Config) (admincli.ServerKeys, error) {
	node, err := loadIdentityWizardNodeIdentity(ctx, cfg)
	if err != nil {
		return admincli.ServerKeys{}, err
	}
	info := node.Identity.Info()
	keys := admincli.ServerKeys{
		PeerID:           info.PeerID,
		SigningPubKeyHex: info.SigningPubKeyHex,
		SigningKeyPath:   info.SigningKeyPath,
	}
	var profile *epm.Profile
	if p, err := epm.LoadProfile(cfg.Storage.Path); err == nil {
		profile = p
	}
	if encPub, encPath, ok := epm.AdvertisedEncryptionKey(node.XPub, 0, profile); ok {
		keys.EncryptionPubHex, keys.EncryptionKeyPath = encPub, encPath
	}
	return keys, nil
}

// adminStore enrolls through the running daemon when there is one. Only a
// refused connection — nothing listening — falls back to opening auth.db
// directly, so a live daemon's database is never opened underneath it.
func adminStore(cmd *cobra.Command, cfg *config.Config) (admincli.ManageStore, func(), error) {
	client, err := newAdminClient(cmd)
	if err == nil {
		return daemonAdminStore{client: client}, func() {}, nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, nil, err
	}
	path, err := resolveAuthDBPath(cfg)
	if err != nil {
		return nil, nil, err
	}
	users, err := auth.NewUserStore(path, cfg.Users)
	if err != nil {
		return nil, nil, err
	}
	return admincli.AuthDBStore{Users: users, Path: path}, func() { users.Close() }, nil
}

// daemonAdminStore enrolls through the daemon's admin-only user API.
type daemonAdminStore struct{ client *adminClient }

func (s daemonAdminStore) users(ctx context.Context) ([]auth.User, error) {
	var users []auth.User
	if err := s.client.get(ctx, "/api/auth/users", &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (s daemonAdminStore) HasAdmin(ctx context.Context) (bool, error) {
	users, err := s.users(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.TrustLevel >= peers.Admin {
			return true, nil
		}
	}
	return false, nil
}

func (s daemonAdminStore) AddUser(ctx context.Context, rowKey, name string, trust peers.TrustLevel, signingPubKeyHex string) error {
	users, err := s.users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if strings.EqualFold(u.SigningPubKeyHex, signingPubKeyHex) {
			return fmt.Errorf("this sign-in key is already enrolled as %q", u.Name)
		}
	}
	return s.client.do(ctx, "POST", "/api/auth/users", map[string]string{
		"xpub":               rowKey,
		"name":               name,
		"trust_level":        trust.String(),
		"signing_pubkey_hex": signingPubKeyHex,
	}, nil)
}

func (s daemonAdminStore) Describe() string { return "running daemon at " + s.client.baseURL }

func (s daemonAdminStore) List(ctx context.Context) ([]admincli.Account, error) {
	users, err := s.users(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]admincli.Account, 0, len(users))
	for _, u := range users {
		accounts = append(accounts, admincli.Account{RowKey: u.XPub, Name: u.Name, Trust: u.TrustLevel, SigningPubKeyHex: u.SigningPubKeyHex, Source: u.Source})
	}
	return accounts, nil
}

func (s daemonAdminStore) Remove(ctx context.Context, rowKey string) error {
	return s.client.do(ctx, "DELETE", "/api/auth/users/"+url.PathEscape(rowKey), nil, nil)
}

func (s daemonAdminStore) SetTrust(ctx context.Context, rowKey string, trust peers.TrustLevel) error {
	return s.client.do(ctx, "PUT", "/api/auth/users/"+url.PathEscape(rowKey), map[string]string{"trust_level": trust.String()}, nil)
}
