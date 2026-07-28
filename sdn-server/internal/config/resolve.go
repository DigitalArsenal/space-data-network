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
//	3. a system location        THE CONFIG THE RUNNING DAEMON USES
//	4. ~/.spacedatanetwork      developer default
//
// Step 3 is the one that fixes the reported bug: the config the daemon actually
// runs with must be findable WITHOUT the operator reading systemd unit files.
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
		return Resolution{Path: p, Source: SourceFlag, Exists: fileExists(p)}, nil
	}
	if p := strings.TrimSpace(os.Getenv("SDN_CONFIG")); p != "" {
		return Resolution{Path: p, Source: SourceEnv, Exists: fileExists(p)}, nil
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
	return cfg, res, nil
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
