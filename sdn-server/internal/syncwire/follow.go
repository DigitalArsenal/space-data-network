package syncwire

// A followed read-chunk (QRP.FOLLOW) keeps its stream open. The subscriber's
// opening request is answered with one page; each further $QRP{KIND=Request}
// frame the subscriber writes on the stream grants one more page. A followed
// page is zero or more runs, each [$QRP Page header with RECORD_COUNT > 0]
// [RECORD_COUNT record frames], ended by one $QRP Page header with
// RECORD_COUNT 0 that carries the page's RESUME_CURSOR: the position after
// the page's last record, a token the producer's scan generation verifies.
// Once everything stored has been sent, the producer answers a grant when new
// records of the lane are stored, or with an empty page (the end header
// alone) after a quiet interval.
//
// The end header is also the producer's statement of the lane (a
// FollowStatement): BATCH_ID names the lane's current batch at the producer
// (the newest batch it holds) and TOTAL_COUNT its records. The subscriber
// holds a batch as current only once it holds every record of the producer's
// list of that batch: it reads the list (an unfollowed read-chunk of the
// batch with the PROJECTION filter, pages of CIDs) and fetches the records it
// lacks by CID (an unfollowed read-chunk of the batch with a CID filter,
// answered as one followed page without a statement).
//
// A page holds at most the records the subscriber asked for (LIMIT, at most
// FollowMaxRecords), FollowMaxPageBytes of record frames, except that its
// first record may alone be larger (up to MaxFrameBytes), and
// FollowMaxHeaderBytes of header frames. The subscriber checks each, and
// each header's RECORD_COUNT, before it allocates or reads, and refuses a
// page past them.

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	// FollowMaxRecords is the most records a followed page holds.
	FollowMaxRecords = 4096
	// FollowMaxPageBytes is the record frame bytes a followed page holds
	// at most, past its first record.
	FollowMaxPageBytes = 8 << 20
	// FollowMaxHeaderBytes is the header frame bytes a followed page holds
	// at most (a run of one record per header, 4,096 runs, at 1 KiB each).
	FollowMaxHeaderBytes = 4 << 20
	// followMaxHeaderFrame bounds one header frame of a followed page.
	followMaxHeaderFrame = 64 << 10
	// FollowCIDPageLimit is the most CIDs one page of a batch's list holds.
	FollowCIDPageLimit = 8192
	// followMaxCIDPageBytes bounds one frame of a batch's list.
	followMaxCIDPageBytes = 4 << 20
	// FollowFetchMaxCIDs is the most CIDs one fetch by CID names.
	FollowFetchMaxCIDs = 512
)

// Read-chunk filters of a followed lane's side reads (an unfollowed request
// naming BATCH_ID).
const (
	// FilterProjection (Eq ProjectionCID) asks for the batch's list of
	// CIDs, FollowCIDPageLimit per page, continued by NEXT_CURSOR.
	FilterProjection = "PROJECTION"
	// ProjectionCID is the one projection: the records' CIDs.
	ProjectionCID = "CID"
	// FilterCID (In: comma-separated CIDs, at most FollowFetchMaxCIDs) asks
	// for those records of the batch, as one followed page; a CID the batch
	// does not hold is left out.
	FilterCID = "CID"
)

// ErrFollowPageLimit is a followed page past its limits.
var ErrFollowPageLimit = errors.New("followed page past its limits")

// FollowStatement is the producer's statement of a lane on a followed
// page's end header: its current batch and that batch's records.
type FollowStatement struct {
	BatchID string
	Records int64
}

// StatementOf reads the statement an end header carries ("" batch: none).
func StatementOf(end *QRP.QRP) FollowStatement {
	if end == nil {
		return FollowStatement{}
	}
	return FollowStatement{BatchID: strings.TrimSpace(string(end.BATCH_ID())), Records: int64(min(end.TOTAL_COUNT(), uint64(1<<62)))}
}

// WriteFollowPage writes one followed page: its runs, then the end header
// carrying stmt.
func WriteFollowPage(w io.Writer, store *storage.FlatSQLStore, page *datasync.Page, records []*storage.Record, stmt FollowStatement) error {
	return writeScanPage(w, store, page, records, &stmt)
}

// FollowPageCut is how many of records (in order) one followed page sends:
// at most limit records and FollowMaxPageBytes of record frames past the
// first record.
func FollowPageCut(records []*storage.Record, limit int) int {
	limit = min(max(limit, 1), FollowMaxRecords)
	var total int64
	for i, r := range records {
		if i >= limit {
			return i
		}
		total += int64(FramePrefixLength + len(r.Data))
		if i > 0 && total > FollowMaxPageBytes {
			return i
		}
	}
	return len(records)
}

// FollowRun is one run of a followed page: its header and its record frames
// (size-prefixed, as stored).
type FollowRun struct {
	Header  *QRP.QRP
	Records [][]byte
}

// ReadFollowPage reads one followed page of at most limit records: its runs
// and its end header. An Error frame is returned as a *RemoteError. A header
// whose RECORD_COUNT exceeds what the page has left, or a page past
// FollowMaxPageBytes or FollowMaxHeaderBytes, is refused with
// ErrFollowPageLimit before more is allocated or read.
func ReadFollowPage(r io.Reader, limit int) (runs []FollowRun, end *QRP.QRP, bytes int64, err error) {
	limit = min(max(limit, 1), FollowMaxRecords)
	records := 0
	var recordBytes, headerBytes int64
	for {
		left := min(followMaxHeaderFrame, int(FollowMaxHeaderBytes-headerBytes)-FramePrefixLength)
		if left < 1 {
			return nil, nil, bytes, fmt.Errorf("%w: more than %d header bytes", ErrFollowPageLimit, FollowMaxHeaderBytes)
		}
		header, frame, err := readPageMax(r, left)
		bytes += int64(len(frame))
		headerBytes += int64(len(frame))
		if err != nil {
			return nil, nil, bytes, err
		}
		count := header.RECORD_COUNT()
		if count == 0 {
			return runs, header, bytes, nil
		}
		if uint64(count) > uint64(limit-records) {
			return nil, nil, bytes, fmt.Errorf("%w: a run of %d records after %d of at most %d", ErrFollowPageLimit, count, records, limit)
		}
		run := FollowRun{Header: header, Records: make([][]byte, 0, count)}
		for i := 0; i < int(count); i++ {
			left := MaxFrameBytes
			if records > 0 {
				left = int(min(int64(MaxFrameBytes), FollowMaxPageBytes-recordBytes-FramePrefixLength))
				if left < 1 {
					return nil, nil, bytes, fmt.Errorf("%w: more than %d record bytes", ErrFollowPageLimit, FollowMaxPageBytes)
				}
			}
			record, err := ReadFrame(r, left)
			if err != nil {
				return nil, nil, bytes, fmt.Errorf("read record %d of %d: %w", i+1, count, err)
			}
			bytes += int64(len(record))
			recordBytes += int64(len(record))
			records++
			run.Records = append(run.Records, record)
		}
		runs = append(runs, run)
	}
}

// Grant writes one credit: a request frame naming the schema.
func Grant(w io.Writer, schema string) error {
	return Request(w, QRPFields{SchemaName: schema})
}

// ErrFollowClosed is a follow stream the subscriber closed.
var ErrFollowClosed = errors.New("follow stream closed by the subscriber")

// cidColumn is the one column of a batch's list page.
var cidColumn = []QRPColumnField{{Name: ProjectionCID, Type: "TEXT"}}

// CIDPage is one page of a batch's list of CIDs: f carries the lane, the
// batch, NEXT_CURSOR and TOTAL_COUNT.
func CIDPage(f QRPFields, cids []string) []byte {
	f.Kind, f.Status = QRPKindPage, QRPStatusOk
	f.Columns = cidColumn
	f.Rows = make([][]QRPCell, len(cids))
	for i, c := range cids {
		f.Rows[i] = []QRPCell{TextCell(c)}
	}
	f.RecordCount = 0
	return BuildQRP(f)
}

// ReadCIDPage reads one page of a batch's list: at most FollowCIDPageLimit
// CIDs in one frame of at most followMaxCIDPageBytes.
func ReadCIDPage(r io.Reader) (cids []string, next string, err error) {
	q, _, err := readPageMax(r, followMaxCIDPageBytes)
	if err != nil {
		return nil, "", err
	}
	if n := q.ROWSLength(); n > FollowCIDPageLimit {
		return nil, "", fmt.Errorf("%w: a list page of %d CIDs, at most %d", ErrFollowPageLimit, n, FollowCIDPageLimit)
	}
	columns, rows := Rows(q)
	col, ok := columns[ProjectionCID]
	if !ok && len(rows) > 0 {
		return nil, "", errors.New("a list page without a CID column")
	}
	cids = make([]string, 0, len(rows))
	for _, row := range rows {
		if col >= len(row) || !row[col].IsText || row[col].Text == "" {
			return nil, "", errors.New("a list page row without a CID")
		}
		cids = append(cids, row[col].Text)
	}
	return cids, string(q.NEXT_CURSOR()), nil
}
