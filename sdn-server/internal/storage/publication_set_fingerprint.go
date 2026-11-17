package storage

import (
	"bufio"
	"container/heap"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// publicationSetFingerprintPage is the rows read per engine scan by
// PagedDatasetPublicationSetFingerprint.
var publicationSetFingerprintPage int64 = 20000

// publicationSetFingerprintRun is the CIDs held in memory at once by
// PagedDatasetPublicationSetFingerprint; a larger lane is sorted in runs of
// this many, each written to a temporary file, and the runs are merged.
var publicationSetFingerprintRun = 1 << 16

// PagedDatasetPublicationSetFingerprint is DatasetPublicationSetFingerprint
// read a page at a time on a format-4 store: one window over a whole lane
// decoded every row at once (the 895,910-row asteroid lane took the node's
// heap from ~0.2 to ~3.4 GB on every auto-publish; multi-body round 2,
// coordinator D3). The fingerprint and the count are the same.
//
// The lane is read in sequence order with a sequence keyset, so each page
// costs its own rows (paging in CID order by offset re-read every earlier
// row: 2,000,000 records examined about 101,000,000 rows). The CIDs are
// sorted in runs of publicationSetFingerprintRun, spilled to temporary files
// and merged into the hash in CID text order, the order of the engine's CID
// window. Other stores answer as DatasetPublicationSetFingerprint does.
func (s *FlatSQLStore) PagedDatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	if p, err := s.SelectPublication(schemaName, providerID, sourceName, batchID); err != nil {
		return "", 0, err
	} else if p != nil {
		defer p.Close()
		h, n := p.Fingerprint()
		return h, n, nil
	}
	b, ok := s.rb.(format4Backend)
	if !ok {
		return s.DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID)
	}
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" {
		return "", 0, errors.New("schema, provider and source are required for a publication fingerprint")
	}
	if err := b.closed(); err != nil {
		return "", 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return "", 0, err
	}
	q := format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, "", ""), Order: format4.OrderSeqAsc, Bulk: true,
		Limit: publicationSetFingerprintPage}
	// The set is the lane as of the read's start: records stored during the
	// read lie past this sequence and are left out.
	if head, err := b.f4TypeHead(typ); err == nil && head.Through > 0 {
		q.SeqThrough = head.Through
	} else if err != nil {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	runs := &cidRuns{}
	defer runs.close()
	buf := make([]string, 0, publicationSetFingerprintRun)
	count := 0
	for {
		recs, err := b.d.api().Scan(b.d.ctx, q)
		if errors.Is(err, format4.ErrNoType) {
			break
		}
		if err != nil {
			return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
		}
		full := int64(len(recs)) == q.Limit
		if full {
			// The rows of one sequence (a record's copies in several feed
			// files) are read in one page: the next page starts after the
			// last sequence this one keeps.
			last := recs[len(recs)-1].Seq
			keep := len(recs)
			for keep > 0 && recs[keep-1].Seq == last {
				keep--
			}
			if keep == 0 {
				return "", 0, fmt.Errorf("fingerprint %s publication set: sequence %d has %d or more rows", schemaName, last, q.Limit)
			}
			recs = recs[:keep]
		}
		for _, r := range recs {
			buf = append(buf, r.CID)
			if len(buf) == publicationSetFingerprintRun {
				if err := runs.spill(buf); err != nil {
					return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
				}
				buf = buf[:0]
			}
		}
		count += len(recs)
		if !full {
			break
		}
		q.SeqAfter = recs[len(recs)-1].Seq
	}
	h := newPublicationSetHash(schemaName, providerID, sourceName, batchID)
	if err := runs.merge(buf, h); err != nil {
		return "", 0, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	return hex.EncodeToString(h.Sum(nil)), count, nil
}

// cidRuns is the sorted runs of CIDs a fingerprint has spilled, one
// temporary file each, one CID a line.
type cidRuns struct{ files []*os.File }

func (r *cidRuns) spill(cids []string) error {
	slices.Sort(cids)
	f, err := os.CreateTemp("", "sdn-publication-fingerprint-*")
	if err != nil {
		return err
	}
	r.files = append(r.files, f)
	w := bufio.NewWriterSize(f, 1<<20)
	for _, c := range cids {
		w.WriteString(c)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}

func (r *cidRuns) close() {
	for _, f := range r.files {
		f.Close()
		os.Remove(f.Name())
	}
	r.files = nil
}

// merge hashes every spilled CID and those in last in CID text order, each
// followed by a newline.
func (r *cidRuns) merge(last []string, h hash.Hash) error {
	slices.Sort(last)
	if len(r.files) == 0 {
		for _, c := range last {
			h.Write([]byte(c))
			h.Write([]byte{'\n'})
		}
		return nil
	}
	m := &cidMerge{}
	for _, f := range r.files {
		src := &cidSource{r: bufio.NewReaderSize(f, 1<<16)}
		if ok, err := src.next(); err != nil {
			return err
		} else if ok {
			m.srcs = append(m.srcs, src)
		}
	}
	if len(last) > 0 {
		m.srcs = append(m.srcs, &cidSource{mem: last[1:], cur: last[0]})
	}
	heap.Init(m)
	for m.Len() > 0 {
		src := m.srcs[0]
		h.Write([]byte(src.cur))
		h.Write([]byte{'\n'})
		ok, err := src.next()
		if err != nil {
			return err
		}
		if ok {
			heap.Fix(m, 0)
		} else {
			heap.Pop(m)
		}
	}
	return nil
}

// cidSource is one sorted run: a spilled file or the in-memory remainder.
type cidSource struct {
	r   *bufio.Reader
	mem []string
	cur string
}

func (s *cidSource) next() (bool, error) {
	if s.r == nil {
		if len(s.mem) == 0 {
			return false, nil
		}
		s.cur, s.mem = s.mem[0], s.mem[1:]
		return true, nil
	}
	line, err := s.r.ReadString('\n')
	if err == io.EOF && line == "" {
		return false, nil
	}
	if err != nil && err != io.EOF {
		return false, err
	}
	s.cur = strings.TrimSuffix(line, "\n")
	return true, nil
}

type cidMerge struct{ srcs []*cidSource }

func (m *cidMerge) Len() int           { return len(m.srcs) }
func (m *cidMerge) Less(i, j int) bool { return m.srcs[i].cur < m.srcs[j].cur }
func (m *cidMerge) Swap(i, j int)      { m.srcs[i], m.srcs[j] = m.srcs[j], m.srcs[i] }
func (m *cidMerge) Push(x any)         { m.srcs = append(m.srcs, x.(*cidSource)) }
func (m *cidMerge) Pop() any {
	x := m.srcs[len(m.srcs)-1]
	m.srcs = m.srcs[:len(m.srcs)-1]
	return x
}
