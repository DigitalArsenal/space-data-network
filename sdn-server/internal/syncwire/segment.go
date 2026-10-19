package syncwire

import (
	"fmt"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Segment is one SEGMENT_COLUMNS row: a dataset publication (list-published,
// read-shard, feed heads), a manifest segment or bundle (open-manifest), or a
// shard of a batch read. Strings and bytes left empty travel as Null cells;
// integers always travel as Int cells.
type Segment struct {
	SchemaName       string
	ProviderID       string
	SourceName       string
	BatchID          string
	QueryProfile     string
	Role             string
	SegmentIndex     int64
	Offset           int64
	Limit            int64
	Cursor           string
	NextCursor       string
	RecordCount      int64
	ByteCount        int64
	ByteOffset       int64
	ByteLength       int64
	ChunkHash        string
	ShardCID         string
	IndexCID         string
	ManifestCID      string
	PNMCID           string
	ShardSHA256      string
	IndexSHA256      string
	QuerySHA256      string
	ResultSHA256     string
	FeedSequence     int64
	PreviousHead     string
	FeedHead         string
	PublishedAtMs    int64
	Signature        []byte
	SignatureType    string
	SigningPublicKey []byte
}

// Segment ROLE values.
const (
	RoleShard         = "shard"
	RoleIndex         = "index"
	RoleSegment       = "segment"
	RoleShardGroupCAR = "shard-group-car"
)

const (
	colString = "string"
	colInt    = "int64"
	colBytes  = "bytes"
)

// segmentColumn binds one column to its Segment field.
type segmentColumn struct {
	name, typ string
	str       func(*Segment) *string
	num       func(*Segment) *int64
	raw       func(*Segment) *[]byte
}

func strCol(name string, f func(*Segment) *string) segmentColumn {
	return segmentColumn{name: name, typ: colString, str: f}
}
func intCol(name string, f func(*Segment) *int64) segmentColumn {
	return segmentColumn{name: name, typ: colInt, num: f}
}
func bytesCol(name string, f func(*Segment) *[]byte) segmentColumn {
	return segmentColumn{name: name, typ: colBytes, raw: f}
}

// segmentColumns is THE column set, in wire order.
var segmentColumns = []segmentColumn{
	strCol("SCHEMA_NAME", func(s *Segment) *string { return &s.SchemaName }),
	strCol("PROVIDER_ID", func(s *Segment) *string { return &s.ProviderID }),
	strCol("SOURCE_NAME", func(s *Segment) *string { return &s.SourceName }),
	strCol("BATCH_ID", func(s *Segment) *string { return &s.BatchID }),
	strCol("QUERY_PROFILE", func(s *Segment) *string { return &s.QueryProfile }),
	strCol("ROLE", func(s *Segment) *string { return &s.Role }),
	intCol("SEGMENT_INDEX", func(s *Segment) *int64 { return &s.SegmentIndex }),
	intCol("OFFSET", func(s *Segment) *int64 { return &s.Offset }),
	intCol("LIMIT", func(s *Segment) *int64 { return &s.Limit }),
	strCol("CURSOR", func(s *Segment) *string { return &s.Cursor }),
	strCol("NEXT_CURSOR", func(s *Segment) *string { return &s.NextCursor }),
	intCol("RECORD_COUNT", func(s *Segment) *int64 { return &s.RecordCount }),
	intCol("BYTE_COUNT", func(s *Segment) *int64 { return &s.ByteCount }),
	intCol("BYTE_OFFSET", func(s *Segment) *int64 { return &s.ByteOffset }),
	intCol("BYTE_LENGTH", func(s *Segment) *int64 { return &s.ByteLength }),
	strCol("CHUNK_HASH", func(s *Segment) *string { return &s.ChunkHash }),
	strCol("SHARD_CID", func(s *Segment) *string { return &s.ShardCID }),
	strCol("INDEX_CID", func(s *Segment) *string { return &s.IndexCID }),
	strCol("MANIFEST_CID", func(s *Segment) *string { return &s.ManifestCID }),
	strCol("PNM_CID", func(s *Segment) *string { return &s.PNMCID }),
	strCol("SHARD_SHA256", func(s *Segment) *string { return &s.ShardSHA256 }),
	strCol("INDEX_SHA256", func(s *Segment) *string { return &s.IndexSHA256 }),
	strCol("QUERY_SHA256", func(s *Segment) *string { return &s.QuerySHA256 }),
	strCol("RESULT_SHA256", func(s *Segment) *string { return &s.ResultSHA256 }),
	intCol("FEED_SEQUENCE", func(s *Segment) *int64 { return &s.FeedSequence }),
	strCol("PREVIOUS_HEAD", func(s *Segment) *string { return &s.PreviousHead }),
	strCol("FEED_HEAD", func(s *Segment) *string { return &s.FeedHead }),
	intCol("PUBLISHED_AT_MS", func(s *Segment) *int64 { return &s.PublishedAtMs }),
	bytesCol("SIGNATURE", func(s *Segment) *[]byte { return &s.Signature }),
	strCol("SIGNATURE_TYPE", func(s *Segment) *string { return &s.SignatureType }),
	bytesCol("SIGNING_PUBLIC_KEY", func(s *Segment) *[]byte { return &s.SigningPublicKey }),
}

// SegmentColumns is the COLUMNS descriptor of every segment page.
var SegmentColumns = func() []QRPColumnField {
	out := make([]QRPColumnField, len(segmentColumns))
	for i, c := range segmentColumns {
		out[i] = QRPColumnField{Name: c.name, Type: c.typ}
	}
	return out
}()

// Cells is the segment's row, in SegmentColumns order.
func (s Segment) Cells() []QRPCell {
	cells := make([]QRPCell, len(segmentColumns))
	for i, c := range segmentColumns {
		switch {
		case c.str != nil:
			cells[i] = TextCell(*c.str(&s))
		case c.num != nil:
			cells[i] = IntCell(*c.num(&s))
		default:
			cells[i] = BytesCell(*c.raw(&s))
		}
	}
	return cells
}

// SegmentRows is the ROWS of segs.
func SegmentRows(segs []Segment) [][]QRPCell {
	rows := make([][]QRPCell, len(segs))
	for i, s := range segs {
		rows[i] = s.Cells()
	}
	return rows
}

// Segments decodes the SEGMENT rows of a page by column NAME. A missing
// column reads as its zero value; a cell of the wrong kind is an error.
func Segments(q *QRP.QRP) ([]Segment, error) {
	columns, rows := Rows(q)
	out := make([]Segment, len(rows))
	for r, row := range rows {
		for _, c := range segmentColumns {
			i, ok := columns[c.name]
			if !ok || i >= len(row) {
				continue
			}
			cell := row[i]
			if !cell.IsText && !cell.IsInt && !cell.IsBytes {
				continue // Null
			}
			switch {
			case c.str != nil && cell.IsText:
				*c.str(&out[r]) = cell.Text
			case c.num != nil && cell.IsInt:
				*c.num(&out[r]) = cell.Int
			case c.raw != nil && cell.IsBytes:
				*c.raw(&out[r]) = cell.Bytes
			default:
				return nil, fmt.Errorf("segment row %d: column %s is not of type %s", r, c.name, c.typ)
			}
		}
	}
	return out, nil
}

// SegmentFromPublication is the row of one dataset publication.
func SegmentFromPublication(pub storage.DatasetShardPublication) Segment {
	s := Segment{
		SchemaName:   pub.SchemaName,
		ProviderID:   pub.ProviderID,
		SourceName:   pub.SourceName,
		BatchID:      pub.BatchID,
		QueryProfile: pub.QueryProfile,
		Offset:       int64(pub.Offset),
		Limit:        int64(pub.Limit),
		RecordCount:  int64(pub.RecordCount),
		ByteCount:    pub.ByteCount,
		ShardCID:     pub.ShardCID,
		IndexCID:     pub.IndexCID,
		ManifestCID:  pub.ManifestCID,
		PNMCID:       pub.PNMCID,
		ShardSHA256:  pub.ShardSHA256,
		IndexSHA256:  pub.IndexSHA256,
		QuerySHA256:  pub.QuerySHA256,
		ResultSHA256: pub.ResultSHA256,
		FeedSequence: pub.FeedSequence,
		PreviousHead: pub.PreviousHead,
		FeedHead:     pub.FeedHead,
	}
	if !pub.PublishedAt.IsZero() {
		s.PublishedAtMs = pub.PublishedAt.UnixMilli()
	}
	return s
}

// Publication is the dataset publication a row names.
func (s Segment) Publication() storage.DatasetShardPublication {
	pub := storage.DatasetShardPublication{
		SchemaName:   s.SchemaName,
		ProviderID:   s.ProviderID,
		SourceName:   s.SourceName,
		BatchID:      s.BatchID,
		QueryProfile: s.QueryProfile,
		Offset:       int(s.Offset),
		Limit:        int(s.Limit),
		RecordCount:  int(s.RecordCount),
		ByteCount:    s.ByteCount,
		ShardCID:     s.ShardCID,
		IndexCID:     s.IndexCID,
		ManifestCID:  s.ManifestCID,
		PNMCID:       s.PNMCID,
		ShardSHA256:  s.ShardSHA256,
		IndexSHA256:  s.IndexSHA256,
		QuerySHA256:  s.QuerySHA256,
		ResultSHA256: s.ResultSHA256,
		FeedSequence: s.FeedSequence,
		PreviousHead: s.PreviousHead,
		FeedHead:     s.FeedHead,
	}
	if s.PublishedAtMs > 0 {
		pub.PublishedAt = time.UnixMilli(s.PublishedAtMs).UTC()
	}
	return pub
}

// SegmentPage builds one Page frame of segment rows.
func SegmentPage(f QRPFields, segs []Segment) []byte {
	f.Kind, f.Status = QRPKindPage, QRPStatusOk
	f.Columns, f.Rows = SegmentColumns, SegmentRows(segs)
	return BuildQRP(f)
}
