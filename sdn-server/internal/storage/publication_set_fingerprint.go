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
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// publicationSetFingerprintPage is the rows read per engine scan by
// PagedDatasetPublicationSetFingerprint and PlanDatasetPublication.
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
	plan, err := b.planPublication(schemaName, providerID, sourceName, batchID, false)
	if err != nil {
		return "", 0, err
	}
	defer plan.Close()
	h, n := plan.Fingerprint()
	return h, n, nil
}

// PublicationPlan is a format-4 lane's records in CID text order (the order
// of the engine's CID window, which a publication series cuts into shards
// by offset and limit), read once by sequence keyset as of the plan's start,
// with the lane's set fingerprint. A series takes its shard windows from it
// in order (Next) and reads each window's records by CID, so each window
// costs its own records instead of re-reading every record before its
// offset. The windows, and so what a series records, are the ones the CID
// window by offset gives over the same set.
type PublicationPlan struct {
	Schema, Provider, Source, Batch string

	fingerprint string
	count       int
	file        *os.File
	r           *bufio.Reader
	pos         int
	pending     *planEntry
}

// PlanDatasetPublication plans a publication series of a lane of a format-4
// store. It answers nil for any other store, and for a lane whose
// publication is a retained selection (SelectPublication), which exports
// from its own references. Close removes the plan's temporary file.
func (s *FlatSQLStore) PlanDatasetPublication(schemaName, providerID, sourceName, batchID string) (*PublicationPlan, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, nil
	}
	if p, err := s.SelectPublication(schemaName, providerID, sourceName, batchID); err != nil {
		return nil, err
	} else if p != nil {
		p.Close()
		return nil, nil
	}
	return b.planPublication(schemaName, providerID, sourceName, batchID, true)
}

func (b format4Backend) planPublication(schemaName, providerID, sourceName, batchID string, keep bool) (*PublicationPlan, error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" {
		return nil, errors.New("schema, provider and source are required for a publication fingerprint")
	}
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	q := format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, "", ""), Order: format4.OrderSeqAsc, Bulk: true,
		Limit: publicationSetFingerprintPage}
	// The set is the lane as of the read's start: records stored during the
	// read lie past this sequence and are left out.
	if head, err := b.f4TypeHead(typ); err == nil && head.Through > 0 {
		q.SeqThrough = head.Through
	} else if err != nil {
		return nil, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	tempDir, err := b.s.sortTempDir()
	if err != nil {
		return nil, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	runs := &cidRuns{dir: tempDir}
	defer runs.close()
	buf := make([]planEntry, 0, publicationSetFingerprintRun)
	count := 0
	for {
		recs, err := b.d.api().Scan(b.d.ctx, q)
		if errors.Is(err, format4.ErrNoType) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
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
				return nil, fmt.Errorf("fingerprint %s publication set: sequence %d has %d or more rows", schemaName, last, q.Limit)
			}
			recs = recs[:keep]
		}
		for _, r := range recs {
			buf = append(buf, planEntry{cid: r.CID, n: r.Len})
			if len(buf) == publicationSetFingerprintRun {
				if err := runs.spill(buf); err != nil {
					return nil, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
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
	plan := &PublicationPlan{Schema: schemaName, Provider: providerID, Source: sourceName, Batch: batchID, count: count}
	var out *bufio.Writer
	if keep {
		if plan.file, err = os.CreateTemp(tempDir, "sdn-publication-plan-*"); err != nil {
			return nil, fmt.Errorf("plan %s publication: %w", schemaName, err)
		}
		out = bufio.NewWriterSize(plan.file, 1<<20)
	}
	h := newPublicationSetHash(schemaName, providerID, sourceName, batchID)
	if err := runs.merge(buf, h, out); err != nil {
		plan.Close()
		return nil, fmt.Errorf("fingerprint %s publication set: %w", schemaName, err)
	}
	plan.fingerprint = hex.EncodeToString(h.Sum(nil))
	if keep {
		if err := out.Flush(); err != nil {
			plan.Close()
			return nil, fmt.Errorf("plan %s publication: %w", schemaName, err)
		}
		if _, err := plan.file.Seek(0, io.SeekStart); err != nil {
			plan.Close()
			return nil, fmt.Errorf("plan %s publication: %w", schemaName, err)
		}
		plan.r = bufio.NewReaderSize(plan.file, 1<<16)
	}
	return plan, nil
}

// Fingerprint is the planned set's fingerprint and count, the values
// PagedDatasetPublicationSetFingerprint gives for the lane.
func (p *PublicationPlan) Fingerprint() (string, int) { return p.fingerprint, p.count }

// Next is the CIDs of the series window that starts at offset: at most limit
// records, and of those the longest prefix whose shard frames (stored length
// plus the size prefix) fit maxBytes (0 = no byte budget), at least one
// record, as IndexedRecordWindowLimitForBytes cuts the CID window; cut
// reports that the budget stopped it. Windows are taken in order: offset is
// where the previous window ended. An empty answer is the end of the set.
func (p *PublicationPlan) Next(offset, limit int, maxBytes int64) (cids []string, cut bool, err error) {
	if p == nil || p.r == nil {
		return nil, false, errors.New("publication plan is closed")
	}
	if offset != p.pos {
		return nil, false, fmt.Errorf("publication plan is at offset %d, not %d", p.pos, offset)
	}
	var used int64
	for len(cids) < limit {
		e := p.pending
		if e == nil {
			if e, err = p.read(); err != nil {
				return nil, false, err
			}
			if e == nil {
				break
			}
		}
		frame := e.n + DatasetShardFrameOverheadBytes
		if maxBytes > 0 && len(cids) > 0 && used+frame > maxBytes {
			p.pending = e
			cut = true
			break
		}
		p.pending = nil
		used += frame
		cids = append(cids, e.cid)
	}
	p.pos += len(cids)
	return cids, cut, nil
}

func (p *PublicationPlan) read() (*planEntry, error) {
	line, err := p.r.ReadString('\n')
	if err == io.EOF && line == "" {
		return nil, nil
	}
	if err != nil && err != io.EOF {
		return nil, err
	}
	e, err := parsePlanEntry(strings.TrimSuffix(line, "\n"))
	return &e, err
}

// Close removes the plan's temporary file. It is safe to call repeatedly.
func (p *PublicationPlan) Close() {
	if p != nil && p.file != nil {
		p.file.Close()
		os.Remove(p.file.Name())
		p.file, p.r = nil, nil
	}
}

// planEntry is one planned record: its CID and stored length.
type planEntry struct {
	cid string
	n   int64
}

func parsePlanEntry(line string) (planEntry, error) {
	c, n, ok := strings.Cut(line, " ")
	if !ok {
		return planEntry{}, fmt.Errorf("publication plan line %q has no length", line)
	}
	v, err := strconv.ParseInt(n, 10, 64)
	return planEntry{cid: c, n: v}, err
}

func writePlanEntry(w *bufio.Writer, e planEntry) {
	w.WriteString(e.cid)
	w.WriteByte(' ')
	w.WriteString(strconv.FormatInt(e.n, 10))
	w.WriteByte('\n')
}

func sortPlanEntries(es []planEntry) {
	slices.SortFunc(es, func(a, b planEntry) int { return strings.Compare(a.cid, b.cid) })
}

// cidRuns is the sorted runs of CIDs a fingerprint has spilled, one
// temporary file each (in dir, the store's sort directory), one CID and its
// stored length a line.
type cidRuns struct {
	dir   string
	files []*os.File
}

func (r *cidRuns) spill(es []planEntry) error {
	sortPlanEntries(es)
	f, err := os.CreateTemp(r.dir, "sdn-publication-fingerprint-*")
	if err != nil {
		return err
	}
	r.files = append(r.files, f)
	w := bufio.NewWriterSize(f, 1<<20)
	for _, e := range es {
		writePlanEntry(w, e)
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
// followed by a newline, and writes each entry to out when out is not nil.
func (r *cidRuns) merge(last []planEntry, h hash.Hash, out *bufio.Writer) error {
	sortPlanEntries(last)
	emit := func(e planEntry) {
		h.Write([]byte(e.cid))
		h.Write([]byte{'\n'})
		if out != nil {
			writePlanEntry(out, e)
		}
	}
	if len(r.files) == 0 {
		for _, e := range last {
			emit(e)
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
		emit(src.cur)
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
	mem []planEntry
	cur planEntry
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
	s.cur, err = parsePlanEntry(strings.TrimSuffix(line, "\n"))
	return err == nil, err
}

type cidMerge struct{ srcs []*cidSource }

func (m *cidMerge) Len() int           { return len(m.srcs) }
func (m *cidMerge) Less(i, j int) bool { return m.srcs[i].cur.cid < m.srcs[j].cur.cid }
func (m *cidMerge) Swap(i, j int)      { m.srcs[i], m.srcs[j] = m.srcs[j], m.srcs[i] }
func (m *cidMerge) Push(x any)         { m.srcs = append(m.srcs, x.(*cidSource)) }
func (m *cidMerge) Pop() any {
	x := m.srcs[len(m.srcs)-1]
	m.srcs = m.srcs[:len(m.srcs)-1]
	return x
}
