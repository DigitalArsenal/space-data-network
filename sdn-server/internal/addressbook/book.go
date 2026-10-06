package addressbook

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
	"sync"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

// maxProfileBytes bounds one attested card.
const maxProfileBytes = 1 << 20

// ErrNotFound means the book has no live entry with that ID.
var ErrNotFound = errors.New("no such entry")

// Options wires a Book to its node.
type Options struct {
	Dir   string                // where the book is kept
	Key   func() crypto.PrivKey // the node's libp2p identity key
	Now   func() time.Time
	NewID func() string
}

// Book is the node's address book. Every change is a newly signed revision.
type Book struct {
	mu      sync.Mutex
	store   store
	key     func() crypto.PrivKey
	now     func() time.Time
	newID   func() string
	records map[string]Record // nil until first read
}

// Entry is a live entry, its latest signed revision.
type Entry struct {
	Record
	PeerID string // the node the card belongs to, when it names one
	VCard  string // the attested card as a vCard
}

func NewBook(o Options) *Book {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = uuid.NewString
	}
	return &Book{store: store{dir: o.Dir}, key: o.Key, now: o.Now, newID: o.NewID}
}

// Entries lists the live entries, oldest first; private ones only on request.
func (b *Book) Entries(withPrivate bool) ([]Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := b.held()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(records))
	for _, r := range records {
		if !r.Deleted && (withPrivate || r.Visibility == Public) {
			out = append(out, entryOf(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].EntryID < out[j].EntryID
	})
	return out, nil
}

// Add signs a card into the book. A card already in it is returned as it is;
// a newer card of a node already in it becomes that entry's next revision.
// visibility nil takes the operator's default. created reports a new entry.
func (b *Book) Add(profile []byte, visibility *Visibility) (entry Entry, created bool, err error) {
	if profile, err = sizePrefixedEPM(profile); err != nil {
		return Entry{}, false, err
	}
	sum := digest(profile)
	subject, _ := epm.PeerIDFromEPM(profile)

	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := b.held()
	if err != nil {
		return Entry{}, false, err
	}
	var next Record
	found := false
	for _, r := range records {
		if r.Deleted {
			continue
		}
		if bytes.Equal(r.ProfileSHA256, sum) {
			return entryOf(r), false, nil
		}
		if held, _ := epm.PeerIDFromEPM(r.Profile); subject != "" && held == subject {
			next, found = r, true
		}
	}
	now := b.millis()
	if found {
		next.Profile, next.ProfileSHA256 = profile, sum
		next.UpdatedAt = after(now, next.UpdatedAt)
	} else {
		next = Record{EntryID: b.newID(), Profile: profile, ProfileSHA256: sum, CreatedAt: now, UpdatedAt: now}
		if next.Visibility, err = b.store.defaultVisibility(); err != nil {
			return Entry{}, false, err
		}
	}
	if visibility != nil {
		next.Visibility = *visibility
	}
	if err := b.commit(records, next); err != nil {
		return Entry{}, false, err
	}
	return entryOf(next), !found, nil
}

// SetVisibility changes who may read an entry.
func (b *Book) SetVisibility(id string, v Visibility) (Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := b.held()
	if err != nil {
		return Entry{}, err
	}
	r, ok := records[id]
	if !ok || r.Deleted {
		return Entry{}, ErrNotFound
	}
	if r.Visibility != v {
		r.Visibility = v
		r.UpdatedAt = after(b.millis(), r.UpdatedAt)
		if err := b.commit(records, r); err != nil {
			return Entry{}, err
		}
	}
	return entryOf(r), nil
}

// Remove revokes an entry with a signed tombstone.
func (b *Book) Remove(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := b.held()
	if err != nil {
		return err
	}
	r, ok := records[id]
	if !ok || r.Deleted {
		return ErrNotFound
	}
	r.Deleted = true
	r.UpdatedAt = after(b.millis(), r.UpdatedAt)
	return b.commit(records, r)
}

// DefaultVisibility is the visibility of new entries.
func (b *Book) DefaultVisibility() (Visibility, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.defaultVisibility()
}

func (b *Book) SetDefaultVisibility(v Visibility) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.setDefaultVisibility(v)
}

// held loads the book once; this Book is its only writer. Revisions another
// identity signed are not this node's entries.
func (b *Book) held() (map[string]Record, error) {
	if b.records != nil {
		return b.records, nil
	}
	records, err := b.store.load()
	if err != nil {
		return nil, err
	}
	if self, err := b.self(); err == nil {
		for id, r := range records {
			if r.NodePeerID != self {
				delete(records, id)
			}
		}
	}
	b.records = records
	return records, nil
}

// commit signs r and saves it with the rest; nothing changes if either fails.
func (b *Book) commit(records map[string]Record, r Record) error {
	if b.key == nil {
		return errors.New("this node's identity key is unavailable")
	}
	if err := r.sign(b.key()); err != nil {
		return err
	}
	next := make(map[string]Record, len(records)+1)
	for id, held := range records {
		next[id] = held
	}
	next[r.EntryID] = r
	if err := b.store.save(next); err != nil {
		return err
	}
	b.records = next
	return nil
}

func (b *Book) self() (string, error) {
	if b.key == nil {
		return "", errors.New("no identity key")
	}
	id, err := peer.IDFromPrivateKey(b.key())
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (b *Book) millis() uint64 { return uint64(b.now().UnixMilli()) }

// after is a revision time strictly later than prev.
func after(now, prev uint64) uint64 {
	if now > prev {
		return now
	}
	return prev + 1
}

func entryOf(r Record) Entry {
	e := Entry{Record: r}
	e.PeerID, _ = epm.PeerIDFromEPM(r.Profile)
	e.VCard, _ = vcard.EPMToVCard(r.Profile)
	return e
}

// sizePrefixedEPM returns an EPM in the size-prefixed form every card on this
// node takes.
func sizePrefixedEPM(b []byte) ([]byte, error) {
	switch {
	case len(b) == 0 || len(b) > maxProfileBytes:
		return nil, errors.New("the card is empty or too large")
	case len(b) >= 8 && standardsEPM.SizePrefixedEPMBufferHasIdentifier(b) && int(binary.LittleEndian.Uint32(b)) == len(b)-4:
		return b, nil
	case standardsEPM.EPMBufferHasIdentifier(b):
		out := make([]byte, 4+len(b))
		binary.LittleEndian.PutUint32(out, uint32(len(b)))
		copy(out[4:], b)
		return out, nil
	}
	return nil, errors.New("the card is not an EPM")
}
