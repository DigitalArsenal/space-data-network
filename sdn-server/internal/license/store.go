package license

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	entitlementStoreRecordKeyInfo = "space-data-network/license-store/v1"
	entitlementStoreIndexKeyInfo  = "space-data-network/license-store/index/v1"
	entitlementStoreKeyCheckName  = "key_check"
	entitlementStoreKeyCheckValue = "space-data-network/license-store/key-check/v1"
)

// EntitlementStore persists xpub subscription state.
type EntitlementStore struct {
	db        *sql.DB
	recordKey []byte
	indexKey  []byte
}

// NewEntitlementStore is retained only to make plaintext construction fail closed.
// Use NewEncryptedEntitlementStore with the provider wrapping key.
func NewEntitlementStore(path string) (*EntitlementStore, error) {
	_ = path
	return nil, errors.New("entitlement store requires NewEncryptedEntitlementStore with provider wrapping key")
}

// NewEncryptedEntitlementStore opens/creates the encrypted entitlement database.
func NewEncryptedEntitlementStore(path string, providerWrappingKey []byte) (*EntitlementStore, error) {
	dbPath := strings.TrimSpace(path)
	if dbPath == "" {
		return nil, errors.New("entitlement db path is required")
	}
	recordKey, indexKey, err := deriveEntitlementStoreKeys(providerWrappingKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		ZeroBytes(recordKey)
		ZeroBytes(indexKey)
		return nil, fmt.Errorf("create entitlement dir: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		ZeroBytes(recordKey)
		ZeroBytes(indexKey)
		return nil, fmt.Errorf("open entitlement db: %w", err)
	}
	store := &EntitlementStore{db: db, recordKey: recordKey, indexKey: indexKey}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		ZeroBytes(recordKey)
		ZeroBytes(indexKey)
		return nil, err
	}
	return store, nil
}

func (s *EntitlementStore) initSchema() error {
	if _, err := s.db.Exec(`PRAGMA secure_delete=ON`); err != nil {
		return fmt.Errorf("enable sqlite secure_delete: %w", err)
	}
	columns, err := s.tableColumns("entitlements")
	if err != nil {
		return err
	}
	if len(columns) > 0 && columns["xpub"] && !columns["record_ciphertext"] {
		if err := s.migratePlaintextEntitlements(); err != nil {
			return err
		}
	} else {
		if len(columns) > 0 && (!columns["xpub_hash"] || !columns["record_ciphertext"] || !columns["record_nonce"]) {
			return errors.New("entitlement table schema is not recognized")
		}
		if err := s.createEncryptedSchema(); err != nil {
			return err
		}
	}
	if err := s.ensureKeyCheck(); err != nil {
		return err
	}
	return nil
}

func (s *EntitlementStore) createEncryptedSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS entitlements (
	xpub_hash TEXT PRIMARY KEY,
	peer_id_hash TEXT,
	status_hint TEXT NOT NULL DEFAULT 'unknown',
	record_nonce BLOB NOT NULL,
	record_ciphertext BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entitlements_peer_id_hash ON entitlements(peer_id_hash);
CREATE INDEX IF NOT EXISTS idx_entitlements_status_hint ON entitlements(status_hint);
CREATE TABLE IF NOT EXISTS entitlement_store_meta (
	key TEXT PRIMARY KEY,
	nonce BLOB NOT NULL,
	ciphertext BLOB NOT NULL
);
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
	err := s.db.Close()
	ZeroBytes(s.recordKey)
	ZeroBytes(s.indexKey)
	return err
}

// GetEntitlement returns entitlement for xpub.
func (s *EntitlementStore) GetEntitlement(xpub string) (*Entitlement, error) {
	xpub = strings.TrimSpace(xpub)
	if xpub == "" {
		return nil, errors.New("xpub is required")
	}

	xpubHash := s.indexHash(xpub)
	row := s.db.QueryRow(`
SELECT record_nonce, record_ciphertext
FROM entitlements WHERE xpub_hash = ?`, xpubHash)

	var nonce, ciphertext []byte
	if err := row.Scan(&nonce, &ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query entitlement: %w", err)
	}
	ent, err := s.openEntitlementRecord(nonce, ciphertext, []byte(xpubHash))
	if err != nil {
		return nil, err
	}
	if ent.XPub != xpub {
		return nil, errors.New("entitlement record xpub does not match lookup key")
	}
	return ent, nil
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
	if err := normalizeEntitlement(ent, true); err != nil {
		return err
	}
	if err := s.upsertEncryptedEntitlement(ent); err != nil {
		return fmt.Errorf("upsert entitlement: %w", err)
	}
	return nil
}

func normalizeEntitlement(ent *Entitlement, updateTimestamp bool) error {
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
	if updateTimestamp || ent.UpdatedAt <= 0 {
		ent.UpdatedAt = time.Now().Unix()
	}
	return nil
}

func (s *EntitlementStore) upsertEncryptedEntitlement(ent *Entitlement) error {
	xpubHash := s.indexHash(ent.XPub)
	peerIDHash := ""
	if ent.PeerID != "" {
		peerIDHash = s.indexHash(ent.PeerID)
	}
	nonce, ciphertext, err := s.sealEntitlementRecord(ent, []byte(xpubHash))
	if err != nil {
		return err
	}
	statusHint := strings.TrimSpace(ent.Status)
	if statusHint == "" {
		statusHint = "unknown"
	}
	_, err = s.db.Exec(`
INSERT INTO entitlements (
	xpub_hash, peer_id_hash, status_hint, record_nonce, record_ciphertext, updated_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(xpub_hash) DO UPDATE SET
	peer_id_hash = excluded.peer_id_hash,
	status_hint = excluded.status_hint,
	record_nonce = excluded.record_nonce,
	record_ciphertext = excluded.record_ciphertext,
	updated_at = excluded.updated_at
`,
		xpubHash,
		peerIDHash,
		statusHint,
		nonce,
		ciphertext,
		ent.UpdatedAt,
	)
	return err
}

func deriveEntitlementStoreKeys(providerWrappingKey []byte) ([]byte, []byte, error) {
	if len(providerWrappingKey) != 32 {
		return nil, nil, fmt.Errorf("provider wrapping key must be 32 bytes, got %d", len(providerWrappingKey))
	}
	recordKey, err := deriveHKDFSHA256(providerWrappingKey, nil, []byte(entitlementStoreRecordKeyInfo), 32)
	if err != nil {
		return nil, nil, fmt.Errorf("derive entitlement record key: %w", err)
	}
	indexKey, err := deriveHKDFSHA256(providerWrappingKey, nil, []byte(entitlementStoreIndexKeyInfo), 32)
	if err != nil {
		ZeroBytes(recordKey)
		return nil, nil, fmt.Errorf("derive entitlement index key: %w", err)
	}
	return recordKey, indexKey, nil
}

func (s *EntitlementStore) indexHash(value string) string {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *EntitlementStore) sealEntitlementRecord(ent *Entitlement, aad []byte) ([]byte, []byte, error) {
	plaintext, err := json.Marshal(ent)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal entitlement: %w", err)
	}
	defer ZeroBytes(plaintext)
	return s.seal(plaintext, aad)
}

func (s *EntitlementStore) openEntitlementRecord(nonce, ciphertext, aad []byte) (*Entitlement, error) {
	plaintext, err := s.open(nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt entitlement: %w", err)
	}
	defer ZeroBytes(plaintext)
	var ent Entitlement
	if err := json.Unmarshal(plaintext, &ent); err != nil {
		return nil, fmt.Errorf("decode entitlement: %w", err)
	}
	return &ent, nil
}

func (s *EntitlementStore) seal(plaintext, aad []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(s.recordKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create entitlement cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create entitlement gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(cryptorand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate entitlement nonce: %w", err)
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

func (s *EntitlementStore) open(nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.recordKey)
	if err != nil {
		return nil, fmt.Errorf("create entitlement cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create entitlement gcm: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid entitlement nonce length: expected %d, got %d", gcm.NonceSize(), len(nonce))
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func (s *EntitlementStore) ensureKeyCheck() error {
	if err := s.createEncryptedSchema(); err != nil {
		return err
	}
	row := s.db.QueryRow(`SELECT nonce, ciphertext FROM entitlement_store_meta WHERE key = ?`, entitlementStoreKeyCheckName)
	var nonce, ciphertext []byte
	err := row.Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		nonce, ciphertext, err := s.seal([]byte(entitlementStoreKeyCheckValue), []byte(entitlementStoreKeyCheckName))
		if err != nil {
			return err
		}
		_, err = s.db.Exec(
			`INSERT INTO entitlement_store_meta (key, nonce, ciphertext) VALUES (?, ?, ?)`,
			entitlementStoreKeyCheckName,
			nonce,
			ciphertext,
		)
		if err != nil {
			return fmt.Errorf("write entitlement store key check: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read entitlement store key check: %w", err)
	}
	plaintext, err := s.open(nonce, ciphertext, []byte(entitlementStoreKeyCheckName))
	if err != nil {
		return fmt.Errorf("entitlement store key check failed: %w", err)
	}
	defer ZeroBytes(plaintext)
	if !bytes.Equal(plaintext, []byte(entitlementStoreKeyCheckValue)) {
		return errors.New("entitlement store key check failed")
	}
	return nil
}

func (s *EntitlementStore) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan %s schema: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s schema: %w", table, err)
	}
	return columns, nil
}

func (s *EntitlementStore) migratePlaintextEntitlements() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin entitlement migration: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
SELECT xpub, peer_id, plan, status, stripe_customer_id, stripe_subscription_id, expires_at, updated_at
FROM entitlements`)
	if err != nil {
		return fmt.Errorf("read plaintext entitlements: %w", err)
	}
	var entitlements []Entitlement
	for rows.Next() {
		var ent Entitlement
		if err := rows.Scan(
			&ent.XPub,
			&ent.PeerID,
			&ent.Plan,
			&ent.Status,
			&ent.StripeCustomerID,
			&ent.StripeSubscriptionID,
			&ent.ExpiresAt,
			&ent.UpdatedAt,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan plaintext entitlement: %w", err)
		}
		if err := normalizeEntitlement(&ent, false); err != nil {
			rows.Close()
			return fmt.Errorf("normalize plaintext entitlement: %w", err)
		}
		entitlements = append(entitlements, ent)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close plaintext entitlement rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read plaintext entitlement rows: %w", err)
	}

	if _, err := tx.Exec(`
CREATE TABLE entitlements_encrypted (
	xpub_hash TEXT PRIMARY KEY,
	peer_id_hash TEXT,
	status_hint TEXT NOT NULL DEFAULT 'unknown',
	record_nonce BLOB NOT NULL,
	record_ciphertext BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
`); err != nil {
		return fmt.Errorf("create encrypted entitlement migration table: %w", err)
	}
	insertStmt, err := tx.Prepare(`
INSERT INTO entitlements_encrypted (
	xpub_hash, peer_id_hash, status_hint, record_nonce, record_ciphertext, updated_at
) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare encrypted entitlement insert: %w", err)
	}
	defer insertStmt.Close()
	for i := range entitlements {
		ent := &entitlements[i]
		xpubHash := s.indexHash(ent.XPub)
		peerIDHash := ""
		if ent.PeerID != "" {
			peerIDHash = s.indexHash(ent.PeerID)
		}
		nonce, ciphertext, err := s.sealEntitlementRecord(ent, []byte(xpubHash))
		if err != nil {
			return fmt.Errorf("encrypt migrated entitlement: %w", err)
		}
		if _, err := s.openEntitlementRecord(nonce, ciphertext, []byte(xpubHash)); err != nil {
			return fmt.Errorf("verify migrated entitlement: %w", err)
		}
		statusHint := strings.TrimSpace(ent.Status)
		if statusHint == "" {
			statusHint = "unknown"
		}
		if _, err := insertStmt.Exec(xpubHash, peerIDHash, statusHint, nonce, ciphertext, ent.UpdatedAt); err != nil {
			return fmt.Errorf("insert migrated entitlement: %w", err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE entitlements`); err != nil {
		return fmt.Errorf("drop plaintext entitlement table: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE entitlements_encrypted RENAME TO entitlements`); err != nil {
		return fmt.Errorf("rename encrypted entitlement table: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_entitlements_peer_id_hash ON entitlements(peer_id_hash)`); err != nil {
		return fmt.Errorf("create peer hash index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_entitlements_status_hint ON entitlements(status_hint)`); err != nil {
		return fmt.Errorf("create status hint index: %w", err)
	}
	if _, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS entitlement_store_meta (
	key TEXT PRIMARY KEY,
	nonce BLOB NOT NULL,
	ciphertext BLOB NOT NULL
);
`); err != nil {
		return fmt.Errorf("create entitlement metadata table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit entitlement migration: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum migrated entitlement db: %w", err)
	}
	log.Printf("migrated %d plaintext entitlement records to encrypted storage", len(entitlements))
	return nil
}
