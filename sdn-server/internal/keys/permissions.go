package keys

// ssh-parity permissions for key material at rest.
//
// OWNER RULING 2026-07-28, verbatim: "the key material will be as secure as the
// private keys in .ssh so let's just make sure the permissions are the same as
// keys there."
//
// So: key directories 0700, key files 0600, owner-only — and, crucially, the
// same thing ssh does when they are not. Writing 0600 was already correct here;
// what was missing is the CHECK. A mode is only a guarantee if something
// verifies it, and permissions drift for reasons no writer controls: a restore
// from a backup that flattened modes, a `cp` without -p, an image build running
// under a permissive umask, an operator editing a mnemonic with a tool that
// recreates the file. Nothing in this daemon would have noticed, and a
// world-readable mnemonic reads exactly like a correct one.
//
// WHY REFUSE THE KEY AND NOT THE PROCESS. ssh does not exit when it finds a bad
// mode; it declines to use THAT key, says so unmistakably, and carries on.
// That is the behaviour worth copying. Killing a daemon at boot over a mode bit
// converts a confidentiality problem into an availability outage on every node
// at once — and an operator who cannot start the node also cannot fix the
// permission. Declining the key fails closed on the thing that is actually
// compromised, and the message says precisely what to run.

import (
	"fmt"
	"io/fs"
	"os"
	"runtime"

	logging "github.com/ipfs/go-log/v2"
)

var permLog = logging.Logger("keys-perms")

// KeyFileMode is the mode a file holding key material must have: owner
// read/write, nothing for group or other. Same as ~/.ssh/id_ed25519.
const KeyFileMode fs.FileMode = 0o600

// KeyDirMode is the mode a directory holding key material must have. Same as
// ~/.ssh — owner needs the execute bit to traverse it.
const KeyDirMode fs.FileMode = 0o700

// ErrUnprotectedKeyFile is returned when key material is readable by anyone
// other than its owner. It is deliberately loud: this is the one error in the
// package an operator must never skim past.
type ErrUnprotectedKeyFile struct {
	Path  string
	Mode  fs.FileMode
	Want  fs.FileMode
	IsDir bool
}

func (e *ErrUnprotectedKeyFile) Error() string {
	kind := "private key file"
	if e.IsDir {
		kind = "key directory"
	}
	return fmt.Sprintf(
		"UNPROTECTED %s: permissions %04o for %q are too open; it is required that key material is NOT accessible by others. "+
			"This key will be ignored. Fix with: chmod %04o %s",
		kind, e.Mode.Perm(), e.Path, e.Want.Perm(), e.Path)
}

// permissionsEnforced reports whether this platform has POSIX modes worth
// checking. Windows does not express confidentiality this way and would fail
// every check for the wrong reason, so the check is skipped there — exactly as
// OpenSSH skips it on Windows.
func permissionsEnforced() bool { return runtime.GOOS != "windows" }

// CheckKeyFilePermissions verifies that a file holding key material is not
// accessible to group or other. A missing file is not a permission problem —
// the caller's own os.ReadFile will report it.
func CheckKeyFilePermissions(path string) error {
	return checkMode(path, KeyFileMode, false)
}

// CheckKeyDirPermissions verifies that a directory holding key material is not
// accessible to group or other.
func CheckKeyDirPermissions(path string) error {
	return checkMode(path, KeyDirMode, true)
}

func checkMode(path string, want fs.FileMode, isDir bool) error {
	if !permissionsEnforced() {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		// Not our problem to report: the read that follows will say so, and a
		// missing key is not an insecure key.
		return nil
	}
	mode := info.Mode().Perm()
	// Only group/other bits matter. An owner-executable key file is odd but
	// not a disclosure, and refusing it would fail nodes over nothing.
	if mode&0o077 == 0 {
		return nil
	}
	return &ErrUnprotectedKeyFile{Path: path, Mode: mode, Want: want, IsDir: isDir}
}

// EnforceKeyFilePermissions is CheckKeyFilePermissions with the ssh-style
// complaint already logged. It returns the error so the caller can decline the
// key; the log exists because a refusal that only shows up in a return value is
// how the last silent failure on this stack stayed invisible for a day.
func EnforceKeyFilePermissions(path string) error {
	err := CheckKeyFilePermissions(path)
	if err != nil {
		permLog.Errorf("%v", err)
	}
	return err
}

// WarnKeyDirPermissions reports a loose key DIRECTORY. A directory mode does
// not expose the key bytes on its own (the files carry their own modes), so
// this warns rather than refusing — matching ssh, which complains about
// ~/.ssh's mode without refusing every key inside it.
func WarnKeyDirPermissions(path string) {
	if err := CheckKeyDirPermissions(path); err != nil {
		permLog.Warnf("%v", err)
	}
}
