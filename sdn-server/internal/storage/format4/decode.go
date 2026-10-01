package format4

// The response side of the wire (contract §3.6): RB1 blocks with fixed
// column names. A response whose header does not carry exactly the expected
// columns is an engine ABI change and fails loudly.

import (
	"fmt"
	"strconv"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

var (
	colsRec = []string{"seq", "cid", "producer", "peer", "ts", "epoch", "key", "sig", "data", "len", "provider", "source",
		"source_url", "batch", "content_key_id", "producer_peer", "producer_pubkey", "at"}
	colsPut       = []string{"i", "action", "seq", "reject"}
	colsSupersede = []string{"tags_deleted", "records_deleted", "files_deleted"}
	colsDelete    = []string{"deleted"}
	colsQuota     = []string{"files_dropped", "records_dropped", "bytes_freed"}
	colsRebuild   = []string{"type", "entries", "mismatches"}
	colsTags      = []string{"cid", "seq", "producer", "provider", "source", "source_url", "batch", "content_key_id",
		"producer_peer", "producer_pubkey", "at"}
	colsHead     = []string{"n", "bytes", "max_seq", "max_ts", "max_at", "through", "more"}
	colsIndex    = []string{"c0", "epoch", "cid"}
	colsCoverage = []string{"day", "n", "min_epoch", "max_epoch"}
	colsCount    = []string{"n"}
	colsTypes    = []string{"type", "records", "copies", "bytes", "copy_bytes", "min_epoch", "max_epoch", "min_ts", "max_ts",
		"max_seq", "through"}
	colsParts = []string{"type", "producer", "peer", "records", "bytes", "min_ts", "max_ts", "max_seq", "files"}
	colsLanes = []string{"type", "producer", "provider", "source", "batch", "content_key_id", "producer_peer", "producer_pubkey",
		"source_url", "records", "bytes", "max_seq", "first", "updated", "min_w", "max_w"}
	colsDisk    = []string{"type", "files", "db_bytes", "wal_bytes", "journal_bytes", "index_bytes", "fts_bytes", "free_bytes"}
	colsFTS     = []string{"type", "state", "through"}
	colsSurface = []string{"name", "kind", "source", "column", "placeholder", "bound"}
)

// rows is a decoded RB1 response.
type rows struct {
	op    string
	names []string
	cells [][]format2.Cell
}

func decodeRows(op string, body []byte, want []string) (*rows, error) {
	var d format2.RB1Decoder
	if err := d.Feed(body); err != nil {
		return nil, fmt.Errorf("format4: %s response: %w", op, err)
	}
	if !d.Done() {
		return nil, fmt.Errorf("format4: %s response has no end record", op)
	}
	if d.Names != nil && !sameStrings(d.Names, want) {
		return nil, fmt.Errorf("format4: %s response columns %v, want %v (engine ABI changed)", op, d.Names, want)
	}
	return &rows{op: op, names: want, cells: d.Rows}, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cellInt(c format2.Cell) int64 { return c.Int64() }

func cellStr(c format2.Cell) string {
	switch c.Type {
	case format2.CellText, format2.CellBlob:
		return string(c.B)
	case format2.CellInt:
		return strconv.FormatInt(c.I, 10)
	case format2.CellReal:
		return strconv.FormatFloat(c.F, 'g', -1, 64)
	}
	return ""
}

func cellOptInt(c format2.Cell) *int64 {
	if c.Type == format2.CellNull {
		return nil
	}
	v := c.Int64()
	return &v
}

func cellBytes(c format2.Cell) []byte {
	if c.Type == format2.CellNull {
		return nil
	}
	return c.B
}

// decodeRec decodes a REC row.
func decodeRec(r []format2.Cell) Rec {
	rec := Rec{Seq: cellInt(r[0]), CID: cellStr(r[1]), Producer: cellStr(r[2]), Peer: cellStr(r[3]), TS: cellInt(r[4]),
		Key: cellStr(r[6]), Sig: cellBytes(r[7]), Data: cellBytes(r[8]), Len: cellInt(r[9])}
	if r[5].Type != format2.CellNull {
		rec.Epoch, rec.HasEpoch = r[5].Int64(), true
	}
	if len(rec.Sig) == 0 {
		rec.Sig = nil
	}
	if r[10].Type != format2.CellNull {
		rec.Tag = &TagInstance{Tag: Tag{Provider: cellStr(r[10]), Source: cellStr(r[11]), SourceURL: cellStr(r[12]),
			Batch: cellStr(r[13]), ContentKeyID: cellStr(r[14]), ProducerPeer: cellStr(r[15]), ProducerPubkey: cellStr(r[16])},
			At: cellInt(r[17])}
	}
	return rec
}

func (rs *rows) recs() []Rec {
	out := make([]Rec, 0, len(rs.cells))
	for _, r := range rs.cells {
		out = append(out, decodeRec(r))
	}
	return out
}

// one returns the single row of a one-row response.
func (rs *rows) one() ([]format2.Cell, error) {
	if len(rs.cells) != 1 {
		return nil, fmt.Errorf("format4: %s returned %d rows, want 1", rs.op, len(rs.cells))
	}
	return rs.cells[0], nil
}
