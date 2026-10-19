package storage

// kubo_pin_ledger.go — the store's half of the node pin policy
// (internal/pinpolicy): the persisted $PIN and the Kubo pin ledger.
//
// The Kubo pin ledger is a separate fact from sdn_pin_ledger. A
// sdn_pin_ledger row says the store keeps a publication's bytes (retention);
// a sdn_kubo_pins row says a pin-policy scope added a CID and whether the
// policy holds a Kubo pin on it. Rows are journaled like every auxiliary
// table, so they come back with a re-derived control database.
//
// The $PIN itself is one size-prefixed FlatBuffer file next to the store's
// journals (pin-policy.pin), written atomically: operator state that no
// journal replay could reconstruct.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

const pinPolicyFileName = "pin-policy.pin"

func (s *FlatSQLStore) initKuboPinTable() error {
	existed, err := s.tableExists("sdn_kubo_pins")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_kubo_pins (
			class INTEGER NOT NULL,
			feed TEXT NOT NULL DEFAULT '',
			cid TEXT NOT NULL,
			byte_count INTEGER NOT NULL DEFAULT 0,
			pinned INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (class, feed, cid)
		)
	`); err != nil {
		return fmt.Errorf("create kubo pin ledger table: %w", err)
	}
	return s.createStartupIndex("sdn_kubo_pins", "idx_sdn_kubo_pins_cid", existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_kubo_pins_cid ON sdn_kubo_pins (cid)
	`)
}

// UpsertKuboPin records one pin-policy row.
func (s *FlatSQLStore) UpsertKuboPin(pin pinpolicy.Pin) error {
	if err := s.requireWritable("upsert kubo pin"); err != nil {
		return err
	}
	pin.Feed, pin.CID = strings.TrimSpace(pin.Feed), strings.TrimSpace(pin.CID)
	if pin.CID == "" {
		return errors.New("kubo pin CID is required")
	}
	if pin.At.IsZero() {
		pin.At = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyKuboPinUpsert(pin); err != nil {
		return err
	}
	return s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventKuboPinUpsert, KuboPin: &pin})
}

func (s *FlatSQLStore) applyKuboPinUpsert(pin pinpolicy.Pin) error {
	pinned := 0
	if pin.Pinned {
		pinned = 1
	}
	if _, err := s.auxWrite().Exec(`
		INSERT INTO sdn_kubo_pins (class, feed, cid, byte_count, pinned, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(class, feed, cid) DO UPDATE SET
			byte_count = CASE WHEN excluded.byte_count > 0 THEN excluded.byte_count ELSE sdn_kubo_pins.byte_count END,
			pinned = excluded.pinned,
			updated_at = excluded.updated_at
	`, int64(pin.Class), pin.Feed, pin.CID, pin.Bytes, pinned, unixOrZero(pin.At)); err != nil {
		return fmt.Errorf("upsert kubo pin: %w", err)
	}
	return nil
}

// DeleteKuboPin drops one pin-policy row.
func (s *FlatSQLStore) DeleteKuboPin(class pinpolicy.Class, feed, cid string) error {
	if err := s.requireWritable("delete kubo pin"); err != nil {
		return err
	}
	pin := pinpolicy.Pin{Class: class, Feed: strings.TrimSpace(feed), CID: strings.TrimSpace(cid)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyKuboPinDelete(pin); err != nil {
		return err
	}
	return s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventKuboPinDelete, KuboPin: &pin})
}

func (s *FlatSQLStore) applyKuboPinDelete(pin pinpolicy.Pin) error {
	if _, err := s.auxWrite().Exec(`DELETE FROM sdn_kubo_pins WHERE class = ? AND feed = ? AND cid = ?`,
		int64(pin.Class), pin.Feed, pin.CID); err != nil {
		return fmt.Errorf("delete kubo pin: %w", err)
	}
	return nil
}

// KuboPins lists every pin-policy row of a class.
func (s *FlatSQLStore) KuboPins(class pinpolicy.Class) ([]pinpolicy.Pin, error) {
	return s.queryKuboPins(`WHERE class = ?`, int64(class))
}

// KuboPinsByCID lists every pin-policy row on one CID.
func (s *FlatSQLStore) KuboPinsByCID(cid string) ([]pinpolicy.Pin, error) {
	return s.queryKuboPins(`WHERE cid = ?`, strings.TrimSpace(cid))
}

func (s *FlatSQLStore) queryKuboPins(where string, args ...any) ([]pinpolicy.Pin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT class, feed, cid, byte_count, pinned, updated_at FROM sdn_kubo_pins `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list kubo pins: %w", err)
	}
	defer rows.Close()
	var out []pinpolicy.Pin
	for rows.Next() {
		var class, byteCount, pinned, updatedAt int64
		var pin pinpolicy.Pin
		if err := rows.Scan(&class, &pin.Feed, &pin.CID, &byteCount, &pinned, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan kubo pin: %w", err)
		}
		pin.Class, pin.Bytes, pin.Pinned = pinpolicy.Class(class), byteCount, pinned != 0
		if updatedAt > 0 {
			pin.At = time.Unix(updatedAt, 0).UTC()
		}
		out = append(out, pin)
	}
	return out, rows.Err()
}

// LoadPinPolicy reads the persisted $PIN; nil when the node never changed
// its policy.
func (s *FlatSQLStore) LoadPinPolicy() ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.basePath, pinPolicyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// SavePinPolicy replaces the persisted $PIN (write, sync, rename).
func (s *FlatSQLStore) SavePinPolicy(frame []byte) error {
	if err := s.requireWritable("save pin policy"); err != nil {
		return err
	}
	final := filepath.Join(s.basePath, pinPolicyFileName)
	tmp, err := os.CreateTemp(s.basePath, ".pin-policy-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(frame); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, final)
}

var _ pinpolicy.Ledger = (*FlatSQLStore)(nil)
