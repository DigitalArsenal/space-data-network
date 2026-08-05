// Package keys provides cryptographic key management for SDN servers.
//
// # Not the identity authority
//
// This package predates the hd-wallet identity system and is NOT where
// user/node identity is minted. The identity authority is the hd-wallet
// xpub: user identity lives in internal/auth.UserStore (keyed by xpub), and
// node identity lives in internal/node.IdentityBundle (derived from the
// same mnemonic/xpub). See GenerateIdentity's doc comment for the narrow,
// non-identity legacy uses this package remains legitimate for, and
// GenerateIdentityFromSeed for how a caller can bind this manager's on-disk
// identity to hd-wallet-derived material deterministically instead.
//
// # Overview
//
// The keys package handles generation, storage, and management of server
// identity keys. Each server has two key pairs:
//
//  1. Signing Key (Ed25519): Used for signing data and proving identity.
//     The public key serves as the server's identifier.
//
//  2. Encryption Key (X25519): Used for ECIES encryption of messages.
//     Enables end-to-end encrypted communication between nodes.
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
//  1. Encrypted Export: Keys are encrypted with AES-256-GCM using a password-
//     derived key (Argon2id). The backup is a JSON file that can be stored
//     securely or transferred.
//
//  2. Mnemonic Phrase: A BIP-39 style 24-word phrase can be generated for
//     offline backup. Note: The current implementation is simplified and
//     should use a proper BIP-39 library for production.
//
//  3. QR Code: The encrypted backup can be encoded for QR code generation,
//     enabling mobile backup scenarios.
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
// Export the authoritative mnemonic as an encrypted identity backup:
//
//	backup, _ := keys.EncryptIdentityBackup(mnemonic, "password")
//
// Import from backup:
//
//	mnemonic, err := keys.DecryptIdentityBackup(backup, "password")
package keys
