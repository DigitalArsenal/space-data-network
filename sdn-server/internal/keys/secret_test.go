package keys

import "testing"

func TestEncryptDecryptSecretRoundTrip(t *testing.T) {
	secret := []byte{0x01, 0x02, 0x00, 0xff, 0xaa, 0x00, 0x10}
	enc, err := EncryptSecret(secret, "test-password")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("expected non-empty ciphertext")
	}

	got, err := DecryptSecret(enc, "test-password")
	if err != nil {
		t.Fatalf("DecryptSecret: %v", err)
	}
	if string(got) != string(secret) {
		t.Fatalf("round trip mismatch: got %x, want %x", got, secret)
	}
}

func TestDecryptSecretWrongPasswordFails(t *testing.T) {
	enc, err := EncryptSecret([]byte("super-secret-bytes"), "correct-password")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	if _, err := DecryptSecret(enc, "wrong-password"); err == nil {
		t.Fatal("expected decryption to fail with wrong password")
	}
}

func TestDecryptSecretCorruptedCiphertextFails(t *testing.T) {
	enc, err := EncryptSecret([]byte("super-secret-bytes"), "test-password")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	// Flip a byte inside the ciphertext region.
	corrupted := append([]byte(nil), enc...)
	corrupted[len(corrupted)-1] ^= 0xff

	if _, err := DecryptSecret(corrupted, "test-password"); err == nil {
		t.Fatal("expected decryption to fail on corrupted ciphertext")
	}
}

func TestDecryptSecretTooShortFails(t *testing.T) {
	if _, err := DecryptSecret([]byte("short"), "test-password"); err == nil {
		t.Fatal("expected error for undersized input")
	}
}
