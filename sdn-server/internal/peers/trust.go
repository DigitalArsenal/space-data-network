// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// TrustLevel represents the trust level of a peer.
type TrustLevel int

const (
	// Untrusted - No connection allowed
	Untrusted TrustLevel = iota
	// Limited - Read-only, rate-limited access
	Limited
	// Standard - Normal peer with standard access
	Standard
	// Trusted - Full access with priority routing
	Trusted
	// Admin - Can manage other peers
	Admin
)

// String returns the string representation of a TrustLevel.
func (t TrustLevel) String() string {
	switch t {
	case Untrusted:
		return "untrusted"
	case Limited:
		return "limited"
	case Standard:
		return "standard"
	case Trusted:
		return "trusted"
	case Admin:
		return "admin"
	default:
		return "unknown"
	}
}

// ParseTrustLevel converts a string to a TrustLevel.
func ParseTrustLevel(s string) (TrustLevel, error) {
	switch s {
	case "untrusted":
		return Untrusted, nil
	case "limited":
		return Limited, nil
	case "standard":
		return Standard, nil
	case "trusted":
		return Trusted, nil
	case "admin":
		return Admin, nil
	default:
		return Untrusted, errors.New("invalid trust level")
	}
}

// MarshalJSON implements json.Marshaler for TrustLevel.
func (t TrustLevel) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// UnmarshalJSON implements json.Unmarshaler for TrustLevel.
func (t *TrustLevel) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	level, err := ParseTrustLevel(s)
	if err != nil {
		return err
	}
	*t = level
	return nil
}

// TrustedPeer represents a peer in the trusted registry.
type TrustedPeer struct {
	// ID is the libp2p peer ID
	ID peer.ID `json:"id"`

	// Addrs are the known multiaddresses for this peer
	Addrs []multiaddr.Multiaddr `json:"-"`

	// AddrsStrings is used for JSON serialization
	AddrsStrings []string `json:"addrs,omitempty"`

	// TrustLevel is the trust level assigned to this peer
	TrustLevel TrustLevel `json:"trust_level"`

	// Name is an optional human-readable name for the peer
	Name string `json:"name,omitempty"`

	// Organization is the organization/group this peer belongs to
	Organization string `json:"organization,omitempty"`

	// Groups are the groups this peer is a member of
	Groups []string `json:"groups,omitempty"`

	// Notes are optional notes about this peer
	Notes string `json:"notes,omitempty"`

	// AddedAt is when this peer was added to the registry
	AddedAt time.Time `json:"added_at"`

	// LastSeen is the last time we connected to this peer
	LastSeen time.Time `json:"last_seen,omitempty"`

	// LastConnected is the last time we successfully connected
	LastConnected time.Time `json:"last_connected,omitempty"`

	// ConnectionCount is the number of times we've connected
	ConnectionCount int64 `json:"connection_count"`

	// MessagesReceived is the total messages received from this peer
	MessagesReceived int64 `json:"messages_received"`

	// MessagesSent is the total messages sent to this peer
	MessagesSent int64 `json:"messages_sent"`

	// BytesReceived is the total bytes received from this peer
	BytesReceived int64 `json:"bytes_received"`

	// BytesSent is the total bytes sent to this peer
	BytesSent int64 `json:"bytes_sent"`

	// EPMData is the optional EPM (Entity Profile Message) for this peer
	EPMData []byte `json:"epm_data,omitempty"`

	// VCardData is the optional vCard representation
	VCardData string `json:"vcard_data,omitempty"`

	// Metadata is additional custom metadata
	Metadata map[string]string `json:"metadata,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for TrustedPeer.
func (tp *TrustedPeer) MarshalJSON() ([]byte, error) {
	type Alias TrustedPeer
	aux := &struct {
		ID           string   `json:"id"`
		AddrsStrings []string `json:"addrs,omitempty"`
		*Alias
	}{
		ID:    tp.ID.String(),
		Alias: (*Alias)(tp),
	}

	// Convert multiaddrs to strings
	aux.AddrsStrings = make([]string, len(tp.Addrs))
	for i, addr := range tp.Addrs {
		aux.AddrsStrings[i] = addr.String()
	}

	return json.Marshal(aux)
}

// UnmarshalJSON implements custom JSON unmarshaling for TrustedPeer.
func (tp *TrustedPeer) UnmarshalJSON(data []byte) error {
	type Alias TrustedPeer
	aux := &struct {
		ID           string   `json:"id"`
		AddrsStrings []string `json:"addrs,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(tp),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse peer ID
	peerID, err := peer.Decode(aux.ID)
	if err != nil {
		return err
	}
	tp.ID = peerID

	// Parse multiaddrs
	tp.Addrs = make([]multiaddr.Multiaddr, 0, len(aux.AddrsStrings))
	for _, addrStr := range aux.AddrsStrings {
		addr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			continue // Skip invalid addresses
		}
		tp.Addrs = append(tp.Addrs, addr)
	}

	return nil
}

// PeerGroup represents a group of peers for organization.
type PeerGroup struct {
	// Name is the unique name of the group
	Name string `json:"name"`

	// Description is an optional description
	Description string `json:"description,omitempty"`

	// DefaultTrustLevel is the default trust level for peers in this group
	DefaultTrustLevel TrustLevel `json:"default_trust_level"`

	// Members are the peer IDs in this group
	Members []peer.ID `json:"-"`

	// MembersStrings is used for JSON serialization
	MembersStrings []string `json:"members,omitempty"`

	// CreatedAt is when this group was created
	CreatedAt time.Time `json:"created_at"`

	// Metadata is additional custom metadata
	Metadata map[string]string `json:"metadata,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for PeerGroup.
func (pg *PeerGroup) MarshalJSON() ([]byte, error) {
	type Alias PeerGroup
	aux := &struct {
		MembersStrings []string `json:"members,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(pg),
	}

	aux.MembersStrings = make([]string, len(pg.Members))
	for i, m := range pg.Members {
		aux.MembersStrings[i] = m.String()
	}

	return json.Marshal(aux)
}

// UnmarshalJSON implements custom JSON unmarshaling for PeerGroup.
func (pg *PeerGroup) UnmarshalJSON(data []byte) error {
	type Alias PeerGroup
	aux := &struct {
		MembersStrings []string `json:"members,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(pg),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	pg.Members = make([]peer.ID, 0, len(aux.MembersStrings))
	for _, mStr := range aux.MembersStrings {
		peerID, err := peer.Decode(mStr)
		if err != nil {
			continue // Skip invalid peer IDs
		}
		pg.Members = append(pg.Members, peerID)
	}

	return nil
}

// ConnectionStats represents connection statistics for a peer.
type ConnectionStats struct {
	PeerID           peer.ID       `json:"peer_id"`
	Connected        bool          `json:"connected"`
	LastConnected    time.Time     `json:"last_connected,omitempty"`
	LastDisconnected time.Time     `json:"last_disconnected,omitempty"`
	ConnectionCount  int64         `json:"connection_count"`
	TotalUptime      time.Duration `json:"total_uptime"`
	CurrentUptime    time.Duration `json:"current_uptime,omitempty"`
	Latency          time.Duration `json:"latency,omitempty"`
	MessagesReceived int64         `json:"messages_received"`
	MessagesSent     int64         `json:"messages_sent"`
	BytesReceived    int64         `json:"bytes_received"`
	BytesSent        int64         `json:"bytes_sent"`
	RateLimited      bool          `json:"rate_limited"`
}

// Registry manages the trusted peer registry.
type Registry struct {
	mu          sync.RWMutex
	peers       map[peer.ID]*TrustedPeer
	groups      map[string]*PeerGroup
	strictMode  bool // Only connect to peers in registry
	persistence PersistenceProvider
}

// PersistenceProvider is an interface for persisting the registry.
type PersistenceProvider interface {
	Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error
	Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error)
}

// NewRegistry creates a new trusted peer registry.
func NewRegistry(strictMode bool, persistence PersistenceProvider) *Registry {
	r := &Registry{
		peers:       make(map[peer.ID]*TrustedPeer),
		groups:      make(map[string]*PeerGroup),
		strictMode:  strictMode,
		persistence: persistence,
	}

	// Load persisted data if available
	if persistence != nil {
		peers, groups, err := persistence.Load()
		if err == nil {
			r.peers = peers
			r.groups = groups
		}
	}

	return r
}

// Errors
var (
	ErrPeerNotFound      = errors.New("peer not found")
	ErrPeerAlreadyExists = errors.New("peer already exists")
	ErrGroupNotFound     = errors.New("group not found")
	ErrGroupAlreadyExists = errors.New("group already exists")
	ErrInvalidPeerID     = errors.New("invalid peer ID")
	ErrInvalidTrustLevel = errors.New("invalid trust level")
)

// AddPeer adds a peer to the registry.
func (r *Registry) AddPeer(tp *TrustedPeer) error {
	if tp.ID == "" {
		return ErrInvalidPeerID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.peers[tp.ID]; exists {
		return ErrPeerAlreadyExists
	}

	if tp.AddedAt.IsZero() {
		tp.AddedAt = time.Now()
	}

	r.peers[tp.ID] = tp
	r.save()
	return nil
}

// UpdatePeer updates an existing peer in the registry.
func (r *Registry) UpdatePeer(tp *TrustedPeer) error {
	if tp.ID == "" {
		return ErrInvalidPeerID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.peers[tp.ID]; !exists {
		return ErrPeerNotFound
	}

	r.peers[tp.ID] = tp
	r.save()
	return nil
}

// RemovePeer removes a peer from the registry.
func (r *Registry) RemovePeer(id peer.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.peers[id]; !exists {
		return ErrPeerNotFound
	}

	delete(r.peers, id)
	r.save()
	return nil
}

// GetPeer retrieves a peer from the registry.
func (r *Registry) GetPeer(id peer.ID) (*TrustedPeer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tp, exists := r.peers[id]
	if !exists {
		return nil, ErrPeerNotFound
	}

	return tp, nil
}

// ListPeers returns all peers in the registry.
func (r *Registry) ListPeers() []*TrustedPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peers := make([]*TrustedPeer, 0, len(r.peers))
	for _, tp := range r.peers {
		peers = append(peers, tp)
	}

	return peers
}

// ListPeersByTrustLevel returns peers with the given trust level.
func (r *Registry) ListPeersByTrustLevel(level TrustLevel) []*TrustedPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peers := make([]*TrustedPeer, 0)
	for _, tp := range r.peers {
		if tp.TrustLevel == level {
			peers = append(peers, tp)
		}
	}

	return peers
}

// ListPeersByGroup returns peers in the given group.
func (r *Registry) ListPeersByGroup(groupName string) []*TrustedPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	group, exists := r.groups[groupName]
	if !exists {
		return nil
	}

	peers := make([]*TrustedPeer, 0, len(group.Members))
	for _, memberID := range group.Members {
		if tp, exists := r.peers[memberID]; exists {
			peers = append(peers, tp)
		}
	}

	return peers
}

// SetTrustLevel updates the trust level for a peer.
func (r *Registry) SetTrustLevel(id peer.ID, level TrustLevel) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tp, exists := r.peers[id]
	if !exists {
		return ErrPeerNotFound
	}

	tp.TrustLevel = level
	r.save()
	return nil
}

// GetTrustLevel returns the trust level for a peer.
func (r *Registry) GetTrustLevel(id peer.ID) TrustLevel {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tp, exists := r.peers[id]
	if !exists {
		if r.strictMode {
			return Untrusted
		}
		return Standard // Default for unknown peers in non-strict mode
	}

	return tp.TrustLevel
}

// IsAllowed checks if a peer is allowed to connect.
func (r *Registry) IsAllowed(id peer.ID) bool {
	level := r.GetTrustLevel(id)
	return level > Untrusted
}

// IsTrusted checks if a peer has Trusted or Admin level.
func (r *Registry) IsTrusted(id peer.ID) bool {
	level := r.GetTrustLevel(id)
	return level >= Trusted
}

// IsAdmin checks if a peer has Admin level.
func (r *Registry) IsAdmin(id peer.ID) bool {
	level := r.GetTrustLevel(id)
	return level == Admin
}

// AddGroup creates a new peer group.
func (r *Registry) AddGroup(group *PeerGroup) error {
	if group.Name == "" {
		return errors.New("group name is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.groups[group.Name]; exists {
		return ErrGroupAlreadyExists
	}

	if group.CreatedAt.IsZero() {
		group.CreatedAt = time.Now()
	}

	r.groups[group.Name] = group
	r.save()
	return nil
}

// RemoveGroup removes a peer group.
func (r *Registry) RemoveGroup(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.groups[name]; !exists {
		return ErrGroupNotFound
	}

	delete(r.groups, name)
	r.save()
	return nil
}

// GetGroup retrieves a peer group.
func (r *Registry) GetGroup(name string) (*PeerGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	group, exists := r.groups[name]
	if !exists {
		return nil, ErrGroupNotFound
	}

	return group, nil
}

// ListGroups returns all peer groups.
func (r *Registry) ListGroups() []*PeerGroup {
	r.mu.RLock()
	defer r.mu.RUnlock()

	groups := make([]*PeerGroup, 0, len(r.groups))
	for _, g := range r.groups {
		groups = append(groups, g)
	}

	return groups
}

// AddPeerToGroup adds a peer to a group.
func (r *Registry) AddPeerToGroup(peerID peer.ID, groupName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	group, exists := r.groups[groupName]
	if !exists {
		return ErrGroupNotFound
	}

	tp, exists := r.peers[peerID]
	if !exists {
		return ErrPeerNotFound
	}

	// Check if already in group
	for _, m := range group.Members {
		if m == peerID {
			return nil // Already in group
		}
	}

	group.Members = append(group.Members, peerID)
	tp.Groups = append(tp.Groups, groupName)
	r.save()
	return nil
}

// RemovePeerFromGroup removes a peer from a group.
func (r *Registry) RemovePeerFromGroup(peerID peer.ID, groupName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	group, exists := r.groups[groupName]
	if !exists {
		return ErrGroupNotFound
	}

	tp, exists := r.peers[peerID]
	if !exists {
		return ErrPeerNotFound
	}

	// Remove from group members
	newMembers := make([]peer.ID, 0, len(group.Members))
	for _, m := range group.Members {
		if m != peerID {
			newMembers = append(newMembers, m)
		}
	}
	group.Members = newMembers

	// Remove from peer's groups
	newGroups := make([]string, 0, len(tp.Groups))
	for _, g := range tp.Groups {
		if g != groupName {
			newGroups = append(newGroups, g)
		}
	}
	tp.Groups = newGroups

	r.save()
	return nil
}

// UpdateStats updates connection statistics for a peer.
func (r *Registry) UpdateStats(id peer.ID, fn func(*TrustedPeer)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tp, exists := r.peers[id]
	if exists {
		fn(tp)
		r.save()
	}
}

// RecordConnection records a connection event for a peer.
func (r *Registry) RecordConnection(id peer.ID) {
	r.UpdateStats(id, func(tp *TrustedPeer) {
		tp.LastConnected = time.Now()
		tp.LastSeen = time.Now()
		tp.ConnectionCount++
	})
}

// RecordMessage records a message event for a peer.
func (r *Registry) RecordMessage(id peer.ID, sent bool, bytes int64) {
	r.UpdateStats(id, func(tp *TrustedPeer) {
		tp.LastSeen = time.Now()
		if sent {
			tp.MessagesSent++
			tp.BytesSent += bytes
		} else {
			tp.MessagesReceived++
			tp.BytesReceived += bytes
		}
	})
}

// SetStrictMode enables or disables strict mode.
func (r *Registry) SetStrictMode(strict bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strictMode = strict
	r.save()
}

// IsStrictMode returns whether strict mode is enabled.
func (r *Registry) IsStrictMode() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.strictMode
}

// GetTrustedAddrInfos returns AddrInfo for all trusted peers (for IPFS Peering.Peers).
func (r *Registry) GetTrustedAddrInfos() []peer.AddrInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]peer.AddrInfo, 0)
	for _, tp := range r.peers {
		if tp.TrustLevel >= Trusted && len(tp.Addrs) > 0 {
			infos = append(infos, peer.AddrInfo{
				ID:    tp.ID,
				Addrs: tp.Addrs,
			})
		}
	}

	return infos
}

// Export exports the registry to JSON.
func (r *Registry) Export() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data := struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}{
		Peers:  make([]*TrustedPeer, 0, len(r.peers)),
		Groups: make([]*PeerGroup, 0, len(r.groups)),
	}

	for _, tp := range r.peers {
		data.Peers = append(data.Peers, tp)
	}
	for _, g := range r.groups {
		data.Groups = append(data.Groups, g)
	}

	return json.MarshalIndent(data, "", "  ")
}

// Import imports peers and groups from JSON.
func (r *Registry) Import(data []byte, merge bool) error {
	var imported struct {
		Peers  []*TrustedPeer `json:"peers"`
		Groups []*PeerGroup   `json:"groups"`
	}

	if err := json.Unmarshal(data, &imported); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !merge {
		r.peers = make(map[peer.ID]*TrustedPeer)
		r.groups = make(map[string]*PeerGroup)
	}

	for _, tp := range imported.Peers {
		if merge {
			if _, exists := r.peers[tp.ID]; exists {
				continue // Skip existing
			}
		}
		r.peers[tp.ID] = tp
	}

	for _, g := range imported.Groups {
		if merge {
			if _, exists := r.groups[g.Name]; exists {
				continue // Skip existing
			}
		}
		r.groups[g.Name] = g
	}

	r.save()
	return nil
}

// save persists the registry if a persistence provider is configured.
func (r *Registry) save() {
	if r.persistence != nil {
		r.persistence.Save(r.peers, r.groups)
	}
}

// PeerCount returns the number of peers in the registry.
func (r *Registry) PeerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// GroupCount returns the number of groups in the registry.
func (r *Registry) GroupCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.groups)
}
