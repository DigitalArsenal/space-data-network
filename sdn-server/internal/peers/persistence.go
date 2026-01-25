// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/mattn/go-sqlite3"
	"github.com/multiformats/go-multiaddr"
)

// SQLitePersistence provides SQLite-based persistence for the peer registry.
type SQLitePersistence struct {
	db   *sql.DB
	path string
}

// NewSQLitePersistence creates a new SQLite persistence provider.
func NewSQLitePersistence(dbPath string) (*SQLitePersistence, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	sp := &SQLitePersistence{
		db:   db,
		path: dbPath,
	}

	if err := sp.initialize(); err != nil {
		db.Close()
		return nil, err
	}

	return sp, nil
}

// initialize creates the required tables.
func (sp *SQLitePersistence) initialize() error {
	schema := `
	CREATE TABLE IF NOT EXISTS peers (
		id TEXT PRIMARY KEY,
		addrs TEXT,
		trust_level INTEGER NOT NULL DEFAULT 2,
		name TEXT,
		organization TEXT,
		groups TEXT,
		notes TEXT,
		added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_seen TIMESTAMP,
		last_connected TIMESTAMP,
		connection_count INTEGER DEFAULT 0,
		messages_received INTEGER DEFAULT 0,
		messages_sent INTEGER DEFAULT 0,
		bytes_received INTEGER DEFAULT 0,
		bytes_sent INTEGER DEFAULT 0,
		epm_data BLOB,
		vcard_data TEXT,
		metadata TEXT
	);

	CREATE TABLE IF NOT EXISTS peer_groups (
		name TEXT PRIMARY KEY,
		description TEXT,
		default_trust_level INTEGER NOT NULL DEFAULT 2,
		members TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		metadata TEXT
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_peers_trust_level ON peers(trust_level);
	CREATE INDEX IF NOT EXISTS idx_peers_organization ON peers(organization);
	CREATE INDEX IF NOT EXISTS idx_peers_last_seen ON peers(last_seen);
	`

	_, err := sp.db.Exec(schema)
	return err
}

// Save persists the peers and groups to the database.
func (sp *SQLitePersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	tx, err := sp.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Save peers
	for _, tp := range peers {
		addrsJSON, _ := json.Marshal(multiaddrsToStrings(tp.Addrs))
		groupsJSON, _ := json.Marshal(tp.Groups)
		metadataJSON, _ := json.Marshal(tp.Metadata)

		_, err := tx.Exec(`
			INSERT OR REPLACE INTO peers (
				id, addrs, trust_level, name, organization, groups, notes,
				added_at, last_seen, last_connected, connection_count,
				messages_received, messages_sent, bytes_received, bytes_sent,
				epm_data, vcard_data, metadata
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			tp.ID.String(),
			string(addrsJSON),
			int(tp.TrustLevel),
			tp.Name,
			tp.Organization,
			string(groupsJSON),
			tp.Notes,
			tp.AddedAt,
			nullableTime(tp.LastSeen),
			nullableTime(tp.LastConnected),
			tp.ConnectionCount,
			tp.MessagesReceived,
			tp.MessagesSent,
			tp.BytesReceived,
			tp.BytesSent,
			tp.EPMData,
			tp.VCardData,
			string(metadataJSON),
		)
		if err != nil {
			return err
		}
	}

	// Save groups
	for _, g := range groups {
		membersJSON, _ := json.Marshal(peerIDsToStrings(g.Members))
		metadataJSON, _ := json.Marshal(g.Metadata)

		_, err := tx.Exec(`
			INSERT OR REPLACE INTO peer_groups (
				name, description, default_trust_level, members, created_at, metadata
			) VALUES (?, ?, ?, ?, ?, ?)
		`,
			g.Name,
			g.Description,
			int(g.DefaultTrustLevel),
			string(membersJSON),
			g.CreatedAt,
			string(metadataJSON),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Load loads the peers and groups from the database.
func (sp *SQLitePersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	peers := make(map[peer.ID]*TrustedPeer)
	groups := make(map[string]*PeerGroup)

	// Load peers
	rows, err := sp.db.Query(`
		SELECT id, addrs, trust_level, name, organization, groups, notes,
			added_at, last_seen, last_connected, connection_count,
			messages_received, messages_sent, bytes_received, bytes_sent,
			epm_data, vcard_data, metadata
		FROM peers
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			idStr         string
			addrsJSON     string
			trustLevel    int
			name          sql.NullString
			organization  sql.NullString
			groupsJSON    string
			notes         sql.NullString
			addedAt       time.Time
			lastSeen      sql.NullTime
			lastConnected sql.NullTime
			connCount     int64
			msgsRecv      int64
			msgsSent      int64
			bytesRecv     int64
			bytesSent     int64
			epmData       []byte
			vcardData     sql.NullString
			metadataJSON  string
		)

		err := rows.Scan(
			&idStr, &addrsJSON, &trustLevel, &name, &organization, &groupsJSON, &notes,
			&addedAt, &lastSeen, &lastConnected, &connCount,
			&msgsRecv, &msgsSent, &bytesRecv, &bytesSent,
			&epmData, &vcardData, &metadataJSON,
		)
		if err != nil {
			continue
		}

		peerID, err := peer.Decode(idStr)
		if err != nil {
			continue
		}

		var addrStrs []string
		json.Unmarshal([]byte(addrsJSON), &addrStrs)

		var peerGroups []string
		json.Unmarshal([]byte(groupsJSON), &peerGroups)

		var metadata map[string]string
		json.Unmarshal([]byte(metadataJSON), &metadata)

		tp := &TrustedPeer{
			ID:               peerID,
			Addrs:            stringsToMultiaddrs(addrStrs),
			TrustLevel:       TrustLevel(trustLevel),
			Name:             name.String,
			Organization:     organization.String,
			Groups:           peerGroups,
			Notes:            notes.String,
			AddedAt:          addedAt,
			LastSeen:         lastSeen.Time,
			LastConnected:    lastConnected.Time,
			ConnectionCount:  connCount,
			MessagesReceived: msgsRecv,
			MessagesSent:     msgsSent,
			BytesReceived:    bytesRecv,
			BytesSent:        bytesSent,
			EPMData:          epmData,
			VCardData:        vcardData.String,
			Metadata:         metadata,
		}

		peers[peerID] = tp
	}

	// Load groups
	groupRows, err := sp.db.Query(`
		SELECT name, description, default_trust_level, members, created_at, metadata
		FROM peer_groups
	`)
	if err != nil {
		return peers, nil, err
	}
	defer groupRows.Close()

	for groupRows.Next() {
		var (
			name        string
			description sql.NullString
			trustLevel  int
			membersJSON string
			createdAt   time.Time
			metadataJSON string
		)

		err := groupRows.Scan(&name, &description, &trustLevel, &membersJSON, &createdAt, &metadataJSON)
		if err != nil {
			continue
		}

		var memberStrs []string
		json.Unmarshal([]byte(membersJSON), &memberStrs)

		var metadata map[string]string
		json.Unmarshal([]byte(metadataJSON), &metadata)

		g := &PeerGroup{
			Name:              name,
			Description:       description.String,
			DefaultTrustLevel: TrustLevel(trustLevel),
			Members:           stringsToPeerIDs(memberStrs),
			CreatedAt:         createdAt,
			Metadata:          metadata,
		}

		groups[name] = g
	}

	return peers, groups, nil
}

// Close closes the database connection.
func (sp *SQLitePersistence) Close() error {
	return sp.db.Close()
}

// Helper functions

func multiaddrsToStrings(addrs []multiaddr.Multiaddr) []string {
	strs := make([]string, len(addrs))
	for i, addr := range addrs {
		strs[i] = addr.String()
	}
	return strs
}

func stringsToMultiaddrs(strs []string) []multiaddr.Multiaddr {
	addrs := make([]multiaddr.Multiaddr, 0, len(strs))
	for _, s := range strs {
		if addr, err := multiaddr.NewMultiaddr(s); err == nil {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

func peerIDsToStrings(ids []peer.ID) []string {
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	return strs
}

func stringsToPeerIDs(strs []string) []peer.ID {
	ids := make([]peer.ID, 0, len(strs))
	for _, s := range strs {
		if id, err := peer.Decode(s); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func nullableTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// JSONFilePersistence provides simple JSON file-based persistence.
type JSONFilePersistence struct {
	path string
}

// NewJSONFilePersistence creates a new JSON file persistence provider.
func NewJSONFilePersistence(path string) *JSONFilePersistence {
	return &JSONFilePersistence{path: path}
}

// Save saves peers and groups to a JSON file.
func (jp *JSONFilePersistence) Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error {
	// Ensure directory exists
	dir := filepath.Dir(jp.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data := struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}{
		Peers:  make([]*TrustedPeer, 0, len(peers)),
		Groups: make([]*PeerGroup, 0, len(groups)),
	}

	for _, tp := range peers {
		data.Peers = append(data.Peers, tp)
	}
	for _, g := range groups {
		data.Groups = append(data.Groups, g)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(jp.path, jsonData, 0644)
}

// Load loads peers and groups from a JSON file.
func (jp *JSONFilePersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	peers := make(map[peer.ID]*TrustedPeer)
	groups := make(map[string]*PeerGroup)

	data, err := os.ReadFile(jp.path)
	if err != nil {
		if os.IsNotExist(err) {
			return peers, groups, nil
		}
		return nil, nil, err
	}

	var loaded struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}

	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, nil, err
	}

	for _, tp := range loaded.Peers {
		peers[tp.ID] = tp
	}
	for _, g := range loaded.Groups {
		groups[g.Name] = g
	}

	return peers, groups, nil
}
