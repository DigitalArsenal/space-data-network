package license

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultPlan          = "free"
	defaultStatus        = entitlementStatusActive
	defaultEntitlementDB = "entitlements.db"
)

// EntitlementStore persists xpub subscription state.
type EntitlementStore struct {
	db *sql.DB
}

// NewEntitlementStore opens/creates the entitlement database.
func NewEntitlementStore(path string) (*EntitlementStore, error) {
	dbPath := strings.TrimSpace(path)
	if dbPath == "" {
		return nil, errors.New("entitlement db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create entitlement dir: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open entitlement db: %w", err)
	}
	store := &EntitlementStore{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *EntitlementStore) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS entitlements (
	xpub TEXT PRIMARY KEY,
	peer_id TEXT,
	plan TEXT NOT NULL DEFAULT 'free',
	status TEXT NOT NULL DEFAULT 'active',
	stripe_customer_id TEXT,
	stripe_subscription_id TEXT,
	expires_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entitlements_plan ON entitlements(plan);
CREATE INDEX IF NOT EXISTS idx_entitlements_status ON entitlements(status);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("init entitlement schema: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *EntitlementStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// GetEntitlement returns entitlement for xpub.
func (s *EntitlementStore) GetEntitlement(xpub string) (*Entitlement, error) {
	xpub = strings.TrimSpace(xpub)
	if xpub == "" {
		return nil, errors.New("xpub is required")
	}

	row := s.db.QueryRow(`
SELECT xpub, peer_id, plan, status, stripe_customer_id, stripe_subscription_id, expires_at, updated_at
FROM entitlements WHERE xpub = ?`, xpub)

	var ent Entitlement
	if err := row.Scan(
		&ent.XPub,
		&ent.PeerID,
		&ent.Plan,
		&ent.Status,
		&ent.StripeCustomerID,
		&ent.StripeSubscriptionID,
		&ent.ExpiresAt,
		&ent.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query entitlement: %w", err)
	}
	return &ent, nil
}

// GetOrCreateEntitlement returns current entitlement or creates an active free plan.
func (s *EntitlementStore) GetOrCreateEntitlement(xpub, peerID string) (*Entitlement, error) {
	ent, err := s.GetEntitlement(xpub)
	if err != nil {
		return nil, err
	}
	if ent != nil {
		return ent, nil
	}

	now := time.Now().Unix()
	newEnt := &Entitlement{
		XPub:      strings.TrimSpace(xpub),
		PeerID:    strings.TrimSpace(peerID),
		Plan:      defaultPlan,
		Status:    defaultStatus,
		ExpiresAt: 0,
		UpdatedAt: now,
	}
	if err := s.UpsertEntitlement(newEnt); err != nil {
		return nil, err
	}
	return newEnt, nil
}

// UpsertEntitlement inserts or updates entitlement.
func (s *EntitlementStore) UpsertEntitlement(ent *Entitlement) error {
	if ent == nil {
		return errors.New("entitlement is required")
	}
	ent.XPub = strings.TrimSpace(ent.XPub)
	if ent.XPub == "" {
		return errors.New("xpub is required")
	}
	ent.PeerID = strings.TrimSpace(ent.PeerID)
	if strings.TrimSpace(ent.Plan) == "" {
		ent.Plan = defaultPlan
	}
	if strings.TrimSpace(ent.Status) == "" {
		ent.Status = defaultStatus
	}
	ent.UpdatedAt = time.Now().Unix()

	_, err := s.db.Exec(`
INSERT INTO entitlements (
	xpub, peer_id, plan, status, stripe_customer_id, stripe_subscription_id, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(xpub) DO UPDATE SET
	peer_id = excluded.peer_id,
	plan = excluded.plan,
	status = excluded.status,
	stripe_customer_id = excluded.stripe_customer_id,
	stripe_subscription_id = excluded.stripe_subscription_id,
	expires_at = excluded.expires_at,
	updated_at = excluded.updated_at
`,
		ent.XPub,
		ent.PeerID,
		ent.Plan,
		ent.Status,
		ent.StripeCustomerID,
		ent.StripeSubscriptionID,
		ent.ExpiresAt,
		ent.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert entitlement: %w", err)
	}
	return nil
}

// IsActive returns true if entitlement status and expiry allow access.
func (e *Entitlement) IsActive(now time.Time) bool {
	if e == nil {
		return false
	}
	if e.Status != entitlementStatusActive {
		return false
	}
	if e.ExpiresAt <= 0 {
		return true
	}
	return now.Unix() < e.ExpiresAt
}
