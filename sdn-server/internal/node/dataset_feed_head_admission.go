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
//  3. Ranking metadata must be plausible: the whole lane named, published_at
//     set and not in the future, feed_sequence in [1, 2^24], no more than
//     2^16 past the highest sequence this node holds for the same batch, a
//     window already recorded keeps its sequence and head, and a batch a
//     newer batch has superseded takes no later heads.
//
// handleDatasetFeedHeadMessage runs the gate and then
// materializeAdmittedDatasetFeedHead, which fetches the window from the
// author (or a trusted relay) and imports it only when the bytes are exactly
// what the signed head commits to. Lane retention ranks batches by publish
// time (storage/dataset_retention.go), never by a sequence that restarts for
// every batch.
//
// RepairImplausibleDatasetFeedPublications removes publication rows a forged
// head already wrote (a frozen lane does not thaw by itself once the gate is
// fixed); `spacedatanetwork store-repair-feed-heads` runs it offline.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	mh "github.com/multiformats/go-multihash"

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
// for its lane (schema, provider, source, query profile):
//
//   - a recorded window keeps its sequence and head;
//   - a new sequence may not leap far past its batch's highest;
//   - a batch is closed once a newer batch of the lane has started: a head
//     extending it must have been published before that newer batch began.
//     Feed sequences restart for every batch, so the lane's order is its
//     batches' publish times; a head that names an old batch with a fresh
//     published_at would otherwise make the old batch the lane's newest
//     (the verifier's S1: a trusted peer rolling a lane back and freezing it).
//
// Every lane field is required. An empty one used to skip these checks and be
// filled in later from the fetched, unsigned index (the verifier's S2).
func (n *Node) checkDatasetFeedHeadAgainstLane(ann sdnpubsub.DatasetFeedHeadAnnouncement) error {
	if strings.TrimSpace(ann.ProviderID) == "" || strings.TrimSpace(ann.SourceName) == "" || strings.TrimSpace(ann.BatchID) == "" || strings.TrimSpace(ann.QueryProfile) == "" {
		return fmt.Errorf("feed head %s does not name its whole lane (provider %q, source %q, batch %q, profile %q)", ann.FeedHead, ann.ProviderID, ann.SourceName, ann.BatchID, ann.QueryProfile)
	}
	if n == nil || n.store == nil {
		return errors.New("no store to check the feed head's lane against")
	}
	rows, err := n.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   ann.Schema,
		ProviderID:   ann.ProviderID,
		SourceName:   ann.SourceName,
		QueryProfile: ann.QueryProfile,
	})
	if err != nil {
		return fmt.Errorf("read lane for feed head %s: %w", ann.FeedHead, err)
	}
	var highest int64
	known := false
	starts := map[string]time.Time{}
	for _, row := range rows {
		batch := strings.TrimSpace(row.BatchID)
		if at, seen := starts[batch]; !seen || row.PublishedAt.Before(at) {
			starts[batch] = row.PublishedAt
		}
		if batch != ann.BatchID {
			continue
		}
		known = true
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
	if !known {
		// A batch this node has never held. Lane order is publish time, so
		// an old one arriving late ranks below the newer batches already here.
		return nil
	}
	start := starts[ann.BatchID]
	for batch, other := range starts {
		if batch != ann.BatchID && other.After(start) && !ann.PublishedAt.Before(other) {
			return fmt.Errorf("feed head %s extends batch %s at %s, after the newer batch %s began at %s", ann.FeedHead, ann.BatchID, ann.PublishedAt.UTC().Format(time.RFC3339), batch, other.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// handleDatasetFeedHeadMessage is the whole receive path for one gossiped feed
// head, and the only one handleDatasetFeedHeadSubscription may use: admission
// (author, bound key, plausible ranking, lane order), then materialization
// bound to the content the author signed. It returns how many records were
// imported; a refused or own head returns an error and imports nothing.
func (n *Node) handleDatasetFeedHeadMessage(ctx context.Context, msg *pubsub.Message, schema string) (int, error) {
	ann, author, err := n.admitDatasetFeedHead(ctx, msg, schema)
	if err != nil {
		if !errors.Is(err, errOwnDatasetFeedHead) {
			relay := peer.ID("")
			if msg != nil {
				relay = msg.ReceivedFrom
			}
			log.Warnf("Rejecting dataset feed head on %s (author %s, relay %s): %v", schema, author.ShortString(), relay.ShortString(), err)
		}
		return 0, err
	}
	imported, err := n.materializeAdmittedDatasetFeedHead(ctx, ann, author, msg.ReceivedFrom)
	if err != nil {
		log.Warnf("Failed to materialize dataset feed head %s from %s on %s: %v", ann.FeedHead, author.ShortString(), schema, err)
		return imported, err
	}
	if imported > 0 {
		log.Infof("Materialized dataset feed head %s from %s on %s: imported=%d", ann.FeedHead, author.ShortString(), schema, imported)
	}
	return imported, nil
}

// errFeedHeadContentMismatch marks fetched bytes that are not what the signed
// head commits to.
var errFeedHeadContentMismatch = errors.New("fetched content does not match the signed feed head")

// materializeAdmittedDatasetFeedHead imports the window an admitted head names.
//
// The bytes are fetched from the author first, then from the relay when the
// relay is itself trusted, and are accepted only when they are exactly what
// the author signed: the shard and index hash to the head's raw CIDs, the
// index names the head's lane, and the feed head recomputed over the fetched
// hashes equals the signed one (DatasetShardPublicationFeedHead commits to the
// shard, index, query and result SHA-256 and to every ranking field). A relay
// therefore cannot substitute content, and nothing in the stored row comes
// from an unsigned source.
func (n *Node) materializeAdmittedDatasetFeedHead(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement, author, relay peer.ID) (int, error) {
	if n == nil || n.store == nil || n.host == nil || n.peerRegistry == nil {
		return 0, errors.New("node is not ready to materialize feed heads")
	}
	if n.laneProducedHere(ann.Schema, ann.ProviderID, ann.SourceName) {
		log.Debugf("Skipping dataset feed head %s from %s: this node produces %s %s/%s", ann.FeedHead, author.ShortString(), ann.Schema, ann.ProviderID, ann.SourceName)
		return 0, nil
	}
	// Trusted is not "everything": the lane must be one this node imports.
	if !n.trustedPeerLaneImportAllowed(author, ann.Schema, ann.ProviderID, ann.SourceName, ann.QueryProfile) {
		return 0, nil
	}
	baseDir := ""
	if n.config != nil {
		baseDir = strings.TrimSpace(n.config.Storage.Path)
	}
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	workRoot := filepath.Join(baseDir, datasetFeedHeadSyncWorkRoot)
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return 0, fmt.Errorf("create feed-head work root: %w", err)
	}
	sources := []peer.ID{author}
	if relay != "" && relay != author && relay != n.host.ID() && n.peerRegistry.IsTrusted(relay) {
		sources = append(sources, relay)
	}
	var errs []error
	for _, source := range sources {
		imported, err := n.importCommittedDatasetFeedHead(ctx, ann, author, source, workRoot)
		if err == nil {
			return imported, nil
		}
		errs = append(errs, fmt.Errorf("from %s: %w", source.ShortString(), err))
	}
	return 0, errors.Join(errs...)
}

// importCommittedDatasetFeedHead fetches the head's shard and index from
// source, checks them against the head's commitment, and only then imports
// them and records the publication.
func (n *Node) importCommittedDatasetFeedHead(ctx context.Context, ann sdnpubsub.DatasetFeedHeadAnnouncement, author, source peer.ID, workRoot string) (int, error) {
	workDir, err := os.MkdirTemp(workRoot, "feed-head-*")
	if err != nil {
		return 0, fmt.Errorf("create feed-head work dir: %w", err)
	}
	defer os.RemoveAll(workDir)
	shardPath := filepath.Join(workDir, "shard.fbshard")
	indexPath := filepath.Join(workDir, "index.json")
	shardHeader, err := n.fetchDatasetFeedHeadAssetToFile(ctx, ann, source, ann.ShardCID, "shard", shardPath)
	if err != nil {
		return 0, err
	}
	indexHeader, err := n.fetchDatasetFeedHeadAssetToFile(ctx, ann, source, ann.IndexCID, "index", indexPath)
	if err != nil {
		return 0, err
	}
	pub, err := committedDatasetFeedHeadPublication(ann, shardPath, indexPath, shardHeader, indexHeader)
	if err != nil {
		return 0, err
	}
	imported, index, err := n.store.ImportDatasetShardFromFilesContext(ctx, shardPath, indexPath, author.String())
	if err != nil {
		return 0, fmt.Errorf("import dataset feed head %s: %w", ann.FeedHead, err)
	}
	if index == nil {
		return imported, fmt.Errorf("import dataset feed head %s returned no index", ann.FeedHead)
	}
	if err := n.cacheDatasetFeedHeadPublicationFiles(pub, shardPath, indexPath); err != nil {
		return imported, err
	}
	if err := n.store.UpsertDatasetShardPublication(pub); err != nil {
		return imported, fmt.Errorf("record replicated dataset shard publication %s: %w", ann.FeedHead, err)
	}
	n.scheduleDatasetSupersede(ann.Schema)
	n.applyLaneRetention(ann.Schema, pub.ProviderID, pub.SourceName,
		storage.RetainCandidate{BatchID: pub.BatchID, PublishedAt: pub.PublishedAt},
		datasetPublicationPins(pub.ShardCID, pub.IndexCID, pub.ManifestCID, pub.PNMCID))
	return imported, nil
}

// committedDatasetFeedHeadPublication builds the publication row for fetched
// files and refuses them unless they are the content the head commits to.
func committedDatasetFeedHeadPublication(ann sdnpubsub.DatasetFeedHeadAnnouncement, shardPath, indexPath string, shardHeader, indexHeader datasetFeedHeadAssetHeader) (storage.DatasetShardPublication, error) {
	var none storage.DatasetShardPublication
	shardSHA, shardBytes, err := sha256OfFile(shardPath)
	if err != nil {
		return none, err
	}
	indexSHA, _, err := sha256OfFile(indexPath)
	if err != nil {
		return none, err
	}
	if err := rawCIDMatches(ann.ShardCID, shardSHA); err != nil {
		return none, fmt.Errorf("%w: shard: %v", errFeedHeadContentMismatch, err)
	}
	if err := rawCIDMatches(ann.IndexCID, indexSHA); err != nil {
		return none, fmt.Errorf("%w: index: %v", errFeedHeadContentMismatch, err)
	}
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return none, fmt.Errorf("read fetched index: %w", err)
	}
	var index storage.DatasetExportIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return none, fmt.Errorf("%w: index does not parse: %v", errFeedHeadContentMismatch, err)
	}
	if !strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(index.SchemaName), ".fbs"), strings.TrimSuffix(ann.Schema, ".fbs")) {
		return none, fmt.Errorf("%w: index schema %q, head %q", errFeedHeadContentMismatch, index.SchemaName, ann.Schema)
	}
	for _, field := range []struct{ name, index, head string }{
		{"provider", index.ProviderID, ann.ProviderID},
		{"source", index.SourceName, ann.SourceName},
		{"batch", index.BatchID, ann.BatchID},
	} {
		if v := strings.TrimSpace(field.index); v != "" && v != field.head {
			return none, fmt.Errorf("%w: index %s %q, head %q", errFeedHeadContentMismatch, field.name, v, field.head)
		}
	}
	if index.ShardCID != "" && index.ShardCID != ann.ShardCID {
		return none, fmt.Errorf("%w: index names shard %s, head %s", errFeedHeadContentMismatch, index.ShardCID, ann.ShardCID)
	}
	if ann.ByteCount > 0 && ann.ByteCount != shardBytes {
		return none, fmt.Errorf("%w: shard is %d bytes, the head says %d", errFeedHeadContentMismatch, shardBytes, ann.ByteCount)
	}
	if ann.RecordCount > 0 && ann.RecordCount != index.RecordCount {
		return none, fmt.Errorf("%w: index holds %d records, the head says %d", errFeedHeadContentMismatch, index.RecordCount, ann.RecordCount)
	}
	if index.ShardSHA256 != "" && index.ShardSHA256 != shardSHA {
		return none, fmt.Errorf("%w: index shard SHA-256 differs from the fetched shard", errFeedHeadContentMismatch)
	}
	// The row's hashes are the ones computed here, never the sender's claims.
	shardHeader.SHA256, shardHeader.ByteCount = shardSHA, shardBytes
	indexHeader.SHA256 = indexSHA
	index.ShardSHA256 = shardSHA
	pub := datasetShardPublicationFromFeedHead(ann, shardHeader, indexHeader, &index)
	if got := storage.DatasetShardPublicationFeedHead(pub); got != ann.FeedHead {
		return none, fmt.Errorf("%w: feed head over the fetched content is %s, the signed head is %s", errFeedHeadContentMismatch, got, ann.FeedHead)
	}
	return pub, nil
}

// rawCIDMatches checks a raw-codec SHA-256 CID (how dataset shards and
// indexes are addressed) against a hex SHA-256. Any other CID form is
// refused: the head must address its assets by content.
func rawCIDMatches(value, sha256Hex string) error {
	c, err := cid.Decode(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("CID %q does not decode: %v", value, err)
	}
	if c.Type() != cid.Raw {
		return fmt.Errorf("CID %s is not a raw-codec CID", value)
	}
	decoded, err := mh.Decode(c.Hash())
	if err != nil || decoded.Code != mh.SHA2_256 {
		return fmt.Errorf("CID %s is not a SHA-256 multihash", value)
	}
	if hex.EncodeToString(decoded.Digest) != sha256Hex {
		return fmt.Errorf("CID %s does not address the fetched bytes", value)
	}
	return nil
}

func sha256OfFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
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
// queryProfile a forged feed head left behind (SEC-031) and, with apply,
// deletes them and their cached shard and index files so the lane's
// retention ranks genuine batches again. A row is implausible when its own
// ranking metadata is (ImplausibleDatasetFeedPublication), when it extends a
// batch after a newer batch of the lane began, or when its sequence leaps more
// than 2^16 past the batch's previous window.
func RepairImplausibleDatasetFeedPublications(store *storage.FlatSQLStore, queryProfile string, now time.Time, apply bool) ([]RepairedDatasetFeedPublication, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	rows, err := store.ListDatasetShardPublicationsForProfile(queryProfile, "")
	if err != nil {
		return nil, err
	}
	laneReasons := implausibleLaneOrder(rows)
	var out []RepairedDatasetFeedPublication
	for i, row := range rows {
		reason, bad := ImplausibleDatasetFeedPublication(row, now)
		if !bad {
			reason, bad = laneReasons[i]
		}
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

// implausibleLaneOrder flags, by index into rows, the rows that break their
// lane's order: a window published after a newer batch of the lane began, and
// a sequence that leaps past the batch's previous window by more than
// datasetFeedHeadMaxSequenceStep.
func implausibleLaneOrder(rows []storage.DatasetShardPublication) map[int]string {
	type laneKey struct{ schema, provider, source, profile string }
	type batchKey struct {
		lane  laneKey
		batch string
	}
	starts := map[batchKey]time.Time{}
	byBatch := map[batchKey][]int{}
	for i, row := range rows {
		key := batchKey{laneKey{row.SchemaName, row.ProviderID, row.SourceName, row.QueryProfile}, strings.TrimSpace(row.BatchID)}
		if at, seen := starts[key]; !seen || row.PublishedAt.Before(at) {
			starts[key] = row.PublishedAt
		}
		byBatch[key] = append(byBatch[key], i)
	}
	out := map[int]string{}
	for key, idx := range byBatch {
		start := starts[key]
		for other, otherStart := range starts {
			if other.lane != key.lane || other.batch == key.batch || !otherStart.After(start) {
				continue
			}
			for _, i := range idx {
				if !rows[i].PublishedAt.Before(otherStart) {
					out[i] = fmt.Sprintf("extends batch %s at %s, after the newer batch %s began at %s", key.batch, rows[i].PublishedAt.UTC().Format(time.RFC3339), other.batch, otherStart.UTC().Format(time.RFC3339))
				}
			}
		}
		ordered := append([]int(nil), idx...)
		sort.Slice(ordered, func(a, b int) bool { return rows[ordered[a]].FeedSequence < rows[ordered[b]].FeedSequence })
		var previous int64
		for _, i := range ordered {
			if rows[i].FeedSequence-previous > datasetFeedHeadMaxSequenceStep {
				if _, flagged := out[i]; !flagged {
					out[i] = fmt.Sprintf("feed_sequence %d leaps past the batch's previous window %d", rows[i].FeedSequence, previous)
				}
				continue
			}
			previous = rows[i].FeedSequence
		}
	}
	return out
}
