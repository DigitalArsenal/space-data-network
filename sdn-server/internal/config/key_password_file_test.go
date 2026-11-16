package config

// security.key_password_file: SDN_KEY_PASSWORD_FILE recorded in the node's own
// config, so a node install.sh set up on a host whose machine key is refused
// finds its key password on every later start with nothing exported.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecretFile(t *testing.T, dir, name, secret string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(secret), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearKeyPasswordEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvKeyPassword, "")
	t.Setenv(EnvKeyPasswordFile, "")
}

// TestKeyPasswordPrecedenceIncludesTheConfigFile pins the whole order:
// SDN_KEY_PASSWORD > SDN_KEY_PASSWORD_FILE > security.key_password >
// security.key_password_file > (machine default: "").
func TestKeyPasswordPrecedenceIncludesTheConfigFile(t *testing.T) {
	clearKeyPasswordEnv(t)
	dir := t.TempDir()
	envFile := writeSecretFile(t, dir, "env-file", "from-env-file\n")
	cfgFile := writeSecretFile(t, dir, "cfg-file", "from-config-file")

	cfg := Default()
	if got, err := KeyPassword(cfg); err != nil || got != "" {
		t.Fatalf("nothing configured: want the machine default (\"\"), got %q, %v", got, err)
	}

	cfg.Security.KeyPasswordFile = cfgFile
	if got, err := KeyPassword(cfg); err != nil || got != "from-config-file" {
		t.Fatalf("security.key_password_file: got %q, %v", got, err)
	}

	inline := Default()
	inline.Security.KeyPassword = "from-config-inline"
	if got, err := KeyPassword(inline); err != nil || got != "from-config-inline" {
		t.Fatalf("security.key_password: got %q, %v", got, err)
	}

	t.Setenv(EnvKeyPasswordFile, envFile)
	if got, err := KeyPassword(cfg); err != nil || got != "from-env-file" {
		t.Fatalf("SDN_KEY_PASSWORD_FILE must beat the config: got %q, %v", got, err)
	}

	t.Setenv(EnvKeyPassword, "from-env-inline")
	if got, err := KeyPassword(cfg); err != nil || got != "from-env-inline" {
		t.Fatalf("SDN_KEY_PASSWORD must beat everything: got %q, %v", got, err)
	}
}

// TestConfigKeyPasswordFileFailsClosed: a recorded file that is missing, too
// open, or empty is an error — never a fall-through to the machine key.
func TestConfigKeyPasswordFileFailsClosed(t *testing.T) {
	clearKeyPasswordEnv(t)
	dir := t.TempDir()

	cfg := Default()
	cfg.Security.KeyPasswordFile = filepath.Join(dir, "not-there")
	if _, err := KeyPassword(cfg); err == nil || !strings.Contains(err.Error(), "security.key_password_file") {
		t.Fatalf("missing file: want an error naming security.key_password_file, got %v", err)
	}

	open := filepath.Join(dir, "too-open")
	if err := os.WriteFile(open, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Security.KeyPasswordFile = open
	if _, err := KeyPassword(cfg); err == nil {
		t.Fatal("a group/world-readable password file must be refused")
	}

	cfg.Security.KeyPasswordFile = writeSecretFile(t, dir, "empty", "")
	if _, err := KeyPassword(cfg); err == nil {
		t.Fatal("an empty password file must be refused")
	}
}

// TestConfigKeyPasswordSourcesAreValidated: both keys at once, or a relative
// file, fail the LOAD — which one seals the identity must never be a guess.
func TestConfigKeyPasswordSourcesAreValidated(t *testing.T) {
	clearKeyPasswordEnv(t)
	dir := t.TempDir()
	file := writeSecretFile(t, dir, "kp", "secret")

	both := filepath.Join(dir, "both.yaml")
	if err := os.WriteFile(both, []byte("security:\n  key_password: inline\n  key_password_file: "+file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(both); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both key password sources: want a load error, got %v", err)
	}

	relative := filepath.Join(dir, "relative.yaml")
	if err := os.WriteFile(relative, []byte("security:\n  key_password_file: key-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(relative); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative key_password_file: want a load error, got %v", err)
	}

	cfg := Default()
	cfg.Security.KeyPassword = "inline"
	cfg.Security.KeyPasswordFile = file
	if _, err := KeyPassword(cfg); err == nil {
		t.Fatal("KeyPassword must refuse a config naming both sources")
	}
}

// TestLoadResolvedExportsTheRecordedFile: loading the node's config makes the
// recorded file act as an exported SDN_KEY_PASSWORD_FILE — so paths that
// resolve the password with no config in hand (KeyPassword(nil): the record
// store's local EPM rows) use it too — and an exported variable still wins.
func TestLoadResolvedExportsTheRecordedFile(t *testing.T) {
	clearKeyPasswordEnv(t)
	dir := t.TempDir()
	file := writeSecretFile(t, dir, "key-password", "recorded-secret")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("storage:\n  path: "+filepath.Join(dir, "data")+"\nsecurity:\n  key_password_file: "+file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadResolved(cfgPath)
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if cfg.Security.KeyPasswordFile != file {
		t.Fatalf("key_password_file not loaded: %+v", cfg.Security)
	}
	if got := os.Getenv(EnvKeyPasswordFile); got != file {
		t.Fatalf("LoadResolved must export %s=%s, got %q", EnvKeyPasswordFile, file, got)
	}
	if got, err := KeyPassword(nil); err != nil || got != "recorded-secret" {
		t.Fatalf("a config-less resolution must see the recorded file: got %q, %v", got, err)
	}
	if !KeyPasswordConfigured(Default()) {
		t.Fatal("KeyPasswordConfigured must see the exported file")
	}

	// An operator's exported file wins and is not overwritten.
	other := writeSecretFile(t, dir, "operator-file", "operator-secret")
	t.Setenv(EnvKeyPasswordFile, other)
	if _, _, err := LoadResolved(cfgPath); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(EnvKeyPasswordFile); got != other {
		t.Fatalf("an exported %s must not be replaced by the config, got %q", EnvKeyPasswordFile, got)
	}
}

// TestKeyPasswordConfigured reports every source without reading any.
func TestKeyPasswordConfigured(t *testing.T) {
	clearKeyPasswordEnv(t)
	if KeyPasswordConfigured(Default()) || KeyPasswordConfigured(nil) {
		t.Fatal("nothing configured")
	}
	cfg := Default()
	cfg.Security.KeyPasswordFile = "/nonexistent/but/configured"
	if !KeyPasswordConfigured(cfg) {
		t.Fatal("security.key_password_file counts, readable or not")
	}
	cfg = Default()
	cfg.Security.KeyPassword = "x"
	if !KeyPasswordConfigured(cfg) {
		t.Fatal("security.key_password counts")
	}
	t.Setenv(EnvKeyPassword, "x")
	if !KeyPasswordConfigured(nil) {
		t.Fatal("SDN_KEY_PASSWORD counts")
	}
}
