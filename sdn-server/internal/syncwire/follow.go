package syncwire

// A followed read-chunk (QRP.FOLLOW) keeps its stream open. The subscriber's
// opening request is answered with one page; each further $QRP{KIND=Request}
// frame the subscriber writes on the stream grants one more page. A followed
// page is zero or more runs, each [$QRP Page header with RECORD_COUNT > 0]
// [RECORD_COUNT record frames], ended by one $QRP Page header with
// RECORD_COUNT 0 that carries the page's NEXT_CURSOR, or on the last page of
// what is stored its RESUME_CURSOR. Once everything stored has been sent, the
// producer answers a grant when new records of the lane are stored, or with
// an empty page (the end header alone) after a quiet interval.

import (
	"errors"
	"fmt"
	"io"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// WriteFollowPage writes one followed page: its runs, then the end header.
func WriteFollowPage(w io.Writer, store *storage.FlatSQLStore, page *datasync.Page, records []*storage.Record) error {
	return writeScanPage(w, store, page, records, true)
}

// FollowRun is one run of a followed page: its header and its record frames
// (size-prefixed, as stored).
type FollowRun struct {
	Header  *QRP.QRP
	Records [][]byte
}

// ReadFollowPage reads one followed page: its runs and its end header. An
// Error frame is returned as a *RemoteError.
func ReadFollowPage(r io.Reader) (runs []FollowRun, end *QRP.QRP, bytes int64, err error) {
	for {
		header, frame, err := ReadPage(r)
		if err != nil {
			return nil, nil, bytes, err
		}
		bytes += int64(len(frame))
		count := int(header.RECORD_COUNT())
		if count == 0 {
			return runs, header, bytes, nil
		}
		run := FollowRun{Header: header, Records: make([][]byte, 0, count)}
		for i := 0; i < count; i++ {
			record, err := ReadFrame(r, MaxFrameBytes)
			if err != nil {
				return nil, nil, bytes, fmt.Errorf("read record %d of %d: %w", i+1, count, err)
			}
			bytes += int64(len(record))
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
