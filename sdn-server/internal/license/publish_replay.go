package license

// Replay protection for signed module publish / entitlement requests
// (security audit SEC-030).
//
// A request is accepted once: its (signer xpub, nonce) pair is recorded in a
// ledger persisted beside catalog.json, so a restart does not reopen the
// window. Entries are kept as long as the request could still pass the
// freshness check (issued_at within ModulePublishMaxClockSkew of the node
// clock), then pruned. Together with the signed audience (the target node's
// peer ID) this means a captured request can be replayed neither later, nor
// twice, nor on another node.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ModulePublishMaxClockSkew bounds how far a request's issued_at may be from
// the node clock, either way.
const ModulePublishMaxClockSkew = 5 * time.Minute

// modulePublishReplayFile is the ledger's name beside catalog.json.
const modulePublishReplayFile = "module-publish-replay.json"

// maxModulePublishReplayEntries bounds the ledger. Only admin-signed fresh
// requests reach it, so hitting the bound means something is wrong: refuse
// rather than forget nonces that are still inside the window.
const maxModulePublishReplayEntries = 100000

// ErrModulePublishReplay is returned for a request whose nonce was already
// used by the same signer.
var ErrModulePublishReplay = errors.New("module publish request was already applied (replayed nonce)")

// ModulePublishReplayStore is the persisted (signer, nonce) ledger.
type ModulePublishReplayStore struct {
	path string

	mu      sync.Mutex
	loaded  bool
	entries map[string]int64 // sha256(signer \x00 nonce) -> issued_at_ms
}

type modulePublishReplayLedger struct {
	Version int              `json:"version"`
	Entries map[string]int64 `json:"entries"`
}

// NewModulePublishReplayStore opens (lazily) the ledger at path.
func NewModulePublishReplayStore(path string) *ModulePublishReplayStore {
	return &ModulePublishReplayStore{path: path}
}

// Consume records (signerXPub, nonce) or refuses it when already recorded. It
// fails closed: an unreadable or unwritable ledger refuses the request.
func (s *ModulePublishReplayStore) Consume(signerXPub, nonce string, issuedAtMs int64, now time.Time) error {
	if s == nil || s.path == "" {
		return errors.New("module publish replay ledger is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	cutoff := now.Add(-ModulePublishMaxClockSkew - time.Minute).UnixMilli()
	for key, issued := range s.entries {
		if issued < cutoff {
			delete(s.entries, key)
		}
	}
	sum := sha256.Sum256([]byte(signerXPub + "\x00" + nonce))
	key := hex.EncodeToString(sum[:])
	if _, seen := s.entries[key]; seen {
		return ErrModulePublishReplay
	}
	if len(s.entries) >= maxModulePublishReplayEntries {
		return errors.New("module publish replay ledger is full; refusing until older entries expire")
	}
	s.entries[key] = issuedAtMs
	if err := s.saveLocked(); err != nil {
		delete(s.entries, key)
		return err
	}
	return nil
}

func (s *ModulePublishReplayStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.entries = make(map[string]int64)
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read module publish replay ledger: %w", err)
	default:
		var ledger modulePublishReplayLedger
		if err := json.Unmarshal(data, &ledger); err != nil || ledger.Version != 1 {
			return fmt.Errorf("module publish replay ledger %s is unreadable; refusing publishes until it is repaired or removed", s.path)
		}
		for key, issued := range ledger.Entries {
			s.entries[key] = issued
		}
	}
	s.loaded = true
	return nil
}

func (s *ModulePublishReplayStore) saveLocked() error {
	data, err := json.Marshal(modulePublishReplayLedger{Version: 1, Entries: s.entries})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create module publish replay ledger directory: %w", err)
	}
	if err := writeFileAtomic(s.path, data, 0600); err != nil {
		return fmt.Errorf("persist module publish replay ledger: %w", err)
	}
	return nil
}
