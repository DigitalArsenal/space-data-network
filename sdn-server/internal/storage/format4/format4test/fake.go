// Package format4test is the in-memory reference of store format 4 (build-out
// contract §5.2 "format4test"): Fake implements format4.API with the
// contract's semantics (§3.5-§3.8), and Conformance is the table-driven suite
// both Fake and the real engine must pass.
//
// Fake keeps everything in memory. It does not run SQL (SQL and Surface
// answer StatusUnsupported) and it does not parse type rules: a record's
// fields come from Fake.Extract, which defaults to DefaultExtract (OMM, MPE
// and CAT, as format 1's Go extraction). Store packages that own a richer
// extraction (format 1's) install it.
package format4test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/internal/cidv1"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// Fields are a record's extracted fields (the engine's typecfg rules).
type Fields struct {
	Epoch            *int64  // content time e, Unix s
	Bucket           *int64  // C-1 bucket time; nil = Epoch
	Col0             *int64  // NORAD
	Col1, Col2, Col3 *string // OBJECT_ID/ENTITY_ID/FILE_ID, OBJECT_TYPE, OPS_STATUS_CODE
	Key              string  // object key, text ("" = none)
	Supersede        string  // supersede-on-ingest object identity ("" = none)
	Text             string  // full-text content
}

// Extractor extracts a record's fields from its plaintext.
type Extractor func(typ string, plain []byte) (Fields, error)

// Fake is the in-memory format-4 store.
type Fake struct {
	// Extract extracts fields (default DefaultExtract). Set before use.
	Extract Extractor
	// Now is the engine clock, Unix s (default time.Now).
	Now func() int64
	// MaxRecordBytes is C-6's largest storable record: the write request
	// area minus 64 KiB (default 8 MiB - 64 KiB).
	MaxRecordBytes int

	mu        sync.Mutex
	opt       format4.Options
	activated bool
	closed    bool
	quota     int64
	types     map[string]*ftype
	stats     [40]uint64
}

var _ format4.API = (*Fake)(nil)

// New returns an empty store, as Open with CreateFresh and no data root.
func New() *Fake {
	return &Fake{opt: format4.Options{Create: format4.CreateFresh, GseqFloor: 1}, activated: true, types: map[string]*ftype{}}
}

// Open emulates format4.Open's create modes (§3.3 config tag 2) and, with a
// DataRoot, the markers the engine writes (§2.2): MIGRATED then STORE at a
// fresh create, and at Activate for a migration target. Records live in
// memory only: a reopen starts empty.
func Open(ctx context.Context, opt format4.Options) (*Fake, error) {
	f := New()
	f.opt = opt
	if f.opt.GseqFloor == 0 {
		f.opt.GseqFloor = 1
	}
	f.activated = opt.Create != format4.CreateForMigration
	if opt.DataRoot == "" {
		return f, nil
	}
	m, err := marker.Read(opt.DataRoot)
	if err != nil {
		return nil, err
	}
	switch opt.Create {
	case format4.OpenExisting:
		if !m.Activated() {
			return nil, &format4.StatusError{Op: "init", Status: format4.StatusFormat, Msg: "no activated store"}
		}
	case format4.CreateFresh:
		if !m.Activated() {
			if m.StoreValid && !m.MigratedPresent {
				return nil, &format4.StatusError{Op: "init", Status: format4.StatusFormat, Msg: "STORE without MIGRATED"}
			}
			if err := writeMarkers(opt.DataRoot, f.opt.GseqFloor, 0); err != nil {
				return nil, err
			}
		}
	case format4.CreateForMigration:
		if m.StorePresent {
			return nil, &format4.StatusError{Op: "init", Status: format4.StatusFormat, Msg: "a migration target has no STORE"}
		}
	default:
		return nil, &format4.StatusError{Op: "init", Status: format4.StatusArg, Msg: "create mode"}
	}
	return f, nil
}

func writeMarkers(dataRoot string, floor uint64, from uint32) error {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return err
	}
	dir := filepath.Join(dataRoot, marker.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, w := range []struct {
		name string
		b    []byte
	}{{marker.MigratedFile, marker.MigratedBytes(uuid, now)}, {marker.StoreFile, marker.StoreBytes(uuid, now, floor, from)}} {
		if err := writeSynced(filepath.Join(dir, w.name), w.b); err != nil {
			return err
		}
	}
	return nil
}

func writeSynced(name string, b []byte) error {
	fh, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(b); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}

// ---- state ----------------------------------------------------------------

// ident is a lane identity (C-3).
type ident struct{ provider, source, batch, ckey, peer, pubkey string }

func identOf(t format4.Tag) ident {
	return ident{t.Provider, t.Source, t.Batch, t.ContentKeyID, t.ProducerPeer, t.ProducerPubkey}
}

func (a ident) less(b ident) bool {
	x := [6]string{a.provider, a.source, a.batch, a.ckey, a.peer, a.pubkey}
	y := [6]string{b.provider, b.source, b.batch, b.ckey, b.peer, b.pubkey}
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

func (a ident) matches(l format4.LaneFilter) bool {
	return (l.Provider == "" || l.Provider == a.provider) && (l.Source == "" || l.Source == a.source) &&
		(l.Batch == "" || l.Batch == a.batch) && (l.ContentKeyID == "" || l.ContentKeyID == a.ckey) &&
		(l.ProducerPeer == "" || l.ProducerPeer == a.peer) && (l.ProducerPubkey == "" || l.ProducerPubkey == a.pubkey)
}

func laneFiltered(l format4.LaneFilter) bool { return l != format4.LaneFilter{} }

type inst struct {
	id ident
	at int64
}

type fcopy struct {
	pid  int
	peer string
	sig  []byte
	tags []inst
}

type frec struct {
	seq    int64
	cid    string
	d      []byte
	ts     int64
	f      Fields
	tb     int64 // bucket month YYYYMM, 0 without a bucket time
	copies map[int]*fcopy
}

func (r *frec) w() int64 {
	if r.f.Epoch != nil {
		return *r.f.Epoch
	}
	return r.ts
}

// pids returns the record's copies' partitions, ascending.
func (r *frec) pids() []int {
	out := make([]int, 0, len(r.copies))
	for pid := range r.copies {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

type fpart struct {
	pid   int
	token string
	peer  string // the first writer's peer
}

type laneKey struct {
	pid int
	id  ident
}

type laneMeta struct {
	url     string
	updated int64
}

type identKey struct {
	provider, source string
	h                [32]byte
}

type cidIdent struct {
	cid string
	id  ident
}

type ftype struct {
	name      string
	spec      format4.TypeSpec
	specBytes []byte
	next      int64
	through   int64
	recs      map[int64]*frec
	byCID     map[string]int64
	idents    map[identKey]string // -> cid holding it
	parts     []*fpart            // pid = index + 1
	byToken   map[string]*fpart
	lanes     map[laneKey]*laneMeta
	urls      map[cidIdent]string
}

func (t *ftype) part(token, peer string) *fpart {
	if p := t.byToken[token]; p != nil {
		return p
	}
	p := &fpart{pid: len(t.parts) + 1, token: token, peer: peer}
	t.parts = append(t.parts, p)
	t.byToken[token] = p
	return p
}

// sortedSeqs returns the type's seqs, ascending.
func (t *ftype) sortedSeqs() []int64 {
	out := make([]int64, 0, len(t.recs))
	for seq := range t.recs {
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ProducerToken is the engine's partition key for a peer (typecfg
// producerToken, A3; C-13): trimmed, ASCII alphanumerics kept, every other
// rune '_', "unattributed" when empty.
func ProducerToken(peer string) string {
	peer = strings.Trim(peer, " \t\n\r\f\v")
	if peer == "" {
		return "unattributed"
	}
	var b strings.Builder
	for _, r := range peer {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func bucketMonth(t *int64) int64 {
	if t == nil {
		return 0
	}
	u := time.Unix(*t, 0).UTC()
	return int64(u.Year()*100 + int(u.Month()))
}

func (f *Fake) now() int64 {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().Unix()
}

func (f *Fake) extract(typ string, plain []byte) (Fields, error) {
	if f.Extract != nil {
		return f.Extract(typ, plain)
	}
	return DefaultExtract(typ, plain)
}

func (f *Fake) maxRecord() int {
	if f.MaxRecordBytes > 0 {
		return f.MaxRecordBytes
	}
	return 8<<20 - 64<<10
}

func statusErr(op string, status int32, msg string) error {
	return &format4.StatusError{Op: op, Status: status, Msg: msg}
}

// begin locks the store for one call; the caller unlocks.
func (f *Fake) begin(ctx context.Context, op string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return statusErr(op, format4.StatusStopped, "")
	}
	if f.types == nil {
		f.types = map[string]*ftype{}
	}
	return nil
}

func (f *Fake) typ(op, name string) (*ftype, error) {
	t := f.types[name]
	if t == nil {
		return nil, statusErr(op, format4.StatusNoType, name)
	}
	return t, nil
}

// ---- control --------------------------------------------------------------

// bucketRule is a spec's month-mapping rule (C-5): its bucket and epoch lines.
var bucketRuleLine = regexp.MustCompile(`(?m)^(bucket|epoch)\s.*$`)

func bucketRule(spec format4.TypeSpec) string {
	return strings.Join(bucketRuleLine.FindAllString(spec.Rules, -1), "\n")
}

// RegisterType registers a type; identical bytes are a no-op, and a changed
// bucket or epoch rule of a type that holds data is refused (C-5).
func (f *Fake) RegisterType(spec format4.TypeSpec) error {
	if err := f.begin(nil, "register_type"); err != nil {
		return err
	}
	defer f.mu.Unlock()
	name := spec.TypeName()
	if name == "" || len(spec.BFBS) == 0 {
		return statusErr("register_type", format4.StatusArg, "spec without a schema name or BFBS")
	}
	enc := spec.Encode()
	if t := f.types[name]; t != nil {
		if string(t.specBytes) == string(enc) {
			return nil
		}
		if len(t.recs) > 0 && bucketRule(t.spec) != bucketRule(spec) {
			return statusErr("register_type", format4.StatusFormat, "the bucket/epoch rule of a type with data cannot change")
		}
		t.spec, t.specBytes = spec, enc
		return nil
	}
	floor := int64(f.opt.GseqFloor)
	if floor < 1 {
		floor = 1
	}
	f.types[name] = &ftype{name: name, spec: spec, specBytes: enc, next: floor, recs: map[int64]*frec{},
		byCID: map[string]int64{}, idents: map[identKey]string{}, byToken: map[string]*fpart{},
		lanes: map[laneKey]*laneMeta{}, urls: map[cidIdent]string{}}
	f.stats[33] = uint64(len(f.types))
	return nil
}

// SetQuota sets the quota (0 = none).
func (f *Fake) SetQuota(bytes int64) error {
	if err := f.begin(nil, "set_quota"); err != nil {
		return err
	}
	defer f.mu.Unlock()
	f.quota = bytes
	return nil
}

// Activate is a migration target's engine activation step: MIGRATED, then
// STORE with migrated_from 1 (§2.3 step 1).
func (f *Fake) Activate(ctx context.Context) error {
	if err := f.begin(ctx, "activate"); err != nil {
		return err
	}
	defer f.mu.Unlock()
	if f.opt.Create != format4.CreateForMigration {
		return statusErr("activate", format4.StatusFormat, "activate needs create mode 2")
	}
	if f.opt.DataRoot != "" {
		if err := writeMarkers(f.opt.DataRoot, f.opt.GseqFloor, 1); err != nil {
			return statusErr("activate", format4.StatusIO, err.Error())
		}
	}
	f.activated = true
	return nil
}

// Stats returns the §3.10 counters the Fake keeps (the rest are 0).
func (f *Fake) Stats() ([]uint64, error) {
	if err := f.begin(nil, "stats"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	s := f.stats
	var parts uint64
	for _, t := range f.types {
		parts += uint64(len(t.parts))
	}
	s[34] = parts
	return append([]uint64(nil), s[:]...), nil
}

// Close stops the store.
func (f *Fake) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// ---- writes ---------------------------------------------------------------

// Put stores a batch (§3.7).
func (f *Fake) Put(ctx context.Context, b format4.Batch) ([]format4.Outcome, error) {
	if err := f.begin(ctx, "PUT"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	if b.Mode != format4.ModeIngest && b.Mode != format4.ModeMigrate {
		return nil, statusErr("PUT", format4.StatusArg, "mode")
	}
	for _, in := range b.Records {
		if b.Mode == format4.ModeIngest && (in.Seq != 0 || len(in.Tags) > 0) {
			return nil, statusErr("PUT", format4.StatusArg, "seq or explicit tags in ingest mode")
		}
	}
	t, err := f.typ("PUT", b.Type)
	if err != nil {
		return nil, err
	}
	at := b.At
	if at == 0 {
		at = f.now()
	}
	callTagsOK := true
	for _, tg := range b.Tags {
		if tg.Provider == "" || tg.Source == "" {
			callTagsOK = false
		}
	}
	token := ProducerToken(b.Peer)
	out := make([]format4.Outcome, len(b.Records))
	f.stats[0]++
	f.stats[1] += uint64(len(b.Records))
	for i, in := range b.Records {
		o := f.putOne(t, b, in, token, at, callTagsOK)
		out[i] = o
		switch o.Action {
		case format4.ActRejected:
			f.stats[7]++
		case format4.ActNew, format4.ActMigrated:
			f.stats[2]++
		case format4.ActCopy:
			f.stats[3]++
		case format4.ActRetag:
			f.stats[4]++
		case format4.ActDup:
			f.stats[5]++
		case format4.ActIdentDup:
			f.stats[6]++
		}
	}
	return out, nil
}

func reject(code int32) format4.Outcome {
	return format4.Outcome{Action: format4.ActRejected, Reject: code}
}

func (f *Fake) putOne(t *ftype, b format4.Batch, in format4.In, token string, at int64, callTagsOK bool) format4.Outcome {
	if _, err := cidv1.Parse(in.CID); err != nil {
		return reject(format4.RejectCIDForm)
	}
	if len(in.Plain)+len(in.Sealed) > f.maxRecord() {
		return reject(format4.RejectTooLarge)
	}
	if maxFrame := t.spec.MaxFrame; maxFrame > 0 && uint64(len(in.Plain)+4) > maxFrame {
		return reject(format4.RejectTooLarge)
	}
	if t.spec.Flags&format2.TypeVerifyCID != 0 && cidv1.Of(in.Plain) != in.CID {
		return reject(format4.RejectCIDMismatch)
	}
	if t.spec.Flags&format2.TypeVerifyBFBS != 0 && !hasIdentifier(in.Plain, t.spec.FID) {
		return reject(format4.RejectFID)
	}
	peer := in.Peer
	if peer == "" {
		peer = b.Peer
	}
	if ProducerToken(peer) != token {
		return reject(format4.RejectTag)
	}
	// The record's tag instances.
	var tags []inst
	if b.Mode == format4.ModeIngest {
		if !callTagsOK {
			return reject(format4.RejectTag)
		}
		for _, tg := range b.Tags {
			tags = append(tags, inst{identOf(tg), at})
		}
	} else {
		for _, ta := range in.Tags {
			if ta.Tag < 0 || ta.Tag >= len(b.Tags) {
				return reject(format4.RejectTag)
			}
			tg := b.Tags[ta.Tag]
			if tg.Provider == "" || tg.Source == "" {
				return reject(format4.RejectTag)
			}
			tags = append(tags, inst{identOf(tg), ta.At})
		}
		if in.Seq <= 0 {
			return reject(format4.RejectSeq)
		}
		if held, ok := t.byCID[in.CID]; ok && held != in.Seq {
			return reject(format4.RejectSeq)
		}
		if r := t.recs[in.Seq]; r != nil && r.cid != in.CID {
			return reject(format4.RejectSeq)
		}
	}
	fields, err := f.extract(t.name, in.Plain)
	if err != nil {
		return reject(format4.RejectAttr)
	}
	stored := in.Plain
	if in.Sealed != nil {
		stored = in.Sealed
	}
	p := t.part(token, peer)
	now := f.now()

	// IQC identity (format 1: scope = the write's (provider, source) lane; a
	// held identity whose record is gone does not count).
	var ik *identKey
	if t.spec.Identity && in.Ident != nil && b.Mode == format4.ModeIngest && len(b.Tags) > 0 {
		k := identKey{b.Tags[0].Provider, b.Tags[0].Source, *in.Ident}
		ik = &k
		if held, ok := t.idents[k]; ok && held != in.CID {
			if seq, live := t.byCID[held]; live {
				return format4.Outcome{Action: format4.ActIdentDup, Seq: seq}
			}
		}
	}

	seq, held := t.byCID[in.CID]
	var action format4.Action
	switch {
	case !held:
		if b.Mode == format4.ModeMigrate {
			seq = in.Seq
			action = format4.ActMigrated
			if seq >= t.next {
				t.next = seq + 1
			}
		} else {
			seq = t.next
			t.next++
			action = format4.ActNew
		}
		bucket := fields.Bucket
		if bucket == nil {
			bucket = fields.Epoch
		}
		t.recs[seq] = &frec{seq: seq, cid: in.CID, d: append([]byte(nil), stored...), ts: in.TS, f: fields,
			tb: bucketMonth(bucket), copies: map[int]*fcopy{}}
		t.byCID[in.CID] = seq
		if seq > t.through {
			t.through = seq
		}
	case t.recs[seq].copies[p.pid] == nil:
		action = format4.ActCopy
	default:
		action = format4.ActDup
		if b.Mode == format4.ModeMigrate {
			action = format4.ActMigrated
		}
	}
	r := t.recs[seq]
	c := r.copies[p.pid]
	if c == nil {
		c = &fcopy{pid: p.pid, peer: peer, sig: append([]byte(nil), in.Sig...)}
		r.copies[p.pid] = c
		if action == format4.ActCopy || action == format4.ActNew || action == format4.ActMigrated {
			f.supersedeOnIngest(t, r, p, b)
		}
	}
	for _, ti := range tags {
		k := laneKey{p.pid, ti.id}
		found := false
		for _, have := range c.tags {
			if have.id == ti.id {
				found = true
				break
			}
		}
		if !found {
			c.tags = append(c.tags, ti)
			if action == format4.ActDup {
				action = format4.ActRetag
			}
		}
		lm := t.lanes[k]
		if lm == nil {
			lm = &laneMeta{}
			t.lanes[k] = lm
		}
		lm.updated = now
	}
	// source_url: the latest write carrying a tag identity sets it (C-3).
	if b.Mode == format4.ModeIngest {
		for _, tg := range b.Tags {
			t.urls[cidIdent{in.CID, identOf(tg)}] = tg.SourceURL
			t.lanes[laneKey{p.pid, identOf(tg)}].url = tg.SourceURL
		}
	} else {
		for _, ta := range in.Tags {
			tg := b.Tags[ta.Tag]
			t.urls[cidIdent{in.CID, identOf(tg)}] = tg.SourceURL
			t.lanes[laneKey{p.pid, identOf(tg)}].url = tg.SourceURL
		}
	}
	if ik != nil {
		t.idents[*ik] = in.CID
	}
	return format4.Outcome{Action: action, Seq: seq}
}

func hasIdentifier(b []byte, fid [4]byte) bool {
	if len(b) >= 8 && [4]byte(b[4:8]) == fid {
		return true
	}
	return len(b) >= 12 && [4]byte(b[8:12]) == fid
}

// supersedeOnIngest is CAT's supersede rule (record_supersede.go): a record
// with an object identity retires, in the same partition, every other copy of
// that identity in this write's source scope (a copy tagged with the source,
// or an untagged one).
func (f *Fake) supersedeOnIngest(t *ftype, r *frec, p *fpart, b format4.Batch) {
	if r.f.Supersede == "" || !strings.Contains(t.spec.Rules, "supersede") {
		return
	}
	source := ""
	if len(b.Tags) > 0 {
		source = b.Tags[0].Source
	}
	for _, seq := range t.sortedSeqs() {
		o := t.recs[seq]
		if o == nil || o == r || o.f.Supersede != r.f.Supersede {
			continue
		}
		c := o.copies[p.pid]
		if c == nil {
			continue
		}
		inScope := len(c.tags) == 0
		for _, ti := range c.tags {
			if source != "" && ti.id.source == source {
				inScope = true
			}
		}
		if inScope {
			f.removeCopy(t, o, p.pid)
			f.stats[8]++
		}
	}
}

// removeCopy drops one copy and, with its last copy, the record.
func (f *Fake) removeCopy(t *ftype, r *frec, pid int) {
	delete(r.copies, pid)
	if len(r.copies) == 0 {
		delete(t.recs, r.seq)
		delete(t.byCID, r.cid)
	}
}

// Supersede deletes every tag instance of (type, provider, source) whose
// batch is not keepBatch, then every copy left with no tag (§3.8 item 7).
func (f *Fake) Supersede(ctx context.Context, typ, provider, source, keepBatch string, apply bool) (format4.SupersedeResult, error) {
	var res format4.SupersedeResult
	if err := f.begin(ctx, "SUPERSEDE"); err != nil {
		return res, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("SUPERSEDE", typ)
	if err != nil {
		return res, err
	}
	type plan struct {
		r    *frec
		pid  int
		keep []inst
	}
	var plans []plan
	before := t.fileCopies()
	after := t.fileCopies()
	for _, seq := range t.sortedSeqs() {
		r := t.recs[seq]
		for _, pid := range r.pids() {
			c := r.copies[pid]
			var keep []inst
			for _, ti := range c.tags {
				if !(ti.id.provider == provider && ti.id.source == source && ti.id.batch != keepBatch) {
					keep = append(keep, ti)
				}
			}
			if dropped := len(c.tags) - len(keep); dropped > 0 {
				res.TagsDeleted += int64(dropped)
				if len(keep) == 0 {
					res.RecordsDeleted++
					after[[2]int64{int64(pid), r.tb}]--
				}
				plans = append(plans, plan{r, pid, keep})
			}
		}
	}
	for k, n := range before {
		if n > 0 && after[k] == 0 {
			res.FilesDeleted++
		}
	}
	if !apply {
		return res, nil
	}
	for _, p := range plans {
		p.r.copies[p.pid].tags = p.keep
		if len(p.keep) == 0 {
			f.removeCopy(t, p.r, p.pid)
		}
	}
	f.stats[9] += uint64(res.TagsDeleted)
	f.stats[10] += uint64(res.RecordsDeleted)
	return res, nil
}

// fileCopies counts the copies in each (pid, bucket month) partition file.
func (t *ftype) fileCopies() map[[2]int64]int64 {
	out := map[[2]int64]int64{}
	for _, r := range t.recs {
		for pid := range r.copies {
			out[[2]int64{int64(pid), r.tb}]++
		}
	}
	return out
}

// Delete removes every copy of each CID; it returns the copies removed.
func (f *Fake) Delete(ctx context.Context, typ string, cids []string) (int64, error) {
	if err := f.begin(ctx, "DELETE"); err != nil {
		return 0, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("DELETE", typ)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, cid := range cids {
		seq, ok := t.byCID[cid]
		if !ok {
			continue
		}
		r := t.recs[seq]
		for _, pid := range r.pids() {
			f.removeCopy(t, r, pid)
			n++
		}
	}
	f.stats[11] += uint64(n)
	return n, nil
}

// QuotaGC drops whole bucket months, oldest first across the types, until
// the stored bytes are at most maxBytes (§3.8 item 11, quota mode 1). Types
// without a bucket time are never dropped by month.
func (f *Fake) QuotaGC(ctx context.Context, maxBytes int64) (format4.QuotaResult, error) {
	var res format4.QuotaResult
	if err := f.begin(ctx, "QUOTA_GC"); err != nil {
		return res, err
	}
	defer f.mu.Unlock()
	total := func() (n int64) {
		for _, t := range f.types {
			for _, r := range t.recs {
				n += int64(len(r.d) * len(r.copies))
			}
		}
		return n
	}
	for total() > maxBytes {
		var oldest int64
		var victim *ftype
		names := f.typeNames()
		for _, name := range names {
			t := f.types[name]
			for _, r := range t.recs {
				if r.tb != 0 && (oldest == 0 || r.tb < oldest) {
					oldest, victim = r.tb, t
				}
			}
		}
		if victim == nil {
			break
		}
		files := map[int]bool{}
		for _, seq := range victim.sortedSeqs() {
			r := victim.recs[seq]
			if r.tb != oldest {
				continue
			}
			for _, pid := range r.pids() {
				files[pid] = true
				res.RecordsDropped++
				res.BytesFreed += int64(len(r.d))
				f.removeCopy(victim, r, pid)
			}
		}
		res.FilesDropped += int64(len(files))
	}
	f.stats[22] += uint64(res.FilesDropped)
	return res, nil
}

func (f *Fake) typeNames() []string {
	out := make([]string, 0, len(f.types))
	for name := range f.types {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Rebuild reports each type's index entries (copies); the Fake's derived
// state is never out of step, so mismatches are 0.
func (f *Fake) Rebuild(ctx context.Context, typ string, what format4.RebuildWhat) ([]format4.RebuildRow, error) {
	if err := f.begin(ctx, "REBUILD"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.RebuildRow
	for _, name := range f.typeNames() {
		if typ != "" && name != typ {
			continue
		}
		var n int64
		for _, r := range f.types[name].recs {
			n += int64(len(r.copies))
		}
		out = append(out, format4.RebuildRow{Type: name, Entries: n})
	}
	if typ != "" && len(out) == 0 {
		return nil, statusErr("REBUILD", format4.StatusNoType, typ)
	}
	return out, nil
}

// ---- reads ----------------------------------------------------------------

// earliest returns the earliest instance (at, then identity) among those
// keep accepts, or nil.
func earliest(tags []inst, keep func(inst) bool) *inst {
	var best *inst
	for i := range tags {
		ti := &tags[i]
		if keep != nil && !keep(*ti) {
			continue
		}
		if best == nil || ti.at < best.at || (ti.at == best.at && ti.id.less(best.id)) {
			best = ti
		}
	}
	return best
}

func (t *ftype) tagInstance(cid string, ti *inst) *format4.TagInstance {
	if ti == nil {
		return nil
	}
	return &format4.TagInstance{Tag: format4.Tag{Provider: ti.id.provider, Source: ti.id.source,
		SourceURL: t.urls[cidIdent{cid, ti.id}], Batch: ti.id.batch, ContentKeyID: ti.id.ckey,
		ProducerPeer: ti.id.peer, ProducerPubkey: ti.id.pubkey}, At: ti.at}
}

func ptr[T any](v T) *T { return &v }

// objectKey is a REC's key outside EPOCH: the object rule's value.
func (r *frec) objectKey() string { return r.f.Key }

func (t *ftype) rec(r *frec, c *fcopy, hydrate bool, tag *inst, key string) format4.Rec {
	out := format4.Rec{Seq: r.seq, CID: r.cid, Producer: t.parts[c.pid-1].token, Peer: c.peer, TS: r.ts,
		Key: key, Sig: append([]byte(nil), c.sig...), Len: int64(len(r.d)), Tag: t.tagInstance(r.cid, tag)}
	if len(out.Sig) == 0 {
		out.Sig = nil
	}
	if r.f.Epoch != nil {
		out.Epoch, out.HasEpoch = *r.f.Epoch, true
	}
	if hydrate {
		out.Data = append([]byte(nil), r.d...)
	}
	return out
}

// Get returns each requested CID's copies (the lowest-pid copy unless
// allCopies), in request order, pid ascending. Tag columns are NULL.
func (f *Fake) Get(ctx context.Context, typ string, cids []string, allCopies, hydrate bool) ([]format4.Rec, error) {
	if err := f.begin(ctx, "GET"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("GET", typ)
	if err != nil {
		return nil, err
	}
	var out []format4.Rec
	for _, cid := range cids {
		if _, err := cidv1.Parse(cid); err != nil {
			return nil, statusErr("GET", format4.StatusArg, "CID "+cid)
		}
		seq, ok := t.byCID[cid]
		if !ok {
			continue
		}
		r := t.recs[seq]
		for i, pid := range r.pids() {
			if i > 0 && !allCopies {
				break
			}
			out = append(out, t.rec(r, r.copies[pid], hydrate, nil, r.objectKey()))
		}
	}
	f.stats[23]++
	return out, nil
}

// Tags returns one row per (cid, tag identity), merged over copies.
func (f *Fake) Tags(ctx context.Context, typ string, cids []string) ([]format4.TagRow, error) {
	if err := f.begin(ctx, "TAGS"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("TAGS", typ)
	if err != nil {
		return nil, err
	}
	var out []format4.TagRow
	for _, cid := range cids {
		seq, ok := t.byCID[cid]
		if !ok {
			continue
		}
		r := t.recs[seq]
		type merged struct {
			at  int64
			pid int
		}
		m := map[ident]*merged{}
		for _, pid := range r.pids() {
			for _, ti := range r.copies[pid].tags {
				if have := m[ti.id]; have == nil || ti.at < have.at {
					m[ti.id] = &merged{ti.at, pid}
				}
			}
		}
		ids := make([]ident, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := m[ids[i]], m[ids[j]]
			if a.at != b.at {
				return a.at < b.at
			}
			return ids[i].less(ids[j])
		})
		for _, id := range ids {
			ti := inst{id, m[id].at}
			out = append(out, format4.TagRow{CID: cid, Seq: seq, Producer: t.parts[m[id].pid-1].token,
				TagInstance: *t.tagInstance(cid, &ti)})
		}
	}
	return out, nil
}

// match is one record that passed a query's filters, with the copy that
// represents it and the matched tag.
type match struct {
	r   *frec
	c   *fcopy
	tag *inst
}

// filter returns the records matching q's type-level filters (cid, peer,
// producer, lane, seq range, predicates, search), each represented by its
// lowest-pid matching copy, in seq order.
func (f *Fake) filter(op string, t *ftype, q format4.Query, seqRange bool) ([]match, error) {
	for _, p := range q.Preds {
		if err := checkPred(p); err != nil {
			return nil, statusErr(op, format4.StatusArg, err.Error())
		}
	}
	var out []match
	through := t.through
	for _, seq := range t.sortedSeqs() {
		r := t.recs[seq]
		if q.CID != "" && r.cid != q.CID {
			continue
		}
		if seqRange {
			if q.SeqAfter != 0 && seq <= q.SeqAfter {
				continue
			}
			if seq > through || (q.SeqThrough != 0 && seq > q.SeqThrough) {
				continue
			}
		}
		if !predsHold(r, q.Preds) {
			continue
		}
		if q.Search != "" && !searchHolds(r.f.Text, q.Search) {
			continue
		}
		for _, pid := range r.pids() {
			c := r.copies[pid]
			if q.Peer != "" && c.peer != q.Peer {
				continue
			}
			if q.Producer != "" && t.parts[pid-1].token != q.Producer {
				continue
			}
			var tag *inst
			if laneFiltered(q.Lane) {
				tag = earliest(c.tags, func(ti inst) bool { return ti.id.matches(q.Lane) })
				if tag == nil {
					continue
				}
			} else {
				tag = earliest(c.tags, nil)
			}
			out = append(out, match{r, c, tag})
			break
		}
	}
	return out, nil
}

func page[T any](rows []T, offset, limit int64) []T {
	if offset > 0 {
		if offset >= int64(len(rows)) {
			return nil
		}
		rows = rows[offset:]
	}
	if limit > 0 && limit < int64(len(rows)) {
		rows = rows[:limit]
	}
	return rows
}

// Scan is SCAN: one row per seq, order 1 (asc, default) or 2 (desc).
func (f *Fake) Scan(ctx context.Context, q format4.Query) ([]format4.Rec, error) {
	if err := f.begin(ctx, "SCAN"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("SCAN", q.Type)
	if err != nil {
		return nil, err
	}
	ms, err := f.filter("SCAN", t, q, true)
	if err != nil {
		return nil, err
	}
	switch q.Order {
	case 0, format4.OrderSeqAsc:
	case format4.OrderSeqDesc:
		for i, j := 0, len(ms)-1; i < j; i, j = i+1, j-1 {
			ms[i], ms[j] = ms[j], ms[i]
		}
	default:
		return nil, statusErr("SCAN", format4.StatusArg, "order")
	}
	var out []format4.Rec
	for _, m := range page(ms, q.Offset, q.Limit) {
		out = append(out, t.rec(m.r, m.c, q.Hydrate, m.tag, m.r.objectKey()))
	}
	f.stats[23]++
	return out, nil
}

// windowOrder sorts matches in WINDOW order 3 (w desc, cid asc; the default)
// or 4 (cid asc).
func windowOrder(op string, ms []match, order format4.Order) error {
	switch order {
	case 0, format4.OrderWDesc:
		sort.SliceStable(ms, func(i, j int) bool {
			a, b := ms[i].r, ms[j].r
			if a.w() != b.w() {
				return a.w() > b.w()
			}
			return a.cid < b.cid
		})
	case format4.OrderCID:
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].r.cid < ms[j].r.cid })
	default:
		return statusErr(op, format4.StatusArg, "order")
	}
	return nil
}

// Head is HEAD: counts, heads and the snapshot; with a byte cap, the longest
// prefix in WINDOW order after the offset whose bytes fit.
func (f *Fake) Head(ctx context.Context, q format4.Query) (format4.Head, error) {
	var h format4.Head
	if err := f.begin(ctx, "HEAD"); err != nil {
		return h, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("HEAD", q.Type)
	if err != nil {
		return h, err
	}
	ms, err := f.filter("HEAD", t, q, true)
	if err != nil {
		return h, err
	}
	h.Through = t.through
	if q.ByteCap > 0 {
		if err := windowOrder("HEAD", ms, q.Order); err != nil {
			return h, err
		}
		ms = page(ms, q.Offset, q.Limit)
		var sum int64
		for i, m := range ms {
			if sum+int64(len(m.r.d)) > q.ByteCap {
				h.More = true
				ms = ms[:i]
				break
			}
			sum += int64(len(m.r.d))
		}
	}
	for _, m := range ms {
		h.N++
		h.Bytes += int64(len(m.r.d))
		h.MaxSeq = max(h.MaxSeq, m.r.seq)
		h.MaxTS = max(h.MaxTS, m.r.ts)
		for _, ti := range m.c.tags {
			if !laneFiltered(q.Lane) || ti.id.matches(q.Lane) {
				h.MaxAt = max(h.MaxAt, ti.at)
			}
		}
	}
	return h, nil
}

// Window is WINDOW: one row per record, order 3 or 4.
func (f *Fake) Window(ctx context.Context, q format4.Query) ([]format4.Rec, error) {
	if err := f.begin(ctx, "WINDOW"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("WINDOW", q.Type)
	if err != nil {
		return nil, err
	}
	ms, err := f.filter("WINDOW", t, q, false)
	if err != nil {
		return nil, err
	}
	if err := windowOrder("WINDOW", ms, q.Order); err != nil {
		return nil, err
	}
	var out []format4.Rec
	for _, m := range page(ms, q.Offset, q.Limit) {
		out = append(out, t.rec(m.r, m.c, q.Hydrate, m.tag, m.r.objectKey()))
	}
	return out, nil
}

// IndexPage is INDEX_PAGE: (c0, epoch, cid), epoch DESC NULLS LAST, cid ASC.
func (f *Fake) IndexPage(ctx context.Context, q format4.Query) ([]format4.IndexRow, error) {
	if err := f.begin(ctx, "INDEX_PAGE"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	t, err := f.typ("INDEX_PAGE", q.Type)
	if err != nil {
		return nil, err
	}
	ms, err := f.filter("INDEX_PAGE", t, q, false)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i].r.f.Epoch, ms[j].r.f.Epoch
		switch {
		case a != nil && b != nil && *a != *b:
			return *a > *b
		case (a == nil) != (b == nil):
			return a != nil
		}
		return ms[i].r.cid < ms[j].r.cid
	})
	var out []format4.IndexRow
	for _, m := range page(ms, q.Offset, q.Limit) {
		row := format4.IndexRow{CID: m.r.cid}
		if m.r.f.Col0 != nil {
			row.Col0 = ptr(*m.r.f.Col0)
		}
		if m.r.f.Epoch != nil {
			row.Epoch = ptr(*m.r.f.Epoch)
		}
		out = append(out, row)
	}
	return out, nil
}

// entityKey is format 1's epochEntityKeySQL for the type's epoch profile.
func entityKey(profile uint8, r *frec) string {
	norad := ""
	if r.f.Col0 != nil {
		norad = strconv.FormatInt(*r.f.Col0, 10)
	}
	entity := ""
	if r.f.Col1 != nil {
		entity = *r.f.Col1
	}
	if profile == 1 {
		switch {
		case norad != "":
			return norad
		case entity != "":
			return entity
		}
		return r.cid
	}
	switch {
	case entity != "":
		return entity
	case norad != "":
		return norad
	}
	return r.cid
}

func (f *Fake) epoch(op string, q format4.EpochQuery) (*ftype, []match, error) {
	t, err := f.typ(op, q.Type)
	if err != nil {
		return nil, nil, err
	}
	qq := q.Query
	qq.CID, qq.Peer, qq.Producer, qq.SeqAfter, qq.SeqThrough, qq.Search = "", "", "", 0, 0, ""
	ms, err := f.filter(op, t, qq, false)
	if err != nil {
		return nil, nil, err
	}
	keep := ms[:0]
	for _, m := range ms {
		if m.r.f.Epoch != nil {
			keep = append(keep, m)
		}
	}
	ms = keep
	switch q.Profile {
	case format4.EpochWindow, format4.EpochCoverage:
		sort.SliceStable(ms, func(i, j int) bool {
			a, b := *ms[i].r.f.Epoch, *ms[j].r.f.Epoch
			if a != b {
				return a < b
			}
			return ms[i].r.cid < ms[j].r.cid
		})
		return t, ms, nil
	case format4.EpochNearest, format4.EpochAsOf, format4.EpochForward:
	default:
		return nil, nil, statusErr(op, format4.StatusArg, "profile")
	}
	at := q.At
	best := map[string]match{}
	better := func(a, b *frec) bool { // a ranks before b
		ea, eb := *a.f.Epoch, *b.f.Epoch
		switch q.Profile {
		case format4.EpochAsOf:
			if ea != eb {
				return ea > eb
			}
		case format4.EpochForward:
			if ea != eb {
				return ea < eb
			}
		default:
			da, db := absDiff(ea, at), absDiff(eb, at)
			if da != db {
				return da < db
			}
			if (ea <= at) != (eb <= at) {
				return ea <= at
			}
			if ea != eb {
				return ea > eb
			}
		}
		return a.cid < b.cid
	}
	for _, m := range ms {
		e := *m.r.f.Epoch
		if (q.Profile == format4.EpochAsOf && e > at) || (q.Profile == format4.EpochForward && e < at) {
			continue
		}
		k := entityKey(t.spec.EpochProfile, m.r)
		if k == "" {
			continue
		}
		if have, ok := best[k]; !ok || better(m.r, have.r) {
			best[k] = m
		}
	}
	keys := make([]string, 0, len(best))
	for k, m := range best {
		if q.MaxDelta > 0 && absDiff(*m.r.f.Epoch, at) > q.MaxDelta {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]match, 0, len(keys))
	for _, k := range keys {
		out = append(out, best[k])
	}
	return t, out, nil
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// Epoch is EPOCH profiles 1-4.
func (f *Fake) Epoch(ctx context.Context, q format4.EpochQuery) ([]format4.Rec, error) {
	if err := f.begin(ctx, "EPOCH"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	if q.Profile == format4.EpochCoverage {
		return nil, statusErr("EPOCH", format4.StatusArg, "coverage is Coverage")
	}
	t, ms, err := f.epoch("EPOCH", q)
	if err != nil {
		return nil, err
	}
	var out []format4.Rec
	for _, m := range page(ms, 0, q.Limit) {
		key := m.r.objectKey()
		if q.Profile != format4.EpochWindow {
			key = entityKey(t.spec.EpochProfile, m.r)
		}
		out = append(out, t.rec(m.r, m.c, q.Hydrate, m.tag, key))
	}
	return out, nil
}

// EpochCount is EPOCH with count only: window rows, point entities.
func (f *Fake) EpochCount(ctx context.Context, q format4.EpochQuery) (int64, error) {
	if err := f.begin(ctx, "EPOCH"); err != nil {
		return 0, err
	}
	defer f.mu.Unlock()
	if q.Profile == format4.EpochCoverage {
		return 0, statusErr("EPOCH", format4.StatusArg, "coverage has no count")
	}
	_, ms, err := f.epoch("EPOCH", q)
	if err != nil {
		return 0, err
	}
	return int64(len(ms)), nil
}

// Coverage is EPOCH profile 5: per UTC day of e.
func (f *Fake) Coverage(ctx context.Context, q format4.EpochQuery) ([]format4.CoverageBucket, error) {
	if err := f.begin(ctx, "EPOCH"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	q.Profile = format4.EpochCoverage
	_, ms, err := f.epoch("EPOCH", q)
	if err != nil {
		return nil, err
	}
	var out []format4.CoverageBucket
	for _, m := range ms {
		e := *m.r.f.Epoch
		day := time.Unix(e, 0).UTC().Format("2006-01-02")
		if n := len(out); n > 0 && out[n-1].Day == day {
			b := &out[n-1]
			b.N++
			b.MinEpoch = min(b.MinEpoch, e)
			b.MaxEpoch = max(b.MaxEpoch, e)
			continue
		}
		out = append(out, format4.CoverageBucket{Day: day, N: 1, MinEpoch: e, MaxEpoch: e})
	}
	return out, nil
}

// Types is SUMMARY kind 1.
func (f *Fake) Types(ctx context.Context) ([]format4.TypeSummary, error) {
	if err := f.begin(ctx, "SUMMARY"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.TypeSummary
	for _, name := range f.typeNames() {
		t := f.types[name]
		s := format4.TypeSummary{Type: name, Through: t.through}
		first := true
		for _, r := range t.recs {
			s.Records++
			s.Copies += int64(len(r.copies))
			s.Bytes += int64(len(r.d))
			s.CopyBytes += int64(len(r.d) * len(r.copies))
			if r.f.Epoch != nil {
				e := *r.f.Epoch
				if s.MinEpoch == nil || e < *s.MinEpoch {
					s.MinEpoch = ptr(e)
				}
				if s.MaxEpoch == nil || e > *s.MaxEpoch {
					s.MaxEpoch = ptr(e)
				}
			}
			if first || r.ts < s.MinTS {
				s.MinTS = r.ts
			}
			s.MaxTS = max(s.MaxTS, r.ts)
			s.MaxSeq = max(s.MaxSeq, r.seq)
			first = false
		}
		out = append(out, s)
	}
	return out, nil
}

// Partitions is SUMMARY kind 2.
func (f *Fake) Partitions(ctx context.Context) ([]format4.PartitionSummary, error) {
	if err := f.begin(ctx, "SUMMARY"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.PartitionSummary
	for _, name := range f.typeNames() {
		t := f.types[name]
		for _, p := range t.parts {
			s := format4.PartitionSummary{Type: name, Producer: p.token, Peer: p.peer}
			files := map[int64]bool{}
			first := true
			for _, r := range t.recs {
				if r.copies[p.pid] == nil {
					continue
				}
				s.Records++
				s.Bytes += int64(len(r.d))
				if first || r.ts < s.MinTS {
					s.MinTS = r.ts
				}
				first = false
				s.MaxTS = max(s.MaxTS, r.ts)
				s.MaxSeq = max(s.MaxSeq, r.seq)
				files[r.tb] = true
			}
			s.Files = int64(len(files))
			out = append(out, s)
		}
	}
	return out, nil
}

// Lanes is SUMMARY kind 3: live lanes per (type, partition).
func (f *Fake) Lanes(ctx context.Context, typ string) ([]format4.Lane, error) {
	if err := f.begin(ctx, "SUMMARY"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.Lane
	for _, name := range f.typeNames() {
		if typ != "" && name != typ {
			continue
		}
		t := f.types[name]
		agg := map[laneKey]*format4.Lane{}
		var keys []laneKey
		for _, seq := range t.sortedSeqs() {
			r := t.recs[seq]
			for _, pid := range r.pids() {
				c := r.copies[pid]
				seen := map[ident]bool{}
				for _, ti := range c.tags {
					if seen[ti.id] {
						continue
					}
					seen[ti.id] = true
					k := laneKey{pid, ti.id}
					l := agg[k]
					if l == nil {
						lm := t.lanes[k]
						l = &format4.Lane{Type: name, Producer: t.parts[pid-1].token,
							Tag: format4.Tag{Provider: ti.id.provider, Source: ti.id.source, Batch: ti.id.batch,
								ContentKeyID: ti.id.ckey, ProducerPeer: ti.id.peer, ProducerPubkey: ti.id.pubkey},
							First: ti.at, MinW: r.w(), MaxW: r.w()}
						if lm != nil {
							l.SourceURL, l.Updated = lm.url, lm.updated
						}
						agg[k] = l
						keys = append(keys, k)
					}
					l.Records++
					l.Bytes += int64(len(r.d))
					l.MaxSeq = max(l.MaxSeq, r.seq)
					l.First = min(l.First, ti.at)
					l.MinW = min(l.MinW, r.w())
					l.MaxW = max(l.MaxW, r.w())
				}
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].pid != keys[j].pid {
				return keys[i].pid < keys[j].pid
			}
			return keys[i].id.less(keys[j].id)
		})
		for _, k := range keys {
			out = append(out, *agg[k])
		}
	}
	return out, nil
}

// Disk is SUMMARY kind 4: the Fake has files but no bytes on disk.
func (f *Fake) Disk(ctx context.Context) ([]format4.DiskSummary, error) {
	if err := f.begin(ctx, "SUMMARY"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.DiskSummary
	for _, name := range f.typeNames() {
		out = append(out, format4.DiskSummary{Type: name, Files: int64(len(f.types[name].fileCopies()))})
	}
	return out, nil
}

// FTS is SUMMARY kind 5: the Fake's text index is always caught up.
func (f *Fake) FTS(ctx context.Context) ([]format4.FTSState, error) {
	if err := f.begin(ctx, "SUMMARY"); err != nil {
		return nil, err
	}
	defer f.mu.Unlock()
	var out []format4.FTSState
	for _, name := range f.typeNames() {
		t := f.types[name]
		s := format4.FTSState{Type: name, State: "off"}
		if t.spec.FullText {
			s.State, s.Through = "ready", t.through
		}
		out = append(out, s)
	}
	return out, nil
}

// SQL is not run by the Fake.
func (f *Fake) SQL(ctx context.Context, req format4.SQLRequest, sink func([]byte) error) (format4.SQLStats, error) {
	return format4.SQLStats{}, statusErr("SQL", format4.StatusUnsupported, "the Fake runs no SQL")
}

// Surface is not served by the Fake.
func (f *Fake) Surface(ctx context.Context) ([]format4.Relation, error) {
	return nil, statusErr("SURFACE", format4.StatusUnsupported, "the Fake runs no SQL")
}

// ---- predicates -------------------------------------------------------------

func checkPred(p format4.Pred) error {
	switch p.Field {
	case format4.FieldEpoch, format4.FieldTS, format4.FieldW, format4.FieldEpochDay,
		format4.FieldCol0, format4.FieldCol1, format4.FieldCol2, format4.FieldCol3:
	default:
		return fmt.Errorf("predicate field %d", p.Field)
	}
	n := len(p.Values)
	switch p.Op {
	case format4.OpEq, format4.OpNe, format4.OpLt, format4.OpLe, format4.OpGt, format4.OpGe, format4.OpLike:
		if n != 1 {
			return fmt.Errorf("predicate op %d takes 1 value, got %d", p.Op, n)
		}
	case format4.OpBetween:
		if n != 2 {
			return fmt.Errorf("BETWEEN takes 2 values, got %d", n)
		}
	case format4.OpIn:
		if n < 1 {
			return errors.New("IN takes at least 1 value")
		}
	case format4.OpNotNull:
		if n != 0 {
			return errors.New("NOTNULL takes no value")
		}
	default:
		return fmt.Errorf("predicate op %d", p.Op)
	}
	return nil
}

// fieldValue is a record's value of a predicate field; ok is false for NULL.
func fieldValue(r *frec, fld format4.Field) (format2.Cell, bool) {
	switch fld {
	case format4.FieldEpoch:
		if r.f.Epoch != nil {
			return format2.Int(*r.f.Epoch), true
		}
	case format4.FieldTS:
		return format2.Int(r.ts), true
	case format4.FieldW:
		return format2.Int(r.w()), true
	case format4.FieldEpochDay:
		if r.f.Epoch != nil {
			return format2.Text(time.Unix(*r.f.Epoch, 0).UTC().Format("2006-01-02")), true
		}
	case format4.FieldCol0:
		if r.f.Col0 != nil {
			return format2.Int(*r.f.Col0), true
		}
	case format4.FieldCol1, format4.FieldCol2, format4.FieldCol3:
		s := []*string{r.f.Col1, r.f.Col2, r.f.Col3}[fld-format4.FieldCol1]
		if s != nil {
			return format2.Text(*s), true
		}
	}
	return format2.Cell{}, false
}

// compare orders a field value against a predicate value with SQLite's
// affinity rules for the field: numeric fields compare numerically (a text
// value that is not a number never matches), text fields as text (BINARY).
// ok is false when the comparison is NULL.
func compare(v, c format2.Cell) (int, bool) {
	if c.Type == format2.CellNull {
		return 0, false
	}
	if v.Type == format2.CellInt {
		var x float64
		switch c.Type {
		case format2.CellInt:
			switch {
			case v.I < c.I:
				return -1, true
			case v.I > c.I:
				return 1, true
			}
			return 0, true
		case format2.CellReal:
			x = c.F
		case format2.CellText:
			p, err := strconv.ParseFloat(strings.TrimSpace(string(c.B)), 64)
			if err != nil {
				return -1, true // INTEGER < TEXT in SQLite's order
			}
			x = p
		default:
			return -1, true
		}
		fv := float64(v.I)
		switch {
		case fv < x:
			return -1, true
		case fv > x:
			return 1, true
		}
		return 0, true
	}
	var s string
	switch c.Type {
	case format2.CellText, format2.CellBlob:
		s = string(c.B)
	case format2.CellInt:
		s = strconv.FormatInt(c.I, 10)
	case format2.CellReal:
		s = strconv.FormatFloat(c.F, 'g', -1, 64)
	}
	return strings.Compare(string(v.B), s), true
}

func predsHold(r *frec, preds []format4.Pred) bool {
	for _, p := range preds {
		v, ok := fieldValue(r, p.Field)
		if !ok {
			return false
		}
		if p.Op == format4.OpNotNull {
			continue
		}
		hold := false
		switch p.Op {
		case format4.OpBetween:
			lo, ok1 := compare(v, p.Values[0])
			hi, ok2 := compare(v, p.Values[1])
			hold = ok1 && ok2 && lo >= 0 && hi <= 0
		case format4.OpIn:
			for _, c := range p.Values {
				if d, ok := compare(v, c); ok && d == 0 {
					hold = true
					break
				}
			}
		case format4.OpLike:
			hold = like(cellText(v), cellText(p.Values[0]))
		default:
			d, ok := compare(v, p.Values[0])
			if !ok {
				return false
			}
			switch p.Op {
			case format4.OpEq:
				hold = d == 0
			case format4.OpNe:
				hold = d != 0
			case format4.OpLt:
				hold = d < 0
			case format4.OpLe:
				hold = d <= 0
			case format4.OpGt:
				hold = d > 0
			case format4.OpGe:
				hold = d >= 0
			}
		}
		if !hold {
			return false
		}
	}
	return true
}

func cellText(c format2.Cell) string {
	switch c.Type {
	case format2.CellInt:
		return strconv.FormatInt(c.I, 10)
	case format2.CellReal:
		return strconv.FormatFloat(c.F, 'g', -1, 64)
	}
	return string(c.B)
}

// like is SQLite's default LIKE: % and _ wildcards, ASCII case-insensitive,
// no escape character.
func like(s, pattern string) bool {
	s, pattern = strings.ToLower(s), strings.ToLower(pattern)
	var rec func(si, pi int) bool
	rec = func(si, pi int) bool {
		for pi < len(pattern) {
			switch pattern[pi] {
			case '%':
				for k := si; k <= len(s); k++ {
					if rec(k, pi+1) {
						return true
					}
				}
				return false
			case '_':
				if si >= len(s) {
					return false
				}
				si++
				pi++
			default:
				if si >= len(s) || s[si] != pattern[pi] {
					return false
				}
				si++
				pi++
			}
		}
		return si == len(s)
	}
	return rec(0, 0)
}

// searchHolds is a small stand-in for FTS5: every bare term of the
// expression (quotes, operators and prefix stars dropped) occurs in the text,
// case-insensitively.
func searchHolds(text, expr string) bool {
	text = strings.ToLower(text)
	for _, term := range strings.FieldsFunc(strings.ToLower(expr), func(r rune) bool {
		return r == ' ' || r == '"' || r == '(' || r == ')' || r == '*'
	}) {
		if term == "and" || term == "or" || term == "not" {
			continue
		}
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
