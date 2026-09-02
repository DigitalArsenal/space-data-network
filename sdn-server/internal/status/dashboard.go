package status

import (
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

// DashboardSchemaRow is one schema's record footprint.
type DashboardSchemaRow struct {
	Schema      string
	RecordCount int64
	TotalBytes  int64
}

// DashboardSourceRow is one (schema, provider, source, batch) ingest lane.
type DashboardSourceRow struct {
	Schema             string
	ProviderID         string
	SourceName         string
	BatchID            string
	RecordCount        int64
	TotalBytes         int64
	FirstIngestAt      int64
	LastIngestAt       int64
	UpdatedAt          int64
	WindowRecords      int64
	PriorWindowRecords int64
	WindowMS           int64

	// IngestTimestamps is an optional exact list of newly observed record
	// arrival times (Unix seconds). The production snapshot lane can omit it:
	// DashboardMonitor then records the non-negative count delta at
	// LastIngestAt. Tests and richer callers use it for exact window counts.
	// It is monitor input only and is not serialized directly.
	IngestTimestamps []int64
}

// DashboardIngestEventKind is one source-ingest transition.
type DashboardIngestEventKind string

const (
	DashboardIngestEventStall   DashboardIngestEventKind = "Stall"
	DashboardIngestEventReject  DashboardIngestEventKind = "Reject"
	DashboardIngestEventRecover DashboardIngestEventKind = "Recover"
)

// DashboardIngestEventRow is one event carried by the dashboard snapshot.
type DashboardIngestEventRow struct {
	Kind       DashboardIngestEventKind
	Schema     string
	ProviderID string
	SourceName string
	Message    string
	Count      int64
	At         int64
}

// DashboardTopicRow is one pubsub topic's live traffic summary.
type DashboardTopicRow struct {
	Topic      string
	RatePerMin float64
	LastSeenAt int64
	Subscribed bool
	// MessageTimestamps optionally carries exact observations for the current
	// minute. When present, DashboardMonitor derives RatePerMin/LastSeenAt.
	MessageTimestamps []int64
}

// DashboardStatsInput is the assembled data BuildDashboardStatsSet serializes.
// The caller reads the store (on a background lane, never a request) and fills
// this in; this package never touches storage itself.
type DashboardStatsInput struct {
	Schemas []DashboardSchemaRow
	Sources []DashboardSourceRow
	Events  []DashboardIngestEventRow
	Topics  []DashboardTopicRow

	TotalRecords int64
	TotalBytes   int64

	// Stale is true when the assembling read hit its budget and these numbers
	// are last-known-good. Reported as stale, never as a confident zero.
	Stale bool
	// AsOf is when the numbers were last true; zero = never read.
	AsOf time.Time

	// Now is the generation time; zero means time.Now().
	Now time.Time
}

// BuildDashboardStatsSet composes the input into a size-prefixed
// DashboardStatsSet FlatBuffer carrying the $NDS file identifier — framed
// exactly like BuildNodeStatusSet's $NST frames, so one binary socket can
// carry both and the client tells them apart by the identifier bytes.
func BuildDashboardStatsSet(in DashboardStatsInput) []byte {
	return defaultDashboardMonitor.Build(in)
}

// buildDashboardStatsSet serializes already-derived rows. DashboardMonitor is
// the stateful observation layer; keeping serialization separate makes its
// transitions deterministic and independently testable.
func buildDashboardStatsSet(in DashboardStatsInput) []byte {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	asOf := int64(0)
	if !in.AsOf.IsZero() {
		asOf = in.AsOf.Unix()
	}

	b := flatbuffers.NewBuilder(1024)

	schemaOffsets := make([]flatbuffers.UOffsetT, len(in.Schemas))
	for i := range in.Schemas {
		row := &in.Schemas[i]
		name := b.CreateString(row.Schema)
		nst.DashboardSchemaStatStart(b)
		nst.DashboardSchemaStatAddSchema(b, name)
		nst.DashboardSchemaStatAddRecordCount(b, row.RecordCount)
		nst.DashboardSchemaStatAddTotalBytes(b, row.TotalBytes)
		schemaOffsets[i] = nst.DashboardSchemaStatEnd(b)
	}
	nst.DashboardStatsSetStartSchemasVector(b, len(schemaOffsets))
	for i := len(schemaOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(schemaOffsets[i])
	}
	schemasVec := b.EndVector(len(schemaOffsets))

	sourceOffsets := make([]flatbuffers.UOffsetT, len(in.Sources))
	for i := range in.Sources {
		row := &in.Sources[i]
		schema := b.CreateString(row.Schema)
		provider := b.CreateString(row.ProviderID)
		source := b.CreateString(row.SourceName)
		batch := b.CreateString(row.BatchID)
		nst.DashboardSourceStatStart(b)
		nst.DashboardSourceStatAddSchema(b, schema)
		nst.DashboardSourceStatAddProviderId(b, provider)
		nst.DashboardSourceStatAddSourceName(b, source)
		nst.DashboardSourceStatAddBatchId(b, batch)
		nst.DashboardSourceStatAddRecordCount(b, row.RecordCount)
		nst.DashboardSourceStatAddTotalBytes(b, row.TotalBytes)
		nst.DashboardSourceStatAddFirstIngestAt(b, row.FirstIngestAt)
		nst.DashboardSourceStatAddLastIngestAt(b, row.LastIngestAt)
		nst.DashboardSourceStatAddUpdatedAt(b, row.UpdatedAt)
		nst.DashboardSourceStatAddWindowRecords(b, row.WindowRecords)
		nst.DashboardSourceStatAddPriorWindowRecords(b, row.PriorWindowRecords)
		nst.DashboardSourceStatAddWindowMs(b, row.WindowMS)
		sourceOffsets[i] = nst.DashboardSourceStatEnd(b)
	}
	nst.DashboardStatsSetStartSourcesVector(b, len(sourceOffsets))
	for i := len(sourceOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(sourceOffsets[i])
	}
	sourcesVec := b.EndVector(len(sourceOffsets))

	eventOffsets := make([]flatbuffers.UOffsetT, len(in.Events))
	for i := range in.Events {
		row := &in.Events[i]
		schema := b.CreateString(row.Schema)
		provider := b.CreateString(row.ProviderID)
		source := b.CreateString(row.SourceName)
		message := b.CreateString(row.Message)
		nst.DashboardIngestEventStart(b)
		nst.DashboardIngestEventAddKind(b, dashboardEventKind(row.Kind))
		nst.DashboardIngestEventAddSchema(b, schema)
		nst.DashboardIngestEventAddProviderId(b, provider)
		nst.DashboardIngestEventAddSourceName(b, source)
		nst.DashboardIngestEventAddMessage(b, message)
		nst.DashboardIngestEventAddCount(b, row.Count)
		nst.DashboardIngestEventAddAt(b, row.At)
		eventOffsets[i] = nst.DashboardIngestEventEnd(b)
	}
	nst.DashboardStatsSetStartEventsVector(b, len(eventOffsets))
	for i := len(eventOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(eventOffsets[i])
	}
	eventsVec := b.EndVector(len(eventOffsets))

	topicOffsets := make([]flatbuffers.UOffsetT, len(in.Topics))
	for i := range in.Topics {
		row := &in.Topics[i]
		topic := b.CreateString(row.Topic)
		nst.DashboardTopicStatStart(b)
		nst.DashboardTopicStatAddTopic(b, topic)
		nst.DashboardTopicStatAddRatePerMin(b, row.RatePerMin)
		nst.DashboardTopicStatAddLastSeenAt(b, row.LastSeenAt)
		nst.DashboardTopicStatAddSubscribed(b, row.Subscribed)
		topicOffsets[i] = nst.DashboardTopicStatEnd(b)
	}
	nst.DashboardStatsSetStartTopicsVector(b, len(topicOffsets))
	for i := len(topicOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(topicOffsets[i])
	}
	topicsVec := b.EndVector(len(topicOffsets))

	nst.DashboardStatsSetStart(b)
	nst.DashboardStatsSetAddGeneratedAt(b, now.Unix())
	nst.DashboardStatsSetAddSchemas(b, schemasVec)
	nst.DashboardStatsSetAddSources(b, sourcesVec)
	nst.DashboardStatsSetAddTotalRecords(b, in.TotalRecords)
	nst.DashboardStatsSetAddTotalBytes(b, in.TotalBytes)
	nst.DashboardStatsSetAddStale(b, in.Stale)
	nst.DashboardStatsSetAddAsOf(b, asOf)
	nst.DashboardStatsSetAddEvents(b, eventsVec)
	nst.DashboardStatsSetAddTopics(b, topicsVec)
	set := nst.DashboardStatsSetEnd(b)

	nst.FinishSizePrefixedDashboardStatsSetBuffer(b, set)
	return b.FinishedBytes()
}

func dashboardEventKind(kind DashboardIngestEventKind) nst.DashboardIngestEventKind {
	switch kind {
	case DashboardIngestEventReject:
		return nst.DashboardIngestEventKindReject
	case DashboardIngestEventRecover:
		return nst.DashboardIngestEventKindRecover
	default:
		return nst.DashboardIngestEventKindStall
	}
}
