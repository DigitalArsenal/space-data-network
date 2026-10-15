package node

// Dataset feed-head admission (security audit SEC-031).
//
// A feed head is a small gossiped pointer to an immutable FlatSQL shard. Its
// ranking metadata (feed_sequence, published_at) decides which batch a lane
// keeps, so a forged head can roll a lane back to a stale batch and then
// evict every genuine newer batch on arrival. The audit showed an untrusted
// peer doing exactly that through one trusted relay: the old handler
// authorized the immediate relay (msg.ReceivedFrom), verified the signature
// against the key embedded in the same message, accepted unsigned heads by
// default, and trusted any sequence.
//
// admitDatasetFeedHead is the replacement gate:
//
//  1. Authority is the StrictSign gossip AUTHOR (msg.GetFrom()), never the
//     relay; the author must be a trusted peer.
//  2. The head must be signed (unsigned heads only with the development
//     opt-out SDN_DEV_ALLOW_UNSIGNED_FEED_HEADS=1), and the signature must
//     verify against a key bound to the author: its peer identity key or a
//     signing key its EPM advertises. The key inside the message is ignored.
//  3. Ranking metadata must be plausible: published_at set and not in the
//     future, feed_sequence in [1, 2^24], no more than 2^16 past the highest
//     sequence this node holds for the same batch, and a window already
//     recorded keeps its sequence and head.
//
// The materialize step that follows fetches the shard from the author.
//
// RepairImplausibleDatasetFeedPublications removes publication rows a forged
// head already wrote (a frozen lane does not thaw by itself once the gate is
// fixed); `spacedatanetwork store-repair-feed-heads` runs it offline.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// allowUnsignedFeedHeadsDevEnv is the development-only opt-out from signed
// feed heads: exactly "1". Never set it on a node that serves anyone.
const allowUnsignedFeedHeadsDevEnv = "SDN_DEV_ALLOW_UNSIGNED_FEED_HEADS"

// datasetFeedHeadMaxSequenceStep bounds how far one head may jump past the
// highest sequence this node already holds for the same batch.
const datasetFeedHeadMaxSequenceStep = int64(1) << 16

// errOwnDatasetFeedHead marks this node's own heads coming back over gossip;
// the caller skips them silently.
var errOwnDatasetFeedHead = errors.New("own dataset feed head")

// admitDatasetFeedHead decides whether one gossiped feed head may be
// materialized, and returns the parsed head and the peer it is authorized as
// (its author, from whom the caller must fetch the shard).
func (n *Node) admitDatasetFeedHead(ctx context.Context, msg *pubsub.Message, schema string) (sdnpubsub.DatasetFeedHeadAnnouncement, peer.ID, error) {
	var none sdnpubsub.DatasetFeedHeadAnnouncement
	if msg == nil || msg.Message == nil {
		return none, "", errors.New("empty gossip message")
	}
	// StrictSign authenticates the original publisher. ReceivedFrom is only
	// the immediate relay and must never select authority or keys.
	author := msg.GetFrom()
	if author == "" {
		return none, "", errors.New("gossip message names no author")
	}
	if n.host != nil && author == n.host.ID() {
		return none, author, errOwnDatasetFeedHead
	}
	ann, err := sdnpubsub.ParseDatasetFeedHeadAnnouncement(msg.Data)
	if err != nil {
		return none, author, fmt.Errorf("invalid dataset feed-head announcement: %w", err)
	}
	if ann.Schema != schema {
		return none, author, fmt.Errorf("feed head for schema %s arrived on the %s topic", ann.Schema, schema)
	}
	if n.peerRegistry == nil || !n.peerRegistry.IsTrusted(author) {
		return none, author, fmt.Errorf("feed head author %s is not a trusted peer", author.ShortString())
	}
	if err := n.verifyDatasetFeedHeadAuthor(ctx, ann, author); err != nil {
		return none, author, err
	}
	if err := sdnpubsub.CheckDatasetFeedHeadBounds(ann, time.Now()); err != nil {
		return none, author, err
	}
	if err := n.checkDatasetFeedHeadAgainstLane(ann); err != nil {
		return none, author, err
	}
	return ann, author, nil
}

// verifyDatasetFeedHeadAuthor requires a signature by a key bound to author.
func (n *Node) verifyDatasetFeedHeadAuthor(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement, author peer.ID) error {
	if strings.TrimSpace(ann.Signature) == "" {
		if os.Getenv(allowUnsignedFeedHeadsDevEnv) == "1" {
			log.Errorf("SECURITY: admitting UNSIGNED dataset feed head %s from %s because %s=1 (development only)", ann.FeedHead, author.ShortString(), allowUnsignedFeedHeadsDevEnv)
			return nil
		}
		return fmt.Errorf("unsigned dataset feed head %s from %s refused", ann.FeedHead, author.ShortString())
	}
	var lastErr error
	for _, candidate := range n.datasetPublicationKeyCandidates(ctx, author, true) {
		raw, err := candidate.Raw()
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		if lastErr = sdnpubsub.VerifyDatasetFeedHeadWithKey(ann, ed25519.PublicKey(raw)); lastErr == nil {
			return nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no Ed25519 key is known for the author")
	}
	return fmt.Errorf("dataset feed head %s is not signed by a key bound to its author %s: %w", ann.FeedHead, author.ShortString(), lastErr)
}

// checkDatasetFeedHeadAgainstLane compares the head with what this node holds
// for the same batch: a recorded window keeps its sequence and head, and a
// new sequence may not leap far past the batch's highest.
func (n *Node) checkDatasetFeedHeadAgainstLane(ann sdnpubsub.DatasetFeedHeadAnnouncement) error {
	if n == nil || n.store == nil {
		return nil
	}
	if strings.TrimSpace(ann.ProviderID) == "" || strings.TrimSpace(ann.SourceName) == "" || strings.TrimSpace(ann.BatchID) == "" || strings.TrimSpace(ann.QueryProfile) == "" {
		// Lane fields filled from the fetched index later; the absolute
		// bounds above still apply.
		return nil
	}
	rows, err := n.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   ann.Schema,
		ProviderID:   ann.ProviderID,
		SourceName:   ann.SourceName,
		BatchID:      ann.BatchID,
		QueryProfile: ann.QueryProfile,
	})
	if err != nil {
		return fmt.Errorf("read lane for feed head %s: %w", ann.FeedHead, err)
	}
	var highest int64
	for _, row := range rows {
		if row.FeedSequence > highest {
			highest = row.FeedSequence
		}
		if row.Offset == ann.Offset && row.Limit == ann.Limit &&
			(row.FeedSequence != ann.FeedSequence || (row.FeedHead != "" && row.FeedHead != ann.FeedHead)) {
			return fmt.Errorf("feed head %s rewrites recorded window offset=%d limit=%d (sequence %d, head %s)", ann.FeedHead, row.Offset, row.Limit, row.FeedSequence, row.FeedHead)
		}
	}
	if ann.FeedSequence > highest+datasetFeedHeadMaxSequenceStep {
		return fmt.Errorf("feed head %s sequence %d leaps past this batch's highest %d", ann.FeedHead, ann.FeedSequence, highest)
	}
	return nil
}

// ImplausibleDatasetFeedPublication reports whether a recorded publication
// row carries ranking metadata no honest producer emits.
func ImplausibleDatasetFeedPublication(pub storage.DatasetShardPublication, now time.Time) (string, bool) {
	switch {
	case pub.FeedSequence > sdnpubsub.DatasetFeedHeadMaxSequence:
		return fmt.Sprintf("feed_sequence %d exceeds %d", pub.FeedSequence, sdnpubsub.DatasetFeedHeadMaxSequence), true
	case pub.FeedSequence < 0:
		return fmt.Sprintf("feed_sequence %d is negative", pub.FeedSequence), true
	case pub.PublishedAt.After(now.Add(sdnpubsub.DatasetFeedHeadMaxFutureSkew)):
		return fmt.Sprintf("published_at %s is in the future", pub.PublishedAt.UTC().Format(time.RFC3339)), true
	}
	return "", false
}

// RepairedDatasetFeedPublication is one row the repair removed (or would).
type RepairedDatasetFeedPublication struct {
	Publication storage.DatasetShardPublication
	Reason      string
}

// RepairImplausibleDatasetFeedPublications finds publication rows in
// queryProfile whose ranking metadata is implausible (a forged feed head's
// residue, SEC-031) and, with apply, deletes them and their cached shard and
// index files so the lane's retention ranks genuine batches again.
func RepairImplausibleDatasetFeedPublications(store *storage.FlatSQLStore, queryProfile string, now time.Time, apply bool) ([]RepairedDatasetFeedPublication, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	rows, err := store.ListDatasetShardPublicationsForProfile(queryProfile, "")
	if err != nil {
		return nil, err
	}
	var out []RepairedDatasetFeedPublication
	for _, row := range rows {
		reason, bad := ImplausibleDatasetFeedPublication(row, now)
		if !bad {
			continue
		}
		out = append(out, RepairedDatasetFeedPublication{Publication: row, Reason: reason})
		if !apply {
			continue
		}
		if _, err := store.DeleteDatasetShardPublication(row); err != nil {
			return out, fmt.Errorf("delete implausible publication %s %s/%s batch %s: %w", row.SchemaName, row.ProviderID, row.SourceName, row.BatchID, err)
		}
		if _, _, err := store.RemoveDatasetPublicationRowFiles(store.DatasetPublicationOutputDir(), row.SchemaName, []storage.DatasetShardPublication{row}); err != nil {
			log.Warnf("Removed implausible publication row %s but not its cached files: %v", row.FeedHead, err)
		}
	}
	return out, nil
}
