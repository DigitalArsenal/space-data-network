package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

func TestNodeMnemonicBackupUsesIdentityRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "data.db")
	cfg.Security.KeyPassword = "at-rest password"
	path := config.MnemonicPathResolved(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	phrase := "legal winner thank year wave sausage worth useful legal winner thank yellow"
	sealed, err := keys.EncryptMnemonic(phrase, cfg.Security.KeyPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{config: cfg}
	got, err := s.loadNodeMnemonic()
	if err != nil {
		t.Fatalf("loadNodeMnemonic: %v", err)
	}
	if got != phrase {
		t.Fatal("backup source is not the stored mnemonic")
	}

	replacement := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	if err := s.installNodeMnemonic(replacement); err != nil {
		t.Fatalf("installNodeMnemonic: %v", err)
	}
	got, err = s.loadNodeMnemonic()
	if err != nil {
		t.Fatalf("load restored mnemonic: %v", err)
	}
	if got != replacement {
		t.Fatal("restore did not replace the authoritative mnemonic")
	}
}
