package credstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRoot   = "test-root-key-password-not-a-real-node-key"
	testUser   = "operator@example.com"
	testSecret = "hunter2-SPACETRACK-plaintext-canary"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir, testRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st, dir
}

// Round-trip: encrypt -> persist -> decrypt.
func TestStoreRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := st.Reveal(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if got.Username != testUser {
		t.Errorf("username = %q, want %q", got.Username, testUser)
	}
	if got.Secret.Reveal() != testSecret {
		t.Error("secret did not survive the round trip")
	}
	if got.VerifiedAt != nil {
		t.Error("a freshly stored credential must not be marked verified")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
}

// A second Store opened over the same dir with the same root key must decrypt —
// i.e. the credential genuinely persists across a daemon restart.
func TestStorePersistsAcrossReopen(t *testing.T) {
	st, dir := newTestStore(t)
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := NewStore(dir, testRoot)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Reveal(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Reveal after reopen: %v", err)
	}
	if got.Secret.Reveal() != testSecret {
		t.Error("secret did not survive reopen")
	}
}

// HARD REQUIREMENT: keystore file 0600, containing dir 0700.
func TestStoreFilePermissions(t *testing.T) {
	st, dir := newTestStore(t)

	di, err := os.Stat(filepath.Join(dir, credDirName))
	if err != nil {
		t.Fatalf("stat secrets dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != DirMode {
		t.Errorf("secrets dir mode = %#o, want %#o", perm, DirMode)
	}

	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fi, err := os.Stat(st.Path())
	if err != nil {
		t.Fatalf("stat keystore: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != FileMode {
		t.Errorf("keystore mode = %#o, want %#o", perm, FileMode)
	}
}

// HARD REQUIREMENT: the ciphertext on disk must not contain the plaintext.
func TestCiphertextOnDiskDoesNotContainPlaintext(t *testing.T) {
	st, _ := newTestStore(t)
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read keystore: %v", err)
	}
	if bytes.Contains(raw, []byte(testSecret)) {
		t.Fatal("SECURITY: plaintext secret found in the on-disk keystore")
	}
	if bytes.Contains(raw, []byte(testUser)) {
		t.Fatal("SECURITY: plaintext username found in the on-disk keystore")
	}
	if !bytes.HasPrefix(raw, credFileMagic) {
		t.Error("keystore missing envelope magic")
	}
	// salt(32) + nonce(24) + AEAD overhead must all be present.
	if len(raw) <= len(credFileMagic)+32+24 {
		t.Error("keystore suspiciously short: envelope may be missing salt/nonce")
	}
}

// Each write must draw a fresh random salt and nonce, so the same plaintext
// never produces the same ciphertext twice.
func TestEachWriteUsesFreshSaltAndNonce(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	second, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("SECURITY: identical ciphertext across writes — salt/nonce are being reused")
	}
}

// Fail closed: a keystore written under one root key must not decrypt under another.
func TestWrongRootKeyFailsClosed(t *testing.T) {
	st, dir := newTestStore(t)
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	wrong, err := NewStore(dir, "a-different-node-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := wrong.Reveal(IDSpaceTrack); err == nil {
		t.Fatal("SECURITY: keystore decrypted under the wrong root key")
	}
	// The error must not echo any credential material.
	if _, err := wrong.Reveal(IDSpaceTrack); err != nil {
		if strings.Contains(err.Error(), testSecret) {
			t.Fatal("SECURITY: error message leaked the secret")
		}
	}
}

// An empty root password must be refused rather than silently defaulting.
func TestEmptyRootPasswordRefused(t *testing.T) {
	if _, err := NewStore(t.TempDir(), ""); err == nil {
		t.Fatal("SECURITY: store constructed with an empty root key password")
	}
}

// HARD REQUIREMENT: the Secret type must never render its plaintext — not under
// encoding/json, not under any fmt verb. This is what makes an accidental log
// line or response-struct embed safe.
func TestSecretNeverRendersPlaintext(t *testing.T) {
	s := Secret(testSecret)

	j, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(j), testSecret) {
		t.Fatal("SECURITY: json.Marshal leaked the secret")
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", s),
		fmt.Sprintf("%s", s),
		fmt.Sprintf("%q", s),
		fmt.Sprintf("%#v", s),
		fmt.Sprintf("%x", s),
		fmt.Sprint(s),
		s.String(),
	} {
		if strings.Contains(rendered, testSecret) {
			t.Fatalf("SECURITY: fmt leaked the secret: %s", rendered)
		}
	}

	// A whole Credential struct — the shape most likely to be logged by accident.
	cred := Credential{Username: testUser, Secret: s, UpdatedAt: time.Now()}
	if out := fmt.Sprintf("%+v", cred); strings.Contains(out, testSecret) {
		t.Fatal("SECURITY: logging a Credential struct leaked the secret")
	}
	cj, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("Marshal cred: %v", err)
	}
	if strings.Contains(string(cj), testSecret) {
		t.Fatal("SECURITY: json.Marshal of a Credential leaked the secret")
	}

	// Reveal remains the one explicit escape hatch.
	if s.Reveal() != testSecret {
		t.Error("Reveal must return the plaintext")
	}
}

// Status is the only shape crossing the API boundary: it must carry no secret.
func TestStatusCarriesNoSecret(t *testing.T) {
	st, _ := newTestStore(t)
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	status, err := st.Status(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Configured {
		t.Error("want configured")
	}
	j, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(j)
	if strings.Contains(body, testSecret) {
		t.Fatal("SECURITY: Status serialization leaked the secret")
	}
	// The full username is PII-ish and is masked; only the masked form appears.
	if strings.Contains(body, testUser) {
		t.Fatal("SECURITY: Status serialization leaked the unmasked username")
	}
	if !strings.Contains(body, "o***@example.com") {
		t.Errorf("expected a masked username, got %s", body)
	}
}

func TestStatusNotConfigured(t *testing.T) {
	st, _ := newTestStore(t)
	status, err := st.Status(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Configured {
		t.Error("empty store must report not configured")
	}
	if status.UsernameMasked != "" {
		t.Error("no username should be reported when not configured")
	}
}

func TestReplaceAndClear(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Replace.
	if err := st.Put(IDSpaceTrack, "second@example.com", "second-secret"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := st.Reveal(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if got.Secret.Reveal() != "second-secret" || got.Username != "second@example.com" {
		t.Error("replace did not take effect")
	}
	// The superseded secret must not linger on disk.
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(raw, []byte(testSecret)) {
		t.Fatal("SECURITY: the replaced secret is still on disk")
	}

	// Clear.
	if err := st.Clear(IDSpaceTrack); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := st.Reveal(IDSpaceTrack); err == nil {
		t.Fatal("credential still readable after Clear")
	}
	status, err := st.Status(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Configured {
		t.Error("cleared credential still reports configured")
	}
	// Clearing an absent credential is a no-op, not an error.
	if err := st.Clear(IDSpaceTrack); err != nil {
		t.Errorf("Clear of absent credential: %v", err)
	}
}

// The store is generic: several named lanes coexist independently.
func TestMultipleNamedCredentials(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.Put(IDSpaceTrack, "st@example.com", "st-secret"); err != nil {
		t.Fatalf("Put spacetrack: %v", err)
	}
	if err := st.Put(IDEDCCPF, "edc@example.com", "edc-secret"); err != nil {
		t.Fatalf("Put edc: %v", err)
	}

	list, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 credentials, got %d", len(list))
	}
	if list[0].ID != IDEDCCPF || list[1].ID != IDSpaceTrack {
		t.Errorf("List not sorted by id: %v", list)
	}
	j, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, secret := range []string{"st-secret", "edc-secret"} {
		if strings.Contains(string(j), secret) {
			t.Fatal("SECURITY: List serialization leaked a secret")
		}
	}

	// Clearing one lane must not disturb the other.
	if err := st.Clear(IDSpaceTrack); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := st.Reveal(IDEDCCPF); err != nil {
		t.Errorf("clearing spacetrack destroyed the edc credential: %v", err)
	}
}

func TestMarkVerified(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.MarkVerified(IDSpaceTrack, time.Now()); err == nil {
		t.Error("MarkVerified on an absent credential should fail")
	}
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	now := time.Now().UTC()
	if err := st.MarkVerified(IDSpaceTrack, now); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	status, err := st.Status(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.VerifiedAt == nil {
		t.Fatal("VerifiedAt not recorded")
	}

	// Replacing the secret must invalidate the prior verification.
	if err := st.Put(IDSpaceTrack, testUser, "a-new-secret"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	status, err = st.Status(IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.VerifiedAt != nil {
		t.Error("replacing the secret must reset verified state to unverified")
	}
}

func TestPutValidation(t *testing.T) {
	st, _ := newTestStore(t)

	for name, tc := range map[string]struct{ id, user, secret string }{
		"empty id":       {"", testUser, testSecret},
		"empty username": {IDSpaceTrack, "  ", testSecret},
		"empty secret":   {IDSpaceTrack, testUser, ""},
	} {
		if err := st.Put(tc.id, tc.user, tc.secret); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

// A corrupt or foreign keystore must fail closed rather than be misparsed.
func TestUnrecognizedFileFailsClosed(t *testing.T) {
	st, _ := newTestStore(t)
	if err := os.WriteFile(st.Path(), []byte(`{"spacetrack":{"secret":"plain"}}`), FileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := st.Reveal(IDSpaceTrack); err == nil {
		t.Fatal("SECURITY: a plaintext JSON keystore was accepted")
	}
}

func TestMaskUsername(t *testing.T) {
	for in, want := range map[string]string{
		"operator@example.com": "o***@example.com",
		"someuser":             "s***",
		"a@b.io":               "a***@b.io",
		"":                     "",
	} {
		if got := MaskUsername(in); got != want {
			t.Errorf("MaskUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

// The derived keystore passphrase must not equal the root password: a leak of
// one must not directly hand over the other.
func TestKeystorePassphraseIsDomainSeparated(t *testing.T) {
	pass, err := derivePassphrase(testRoot)
	if err != nil {
		t.Fatalf("derivePassphrase: %v", err)
	}
	if string(pass) == testRoot {
		t.Fatal("SECURITY: keystore passphrase is the raw root password")
	}
	if bytes.Contains(pass, []byte(testRoot)) {
		t.Fatal("SECURITY: keystore passphrase embeds the root password")
	}
	again, err := derivePassphrase(testRoot)
	if err != nil {
		t.Fatalf("derivePassphrase: %v", err)
	}
	if !bytes.Equal(pass, again) {
		t.Fatal("derivation is not deterministic — the keystore would be unreadable after restart")
	}
}
