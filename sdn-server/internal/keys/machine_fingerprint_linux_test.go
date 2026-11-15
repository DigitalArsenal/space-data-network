//go:build linux

package keys

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFingerprintCheckReadsThePlatformSource drives the Linux source through
// a fixture in the three states the kernel can present to a node: readable
// (ok), missing (absent: a DMI-less VM, some containers) and present but
// unreadable (refused: product_uuid is 0400 and the node is not root).
func TestFingerprintCheckReadsThePlatformSource(t *testing.T) {
	saved := platuuidPath
	t.Cleanup(func() { platuuidPath = saved })
	dir := t.TempDir()

	platuuidPath = filepath.Join(dir, "product_uuid")
	if err := os.WriteFile(platuuidPath, []byte("4C4C4544-0000-1000-8000-000000000000\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if got := CheckMachineFingerprint(); got.Status != FingerprintOK {
		t.Fatalf("readable source: want ok, got %+v", got)
	}

	platuuidPath = filepath.Join(dir, "missing")
	if got := CheckMachineFingerprint(); got.Status != FingerprintAbsent {
		t.Fatalf("missing source: want absent, got %+v", got)
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file; the refused state needs an unprivileged account")
	}
	platuuidPath = filepath.Join(dir, "unreadable")
	if err := os.WriteFile(platuuidPath, []byte("4C4C4544-0000-1000-8000-000000000000\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	got := CheckMachineFingerprint()
	if got.Status != FingerprintRefused || !errors.Is(got.Err, fs.ErrPermission) {
		t.Fatalf("unreadable source: want refused with a permission error, got %+v", got)
	}
	if !strings.Contains(got.Why, "0400") {
		t.Fatalf("a permission refusal must explain the kernel's 0400: %q", got.Why)
	}
	if _, err := DeriveDefaultPassword(); err == nil {
		t.Fatal("the derivation must refuse exactly when the check says refused")
	}
	if hint := noIdentityHint(dir); !strings.Contains(hint, "REFUSED") || strings.Contains(hint, "did not open this secret") {
		t.Fatalf("fresh-node hint on a refused host:\n%s", hint)
	}
}
