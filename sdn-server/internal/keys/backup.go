// Package keys provides cryptographic key management for SDN servers.
package keys

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidBackup    = errors.New("invalid backup data")
	ErrDecryptionFailed = errors.New("failed to decrypt backup - wrong password?")
	ErrInvalidMnemonic  = errors.New("invalid mnemonic phrase")
)

// EncryptIdentityBackup seals the node's authoritative BIP-39 mnemonic for transport.
func EncryptIdentityBackup(mnemonic, password string) (string, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return "", ErrInvalidMnemonic
	}
	sealed, err := EncryptMnemonic(mnemonic, password)
	if err != nil {
		return "", fmt.Errorf("encrypt identity backup: %w", err)
	}
	envelope := struct {
		Version    int    `json:"version"`
		Kind       string `json:"kind"`
		Ciphertext string `json:"ciphertext"`
	}{1, "sdn-node-mnemonic", base64.StdEncoding.EncodeToString(sealed)}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal identity backup: %w", err)
	}
	return string(encoded), nil
}

// DecryptIdentityBackup opens an identity backup and returns its mnemonic.
func DecryptIdentityBackup(backup, password string) (string, error) {
	var envelope struct {
		Version    int    `json:"version"`
		Kind       string `json:"kind"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal([]byte(backup), &envelope); err != nil || envelope.Version != 1 || envelope.Kind != "sdn-node-mnemonic" {
		return "", ErrInvalidBackup
	}
	sealed, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return "", ErrInvalidBackup
	}
	mnemonic, err := DecryptMnemonic(sealed, password)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return "", ErrInvalidMnemonic
	}
	return mnemonic, nil
}
