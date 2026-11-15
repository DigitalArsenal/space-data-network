package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// TestCLINamesTheGeneratedKeyPasswordFileWhenRefused: the container
// entrypoint and the .deb/.rpm unit give their generated file to the daemon
// only, so `docker exec ... show-identity --show-mnemonic` (the documented
// backup step) and `sudo -u sdn ... key export` reached the machine key and
// were refused with nothing pointing at the file. The refusal now names it and
// how to pass it.
func TestCLINamesTheGeneratedKeyPasswordFileWhenRefused(t *testing.T) {
	cfg := freshNodeConfig(t)
	old := deriveDefaultKeyPassword
	deriveDefaultKeyPassword = func() (string, error) {
		return "", errors.New("machine fingerprint (v4): /sys/class/dmi/id/product_uuid exists but cannot be read")
	}
	t.Cleanup(func() { deriveDefaultKeyPassword = old })

	_, err := resolveKeyCLIPassword(cfg)
	if err == nil || strings.Contains(err.Error(), "generated key password file") {
		t.Fatalf("no generated file: the refusal alone, got %v", err)
	}

	generated := filepath.Join(filepath.Dir(config.KeyDir(cfg)), "key-password")
	writeFile(t, generated, strings.Repeat("cd", 32), 0o400)
	_, err = resolveKeyCLIPassword(cfg)
	if err == nil {
		t.Fatal("still refused: the file is named, never read implicitly")
	}
	for _, want := range []string{
		"cannot be read",
		"generated key password file: " + generated,
		"SDN_KEY_PASSWORD_FILE=" + generated,
		"/app/entrypoint.sh",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must contain %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), strings.Repeat("cd", 32)) {
		t.Fatal("the password itself must never be printed")
	}

	// With the variable set, the same command opens it.
	t.Setenv(config.EnvKeyPasswordFile, generated)
	if got, err := resolveKeyCLIPassword(cfg); err != nil || got != strings.Repeat("cd", 32) {
		t.Fatalf("with SDN_KEY_PASSWORD_FILE the command resolves the file: %q %v", got, err)
	}
}

// TestFreshPasswordFileKeyDirWithin: the container keeps its generated file
// in /app/data. A config whose key directory is elsewhere (storage in an
// operator volume mounted outside /app/data) would seal the identity there
// under a password kept in /app/data, an anonymous volume the next `docker
// run` does not reuse: a later lockout. `--key-dir-within` refuses that.
func TestFreshPasswordFileKeyDirWithin(t *testing.T) {
	base := filepath.Join(t.TempDir(), "app", "data")
	inside := freshNodeConfig(t)
	inside.Storage.Path = filepath.Join(base, "store")
	if _, ok := keyDirWithin(inside, base); !ok {
		t.Fatalf("key dir %s is inside %s", config.KeyDir(inside), base)
	}
	for name, storage := range map[string]string{
		"another volume":   filepath.Join(t.TempDir(), "data", "store"),
		"a sibling prefix": filepath.Join(filepath.Dir(base), "data2", "store"),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := freshNodeConfig(t)
			cfg.Storage.Path = storage
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := config.Save(cfgPath, cfg); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "key-password")

			oldConfig, oldWithin := configPath, freshKeyDirWithin
			configPath, freshKeyDirWithin = cfgPath, base
			t.Cleanup(func() { configPath, freshKeyDirWithin = oldConfig, oldWithin })
			var out bytes.Buffer
			keyFreshPasswordFileCmd.SetOut(&out)
			t.Cleanup(func() { keyFreshPasswordFileCmd.SetOut(nil) })
			if err := keyFreshPasswordFileCmd.RunE(keyFreshPasswordFileCmd, []string{target}); err != nil {
				t.Fatalf("RunE: %v", err)
			}
			if !strings.HasPrefix(out.String(), "skipped: ") || !strings.Contains(out.String(), "is not inside "+base) {
				t.Fatalf("want a skip naming %s, got %q", base, out.String())
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("nothing may be created when the key dir is elsewhere: %v", err)
			}
		})
	}
}
