package admin

import (
	"testing"
	"time"
)

func TestGenerateTOTPSetup(t *testing.T) {
	secret, uri, err := GenerateTOTPSetup("testuser")
	if err != nil {
		t.Fatalf("Failed to generate TOTP setup: %v", err)
	}

	if secret == "" {
		t.Error("Secret should not be empty")
	}

	if uri == "" {
		t.Error("URI should not be empty")
	}

	// URI should contain otpauth scheme
	if len(uri) < 10 || uri[:10] != "otpauth://" {
		t.Errorf("URI should start with otpauth://, got: %s", uri)
	}

	// URI should contain the username
	if !containsString(uri, "testuser") {
		t.Errorf("URI should contain username, got: %s", uri)
	}

	// URI should contain the issuer
	if !containsString(uri, "SpaceDataNetwork") {
		t.Errorf("URI should contain issuer, got: %s", uri)
	}
}

func TestTOTPCodeGeneration(t *testing.T) {
	// Use a known secret for reproducible tests
	secret := "JBSWY3DPEHPK3PXP" // base32 encoded "Hello!"

	// Generate code at a known time
	knownTime := time.Unix(1234567890, 0) // Fixed time
	code := generateTOTPCodeAtTime(secret, knownTime)

	if len(code) != totpDigits {
		t.Errorf("Code should be %d digits, got: %d (%s)", totpDigits, len(code), code)
	}

	// Same time should produce same code
	code2 := generateTOTPCodeAtTime(secret, knownTime)
	if code != code2 {
		t.Errorf("Same time should produce same code: %s vs %s", code, code2)
	}

	// Different time period should produce different code
	differentTime := knownTime.Add(60 * time.Second) // 2 periods later
	code3 := generateTOTPCodeAtTime(secret, differentTime)
	if code == code3 {
		t.Errorf("Different time periods should produce different codes: both got %s", code)
	}
}

func TestValidateTOTP(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"

	// Generate current code
	now := time.Now()
	currentCode := generateTOTPCodeAtTime(secret, now)

	// Should validate against current time
	if !validateTOTPAtTime(secret, currentCode, now) {
		t.Errorf("Current code should validate: %s", currentCode)
	}

	// Should validate within skew window
	pastTime := now.Add(-time.Duration(totpPeriod) * time.Second)
	pastCode := generateTOTPCodeAtTime(secret, pastTime)
	if !validateTOTPAtTime(secret, pastCode, now) {
		t.Errorf("Past code (within skew) should validate: %s", pastCode)
	}

	// Code from far in the future should not validate
	farFuture := now.Add(5 * time.Duration(totpPeriod) * time.Second)
	futureCode := generateTOTPCodeAtTime(secret, farFuture)
	if validateTOTPAtTime(secret, futureCode, now) {
		t.Errorf("Far future code should not validate: %s", futureCode)
	}

	// Wrong length should not validate
	if validateTOTPAtTime(secret, "123", now) {
		t.Error("Short code should not validate")
	}

	// Empty code should not validate
	if validateTOTPAtTime(secret, "", now) {
		t.Error("Empty code should not validate")
	}
}

func TestTOTPRoundTrip(t *testing.T) {
	// Generate a fresh secret
	secret, _, err := GenerateTOTPSetup("testuser")
	if err != nil {
		t.Fatalf("Failed to generate setup: %v", err)
	}

	// Generate code from that secret
	code, err := GenerateTOTPCode(secret)
	if err != nil {
		t.Fatalf("Failed to generate code: %v", err)
	}

	// Should validate
	if !ValidateTOTP(secret, code) {
		t.Errorf("Generated code should validate against own secret")
	}
}

func TestTOTPWithDifferentSecrets(t *testing.T) {
	secret1, _, _ := GenerateTOTPSetup("user1")
	secret2, _, _ := GenerateTOTPSetup("user2")

	code1, _ := GenerateTOTPCode(secret1)

	// Code from secret1 should not validate against secret2
	if ValidateTOTP(secret2, code1) {
		t.Error("Code from one secret should not validate against different secret")
	}
}

func TestInvalidSecret(t *testing.T) {
	// Invalid base32 should return empty code
	code := generateTOTPCode("not-valid-base32!!!", 12345)
	if code != "" {
		t.Errorf("Invalid secret should produce empty code, got: %s", code)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
