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
	// reports the last PNM the ledger already persisted.
	store *storage.FlatSQLStore
	// pnmSink write-through-persists a refreshed PNM so the value survives a
	// restart and stays available when the store is busy. May be nil.
	pnmSink func(providerID, sourceName string, pnm sourcemetrics.PNM)
	rl      *rateLimiter
}

// NewAppsHandler constructs the $APPS handler. Any dependency may be nil; the
// feed degrades to whatever it can honestly report.
func NewAppsHandler(runtime AppsRuntimeSource, metrics AppsMetricsSource, store *storage.FlatSQLStore,
	pnmSink func(providerID, sourceName string, pnm sourcemetrics.PNM)) *AppsHandler {
	return &AppsHandler{runtime: runtime, metrics: metrics, store: store, pnmSink: pnmSink, rl: newRateLimiter()}
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
	// Origin is always "retrieved" for this ledger: these records were PULLED
	// from a publisher, never fitted or otherwise derived by this node.
	Origin string `json:"origin"`

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

// sourceViews reads the ledger and refreshes each row's last PNM from the
// authoritative dataset-publication index.
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
		if row.LastRetrievedAt != nil {
			view.LastRetrievedAt = row.LastRetrievedAt.UTC().Format(time.RFC3339)
			if row.DebounceHours > 0 {
				next := row.LastRetrievedAt.Add(time.Duration(row.DebounceHours * float64(time.Hour)))
				view.NextEligibleAt = next.UTC().Format(time.RFC3339)
			}
		}

		pnm := row.LastPNM
		if refreshed := h.latestPNM(row.ProviderID, row.SourceName); refreshed != nil {
			pnm = refreshed
			if h.pnmSink != nil {
				h.pnmSink(row.ProviderID, row.SourceName, *refreshed)
			}
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
