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
// A page holds at most the records the subscriber asked for (LIMIT, at most
// FollowMaxRecords) and FollowMaxPageBytes of record frames, except that its
// first record may alone be larger (up to MaxFrameBytes). The subscriber
// checks both, and each header's RECORD_COUNT, before it allocates or reads,
// and refuses a page past them.

import (
	"errors"
	"fmt"
	"io"

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
	// followMaxHeaderBytes bounds one header frame of a followed page.
	followMaxHeaderBytes = 64 << 10
)

// ErrFollowPageLimit is a followed page past its limits.
var ErrFollowPageLimit = errors.New("followed page past its limits")

// WriteFollowPage writes one followed page: its runs, then the end header.
func WriteFollowPage(w io.Writer, store *storage.FlatSQLStore, page *datasync.Page, records []*storage.Record) error {
	return writeScanPage(w, store, page, records, true)
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
// FollowMaxPageBytes, is refused with ErrFollowPageLimit before more is
// allocated or read.
func ReadFollowPage(r io.Reader, limit int) (runs []FollowRun, end *QRP.QRP, bytes int64, err error) {
	limit = min(max(limit, 1), FollowMaxRecords)
	records := 0
	var recordBytes int64
	for {
		header, frame, err := readPageMax(r, followMaxHeaderBytes)
		if err != nil {
			return nil, nil, bytes, err
		}
		bytes += int64(len(frame))
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
