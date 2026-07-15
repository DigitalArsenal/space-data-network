// Package channels defines the SDN streaming-channel contract on kubo's
// libp2p gossipsub: one pubsub topic per (provider, 3-letter SDS standard),
// with records fanning out from the store to every subscriber of that channel.
//
// # Naming grammar (ported subset)
//
// The channel-ID grammar is ported verbatim from
// sdn-server/internal/channels/channels.go — the naming subset ONLY
// (ChannelID / ChannelIDInput / ParseChannelID / FormatChannelID /
// AssertStandardCode). The HTTP-delivery, access-gate, grants, metadata and
// encryption machinery from that package is intentionally NOT ported: Phase 4
// is plain gossipsub with no private-access layer (owner-approved default).
//
// One deliberate divergence: AssertStandardCode here validates the 3-letter
// uppercase FORMAT only (^[A-Z]{3}$). The sdn-server original additionally
// checked the code against its embedded SupportedSchemas registry. That check
// is dropped so this package — like sdnstore, which "embeds no SDS schemas of
// its own and can store any 3-letter type the provider knows" — stays
// SDS-type-neutral. The provider that knows the type is the authority.
//
// # Wire topic scheme (per provider, per standard)
//
// The per-(provider, standard) wire topic did not exist in sdn-server (its
// pubsub and DiscoveryTopic were per-standard only). It is designed here as:
//
//	/spacedatanetwork/channels/<STANDARD3>/<sourceID>
//
//	e.g.  /spacedatanetwork/channels/OMM/celestrak-gp
//
// STANDARD3 is the validated 3-letter uppercase code; sourceID is the provider
// id (an opaque, non-empty string with no "--"). gossipsub topics are arbitrary
// UTF-8 strings, so both segments are valid on the wire as-is. Two sources of
// the same standard therefore land on DISTINCT topics (they differ only in the
// trailing sourceID segment), which is what gives per-provider isolation.
//
// gossipsub has no wildcard subscription, so "subscribe to a standard with no
// specific source" means subscribing to each (source, standard) topic the
// subscriber already knows; StandardTopicPrefix exposes the shared prefix so a
// caller can enumerate/subscribe to the sources it knows. The minimal,
// exact gate is Publish(source, standard, bytes) + Subscribe(standard, source).
//
// # Message identity (keyed by CID)
//
// Fan-out publishes the record BYTES as the message payload (SDS records are
// small — an OMM is ~565 B). The record's CID is the raw-codec sha256 CIDv1 of
// those exact bytes — the SAME CID sdnstore content-addresses the block under —
// so it is a pure function of the payload and is recoverable at both ends via
// CIDOf(msg.Data). MessageIDFn additionally lets a gossipsub instance use that
// CID as its wire message-id (pubsub.WithMessageIdFn(channels.MessageIDFn)),
// which makes byte-identical records dedup at the pubsub layer too.
package channels

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	cid "github.com/ipfs/go-cid"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	mh "github.com/multiformats/go-multihash"
)

// TopicPrefix is the shared prefix for every per-(provider, standard) channel
// wire topic.
const TopicPrefix = "/spacedatanetwork/channels/"

var (
	uuidPattern         = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	standardCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ChannelID is the parsed form of a channel identifier.
type ChannelID struct {
	ChannelID    string
	SourceID     string
	StandardCode string
	FeedUUID     string
}

// ChannelIDInput is the unparsed input to FormatChannelID.
type ChannelIDInput struct {
	SourceID     string
	StandardCode string
	FeedUUID     string
}

// ParseChannelID parses "sourceID-STANDARD[-feedUUID]" into its parts. It is the
// inverse of FormatChannelID (ported verbatim from sdn-server's naming subset).
func ParseChannelID(channelID string) (ChannelID, error) {
	value := strings.TrimSpace(channelID)
	if value == "" || strings.Contains(value, "--") {
		return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
	}
	parts := strings.Split(value, "-")
	if len(parts) < 2 {
		return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
	}
	for _, part := range parts {
		if part == "" {
			return ChannelID{}, fmt.Errorf("invalid channel ID %q", channelID)
		}
	}

	standardIndex := len(parts) - 1
	feedUUID := ""
	if len(parts) >= 7 {
		maybeUUID := strings.Join(parts[len(parts)-5:], "-")
		if uuidPattern.MatchString(maybeUUID) {
			feedUUID = maybeUUID
			standardIndex = len(parts) - 6
		}
	}
	if standardIndex <= 0 {
		return ChannelID{}, fmt.Errorf("channel sourceId is required in %q", channelID)
	}

	standardCode, err := AssertStandardCode(parts[standardIndex])
	if err != nil {
		return ChannelID{}, err
	}
	sourceID := strings.Join(parts[:standardIndex], "-")
	formatted, err := FormatChannelID(ChannelIDInput{
		SourceID:     sourceID,
		StandardCode: standardCode,
		FeedUUID:     feedUUID,
	})
	if err != nil {
		return ChannelID{}, err
	}
	return ChannelID{
		ChannelID:    formatted,
		SourceID:     sourceID,
		StandardCode: standardCode,
		FeedUUID:     feedUUID,
	}, nil
}

// FormatChannelID renders "sourceID-STANDARD[-feedUUID]" from its parts,
// validating the standard code and (if present) the feed UUID.
func FormatChannelID(input ChannelIDInput) (string, error) {
	sourceID := strings.TrimSpace(input.SourceID)
	if sourceID == "" || strings.Contains(sourceID, "--") {
		return "", fmt.Errorf("channel sourceId is required")
	}
	standardCode, err := AssertStandardCode(input.StandardCode)
	if err != nil {
		return "", err
	}
	feedUUID := strings.TrimSpace(input.FeedUUID)
	if feedUUID != "" && !uuidPattern.MatchString(feedUUID) {
		return "", fmt.Errorf("invalid feedUuid %q", input.FeedUUID)
	}
	if feedUUID != "" {
		return sourceID + "-" + standardCode + "-" + feedUUID, nil
	}
	return sourceID + "-" + standardCode, nil
}

// AssertStandardCode validates and normalizes a 3-letter SDS standard code.
// Format-only (^[A-Z]{3}$): this package is SDS-type-neutral (see the package
// doc); it does not consult an SDS schema registry.
func AssertStandardCode(value string) (string, error) {
	code := strings.TrimSpace(value)
	if !standardCodePattern.MatchString(code) {
		return "", fmt.Errorf("invalid standardCode %q", value)
	}
	return code, nil
}

// normalizeSource validates and normalizes a provider source id for the wire
// topic. It mirrors FormatChannelID's sourceID rule (non-empty, no "--").
func normalizeSource(sourceID string) (string, error) {
	s := strings.TrimSpace(sourceID)
	if s == "" || strings.Contains(s, "--") {
		return "", fmt.Errorf("invalid sourceID %q", sourceID)
	}
	return s, nil
}

// WireTopic returns the per-(provider, standard) gossipsub topic
// "/spacedatanetwork/channels/<STANDARD3>/<sourceID>". Distinct sources of the
// same standard produce distinct topics.
func WireTopic(sourceID, standardCode string) (string, error) {
	code, err := AssertStandardCode(standardCode)
	if err != nil {
		return "", err
	}
	src, err := normalizeSource(sourceID)
	if err != nil {
		return "", err
	}
	return TopicPrefix + code + "/" + src, nil
}

// StandardTopicPrefix returns "/spacedatanetwork/channels/<STANDARD3>/", the
// shared prefix under which all of a standard's per-source topics live. It lets
// a subscriber enumerate/subscribe to the sources it knows (gossipsub has no
// wildcard subscription).
func StandardTopicPrefix(standardCode string) (string, error) {
	code, err := AssertStandardCode(standardCode)
	if err != nil {
		return "", err
	}
	return TopicPrefix + code + "/", nil
}

// CIDOf returns the record's content id: the raw-codec, sha256 CIDv1 of the
// exact FlatBuffer bytes. This is byte-for-byte the CID sdnstore stores the
// block under, so a channel subscriber recovers the same CID the store has.
func CIDOf(fb []byte) (cid.Cid, error) {
	h, err := mh.Sum(fb, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, h), nil
}

// MessageIDFn is a gossipsub MsgIdFunction that keys each message by the CID of
// its payload bytes. Pass it when constructing gossipsub
// (pubsub.WithMessageIdFn(channels.MessageIDFn)) so the wire message-id is the
// record's CID and byte-identical records dedup at the pubsub layer. When the
// CID cannot be computed it falls back to gossipsub's default id.
func MessageIDFn(m *pb.Message) string {
	if m == nil {
		return ""
	}
	c, err := CIDOf(m.GetData())
	if err != nil {
		return pubsub.DefaultMsgIdFn(m)
	}
	return c.String()
}

// Channels fans SDS records out over per-(provider, standard) gossipsub topics.
// It joins each topic lazily and caches the join; it does not own the
// underlying *pubsub.PubSub (that is the node's, or a test's).
type Channels struct {
	ps *pubsub.PubSub

	mu     sync.Mutex
	topics map[string]*pubsub.Topic
}

// New wraps a gossipsub instance for channel fan-out.
func New(ps *pubsub.PubSub) *Channels {
	return &Channels{ps: ps, topics: make(map[string]*pubsub.Topic)}
}

func (c *Channels) joinTopic(name string) (*pubsub.Topic, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.topics[name]; ok {
		return t, nil
	}
	t, err := c.ps.Join(name)
	if err != nil {
		return nil, err
	}
	c.topics[name] = t
	return t, nil
}

// Publish fans one record out to its (source, standard) channel. The message
// payload is the record bytes; the CID is derivable from them via CIDOf.
func (c *Channels) Publish(ctx context.Context, sourceID, standard3 string, recordBytes []byte) error {
	if len(recordBytes) == 0 {
		return fmt.Errorf("channels: record bytes must be non-empty")
	}
	name, err := WireTopic(sourceID, standard3)
	if err != nil {
		return err
	}
	t, err := c.joinTopic(name)
	if err != nil {
		return fmt.Errorf("channels: join %s: %w", name, err)
	}
	if err := t.Publish(ctx, recordBytes); err != nil {
		return fmt.Errorf("channels: publish %s: %w", name, err)
	}
	return nil
}

// Subscription is a live subscription to one or more (source, standard) channel
// topics of a single standard.
type Subscription struct {
	Standard string
	subs     []*pubsub.Subscription
}

// Subscribe subscribes to the given sources of one standard. Every source is an
// exact (source, standard) channel; subscribing to several sources of the same
// standard is how a caller follows "a standard" when it knows the sources.
// At least one source is required (gossipsub has no wildcard subscription).
func (c *Channels) Subscribe(standard3 string, sources ...string) (*Subscription, error) {
	code, err := AssertStandardCode(standard3)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("channels: subscribe to %s requires at least one source", code)
	}
	out := &Subscription{Standard: code}
	for _, src := range sources {
		name, err := WireTopic(src, code)
		if err != nil {
			out.Cancel()
			return nil, err
		}
		t, err := c.joinTopic(name)
		if err != nil {
			out.Cancel()
			return nil, fmt.Errorf("channels: join %s: %w", name, err)
		}
		sub, err := t.Subscribe()
		if err != nil {
			out.Cancel()
			return nil, fmt.Errorf("channels: subscribe %s: %w", name, err)
		}
		out.subs = append(out.subs, sub)
	}
	return out, nil
}

// Next returns the next message across all subscribed sources of the standard.
// For a single-source subscription it blocks on that source; for a multi-source
// subscription it fans the sources in over one internal channel.
func (s *Subscription) Next(ctx context.Context) (*pubsub.Message, error) {
	switch len(s.subs) {
	case 0:
		return nil, fmt.Errorf("channels: subscription has no sources")
	case 1:
		return s.subs[0].Next(ctx)
	default:
		return s.nextMerged(ctx)
	}
}

func (s *Subscription) nextMerged(ctx context.Context) (*pubsub.Message, error) {
	type result struct {
		msg *pubsub.Message
		err error
	}
	ch := make(chan result, len(s.subs))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, sub := range s.subs {
		go func(sub *pubsub.Subscription) {
			m, err := sub.Next(cctx)
			select {
			case ch <- result{m, err}:
			case <-cctx.Done():
			}
		}(sub)
	}
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cancel cancels every underlying subscription.
func (s *Subscription) Cancel() {
	for _, sub := range s.subs {
		if sub != nil {
			sub.Cancel()
		}
	}
	s.subs = nil
}

// Publisher returns an sdnstore OnStore hook (matching the
// func(ctx, source, sdsType, cid.Cid, fb) error signature) that fans each
// newly stored record out to its (source, standard) channel. Wire it into
// sdnstore.Config.OnStore so a Phase-3 Store() call publishes to the channel.
// sdnstore does not import this package; the coupling is this callback only.
func (c *Channels) Publisher() func(ctx context.Context, source, sdsType string, recordCID cid.Cid, fb []byte) error {
	return func(ctx context.Context, source, sdsType string, _ cid.Cid, fb []byte) error {
		return c.Publish(ctx, source, sdsType, fb)
	}
}
