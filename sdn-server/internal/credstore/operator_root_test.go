package credstore

// The credential store resolves its root from the operator's key password the
// way the node identity does — SDN_KEY_PASSWORD > SDN_KEY_PASSWORD_FILE >
// security.key_password > security.key_password_file — so a node whose
// identity needs a password file (a Linux service account whose machine key
// is refused) has a credential store too, and a keystore that was sealed
// under the machine key keeps opening.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

func clearOperatorPasswordEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvKeyPassword, "")
	t.Setenv(config.EnvKeyPasswordFile, "")
}

func ownerOnlyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRootFollowsTheIdentityPasswordPrecedence pins every source and its rank.
func TestRootFollowsTheIdentityPasswordPrecedence(t *testing.T) {
	clearOperatorPasswordEnv(t)
	dir := t.TempDir()
	envFile := ownerOnlyFile(t, dir, "env-file", "root-from-env-file\n")
	cfgFile := ownerOnlyFile(t, dir, "cfg-file", "root-from-config-file")

	withFile := config.Default()
	withFile.Security.KeyPasswordFile = cfgFile
	if root, err := ResolveRoot(withFile, nil); err != nil || root != "root-from-config-file" {
		t.Fatalf("security.key_password_file: got %q, %v", root, err)
	}

	withInline := config.Default()
	withInline.Security.KeyPassword = "root-from-config-inline"
	if root, err := ResolveRoot(withInline, nil); err != nil || root != "root-from-config-inline" {
		t.Fatalf("security.key_password: got %q, %v", root, err)
	}

	t.Setenv(config.EnvKeyPasswordFile, envFile)
	if root, err := ResolveRoot(withFile, nil); err != nil || root != "root-from-env-file" {
		t.Fatalf("SDN_KEY_PASSWORD_FILE must beat the config: got %q, %v", root, err)
	}
	if root, err := ResolveRoot(nil, nil); err != nil || root != "root-from-env-file" {
		t.Fatalf("SDN_KEY_PASSWORD_FILE with no config: got %q, %v", root, err)
	}

	// SDN_KEY_PASSWORD wins, VERBATIM (untrimmed), exactly as it always has
	// been this store's root, so a keystore sealed under it keeps opening.
	t.Setenv(config.EnvKeyPassword, "  root-from-env-inline ")
	if root, err := ResolveRoot(withFile, nil); err != nil || root != "  root-from-env-inline " {
		t.Fatalf("SDN_KEY_PASSWORD must win verbatim: got %q, %v", root, err)
	}
}

// TestPasswordFileNodeHasACredentialStore is the reported symptom: with the
// identity's password in a file and NO usable machine key, the store opened
// under nothing and the node logged "credential store unavailable". Now it
// opens under the file, needs no node key or machine derivation, and its
// contents survive a restart.
func TestPasswordFileNodeHasACredentialStore(t *testing.T) {
	clearOperatorPasswordEnv(t)
	dir := t.TempDir()
	file := ownerOnlyFile(t, t.TempDir(), "key-password", strings.Repeat("ab", 32))
	t.Setenv(config.EnvKeyPasswordFile, file)

	st, err := OpenStore(dir, nil, cp(nodeKeyA))
	if err != nil {
		t.Fatalf("a password-file node must have a credential store: %v", err)
	}
	if err := st.Put(IDSpaceTrack, "operator@example.com", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(st.fallbacks) != 0 {
		t.Fatalf("a keystore that did not exist yet needs no machine-derived fallback, got %d", len(st.fallbacks))
	}

	// Restart, recorded in the config instead of the environment.
	t.Setenv(config.EnvKeyPasswordFile, "")
	cfg := config.Default()
	cfg.Security.KeyPasswordFile = file
	again, err := OpenStore(dir, cfg, cp(nodeKeyA))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := again.Reveal(IDSpaceTrack)
	if err != nil || got.Secret.Reveal() != "s3cr3t" {
		t.Fatalf("credential did not survive the restart: %+v, %v", got, err)
	}

	// Sealed under the FILE's password, not the machine: a store that
	// re-derives machine state does not open it.
	t.Setenv(config.EnvKeyPasswordFile, "")
	if machine, err := OpenStore(dir, nil, cp(nodeKeyA)); err == nil {
		if _, err := machine.Reveal(IDSpaceTrack); err == nil {
			t.Fatal("a keystore sealed under the password file must not open under the machine key")
		}
	}
}

// TestUnreadablePasswordFileFailsClosed: a configured file that cannot be
// read is an error, never a silent fall-through to the machine key.
func TestUnreadablePasswordFileFailsClosed(t *testing.T) {
	clearOperatorPasswordEnv(t)
	t.Setenv(config.EnvKeyPasswordFile, filepath.Join(t.TempDir(), "not-mounted"))
	if _, err := ResolveRoot(nil, cp(nodeKeyA)); err == nil {
		t.Fatal("an unreadable SDN_KEY_PASSWORD_FILE must not fall back to the machine key")
	}
	if _, err := OpenStore(t.TempDir(), nil, cp(nodeKeyA)); err == nil {
		t.Fatal("OpenStore must fail closed on an unreadable SDN_KEY_PASSWORD_FILE")
	}
}

// sealUnderMachine writes a keystore holding one credential under the
// machine root built from machine (a derivation generation's password) and
// nodeKeyA — what an earlier build left on a node with a password file.
func sealUnderMachine(t *testing.T, dir, machine string) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Skip("hostname unavailable")
	}
	root, err := DeriveRootKey(machine, cp(nodeKeyA), hostname)
	if err != nil {
		t.Fatal(err)
	}
	old, err := NewStore(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Put(IDSpaceTrack, "operator@example.com", "machine-sealed"); err != nil {
		t.Fatal(err)
	}
	return root
}

func passwordFileConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Security.KeyPasswordFile = ownerOnlyFile(t, t.TempDir(), "key-password", "operator-root")
	return cfg
}

func opensUnder(t *testing.T, dir, root string) bool {
	t.Helper()
	st, err := NewStore(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Reveal(IDSpaceTrack)
	return err == nil
}

// TestMachineSealedKeystoreStaysMachineSealedUnderAPasswordFile: before this
// store honoured SDN_KEY_PASSWORD_FILE and the config, a node with a password
// file and a usable machine key (a container from deployment/remote, say)
// sealed its credentials under the machine key. Upgrading must not orphan
// them, and must not re-seal them one way either: an earlier build — a
// rollback, or the update helper's automatic revert after a failed health
// gate — has to open the store after this build has read AND written it. An
// existing store stays sealed as it is, like the identity.
func TestMachineSealedKeystoreStaysMachineSealedUnderAPasswordFile(t *testing.T) {
	clearOperatorPasswordEnv(t)
	v4, derr := keys.DeriveDefaultPassword()
	if derr != nil {
		t.Skipf("v4 machine key refused on this host (%v); TestMachineSealedKeystoreMigratesWhereTheMachineKeyIsRefused covers it", derr)
	}
	dir := t.TempDir()
	machine := sealUnderMachine(t, dir, v4)

	upgraded, err := OpenStore(dir, passwordFileConfig(t), cp(nodeKeyA))
	if err != nil {
		t.Fatalf("open under the password file: %v", err)
	}
	got, err := upgraded.Reveal(IDSpaceTrack)
	if err != nil || got.Secret.Reveal() != "machine-sealed" {
		t.Fatalf("a machine-sealed keystore must keep opening: %+v, %v", got, err)
	}
	if err := upgraded.Put(IDSpaceTrack, "operator@example.com", "written-by-this-build"); err != nil {
		t.Fatal(err)
	}
	if !opensUnder(t, dir, machine) {
		t.Fatal("an earlier build (the machine root alone) no longer opens the store this build read and wrote: re-sealed one way")
	}
	if opensUnder(t, dir, "operator-root") {
		t.Fatal("the existing machine-sealed store was moved under the password")
	}

	// A later start under the same file keeps opening it.
	again, err := OpenStore(dir, passwordFileConfig(t), cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Reveal(IDSpaceTrack); err != nil || got.Secret.Reveal() != "written-by-this-build" {
		t.Fatalf("reopen: %+v, %v", got, err)
	}
}

// TestOlderMachineGenerationMigratesAsBefore: a store sealed under an older
// machine generation (v3) on a node with a password file and a usable v4
// key migrates to v4 exactly as it does with no password at all (the machine
// path, unchanged), not to the password.
func TestOlderMachineGenerationMigratesAsBefore(t *testing.T) {
	clearOperatorPasswordEnv(t)
	v4, derr := keys.DeriveDefaultPassword()
	if derr != nil {
		t.Skipf("v4 machine key refused on this host: %v", derr)
	}
	dir := t.TempDir()
	sealUnderMachine(t, dir, keys.DeriveV3Password())
	st, err := OpenStore(dir, passwordFileConfig(t), cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.Reveal(IDSpaceTrack); err != nil || got.Secret.Reveal() != "machine-sealed" {
		t.Fatalf("a v3-sealed store must open: %+v, %v", got, err)
	}
	hostname, _ := os.Hostname()
	v4root, err := DeriveRootKey(v4, cp(nodeKeyA), hostname)
	if err != nil {
		t.Fatal(err)
	}
	if !opensUnder(t, dir, v4root) || opensUnder(t, dir, "operator-root") {
		t.Fatal("a v3-sealed store must migrate to v4 (the machine path), not to the password")
	}
}

// TestMachineSealedKeystoreMigratesWhereTheMachineKeyIsRefused: where the
// machine key is now refused, no build opens the store under it any more;
// the machine generations that still derive are read fallbacks, and the
// first read re-seals under the operator password — the one way the
// credentials survive.
func TestMachineSealedKeystoreMigratesWhereTheMachineKeyIsRefused(t *testing.T) {
	clearOperatorPasswordEnv(t)
	dir := t.TempDir()
	sealUnderMachine(t, dir, keys.DeriveV3Password())
	old := deriveMachinePassword
	deriveMachinePassword = func() (string, error) {
		return "", errors.New("machine fingerprint (v4): /sys/class/dmi/id/product_uuid exists but cannot be read")
	}
	t.Cleanup(func() { deriveMachinePassword = old })

	st, err := OpenStore(dir, passwordFileConfig(t), cp(nodeKeyA))
	if err != nil {
		t.Fatalf("open under the password file: %v", err)
	}
	if got, err := st.Reveal(IDSpaceTrack); err != nil || got.Secret.Reveal() != "machine-sealed" {
		t.Fatalf("the credentials must survive: %+v, %v", got, err)
	}
	if !opensUnder(t, dir, "operator-root") {
		t.Fatal("with the machine key refused, the store must be re-sealed under the password")
	}
}

// TestMachineSealedKeystoreStillOpensWithoutAPassword: the control. With no
// operator password anywhere, nothing about the machine path changed.
func TestMachineSealedKeystoreStillOpensWithoutAPassword(t *testing.T) {
	clearOperatorPasswordEnv(t)
	if _, err := keys.DeriveDefaultPassword(); err != nil {
		t.Skipf("v4 machine key refused on this host: %v", err)
	}
	dir := t.TempDir()
	first, err := OpenStore(dir, config.Default(), cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Put(IDSpaceTrack, "operator@example.com", "machine-root"); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(dir, config.Default(), cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := second.Reveal(IDSpaceTrack); err != nil || got.Secret.Reveal() != "machine-root" {
		t.Fatalf("machine-sealed keystore under the machine key: %+v, %v", got, err)
	}
	root, err := ResolveRoot(config.Default(), cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NewStore(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Reveal(IDSpaceTrack); err != nil {
		t.Fatalf("still sealed under the machine root: %v", err)
	}
}

// TestInlinePasswordStillAttachesNoFallbacks: SDN_KEY_PASSWORD is the source
// this store always honoured, so a keystore under it was never machine-sealed
// and gets no machine-derived fallback — even when the file exists.
func TestInlinePasswordStillAttachesNoFallbacks(t *testing.T) {
	clearOperatorPasswordEnv(t)
	t.Setenv(config.EnvKeyPassword, "explicit-operator-secret")
	dir := t.TempDir()
	st, err := OpenStore(dir, nil, cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(IDSpaceTrack, "u", "s"); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(dir, nil, cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.fallbacks) != 0 {
		t.Fatalf("SDN_KEY_PASSWORD must attach no machine-derived fallbacks, got %d", len(again.fallbacks))
	}
}

// TestForeignKeystoreFailsClosedUnderAPasswordFile: the machine fallbacks a
// password-file node carries do not make a foreign keystore open.
func TestForeignKeystoreFailsClosedUnderAPasswordFile(t *testing.T) {
	clearOperatorPasswordEnv(t)
	dir := t.TempDir()
	foreign, err := NewStore(dir, "a-root-from-an-entirely-different-machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Put(IDSpaceTrack, "u", "s"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvKeyPasswordFile, ownerOnlyFile(t, t.TempDir(), "key-password", "operator-root"))
	st, err := OpenStore(dir, nil, cp(nodeKeyA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reveal(IDSpaceTrack); err == nil {
		t.Fatal("a foreign keystore must NOT open — fail closed")
	}
}
