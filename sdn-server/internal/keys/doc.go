// Package keys provides cryptographic key management for SDN servers.
//
// # Overview
//
// The keys package handles generation, storage, and management of server
// identity keys. Each server has two key pairs:
//
// 1. Signing Key (Ed25519): Used for signing data and proving identity.
//    The public key serves as the server's identifier.
//
// 2. Encryption Key (X25519): Used for ECIES encryption of messages.
//    Enables end-to-end encrypted communication between nodes.
//
// # Key Storage
//
// Keys are stored in individual files with secure permissions (0600):
//   - signing_private.key
//   - signing_public.key
//   - encryption_private.key
//   - encryption_public.key
//
// # Backup and Recovery
//
// The package provides multiple backup options:
//
// 1. Encrypted Export: Keys are encrypted with AES-256-GCM using a password-
//    derived key (Argon2id). The backup is a JSON file that can be stored
//    securely or transferred.
//
// 2. Mnemonic Phrase: A BIP-39 style 24-word phrase can be generated for
//    offline backup. Note: The current implementation is simplified and
//    should use a proper BIP-39 library for production.
//
// 3. QR Code: The encrypted backup can be encoded for QR code generation,
//    enabling mobile backup scenarios.
//
// # Usage
//
// Generate new identity:
//
//	mgr, _ := keys.NewManager("/path/to/data")
//	identity, _ := mgr.GenerateIdentity()
//
// Load existing identity:
//
//	identity, _ := mgr.LoadIdentity()
//
// Sign data:
//
//	signature, _ := mgr.Sign(data)
//
// Export encrypted backup:
//
//	backup, _ := mgr.ExportEncrypted("password")
//
// Import from backup:
//
//	err := mgr.ImportEncrypted(backupJSON, "password")
package keys
