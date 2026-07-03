package trust

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Store persists the trust DAG in lightweight index tables in its own
// engine-backed database (journal-durable): the Graph enforces the acyclic
// invariant in memory, the Store is its durable row image.
type Store struct {
	db     *sql.DB
	closer func() error
}

// NewStore wraps an existing database handle (tests pass an in-memory
// sqlite DB). The caller retains ownership of db.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.initTables(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewStoreWithFlatSQL opens the trust index tables in a private
// engine-backed database next to the node's datastore (journal
// `trust.sdnj`) — no shared db file, no cross-subsystem contention.
func NewStoreWithFlatSQL(flatStore *storage.FlatSQLStore) (*Store, error) {
	db, closer, err := flatsqldrv.OpenStandalone(filepath.Join(filepath.Dir(flatStore.Path()), "trust.sdnj"))
	if err != nil {
		return nil, fmt.Errorf("trust: open index database: %w", err)
	}
	s := &Store{db: db, closer: closer}
	if err := s.initTables(); err != nil {
		closer()
		return nil, err
	}
	return s, nil
}

// Close releases the store's own database (no-op for wrapped DBs).
func (s *Store) Close() error {
	if s.closer != nil {
		return s.closer()
	}
	return nil
}

func (s *Store) initTables() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS trust_nodes (
			node_id TEXT PRIMARY KEY
		);
		CREATE TABLE IF NOT EXISTS trust_edges (
			truster TEXT NOT NULL,
			trustee TEXT NOT NULL,
			weight REAL NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (truster, trustee)
		);
		CREATE INDEX IF NOT EXISTS idx_trust_edges_trustee ON trust_edges(trustee);
	`)
	if err != nil {
		return fmt.Errorf("trust: init tables: %w", err)
	}
	return nil
}

// UpsertNode persists a node row (idempotent).
func (s *Store) UpsertNode(id string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO trust_nodes(node_id) VALUES (?)`, id)
	return err
}

// UpsertEdge persists an edge row. Callers MUST have inserted the edge into a
// Graph first (which enforces acyclicity) — the store is the durable image,
// not the invariant keeper.
func (s *Store) UpsertEdge(e Edge) error {
	if err := s.UpsertNode(e.Truster); err != nil {
		return err
	}
	if err := s.UpsertNode(e.Trustee); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO trust_edges(truster, trustee, weight, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(truster, trustee) DO UPDATE SET weight=excluded.weight, updated_at=excluded.updated_at
	`, e.Truster, e.Trustee, e.Weight, e.UpdatedAtMs)
	return err
}

// DeleteEdge removes an edge row.
func (s *Store) DeleteEdge(truster, trustee string) error {
	_, err := s.db.Exec(`DELETE FROM trust_edges WHERE truster = ? AND trustee = ?`, truster, trustee)
	return err
}

// DeleteNode removes a node row and every edge touching it.
func (s *Store) DeleteNode(id string) error {
	if _, err := s.db.Exec(`DELETE FROM trust_edges WHERE truster = ? OR trustee = ?`, id, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM trust_nodes WHERE node_id = ?`, id)
	return err
}

// LoadGraph rebuilds the in-memory DAG from the persisted rows. Every edge is
// re-validated through Graph.SetEdge, so a corrupted/cyclic row image fails
// loudly instead of silently producing a cyclic graph.
func (s *Store) LoadGraph() (*Graph, error) {
	g := NewGraph()
	nodeRows, err := s.db.Query(`SELECT node_id FROM trust_nodes`)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var id string
		if err := nodeRows.Scan(&id); err != nil {
			return nil, err
		}
		if err := g.AddNode(id); err != nil {
			return nil, err
		}
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}

	edgeRows, err := s.db.Query(`SELECT truster, trustee, weight, updated_at FROM trust_edges ORDER BY updated_at ASC, truster ASC, trustee ASC`)
	if err != nil {
		return nil, err
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var e Edge
		if err := edgeRows.Scan(&e.Truster, &e.Trustee, &e.Weight, &e.UpdatedAtMs); err != nil {
			return nil, err
		}
		if err := g.SetEdge(e); err != nil {
			return nil, fmt.Errorf("trust: persisted edge %s->%s invalid: %w", e.Truster, e.Trustee, err)
		}
	}
	if err := edgeRows.Err(); err != nil {
		return nil, err
	}
	return g, nil
}
