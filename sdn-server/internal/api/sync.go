package api

// $DSS sync lane (fbcs program): one Data Sync Status record per stored lane
// (schema, provider_id, source_name), computed live from the store's source
// summaries, the dataset publication ledger, the pin ledger, the channel
// subscription registry and the lane's own action state, served as
// size-prefixed $DSS frames. Actions map onto the node's existing primitives
// (channel subscribe, trusted-peer catch-up, Kubo pin, catalog hydrate).
//
//	GET  /api/v1/sync[?schema=&provider_id=&source=&origin=]   anonymous read
//	GET  /api/v1/sync/{schema}/{provider}/{source}             anonymous read
//	POST /api/v1/sync   body = one $DSS with REQUESTED_ACTION   admin
//
// Path segments are url-escaped; "-" stands for an empty provider or source.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func init() {
	RegisterAdminMount("sync", mountSync)
}

const (
	// SyncPath is the sync lane.
	SyncPath = "/api/v1/sync"
	// SyncSchemaName is the store form of the $DSS standard.
	SyncSchemaName = "DSS.fbs"
	// SubscriptionRegistryFileName is the durable subscription list, kept in
	// the storage directory next to the archive plane.
	SubscriptionRegistryFileName = "channel-subscriptions.json"

	// syncActionTimeout bounds one lane action.
	syncActionTimeout = 30 * time.Minute
	// syncRequestFrameBytes bounds a POST /api/v1/sync body.
	syncRequestFrameBytes int64 = 1 << 20
)

// $DSS STATUS values (dssSyncState).
const (
	DSSStateIdle    int8 = 0
	DSSStateSyncing int8 = 1
	DSSStateSynced  int8 = 2
	DSSStateCapped  int8 = 3
	DSSStateError   int8 = 4
)

// $DSS PIN_POLICY values (dssPinPolicy).
const (
	DSSPinNone    int8 = 0
	DSSPinCache   int8 = 1
	DSSPinPin     int8 = 2
	DSSPinArchive int8 = 3
)

// $DSS REQUESTED_ACTION values (dssAction).
const (
	DSSActionNone        int8 = 0
	DSSActionSync        int8 = 1
	DSSActionSubscribe   int8 = 2
	DSSActionUnsubscribe int8 = 3
	DSSActionPin         int8 = 4
	DSSActionUnpin       int8 = 5
	DSSActionHydrate     int8 = 6
)

// $DSS RETENTION values (dssRetention): the lane's rule for what a
// subscription keeps. ReplaceCurrent (the default) supersedes the lane's
// previous batch with each publication; ArchiveAll keeps and pins every one.
const (
	DSSRetentionReplaceCurrent int8 = 0
	DSSRetentionArchiveAll     int8 = 1
)

// retentionWordToOrdinal maps a registry word onto the $DSS enum ordinal.
func retentionWordToOrdinal(word string) int8 {
	if normalized, ok := channels.NormalizeRetention(word); ok && normalized == channels.RetentionArchiveAll {
		return DSSRetentionArchiveAll
	}
	return DSSRetentionReplaceCurrent
}

// retentionOrdinalToWord maps a $DSS enum ordinal onto the registry word.
func retentionOrdinalToWord(ordinal int8) (string, bool) {
	switch ordinal {
	case DSSRetentionReplaceCurrent:
		return channels.RetentionReplaceCurrent, true
	case DSSRetentionArchiveAll:
		return channels.RetentionArchiveAll, true
	}
	return "", false
}

// laneKey names one sync lane. schema is the store form ("OMM.fbs").
type laneKey struct {
	schema     string
	providerID string
	sourceName string
}

func newLaneKey(schema, providerID, sourceName string) laneKey {
	return laneKey{
		schema:     storeSchemaName(schema),
		providerID: strings.TrimSpace(providerID),
		sourceName: strings.TrimSpace(sourceName),
	}
}

// code returns the standard code ("OMM").
func (k laneKey) code() string {
	return strings.ToUpper(strings.TrimSuffix(k.schema, ".fbs"))
}

func (k laneKey) String() string {
	return k.schema + "/" + k.providerID + "/" + k.sourceName
}

// storeSchemaName normalizes "omm", "OMM" or "OMM.fbs" to "OMM.fbs".
func storeSchemaName(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return ""
	}
	return strings.ToUpper(strings.TrimSuffix(schema, ".fbs")) + ".fbs"
}

// SyncFilter narrows a lane listing.
type SyncFilter struct {
	Schema     string
	ProviderID string
	SourceName string
	OriginID   string
	// Exact selects the single lane named by Schema/ProviderID/SourceName
	// (empty provider or source match empty, not "any").
	Exact bool
}

// SyncLane is one resolved lane and its encoded $DSS frame.
type SyncLane struct {
	Key laneKey

	Status            int8
	Error             string
	SyncedRows        uint64
	TotalRows         uint64
	LocalRows         uint64
	PinnedRows        uint64
	MissingRows       uint64
	DeltaRows         uint64
	CachedBytes       uint64
	PinnedBytes       uint64
	ProviderPeerID    string
	ProviderPublicKey string
	SnapshotID        string
	Head              string
	HighWaterMark     string
	QueryProfile      string
	SyncProtocol      string
	LastSyncedAt      string
	LastSyncStartedAt uint64

	DatasetID   string
	ConnectorID string
	OriginID    string
	ChannelID   string
	Topic       string
	Subscribed  bool
	PinPolicy   int8
	Retention   int8
	Visibility  string
	Encryption  string
	GrantState  string

	FeedHead           string
	LastPublicationCID string
	LastPNMCID         string

	RequestedAction int8

	// Frame is the size-prefixed $DSS buffer.
	Frame []byte
}

// laneAction is the per-lane action state the $DSS STATUS reflects.
type laneAction struct {
	running    bool
	action     string
	lastError  string
	startedAt  time.Time
	finishedAt time.Time
	syncedRows int64
}

// SyncHandler serves the sync lane.
type SyncHandler struct {
	deps       *AdminMountDeps
	connectors *ConnectorsHandler

	mu      sync.Mutex
	actions map[laneKey]*laneAction

	subsMu   sync.Mutex
	subsPath string
}

var (
	syncHandlersMu sync.Mutex
	syncHandlers   = map[*AdminMountDeps]*SyncHandler{}
)

// syncHandlerFor returns the one SyncHandler for a deps value, creating it on
// first use. The archive lane shares it so an import shows on the lane's
// $DSS; mount order between lanes is therefore irrelevant.
func syncHandlerFor(deps *AdminMountDeps) *SyncHandler {
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	syncHandlersMu.Lock()
	defer syncHandlersMu.Unlock()
	if h, ok := syncHandlers[deps]; ok {
		return h
	}
	h := NewSyncHandler(deps)
	syncHandlers[deps] = h
	return h
}

// NewSyncHandler builds a handler over the mount deps and loads the durable
// subscription list into the channel registry.
func NewSyncHandler(deps *AdminMountDeps) *SyncHandler {
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	h := &SyncHandler{
		deps:       deps,
		connectors: NewConnectorsHandler(deps),
		actions:    map[laneKey]*laneAction{},
	}
	if deps.Store != nil {
		if dir := deps.Store.ArchiveOutputDir(); dir != "" {
			h.subsPath = filepath.Join(filepath.Dir(dir), SubscriptionRegistryFileName)
		}
	}
	if deps.Channels != nil && deps.Channels.subscriptions != nil {
		if deps.Config != nil {
			deps.Channels.subscriptions.SetDefaultRetention(deps.Config.Subscriptions.EffectiveDefaultRetention())
		}
		if h.subsPath != "" {
			if err := deps.Channels.subscriptions.LoadFrom(h.subsPath); err != nil {
				log.Warnf("Sync lane: subscription list %s not loaded: %v", h.subsPath, err)
			}
		}
	}
	return h
}

func mountSync(mux *http.ServeMux, deps *AdminMountDeps) {
	h := syncHandlerFor(deps)
	h.RegisterRoutes(mux)
	log.Infof("Sync lane ($DSS) mounted at %s", SyncPath)
}

// RegisterRoutes mounts the sync routes.
func (h *SyncHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(SyncPath, h.handleCollection)
	mux.HandleFunc(SyncPath+"/", h.handleLane)
}

// SubscriptionFilePath is where the durable subscription list lives.
func (h *SyncHandler) SubscriptionFilePath() string {
	return h.subsPath
}

// BuildSyncFrames computes the $DSS set for a filter from the mount deps
// alone (no action state: every lane reads IDLE/SYNCED).
func BuildSyncFrames(deps *AdminMountDeps, filter SyncFilter) ([]SyncLane, error) {
	return NewSyncHandler(deps).build(filter)
}

// build computes the lanes matching filter.
func (h *SyncHandler) build(filter SyncFilter) ([]SyncLane, error) {
	deps := h.deps
	if deps == nil || deps.Store == nil {
		return nil, errors.New("sync lane needs a store")
	}
	filter.Schema = storeSchemaName(filter.Schema)
	filter.ProviderID = strings.TrimSpace(filter.ProviderID)
	filter.SourceName = strings.TrimSpace(filter.SourceName)
	filter.OriginID = strings.TrimSpace(filter.OriginID)

	summary, err := deps.Store.DataSummary()
	if err != nil {
		return nil, fmt.Errorf("read source summaries: %w", err)
	}

	type laneAgg struct {
		localRows  int64
		producer   string
		producerPK string
	}
	lanes := map[laneKey]*laneAgg{}
	order := make([]laneKey, 0)
	for _, src := range summary.Sources {
		if src.Count <= 0 {
			continue
		}
		key := newLaneKey(src.SchemaName, src.ProviderID, src.SourceName)
		if key.schema == "" || !laneMatches(key, filter) {
			continue
		}
		agg := lanes[key]
		if agg == nil {
			agg = &laneAgg{}
			lanes[key] = agg
			order = append(order, key)
		}
		agg.localRows += src.Count
		if agg.producer == "" && strings.TrimSpace(src.ProducerPeerID) != "" {
			agg.producer = strings.TrimSpace(src.ProducerPeerID)
			agg.producerPK = strings.TrimSpace(src.ProducerPublicKey)
		}
	}
	if filter.Exact {
		// The named lane answers even before it holds records (an import in
		// flight, a lane just subscribed).
		key := newLaneKey(filter.Schema, filter.ProviderID, filter.SourceName)
		if key.schema != "" {
			if _, ok := lanes[key]; !ok {
				lanes[key] = &laneAgg{}
				order = append(order, key)
			}
		}
	}

	// Connector origin per (provider, source).
	connectorByLane := map[connectorLaneKey]*Connector{}
	if connectors, err := h.connectors.snapshot(false); err == nil {
		for i := range connectors {
			c := &connectors[i]
			connectorByLane[connectorLaneKey{providerID: c.ProviderID, sourceName: c.SourceName}] = c
		}
	} else {
		log.Debugf("sync lane: connectors unavailable: %v", err)
	}

	// Newest publication per lane, one ledger read for every schema.
	newestPub := map[laneKey]*storage.DatasetShardPublication{}
	if pubs, err := deps.Store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, filter.Schema); err == nil {
		for i := range pubs {
			pub := &pubs[i]
			key := newLaneKey(pub.SchemaName, pub.ProviderID, pub.SourceName)
			cur := newestPub[key]
			if cur == nil || pub.FeedSequence > cur.FeedSequence ||
				(pub.FeedSequence == cur.FeedSequence && pub.PublishedAt.After(cur.PublishedAt)) {
				newestPub[key] = pub
			}
		}
	} else {
		log.Debugf("sync lane: publications unavailable: %v", err)
	}

	// Pin ledger, one read, classified per lane.
	pinsByLane := map[laneKey][]storage.PinLedgerEntry{}
	if entries, err := deps.Store.ListPinLedgerEntries(storage.PinLedgerQuery{SchemaName: filter.Schema}); err == nil {
		for _, entry := range entries {
			key := newLaneKey(entry.SchemaName, entry.ProviderID, entry.SourceName)
			pinsByLane[key] = append(pinsByLane[key], entry)
		}
	} else {
		log.Debugf("sync lane: pin ledger unavailable: %v", err)
	}

	// The node default rule: every lane reads it until the subscriber
	// chooses otherwise.
	defaultRetention := channels.RetentionReplaceCurrent
	if deps.Channels != nil && deps.Channels.subscriptions != nil {
		defaultRetention = deps.Channels.subscriptions.DefaultRetention()
	}

	out := make([]SyncLane, 0, len(order))
	for _, key := range order {
		agg := lanes[key]
		lane := SyncLane{
			Key:          key,
			LocalRows:    uint64(nonNegative64(agg.localRows)),
			QueryProfile: storage.DatasetPublicationQueryProfile,
			SyncProtocol: datasync.ProtocolID,
			Visibility:   "public",
			Encryption:   "none",
			GrantState:   "not-required",
			Retention:    retentionWordToOrdinal(defaultRetention),
		}
		lane.ProviderPeerID, lane.ProviderPublicKey = agg.producer, agg.producerPK

		connectorKey := connectorLaneKey{providerID: key.providerID, sourceName: key.sourceName}
		if c := connectorByLane[connectorKey]; c != nil {
			lane.ConnectorID, lane.OriginID, lane.DatasetID = c.ConnectorID, c.OriginID, c.DatasetID
		} else if id := connectorKey.id(); id != "" {
			lane.ConnectorID = id
		}
		if filter.OriginID != "" && lane.OriginID != filter.OriginID {
			continue
		}

		// Channel identity and subscription state.
		if sourceID := datasetPublicationSourceID(key.providerID, key.sourceName); sourceID != "" {
			if channelID, err := channels.FormatChannelID(channels.ChannelIDInput{SourceID: sourceID, StandardCode: key.code()}); err == nil {
				lane.ChannelID = channelID
				if parsed, err := channels.ParseChannelID(channelID); err == nil && deps.Channels != nil {
					state := deps.Channels.subscriptions.Get(parsed)
					lane.Subscribed = state.Subscribed
					lane.Retention = retentionWordToOrdinal(state.Retention)
					if state.Visibility != "" {
						lane.Visibility = state.Visibility
					}
					if state.EncryptionState != "" {
						lane.Encryption = state.EncryptionState
					}
					if state.GrantState != "" {
						lane.GrantState = state.GrantState
					}
				}
			}
		}
		lane.Topic = channels.DiscoveryTopic(key.code())

		// Pin policy from the ledger rows of the lane.
		pins := pinsByLane[key]
		lane.PinPolicy = classifyPinPolicy(pins)

		// Newest publication.
		pub := newestPub[key]
		if pub != nil {
			lane.FeedHead, lane.LastPublicationCID, lane.LastPNMCID = pub.FeedHead, pub.ManifestCID, pub.PNMCID
			lane.TotalRows = uint64(nonNegative(pub.RecordCount))
			if lane.TotalRows > lane.LocalRows {
				lane.DeltaRows = lane.TotalRows - lane.LocalRows
				lane.MissingRows = lane.DeltaRows
			}
		}

		// Local replica facts, only where the ledger knows the lane.
		if pub != nil || len(pins) > 0 {
			if stats, err := deps.Store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
				SchemaName: key.schema, ProviderID: key.providerID, SourceName: key.sourceName,
			}); err == nil {
				for i := range stats {
					stat := &stats[i]
					if strings.TrimSpace(stat.ProviderID) != key.providerID || strings.TrimSpace(stat.SourceName) != key.sourceName {
						continue
					}
					lane.PinnedRows = uint64(nonNegative64(stat.PinnedRows))
					lane.CachedBytes = uint64(nonNegative64(stat.CachedBytes))
					lane.PinnedBytes = uint64(nonNegative64(stat.PinnedBytes))
					lane.SnapshotID, lane.Head, lane.HighWaterMark = stat.SnapshotID, stat.Head, stat.HighWaterMark
					if !stat.LastSyncedAt.IsZero() {
						lane.LastSyncedAt = stat.LastSyncedAt.UTC().Format(time.RFC3339)
					}
					break
				}
			} else {
				log.Debugf("sync lane: replica stats for %s unavailable: %v", key, err)
			}
		}

		// Action state.
		h.mu.Lock()
		if act := h.actions[key]; act != nil {
			lane.Error = act.lastError
			lane.SyncedRows = uint64(nonNegative64(act.syncedRows))
			if !act.startedAt.IsZero() {
				lane.LastSyncStartedAt = uint64(act.startedAt.UnixMilli())
			}
			switch {
			case act.running:
				lane.Status = DSSStateSyncing
			case act.lastError != "":
				lane.Status = DSSStateError
			}
		}
		h.mu.Unlock()
		if lane.Status == DSSStateIdle {
			if lane.LocalRows >= lane.TotalRows {
				lane.Status = DSSStateSynced
			}
		}

		lane.Frame = encodeDSS(&lane)
		out = append(out, lane)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.schema != out[j].Key.schema {
			return out[i].Key.schema < out[j].Key.schema
		}
		if out[i].Key.providerID != out[j].Key.providerID {
			return out[i].Key.providerID < out[j].Key.providerID
		}
		return out[i].Key.sourceName < out[j].Key.sourceName
	})
	return out, nil
}

func laneMatches(key laneKey, filter SyncFilter) bool {
	if filter.Exact {
		return key.schema == filter.Schema && key.providerID == filter.ProviderID && key.sourceName == filter.SourceName
	}
	if filter.Schema != "" && key.schema != filter.Schema {
		return false
	}
	if filter.ProviderID != "" && key.providerID != filter.ProviderID {
		return false
	}
	if filter.SourceName != "" && key.sourceName != filter.SourceName {
		return false
	}
	return true
}

// classifyPinPolicy maps a lane's ledger rows onto the $DSS PIN_POLICY words:
// Archive when an archive-plane row exists, Pin when a verified permanent
// shard pin exists, Cache when any row exists, else None.
func classifyPinPolicy(entries []storage.PinLedgerEntry) int8 {
	policy := DSSPinNone
	for _, entry := range entries {
		switch {
		case entry.Role == storage.PinLedgerRoleArchive:
			return DSSPinArchive
		case entry.Role == "shard" && entry.VerificationState == "verified" && entry.TTL == 0:
			policy = DSSPinPin
		case policy == DSSPinNone:
			policy = DSSPinCache
		}
	}
	return policy
}

// encodeDSS serializes one size-prefixed $DSS frame.
func encodeDSS(l *SyncLane) []byte {
	b := flatbuffers.NewBuilder(1024)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	providerPeerID := str(l.ProviderPeerID)
	providerPublicKey := str(l.ProviderPublicKey)
	snapshotID := str(l.SnapshotID)
	head := str(l.Head)
	highWaterMark := str(l.HighWaterMark)
	queryProfile := str(l.QueryProfile)
	syncProtocol := str(l.SyncProtocol)
	lastSyncedAt := str(l.LastSyncedAt)
	errorText := str(l.Error)
	schemaName := str(l.Key.schema)
	providerID := str(l.Key.providerID)
	sourceName := str(l.Key.sourceName)
	datasetID := str(l.DatasetID)
	connectorID := str(l.ConnectorID)
	channelID := str(l.ChannelID)
	topic := str(l.Topic)
	visibility := str(l.Visibility)
	encryption := str(l.Encryption)
	grantState := str(l.GrantState)
	feedHead := str(l.FeedHead)
	lastPublicationCID := str(l.LastPublicationCID)
	lastPNMCID := str(l.LastPNMCID)
	originID := str(l.OriginID)

	DSS.DSSStart(b)
	DSS.DSSAddSTATUS(b, enumOf(DSS.EnumValuesdssSyncState, l.Status))
	DSS.DSSAddSYNCED_ROWS(b, l.SyncedRows)
	DSS.DSSAddTOTAL_ROWS(b, l.TotalRows)
	DSS.DSSAddLOCAL_ROWS(b, l.LocalRows)
	DSS.DSSAddPINNED_ROWS(b, l.PinnedRows)
	DSS.DSSAddMISSING_ROWS(b, l.MissingRows)
	DSS.DSSAddCACHED_BYTES(b, l.CachedBytes)
	DSS.DSSAddPINNED_BYTES(b, l.PinnedBytes)
	if providerPeerID != 0 {
		DSS.DSSAddPROVIDER_PEER_ID(b, providerPeerID)
	}
	if providerPublicKey != 0 {
		DSS.DSSAddPROVIDER_PUBLIC_KEY(b, providerPublicKey)
	}
	if snapshotID != 0 {
		DSS.DSSAddSNAPSHOT_ID(b, snapshotID)
	}
	if head != 0 {
		DSS.DSSAddHEAD(b, head)
	}
	if highWaterMark != 0 {
		DSS.DSSAddHIGH_WATER_MARK(b, highWaterMark)
	}
	if queryProfile != 0 {
		DSS.DSSAddQUERY_PROFILE(b, queryProfile)
	}
	if syncProtocol != 0 {
		DSS.DSSAddSYNC_PROTOCOL(b, syncProtocol)
	}
	if lastSyncedAt != 0 {
		DSS.DSSAddLAST_SYNCED_AT(b, lastSyncedAt)
	}
	if errorText != 0 {
		DSS.DSSAddERROR(b, errorText)
	}
	if schemaName != 0 {
		DSS.DSSAddSCHEMA_NAME(b, schemaName)
	}
	if providerID != 0 {
		DSS.DSSAddPROVIDER_ID(b, providerID)
	}
	if sourceName != 0 {
		DSS.DSSAddSOURCE_NAME(b, sourceName)
	}
	if datasetID != 0 {
		DSS.DSSAddDATASET_ID(b, datasetID)
	}
	if connectorID != 0 {
		DSS.DSSAddCONNECTOR_ID(b, connectorID)
	}
	if channelID != 0 {
		DSS.DSSAddCHANNEL_ID(b, channelID)
	}
	if topic != 0 {
		DSS.DSSAddTOPIC(b, topic)
	}
	DSS.DSSAddSUBSCRIBED(b, l.Subscribed)
	DSS.DSSAddPIN_POLICY(b, enumOf(DSS.EnumValuesdssPinPolicy, l.PinPolicy))
	DSS.DSSAddRETENTION(b, enumOf(DSS.EnumValuesdssRetention, l.Retention))
	if visibility != 0 {
		DSS.DSSAddVISIBILITY(b, visibility)
	}
	if encryption != 0 {
		DSS.DSSAddENCRYPTION_STATE(b, encryption)
	}
	if grantState != 0 {
		DSS.DSSAddGRANT_STATE(b, grantState)
	}
	if feedHead != 0 {
		DSS.DSSAddFEED_HEAD(b, feedHead)
	}
	if lastPublicationCID != 0 {
		DSS.DSSAddLAST_PUBLICATION_CID(b, lastPublicationCID)
	}
	if lastPNMCID != 0 {
		DSS.DSSAddLAST_PNM_CID(b, lastPNMCID)
	}
	DSS.DSSAddDELTA_ROWS(b, l.DeltaRows)
	DSS.DSSAddLAST_SYNC_STARTED_AT(b, l.LastSyncStartedAt)
	DSS.DSSAddREQUESTED_ACTION(b, enumOf(DSS.EnumValuesdssAction, l.RequestedAction))
	if originID != 0 {
		DSS.DSSAddORIGIN_ID(b, originID)
	}
	root := DSS.DSSEnd(b)
	DSS.FinishSizePrefixedDSSBuffer(b, root)
	return b.FinishedBytes()
}

// DecodeDSS reads a size-prefixed $DSS frame.
func DecodeDSS(frame []byte) (*DSS.DSS, error) {
	if len(frame) < frameIdentifierOffset+frameIdentifierLength || !DSS.SizePrefixedDSSBufferHasIdentifier(frame) {
		return nil, fmt.Errorf("frame is not a $DSS buffer (identifier %q)", FrameIdentifier(frame))
	}
	return DSS.GetSizePrefixedRootAsDSS(frame, 0), nil
}

// EncodeDSSAction builds the request frame a client posts to /api/v1/sync.
func EncodeDSSAction(schema, providerID, sourceName string, action int8) []byte {
	lane := SyncLane{Key: newLaneKey(schema, providerID, sourceName), RequestedAction: action}
	return encodeDSS(&lane)
}

// EncodeDSSSubscribe builds a Subscribe request carrying the lane's retention
// rule (DSSRetentionReplaceCurrent or DSSRetentionArchiveAll).
func EncodeDSSSubscribe(schema, providerID, sourceName string, retention int8) []byte {
	lane := SyncLane{Key: newLaneKey(schema, providerID, sourceName), RequestedAction: DSSActionSubscribe, Retention: retention}
	return encodeDSS(&lane)
}

// --- routes -----------------------------------------------------------------

func (h *SyncHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		h.writeLanes(w, SyncFilter{
			Schema:     q.Get("schema"),
			ProviderID: q.Get("provider_id"),
			SourceName: q.Get("source"),
			OriginID:   q.Get("origin"),
		}, http.StatusOK)
	case http.MethodPost:
		h.deps.adminGate(h.handleAction)(w, r)
	default:
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read sync state or POST to request an action.", 0)
	}
}

func (h *SyncHandler) handleLane(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read one lane's sync state.", 0)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, SyncPath+"/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "A lane is addressed as /api/v1/sync/{schema}/{provider}/{source}.", 0)
		return
	}
	segment := func(raw string) string {
		value, err := url.PathUnescape(raw)
		if err != nil {
			value = raw
		}
		if value == "-" {
			return ""
		}
		return value
	}
	filter := SyncFilter{Schema: segment(parts[0]), ProviderID: segment(parts[1]), SourceName: segment(parts[2]), Exact: true}
	if storeSchemaName(filter.Schema) == "" {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That lane is not held on this node.", 0)
		return
	}
	h.writeLanes(w, filter, http.StatusOK)
}

func (h *SyncHandler) writeLanes(w http.ResponseWriter, filter SyncFilter, status int) {
	lanes, err := h.build(filter)
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "Sync state is not available right now.", 5*time.Second)
		return
	}
	frames := make([][]byte, 0, len(lanes))
	for i := range lanes {
		frames = append(frames, lanes[i].Frame)
	}
	WriteFrameStream(w, status, frames, map[string]string{StreamSchemaHeader: SyncSchemaName})
}

// handleAction runs one $DSS REQUESTED_ACTION and answers with the lane's
// recomputed $DSS.
func (h *SyncHandler) handleAction(w http.ResponseWriter, r *http.Request) {
	frames, err := ReadFrames(r.Body, syncRequestFrameBytes)
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != DSS.DSSIdentifier {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $DSS frame.", 0)
		return
	}
	req, err := DecodeDSS(frames[0])
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $DSS frame.", 0)
		return
	}
	key := newLaneKey(string(req.SCHEMA_NAME()), string(req.PROVIDER_ID()), string(req.SOURCE_NAME()))
	if key.schema == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The $DSS frame must name a schema.", 0)
		return
	}
	action := int8(req.REQUESTED_ACTION())
	if action == DSSActionNone {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The $DSS frame must carry a REQUESTED_ACTION.", 0)
		return
	}
	retentionWord, ok := retentionOrdinalToWord(int8(req.RETENTION()))
	if !ok {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "That retention rule is not one this node understands.", 0)
		return
	}
	if running, name := h.runningAction(key); running {
		WriteErrorFrame(w, http.StatusConflict, "busy", "A "+name+" is already running on that source.", 5*time.Second)
		return
	}
	switch action {
	case DSSActionSubscribe, DSSActionUnsubscribe:
		h.toggleSubscription(key, action == DSSActionSubscribe, retentionWord)
	case DSSActionSync:
		h.startSync(key)
	case DSSActionPin, DSSActionUnpin:
		h.startPin(key, action == DSSActionPin)
	case DSSActionHydrate:
		h.startHydrate(key)
	default:
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "That action is not one this node understands.", 0)
		return
	}
	h.writeLanes(w, SyncFilter{Schema: key.schema, ProviderID: key.providerID, SourceName: key.sourceName, Exact: true}, http.StatusAccepted)
}

func (h *SyncHandler) runningAction(key laneKey) (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if act := h.actions[key]; act != nil && act.running {
		return true, act.action
	}
	return false, ""
}

// failLane records a synchronous refusal on the lane (STATUS=ERROR).
func (h *SyncHandler) failLane(key laneKey, action, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	act := h.actions[key]
	if act == nil {
		act = &laneAction{}
		h.actions[key] = act
	}
	act.running = false
	act.action = action
	act.lastError = message
	act.finishedAt = time.Now()
}

// beginAction marks the lane busy; the returned finish must be called once.
// It reports false when another action already holds the lane.
func (h *SyncHandler) beginAction(key laneKey, action string) (func(rows int, err error), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	act := h.actions[key]
	if act == nil {
		act = &laneAction{}
		h.actions[key] = act
	}
	if act.running {
		return nil, false
	}
	act.running = true
	act.action = action
	act.lastError = ""
	act.startedAt = time.Now()
	act.syncedRows = 0
	return func(rows int, err error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		act.running = false
		act.finishedAt = time.Now()
		act.syncedRows = int64(rows)
		if err != nil {
			act.lastError = plainActionError(action, err)
		}
	}, true
}

// StartLaneAction marks a lane busy under a named action for callers outside
// this file (the archive import). The finish func must be called once.
func (h *SyncHandler) StartLaneAction(schema, providerID, sourceName, action string) (func(rows int, err error), bool) {
	return h.beginAction(newLaneKey(schema, providerID, sourceName), action)
}

// plainActionError turns a failure into the sentence the lane shows.
func plainActionError(action string, err error) string {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "The " + action + " did not complete."
	}
	return "The " + action + " did not complete: " + msg + "."
}

// toggleSubscription flips the lane's channel subscription. On Subscribe the
// retention word becomes the lane's rule (re-subscribing an already
// subscribed lane only changes the rule); Unsubscribe ignores it.
func (h *SyncHandler) toggleSubscription(key laneKey, subscribe bool, retentionWord string) {
	verb := "subscribe to"
	if !subscribe {
		verb = "unsubscribe from"
	}
	if h.deps.Channels == nil || h.deps.Channels.subscriptions == nil {
		h.failLane(key, "subscription change", "This node cannot "+verb+" that source.")
		return
	}
	sourceID := datasetPublicationSourceID(key.providerID, key.sourceName)
	channelID, err := channels.FormatChannelID(channels.ChannelIDInput{SourceID: sourceID, StandardCode: key.code()})
	if err != nil {
		h.failLane(key, "subscription change", "That source has no channel to "+verb+".")
		return
	}
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		h.failLane(key, "subscription change", "That source has no channel to "+verb+".")
		return
	}
	if subscribe {
		h.deps.Channels.subscriptions.SubscribeWithRetention(parsed, retentionWord)
	} else {
		h.deps.Channels.subscriptions.Unsubscribe(parsed)
	}
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	if h.subsPath != "" {
		if err := h.deps.Channels.subscriptions.SaveTo(h.subsPath); err != nil {
			log.Warnf("Sync lane: subscription list %s not saved: %v", h.subsPath, err)
		}
	}
	// A successful toggle clears any earlier refusal on the lane.
	h.mu.Lock()
	if act := h.actions[key]; act != nil && !act.running {
		act.lastError = ""
	}
	h.mu.Unlock()
}

func (h *SyncHandler) startSync(key laneKey) {
	if h.deps.SyncLane == nil {
		h.failLane(key, "sync", "This node cannot sync that source.")
		return
	}
	finish, ok := h.beginAction(key, "sync")
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncActionTimeout)
		defer cancel()
		n, err := h.deps.SyncLane(ctx, key.schema, key.providerID, key.sourceName)
		finish(n, err)
	}()
}

func (h *SyncHandler) startPin(key laneKey, pin bool) {
	verb := "pin"
	if !pin {
		verb = "unpin"
	}
	if (pin && h.deps.PinCID == nil) || (!pin && h.deps.UnpinCID == nil) {
		h.failLane(key, verb, "This node cannot "+verb+" that source.")
		return
	}
	pub, ok := h.newestPublication(key)
	if !ok {
		h.failLane(key, verb, "That source has no publication to "+verb+".")
		return
	}
	finish, ok := h.beginAction(key, verb)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncActionTimeout)
		defer cancel()
		finish(h.pinPublication(ctx, key, pub, pin))
	}()
}

// newestPublication finds the lane's newest publication (max FeedSequence).
func (h *SyncHandler) newestPublication(key laneKey) (storage.DatasetShardPublication, bool) {
	pubs, err := h.deps.Store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, key.schema)
	if err != nil {
		return storage.DatasetShardPublication{}, false
	}
	var newest *storage.DatasetShardPublication
	for i := range pubs {
		pub := &pubs[i]
		if strings.TrimSpace(pub.ProviderID) != key.providerID || strings.TrimSpace(pub.SourceName) != key.sourceName {
			continue
		}
		if newest == nil || pub.FeedSequence > newest.FeedSequence ||
			(pub.FeedSequence == newest.FeedSequence && pub.PublishedAt.After(newest.PublishedAt)) {
			newest = pub
		}
	}
	if newest == nil {
		return storage.DatasetShardPublication{}, false
	}
	return *newest, true
}

// pinPublication pins (or unpins) every CID of a publication and records the
// outcome in the pin ledger, mirroring the publication lane's own rows.
func (h *SyncHandler) pinPublication(ctx context.Context, key laneKey, pub storage.DatasetShardPublication, pin bool) (int, error) {
	type target struct {
		cid, role, hash string
		rows, bytes     int64
	}
	targets := []target{
		{cid: pub.ShardCID, role: "shard", hash: pub.ShardSHA256, rows: int64(pub.RecordCount), bytes: pub.ByteCount},
		{cid: pub.IndexCID, role: "index", hash: pub.IndexSHA256},
		{cid: pub.ManifestCID, role: "manifest"},
		{cid: pub.PNMCID, role: "pnm"},
	}
	state := "verified"
	if !pin {
		state = "unpinned"
	}
	snapshotID := pub.FeedHead
	if snapshotID == "" {
		snapshotID = pub.ManifestCID
	}
	now := time.Now().UTC()
	done := 0
	var errs []error
	for _, t := range targets {
		cid := strings.TrimSpace(t.cid)
		if cid == "" {
			continue
		}
		var err error
		if pin {
			err = h.deps.PinCID(ctx, cid)
		} else {
			err = h.deps.UnpinCID(ctx, cid)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", t.role, cid, err))
			continue
		}
		entry := storage.PinLedgerEntry{
			CID:               cid,
			SchemaName:        key.schema,
			ProviderPeerID:    h.deps.NodePeerID,
			ProviderID:        pub.ProviderID,
			SourceName:        pub.SourceName,
			BatchID:           pub.BatchID,
			QueryProfile:      pub.QueryProfile,
			SnapshotID:        snapshotID,
			Head:              pub.FeedHead,
			ByteHash:          t.hash,
			Role:              t.role,
			RowCount:          t.rows,
			ByteCount:         t.bytes,
			TTL:               0,
			VerificationState: state,
			VerifiedAt:        now,
			UpdatedAt:         now,
		}
		if err := h.deps.Store.UpsertPinLedgerEntry(entry); err != nil {
			errs = append(errs, fmt.Errorf("ledger %s %s: %w", t.role, cid, err))
			continue
		}
		done++
	}
	if pin {
		return pub.RecordCount, errors.Join(errs...)
	}
	return 0, errors.Join(errs...)
}

func (h *SyncHandler) startHydrate(key laneKey) {
	finish, ok := h.beginAction(key, "hydrate")
	if !ok {
		return
	}
	go func() {
		store := h.deps.Store
		replayed, err := store.ReplayRecordCatalog(false, nil)
		if err != nil {
			finish(replayed, err)
			return
		}
		if err := store.RebuildSourceSummaries(); err != nil {
			finish(replayed, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), syncActionTimeout)
		defer cancel()
		n, err := store.HydrateEngineHotWindowFromRecordCatalogContext(ctx)
		if err != nil {
			finish(n, err)
			return
		}
		finish(n, nil)
	}()
}
