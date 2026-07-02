// Package channelkeys implements group-chat channel key management on top of
// the unified ECIES one-to-many primitive (internal/ecies): a channel owns a
// single symmetric content key, membership is a set of member encryption keys,
// and the content key is wrapped one-to-many to every member as an SDS
// $ENC/$KMF envelope. Membership changes rekey the channel as required for
// forward secrecy.
//
// This is the key-management layer for WS9 encrypted pub/sub channel chat; the
// message layer (AES-256-GCM under the content key) is built on top of it. The
// content key never leaves this package except via ContentKey() (a copy) for
// the message-encryption step, and to each member wrapped under their own key.
package channelkeys

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

const contentKeyBytes = 32

// Member is one channel participant's encryption identity.
type Member struct {
	// ID is the stable member identifier (peer id / handle). It is also stamped
	// into the wrapped envelope's RECIPIENT_KEY_ID so a member can locate its
	// own envelope in a broadcast set.
	ID string
	// PublicKey is the member's encryption public key: X25519 (32 bytes) or
	// secp256k1 compressed (33 bytes), matching KeyExchange.
	PublicKey []byte
	// KeyExchange is the ECDH curve for this member.
	KeyExchange ecies.KeyExchange
}

// MemberEnvelope is one member's wrapped copy of the channel content key.
type MemberEnvelope struct {
	MemberID string
	Epoch    uint64
	ENC      []byte // $ENC header bytes
	KMF      []byte // $KMF payload bytes (this member's wrap of the content key)
}

// Channel is a keyed group-chat channel: an id, a rotating symmetric content
// key with an epoch counter, a member set, and the ECIES context (domain
// separator) used when wrapping the content key.
type Channel struct {
	id         string
	context    string
	epoch      uint64
	contentKey []byte
	members    map[string]Member
	rng        io.Reader
}

// Option configures a new Channel.
type Option func(*Channel)

// WithContext sets the ECIES context (domain separator) used for key wrapping.
// Defaults to a channel-scoped context derived from the channel id.
func WithContext(ctx string) Option {
	return func(c *Channel) { c.context = ctx }
}

// WithRand injects the randomness source for content-key generation
// (deterministic in tests). Defaults to crypto/rand.
func WithRand(r io.Reader) Option {
	return func(c *Channel) { c.rng = r }
}

// New creates a channel with a freshly generated content key at epoch 1 and no
// members. Add members with AddMember, then WrapForMembers to mint envelopes.
func New(id string, opts ...Option) (*Channel, error) {
	if id == "" {
		return nil, errors.New("channelkeys: channel id required")
	}
	c := &Channel{
		id:      id,
		context: defaultContext(id),
		epoch:   1,
		members: map[string]Member{},
		rng:     rand.Reader,
	}
	for _, o := range opts {
		o(c)
	}
	if c.context == "" {
		c.context = defaultContext(id)
	}
	if c.rng == nil {
		c.rng = rand.Reader
	}
	key, err := c.newContentKey()
	if err != nil {
		return nil, err
	}
	c.contentKey = key
	return c, nil
}

func defaultContext(id string) string {
	return "space-data-network/channel/" + id + "/v1"
}

func (c *Channel) newContentKey() ([]byte, error) {
	key := make([]byte, contentKeyBytes)
	if _, err := io.ReadFull(c.rng, key); err != nil {
		return nil, fmt.Errorf("channelkeys: content key: %w", err)
	}
	return key, nil
}

// ID returns the channel id.
func (c *Channel) ID() string { return c.id }

// Epoch returns the current content-key epoch (bumps on rekey).
func (c *Channel) Epoch() uint64 { return c.epoch }

// Context returns the ECIES context used for wrapping.
func (c *Channel) Context() string { return c.context }

// ContentKey returns a copy of the current 32-byte symmetric content key for
// message encryption. Callers must not retain it across a rekey.
func (c *Channel) ContentKey() []byte {
	out := make([]byte, len(c.contentKey))
	copy(out, c.contentKey)
	return out
}

// Members returns the current member set, sorted by id.
func (c *Channel) Members() []Member {
	out := make([]Member, 0, len(c.members))
	for _, m := range c.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AddMember adds a member to the set. Adding does NOT rotate the content key:
// the new member shares the current key and can read current and future
// messages (the common shared-group-key model). It does not grant access to
// messages already encrypted under an earlier epoch the member never held.
// Re-run WrapForMembers to mint the new member's envelope.
func (c *Channel) AddMember(m Member) error {
	if m.ID == "" {
		return errors.New("channelkeys: member id required")
	}
	if len(m.PublicKey) == 0 {
		return errors.New("channelkeys: member public key required")
	}
	c.members[m.ID] = m
	return nil
}

// RemoveMember removes a member and rekeys the channel (fresh content key, epoch
// bumped) so the removed member cannot read messages published afterward —
// forward secrecy. Messages already published under the prior epoch remain
// readable by whoever held that key. Returns an error if the member is absent.
func (c *Channel) RemoveMember(id string) error {
	if _, ok := c.members[id]; !ok {
		return fmt.Errorf("channelkeys: member %q not in channel", id)
	}
	delete(c.members, id)
	return c.Rekey()
}

// Rekey rotates the content key and bumps the epoch. Existing members re-wrap
// under the new key on the next WrapForMembers; anyone not in the current set
// is excluded from the new epoch.
func (c *Channel) Rekey() error {
	key, err := c.newContentKey()
	if err != nil {
		return err
	}
	c.contentKey = key
	c.epoch++
	return nil
}

// WrapForMembers wraps the current content key one-to-many to every current
// member, returning one $ENC/$KMF envelope per member (stamped with the member
// id as RECIPIENT_KEY_ID and the current epoch). Each member independently
// Unwraps its own envelope with UnwrapForMember. Returns an error if the
// channel has no members.
func (c *Channel) WrapForMembers() ([]MemberEnvelope, error) {
	members := c.Members()
	if len(members) == 0 {
		return nil, errors.New("channelkeys: channel has no members to wrap for")
	}
	recipients := make([]ecies.Recipient, len(members))
	for i, m := range members {
		recipients[i] = ecies.Recipient{
			PublicKey:   m.PublicKey,
			KeyExchange: m.KeyExchange,
			KeyID:       []byte(m.ID),
		}
	}
	envs, err := ecies.WrapForRecipients(c.contentKey, recipients, c.context)
	if err != nil {
		return nil, err
	}
	out := make([]MemberEnvelope, len(envs))
	for i := range envs {
		out[i] = MemberEnvelope{
			MemberID: members[i].ID,
			Epoch:    c.epoch,
			ENC:      envs[i].ENC,
			KMF:      envs[i].KMF,
		}
	}
	return out, nil
}

// UnwrapForMember recovers the channel content key from a member's envelope
// using the member's private key. The context must match the channel's.
func UnwrapForMember(memberPrivateKey, encBytes, kmfBytes []byte, context string) ([]byte, error) {
	return ecies.Unwrap(memberPrivateKey, encBytes, kmfBytes, context)
}
