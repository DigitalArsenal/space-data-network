// Package audit provides tamper-evident audit logging for SDN servers.
//
// # Overview
//
// The audit package implements a hash-linked audit log chain, where each entry
// contains the SHA-256 hash of the previous entry. This creates a tamper-evident
// log that makes unauthorized modifications detectable.
//
// # Hash Chain
//
// Each audit log entry includes:
//   - Previous Hash: The SHA-256 hash of the previous entry (genesis hash for first entry)
//   - Entry Hash: The SHA-256 hash of the current entry's data + previous hash
//
// This creates a blockchain-like structure where any modification to historical
// entries will break the hash chain, making tampering detectable.
//
// # Event Types
//
// The package defines standard event types for common operations:
//   - admin.login, admin.logout: Authentication events
//   - admin.password_change: Security-sensitive changes
//   - peer.trust_change, peer.add, peer.remove: Peer management
//   - config.change: Configuration modifications
//   - key.generate, key.backup, key.restore: Key management
//   - setup.start, setup.complete: First-time setup
//   - server.start, server.stop: Server lifecycle
//
// # Severity Levels
//
//   - info: Normal operations
//   - warning: Potentially concerning events
//   - error: Failed operations
//   - critical: Security-critical events
//
// # Querying
//
// The audit log supports flexible querying:
//   - By event type
//   - By severity
//   - By actor (admin ID)
//   - By time range
//   - With pagination (limit/offset)
//
// # Chain Verification
//
// The VerifyChain method validates the entire audit log:
//
//	valid, err := logger.VerifyChain()
//	if !valid {
//	    // Tampering detected!
//	}
//
// # Usage
//
// Create logger:
//
//	logger, _ := audit.NewLogger("/path/to/data")
//
// Log an event:
//
//	logger.Log(audit.EventTypeAdminLogin, audit.SeverityInfo, "Admin logged in",
//	    adminID, clientIP, map[string]interface{}{"username": "admin"})
//
// Query events:
//
//	entries, _ := logger.Query(audit.QueryOptions{
//	    EventType: audit.EventTypeAdminLogin,
//	    Limit: 100,
//	})
//
// Verify chain integrity:
//
//	valid, _ := logger.VerifyChain()
package audit
