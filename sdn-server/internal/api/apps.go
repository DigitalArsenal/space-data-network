package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// GET /api/apps — the $APPS feed.
//
// What is running on this node, and for each retrieval app, what it has
// actually pulled: last_retrieved_at, the debounce window it honours, the size
// of the last pull, and the last publication notification — all keyed by SOURCE
// ID, all read from the host's operational ledger (internal/sourcemetrics).
//
// ANONYMOUS-SAFE by construction. Everything here is either already public
// (the record store's own published provenance) or an operational fact about
// public data retrieval: app id/name/version, timer cadence, run counts, source
// URLs that are themselves public endpoints, byte counts and timestamps. No
// credentials, no operator identity, no local filesystem paths, no peer
// addresses. A read of this endpoint must never be a privilege.
//
// The app list is read from the FLOW SERVICE REGISTRY (timer-served flow
// bundles registered with the plugin manager) — NOT the legacy module list,
// which is empty on a flow-only node.

// AppsRuntimeSource supplies the runtime snapshot of loaded apps. Injected by
// the node so this package does not reach into the plugin manager's lifecycle.
type AppsRuntimeSource func() plugins.RuntimeSnapshot

// AppsMetricsSource supplies the operational retrieval ledger.
type AppsMetricsSource func() ([]sourcemetrics.Source, error)

// AppsHandler serves the anonymous $APPS feed.
type AppsHandler struct {
	runtime AppsRuntimeSource
	metrics AppsMetricsSource
	// store is used ONLY to refresh each source's last PNM from the
	// authoritative dataset-publication index. Nil is fine — the feed then
	// reports the last PNM the ledger already persisted. It is an interface so
	// the "this feed always answers" property can be tested against a store
	// that is deliberately slow.
	store datasetPublicationReader
	// producers reports what OTHER nodes have contributed to this node's store.
	// Nil is fine — the feed then reports only what this node pulled itself.
	producers producerProgressReader
	// selfPeerID is this node's own libp2p identity, used to tell records this
	// node published from records a peer sent it. Empty is fine: the feed then
	// relies on the ledger check alone, which is already the primary filter.
	selfPeerID string
	// pnmSink write-through-persists a refreshed PNM so the value survives a
	// restart and stays available when the store is busy. May be nil.
	pnmSink func(providerID, sourceName string, pnm sourcemetrics.PNM)
	rl      *rateLimiter
}

// NewAppsHandler constructs the $APPS handler. Any dependency may be nil; the
// feed degrades to whatever it can honestly report.
func NewAppsHandler(runtime AppsRuntimeSource, metrics AppsMetricsSource, store *storage.FlatSQLStore,
	pnmSink func(providerID, sourceName string, pnm sourcemetrics.PNM)) *AppsHandler {
	h := &AppsHandler{runtime: runtime, metrics: metrics, pnmSink: pnmSink, rl: newRateLimiter()}
	// A typed-nil *FlatSQLStore in an interface is not == nil, so only attach a
	// real store; otherwise the nil-store path below would never be taken.
	if store != nil {
		h.store = store
		h.producers = store
	}
	return h
}

// WithSelfPeerID tells the feed which producer identity is this node's own, so
// records this node published are never reported as received from a peer.
// Optional: an unset id only costs precision in that one distinction.
func (h *AppsHandler) WithSelfPeerID(peerID string) *AppsHandler {
	h.selfPeerID = strings.TrimSpace(peerID)
	return h
}

// datasetPublicationReader is the one store capability this feed needs.
type datasetPublicationReader interface {
	ListDatasetShardPublications(storage.DatasetShardPublicationQuery) ([]storage.DatasetShardPublication, error)
}

// producerProgressReader supplies per-producer contribution to each source
// lane, so the feed can report data this node RECEIVED as well as data it
// pulled. Separate from datasetPublicationReader so a test can be slow in one
// and fast in the other.
type producerProgressReader interface {
	ProducerSourceProgress() ([]storage.ProducerSourceProgress, error)
}

// RegisterRoutes registers the public $APPS route.
func (h *AppsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		if !h.rl.Allow(w, r) {
			return
		}
		h.handleApps(w, r)
	})
}

// appSourceView is one retrieval source in the feed.
type appSourceView struct {
	SourceID   string `json:"source_id"`
	AppID      string `json:"app_id,omitempty"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	SourceURL  string `json:"source_url,omitempty"`
	// Origin is "retrieved" for every row here: these records were PULLED from a
	// publisher — by this node, or by the peer that sent them — never fitted or
	// otherwise derived.
	Origin string `json:"origin"`
	// Via says HOW the records reached this node: "local" when this node pulled
	// them and has a retrieval ledger row to prove it, "pubsub" when they
	// arrived from another producer over the network. A node that showed only
	// its own pulls looked idle while a peer was filling its store.
	Via string `json:"via,omitempty"`
	// ProducerPeerID attributes a received row to the peer that produced it.
	// Empty on local rows: this node is the producer, and its own identity is
	// already published elsewhere on this surface.
	ProducerPeerID string `json:"producer_peer_id,omitempty"`
	// SchemaName is the record type a received row carries. Local rows report
	// their schemas per pull in last_schemas instead.
	SchemaName string `json:"schema_name,omitempty"`
	// RecordCount / TotalBytes are cumulative for a received row: what this
	// producer has contributed to this lane in total, not "the last pull",
	// because a receiver never sees pulls — only records arriving.
	RecordCount int64  `json:"record_count,omitempty"`
	TotalBytes  int64  `json:"total_bytes,omitempty"`
	BatchCount  int64  `json:"batch_count,omitempty"`
	FirstSeenAt string `json:"first_seen_at,omitempty"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`

	LastRetrievedAt string  `json:"last_retrieved_at,omitempty"`
	DebounceHours   float64 `json:"debounce_hours"`
	// NextEligibleAt is last_retrieved_at + the debounce window: the earliest
	// moment this node will pull the source again.
	NextEligibleAt string `json:"next_eligible_at,omitempty"`

	LastPullSizeBytes int64 `json:"last_pull_size_bytes"`

	LastStatus     int    `json:"last_status,omitempty"`
	LastDurationMs int64  `json:"last_duration_ms,omitempty"`
	LastError      string `json:"last_error,omitempty"`

	LastBatchID       string   `json:"last_batch_id,omitempty"`
	LastBatchRepeated bool     `json:"last_batch_repeated"`
	LastSchemas       []string `json:"last_schemas,omitempty"`
	// LastRecords is what the last pull PARSED; LastInserted is what was new.
	// Neither is omitted when zero: "0 inserted" is the signal that a pull
	// found nothing the store did not already hold, which is the single most
	// useful number on the board and must never render as absent.
	LastRecords  int `json:"last_records"`
	LastInserted int `json:"last_inserted"`

	FetchCount  int64 `json:"fetch_count"`
	IngestCount int64 `json:"ingest_count"`

	LastPNM *appPNMView `json:"last_pnm,omitempty"`
}

type appPNMView struct {
	ID          string `json:"id,omitempty"`
	CID         string `json:"cid"`
	Schema      string `json:"schema,omitempty"`
	FeedHead    string `json:"feed_head,omitempty"`
	RecordCount int    `json:"record_count,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

type appTimerView struct {
	TriggerID     string  `json:"trigger_id"`
	IntervalMs    uint64  `json:"interval_ms,omitempty"`
	IntervalHours float64 `json:"interval_hours,omitempty"`
	Description   string  `json:"description,omitempty"`
}

type appView struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// Kind is "flow" for timer-served flow bundles (the ingest apps) and
	// "module" for anything else the runtime registry reports.
	Kind          string          `json:"kind"`
	Status        string          `json:"status,omitempty"`
	StatusMessage string          `json:"status_message,omitempty"`
	UptimeMs      int64           `json:"uptime_ms,omitempty"`
	RunCount      uint64          `json:"run_count"`
	ErrorCount    uint64          `json:"error_count"`
	LastRunAt     string          `json:"last_run_at,omitempty"`
	LastRunStatus string          `json:"last_run_status,omitempty"`
	Timers        []appTimerView  `json:"timers,omitempty"`
	Sources       []appSourceView `json:"sources"`
}

type appsResponse struct {
	GeneratedAt string          `json:"generated_at"`
	Count       int             `json:"count"`
	Apps        []appView       `json:"apps"`
	Sources     []appSourceView `json:"sources"`
}

func (h *AppsHandler) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	sources := h.sourceViews()
	// Data this node RECEIVED from other producers belongs on the same board as
	// data it pulled: the question "is this node getting the catalog?" has the
	// same answer either way. Appended after the ledger rows and bounded by the
	// same rule — the feed always answers, even mid-ingest.
	sources = append(sources, h.remoteProducerViews()...)

	// Group sources under the app that produced them. Attribution comes from
	// the producing module/flow id the storage connector recorded — the host
	// never needs to know which app "owns" which data source.
	byApp := map[string][]appSourceView{}
	for _, src := range sources {
		if src.AppID != "" {
			byApp[src.AppID] = append(byApp[src.AppID], src)
		}
	}

	apps := make([]appView, 0, 4)
	if h.runtime != nil {
		snapshot := h.runtime()
		for _, entry := range snapshot.Modules {
			view := appView{
				ID:            entry.ID,
				Version:       entry.Version,
				Kind:          "module",
				Status:        entry.Status,
				StatusMessage: entry.StatusMessage,
				UptimeMs:      entry.Stats.UptimeMs,
				RunCount:      entry.Stats.TimerRunCount,
				ErrorCount:    entry.Stats.ErrorCount,
				LastRunAt:     entry.Stats.LastInvokeAt,
				LastRunStatus: entry.Stats.LastTimerStatus,
			}
			if entry.Manifest != nil {
				view.Name = entry.Manifest.Name
				if strings.EqualFold(entry.Manifest.PluginFamily, "FLOW") {
					view.Kind = "flow"
				}
				for _, timer := range entry.Manifest.Timers {
					t := appTimerView{
						TriggerID:   timer.TimerID,
						IntervalMs:  timer.DefaultIntervalMs,
						Description: timer.Description,
					}
					if timer.DefaultIntervalMs > 0 {
						t.IntervalHours = float64(timer.DefaultIntervalMs) / float64(time.Hour/time.Millisecond)
					}
					view.Timers = append(view.Timers, t)
				}
			}
			view.Sources = byApp[entry.ID]
			if view.Sources == nil {
				view.Sources = []appSourceView{}
			}
			apps = append(apps, view)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })

	writeJSON(w, http.StatusOK, appsResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Count:       len(apps),
		Apps:        apps,
		Sources:     sources,
	})
}

// pnmRefreshBudget bounds the OPTIONAL authoritative PNM lookup for the WHOLE
// feed. The record store is single-writer and a full CelesTrak SATCAT ingest
// occupies it for tens of minutes on a small host; measured on host-01
// mid-ingest, /api/v1/id answered in 10 ms, /api/v1/stats in 2.4 s, and this
// feed — which queried the store once per source with no bound — did not answer
// at all. A status surface that stops answering exactly when the node is busiest
// is worse than useless: "is it working?" is asked PRECISELY then. So the
// durable ledger is the answer, and the store refresh is a best-effort garnish
// that is abandoned the moment it is slow.
const pnmRefreshBudget = 750 * time.Millisecond

// sourceViews reads the ledger and refreshes each row's last PNM from the
// authoritative dataset-publication index, within a strict overall budget.
func (h *AppsHandler) sourceViews() []appSourceView {
	out := make([]appSourceView, 0, 8)
	if h.metrics == nil {
		return out
	}
	rows, err := h.metrics()
	if err != nil {
		log.Warnf("apps feed: read source metrics: %v", err)
		return out
	}

	// Best-effort authoritative PNM refresh, bounded for the whole feed. The
	// lookup runs on its own goroutine so a store blocked behind an ingest
	// cannot hold the response: when the budget expires the ledger's persisted
	// values are served instead, and the abandoned goroutine's late result is
	// simply discarded.
	refreshed := h.refreshPNMsWithinBudget(rows)

	for _, row := range rows {
		view := appSourceView{
			SourceID:          row.SourceID,
			AppID:             row.AppID,
			ProviderID:        row.ProviderID,
			SourceName:        row.SourceName,
			SourceURL:         row.SourceURL,
			Origin:            row.Origin,
			DebounceHours:     row.DebounceHours,
			LastPullSizeBytes: row.LastPullSizeBytes,
			LastStatus:        row.LastStatus,
			LastDurationMs:    row.LastDurationMs,
			LastError:         row.LastError,
			LastBatchID:       row.LastBatchID,
			LastBatchRepeated: row.LastBatchRepeated,
			LastSchemas:       row.LastSchemas,
			LastRecords:       row.LastRecords,
			LastInserted:      row.LastInserted,
			FetchCount:        row.FetchCount,
			IngestCount:       row.IngestCount,
		}
		if view.Origin == "" {
			view.Origin = "retrieved"
		}
		view.Via = viaLocal
		if row.LastRetrievedAt != nil {
			view.LastRetrievedAt = row.LastRetrievedAt.UTC().Format(time.RFC3339)
			if row.DebounceHours > 0 {
				next := row.LastRetrievedAt.Add(time.Duration(row.DebounceHours * float64(time.Hour)))
				view.NextEligibleAt = next.UTC().Format(time.RFC3339)
			}
		}

		pnm := row.LastPNM
		if fresh, ok := refreshed[row.SourceID]; ok && fresh != nil {
			pnm = fresh
		}
		if pnm != nil && pnm.CID != "" {
			pv := &appPNMView{
				ID:          pnm.ID,
				CID:         pnm.CID,
				Schema:      pnm.Schema,
				FeedHead:    pnm.FeedHead,
				RecordCount: pnm.RecordCount,
			}
			if !pnm.PublishedAt.IsZero() {
				pv.PublishedAt = pnm.PublishedAt.UTC().Format(time.RFC3339)
			}
			view.LastPNM = pv
		}
		out = append(out, view)
	}
	return out
}

// Via values. Lowercase, like every other field this API synthesizes (SDS
// record keys keep their IDL capitalization; these are not record fields).
const (
	viaLocal  = "local"
	viaPubsub = "pubsub"
)

// remoteProducerBudget bounds the received-data read for the WHOLE feed, for
// the same reason pnmRefreshBudget bounds the PNM refresh: this query touches
// the single-writer record store, which a full ingest occupies for tens of
// minutes on a small host. Received rows are valuable, but not more valuable
// than the endpoint answering at all.
const remoteProducerBudget = 750 * time.Millisecond

// remoteProducerViews reports what OTHER producers have contributed to this
// node's store — records that arrived over the network rather than from a pull
// this node performed.
//
// Attribution is by PRODUCER IDENTITY, not by lane. Two nodes pulling the same
// public source is normal and expected, so "this node has a ledger row for that
// lane" does not mean a peer's records are a duplicate: they are a different
// fact about a different producer, and hiding them would make a receiving node
// look idle while its store filled — and would keep hiding them after the node
// stopped pulling that lane itself, since the ledger row survives. So both rows
// stand, told apart by via and producer_peer_id.
//
// This node's own records are excluded: a local publish is never reported as
// somebody else's.
func (h *AppsHandler) remoteProducerViews() []appSourceView {
	out := make([]appSourceView, 0, 4)
	if h.producers == nil {
		return out
	}

	type result struct {
		rows []storage.ProducerSourceProgress
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := h.producers.ProducerSourceProgress()
		done <- result{rows: rows, err: err}
	}()

	var rows []storage.ProducerSourceProgress
	select {
	case r := <-done:
		if r.err != nil {
			log.Warnf("apps feed: read producer source progress: %v", r.err)
			return out
		}
		rows = r.rows
	case <-time.After(remoteProducerBudget):
		// The store is busy — almost certainly ingesting, which is exactly when
		// this endpoint gets asked whether the node is working. Answer with the
		// ledger alone rather than not at all.
		log.Debugf("apps feed: producer source progress exceeded %s budget; serving local ledger only", remoteProducerBudget)
		return out
	}

	for _, row := range rows {
		producer := strings.TrimSpace(row.ProducerPeerID)
		if producer == "" || producer == h.selfPeerID {
			continue
		}
		// The store back-fills an absent producer with the provider id, and
		// stamps locally synthesized rows with a non-peer marker. Neither is a
		// remote producer, and reporting them as one would invent a peer.
		if producer == strings.TrimSpace(row.ProviderID) || producer == localProducerMarker {
			continue
		}
		view := appSourceView{
			SourceID:       strings.Join([]string{producer, row.ProviderID, row.SourceName, row.SchemaName}, "/"),
			ProviderID:     row.ProviderID,
			SourceName:     row.SourceName,
			Origin:         "retrieved",
			Via:            viaPubsub,
			ProducerPeerID: producer,
			SchemaName:     row.SchemaName,
			RecordCount:    row.Count,
			TotalBytes:     row.TotalBytes,
			BatchCount:     row.BatchCount,
			LastBatchID:    row.LastBatchID,
		}
		if row.FirstSeenUnix > 0 {
			view.FirstSeenAt = time.Unix(row.FirstSeenUnix, 0).UTC().Format(time.RFC3339)
		}
		if row.LastSeenUnix > 0 {
			view.LastSeenAt = time.Unix(row.LastSeenUnix, 0).UTC().Format(time.RFC3339)
			view.LastRetrievedAt = view.LastSeenAt
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out
}

// localProducerMarker is what the store stamps on rows it synthesized locally
// (see storage's local-EPM row); it is not a peer identity.
const localProducerMarker = "local-node"

// refreshPNMsWithinBudget looks up the authoritative last PNM for each source,
// abandoning the whole effort once pnmRefreshBudget expires. Results that do
// arrive are written back to the ledger so the next request has them durably
// even if the store is busy again.
func (h *AppsHandler) refreshPNMsWithinBudget(rows []sourcemetrics.Source) map[string]*sourcemetrics.PNM {
	out := map[string]*sourcemetrics.PNM{}
	if h.store == nil || len(rows) == 0 {
		return out
	}
	type result struct {
		sourceID   string
		providerID string
		sourceName string
		pnm        *sourcemetrics.PNM
	}
	results := make(chan result, len(rows))
	go func() {
		for _, row := range rows {
			results <- result{
				sourceID:   row.SourceID,
				providerID: row.ProviderID,
				sourceName: row.SourceName,
				pnm:        h.latestPNM(row.ProviderID, row.SourceName),
			}
		}
	}()

	deadline := time.After(pnmRefreshBudget)
	for range rows {
		select {
		case r := <-results:
			if r.pnm == nil {
				continue
			}
			out[r.sourceID] = r.pnm
			if h.pnmSink != nil {
				h.pnmSink(r.providerID, r.sourceName, *r.pnm)
			}
		case <-deadline:
			return out
		}
	}
	return out
}

// latestPNM reads the most recent dataset-shard publication for a source from
// the record store — the authoritative "last PNM". Returns nil when the store
// is unavailable or nothing has been published for the source yet.
func (h *AppsHandler) latestPNM(providerID, sourceName string) *sourcemetrics.PNM {
	if h.store == nil || strings.TrimSpace(providerID) == "" || strings.TrimSpace(sourceName) == "" {
		return nil
	}
	publications, err := h.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		ProviderID: providerID,
		SourceName: sourceName,
	})
	if err != nil || len(publications) == 0 {
		return nil
	}
	var latest *storage.DatasetShardPublication
	for i := range publications {
		p := &publications[i]
		if p.PNMCID == "" {
			continue
		}
		if latest == nil ||
			p.FeedSequence > latest.FeedSequence ||
			(p.FeedSequence == latest.FeedSequence && p.PublishedAt.After(latest.PublishedAt)) {
			latest = p
		}
	}
	if latest == nil {
		return nil
	}
	return &sourcemetrics.PNM{
		ID:          latest.BatchID,
		CID:         latest.PNMCID,
		Schema:      latest.SchemaName,
		FeedHead:    latest.FeedHead,
		RecordCount: latest.RecordCount,
		PublishedAt: latest.PublishedAt,
	}
}
