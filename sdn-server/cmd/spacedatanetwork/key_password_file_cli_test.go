package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

func refusedCheck() keys.FingerprintCheck {
	return keys.FingerprintCheck{Status: keys.FingerprintRefused, User: "sdn", Reason: "test refusal"}
}

func checkReturning(status keys.FingerprintStatus) func() keys.FingerprintCheck {
	return func() keys.FingerprintCheck { return keys.FingerprintCheck{Status: status, User: "sdn"} }
}

// freshNodeConfig is a node with no identity and no key password, in a temp
// directory.
func freshNodeConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(config.EnvKeyPassword, "")
	t.Setenv(config.EnvKeyPasswordFile, "")
	t.Setenv(config.EnvMnemonicFile, "")
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "data")
	return cfg
}

// TestFreshPasswordFileIsCreatedForARefusedFreshNode: the one case that
// writes — 32 random bytes as 64 hex characters, mode 0400, readable by the
// node's own password reader.
func TestFreshPasswordFileIsCreatedForARefusedFreshNode(t *testing.T) {
	cfg := freshNodeConfig(t)
	path := filepath.Join(t.TempDir(), "sub", "key-password")

	outcome, err := ensureFreshKeyPasswordFile(cfg, path, refusedCheck)
	if err != nil {
		t.Fatalf("ensureFreshKeyPasswordFile: %v", err)
	}
	if outcome.Action != "created" || outcome.String() != "created "+path {
		t.Fatalf("want created, got %q", outcome)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode %04o, want 0400", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 64 {
		t.Fatalf("want 64 hex characters, got %d bytes", len(data))
	}
	if raw, err := hex.DecodeString(string(data)); err != nil || len(raw) != 32 {
		t.Fatalf("not 32 bytes of hex: %v", err)
	}
	if got, err := config.ReadKeyPasswordFile(path); err != nil || got != string(data) {
		t.Fatalf("the node's reader must accept the generated file: %v", err)
	}

	// Two fresh nodes never share a password.
	other := filepath.Join(t.TempDir(), "key-password")
	if _, err := ensureFreshKeyPasswordFile(freshNodeConfig(t), other, refusedCheck); err != nil {
		t.Fatal(err)
	}
	otherData, _ := os.ReadFile(other)
	if bytes.Equal(otherData, data) {
		t.Fatal("two generated password files are identical")
	}
}

// TestFreshPasswordFileLeavesEverythingElseAlone: every reason not to write,
// and that nothing is written for any of them.
func TestFreshPasswordFileLeavesEverythingElseAlone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, cfg *config.Config)
		check func() keys.FingerprintCheck
		want  string
	}{
		{"fingerprint ok", nil, checkReturning(keys.FingerprintOK), "is ok"},
		{"fingerprint absent", nil, checkReturning(keys.FingerprintAbsent), "is absent"},
		{"identity exists (mnemonic)", func(t *testing.T, cfg *config.Config) {
			writeFile(t, config.MnemonicPath(cfg), "sealed-mnemonic-placeholder", 0o600)
		}, refusedCheck, "already has an identity"},
		{"identity exists (node.key only)", func(t *testing.T, cfg *config.Config) {
			writeFile(t, filepath.Join(config.KeyDir(cfg), "node.key"), "sdnkey1\x00placeholder", 0o600)
		}, refusedCheck, "already has an identity"},
		{"identity exists (SDN_MNEMONIC_FILE)", func(t *testing.T, cfg *config.Config) {
			p := filepath.Join(t.TempDir(), "mounted-mnemonic")
			writeFile(t, p, "sealed-mnemonic-placeholder", 0o600)
			t.Setenv(config.EnvMnemonicFile, p)
		}, refusedCheck, "already has an identity"},
		{"SDN_KEY_PASSWORD set", func(t *testing.T, _ *config.Config) {
			t.Setenv(config.EnvKeyPassword, "operator")
		}, refusedCheck, "already configured"},
		{"SDN_KEY_PASSWORD_FILE set", func(t *testing.T, _ *config.Config) {
			t.Setenv(config.EnvKeyPasswordFile, "/run/secrets/operator")
		}, refusedCheck, "already configured"},
		{"security.key_password set", func(t *testing.T, cfg *config.Config) {
			cfg.Security.KeyPassword = "operator"
		}, refusedCheck, "already configured"},
		{"security.key_password_file set", func(t *testing.T, cfg *config.Config) {
			cfg.Security.KeyPasswordFile = "/etc/operator-key-password"
		}, refusedCheck, "already configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := freshNodeConfig(t)
			if tc.setup != nil {
				tc.setup(t, cfg)
			}
			path := filepath.Join(t.TempDir(), "key-password")
			outcome, err := ensureFreshKeyPasswordFile(cfg, path, tc.check)
			if err != nil {
				t.Fatalf("ensureFreshKeyPasswordFile: %v", err)
			}
			if outcome.Action != "skipped" || !strings.Contains(outcome.Reason, tc.want) {
				t.Fatalf("want skipped (%q), got %q", tc.want, outcome)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("nothing may be written when skipped: %v", err)
			}
		})
	}
}

// TestFreshPasswordFileNeverOverwrites: an existing file is kept byte for
// byte and reported; one anyone else can read is refused, not used.
func TestFreshPasswordFileNeverOverwrites(t *testing.T) {
	cfg := freshNodeConfig(t)
	path := filepath.Join(t.TempDir(), "key-password")
	writeFile(t, path, "operator-made-this", 0o400)

	outcome, err := ensureFreshKeyPasswordFile(cfg, path, refusedCheck)
	if err != nil || outcome.Action != "exists" {
		t.Fatalf("want exists, got %q, %v", outcome, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "operator-made-this" {
		t.Fatalf("an existing password file was overwritten: %q", data)
	}

	open := filepath.Join(t.TempDir(), "key-password")
	writeFile(t, open, "world-readable", 0o644)
	if _, err := ensureFreshKeyPasswordFile(cfg, open, refusedCheck); err == nil {
		t.Fatal("a group/world-readable password file must be refused")
	}
	if data, _ := os.ReadFile(open); string(data) != "world-readable" {
		t.Fatal("a refused file must not be touched")
	}

	if _, err := ensureFreshKeyPasswordFile(cfg, "relative/key-password", refusedCheck); err == nil {
		t.Fatal("a relative path must be refused")
	}
}

// TestInitKeyPasswordFileRecordsAndSealsUnderIt: install.sh's second step.
// The file is recorded in the config init saves, the identity is sealed under
// it, and a later run that loads the saved config opens that identity with
// nothing exported.
func TestInitKeyPasswordFileRecordsAndSealsUnderIt(t *testing.T) {
	cfg := freshNodeConfig(t)
	path := filepath.Join(t.TempDir(), "key-password")
	if _, err := ensureFreshKeyPasswordFile(cfg, path, refusedCheck); err != nil {
		t.Fatal(err)
	}
	if err := adoptKeyPasswordFile(cfg, path); err != nil {
		t.Fatalf("adoptKeyPasswordFile: %v", err)
	}
	if cfg.Security.KeyPasswordFile != path {
		t.Fatalf("not recorded: %+v", cfg.Security)
	}
	result, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		return testVectorMnemonic(), nil
	})
	if err != nil || !result.Created {
		t.Fatalf("ensureNodeMnemonic: %+v, %v", result, err)
	}
	secret, err := config.ReadKeyPasswordFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := keys.DecryptMnemonic(sealed, secret); err != nil || got != testVectorMnemonic() {
		t.Fatalf("identity not sealed under the recorded file: %v", err)
	}

	// The saved config carries it; a fresh process (no env) finds it.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvKeyPasswordFile, "")
	loaded, _, err := config.LoadResolved(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Security.KeyPasswordFile != path {
		t.Fatalf("saved config lost key_password_file: %+v", loaded.Security)
	}
	if got, err := config.KeyPassword(loaded); err != nil || got != secret {
		t.Fatalf("a later start must resolve the recorded file: %v", err)
	}

	// Re-running init with the same file over the now-existing identity is
	// fine (an upgrade re-run); a different file is not.
	if err := adoptKeyPasswordFile(loaded, path); err != nil {
		t.Fatalf("re-adopting the same file: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeFile(t, other, "another-password", 0o400)
	if err := adoptKeyPasswordFile(loaded, other); err == nil {
		t.Fatal("recording a different file over an existing one must be refused")
	}
}

// TestInitKeyPasswordFileRefusesAnIdentityItDoesNotOpen: recording a file on
// a node whose identity is sealed under something else would lock the node
// out of it. That is `key reseal`, never init.
func TestInitKeyPasswordFileRefusesAnIdentityItDoesNotOpen(t *testing.T) {
	cfg := freshNodeConfig(t)
	enc, err := keys.EncryptMnemonic(testVectorMnemonic(), "sealed-under-something-else")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config.MnemonicPath(cfg), string(enc), 0o600)
	path := filepath.Join(t.TempDir(), "key-password")
	writeFile(t, path, "not-the-sealing-password", 0o400)

	err = adoptKeyPasswordFile(cfg, path)
	if err == nil || !strings.Contains(err.Error(), "reseal") {
		t.Fatalf("want a refusal pointing at key reseal, got %v", err)
	}
	if cfg.Security.KeyPasswordFile != "" {
		t.Fatal("nothing may be recorded when refused")
	}

	inline := freshNodeConfig(t)
	inline.Security.KeyPassword = "operator"
	if err := adoptKeyPasswordFile(inline, path); err == nil {
		t.Fatal("security.key_password and --key-password-file together must be refused")
	}
}

// TestEnsureNodeMnemonicRefusalOnAFreshNodeSaysNoIdentity: init on a fresh
// node that cannot get a key password explains that there is no identity
// yet — it must not say a secret failed to open.
func TestEnsureNodeMnemonicRefusalOnAFreshNodeSaysNoIdentity(t *testing.T) {
	cfg := freshNodeConfig(t)
	t.Setenv(config.EnvKeyPasswordFile, filepath.Join(t.TempDir(), "not-mounted"))
	_, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		t.Fatal("no identity may be generated without a key password")
		return "", nil
	})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no node identity exists") || strings.Contains(msg, "did not open this secret") {
		t.Fatalf("fresh-node refusal must say there is no identity yet:\n%s", msg)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
