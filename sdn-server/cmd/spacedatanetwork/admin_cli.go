package main

// `spacedatanetwork admin init` — enroll a node admin from the server itself,
// so a remote node can be administered from the browser dashboard with the
// same credentials. The wizard lives in internal/admincli; this file is the
// wiring: the node's own keys, the HD wallet module, and where the admin row
// lands (the running daemon's user API, or auth.db when the daemon is down).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spacedatanetwork/sdn-server/internal/admincli"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
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

func init() {
	f := adminInitCmd.Flags()
	f.StringVar(&adminInitName, "name", "", "admin display name")
	f.StringVar(&adminInitSource, "source", "", "identity source: password, mnemonic or pubkey")
	f.StringVar(&adminInitUsername, "username", "", "username (password source)")
	f.StringVar(&adminInitPublicKey, "pubkey", "", "64-hex Ed25519 sign-in public key (pubkey source)")
	f.BoolVar(&adminInitSecretStdin, "secret-stdin", false, "read the password or recovery phrase as one line of stdin")
	f.BoolVar(&adminInitYes, "yes", false, "enroll without the final confirmation")
	addSessionTokenFlag(adminInitCmd)
	adminCmd.AddCommand(adminInitCmd)
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
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	keys, err := adminServerKeys(ctx, cfg)
	if err != nil {
		return err
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

	store, closeStore, err := adminStore(cmd, cfg)
	if err != nil {
		return err
	}
	defer closeStore()

	terminal := admincli.Terminal{In: bufio.NewReader(os.Stdin), Out: os.Stderr}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		terminal.ReadSecret = func(prompt string) ([]byte, error) {
			fmt.Fprint(os.Stderr, prompt)
			secret, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return secret, err
		}
	}

	_, err = admincli.Init(ctx, admincli.Options{
		Name:            adminInitName,
		Source:          source,
		Username:        adminInitUsername,
		PublicKey:       adminInitPublicKey,
		SecretFromStdin: adminInitSecretStdin,
		Yes:             adminInitYes,
	}, terminal, keys, walletderive.HDWallet{HDWalletModule: hw}, store)
	return err
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
func adminStore(cmd *cobra.Command, cfg *config.Config) (admincli.Store, func(), error) {
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
