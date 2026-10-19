package syncwire

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// scanWriteBuffer batches small record frames into large socket writes.
const scanWriteBuffer = 256 << 10

// ScanQuery decodes a $QRP{KIND=Request} into the record-scan query that
// POST /api/v1/data/scan and the read-chunk op both run. A continuation
// cursor carries its snapshot, so the next page does not count again.
func ScanQuery(q *QRP.QRP) (datasync.QueryRequest, error) {
	req := datasync.QueryRequest{
		Schema:         NormalizeSchema(string(q.SCHEMA_NAME())),
		ProviderID:     string(q.PROVIDER_ID()),
		SourceName:     string(q.SOURCE_NAME()),
		BatchID:        string(q.BATCH_ID()),
		ProducerPeerID: string(q.PRODUCER_PEER_ID()),
		PeerID:         string(q.ORIGIN_ID()),
		Search:         strings.TrimSpace(string(q.SEARCH())),
		Cursor:         string(q.CURSOR()),
		ResumeCursor:   string(q.RESUME_CURSOR()),
		Limit:          int(q.LIMIT()),
		IncludeData:    true,
	}
	if req.Schema == "" {
		return datasync.QueryRequest{}, errors.New("SCHEMA_NAME is required")
	}
	if snapshotID, total, ok := datasync.CursorSnapshot(req.Cursor); ok {
		req.SnapshotID, req.Head, req.TotalCount = snapshotID, snapshotID, total
	}
	return req, nil
}

// NormalizeSchema is the store-form schema name ("OMM" -> "OMM.fbs").
func NormalizeSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" || strings.Contains(schema, ".") {
		return schema
	}
	return schema + ".fbs"
}

type scanLane struct {
	providerID, sourceName, batchID, producerPeerID, originID string
}

func laneOf(rec *storage.Record) scanLane {
	return scanLane{rec.SourceTags.ProviderID, rec.SourceTags.SourceName, rec.SourceTags.BatchID, rec.SourceTags.ProducerPeerID, rec.PeerID}
}

// WriteScanPage writes one scan page as runs of [$QRP{KIND=Page} header]
// [RECORD_COUNT verbatim record frames]. A run groups consecutive records of
// one lane; its header names the lane (PROVIDER_ID, SOURCE_NAME, BATCH_ID,
// PRODUCER_PEER_ID, ORIGIN_ID = storing peer) and repeats the page fields
// (CURSOR, NEXT_CURSOR, RESUME_CURSOR on the last page, TOTAL_COUNT, LIMIT,
// ETAG = page chunk hash). An empty page is one header with RECORD_COUNT 0.
func WriteScanPage(w io.Writer, store *storage.FlatSQLStore, page *datasync.Page, records []*storage.Record) error {
	fileIdentifier := ""
	if len(records) > 0 && len(records[0].Data) >= FrameIdentifierOffset {
		fileIdentifier = string(records[0].Data[FramePrefixLength:FrameIdentifierOffset])
	}
	header := func(lane scanLane, count int) []byte {
		return BuildQRP(QRPFields{
			Kind:           QRPKindPage,
			Status:         QRPStatusOk,
			SchemaName:     page.Schema,
			FileIdentifier: fileIdentifier,
			ProviderID:     lane.providerID,
			SourceName:     lane.sourceName,
			BatchID:        lane.batchID,
			ProducerPeerID: lane.producerPeerID,
			OriginID:       lane.originID,
			Limit:          uint32(page.Limit),
			Cursor:         page.Cursor,
			NextCursor:     page.NextCursor,
			ResumeCursor:   page.ResumeCursor,
			RecordCount:    uint32(count),
			TotalCount:     uint64(page.TotalCount),
			Etag:           page.ChunkHash,
			GeneratedAtMs:  time.Now().UnixMilli(),
		})
	}

	out := bufio.NewWriterSize(w, scanWriteBuffer)
	if len(records) == 0 {
		if _, err := out.Write(header(scanLane{}, 0)); err != nil {
			return err
		}
		return out.Flush()
	}
	for start := 0; start < len(records); {
		lane := laneOf(records[start])
		end := start + 1
		for end < len(records) && laneOf(records[end]) == lane {
			end++
		}
		if _, err := out.Write(header(lane, end-start)); err != nil {
			return err
		}
		if err := store.WriteRawRecordFrames(out, records[start:end]); err != nil {
			return err
		}
		start = end
	}
	return out.Flush()
}
