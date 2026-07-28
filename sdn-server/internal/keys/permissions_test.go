package keys

// ssh-parity permissions for key material (owner ruling 2026-07-28).
//
// Writing 0600 was always right; the gap was that nothing CHECKED. A mode is
// only a guarantee if something verifies it, and permissions drift for reasons
// no writer controls — a restore that flattened modes, a cp without -p, a
// permissive umask in an image build. A world-readable mnemonic reads exactly
// like a correct one.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeMode(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

func TestOwnerOnlyKeyFileIsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := writeMode(t, dir, "mnemonic", 0o600)
	if err := CheckKeyFilePermissions(path); err != nil {
		t.Fatalf("0600 key file rejected: %v", err)
	}
}

// The whole point: anything group- or world-readable is refused.
func TestGroupOrWorldReadableKeyFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o660, 0o666, 0o777} {
		dir := t.TempDir()
		path := writeMode(t, dir, "mnemonic", mode)
		err := CheckKeyFilePermissions(path)
		if err == nil {
			t.Errorf("mode %04o was accepted; key material is exposed", mode)
			continue
		}
		var unprotected *ErrUnprotectedKeyFile
		if !errors.As(err, &unprotected) {
			t.Errorf("mode %04o: unexpected error type %T", mode, err)
		}
	}
}

// An operator must be able to act on the message without reading the source —
// ssh's works because it names the file, the mode, and the fix.
func TestRefusalMessageIsActionable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	dir := t.TempDir()
	path := writeMode(t, dir, "mnemonic", 0o644)
	err := CheckKeyFilePermissions(path)
	if err == nil {
		t.Fatal("0644 mnemonic accepted")
	}
	msg := err.Error()
	for _, want := range []string{"UNPROTECTED", "0644", path, "chmod 0600", "will be ignored"} {
		if !contains(msg, want) {
			t.Errorf("refusal message lacks %q: %s", want, msg)
		}
	}
}

// An owner-executable key file is odd but discloses nothing. Refusing it would
// fail nodes over a bit that protects no one.
func TestOwnerExecutableBitIsNotADisclosure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	dir := t.TempDir()
	path := writeMode(t, dir, "node.key", 0o700)
	if err := CheckKeyFilePermissions(path); err != nil {
		t.Fatalf("0700 key file rejected over an owner-only bit: %v", err)
	}
}

// A missing key is not an insecure key: the caller's own read reports absence,
// and reporting it here would turn every fresh node into a permission error.
func TestMissingFileIsNotAPermissionError(t *testing.T) {
	if err := CheckKeyFilePermissions(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("absent key file reported as a permission problem: %v", err)
	}
}

func TestKeyDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.Mkdir(good, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckKeyDirPermissions(good); err != nil {
		t.Fatalf("0700 key dir rejected: %v", err)
	}

	loose := filepath.Join(dir, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	err := CheckKeyDirPermissions(loose)
	if err == nil {
		t.Fatal("0755 key dir accepted")
	}
	var unprotected *ErrUnprotectedKeyFile
	if !errors.As(err, &unprotected) || !unprotected.IsDir {
		t.Fatalf("directory refusal not reported as a directory: %v", err)
	}
	if !contains(err.Error(), "chmod 0700") {
		t.Errorf("directory message does not give the fix: %s", err)
	}
}

// The check has to be wired into the LOAD, not just available: a checker
// nothing calls is the same as no checker.
func TestManagerRefusesToLoadAnExposedKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	dir := t.TempDir()
	m := &Manager{basePath: dir}

	writeMode(t, dir, "signing_private.key", 0o600)
	if _, err := m.loadKey("signing_private.key"); err != nil {
		t.Fatalf("owner-only key refused: %v", err)
	}

	writeMode(t, dir, "exposed_private.key", 0o644)
	data, err := m.loadKey("exposed_private.key")
	if err == nil {
		t.Fatal("loadKey read a world-readable private key")
	}
	if data != nil {
		t.Fatal("loadKey returned bytes for a refused key")
	}
	var unprotected *ErrUnprotectedKeyFile
	if !errors.As(err, &unprotected) {
		t.Fatalf("unexpected error type %T: %v", err, err)
	}
}
