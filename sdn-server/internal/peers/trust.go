// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// TrustLevel represents the trust level of a peer, aligned with the PGP/GPG
// ownertrust scale (WS12, alignment plan Phase C1): unknown / never /
// marginal / full / ultimate. Ultimate (5) is the numeric maximum and is
// reserved for the node's own identity (browser-user-is-the-node-key,
// Phase F) — it is never granted to a remote peer or session.
//
// # Numeric scale and legacy compatibility
//
// TrustLevel is persisted as a raw signed integer in two places outside
// this package's control: internal/peers' own FlatSQL PRR records (int8,
// see persistence.go) and internal/auth's user-session SQL column. Both
// key on the ORIGINAL 0-4 values (Untrusted=0 … Admin=4). Renumbering them
// would silently reinterpret every already-persisted trust assignment on
// upgrade, which we cannot migrate from this package (internal/auth is out
// of scope for this change). The legacy values are therefore kept
// numerically unchanged and simply reinterpreted/relabeled on the PGP
// scale; the only genuinely NEW numeric value is Ultimate(5). Never(-1) is
// also new — it deliberately falls OUTSIDE the legacy 0-4 range so it can
// never collide with a value any pre-WS12 build could have written.
//
// Legacy -> PGP mapping (also the deterministic legacy-migration target;
// see the const doc comments below for the per-value rationale):
//
//	Untrusted(0) -> Unknown(0)    no assertion made; fail-closed default (unchanged value)
//	Limited(1)   -> Marginal(1)   weakest positive assertion (unchanged value)
//	Standard(2)  -> (no PGP alias) between Marginal and Full; classifies as
//	                ">= Marginal" via Ownertrust() (unchanged value)
//	Trusted(3)   -> Full(3)       full confidence / elevated access (unchanged value)
//	Admin(4)     -> (no PGP alias, classifies as ">= Full") — see Admin's
//	                doc comment for why Admin maps to Full-strength rather
//	                than Ultimate (unchanged value)
//
// No legacy record can ever decode to Never(-1) or Ultimate(5); those are
// exclusively forward-assigned by post-WS12 code.
type TrustLevel int

const (
	// Never is deliberate, explicit distrust (PGP ownertrust "n"): a hard
	// veto. Unlike Unknown (no opinion), Never is a positive assertion
	// that this identity must NOT be trusted, and computed web-of-trust
	// validity (Phase C2) can never override it (fail-closed). Never has
	// no legacy equivalent — pre-WS12 code only ever expressed "blocked"
	// as Untrusted — so it is intentionally given a value (-1) outside
	// the legacy 0-4 persisted range: no existing stored record can ever
	// decode to Never, and no ambiguity is possible.
	Never TrustLevel = -1

	// Untrusted is the legacy name for "no positive trust assertion"; see
	// Unknown below for its PGP-scale alias. It is also the Go zero value
	// and the fail-closed default for peers absent from the registry
	// (strict mode) or with corrupted/out-of-range persisted trust bytes.
	Untrusted TrustLevel = 0
	// Limited is the legacy "read-only, rate-limited access" tier; see
	// Marginal below for its PGP-scale alias.
	Limited TrustLevel = 1
	// Standard is the legacy "normal peer, standard access" tier and the
	// default granted to unknown peers in non-strict mode. It has no
	// dedicated PGP-scale name: it sits strictly between Marginal and
	// Full, and Ownertrust() classifies it as ">= Marginal, < Full".
	Standard TrustLevel = 2
	// Trusted is the legacy "full access, priority routing" tier; see
	// Full below for its PGP-scale alias.
	Trusted TrustLevel = 3
	// Admin is the legacy "can manage other peers" tier: an OPERATIONAL
	// super-user privilege (admin API routes, session gating), not a
	// signing-confidence assertion. Every current call site grants it to
	// human operator sessions or ACL-managed peers, never to the node's
	// own key, so it deliberately does NOT alias Ultimate — doing so
	// would let a plain admin grant satisfy checks meant exclusively for
	// "this key IS my own node" (Phase F / Phase D auto-pin gating).
	// Admin(4) > Full(3) numerically, so it still satisfies every
	// "Full-or-better" web-of-trust check via Ownertrust().
	Admin TrustLevel = 4

	// Ultimate is the maximum trust level on the PGP ownertrust scale
	// (PGP "u"): this key IS the node's own identity — the
	// browser-user-is-the-node-key case (Phase F). New; no legacy stored
	// record can carry this value, and only Phase F code is expected to
	// assign it.
	Ultimate TrustLevel = 5

	// Unknown is the PGP-scale alias of Untrusted (PGP "-"/"o"): no trust
	// assertion has been made about this identity.
	Unknown = Untrusted
	// Marginal is the PGP-scale alias of Limited (PGP "m"): partial
	// confidence. Per PGP web-of-trust validity, >=3 marginal trusters
	// (or >=1 full/ultimate truster) computes a subject VALID.
	Marginal = Limited
	// Full is the PGP-scale alias of Trusted (PGP "f"): full confidence.
	// A single Full (or Admin, or Ultimate) truster alone computes a
	// subject VALID.
	Full = Trusted
)

// Ownertrust classifies a TrustLevel into one of the PGP ownertrust
// buckets, folding the legacy tiers that have no dedicated PGP alias
// (Standard, Admin) into the bucket their numeric value qualifies for.
// Used by the web-of-trust validity computation (Phase C2) to decide
// whether a truster's assigned level counts as a "marginal" or "full"
// vote.
type Ownertrust int

const (
	OwnertrustNever Ownertrust = iota
	OwnertrustUnknown
	OwnertrustMarginal
	OwnertrustFull
	OwnertrustUltimate
)

// String returns the PGP ownertrust bucket name.
func (o Ownertrust) String() string {
	switch o {
	case OwnertrustNever:
		return "never"
	case OwnertrustUnknown:
		return "unknown"
	case OwnertrustMarginal:
		return "marginal"
	case OwnertrustFull:
		return "full"
	case OwnertrustUltimate:
		return "ultimate"
	default:
		return "unknown"
	}
}

// Ownertrust classifies t. Standard falls into OwnertrustMarginal (it is
// strictly above Marginal's own value but below Full); Admin falls into
// OwnertrustFull (strictly above Full's value but below Ultimate) — see
// the Admin const doc comment for why it does not classify as Ultimate.
func (t TrustLevel) Ownertrust() Ownertrust {
	switch {
	case t <= Never:
		return OwnertrustNever
	case t < Marginal: // == Untrusted/Unknown
		return OwnertrustUnknown
	case t < Full: // Limited/Marginal or Standard
		return OwnertrustMarginal
	case t < Ultimate: // Trusted/Full or Admin
		return OwnertrustFull
	default: // Ultimate
		return OwnertrustUltimate
	}
}

// String returns the string representation of a TrustLevel, using the
// canonical PGP ownertrust name where one exists (Phase C1 alignment).
func (t TrustLevel) String() string {
	switch t {
	case Never:
		return "never"
	case Untrusted: // == Unknown
		return "unknown"
	case Limited: // == Marginal
		return "marginal"
	case Standard:
		return "standard"
	case Trusted: // == Full
		return "full"
	case Admin:
		return "admin"
	case Ultimate:
		return "ultimate"
	default:
		return "unknown"
	}
}

// ParseTrustLevel converts a string to a TrustLevel. Both the canonical
// PGP ownertrust names and the legacy names are accepted as input (so
// existing API clients/config/scripts that submit "untrusted", "limited",
// or "trusted" keep working); output formatting (String/JSON) always uses
// the canonical PGP name.
func ParseTrustLevel(s string) (TrustLevel, error) {
	switch s {
	case "never":
		return Never, nil
	case "unknown", "untrusted":
		return Untrusted, nil
	case "marginal", "limited":
		return Limited, nil
	case "standard":
		return Standard, nil
	case "full", "trusted":
		return Trusted, nil
	case "admin":
		return Admin, nil
	case "ultimate":
		return Ultimate, nil
	default:
		return Untrusted, errors.New("invalid trust level")
	}
}

// Web-of-trust validity thresholds and edge weights (Phase C2). A subject
// is computed VALID when its trusters, evaluated through the wired
// internal/trust.Graph, meet either bar: enough Marginal-or-better votes,
// or a single Full-or-better vote.
const (
	// MinMarginalTrusters is the PGP web-of-trust rule: >=3 marginal (or
	// better) trusters computes a subject VALID.
	MinMarginalTrusters = 3
	// MinFullTrusters is the PGP web-of-trust rule: >=1 full (or
	// ultimate) truster alone computes a subject VALID.
	MinFullTrusters = 1

	// MarginalEdgeWeight is the trust.Edge.Weight floor that counts as a
	// "marginal" vote in ComputeValidity.
	MarginalEdgeWeight = 0.5
	// FullEdgeWeight is the trust.Edge.Weight floor that counts as a
	// "full" (or ultimate) vote in ComputeValidity.
	FullEdgeWeight = 1.0
)

// EdgeWeight converts t into the [0,1] weight used by internal/trust.Edge
// when this node's own trust assignments are projected into the
// web-of-trust graph: Never/Unknown contribute zero (fail-safe — no bonus
// from an unassigned or explicitly-distrusted truster), Marginal-or-better
// (Limited/Standard) contributes MarginalEdgeWeight, and Full-or-better
// (Trusted/Admin/Ultimate) contributes FullEdgeWeight.
func (t TrustLevel) EdgeWeight() float64 {
	switch t.Ownertrust() {
	case OwnertrustFull, OwnertrustUltimate:
		return FullEdgeWeight
	case OwnertrustMarginal:
		return MarginalEdgeWeight
	default:
		return 0
	}
}

// ComputeValidity reports whether subject clears PGP web-of-trust validity
// given the edges of g: it counts, among subject's direct trusters, how
// many assert a Marginal-or-better vs. Full-or-better weight (see
// MarginalEdgeWeight/FullEdgeWeight), then applies the standard rule
// (>=MinMarginalTrusters marginal, OR >=MinFullTrusters full). A nil graph
// (or a subject with no trusters) is fail-safe: never valid, zero counts —
// exactly "no bonus" from missing graph data.
func ComputeValidity(g *trust.Graph, subject string) (valid bool, marginalCount, fullCount int) {
	if g == nil {
		return false, 0, 0
	}
	for _, truster := range g.Trusters(subject) {
		edge, ok := g.Edge(truster, subject)
		if !ok {
			continue
		}
		switch {
		case edge.Weight >= FullEdgeWeight:
			fullCount++
		case edge.Weight >= MarginalEdgeWeight:
			marginalCount++
		}
	}
	valid = fullCount >= MinFullTrusters || marginalCount >= MinMarginalTrusters
	return valid, marginalCount, fullCount
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

	// trustGraph is the optional web-of-trust DAG (Phase C2, internal/trust)
	// consulted by EffectiveTrustLevel to compute PGP web-of-trust validity
	// on top of direct assignments. nil (the default) means "no graph
	// wired" and every accessor degrades to exactly the pre-C2
	// direct-assignment behavior (fail-safe).
	trustGraph *trust.Graph

	// trustChangeHandlers are notified (see fireTrustChange) whenever a
	// peer's DIRECTLY ASSIGNED trust level changes via AddPeer, UpdatePeer,
	// SetTrustLevel, or RemovePeer. Phase D: auto-subscribe/auto-pin keys
	// off promotion to/demotion from Full (see IsFullyTrusted).
	trustChangeHandlers []TrustChangeHandler
}

// TrustChangeHandler is invoked when a peer's directly assigned trust level
// changes (old != newLevel). old/newLevel are DIRECT levels — the same
// value GetTrustLevel/SetTrustLevel operate on — not EffectiveTrustLevel,
// so handlers can reason about registry mutations without recomputing the
// web-of-trust graph on every event.
type TrustChangeHandler func(id peer.ID, old, newLevel TrustLevel)

// OnTrustChange registers a handler to be notified of future direct
// trust-level changes. Registration is synchronous (the handler is
// appended under the registry lock and this call returns immediately), but
// DISPATCH is asynchronous: fireTrustChange runs each handler in its own
// goroutine so a slow or network-bound handler (e.g. subscribing to a
// pubsub topic, kicking off a catch-up backfill) can never block a trust
// mutation call (AddPeer/UpdatePeer/SetTrustLevel/RemovePeer). Handlers
// registered with a nil value are ignored.
func (r *Registry) OnTrustChange(handler TrustChangeHandler) {
	if handler == nil {
		return
	}
	r.mu.Lock()
	r.trustChangeHandlers = append(r.trustChangeHandlers, handler)
	r.mu.Unlock()
}

// fireTrustChange dispatches old->newLevel to every registered
// TrustChangeHandler on its own goroutine per handler. A no-op transition
// (old == newLevel) is not dispatched at all, so callers that re-assert an
// already-current trust level (e.g. SetTrustLevel(id, Full) twice in a row)
// do not cause repeat side effects such as duplicate catch-up backfills.
func (r *Registry) fireTrustChange(id peer.ID, old, newLevel TrustLevel) {
	if old == newLevel {
		return
	}
	r.mu.RLock()
	handlers := make([]TrustChangeHandler, len(r.trustChangeHandlers))
	copy(handlers, r.trustChangeHandlers)
	r.mu.RUnlock()
	for _, h := range handlers {
		go h(id, old, newLevel)
	}
}

// unknownPeerDirectTrustLevel returns the DIRECT trust level GetTrustLevel
// would report for a peer.ID absent from the registry, i.e. the "before
// AddPeer" / "after RemovePeer" baseline used to compute trust-change
// events for those two mutations.
func (r *Registry) unknownPeerDirectTrustLevel() TrustLevel {
	if r.strictMode {
		return Untrusted
	}
	return Standard
}

// PersistenceProvider is an interface for persisting the registry.
type PersistenceProvider interface {
	Save(peers map[peer.ID]*TrustedPeer, groups map[string]*PeerGroup) error
	Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error)
}

// NewRegistry creates a new trusted peer registry.
func NewRegistry(strictMode bool, persistence PersistenceProvider) *Registry {
	if isNilPersistenceProvider(persistence) {
		persistence = nil
	}

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

func isNilPersistenceProvider(persistence PersistenceProvider) bool {
	if persistence == nil {
		return true
	}
	value := reflect.ValueOf(persistence)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Errors
var (
	ErrPeerNotFound       = errors.New("peer not found")
	ErrPeerAlreadyExists  = errors.New("peer already exists")
	ErrGroupNotFound      = errors.New("group not found")
	ErrGroupAlreadyExists = errors.New("group already exists")
	ErrInvalidPeerID      = errors.New("invalid peer ID")
	ErrInvalidTrustLevel  = errors.New("invalid trust level")
)

// AddPeer adds a peer to the registry.
func (r *Registry) AddPeer(tp *TrustedPeer) error {
	if tp.ID == "" {
		return ErrInvalidPeerID
	}

	r.mu.Lock()

	if _, exists := r.peers[tp.ID]; exists {
		r.mu.Unlock()
		return ErrPeerAlreadyExists
	}

	if tp.AddedAt.IsZero() {
		tp.AddedAt = time.Now()
	}

	old := r.unknownPeerDirectTrustLevel()
	r.peers[tp.ID] = tp
	r.save()
	r.mu.Unlock()

	r.fireTrustChange(tp.ID, old, tp.TrustLevel)
	return nil
}

// UpdatePeer updates an existing peer in the registry.
func (r *Registry) UpdatePeer(tp *TrustedPeer) error {
	if tp.ID == "" {
		return ErrInvalidPeerID
	}

	r.mu.Lock()

	existing, exists := r.peers[tp.ID]
	if !exists {
		r.mu.Unlock()
		return ErrPeerNotFound
	}

	old := existing.TrustLevel
	r.peers[tp.ID] = tp
	r.save()
	r.mu.Unlock()

	r.fireTrustChange(tp.ID, old, tp.TrustLevel)
	return nil
}

// RemovePeer removes a peer from the registry.
func (r *Registry) RemovePeer(id peer.ID) error {
	r.mu.Lock()

	existing, exists := r.peers[id]
	if !exists {
		r.mu.Unlock()
		return ErrPeerNotFound
	}

	old := existing.TrustLevel
	delete(r.peers, id)
	r.save()
	r.mu.Unlock()

	r.fireTrustChange(id, old, r.unknownPeerDirectTrustLevel())
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

	tp, exists := r.peers[id]
	if !exists {
		r.mu.Unlock()
		return ErrPeerNotFound
	}

	old := tp.TrustLevel
	tp.TrustLevel = level
	r.save()
	r.mu.Unlock()

	r.fireTrustChange(id, old, level)
	return nil
}

// GetTrustLevel returns the DIRECTLY ASSIGNED trust level for a peer,
// ignoring any web-of-trust graph (see EffectiveTrustLevel for the
// computed-validity-augmented accessor). Used for rate limiting / display
// where only the operator's own explicit assignment should matter.
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

// SetTrustGraph wires (or clears, with nil) the web-of-trust DAG consulted
// by EffectiveTrustLevel (Phase C2). Passing nil restores the exact
// pre-C2, direct-assignment-only behavior (fail-safe default).
func (r *Registry) SetTrustGraph(g *trust.Graph) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trustGraph = g
}

// TrustGraph returns the currently wired web-of-trust graph, or nil.
func (r *Registry) TrustGraph() *trust.Graph {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.trustGraph
}

// ComputedValidity reports whether id clears PGP web-of-trust validity per
// the wired trust graph (nil/empty graph => never valid; fail-safe, no
// bonus) along with the marginal/full truster counts that were tallied.
// See ComputeValidity for the underlying rule.
func (r *Registry) ComputedValidity(id peer.ID) (valid bool, marginalCount, fullCount int) {
	r.mu.RLock()
	g := r.trustGraph
	r.mu.RUnlock()
	return ComputeValidity(g, id.String())
}

// EffectiveTrustLevel is the LIVE trust decision: it folds computed
// web-of-trust validity (Phase C2) on top of the peer's direct assignment.
//
//   - A direct assignment of Never is a hard veto: computed validity can
//     NEVER override it (fail-closed on an explicit "do not trust").
//   - Otherwise, direct assignment is a FLOOR: if the peer also clears
//     computed web-of-trust validity (>=3 marginal trusters, or >=1
//     full/ultimate truster — see ComputeValidity), the effective level is
//     raised to at least Marginal. A direct assignment already at or above
//     Marginal is never lowered by this computation ("direct assignments
//     still win where higher").
//   - No trust graph wired (the default) degrades to exactly the direct
//     assignment, i.e. today's pre-C2 behavior (fail-safe).
//
// This is the accessor IsAllowed/IsTrusted/IsAdmin — and therefore the
// connection gater, pubsub gating, and admin ACL checks — consult.
func (r *Registry) EffectiveTrustLevel(id peer.ID) TrustLevel {
	direct := r.GetTrustLevel(id)
	if direct == Never {
		return direct
	}
	valid, _, _ := r.ComputedValidity(id)
	if valid && Marginal > direct {
		return Marginal
	}
	return direct
}

// IsValid reports computed PGP web-of-trust validity for id, independent
// of its direct assignment (Never still reports whatever ComputeValidity
// found; callers wanting the veto-aware decision should use
// EffectiveTrustLevel/IsAllowed/IsTrusted instead).
func (r *Registry) IsValid(id peer.ID) bool {
	valid, _, _ := r.ComputedValidity(id)
	return valid
}

// IsFullyTrusted reports whether id's EFFECTIVE trust (direct assignment
// augmented by computed validity) is Full or above (Trusted/Full, Admin,
// or Ultimate). Exposed for Phase D: auto-subscribe/auto-pin should key
// off this, not IsTrusted (which only checks direct-or-Marginal-raised
// trust).
func (r *Registry) IsFullyTrusted(id peer.ID) bool {
	return r.EffectiveTrustLevel(id) >= Full
}

// IsAllowed checks if a peer is allowed to connect.
func (r *Registry) IsAllowed(id peer.ID) bool {
	level := r.EffectiveTrustLevel(id)
	return level > Untrusted
}

// IsTrusted checks if a peer has Trusted/Full, Admin, or Ultimate level.
// Computed web-of-trust validity only raises a peer to Marginal (see
// EffectiveTrustLevel), so this remains a direct-assignment check unless
// the peer was already directly assigned Trusted or above.
func (r *Registry) IsTrusted(id peer.ID) bool {
	level := r.EffectiveTrustLevel(id)
	return level >= Trusted
}

// IsAdmin checks if a peer has Admin level. Computed validity never grants
// Admin (its floor is Marginal), so this is equivalent to a direct check.
func (r *Registry) IsAdmin(id peer.ID) bool {
	level := r.EffectiveTrustLevel(id)
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
		if err := r.persistence.Save(r.peers, r.groups); err != nil {
			log.Warnf("Failed to persist peer registry: %v", err)
		}
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
