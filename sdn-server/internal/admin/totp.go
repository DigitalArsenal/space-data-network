// Package admin provides admin authentication and session management for SDN servers.
package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// TOTP settings per RFC 6238
	totpDigits   = 6
	totpPeriod   = 30 // seconds
	totpSkew     = 1  // allow +/- 1 period for clock drift
	totpIssuer   = "SpaceDataNetwork"
	totpSecretLen = 20 // 160 bits
)

// GenerateTOTPSetup generates a new TOTP secret and returns the secret
// and a provisioning URI for use with authenticator apps.
func GenerateTOTPSetup(username string) (secret, uri string, err error) {
	// Generate random secret
	secretBytes := make([]byte, totpSecretLen)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate TOTP secret: %w", err)
	}

	// Encode as base32 (standard for TOTP)
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

	// Build otpauth URI for QR code scanning
	// Format: otpauth://totp/ISSUER:ACCOUNT?secret=SECRET&issuer=ISSUER&algorithm=SHA1&digits=6&period=30
	uri = fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		totpIssuer, username, secret, totpIssuer, totpDigits, totpPeriod)

	return secret, uri, nil
}

// ValidateTOTP validates a TOTP code against a secret.
// It checks the current time window and allows +/- totpSkew periods for clock drift.
func ValidateTOTP(secret, code string) bool {
	if len(code) != totpDigits {
		return false
	}

	now := time.Now().Unix()
	currentCounter := now / totpPeriod

	// Check current and adjacent time windows
	for i := -int64(totpSkew); i <= int64(totpSkew); i++ {
		expected := generateTOTPCode(secret, currentCounter+i)
		if expected == code {
			return true
		}
	}

	return false
}

// validateTOTPAtTime validates a TOTP code at a specific time (for testing).
func validateTOTPAtTime(secret, code string, t time.Time) bool {
	if len(code) != totpDigits {
		return false
	}

	counter := t.Unix() / totpPeriod

	for i := -int64(totpSkew); i <= int64(totpSkew); i++ {
		expected := generateTOTPCode(secret, counter+i)
		if expected == code {
			return true
		}
	}

	return false
}

// GenerateTOTPCode generates the current TOTP code for a secret (for testing/display).
func GenerateTOTPCode(secret string) (string, error) {
	now := time.Now().Unix()
	counter := now / totpPeriod
	code := generateTOTPCode(secret, counter)
	if code == "" {
		return "", fmt.Errorf("failed to generate TOTP code")
	}
	return code, nil
}

// generateTOTPCodeAtTime generates the TOTP code at a specific time (for testing).
func generateTOTPCodeAtTime(secret string, t time.Time) string {
	counter := t.Unix() / totpPeriod
	return generateTOTPCode(secret, counter)
}

// generateTOTPCode generates a TOTP code for a given counter value.
// Implements RFC 4226 (HOTP) with RFC 6238 (TOTP) time-based counter.
func generateTOTPCode(secret string, counter int64) string {
	// Decode base32 secret
	secretUpper := strings.ToUpper(secret)
	// Try with and without padding
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretUpper)
	if err != nil {
		key, err = base32.StdEncoding.DecodeString(secretUpper)
		if err != nil {
			return ""
		}
	}

	// Convert counter to big-endian 8 bytes
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))

	// HMAC-SHA1
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	hash := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 section 5.4)
	offset := hash[len(hash)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// Modulo to get N digits
	code := truncated % uint32(math.Pow10(totpDigits))

	return fmt.Sprintf("%0*d", totpDigits, code)
}
