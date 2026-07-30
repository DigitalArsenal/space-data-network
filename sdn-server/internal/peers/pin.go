package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PIN — the operator's answer to "why is this row here?"
//
// OWNER RULING 2026-07-30, verbatim: "The Peers table should not ever show
// peers that have never been seen, UNLESS they have been added manually and
// 'pinned' (we need an interface for that). When a peer drops off the network
// it should just disappear."
//
// A pin is therefore the ONLY reason an unreachable peer keeps a seat on the
// peer board. Everything else on that board is there because it is connected
// right now, and vanishes the moment it is not (epm.BuildObservedSDNPeers).
//
// WHY A SEPARATE, PURPOSE-BUILT STORE rather than turning on registry
// persistence: the registry admits a row for every peer that merely completes
// an EPM exchange (epm/protocol.go:170) or is discovered through DHT rendezvous
// (node/advertisement_discovery.go:279). On a node with a documented inbound
// flood (task sdn-inbound-junk-flood-policy: 1095 distinct IPs refilling a 1152
// ceiling in ~65 minutes) persisting THAT is an unbounded file written from the
// connection path. Pins are operator-authored, bounded, and few — so they get
// their own small file and the registry stays in memory.
//
// Live-state note (measured 2026-07-30, host-01): peers.registry_path is
// "/opt/data/sdn-module-delivery/peers.db", and a ".db" suffix is REFUSED as a
// legacy sidecar (node.go:382) — so the live node had NO peer persistence of
// any kind. A pin that does not survive a restart is not a pin, which is why
// PinPathFor derives a real JSON path instead of inheriting that refusal.

const (
	// PinSourceConfig marks a pin declared in the node's config file under
	// peers.trusted_peers. It is owned by that file and is NOT removable
	// through the API — the operator edits the file.
	PinSourceConfig = "config"
	// PinSourceOperator marks a pin created through the pin API.
	PinSourceOperator = "pinned"

	// pinFileName is the pin store's file name inside the node's data dir.
	pinFileName = "peer-pins.json"

	// maxPins bounds the store. Pins are hand-authored; a five-figure pin
	// file means something is writing them that should not be.
	maxPins = 4096
)

// ErrPinNotFound is returned when unpinning a peer that is not pinned.
var ErrPinNotFound = errors.New("peers: peer is not pinned")

// ErrConfigPin is returned when the API is asked to remove a pin that comes
// from the config file. The UI must say where to change it instead of failing
// silently — the row is locked because a REAL file and key own it.
var ErrConfigPin = errors.New("peers: pin is declared in the node config file")

// ErrPinLimit is returned when the pin store is full.
var ErrPinLimit = errors.New("peers: pin limit reached")

// Pin is one operator- or config-declared peer that keeps its seat on the peer
// board whether or not it is reachable.
//
// JSON keys are lowercase: these are API-synthesized fields, not SDS record
// fields (standing capitalization rule — SDS keys match IDL capitalization
// exactly, synthesized fields stay lowercase).
type Pin struct {
	PeerID   string    `json:"peer_id"`
	Addrs    []string  `json:"addrs,omitempty"`
	Name     string    `json:"name,omitempty"`
	Note     string    `json:"note,omitempty"`
	PinnedAt time.Time `json:"pinned_at"`
	PinnedBy string    `json:"pinned_by,omitempty"`
	// Source is PinSourceConfig or PinSourceOperator.
	Source string `json:"source"`
}

// PinStore holds the pinned peers. Operator pins are durable (a JSON file);
// config pins are re-declared from the config file on every boot and are never
// written to disk, so deleting a line from the config actually removes the pin.
type PinStore struct {
	mu   sync.RWMutex
	path string
	pins map[string]Pin
}

// PinPathFor derives the pin file path from the configured registry path.
//
// The registry path may be a legacy ".db" sidecar the registry itself refuses;
// pins do not inherit that refusal — they land in peer-pins.json beside it. An
// empty registry path yields an empty pin path, and the store then runs in
// memory (tests, ephemeral nodes) with every mutation still succeeding.
func PinPathFor(registryPath string) string {
	trimmed := strings.TrimSpace(registryPath)
	if trimmed == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(trimmed), pinFileName)
}

// NewPinStore opens (and creates the directory for) the pin store at path.
// A store with an empty path is in-memory only. A malformed or unreadable file
// is a hard error: silently starting with zero pins would drop peers the
// operator deliberately kept, which is the failure this whole task is about.
func NewPinStore(path string) (*PinStore, error) {
	store := &PinStore{path: strings.TrimSpace(path), pins: make(map[string]Pin)}
	if store.path == "" {
		return store, nil
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("peers: create pin store dir: %w", err)
	}
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peers: read pin store: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return store, nil
	}
	var pins []Pin
	if err := json.Unmarshal(data, &pins); err != nil {
		// PRESERVE, NEVER OVERWRITE. The caller may choose to carry on with an
		// empty store (node.go does, so a bad pin file cannot take the node
		// offline) — and if it does, the very next Pin() would write over this
		// file and destroy whatever the operator had kept. Move it aside first,
		// so "my pins vanished" is always recoverable from disk.
		aside := store.path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		if renameErr := os.Rename(store.path, aside); renameErr != nil {
			return nil, fmt.Errorf("peers: parse pin store %s: %w (and it could not be moved aside: %v)", store.path, err, renameErr)
		}
		return nil, fmt.Errorf("peers: parse pin store %s: %w (preserved at %s)", store.path, err, aside)
	}
	for _, pin := range pins {
		id := strings.TrimSpace(pin.PeerID)
		if id == "" {
			continue
		}
		// Only operator pins are ever persisted; a config pin found in the
		// file is stale (the config no longer declares it) and is dropped.
		if pin.Source != PinSourceOperator {
			continue
		}
		pin.PeerID = id
		store.pins[id] = pin
	}
	return store, nil
}

// DeclareConfigPin records a pin owned by the node's config file. note is the
// real file and key an operator can edit, so the UI never tells anyone to go
// change something that does not exist.
//
// A config declaration overrides an operator pin for the same peer: the file
// wins, and the row locks.
func (s *PinStore) DeclareConfigPin(id peer.ID, addrs []string, note string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins[id.String()] = Pin{
		PeerID:   id.String(),
		Addrs:    append([]string(nil), addrs...),
		Note:     strings.TrimSpace(note),
		PinnedAt: time.Now().UTC(),
		Source:   PinSourceConfig,
	}
}

// Pin records an operator pin and persists the store. Re-pinning an existing
// operator pin updates it in place.
func (s *PinStore) Pin(pin Pin) (Pin, error) {
	if s == nil {
		return Pin{}, errors.New("peers: nil pin store")
	}
	id := strings.TrimSpace(pin.PeerID)
	if id == "" {
		return Pin{}, errors.New("peers: pin requires a peer id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.pins[id]; ok && existing.Source == PinSourceConfig {
		return Pin{}, ErrConfigPin
	}
	if _, ok := s.pins[id]; !ok && len(s.pins) >= maxPins {
		return Pin{}, ErrPinLimit
	}
	pin.PeerID = id
	pin.Source = PinSourceOperator
	if pin.PinnedAt.IsZero() {
		pin.PinnedAt = time.Now().UTC()
	}
	s.pins[id] = pin
	if err := s.saveLocked(); err != nil {
		return Pin{}, err
	}
	return pin, nil
}

// Unpin removes an operator pin and persists the store. Config pins are
// refused with ErrConfigPin — they are removed by editing the config file.
func (s *PinStore) Unpin(id string) error {
	if s == nil {
		return ErrPinNotFound
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.pins[id]
	if !ok {
		return ErrPinNotFound
	}
	if existing.Source == PinSourceConfig {
		return ErrConfigPin
	}
	delete(s.pins, id)
	return s.saveLocked()
}

// Get returns the pin for a peer id, if any.
func (s *PinStore) Get(id string) (Pin, bool) {
	if s == nil {
		return Pin{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pin, ok := s.pins[strings.TrimSpace(id)]
	return pin, ok
}

// List returns every pin, config pins first, then by peer id — a stable order
// so the board does not reshuffle between frames.
func (s *PinStore) List() []Pin {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Pin, 0, len(s.pins))
	for _, pin := range s.pins {
		out = append(out, pin)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Source == PinSourceConfig) != (out[j].Source == PinSourceConfig) {
			return out[i].Source == PinSourceConfig
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// Len reports how many peers are pinned.
func (s *PinStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pins)
}

// saveLocked writes operator pins atomically (temp file + rename) so a crash
// mid-write cannot leave a truncated pin file that would fail to parse on the
// next boot. Caller holds s.mu.
func (s *PinStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	durable := make([]Pin, 0, len(s.pins))
	for _, pin := range s.pins {
		if pin.Source == PinSourceOperator {
			durable = append(durable, pin)
		}
	}
	sort.Slice(durable, func(i, j int) bool { return durable[i].PeerID < durable[j].PeerID })
	data, err := json.MarshalIndent(durable, "", "  ")
	if err != nil {
		return fmt.Errorf("peers: encode pin store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("peers: write pin store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("peers: commit pin store: %w", err)
	}
	return nil
}
