package keys

import "testing"

func TestIdentityBackupRoundTrip(t *testing.T) {
	phrase := "legal winner thank year wave sausage worth useful legal winner thank yellow"
	backup, err := EncryptIdentityBackup(phrase, "transport password")
	if err != nil {
		t.Fatalf("EncryptIdentityBackup: %v", err)
	}
	got, err := DecryptIdentityBackup(backup, "transport password")
	if err != nil {
		t.Fatalf("DecryptIdentityBackup: %v", err)
	}
	if got != phrase {
		t.Fatalf("restored phrase differs from identity root")
	}
	if _, err := DecryptIdentityBackup(backup, "wrong password"); err != ErrDecryptionFailed {
		t.Fatalf("wrong-password error = %v, want %v", err, ErrDecryptionFailed)
	}
}
