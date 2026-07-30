package peers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	testPinPeerA = "12D3KooWKh3diobFtzBk2RvdwR4TuFB8nkU31th8Mc2iKb7bZBWs"
	testPinPeerB = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
)

// A pin that does not survive a restart is not a pin (owner ruling 2026-07-30).
func TestPinStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-pins.json")

	store, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
	if _, err := store.Pin(Pin{PeerID: testPinPeerA, Name: "vm-orbit-det-01", Note: "owner LAN box"}); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	reopened, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	pin, ok := reopened.Get(testPinPeerA)
	if !ok {
		t.Fatal("pin did not survive the restart")
	}
	if pin.Name != "vm-orbit-det-01" || pin.Note != "owner LAN box" {
		t.Fatalf("pin lost its fields: %+v", pin)
	}
	if pin.Source != PinSourceOperator {
		t.Fatalf("source = %q, want %q", pin.Source, PinSourceOperator)
	}
}

// Config pins are re-declared from the config file every boot and never
// persisted, so deleting the line actually removes the pin.
func TestConfigPinsAreNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-pins.json")
	store, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
	id, err := peer.Decode(testPinPeerB)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	store.DeclareConfigPin(id, []string{"/ip4/167.172.219.213/tcp/4001"}, "/etc/x.yaml · peers.trusted_peers")
	// Force a write so the file exists.
	if _, err := store.Pin(Pin{PeerID: testPinPeerA}); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	reopened, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reopened.Get(testPinPeerB); ok {
		t.Fatal("a config pin was persisted; removing it from the config would not remove the pin")
	}
	if _, ok := reopened.Get(testPinPeerA); !ok {
		t.Fatal("the operator pin should have survived")
	}
}

// A row locked as config-owned must name a real file, and must not be
// removable through the API — the operator edits the file.
func TestConfigPinIsNotUnpinnable(t *testing.T) {
	store, err := NewPinStore(filepath.Join(t.TempDir(), "peer-pins.json"))
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
	id, _ := peer.Decode(testPinPeerB)
	store.DeclareConfigPin(id, nil, "/etc/space-data-network/config.yaml · peers.trusted_peers")

	if err := store.Unpin(testPinPeerB); !errors.Is(err, ErrConfigPin) {
		t.Fatalf("Unpin(config pin) = %v, want ErrConfigPin", err)
	}
	if _, err := store.Pin(Pin{PeerID: testPinPeerB}); !errors.Is(err, ErrConfigPin) {
		t.Fatalf("Pin(over config pin) = %v, want ErrConfigPin", err)
	}
	pin, _ := store.Get(testPinPeerB)
	if pin.Note == "" {
		t.Fatal("a locked row must name the file that owns it")
	}
}

func TestUnpinRemovesOperatorPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-pins.json")
	store, _ := NewPinStore(path)
	if _, err := store.Pin(Pin{PeerID: testPinPeerA}); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := store.Unpin(testPinPeerA); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if err := store.Unpin(testPinPeerA); !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("second Unpin = %v, want ErrPinNotFound", err)
	}
	reopened, _ := NewPinStore(path)
	if reopened.Len() != 0 {
		t.Fatalf("unpin did not persist: %d pins remain", reopened.Len())
	}
}

// A corrupt pin file is a hard error, never a silent empty pin set: silently
// starting with zero pins drops peers the operator deliberately kept.
func TestCorruptPinFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-pins.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := NewPinStore(path); err == nil {
		t.Fatal("a malformed pin store must fail loudly")
	}

	// ...and the bad bytes must be PRESERVED, not left in place to be
	// overwritten by the next Pin(). node.go carries on with an empty store so
	// a bad pin file cannot take the node offline, which makes this the only
	// thing standing between a JSON typo and silent, permanent pin loss.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var preserved string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "peer-pins.json.corrupt-") {
			preserved = e.Name()
		}
	}
	if preserved == "" {
		t.Fatal("the malformed pin file was not preserved")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the malformed file is still at the live path; the next Pin() would overwrite it")
	}

	// A fresh store at the same path now opens clean and is writable.
	store, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("reopen after corruption: %v", err)
	}
	if _, err := store.Pin(Pin{PeerID: testPinPeerA}); err != nil {
		t.Fatalf("pin after corruption: %v", err)
	}
}

// The pin path must not inherit the registry's refusal of legacy ".db" paths —
// that refusal is why the live node had nowhere to keep a pin.
func TestPinPathForLegacyDatabasePath(t *testing.T) {
	got := PinPathFor("/opt/data/sdn-module-delivery/peers.db")
	want := filepath.Join("/opt/data/sdn-module-delivery", pinFileName)
	if got != want {
		t.Fatalf("PinPathFor = %q, want %q", got, want)
	}
	if PinPathFor("  ") != "" {
		t.Fatal("an empty registry path must yield an in-memory pin store")
	}
}

func TestParsePinTargetAcceptsBareIDAndMultiaddr(t *testing.T) {
	id, addrs, err := parsePinTarget(testPinPeerA, nil)
	if err != nil || id.String() != testPinPeerA || len(addrs) != 0 {
		t.Fatalf("bare id: id=%v addrs=%v err=%v", id, addrs, err)
	}

	full := "/ip4/10.100.10.20/tcp/4001/p2p/" + testPinPeerA
	id, addrs, err = parsePinTarget(full, nil)
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	if id.String() != testPinPeerA {
		t.Fatalf("multiaddr id = %s", id)
	}
	if len(addrs) != 1 || addrs[0] != "/ip4/10.100.10.20/tcp/4001" {
		t.Fatalf("multiaddr addrs = %v", addrs)
	}

	if _, _, err := parsePinTarget("", nil); err == nil {
		t.Fatal("an empty peer_id must be rejected")
	}
	if _, _, err := parsePinTarget("not-a-peer", nil); err == nil {
		t.Fatal("a malformed peer id must be rejected")
	}
}
