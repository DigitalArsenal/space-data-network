package main

// store_migrate_format4_check.go: the check of `store-migrate --to 4`
// (store_migrate_format4.go), a hard fail before activation. The format-1
// store's index rows are walked again (with which tables hold each record,
// no record bytes); the format-4 store's copies and tags are read for them
// and compared, record by record and in order-free digests against what the
// copy sent; then the counters.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// ---- check --------------------------------------------------------------------------

// migrate4ReadCIDs is how many CIDs one GET or TAGS names: a read request
// must fit a read slot's request area (64 KiB by default, C-6) at 36 bytes a
// CID.
const migrate4ReadCIDs = 1024

// typeFields is what a type's rules (format 2's rule text, contract §2.3)
// extract: the legacy index columns COL0..COL3, the epoch, the object key.
type typeFields struct {
	cols     [4]bool
	epoch    bool
	epochDay bool
	object   []int
}

func parseTypeFields(rules string) (typeFields, error) {
	var f typeFields
	for _, line := range strings.Split(rules, "\n") {
		w := strings.Fields(line)
		if len(w) < 2 {
			continue
		}
		switch w[0] {
		case "epoch":
			f.epoch = true
		case "epoch_day":
			f.epochDay = true
		case "col":
			n, err := strconv.Atoi(w[1])
			if err != nil || n < 0 || n > 4 {
				return f, fmt.Errorf("rule %q", line)
			}
			if n < 4 {
				f.cols[n] = true
			}
		case "object":
			for _, s := range strings.Split(w[1], ",") {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 || n > 3 {
					return f, fmt.Errorf("rule %q", line)
				}
				f.object = append(f.object, n)
			}
		}
	}
	return f, nil
}

// legacyColumn is format 1's value of COLn (NULL = absent).
func legacyColumn(e storage.IndexEntry, n int) (format2.Cell, bool) {
	switch n {
	case 0:
		return format2.Int(e.NoradCatID.Int64), e.NoradCatID.Valid
	case 1:
		return format2.Text(e.EntityID.String), e.EntityID.Valid
	case 2:
		return format2.Text(e.ObjectType.String), e.ObjectType.Valid
	case 3:
		return format2.Text(e.OpsStatusCode.String), e.OpsStatusCode.Valid
	}
	return format2.Cell{}, false
}

// expectedKey is the object key the engine must hold for a record with
// format 1's columns: the first present column of the object rule.
func (f typeFields) expectedKey(e storage.IndexEntry) string {
	for _, n := range f.object {
		if v, ok := legacyColumn(e, n); ok {
			if n == 0 {
				return strconv.FormatInt(v.I, 10)
			}
			if s := string(v.B); s != "" {
				return s
			}
		}
	}
	return ""
}

// laneKey5 is format 1's lane (sdn_record_source_summary): a tag identity
// without its content key.
type laneKey5 struct{ typ, provider, source, batch, peer, pubkey string }

type laneCount struct{ n, bytes int64 }

// check compares the target with the format-1 store (want: what the copy
// sent, per schema).
func (m *migrator4) check(ctx context.Context, want map[string]*migrate4Progress) (*migrate4Check, error) {
	t0 := time.Now()
	c := &migrate4Check{}
	defer func() {
		c.Took = time.Since(t0).Round(time.Millisecond).String()
		m.addTime("check", time.Since(t0))
	}()
	m.logf("checking the format-4 store against format 1")
	lanes := map[laneKey5]laneCount{}
	items, stop := m.readFormat1(ctx, m.schemas, nil, false)
	defer stop()
	var sc *schemaCheck
	for it := range items {
		var err error
		switch {
		case it.err != nil:
			err = it.err
		case it.start:
			sc, err = m.newSchemaCheck(it.schema)
		case it.page != nil:
			err = m.checkPage(ctx, sc, it.page.entries, c, lanes)
		case it.end:
			c.Schemas++
			m.endSchemaCheck(sc, want[sc.schema], c)
		}
		if err != nil {
			return c, err
		}
	}
	if err := ctx.Err(); err != nil {
		return c, err
	}
	// Index rows of a schema no producer table holds are orphans too.
	for _, s := range m.index {
		if _, ok := m.bySchema[s.Schema]; !ok && s.Rows > 0 {
			if c.Orphans == nil {
				c.Orphans = map[string]int64{}
			}
			c.Orphans[s.Schema] = s.Rows
			m.note("%s: %d index rows and no producer table: orphans, not migrated", s.Schema, s.Rows)
		}
	}
	if err := m.checkCounters(ctx, c, want, lanes); err != nil {
		return c, err
	}
	rows, err := m.api.Rebuild(ctx, "", format4.RebuildVerify)
	if err != nil {
		return c, fmt.Errorf("verify rebuild: %w", err)
	}
	c.Rebuild = rows
	for _, r := range rows {
		if r.Mismatches != 0 {
			c.bad("verify rebuild %s: %d mismatches in %d entries", r.Type, r.Mismatches, r.Entries)
		}
	}
	if len(m.j.Rejected) > 0 {
		n := map[int32]int{}
		for _, r := range m.j.Rejected {
			n[r.Code]++
		}
		for code, k := range n {
			c.bad("%d record copies refused by the engine, code %d (%s)", k, code, format4.RejectReason(code))
		}
	}
	if c.MismatchCount > 0 {
		return c, fmt.Errorf("store-migrate --to 4: the check failed (%d mismatches); format 4 was not activated", c.MismatchCount)
	}
	return c, nil
}

// schemaCheck is the check of one schema in progress: what format 4 holds,
// accumulated page by page (got), to compare with what the copy sent.
type schemaCheck struct {
	schema, typ string
	fields      typeFields
	tables      []storage.LegacyTable
	tableOf     map[string]int // producer token -> table index (name order)
	got         migrate4Progress
}

func (m *migrator4) newSchemaCheck(schema string) (*schemaCheck, error) {
	typ, err := format4.TypeOf(schema)
	if err != nil {
		return nil, err
	}
	fields, err := parseTypeFields(m.specs[schema].Rules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", schema, err)
	}
	m.logf("checking %s", schema)
	sc := &schemaCheck{schema: schema, typ: typ, fields: fields, tables: m.bySchema[schema], tableOf: map[string]int{}}
	for i, t := range sc.tables {
		sc.tableOf[t.Token] = i
	}
	return sc, nil
}

// checkPage reads a page's records from format 4 and compares them with
// format 1's index rows; lanes accumulates the recount of the lanes.
func (m *migrator4) checkPage(ctx context.Context, sc *schemaCheck, entries []storage.IndexCopies, c *migrate4Check, lanes map[laneKey5]laneCount) error {
	schema, tables := sc.schema, sc.tables
	cids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Orphan() {
			sc.got.Orphans++
		} else {
			cids = append(cids, e.CID)
		}
	}
	var recs []format4.Rec
	var tags []format4.TagRow
	for k := 0; k < len(cids); k += migrate4ReadCIDs {
		chunk := cids[k:min(k+migrate4ReadCIDs, len(cids))]
		r, err := m.api.Get(ctx, sc.typ, chunk, true, true)
		if err != nil {
			return fmt.Errorf("%s get: %w", sc.typ, err)
		}
		tg, err := m.api.Tags(ctx, sc.typ, chunk)
		if err != nil {
			return fmt.Errorf("%s tags: %w", sc.typ, err)
		}
		recs, tags = append(recs, r...), append(tags, tg...)
	}
	copies := map[string][]format4.Rec{}
	for _, r := range recs {
		copies[r.CID] = append(copies[r.CID], r)
	}
	firstLen := map[string]int64{}
	var samples []storage.IndexEntry
	for _, e := range entries {
		if e.Orphan() {
			if len(copies[e.CID]) > 0 {
				c.bad("%s %s (index rowid %d): an orphan in format 1, %d copies in format 4", schema, e.CID, e.RowID, len(copies[e.CID]))
			}
			continue
		}
		sc.got.Held++
		cs := copies[e.CID]
		want, err := cidDigest(e.CID)
		if err != nil {
			c.bad("%s %s: %v", schema, e.CID, err)
			continue
		}
		c.Records++
		// Exactly the tables holding the record hold a copy.
		byTable := make([]*format4.Rec, len(tables))
		for i := range cs {
			r := &cs[i]
			c.Copies++
			ti, ok := sc.tableOf[r.Producer]
			switch {
			case !ok:
				c.bad("%s %s: a copy in partition %q, which no format-1 table names", schema, e.CID, r.Producer)
				continue
			case !e.Held[ti]:
				c.bad("%s %s: a copy in partition %q; format 1's %s does not hold the record", schema, e.CID, r.Producer, tables[ti].Name)
			case byTable[ti] != nil:
				c.bad("%s %s: two copies in partition %q", schema, e.CID, r.Producer)
			}
			byTable[ti] = r
			if r.Seq != e.RowID {
				c.bad("%s %s (%s): seq %d, format-1 index rowid %d", schema, e.CID, r.Producer, r.Seq, e.RowID)
			}
			if encfield.IsSealed(r.Data) {
				c.Sealed++
			} else {
				c.Rehashed++
				if sha256.Sum256(r.Data) != want {
					c.bad("%s %s (%s): the stored bytes do not hash to the CID", schema, e.CID, r.Producer)
				}
			}
			if int64(len(r.Data)) != r.Len {
				c.bad("%s %s (%s): len %d, %d bytes", schema, e.CID, r.Producer, r.Len, len(r.Data))
			}
			sc.got.CopyDigest.add(copyDigestOf(r.Seq, r.Producer, r.Peer, r.TS, r.Sig, r.Data))
		}
		var first *format4.Rec
		for ti, held := range e.Held {
			if held && byTable[ti] == nil {
				c.bad("%s %s: format 1's %s holds the record, format 4 has no copy in partition %q", schema, e.CID, tables[ti].Name, tables[ti].Token)
			}
			if first == nil && byTable[ti] != nil {
				first = byTable[ti]
			}
		}
		if first != nil {
			firstLen[e.CID] = first.Len
			m.checkFields(schema, sc.fields, e.IndexEntry, *first, c)
		}
		if e.RowID%migrate4SampleEvery == 0 {
			samples = append(samples, e.IndexEntry)
		}
	}
	for _, t := range tags {
		sc.got.Tags++
		sc.got.TagDigest.add(tagDigestOf(t.CID, t.Tag, t.At))
		k := laneKey5{sc.typ, t.Provider, t.Source, t.Batch, t.ProducerPeer, t.ProducerPubkey}
		lc := lanes[k]
		lc.n++
		lc.bytes += firstLen[t.CID]
		lanes[k] = lc
	}
	c.TagInstances += int64(len(tags))
	return m.checkColumns(ctx, schema, sc.typ, sc.fields, samples, c)
}

// endSchemaCheck compares a schema's totals and digests with what the copy
// sent (w).
func (m *migrator4) endSchemaCheck(sc *schemaCheck, w *migrate4Progress, c *migrate4Check) {
	schema, got := sc.schema, &sc.got
	if w == nil {
		w = &migrate4Progress{}
	}
	if got.Held != w.Held || got.Orphans != w.Orphans {
		c.bad("%s: %d held records and %d orphans; the copy sent %d and skipped %d", schema, got.Held, got.Orphans, w.Held, w.Orphans)
	}
	if got.Orphans > 0 {
		if c.Orphans == nil {
			c.Orphans = map[string]int64{}
		}
		c.Orphans[schema] = got.Orphans
		m.note("%s: %d index rows are orphans (no producer table holds the record); not migrated", schema, got.Orphans)
	}
	if got.CopyDigest != w.CopyDigest {
		c.bad("%s: the copies (bytes, ts, signature, peer, producer, seq) differ from what the copy sent", schema)
	}
	if got.TagDigest != w.TagDigest || got.Tags != w.Tags {
		c.bad("%s: the tag instances (%d) differ from what the copy sent (%d)", schema, got.Tags, w.Tags)
	}
}

func cidDigest(text string) ([32]byte, error) {
	var d [32]byte
	c, err := cid.Decode(text)
	if err != nil {
		return d, err
	}
	b := c.Bytes()
	if len(b) != 36 || b[0] != 0x01 || b[1] != 0x55 || b[2] != 0x12 || b[3] != 0x20 {
		return d, fmt.Errorf("not a CIDv1 raw sha2-256")
	}
	copy(d[:], b[4:])
	return d, nil
}

// checkFields compares the epoch and object key the engine extracted with
// format 1's index columns, and the columns the type's rules cannot serve.
func (m *migrator4) checkFields(schema string, f typeFields, e storage.IndexEntry, r format4.Rec, c *migrate4Check) {
	c.FieldChecks++
	switch {
	case r.HasEpoch != e.EpochUnix.Valid:
		c.bad("%s %s: epoch present %v in format 4, %v in format 1", schema, e.CID, r.HasEpoch, e.EpochUnix.Valid)
	case r.HasEpoch && r.Epoch != e.EpochUnix.Int64:
		c.bad("%s %s: epoch %d in format 4, %d in format 1", schema, e.CID, r.Epoch, e.EpochUnix.Int64)
	}
	if want := f.expectedKey(e); r.Key != want {
		c.bad("%s %s: object key %q in format 4, %q from format 1's columns", schema, e.CID, r.Key, want)
	}
	// Format 4 derives the day from the epoch (EPOCH_DAY); format 1 stored it.
	wantDay := ""
	if e.EpochUnix.Valid && f.epochDay {
		wantDay = time.Unix(e.EpochUnix.Int64, 0).UTC().Format("2006-01-02")
	}
	if e.EpochDay.String != wantDay || e.EpochDay.Valid != (wantDay != "") {
		c.bad("%s %s: format 1's epoch_day %q is not the UTC day of its epoch (%q)", schema, e.CID, e.EpochDay.String, wantDay)
	}
	for n := 0; n < 4; n++ {
		if _, ok := legacyColumn(e, n); ok && !f.cols[n] {
			c.bad("%s %s: format 1 indexes COL%d, which %s's rules do not extract", schema, e.CID, n, schema)
		}
	}
}

// checkColumns asks the engine, for each sampled record, whether its
// extracted columns equal format 1's: every present column at once (exactly
// the record matches), then each absent column (NOTNULL matches nothing).
func (m *migrator4) checkColumns(ctx context.Context, schema, typ string, f typeFields, samples []storage.IndexEntry, c *migrate4Check) error {
	if len(samples) == 0 {
		return nil
	}
	type probe struct {
		e     storage.IndexEntry
		preds []format4.Pred
		want  int64
		what  string
	}
	var probes []probe
	for _, e := range samples {
		var eq []format4.Pred
		for n := 0; n < 4; n++ {
			if !f.cols[n] {
				continue
			}
			field := format4.Field(int(format4.FieldCol0) + n)
			if v, ok := legacyColumn(e, n); ok {
				eq = append(eq, format4.Pred{Field: field, Op: format4.OpEq, Values: []format2.Cell{v}})
			} else {
				probes = append(probes, probe{e: e, want: 0, what: fmt.Sprintf("COL%d absent", n),
					preds: []format4.Pred{{Field: field, Op: format4.OpNotNull}}})
			}
		}
		if len(eq) > 0 {
			probes = append(probes, probe{e: e, preds: eq, want: 1, what: "every present column"})
		}
		c.ColumnSamples++
	}
	t0 := time.Now()
	defer func() { m.addTime("check_columns", time.Since(t0)) }()
	var mu sync.Mutex
	var firstErr error
	work := make(chan probe)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				// The record by its seq (= the index rowid, checked per copy):
				// a seq range is the engine's cursor path (0.16 ms a probe on
				// the fixture; by CID a HEAD took 5 s, a type-wide scan).
				h, err := m.api.Head(ctx, format4.Query{Type: typ, SeqAfter: p.e.RowID - 1, SeqThrough: p.e.RowID, Preds: p.preds})
				mu.Lock()
				switch {
				case err != nil:
					if firstErr == nil {
						firstErr = fmt.Errorf("%s column check of %s: %w", typ, p.e.CID, err)
					}
				case h.N != p.want:
					c.bad("%s %s: %s: the engine matches %d, format 1's columns say %d", schema, p.e.CID, p.what, h.N, p.want)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range probes {
		work <- p
	}
	close(work)
	wg.Wait()
	return firstErr
}

// checkCounters compares format 4's partition, type and lane counters with
// what the copy sent and with format 1's.
func (m *migrator4) checkCounters(ctx context.Context, c *migrate4Check, want map[string]*migrate4Progress, lanes map[laneKey5]laneCount) error {
	// Partitions: (type, producer) = a producer table.
	parts, err := m.api.Partitions(ctx)
	if err != nil {
		return err
	}
	type pk struct{ typ, producer string }
	got := map[pk]format4.PartitionSummary{}
	for _, p := range parts {
		got[pk{p.Type, p.Producer}] = p
	}
	oracle, err := m.src.PartitionCounters()
	if err != nil {
		return err
	}
	seen := map[pk]bool{}
	for _, t := range m.tables {
		typ, _ := format4.TypeOf(t.Schema)
		k := pk{typ, t.Token}
		seen[k] = true
		c.Partitions++
		var sent migrate4Tally
		if w := want[t.Schema]; w != nil {
			sent = w.Copies[t.Name]
		}
		if g := got[k]; g.Records != sent.Rows || g.Bytes != sent.Bytes {
			c.bad("partition %s/%s: %d records, %d B; the copy sent %d, %d B", typ, t.Token, g.Records, g.Bytes, sent.Rows, sent.Bytes)
		}
		// Every row of the table was copied: format 1's counter, or a
		// recount where the counter drifted, holds exactly what was read.
		if o, ok := oracle[t.Name]; ok && o.Count == sent.Rows && o.Bytes == sent.SourceBytes {
			continue
		}
		rc, err := m.src.TableCounter(t)
		if err != nil {
			return err
		}
		if o, ok := oracle[t.Name]; ok && (o.Count != rc.Count || o.Bytes != rc.Bytes) {
			m.note("format 1's counter of %s (%d, %d B) differs from a recount (%d, %d B); the recount is the oracle",
				t.Name, o.Count, o.Bytes, rc.Count, rc.Bytes)
		}
		if rc.Count != sent.Rows || rc.Bytes != sent.SourceBytes {
			c.bad("table %s holds %d rows (%d B), %d (%d B) were migrated: rows without a datasync index row are not migrated",
				t.Name, rc.Count, rc.Bytes, sent.Rows, sent.SourceBytes)
		}
	}
	for k, g := range got {
		if !seen[k] && g.Records > 0 {
			c.bad("partition %s/%s holds %d records that no format-1 table names", k.typ, k.producer, g.Records)
		}
	}
	// Types: unique records, copies and bytes, the last seq.
	types, err := m.api.Types(ctx)
	if err != nil {
		return err
	}
	byType := map[string]format4.TypeSummary{}
	for _, t := range types {
		byType[t.Type] = t
	}
	for _, schema := range m.schemas {
		typ, _ := format4.TypeOf(schema)
		w := want[schema]
		if w == nil {
			w = &migrate4Progress{}
		}
		var copies, bytes int64
		for _, t := range w.Copies {
			copies += t.Rows
			bytes += t.Bytes
		}
		g := byType[typ]
		if g.Records != w.Held || g.Copies != copies || g.CopyBytes != bytes || g.MaxSeq != w.MaxSeq {
			c.bad("type %s: %d records, %d copies, %d B, max seq %d; the copy sent %d, %d, %d B, %d",
				typ, g.Records, g.Copies, g.CopyBytes, g.MaxSeq, w.Held, copies, bytes, w.MaxSeq)
		}
	}
	// Lanes: per format-1 lane, summed over partitions and content keys, the
	// recount from the tags (each tag counts its record's FIRST copy).
	engineLanes, err := m.api.Lanes(ctx, "")
	if err != nil {
		return err
	}
	gotLanes := map[laneKey5]laneCount{}
	for _, l := range engineLanes {
		k := laneKey5{l.Type, l.Provider, l.Source, l.Batch, l.ProducerPeer, l.ProducerPubkey}
		lc := gotLanes[k]
		lc.n += l.Records
		lc.bytes += l.Bytes
		gotLanes[k] = lc
	}
	for k, w := range lanes {
		c.Lanes++
		if g := gotLanes[k]; g != w {
			c.bad("lane %v: %d records, %d B in format 4; the tags recount %d, %d B", k, g.n, g.bytes, w.n, w.bytes)
		}
	}
	for k, g := range gotLanes {
		if _, ok := lanes[k]; !ok && g.n > 0 {
			c.bad("lane %v: %d records in format 4, none in the recount", k, g.n)
		}
	}
	// Format 1's lane counters are incremental and can drift: report it.
	summary, err := m.src.SourceSummaries()
	if err != nil {
		return err
	}
	drift := 0
	for _, s := range summary {
		typ, err := format4.TypeOf(s.Schema)
		if err != nil {
			continue
		}
		k := laneKey5{typ, s.ProviderID, s.SourceName, s.BatchID, s.ProducerPeerID, s.ProducerPublicKey}
		if w := lanes[k]; w.n != s.Count || w.bytes != s.Bytes {
			if drift++; drift <= 5 {
				m.note("format 1's lane %v (%d, %d B) differs from the recount (%d, %d B); the recount is the oracle", k, s.Count, s.Bytes, w.n, w.bytes)
			}
		}
	}
	if drift > 5 {
		m.note("%d format-1 lane counters differ from the recount in all", drift)
	}
	return nil
}
