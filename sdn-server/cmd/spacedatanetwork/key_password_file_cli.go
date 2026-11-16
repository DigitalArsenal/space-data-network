package main

// key_password_file_cli.go — a FRESH node on a host whose machine key is
// refused gets a key password file instead of a failed first start.
//
// The at-rest key of a node with no key password is derived from the machine
// and the account it runs as (internal/keys, scheme v4). On most Linux hosts
// the kernel creates /sys/class/dmi/id/product_uuid with mode 0400, so for
// every account but root that derivation is REFUSED — by design: a key that
// depends on the privilege of the reading process forked the key space once,
// and v4 exists to stop that. Until now the answer was a documented manual
// step (make a random file, point SDN_KEY_PASSWORD_FILE at it) that every
// non-root install had to take before its first start: install.sh as a user,
// the .deb/.rpm service (user sdn), the container (uid 1000).
//
// The installers now take that step themselves, for a FRESH node only, through
// the three commands here:
//
//	key fingerprint-check     ok | absent | refused, for this account
//	key fresh-password-file   create the file, only when the node is fresh
//	                          and its fingerprint is refused
//	init --key-password-file  record the file in the node's config
//
// A node that already has an identity is never touched: its identity stays
// sealed under whatever it was sealed under, and nothing here changes v4.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spf13/cobra"
)

var keyFingerprintCheckCmd = &cobra.Command{
	Use:   "fingerprint-check",
	Short: "Say whether this account can derive the machine at-rest key: ok, absent or refused",
	Long: `Report whether a node running as this account could seal its identity under
the machine-derived at-rest key, without deriving anything (no key is built and
no source value is printed).

stdout is one word, for scripts:

  ok       every source was read; the machine key is available
  absent   the machine key is available; the platform identifier does not
           exist on this machine and is recorded as absent
  refused  the machine key is refused for this account (on Linux, typically
           /sys/class/dmi/id/product_uuid, which the kernel makes root-only)

The reason goes to stderr. A node that is refused needs a key password before
its first start: SDN_KEY_PASSWORD_FILE, or security.key_password_file in its
config. The installers create one automatically for a fresh node.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		check := keys.CheckMachineFingerprint()
		fmt.Fprintln(cmd.OutOrStdout(), check.Status)
		errOut := cmd.ErrOrStderr()
		fmt.Fprintln(errOut, check.Reason)
		if check.Why != "" {
			fmt.Fprintln(errOut, "why: "+check.Why)
		}
		if check.Status == keys.FingerprintRefused {
			fmt.Fprintln(errOut, "a node running as this account needs SDN_KEY_PASSWORD_FILE (or security.key_password_file) before its first start")
		}
		return nil
	},
}

var keyFreshPasswordFileCmd = &cobra.Command{
	Use:   "fresh-password-file PATH",
	Short: "Create a key password file for a fresh node whose machine fingerprint is refused",
	Long: `For installers. Only for a FRESH node — no identity in its key directory and no
key password configured (SDN_KEY_PASSWORD, SDN_KEY_PASSWORD_FILE,
security.key_password, security.key_password_file) — and only when this
account's machine fingerprint is refused (see 'key fingerprint-check'): create
PATH holding 32 random bytes as 64 hex characters, mode 0400, for the node to
seal its new identity under. An existing PATH is never overwritten.

Prints one line: "created PATH", "exists PATH" (an owner-only file already
there, kept as it is), or "skipped: <reason>". Nothing else changes: the caller
records PATH where the node will find it — install.sh with
'init --key-password-file', the .deb/.rpm with a systemd drop-in, the container
with SDN_KEY_PASSWORD_FILE.

--key-dir-within DIR (the container: /app/data, its one data volume) skips
when the node's key directory is not inside DIR: PATH would then be kept
somewhere the identity is not, and lost apart from it.

Keep the file for the life of the node and back it up apart from the node's
data: the identity cannot be opened without it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Plain Load, not LoadResolved: this command asks whether a key
		// password is configured, and LoadResolved exports the config's own
		// security.key_password_file into the environment first.
		res, err := config.ResolvePath(configPath)
		if err != nil {
			return err
		}
		cfg, err := config.Load(res.Path)
		if err != nil {
			return fmt.Errorf("load config %s: %w", res.Describe(), err)
		}
		if within := strings.TrimSpace(freshKeyDirWithin); within != "" {
			if outcome, inside := keyDirWithin(cfg, within); !inside {
				fmt.Fprintln(cmd.OutOrStdout(), outcome)
				return nil
			}
		}
		outcome, err := ensureFreshKeyPasswordFile(cfg, args[0], keys.CheckMachineFingerprint)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), outcome)
		return nil
	},
}

var (
	initKeyPasswordFile string
	freshKeyDirWithin   string
)

// keyDirWithin reports whether the node's key directory is inside dir, and
// the skip outcome when it is not.
//
// The container keeps its generated file in its data volume
// (/app/data/key-password). A config whose storage lives outside /app/data
// (an operator's own volume mounted elsewhere) would put the identity there
// and the password in /app/data — an anonymous volume that the next `docker
// run` does not reuse. The next container then finds an identity, no file,
// and an identity it cannot open. So nothing is generated, and the node's
// first start says how to give it a password file.
func keyDirWithin(cfg *config.Config, dir string) (freshPasswordFileOutcome, bool) {
	keyDir, kerr := filepath.Abs(config.KeyDir(cfg))
	base, berr := filepath.Abs(dir)
	if kerr == nil && berr == nil {
		if rel, err := filepath.Rel(base, keyDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return freshPasswordFileOutcome{}, true
		}
	}
	return skipped("this node's key directory %s is not inside %s, where the file would be kept; "+
		"give the node a key password file of your own (SDN_KEY_PASSWORD_FILE)", config.KeyDir(cfg), dir), false
}

func init() {
	keyCmd.AddCommand(keyFingerprintCheckCmd)
	keyCmd.AddCommand(keyFreshPasswordFileCmd)
	keyFreshPasswordFileCmd.Flags().StringVar(&freshKeyDirWithin, "key-dir-within", "",
		"create nothing unless the node's key directory is inside this directory (the container: its data volume)")
	initCmd.Flags().StringVar(&initKeyPasswordFile, "key-password-file", "",
		"record this absolute path as security.key_password_file and seal a new identity under it "+
			"(an existing identity must already be sealed under it)")
}

// freshPasswordFileOutcome is what 'key fresh-password-file' did.
type freshPasswordFileOutcome struct {
	// Action is "created", "exists" or "skipped".
	Action string
	Path   string
	Reason string
}

func (o freshPasswordFileOutcome) String() string {
	if o.Action == "skipped" {
		return "skipped: " + o.Reason
	}
	return o.Action + " " + o.Path
}

func skipped(format string, args ...any) freshPasswordFileOutcome {
	return freshPasswordFileOutcome{Action: "skipped", Reason: fmt.Sprintf(format, args...)}
}

// ensureFreshKeyPasswordFile is 'key fresh-password-file'. Every condition is
// a reason to leave the node alone, checked before anything is written:
//
//  1. a key password is already configured (environment or config);
//  2. the node already has an identity (its key directory, or
//     SDN_MNEMONIC_FILE) — whatever it is sealed under stays as it is;
//  3. the machine fingerprint is not refused for this account (ok or
//     absent): the machine key works and the node needs no file.
//
// Only then is PATH created — or, if it already exists, kept and reported.
func ensureFreshKeyPasswordFile(cfg *config.Config, path string, check func() keys.FingerprintCheck) (freshPasswordFileOutcome, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return freshPasswordFileOutcome{}, fmt.Errorf("key password file %q must be an absolute path", path)
	}
	if cfg == nil {
		return freshPasswordFileOutcome{}, errors.New("config is required")
	}
	if config.KeyPasswordConfigured(cfg) {
		return skipped("a key password is already configured for this node"), nil
	}
	keyDir := config.KeyDir(cfg)
	if keys.HasSealedIdentity(keyDir) {
		return skipped("this node already has an identity in %s; it stays sealed as it is", keyDir), nil
	}
	if mnemonicPath := strings.TrimSpace(config.MnemonicPathResolved(cfg)); mnemonicPath != "" {
		if _, err := os.Lstat(mnemonicPath); err == nil || !os.IsNotExist(err) {
			return skipped("this node already has an identity at %s; it stays sealed as it is", mnemonicPath), nil
		}
	}
	if fc := check(); fc.Status != keys.FingerprintRefused {
		return skipped("this account's machine fingerprint is %s; the node needs no key password file", fc.Status), nil
	}

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		// Never overwritten. It must still be usable, or the node would fail
		// later with a less direct error.
		if !info.Mode().IsRegular() {
			return freshPasswordFileOutcome{}, fmt.Errorf("%s exists and is not a regular file", path)
		}
		if perr := keys.CheckKeyFilePermissions(path); perr != nil {
			return freshPasswordFileOutcome{}, perr
		}
		if info.Size() == 0 {
			return freshPasswordFileOutcome{}, fmt.Errorf("%s exists and is empty", path)
		}
		return freshPasswordFileOutcome{Action: "exists", Path: path}, nil
	case !os.IsNotExist(err):
		return freshPasswordFileOutcome{}, fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := writeNewKeyPasswordFile(path, rand.Reader); err != nil {
		return freshPasswordFileOutcome{}, err
	}
	return freshPasswordFileOutcome{Action: "created", Path: path}, nil
}

// keyPasswordFileBytes is the random material in a generated password file;
// the file holds it hex-encoded (64 characters, no newline).
const keyPasswordFileBytes = 32

// writeNewKeyPasswordFile creates path exclusively (O_EXCL: never over an
// existing file, even one that appeared since the caller looked), mode 0400,
// holding keyPasswordFileBytes random bytes as hex. A partial file is removed.
func writeNewKeyPasswordFile(path string, random io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	raw := make([]byte, keyPasswordFileBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return fmt.Errorf("read random bytes: %w", err)
	}
	secret := []byte(hex.EncodeToString(raw))
	for i := range raw {
		raw[i] = 0
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, werr := f.Write(secret)
	for i := range secret {
		secret[i] = 0
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		// O_CREATE's mode passes through the umask; set it outright.
		werr = os.Chmod(path, 0o400)
	}
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", path, werr)
	}
	return nil
}

// adoptKeyPasswordFile is 'init --key-password-file PATH': it records PATH as
// security.key_password_file in cfg (the caller saves cfg) and exports it for
// this process, so the identity init creates is sealed under it.
//
// It refuses anything that would leave the node unable to open its identity
// later: another password source configured, a different file already
// recorded, or an EXISTING identity that PATH does not open (re-sealing an
// identity is 'key reseal', a deliberate operator step).
func adoptKeyPasswordFile(cfg *config.Config, path string) error {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return fmt.Errorf("--key-password-file %q must be an absolute path", path)
	}
	if strings.TrimSpace(os.Getenv(config.EnvKeyPassword)) != "" {
		return fmt.Errorf("--key-password-file: %s is set; unset it or do not pass --key-password-file", config.EnvKeyPassword)
	}
	if env := strings.TrimSpace(os.Getenv(config.EnvKeyPasswordFile)); env != "" && env != path {
		return fmt.Errorf("--key-password-file %s: %s names a different file (%s)", path, config.EnvKeyPasswordFile, env)
	}
	if strings.TrimSpace(cfg.Security.KeyPassword) != "" {
		return errors.New("--key-password-file: security.key_password is set in the config; set one of them")
	}
	if recorded := strings.TrimSpace(cfg.Security.KeyPasswordFile); recorded != "" && recorded != path {
		return fmt.Errorf("--key-password-file %s: the config already records security.key_password_file=%s", path, recorded)
	}

	// The node's own reader: owner-only permissions enforced, one trailing
	// newline trimmed, empty refused.
	secret, err := config.ReadKeyPasswordFile(path)
	if err != nil {
		return err
	}
	if err := identityOpensUnder(config.KeyDir(cfg), config.MnemonicPathResolved(cfg), secret); err != nil {
		return err
	}
	cfg.Security.KeyPasswordFile = path
	return config.ExportKeyPasswordFile(cfg)
}

// identityOpensUnder checks that an EXISTING identity in keyDir (the
// mnemonic, else the node key) opens under password. No identity: nothing to
// check.
func identityOpensUnder(keyDir, mnemonicPath, password string) error {
	if strings.TrimSpace(mnemonicPath) == "" {
		mnemonicPath = filepath.Join(keyDir, "mnemonic")
	}
	if data, err := os.ReadFile(mnemonicPath); err == nil {
		if !keys.IsMnemonicEncrypted(data) {
			return nil // plaintext: the node seals it under the configured password on first load
		}
		if _, derr := keys.DecryptMnemonic(data, password); derr != nil {
			return fmt.Errorf("--key-password-file: this node's identity at %s is not sealed under that file; "+
				"to move it there, configure the file and run `spacedatanetwork key reseal --confirm`", mnemonicPath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", mnemonicPath, err)
	}
	nodeKeyPath := filepath.Join(keyDir, "node.key")
	data, err := os.ReadFile(nodeKeyPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", nodeKeyPath, err)
	}
	if !strings.HasPrefix(string(data), nodeKeyEncPrefix) {
		return nil
	}
	if _, derr := keys.DecryptSecret(data[len(nodeKeyEncPrefix):], password); derr != nil {
		return fmt.Errorf("--key-password-file: this node's key at %s is not sealed under that file; "+
			"to move it there, configure the file and run `spacedatanetwork key reseal --confirm`", nodeKeyPath)
	}
	return nil
}
