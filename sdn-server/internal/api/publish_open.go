package api

// OPEN PUBLISHING AND IPFS PUBLISHING — two operator switches on the existing
// publish route (POST /api/v1/data/publish/{schema}), no new route.
//
//   publishing.open_schemas   A standard listed here may be published, and its
//                             records read by CID, by anyone, without an
//                             account on this node: a record that carries its
//                             own author signature (a shared scenario) needs a
//                             node to hold it, not a node account to vouch for
//                             it. The lane is bounded node wide: one record
//                             size cap, one publish rate and one storage quota
//                             for every open publisher together. A request with
//                             a node session publishes as that session.
//   publishing.ipfs_schemas   A record of a standard listed here is also put in
//                             this node's blockstore as a pinned CIDv1 raw block
//                             (the record CID and the block CID are the same
//                             string) and announced to the routing system, so
//                             any IPFS peer or gateway fetches it by CID.
//
// Both lists are empty by default: a node opens nothing it was not told to.
// The blockstore half fails OPEN, like the account EPM lane: the record is
// stored either way, and the response says what IPFS holds.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const (
	// OpenPublisher is the publisher identity of every record published
	// without a node session: quotas and provenance count them as one.
	OpenPublisher = "open-publish"

	publishPath                = "/api/v1/data/publish/"
	recordsPath                = "/api/v1/data/records/"
	defaultOpenMaxRecordBytes  = 1 << 20 // Kubo's block limit: a bigger record could not be exchanged as one block
	defaultOpenPublishesPerMin = 30
	recordBlockTimeout         = 30 * time.Second
)

// RecordBlockstore is the node's IPFS blockstore, as the publish route needs
// it: bytes in, the pinned block's CID out; a held block announced once.
type RecordBlockstore interface {
	PutPinnedRawBlock(ctx context.Context, data []byte) (string, error)
	ProvideOnce(ctx context.Context, cid string) error
}

// openLane is the node-wide budget every sessionless open publish shares.
type openLane struct {
	once    sync.Once
	limiter *rate.Limiter
}

// IPFSPublication is what a publish response says about the record in IPFS.
type IPFSPublication struct {
	CID      string `json:"cid"`
	Pinned   bool   `json:"pinned"`
	Provided bool   `json:"provided"`
	Error    string `json:"error,omitempty"`
}

// SetRecordBlockstore attaches the node's blockstore; nil (the default)
// publishes to the record store only.
func (h *PublishHandler) SetRecordBlockstore(blockstore RecordBlockstore) {
	h.blockstore = blockstore
}

// OpenSchemaPublicPaths are the access-gate rules the open schemas need (the
// publish route, and the record read by CID): on a locked node a path without
// one needs an admin before any handler runs.
func OpenSchemaPublicPaths(cfg *config.PublishingConfig) []string {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	paths := make([]string, 0, 2*len(cfg.OpenSchemas))
	for _, schema := range cfg.OpenSchemas {
		if schema = strings.TrimSpace(schema); schema != "" {
			paths = append(paths, "POST "+publishPath+schema, recordsPath+schema+"/")
		}
	}
	return paths
}

func listed(list []string, schema string) bool {
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry), schema) {
			return true
		}
	}
	return false
}

func (h *PublishHandler) isOpenSchema(schema string) bool {
	return schema != "" && listed(h.cfg.OpenSchemas, schema) && h.isSchemaAllowed(schema)
}

func (h *PublishHandler) isIPFSSchema(schema string) bool {
	return listed(h.cfg.IPFSSchemas, schema)
}

func (h *PublishHandler) openMaxRecordBytes() int64 {
	if h.cfg.OpenMaxRecordBytes > 0 {
		return int64(h.cfg.OpenMaxRecordBytes)
	}
	return defaultOpenMaxRecordBytes
}

func (h *PublishHandler) openLimiterFor() *rate.Limiter {
	h.open.once.Do(func() {
		perMinute := h.cfg.OpenPublishesPerMinute
		if perMinute <= 0 {
			perMinute = defaultOpenPublishesPerMin
		}
		h.open.limiter = rate.NewLimiter(rate.Limit(float64(perMinute)/60), perMinute)
	})
	return h.open.limiter
}

// openOr serves an open schema's single-record publish with or without a node
// session, and everything else through protect.
func (h *PublishHandler) openOr(protect func(http.HandlerFunc) http.HandlerFunc, next http.HandlerFunc) http.HandlerFunc {
	protected := protect(next)
	open := h.authHandler.OptionalAuth(func(w http.ResponseWriter, r *http.Request) {
		if auth.SessionFromContext(r.Context()) == nil {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !h.openLimiterFor().Allow() {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "open publishing is busy on this node; try again in a minute")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, h.openMaxRecordBytes())
			session := &auth.Session{XPub: OpenPublisher, TrustLevel: peers.Untrusted}
			r = r.WithContext(auth.ContextWithSession(r.Context(), session))
		}
		next(w, r)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		schema := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, publishPath), "/")
		if h.isOpenSchema(schema) {
			open(w, r)
			return
		}
		protected(w, r)
	}
}

// publishToIPFS pins the stored record's bytes as a raw block and announces
// it, when its standard is an IPFS schema and a blockstore is attached. nil
// means the record is not published to IPFS on this node.
func (h *PublishHandler) publishToIPFS(ctx context.Context, schema, cid string, data []byte) *IPFSPublication {
	if h.blockstore == nil || !h.isIPFSSchema(schema) {
		return nil
	}
	out := &IPFSPublication{CID: cid}
	ctx, cancel := context.WithTimeout(ctx, recordBlockTimeout)
	defer cancel()
	blockCID, err := h.blockstore.PutPinnedRawBlock(ctx, data)
	if err != nil {
		out.Error = "pin: " + err.Error()
		log.Warnf("Could not pin %s record %s in the blockstore: %v", schema, cid, err)
		return out
	}
	if strings.TrimSpace(blockCID) != cid {
		out.Error = "the blockstore holds the record as " + blockCID
		log.Warnf("%s record %s pinned in the blockstore under a different CID (%s)", schema, cid, blockCID)
		return out
	}
	out.Pinned = true
	if err := h.blockstore.ProvideOnce(ctx, cid); err != nil {
		out.Error = "provide: " + err.Error()
		log.Warnf("Could not announce %s record %s: %v", schema, cid, err)
		return out
	}
	out.Provided = true
	return out
}
