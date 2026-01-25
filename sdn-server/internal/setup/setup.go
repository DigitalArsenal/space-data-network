// Package setup provides first-time server setup security for the SDN server.
// It implements secure token-based initialization with time-limited access.
package setup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("sdn-setup")

// Errors
var (
	ErrSetupAlreadyComplete = errors.New("setup already complete")
	ErrSetupTokenExpired    = errors.New("setup token has expired")
	ErrSetupTokenInvalid    = errors.New("invalid setup token")
	ErrSetupNotStarted      = errors.New("setup mode not started")
)

const (
	// TokenLength is the length of the setup token in bytes (32 chars when hex encoded)
	TokenLength = 16

	// TokenExpiry is how long the setup token is valid
	TokenExpiry = 10 * time.Minute

	// SetupStateFile is the name of the file that indicates setup completion
	SetupStateFile = "setup_complete"

	// TokenHashFile stores the hash of the setup token
	TokenHashFile = "setup_token_hash"
)

// Manager handles first-time setup state and token verification.
type Manager struct {
	basePath       string
	tokenHash      []byte
	tokenCreatedAt time.Time
	tokenUsed      bool
	setupComplete  bool
	mu             sync.RWMutex
}

// NewManager creates a new setup manager.
func NewManager(basePath string) (*Manager, error) {
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create setup directory: %w", err)
	}

	m := &Manager{
		basePath: basePath,
	}

	// Check if setup is already complete
	if m.isSetupComplete() {
		m.setupComplete = true
		log.Info("Server setup already complete")
	}

	return m, nil
}

// IsSetupRequired returns true if first-time setup is needed.
func (m *Manager) IsSetupRequired() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.setupComplete
}

// IsSetupComplete returns true if setup has been completed.
func (m *Manager) IsSetupComplete() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.setupComplete
}

// StartSetupMode generates a new setup token and enters setup mode.
// Returns the plaintext token (only shown once).
func (m *Manager) StartSetupMode() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.setupComplete {
		return "", ErrSetupAlreadyComplete
	}

	// Generate cryptographically random token
	tokenBytes := make([]byte, TokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	// Format token as SETUP-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX
	token := formatToken(tokenBytes)

	// Store hash of token (not plaintext)
	hash := sha256.Sum256([]byte(token))
	m.tokenHash = hash[:]
	m.tokenCreatedAt = time.Now()
	m.tokenUsed = false

	// Persist token hash to disk for crash recovery
	if err := m.persistTokenHash(); err != nil {
		log.Warnf("Failed to persist token hash: %v", err)
	}

	log.Info("Setup mode started, token generated")
	return token, nil
}

// VerifyToken verifies the setup token and marks it as used.
func (m *Manager) VerifyToken(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.setupComplete {
		return ErrSetupAlreadyComplete
	}

	if len(m.tokenHash) == 0 {
		return ErrSetupNotStarted
	}

	// Check if token has expired
	if time.Since(m.tokenCreatedAt) > TokenExpiry {
		m.tokenHash = nil
		return ErrSetupTokenExpired
	}

	// Check if token was already used
	if m.tokenUsed {
		return ErrSetupTokenExpired
	}

	// Verify token hash using constant-time comparison
	providedHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(m.tokenHash, providedHash[:]) != 1 {
		return ErrSetupTokenInvalid
	}

	// Mark token as used
	m.tokenUsed = true
	log.Info("Setup token verified successfully")

	return nil
}

// CompleteSetup marks the setup as complete and cleans up.
func (m *Manager) CompleteSetup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.setupComplete {
		return ErrSetupAlreadyComplete
	}

	// Create setup complete marker file
	completePath := filepath.Join(m.basePath, SetupStateFile)
	if err := os.WriteFile(completePath, []byte(time.Now().UTC().Format(time.RFC3339)), 0600); err != nil {
		return fmt.Errorf("failed to mark setup complete: %w", err)
	}

	// Remove token hash file
	hashPath := filepath.Join(m.basePath, TokenHashFile)
	os.Remove(hashPath) // Ignore error - file may not exist

	m.setupComplete = true
	m.tokenHash = nil
	m.tokenUsed = true

	log.Info("Server setup completed successfully")
	return nil
}

// GetTokenExpiry returns when the current token expires.
func (m *Manager) GetTokenExpiry() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokenCreatedAt.Add(TokenExpiry)
}

// RemainingTime returns how much time is left before token expiry.
func (m *Manager) RemainingTime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	remaining := TokenExpiry - time.Since(m.tokenCreatedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// isSetupComplete checks if setup has been completed by looking for marker file.
func (m *Manager) isSetupComplete() bool {
	completePath := filepath.Join(m.basePath, SetupStateFile)
	_, err := os.Stat(completePath)
	return err == nil
}

// persistTokenHash saves the token hash to disk for crash recovery.
func (m *Manager) persistTokenHash() error {
	hashPath := filepath.Join(m.basePath, TokenHashFile)
	data := fmt.Sprintf("%s:%d", hex.EncodeToString(m.tokenHash), m.tokenCreatedAt.Unix())
	return os.WriteFile(hashPath, []byte(data), 0600)
}

// formatToken formats a byte slice as SETUP-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX
func formatToken(data []byte) string {
	hexStr := hex.EncodeToString(data)
	// Split into groups of 4
	var parts []string
	for i := 0; i < len(hexStr); i += 4 {
		end := i + 4
		if end > len(hexStr) {
			end = len(hexStr)
		}
		parts = append(parts, hexStr[i:end])
	}
	return "SETUP-" + joinParts(parts, "-")
}

func joinParts(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

// PrintSetupBanner prints the setup token in an ASCII art box.
func PrintSetupBanner(token string, listenAddr string) {
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  FIRST-TIME SETUP - Space Data Network Server")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("  Your one-time setup token (valid for 10 minutes):")
	fmt.Println()
	fmt.Printf("      %s\n", token)
	fmt.Println()
	fmt.Printf("  Open http://%s/setup in your browser\n", listenAddr)
	fmt.Println("  and enter this token to complete setup.")
	fmt.Println()
	fmt.Println("  WARNING: This token will only be shown once!")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()
}
