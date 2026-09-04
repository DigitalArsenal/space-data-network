package api

// $ICN connectors (fbcs program): one signed-by-storage Ingest Connector
// record per (provider_id, source_name) lane this node holds or fetches,
// computed live from the store's source summaries, the retrieval ledger
// (internal/sourcemetrics), the running flow services and the dataset
// publication ledger, resolved to an ORIGIN (an organisation, never a tag
// string — see connector_origins.go), persisted through the FlatSQL engine
// like every other standard and served as size-prefixed $ICN frames.
//
//	GET  /api/v1/connectors[?origin=<id>&schema=<CODE>]   anonymous read
//	GET  /api/v1/connectors/{id}                          anonymous read
//	POST /api/v1/connectors/{id}/run                      admin
//
// id = url-escaped CONNECTOR_ID "<provider_id>/<source_name>".

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/ICN"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func init() {
	RegisterAdminMount("connectors", mountConnectors)
}

const (
	// ConnectorsPath is the connector lane.
	ConnectorsPath = "/api/v1/connectors"
	// ConnectorsSchemaName is the store form of the $ICN standard.
	ConnectorsSchemaName = "ICN.fbs"

	// connectorsSnapshotInterval bounds how often the live set is recomputed
	// under read load — the same 5 s the dashboard stats lane uses.
	connectorsSnapshotInterval = 5 * time.Second
	// connectorPersistCoalesce batches ingest observations before the lane's
	// $ICN row is rewritten (one GP payload lands as OMM and MPE back to back).
	connectorPersistCoalesce = 2 * time.Second
	// connectorPersistBootDelay lets the store finish coming up before the
	// boot-time persist, which must never sit on the request path.
	connectorPersistBootDelay = 15 * time.Second

	// DefaultMinFetchIntervalMs is sourcemetrics.DefaultDebounceHours in ms.
	DefaultMinFetchIntervalMs = uint64(sourcemetrics.DefaultDebounceHours * float64(time.Hour/time.Millisecond))
)

// $ICN KIND values (icnConnectorKind).
const (
	ICNKindUploadSession   int8 = 0
	ICNKindHttpsPull       int8 = 1
	ICNKindFilesystemWatch int8 = 2
)

// $ICN STATUS values (icnConnectorStatus).
const (
	ICNStatusDraft     int8 = 0
	ICNStatusValidated int8 = 1
	ICNStatusActive    int8 = 2
	ICNStatusPaused    int8 = 3
	ICNStatusError     int8 = 4
	ICNStatusRetired   int8 = 5
)

// Connector is one resolved connector lane and its encoded $ICN frame.
type Connector struct {
	ConnectorID string
	ProviderID  string
	SourceName  string

	Kind          int8
	Status        int8
	StatusMessage string

	EndpointURL string
	HTTPMethod  string

	TargetSchema string
	EmitsSchemas []string

	PollIntervalMs     uint32
	MinFetchIntervalMs uint64
	NextEligibleAt     uint64

	LastHTTPStatus         uint16
	LastSourceEtag         string
	LastSourceLastModified string
	LastDurationMs         uint64
	FetchCount             uint64

	LastBatchID       string
	LastRecordCount   uint64
	LastInsertedCount uint64
	IngestCount       uint64
	LastIngestAt      uint64
	LastErrorAt       uint64
	LastError         string

	ProviderPeerID string

	OriginID   string
	OriginName string
	DatasetID  string
	License    string
	LicenseURL string
	Citation   string

	LastPublicationCID string
	LastPNMCID         string
	FeedHead           string

	CreatedAt uint64
	UpdatedAt uint64

	// RecordCount is the number of records this node holds for the lane
	// (not an $ICN field; the UI reads counts from $NDS).
	RecordCount int64
	// AppID is the flow program that owns the lane's fetch on this node
	// (sourcemetrics app_id), empty when none does.
	AppID string

	// Frame is the size-prefixed $ICN buffer.
	Frame []byte
}

type connectorLaneKey struct {
	providerID string
	sourceName string
}

func (k connectorLaneKey) id() string {
	return sourcemetrics.SourceID(k.providerID, k.sourceName)
}

// ConnectorsHandler serves and persists the connector set.
type ConnectorsHandler struct {
	deps *AdminMountDeps

	mu         sync.Mutex
	builtAt    time.Time
	connectors []Connector
	buildErr   error

	endpointMu    sync.Mutex
	endpointCache map[connectorLaneKey]connectorEndpoint

	persistOnce sync.Once
	persistCh   chan connectorLaneKey
	stopCh      chan struct{}
	stopOnce    sync.Once
	removeTap   func()
}

type connectorEndpoint struct {
	batchID string
	url     string
}

// NewConnectorsHandler builds a handler over the mount deps.
func NewConnectorsHandler(deps *AdminMountDeps) *ConnectorsHandler {
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	return &ConnectorsHandler{
		deps:          deps,
		endpointCache: map[connectorLaneKey]connectorEndpoint{},
		persistCh:     make(chan connectorLaneKey, 256),
		stopCh:        make(chan struct{}),
	}
}

func mountConnectors(mux *http.ServeMux, deps *AdminMountDeps) {
	h := NewConnectorsHandler(deps)
	h.RegisterRoutes(mux)
	h.StartPersistence()
	log.Infof("Connector lane ($ICN) mounted at %s", ConnectorsPath)
}

// RegisterRoutes mounts the connector routes.
func (h *ConnectorsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(ConnectorsPath, h.handleList)
	mux.HandleFunc(ConnectorsPath+"/", h.handleItem)
}

// StartPersistence writes every connector's $ICN row once (after a short
// settle delay) and again for a lane after each ingest observation on it.
func (h *ConnectorsHandler) StartPersistence() {
	if h == nil || h.deps == nil || h.deps.Store == nil {
		return
	}
	h.persistOnce.Do(func() {
		h.removeTap = caps.AddIngestObserver(func(obs caps.IngestObservation) {
			key := connectorLaneKey{providerID: strings.TrimSpace(obs.ProviderID), sourceName: strings.TrimSpace(obs.SourceName)}
			if key.id() == "" {
				return
			}
			select {
			case h.persistCh <- key:
			default:
				// A full queue means a persist is already pending; the
				// coalesced pass will pick the lane up from the live set.
			}
		})
		go h.persistLoop()
	})
}

// Stop detaches the ingest tap and ends the persist loop.
func (h *ConnectorsHandler) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() {
		if h.removeTap != nil {
			h.removeTap()
		}
		close(h.stopCh)
	})
}

func (h *ConnectorsHandler) persistLoop() {
	boot := time.NewTimer(connectorPersistBootDelay)
	defer boot.Stop()
	pending := map[connectorLaneKey]struct{}{}
	var flush *time.Timer
	var flushC <-chan time.Time
	for {
		select {
		case <-h.stopCh:
			return
		case <-boot.C:
			if n, err := PersistConnectors(h.deps); err != nil {
				log.Warnf("Connector lane: boot persist wrote %d $ICN row(s): %v", n, err)
			} else if n > 0 {
				log.Infof("Connector lane: persisted %d $ICN row(s)", n)
			}
		case key := <-h.persistCh:
			pending[key] = struct{}{}
			if flush == nil {
				flush = time.NewTimer(connectorPersistCoalesce)
				flushC = flush.C
			}
		case <-flushC:
			keys := make([]connectorLaneKey, 0, len(pending))
			for key := range pending {
				keys = append(keys, key)
			}
			pending = map[connectorLaneKey]struct{}{}
			flush = nil
			flushC = nil
			if _, err := h.persistLanes(keys); err != nil {
				log.Warnf("Connector lane: persist after ingest: %v", err)
			}
		}
	}
}

// persistLanes rebuilds the live set and stores the $ICN rows of the named
// lanes.
func (h *ConnectorsHandler) persistLanes(keys []connectorLaneKey) (int, error) {
	if len(keys) == 0 || h.deps == nil || h.deps.Store == nil {
		return 0, nil
	}
	connectors, err := h.snapshot(true)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, key := range keys {
		want[key.id()] = true
	}
	var stored int
	var errs []error
	for i := range connectors {
		if !want[connectors[i].ConnectorID] {
			continue
		}
		if _, err := h.deps.Store.Store(ConnectorsSchemaName, connectors[i].Frame, h.deps.NodePeerID, nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", connectors[i].ConnectorID, err))
			continue
		}
		stored++
	}
	return stored, errors.Join(errs...)
}

// PersistConnectors computes the live connector set and stores one $ICN row
// per connector through the engine (Store dedupes identical frames by CID).
func PersistConnectors(deps *AdminMountDeps) (int, error) {
	if deps == nil || deps.Store == nil {
		return 0, errors.New("connector persistence needs a store")
	}
	connectors, err := BuildConnectorFrames(deps)
	if err != nil {
		return 0, err
	}
	var stored int
	var errs []error
	for i := range connectors {
		if _, err := deps.Store.Store(ConnectorsSchemaName, connectors[i].Frame, deps.NodePeerID, nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", connectors[i].ConnectorID, err))
			continue
		}
		stored++
	}
	return stored, errors.Join(errs...)
}

// snapshot returns the live set, recomputing it at most every
// connectorsSnapshotInterval unless forced.
func (h *ConnectorsHandler) snapshot(force bool) ([]Connector, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !force && !h.builtAt.IsZero() && time.Since(h.builtAt) < connectorsSnapshotInterval {
		return h.connectors, h.buildErr
	}
	connectors, err := h.build()
	if err != nil && h.connectors != nil {
		// Keep serving the last good set; the error is logged, not surfaced
		// as a blank screen.
		log.Debugf("connector lane rebuild failed; serving the previous set: %v", err)
		h.builtAt = time.Now()
		return h.connectors, nil
	}
	h.connectors, h.buildErr, h.builtAt = connectors, err, time.Now()
	return connectors, err
}

// BuildConnectorFrames computes one connector per lane from the mount deps.
func BuildConnectorFrames(deps *AdminMountDeps) ([]Connector, error) {
	h := NewConnectorsHandler(deps)
	return h.build()
}

type connectorLaneAgg struct {
	schemaCounts  map[string]int64
	producer      string
	producerAt    int64
	newestBatch   map[string]string // schema -> newest batch id
	newestBatchAt map[string]int64
	firstSeen     int64
	lastSeen      int64
}

func (h *ConnectorsHandler) build() ([]Connector, error) {
	deps := h.deps
	if deps == nil || deps.Store == nil {
		return nil, errors.New("connector lane needs a store")
	}
	now := time.Now()

	summary, err := deps.Store.DataSummary()
	if err != nil {
		return nil, fmt.Errorf("read source summaries: %w", err)
	}
	lanes := map[connectorLaneKey]*connectorLaneAgg{}
	laneFor := func(key connectorLaneKey) *connectorLaneAgg {
		agg := lanes[key]
		if agg == nil {
			agg = &connectorLaneAgg{
				schemaCounts:  map[string]int64{},
				newestBatch:   map[string]string{},
				newestBatchAt: map[string]int64{},
			}
			lanes[key] = agg
		}
		return agg
	}
	for _, src := range summary.Sources {
		key := connectorLaneKey{providerID: strings.TrimSpace(src.ProviderID), sourceName: strings.TrimSpace(src.SourceName)}
		if key.id() == "" || src.Count <= 0 {
			continue
		}
		agg := laneFor(key)
		agg.schemaCounts[src.SchemaName] += src.Count
		if agg.producer == "" && strings.TrimSpace(src.ProducerPeerID) != "" {
			agg.producer = strings.TrimSpace(src.ProducerPeerID)
		}
		if _, ok := agg.newestBatch[src.SchemaName]; !ok {
			agg.newestBatch[src.SchemaName] = src.BatchID
		}
	}
	// Recency (newest batch, first/last seen, most recent producer) comes
	// from the per-producer progress scan of the same summary table; it is
	// best effort and never blocks the lane.
	if progress, err := deps.Store.ProducerSourceProgress(); err == nil {
		for _, p := range progress {
			key := connectorLaneKey{providerID: strings.TrimSpace(p.ProviderID), sourceName: strings.TrimSpace(p.SourceName)}
			agg, ok := lanes[key]
			if !ok {
				continue
			}
			if p.UpdatedAtUnix >= agg.producerAt && strings.TrimSpace(p.ProducerPeerID) != "" {
				agg.producer, agg.producerAt = strings.TrimSpace(p.ProducerPeerID), p.UpdatedAtUnix
			}
			if p.LastBatchID != "" && p.UpdatedAtUnix >= agg.newestBatchAt[p.SchemaName] {
				agg.newestBatch[p.SchemaName], agg.newestBatchAt[p.SchemaName] = p.LastBatchID, p.UpdatedAtUnix
			}
			if p.FirstSeenUnix > 0 && (agg.firstSeen == 0 || p.FirstSeenUnix < agg.firstSeen) {
				agg.firstSeen = p.FirstSeenUnix
			}
			if p.LastSeenUnix > agg.lastSeen {
				agg.lastSeen = p.LastSeenUnix
			}
		}
	} else {
		log.Debugf("connector lane: producer progress unavailable: %v", err)
	}

	ledger := map[string]*sourcemetrics.Source{}
	if deps.SourceMetrics != nil {
		rows, err := deps.SourceMetrics.Sources()
		if err != nil {
			log.Debugf("connector lane: retrieval ledger unreadable: %v", err)
		}
		for i := range rows {
			row := &rows[i]
			key := connectorLaneKey{providerID: strings.TrimSpace(row.ProviderID), sourceName: strings.TrimSpace(row.SourceName)}
			if key.id() == "" {
				continue
			}
			ledger[key.id()] = row
			if _, ok := lanes[key]; !ok {
				laneFor(key)
			}
		}
	}

	flows := map[string]FlowServiceInfo{}
	if deps.FlowServices != nil {
		for _, info := range deps.FlowServices() {
			if id := strings.TrimSpace(info.ProgramID); id != "" {
				flows[id] = info
			}
		}
	}

	var configOrigins []config.ConnectorOriginConfig
	var flowServices []config.FlowService
	if deps.Config != nil {
		configOrigins = deps.Config.Connectors.Origins
		flowServices = deps.Config.Flows.Services
	}

	pubCache := map[string][]storage.DatasetShardPublication{}
	publicationsFor := func(schema string) []storage.DatasetShardPublication {
		if pubs, ok := pubCache[schema]; ok {
			return pubs
		}
		pubs, err := deps.Store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, schema)
		if err != nil {
			log.Debugf("connector lane: publications for %s unavailable: %v", schema, err)
			pubs = nil
		}
		pubCache[schema] = pubs
		return pubs
	}

	keys := make([]connectorLaneKey, 0, len(lanes))
	for key := range lanes {
		keys = append(keys, key)
	}
	out := make([]Connector, 0, len(keys))
	for _, key := range keys {
		agg := lanes[key]
		row := ledger[key.id()]
		c := Connector{
			ConnectorID: key.id(),
			ProviderID:  key.providerID,
			SourceName:  key.sourceName,
			UpdatedAt:   uint64(now.UnixMilli()),
		}

		// Emitted schemas, sorted; ledger schemas fill in for a lane with
		// a row but no records held (a failed or superseded pull).
		for schema, count := range agg.schemaCounts {
			c.EmitsSchemas = append(c.EmitsSchemas, schema)
			c.RecordCount += count
		}
		if len(c.EmitsSchemas) == 0 && row != nil {
			for _, schema := range row.LastSchemas {
				if schema = strings.TrimSpace(schema); schema != "" {
					c.EmitsSchemas = append(c.EmitsSchemas, schema)
				}
			}
		}
		sort.Strings(c.EmitsSchemas)

		// Endpoint: ledger source_url, else the newest record's source tag.
		if row != nil {
			c.EndpointURL = strings.TrimSpace(row.SourceURL)
			c.AppID = strings.TrimSpace(row.AppID)
		}
		origin := ResolveConnectorOrigin(row, configOrigins, key.providerID, key.sourceName, c.EndpointURL)
		c.TargetSchema = pickTargetSchema(origin.PrimarySchema, row, agg.schemaCounts, c.EmitsSchemas)
		if c.EndpointURL == "" && c.TargetSchema != "" {
			c.EndpointURL = h.endpointFromTags(key, c.TargetSchema, agg.newestBatch[c.TargetSchema])
			if c.EndpointURL != "" {
				origin = ResolveConnectorOrigin(row, configOrigins, key.providerID, key.sourceName, c.EndpointURL)
			}
		}
		if isHTTPURL(c.EndpointURL) {
			c.Kind, c.HTTPMethod = ICNKindHttpsPull, http.MethodGet
		} else {
			c.Kind = ICNKindUploadSession
		}

		// Producer: the node that ingested the records (never this node
		// unless it produced them).
		c.ProviderPeerID = agg.producer

		// Newest batch + its licence (tier (a) of the origin order).
		newestBatch := agg.newestBatch[c.TargetSchema]
		if row != nil && strings.TrimSpace(row.LastBatchID) != "" {
			c.LastBatchID = strings.TrimSpace(row.LastBatchID)
		} else {
			c.LastBatchID = newestBatch
		}
		if newestBatch != "" && c.TargetSchema != "" {
			if lic, found, err := deps.Store.SourceBatchLicenseFor(c.TargetSchema, key.providerID, key.sourceName, newestBatch); err == nil && found && !lic.IsEmpty() {
				origin.License, origin.LicenseURL, origin.Citation = lic.License, lic.LicenseURL, lic.Citation
			}
		}
		c.OriginID, c.OriginName, c.DatasetID = origin.OriginID, origin.OriginName, origin.DatasetID
		c.License, c.LicenseURL, c.Citation = origin.License, origin.LicenseURL, origin.Citation

		// Cadence: the owning flow's timer, else the registry cadence.
		var owner *FlowServiceInfo
		if c.AppID != "" {
			if info, ok := flows[c.AppID]; ok {
				owner = &info
			}
		}
		if owner != nil && owner.TimerIntervalMs > 0 {
			c.PollIntervalMs = clampUint32(owner.TimerIntervalMs)
		} else if origin.PollIntervalMs > 0 {
			c.PollIntervalMs = clampUint32(origin.PollIntervalMs)
		}
		c.MinFetchIntervalMs = DefaultMinFetchIntervalMs
		if owner != nil && owner.RetrievalInterval > 0 {
			c.MinFetchIntervalMs = uint64(owner.RetrievalInterval.Milliseconds())
		} else if c.AppID != "" {
			for _, svc := range flowServices {
				if strings.TrimSpace(svc.Flow) != c.AppID {
					continue
				}
				if d, ok, err := svc.EffectiveRetrievalInterval(); err == nil && ok && d > 0 {
					c.MinFetchIntervalMs = uint64(d.Milliseconds())
				}
				break
			}
		}

		// Fetch facts for the endpoint, from the ledger's fetch events.
		if row != nil && c.EndpointURL != "" && deps.SourceMetrics != nil {
			status, durationMs, count, _, etag, lastModified := deps.SourceMetrics.FetchEvent(c.EndpointURL)
			if status > 0 {
				c.LastHTTPStatus = uint16(status)
			}
			if durationMs > 0 {
				c.LastDurationMs = uint64(durationMs)
			}
			if count > 0 {
				c.FetchCount = uint64(count)
			}
			c.LastSourceEtag, c.LastSourceLastModified = etag, lastModified
		}
		if row != nil {
			if c.LastHTTPStatus == 0 && row.LastStatus > 0 {
				c.LastHTTPStatus = uint16(row.LastStatus)
			}
			if c.LastDurationMs == 0 && row.LastDurationMs > 0 {
				c.LastDurationMs = uint64(row.LastDurationMs)
			}
			if c.FetchCount == 0 && row.FetchCount > 0 {
				c.FetchCount = uint64(row.FetchCount)
			}
			c.LastRecordCount = uint64(nonNegative(row.LastRecords))
			c.LastInsertedCount = uint64(nonNegative(row.LastInserted))
			c.IngestCount = uint64(nonNegative64(row.IngestCount))
			if row.LastRetrievedAt != nil {
				c.LastIngestAt = uint64(row.LastRetrievedAt.UnixMilli())
			}
		}
		if c.LastIngestAt == 0 && agg.lastSeen > 0 {
			c.LastIngestAt = uint64(agg.lastSeen) * 1000
		}
		if agg.firstSeen > 0 {
			c.CreatedAt = uint64(agg.firstSeen) * 1000
		} else {
			c.CreatedAt = c.LastIngestAt
		}

		// Next eligible fetch: the attempt stamp (or the last pull) plus the
		// debounce window widened by consecutive failures.
		if row != nil && deps.SourceMetrics != nil {
			baseHours := float64(c.MinFetchIntervalMs) / float64(time.Hour/time.Millisecond)
			var candidates []time.Time
			if c.AppID != "" {
				if last, failures := deps.SourceMetrics.AttemptState(c.AppID); last != nil {
					hours := sourcemetrics.EffectiveDebounceHoursFrom(baseHours, failures)
					candidates = append(candidates, last.Add(time.Duration(hours*float64(time.Hour))))
				}
			}
			if row.LastRetrievedAt != nil {
				candidates = append(candidates, row.LastRetrievedAt.Add(time.Duration(baseHours*float64(time.Hour))))
			}
			for _, t := range candidates {
				if ms := uint64(t.UnixMilli()); ms > c.NextEligibleAt {
					c.NextEligibleAt = ms
				}
			}
		}

		// Status.
		c.Status, c.StatusMessage = connectorStatus(row, owner, c.LastHTTPStatus)
		if c.Status == ICNStatusError && row != nil {
			switch {
			case row.Invalidated:
				c.LastError = strings.TrimSpace(row.InvalidatedReason)
				if row.InvalidatedAt != nil {
					c.LastErrorAt = uint64(row.InvalidatedAt.UnixMilli())
				}
			case strings.TrimSpace(row.LastError) != "":
				c.LastError = strings.TrimSpace(row.LastError)
				c.LastErrorAt = c.LastIngestAt
			default:
				c.LastError = c.StatusMessage
				c.LastErrorAt = c.LastIngestAt
			}
		}

		// Newest publication across the emitted schemas.
		var newestPub *storage.DatasetShardPublication
		for _, schema := range c.EmitsSchemas {
			for i := range publicationsFor(schema) {
				pub := &pubCache[schema][i]
				if pub.ProviderID != key.providerID || pub.SourceName != key.sourceName {
					continue
				}
				if newestPub == nil || pub.FeedSequence > newestPub.FeedSequence ||
					(pub.FeedSequence == newestPub.FeedSequence && pub.PublishedAt.After(newestPub.PublishedAt)) {
					newestPub = pub
				}
			}
		}
		if newestPub != nil {
			c.LastPublicationCID, c.LastPNMCID, c.FeedHead = newestPub.ManifestCID, newestPub.PNMCID, newestPub.FeedHead
		}

		c.Frame = encodeICN(&c)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OriginID != out[j].OriginID {
			return out[i].OriginID < out[j].OriginID
		}
		if out[i].SourceName != out[j].SourceName {
			return out[i].SourceName < out[j].SourceName
		}
		return out[i].ProviderID < out[j].ProviderID
	})
	return out, nil
}

// connectorStatus maps ledger + flow facts onto the $ICN STATUS words.
func connectorStatus(row *sourcemetrics.Source, owner *FlowServiceInfo, lastHTTPStatus uint16) (int8, string) {
	if row == nil {
		return ICNStatusValidated, "Records are held here; this node has not fetched them."
	}
	if row.Invalidated {
		msg := strings.TrimSpace(row.InvalidatedReason)
		if msg == "" {
			msg = "The last recorded pull could not be confirmed against the records held here."
		}
		return ICNStatusError, msg
	}
	if msg := strings.TrimSpace(row.LastError); msg != "" {
		return ICNStatusError, "The last fetch failed: " + msg
	}
	if lastHTTPStatus >= 400 {
		return ICNStatusError, fmt.Sprintf("The last fetch answered HTTP %d.", lastHTTPStatus)
	}
	if owner == nil || !owner.Running {
		if strings.TrimSpace(row.AppID) == "" {
			return ICNStatusPaused, "No fetch on this node owns this source."
		}
		return ICNStatusPaused, "The fetch that owns this source is not running on this node."
	}
	if lastHTTPStatus == 0 {
		return ICNStatusActive, "Fetching on schedule."
	}
	return ICNStatusActive, fmt.Sprintf("Fetching on schedule; the last fetch answered HTTP %d.", lastHTTPStatus)
}

// pickTargetSchema chooses the primary emitted schema: the registry's, else
// the ledger's first, else the schema with the most records.
func pickTargetSchema(primary string, row *sourcemetrics.Source, counts map[string]int64, emitted []string) string {
	has := func(schema string) bool {
		for _, s := range emitted {
			if s == schema {
				return true
			}
		}
		return false
	}
	if primary != "" && (len(emitted) == 0 || has(primary)) {
		return primary
	}
	if row != nil {
		for _, schema := range row.LastSchemas {
			if schema = strings.TrimSpace(schema); schema != "" && (len(emitted) == 0 || has(schema)) {
				return schema
			}
		}
	}
	best, bestCount := "", int64(-1)
	for _, schema := range emitted {
		if count := counts[schema]; count > bestCount || (count == bestCount && schema < best) {
			best, bestCount = schema, count
		}
	}
	return best
}

// endpointFromTags reads the newest record's source_url for a lane, cached
// per newest batch so the read happens once per batch, not per snapshot.
func (h *ConnectorsHandler) endpointFromTags(key connectorLaneKey, schema, batchID string) string {
	if h == nil || h.deps == nil || h.deps.Store == nil || schema == "" {
		return ""
	}
	h.endpointMu.Lock()
	cached, ok := h.endpointCache[key]
	h.endpointMu.Unlock()
	if ok && cached.batchID == batchID {
		return cached.url
	}
	sourceURL := ""
	records, err := h.deps.Store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: schema,
		ProviderID: key.providerID,
		SourceName: key.sourceName,
		BatchID:    batchID,
		Limit:      1,
	})
	if err == nil && len(records) > 0 {
		if tags, err := h.deps.Store.GetSourceTags(schema, records[0].CID); err == nil {
			sourceURL = strings.TrimSpace(tags.SourceURL)
		}
	}
	h.endpointMu.Lock()
	h.endpointCache[key] = connectorEndpoint{batchID: batchID, url: sourceURL}
	h.endpointMu.Unlock()
	return sourceURL
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

func clampUint32(v int64) uint32 {
	if v <= 0 {
		return 0
	}
	if v > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}

func nonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func nonNegative64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// encodeICN serializes one size-prefixed $ICN frame.
func encodeICN(c *Connector) []byte {
	b := flatbuffers.NewBuilder(1024)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	connectorID := str(c.ConnectorID)
	targetSchema := str(c.TargetSchema)
	statusMessage := str(c.StatusMessage)
	endpointURL := str(c.EndpointURL)
	httpMethod := str(c.HTTPMethod)
	lastError := str(c.LastError)
	providerPeerID := str(c.ProviderPeerID)
	originID := str(c.OriginID)
	originName := str(c.OriginName)
	datasetID := str(c.DatasetID)
	providerID := str(c.ProviderID)
	sourceName := str(c.SourceName)
	license := str(c.License)
	licenseURL := str(c.LicenseURL)
	citation := str(c.Citation)
	lastEtag := str(c.LastSourceEtag)
	lastModified := str(c.LastSourceLastModified)
	lastBatchID := str(c.LastBatchID)
	lastPublicationCID := str(c.LastPublicationCID)
	lastPNMCID := str(c.LastPNMCID)
	feedHead := str(c.FeedHead)

	var emits flatbuffers.UOffsetT
	if len(c.EmitsSchemas) > 0 {
		offsets := make([]flatbuffers.UOffsetT, len(c.EmitsSchemas))
		for i, schema := range c.EmitsSchemas {
			offsets[i] = b.CreateString(schema)
		}
		ICN.ICNStartEMITS_SCHEMASVector(b, len(offsets))
		for i := len(offsets) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offsets[i])
		}
		emits = b.EndVector(len(offsets))
	}

	ICN.ICNStart(b)
	if connectorID != 0 {
		ICN.ICNAddCONNECTOR_ID(b, connectorID)
	}
	ICN.ICNAddKIND(b, enumOf(ICN.EnumValuesicnConnectorKind, c.Kind))
	if targetSchema != 0 {
		ICN.ICNAddTARGET_SCHEMA(b, targetSchema)
	}
	ICN.ICNAddSTATUS(b, enumOf(ICN.EnumValuesicnConnectorStatus, c.Status))
	if statusMessage != 0 {
		ICN.ICNAddSTATUS_MESSAGE(b, statusMessage)
	}
	if endpointURL != 0 {
		ICN.ICNAddENDPOINT_URL(b, endpointURL)
	}
	if httpMethod != 0 {
		ICN.ICNAddHTTP_METHOD(b, httpMethod)
	}
	ICN.ICNAddPOLL_INTERVAL_MS(b, c.PollIntervalMs)
	ICN.ICNAddLAST_INGEST_AT(b, c.LastIngestAt)
	ICN.ICNAddLAST_ERROR_AT(b, c.LastErrorAt)
	if lastError != 0 {
		ICN.ICNAddLAST_ERROR(b, lastError)
	}
	ICN.ICNAddCREATED_AT(b, c.CreatedAt)
	ICN.ICNAddUPDATED_AT(b, c.UpdatedAt)
	if providerPeerID != 0 {
		ICN.ICNAddPROVIDER_PEER_ID(b, providerPeerID)
	}
	if originID != 0 {
		ICN.ICNAddORIGIN_ID(b, originID)
	}
	if originName != 0 {
		ICN.ICNAddORIGIN_NAME(b, originName)
	}
	if datasetID != 0 {
		ICN.ICNAddDATASET_ID(b, datasetID)
	}
	if providerID != 0 {
		ICN.ICNAddPROVIDER_ID(b, providerID)
	}
	if sourceName != 0 {
		ICN.ICNAddSOURCE_NAME(b, sourceName)
	}
	if license != 0 {
		ICN.ICNAddLICENSE(b, license)
	}
	if licenseURL != 0 {
		ICN.ICNAddLICENSE_URL(b, licenseURL)
	}
	if citation != 0 {
		ICN.ICNAddCITATION(b, citation)
	}
	ICN.ICNAddMIN_FETCH_INTERVAL_MS(b, c.MinFetchIntervalMs)
	ICN.ICNAddNEXT_ELIGIBLE_AT(b, c.NextEligibleAt)
	ICN.ICNAddLAST_HTTP_STATUS(b, c.LastHTTPStatus)
	if lastEtag != 0 {
		ICN.ICNAddLAST_SOURCE_ETAG(b, lastEtag)
	}
	if lastModified != 0 {
		ICN.ICNAddLAST_SOURCE_LAST_MODIFIED(b, lastModified)
	}
	if lastBatchID != 0 {
		ICN.ICNAddLAST_BATCH_ID(b, lastBatchID)
	}
	ICN.ICNAddLAST_RECORD_COUNT(b, c.LastRecordCount)
	ICN.ICNAddLAST_INSERTED_COUNT(b, c.LastInsertedCount)
	ICN.ICNAddLAST_DURATION_MS(b, c.LastDurationMs)
	ICN.ICNAddFETCH_COUNT(b, c.FetchCount)
	ICN.ICNAddINGEST_COUNT(b, c.IngestCount)
	if lastPublicationCID != 0 {
		ICN.ICNAddLAST_PUBLICATION_CID(b, lastPublicationCID)
	}
	if lastPNMCID != 0 {
		ICN.ICNAddLAST_PNM_CID(b, lastPNMCID)
	}
	if feedHead != 0 {
		ICN.ICNAddFEED_HEAD(b, feedHead)
	}
	if emits != 0 {
		ICN.ICNAddEMITS_SCHEMAS(b, emits)
	}
	root := ICN.ICNEnd(b)
	ICN.FinishSizePrefixedICNBuffer(b, root)
	return b.FinishedBytes()
}

// DecodeICN reads a size-prefixed (or bare) $ICN frame.
func DecodeICN(frame []byte) (*ICN.ICN, error) {
	switch {
	case len(frame) >= frameIdentifierOffset+frameIdentifierLength && ICN.SizePrefixedICNBufferHasIdentifier(frame):
		return ICN.GetSizePrefixedRootAsICN(frame, 0), nil
	case len(frame) >= framePrefixLength+frameIdentifierLength && ICN.ICNBufferHasIdentifier(frame):
		return ICN.GetRootAsICN(frame, 0), nil
	}
	return nil, fmt.Errorf("frame is not a $ICN buffer (identifier %q)", FrameIdentifier(frame))
}

// --- HTTP ---

func normalizeSchemaFilter(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	return strings.TrimSuffix(raw, ".FBS")
}

func connectorMatchesSchema(c *Connector, code string) bool {
	if code == "" {
		return true
	}
	if normalizeSchemaFilter(c.TargetSchema) == code {
		return true
	}
	for _, schema := range c.EmitsSchemas {
		if normalizeSchemaFilter(schema) == code {
			return true
		}
	}
	return false
}

func (h *ConnectorsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read connectors.", 0)
		return
	}
	connectors, err := h.snapshot(false)
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "connectors_unavailable",
			"The connector set could not be read right now; try again shortly.", 5*time.Second)
		return
	}
	origin := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("origin")))
	schema := normalizeSchemaFilter(r.URL.Query().Get("schema"))
	frames := make([][]byte, 0, len(connectors))
	for i := range connectors {
		c := &connectors[i]
		if origin != "" && strings.ToLower(c.OriginID) != origin {
			continue
		}
		if !connectorMatchesSchema(c, schema) {
			continue
		}
		frames = append(frames, c.Frame)
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{StreamSchemaHeader: ConnectorsSchemaName})
}

func (h *ConnectorsHandler) handleItem(w http.ResponseWriter, r *http.Request) {
	// The id is "<provider_id>/<source_name>", url-escaped by the client; the
	// escaped path keeps the %2F so the id survives the split. A literal
	// slash is tolerated too: everything before a trailing "run" is the id.
	rest := strings.TrimPrefix(r.URL.EscapedPath(), ConnectorsPath+"/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		h.handleList(w, r)
		return
	}
	action := ""
	if len(parts) > 1 && parts[len(parts)-1] == "run" {
		action = "run"
		parts = parts[:len(parts)-1]
	}
	id, err := url.PathUnescape(strings.Join(parts, "/"))
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_connector_id", "The connector id could not be decoded.", 0)
		return
	}
	id = strings.TrimSpace(id)
	switch action {
	case "":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read a connector.", 0)
			return
		}
		c, ok := h.find(id, false)
		if !ok {
			WriteErrorFrame(w, http.StatusNotFound, "not_found", fmt.Sprintf("No connector named %s on this node.", id), 0)
			return
		}
		WriteFrameStream(w, http.StatusOK, [][]byte{c.Frame}, map[string]string{StreamSchemaHeader: ConnectorsSchemaName})
	case "run":
		h.deps.adminGate(func(w http.ResponseWriter, r *http.Request) {
			h.handleRun(w, r, id)
		})(w, r)
	default:
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "No such connector route.", 0)
	}
}

func (h *ConnectorsHandler) find(id string, force bool) (Connector, bool) {
	connectors, err := h.snapshot(force)
	if err != nil {
		return Connector{}, false
	}
	for i := range connectors {
		if connectors[i].ConnectorID == id {
			return connectors[i], true
		}
	}
	return Connector{}, false
}

func (h *ConnectorsHandler) handleRun(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST to run a connector.", 0)
		return
	}
	c, ok := h.find(id, false)
	if !ok {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", fmt.Sprintf("No connector named %s on this node.", id), 0)
		return
	}
	const noFetch = "This node does not run a fetch for this source."
	if h.deps.RunFlowNow == nil || c.AppID == "" {
		WriteErrorFrame(w, http.StatusNotFound, "no_fetch", noFetch, 0)
		return
	}
	if h.deps.FlowServices != nil {
		owned := false
		for _, info := range h.deps.FlowServices() {
			if strings.TrimSpace(info.ProgramID) == c.AppID {
				owned = true
				break
			}
		}
		if !owned {
			WriteErrorFrame(w, http.StatusNotFound, "no_fetch", noFetch, 0)
			return
		}
	}
	ctx := r.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	skipped, reason, err := h.deps.RunFlowNow(ctx, c.AppID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			WriteErrorFrame(w, http.StatusNotFound, "no_fetch", noFetch, 0)
			return
		}
		WriteErrorFrame(w, http.StatusInternalServerError, "fetch_failed", "The fetch could not be started: "+err.Error(), 0)
		return
	}
	if skipped {
		retryAfter := time.Minute
		msg := reason
		if c.NextEligibleAt > 0 {
			next := time.UnixMilli(int64(c.NextEligibleAt)).UTC()
			msg = "Next eligible at " + next.Format(time.RFC3339)
			if until := time.Until(next); until > 0 {
				retryAfter = until
			}
		}
		if strings.TrimSpace(msg) == "" {
			msg = "The fetch is inside its minimum interval."
		}
		WriteErrorFrame(w, http.StatusTooManyRequests, "debounce", msg, retryAfter)
		return
	}
	fresh, ok := h.find(id, true)
	if !ok {
		fresh = c
	}
	WriteFrameStream(w, http.StatusAccepted, [][]byte{fresh.Frame}, map[string]string{StreamSchemaHeader: ConnectorsSchemaName})
}
