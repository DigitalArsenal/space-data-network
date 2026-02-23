// Package setup provides first-time server setup security for the SDN server.
//
// # Overview
//
// The setup package implements a secure first-time setup flow for SDN servers.
// When a server starts for the first time (no identity key exists), it enters
// setup mode and generates a one-time setup token.
//
// # Security Model
//
// 1. Token Generation: A cryptographically random 32-character token is generated
//    and displayed in the terminal. The token is formatted as:
//    SETUP-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX
//
// 2. Token Storage: Only the SHA-256 hash of the token is stored, never the
//    plaintext. This prevents token theft from disk.
//
// 3. Token Expiry: The token expires after 10 minutes or first use, whichever
//    comes first.
//
// 4. Setup Completion: After token verification, the server generates:
//    - Ed25519 signing keypair for identity/signatures
//    - X25519 encryption keypair for secure communication
//    - Admin account with password authentication
//    - EPM (Entity Profile Message) for the server identity
//
// # Usage
//
// Create a new setup manager:
//
//	mgr, err := setup.NewManager("/path/to/data")
//
// Check if setup is required:
//
//	if mgr.IsSetupRequired() {
//	    token, _ := mgr.StartSetupMode()
//	    setup.PrintSetupBanner(token, "localhost:5001")
//	}
//
// Verify a setup token:
//
//	err := mgr.VerifyToken("SETUP-xxxx-xxxx-...")
//
// Complete setup:
//
//	err := mgr.CompleteSetup()
package setup
