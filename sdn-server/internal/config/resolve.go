package config

// Config and key-path resolution shared by the CLI and the daemon.
//
// OWNER, 2026-07-27, after `show-identity` failed on host-01:
// "CLI needs to actually work with the new architecture."
//
// The failure was not one command. The CLI resolved config as
// `$HOME/.spacedatanetwork/config.yaml` whenever `-c` was absent, while the
// deployed node runs as a service with `--config /etc/space-data-network/...`
// and derives its keys from THAT config's storage path. An operator on the host
// therefore addressed a node that does not exist — and the failures were not
// even honest: `open` and `status` printed the DEFAULT admin URL rather than
// erroring, so they silently reported on the wrong node.
//
// # Resolution order
//
//	1. --config / -c            explicit operator intent
//	2. SDN_CONFIG               explicit environment intent
//	3. the RUNNING DAEMON       read the config off the live process
//	4. a system location        well-known deployed paths
//	5. ~/.spacedatanetwork      developer default
//
// Step 3 is the one that fixes the reported bug, and step 4 alone was not
// enough: the config the daemon actually runs with must be findable WITHOUT the
// operator reading systemd unit files, and on a host running one daemon it must
// never matter how many yaml files happen to sit in /etc. See running_daemon.go.
//
// # One derivation, shared
//
// KeyDir/MnemonicPath are the same functions the daemon uses, so the CLI can
// never look somewhere the daemon does not write. That is the same principle as
// the show-identity derivation rule: never re-implement, always share.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// ConfigSource records HOW a config path was chosen, so errors can tell the
// operator what happened rather than making them guess.
type ConfigSource string

const (
	SourceFlag    ConfigSource = "--config flag"
	SourceEnv     ConfigSource = "SDN_CONFIG environment variable"
	SourceSystem  ConfigSource = "system location (the running daemon's config)"
	SourceHome    ConfigSource = "home default"
	SourceMissing ConfigSource = "no config found"
)

// systemConfigGlobs are the well-known locations a deployed node's config lives
// in. They are ordered: the first location that yields exactly one match wins.
//
//   - /etc/space-data-network   host installs (systemd service), the host-01 shape
//   - /app/config               container installs (deployment/remote/), where the
//     compose file mounts node.yaml
//
// Exposed as a variable ONLY so tests can point it at a fixture root; nothing in
// the shipped binary reassigns it.
var systemConfigGlobs = []string{
	"/etc/space-data-network/*.yaml",
	"/etc/space-data-network/*.yml",
	"/app/config/*.yaml",
	"/app/config/*.yml",
	"/usr/local/etc/space-data-network/*.yaml",
}

// Resolution is the outcome of resolving a config path.
type Resolution struct {
	// Path is the config file path that was selected. Never empty.
	Path string
	// Source explains how Path was chosen, for error messages.
	Source ConfigSource
	// Exists reports whether Path is actually present on disk. A resolution
	// can succeed with Exists=false (the home default on a fresh machine);
	// callers that need node state must say so in their error.
	Exists bool
	// Candidates lists every system config found when more than one matched.
	// Populated only for the ambiguity error.
	Candidates []string
}

// Describe renders the resolution for an error message or a --verbose line.
func (r Resolution) Describe() string {
	if !r.Exists {
		return fmt.Sprintf("%s (from %s, DOES NOT EXIST)", r.Path, r.Source)
	}
	return fmt.Sprintf("%s (from %s)", r.Path, r.Source)
}

// OverrideHint is the sentence appended to every "I could not find node state"
// error so the operator always knows the way out.
const OverrideHint = "if your node runs elsewhere, pass -c <config> or set SDN_CONFIG"

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// findSystemConfig looks for the config a deployed daemon would be using.
//
// Exactly one match in the first matching location wins. Several matches is an
// ERROR, not a guess: on a host running more than one node profile, silently
// picking one would point every CLI command at the wrong node — which is the
// class of bug this whole function exists to end.
func findSystemConfig() (string, []string, error) {
	for _, pattern := range systemConfigGlobs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		var files []string
		for _, m := range matches {
			if fileExists(m) {
				files = append(files, m)
			}
		}
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		if len(files) == 1 {
			return files[0], nil, nil
		}
		return "", files, fmt.Errorf(
			"found %d configs in %s:\n  %s\nCannot tell which node you mean — pass -c <config> or set SDN_CONFIG",
			len(files), filepath.Dir(pattern), strings.Join(files, "\n  "))
	}
	return "", nil, nil
}

// ResolvePath applies the documented order and reports what it chose.
func ResolvePath(explicit string) (Resolution, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		return namedOrRunningDaemon(Resolution{Path: p, Source: SourceFlag, Exists: fileExists(p)}), nil
	}
	if p := strings.TrimSpace(os.Getenv("SDN_CONFIG")); p != "" {
		return namedOrRunningDaemon(Resolution{Path: p, Source: SourceEnv, Exists: fileExists(p)}), nil
	}
	// Tier 3: ask the running daemon. This outranks the directory scan because
	// it answers the actual question ("which node runs here?") instead of
	// inferring it from filenames — see running_daemon.go for why that matters.
	daemonRes, resolved, err := resolveFromRunningDaemon()
	if err != nil {
		return daemonRes, err
	}
	if resolved {
		return daemonRes, nil
	}
	systemPath, candidates, err := findSystemConfig()
	if err != nil {
		return Resolution{Source: SourceMissing, Candidates: candidates}, err
	}
	if systemPath != "" {
		return Resolution{Path: systemPath, Source: SourceSystem, Exists: true}, nil
	}
	home := DefaultPath()
	return Resolution{Path: home, Source: SourceHome, Exists: fileExists(home)}, nil
}

// namedOrRunningDaemon returns res as the running daemon's resolution when
// the file it names (-c or SDN_CONFIG) is the very file a running daemon of a
// trusted account was started with: then the config IS that daemon's, so
// IsOwnDaemonConfig holds and the CLI anchors to the certificate it declares,
// exactly as when the path came from the process table. Stack issue 839:
// `update install --config <the node's config>` reached a TLS daemon through
// system roots, concluded no daemon ran, and was refused by the store lock.
func namedOrRunningDaemon(res Resolution) Resolution {
	named, err := os.Stat(res.Path)
	if err != nil || named.IsDir() {
		return res
	}
	for _, d := range FindRunningDaemons() {
		if d.ConfigPath == "" || !trustedDaemonOwner(d) {
			continue
		}
		path := d.ConfigPath
		if !filepath.IsAbs(path) {
			// A relative --config is relative to the daemon's working
			// folder, which the process table does not say; only an
			// absolute path can name the same file with certainty.
			continue
		}
		if info, err := os.Stat(path); err == nil && os.SameFile(named, info) {
			return Resolution{Path: res.Path, Source: ConfigSource(fmt.Sprintf("%s (pid %d), named by %s", SourceRunningDaemonPrefix, d.PID, res.Source)), Exists: true}
		}
	}
	return res
}

// LoadResolved resolves and loads in one step. Every CLI command that touches
// node state should use this instead of Load(configPath), so the resolution is
// identical everywhere and the Resolution is available for error messages.
func LoadResolved(explicit string) (*Config, Resolution, error) {
	res, err := ResolvePath(explicit)
	if err != nil {
		return nil, res, err
	}
	cfg, err := Load(res.Path)
	if err != nil {
		return nil, res, fmt.Errorf("load config %s: %w", res.Describe(), err)
	}
	if err := ExportKeyPasswordFile(cfg); err != nil {
		return nil, res, fmt.Errorf("config %s: %w", res.Describe(), err)
	}
	return cfg, res, nil
}

// ExportKeyPasswordFile makes security.key_password_file act exactly like an
// exported SDN_KEY_PASSWORD_FILE for this process (and the processes it
// starts), unless SDN_KEY_PASSWORD or SDN_KEY_PASSWORD_FILE is already in the
// environment — an exported variable wins, as it does over every config
// source.
//
// Why the environment and not only KeyPassword(cfg): some at-rest paths
// resolve the password without a config in hand (the record store's local
// EPM rows call KeyPassword(nil)). A config-recorded file that only the
// config-aware paths honoured would seal those rows under the machine key —
// which, on the very nodes the file exists for, is refused. Exporting it once,
// where the node's config is loaded, gives every path the same source.
func ExportKeyPasswordFile(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	path := strings.TrimSpace(cfg.Security.KeyPasswordFile)
	if path == "" {
		return nil
	}
	if strings.TrimSpace(os.Getenv(EnvKeyPassword)) != "" || strings.TrimSpace(os.Getenv(EnvKeyPasswordFile)) != "" {
		return nil
	}
	if err := cfg.Security.validateKeyPasswordSource(); err != nil {
		return err
	}
	return os.Setenv(EnvKeyPasswordFile, path)
}

// KeyDir returns the node's key directory for a config.
//
// THIS IS THE ONE DERIVATION. internal/node derives its key directory the same
// way (filepath.Dir(storage.path)/keys); routing every caller through this
// function is what stops the CLI and the daemon disagreeing about where the
// identity lives — the exact drift that produced the host-01 failure.
func KeyDir(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(cfg.Storage.Path), "keys")
}

// UICacheDir returns the directory holding the read-surface caches that must
// SURVIVE A RESTART: <storage.path>/ui-cache.
//
// host-01 spends 60-100 minutes hydrating under the store lock after each
// daemon restart, and every RAM-only read cache starts empty, so for that whole
// window the anonymous surfaces answered STORE_BUSY / SNAPSHOT_COLD — the node
// forgot what it had been serving a minute earlier. The caches write their
// last-known-good answers here and load them at boot.
//
// It is DERIVED, never configured: one derivation is what stops the daemon and
// any tool disagreeing about where the cache lives, the same rule KeyDir
// follows. An empty basePath returns "" and every caller then runs RAM-only.
//
// The directory is created 0700 — these are the node's own answers to its own
// public surfaces, but the store directory's posture is the node's, not the
// world's.
func UICacheDir(basePath string) (string, error) {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		return "", nil
	}
	dir := filepath.Join(basePath, "ui-cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ui cache dir %s: %w", dir, err)
	}
	return dir, nil
}

// MnemonicPath returns the node's encrypted mnemonic file for a config.
func MnemonicPath(cfg *Config) string {
	dir := KeyDir(cfg)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mnemonic")
}

// DescribeMissingNodeState builds the error an operator should see when a path
// derived from the resolved config is not there.
//
// A bare ENOENT is useless on a host with several possible nodes: it names a
// path but not WHY the CLI looked there. This names the config that produced
// the path and how to override it.
func DescribeMissingNodeState(what string, path string, res Resolution) error {
	return fmt.Errorf("no %s at %s\n  config: %s\n  %s", what, path, res.Describe(), OverrideHint)
}

// DescribePermissionDenied builds the error for key material the CLI can see
// but not read.
//
// On a service host the key directory is 0700 owned by the service user, and
// that is CORRECT — the at-rest encryption is hardware-derived per machine, so
// file permissions are the thing keeping another local user out. The CLI must
// never respond by loosening them; it tells the operator who owns the file and
// to run as that user or root.
func DescribePermissionDenied(what string, path string, owner string, res Resolution) error {
	who := owner
	if strings.TrimSpace(who) == "" {
		who = "the service user"
	}
	return fmt.Errorf(
		"cannot read %s at %s: permission denied\n"+
			"  config: %s\n"+
			"  the key directory is owned by %s and is intentionally 0700 —\n"+
			"  run this command as %s (or root). Do NOT loosen the permissions",
		what, path, res.Describe(), who, who)
}

// SecretFileEnv names the environment variables that point at FILES holding
// secrets, rather than carrying the secret inline.
//
// SEAL COUNCIL HANDOFF (Hephaestus, 2026-07-28): the remote deploy script feeds
// key material to a container through mounted files —
// SDN_KEY_PASSWORD_FILE=/run/sdn-key/key_password and
// SDN_MNEMONIC_FILE=/run/secrets/sdn_mnemonic — and it GUARDS on the binary
// supporting them (it greps the binary and refuses to import a phrase into an
// image that would silently ignore the variable and generate a fresh identity
// instead). Honoring them is therefore a compatibility contract with the
// deploy path, not a convenience.
//
// A file is strictly better custody than an inline env var: it does not appear
// in `ps`, in the container's `env`, in docker inspect output, or in a shell
// history, and it can be a tmpfs mount that never touches disk.
const (
	EnvKeyPassword     = "SDN_KEY_PASSWORD"
	EnvKeyPasswordFile = "SDN_KEY_PASSWORD_FILE"
	EnvMnemonicFile    = "SDN_MNEMONIC_FILE"
)

// readSecretFile reads a secret from a file, trimming exactly one trailing
// newline. Secrets written by `echo` or a heredoc pick one up, and a trailing
// newline silently changes a password.
//
// The at-rest key password is the thing that opens the mnemonic, so it is key
// material and gets ssh-parity treatment: a file anyone but its owner can read
// is refused, not read (owner ruling 2026-07-28, keys.EnforceKeyFilePermissions).
func readSecretFile(path string) (string, error) {
	if err := keys.EnforceKeyFilePermissions(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// KeyPassword resolves the at-rest key password.
//
// Order: SDN_KEY_PASSWORD (inline) > SDN_KEY_PASSWORD_FILE (mounted secret) >
// config security.key_password > config security.key_password_file > the
// machine-derived default. Inline wins so an operator debugging by hand can
// override a mounted file without unmounting it; the environment wins over the
// config for the same reason. The two config keys are mutually exclusive.
//
// A CONFIGURED-BUT-UNREADABLE file is an ERROR, never a silent fallback: falling
// through to the machine default would decrypt nothing and report "wrong
// password", sending the operator hunting for a corrupt mnemonic when the real
// fault is a missing mount.
func KeyPassword(cfg *Config) (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvKeyPassword)); p != "" {
		return p, nil
	}
	if path := strings.TrimSpace(os.Getenv(EnvKeyPasswordFile)); path != "" {
		return readKeyPasswordFile(EnvKeyPasswordFile, path)
	}
	if cfg == nil {
		return "", nil
	}
	if err := cfg.Security.validateKeyPasswordSource(); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.Security.KeyPassword) != "" {
		return cfg.Security.KeyPassword, nil
	}
	if path := strings.TrimSpace(cfg.Security.KeyPasswordFile); path != "" {
		return readKeyPasswordFile("security.key_password_file", path)
	}
	return "", nil // caller applies the machine-derived default
}

// ReadKeyPasswordFile reads a key password file exactly as the node does for
// security.key_password_file: owner-only permissions enforced, one trailing
// newline trimmed, an empty file refused.
func ReadKeyPasswordFile(path string) (string, error) {
	return readKeyPasswordFile("security.key_password_file", strings.TrimSpace(path))
}

// readKeyPasswordFile reads a configured password file; source names where the
// path came from, for the error.
func readKeyPasswordFile(source, path string) (string, error) {
	secret, err := readSecretFile(path)
	if err != nil {
		return "", fmt.Errorf("%s=%s: %w", source, path, err)
	}
	if secret == "" {
		return "", fmt.Errorf("%s=%s is empty", source, path)
	}
	return secret, nil
}

// KeyPasswordConfigured reports whether an explicit key password source is
// set for cfg — in the environment or in the config — without reading it.
// Installers use it to leave a node that already has one alone.
func KeyPasswordConfigured(cfg *Config) bool {
	if strings.TrimSpace(os.Getenv(EnvKeyPassword)) != "" || strings.TrimSpace(os.Getenv(EnvKeyPasswordFile)) != "" {
		return true
	}
	return cfg != nil && (strings.TrimSpace(cfg.Security.KeyPassword) != "" || strings.TrimSpace(cfg.Security.KeyPasswordFile) != "")
}

// validateKeyPasswordSource rejects a config that names both key password
// sources (which one seals the identity would be a guess) or a relative
// password file (relative to WHICH directory would be a guess too: the daemon,
// the service and the CLI run from different ones).
func (s SecurityConfig) validateKeyPasswordSource() error {
	file := strings.TrimSpace(s.KeyPasswordFile)
	if file == "" {
		return nil
	}
	if strings.TrimSpace(s.KeyPassword) != "" {
		return fmt.Errorf("security.key_password and security.key_password_file are both set; set one of them")
	}
	if !filepath.IsAbs(file) {
		return fmt.Errorf("security.key_password_file must be an absolute path, got %q", file)
	}
	return nil
}

// MnemonicPathResolved returns the mnemonic file to read, honoring
// SDN_MNEMONIC_FILE. Empty config yields empty, like MnemonicPath.
func MnemonicPathResolved(cfg *Config) string {
	if path := strings.TrimSpace(os.Getenv(EnvMnemonicFile)); path != "" {
		return path
	}
	return MnemonicPath(cfg)
}
