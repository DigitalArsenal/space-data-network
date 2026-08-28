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
	Schema        string
	ProviderID    string
	SourceName    string
	BatchID       string
	RecordCount   int64
	TotalBytes    int64
	FirstIngestAt int64
	LastIngestAt  int64
	UpdatedAt     int64
}

// DashboardStatsInput is the assembled data BuildDashboardStatsSet serializes.
// The caller reads the store (on a background lane, never a request) and fills
// this in; this package never touches storage itself.
type DashboardStatsInput struct {
	Schemas []DashboardSchemaRow
	Sources []DashboardSourceRow

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
		sourceOffsets[i] = nst.DashboardSourceStatEnd(b)
	}
	nst.DashboardStatsSetStartSourcesVector(b, len(sourceOffsets))
	for i := len(sourceOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(sourceOffsets[i])
	}
	sourcesVec := b.EndVector(len(sourceOffsets))

	nst.DashboardStatsSetStart(b)
	nst.DashboardStatsSetAddGeneratedAt(b, now.Unix())
	nst.DashboardStatsSetAddSchemas(b, schemasVec)
	nst.DashboardStatsSetAddSources(b, sourcesVec)
	nst.DashboardStatsSetAddTotalRecords(b, in.TotalRecords)
	nst.DashboardStatsSetAddTotalBytes(b, in.TotalBytes)
	nst.DashboardStatsSetAddStale(b, in.Stale)
	nst.DashboardStatsSetAddAsOf(b, asOf)
	set := nst.DashboardStatsSetEnd(b)

	nst.FinishSizePrefixedDashboardStatsSetBuffer(b, set)
	return b.FinishedBytes()
}
