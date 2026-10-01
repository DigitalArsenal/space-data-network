package format4_test

// An engine double: the ENGINE side of the mailbox (contract §3.4), written
// independently of the client, over Go memory, executing every op (§3.5)
// against format4test.Fake and answering in RB1 (§3.6). The real Engine
// client runs against it unchanged, so every op round-trips through the real
// request encoders, slots, queues, doorbells, rings and RB1 decoders.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/format4test"
)

// ---- Go shared memory ------------------------------------------------------

type goMem struct{ b []byte }

func newGoMem(n int) *goMem {
	words := make([]uint64, (n+7)/8)
	return &goMem{b: unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)}
}

func (m *goMem) p32(off uint32) *uint32 { return (*uint32)(unsafe.Pointer(&m.b[off])) }
func (m *goMem) p64(off uint32) *uint64 { return (*uint64)(unsafe.Pointer(&m.b[off])) }

func (m *goMem) Load32(off uint32) uint32          { return atomic.LoadUint32(m.p32(off)) }
func (m *goMem) Store32(off uint32, v uint32)      { atomic.StoreUint32(m.p32(off), v) }
func (m *goMem) Add32(off uint32, d uint32) uint32 { return atomic.AddUint32(m.p32(off), d) }
func (m *goMem) CAS32(off uint32, old, new uint32) bool {
	return atomic.CompareAndSwapUint32(m.p32(off), old, new)
}
func (m *goMem) Load64(off uint32) uint64     { return atomic.LoadUint64(m.p64(off)) }
func (m *goMem) Store64(off uint32, v uint64) { atomic.StoreUint64(m.p64(off), v) }
func (m *goMem) CAS64(off uint32, old, new uint64) bool {
	return atomic.CompareAndSwapUint64(m.p64(off), old, new)
}
func (m *goMem) ReadInto(off uint32, dst []byte)   { copy(dst, m.b[off:]) }
func (m *goMem) WriteBytes(off uint32, src []byte) { copy(m.b[off:], src) }

// ---- the double ---------------------------------------------------------------

type doubleCfg struct {
	slots    [2]uint32 // write, read
	reqBytes [2]uint32
	ring     [2]uint32
	threads  []uint32 // class per service thread
}

func defaultDoubleCfg() doubleCfg {
	return doubleCfg{slots: [2]uint32{4, 8}, reqBytes: [2]uint32{1 << 20, 64 << 10}, ring: [2]uint32{4096, 4096},
		threads: []uint32{1, 1, 2, 2, 3, 4, 5}}
}

type double struct {
	t      testing.TB
	cfg    doubleCfg
	mem    *goMem
	fake   *format4test.Fake
	layout []byte

	slotBase, stride [2]uint32
	cells, enq, deq  [4]uint32
	mask             [4]uint32
	stopWord         uint32
	doorbell         []uint32

	wake     []chan struct{}
	stopping chan struct{}
	stopOnce sync.Once
	stopped  atomic.Bool
	wg       sync.WaitGroup

	busyWrites atomic.Int32 // the next N write requests end P4_E_BUSY, nothing done
	slowReads  atomic.Int64 // ns each read request sleeps before it starts (cancel tests)
	ops        atomic.Int64
	puts       atomic.Int64
	onRequest  func(op uint32, req []byte) // set before the first request
}

const layoutAt = 0

func newDouble(t testing.TB, cfg doubleCfg) *double {
	d := &double{t: t, cfg: cfg, fake: format4test.New(), stopping: make(chan struct{})}
	at := uint32(1024)
	alloc := func(n, align uint32) uint32 {
		at = (at + align - 1) / align * align
		p := at
		at += n
		return p
	}
	d.stopWord = alloc(8, 8)
	for range cfg.threads {
		d.doorbell = append(d.doorbell, alloc(8, 8))
		d.wake = append(d.wake, make(chan struct{}, 1))
	}
	total := cfg.slots[0] + cfg.slots[1]
	qcap := uint32(1)
	for qcap < total*2 {
		qcap <<= 1
	}
	for q := 0; q < 4; q++ {
		d.enq[q], d.deq[q] = alloc(8, 64), alloc(8, 64)
		d.cells[q] = alloc(qcap*16, 64)
		d.mask[q] = qcap - 1
	}
	for p := 0; p < 2; p++ {
		d.stride[p] = (448 + cfg.reqBytes[p] + cfg.ring[p] + 63) / 64 * 64
		d.slotBase[p] = alloc(d.stride[p]*cfg.slots[p], 64)
	}
	d.mem = newGoMem(int(at) + 64)
	for q := 0; q < 4; q++ {
		for k := uint32(0); k <= d.mask[q]; k++ {
			d.mem.Store64(d.cells[q]+16*k, uint64(k))
		}
	}
	l := make([]byte, 640)
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(l[off:], v) }
	put(0, 1)
	put(4, 640)
	put(8, 448)
	put(12, d.stopWord)
	for p := 0; p < 2; p++ {
		put(16+4*p, cfg.slots[p])
		put(24+4*p, d.slotBase[p])
		put(32+4*p, d.stride[p])
		put(40+4*p, cfg.reqBytes[p])
		put(48+4*p, cfg.ring[p])
	}
	for q := 0; q < 4; q++ {
		put(56+4*q, d.cells[q])
		put(72+4*q, d.mask[q])
		put(88+4*q, d.enq[q])
		put(104+4*q, d.deq[q])
	}
	put(120, uint32(len(cfg.threads)))
	for i, c := range cfg.threads {
		put(128+4*i, c)
		put(384+4*i, d.doorbell[i])
	}
	d.layout = l
	for i, c := range cfg.threads {
		if c >= 1 && c <= 4 {
			d.wg.Add(1)
			go d.serve(i, c)
		}
	}
	t.Cleanup(func() { d.stop() })
	return d
}

// engine opens the real client over the double.
func (d *double) engine(t testing.TB) *format4.Engine {
	e, err := format4.AttachForTest(d.mem, d, d, d.layout)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (d *double) stop() {
	d.stopOnce.Do(func() {
		d.mem.Store32(d.stopWord, 1)
		d.stopped.Store(true)
		close(d.stopping)
		d.wg.Wait()
	})
}

// host
func (d *double) Enter() bool { return !d.stopped.Load() }
func (d *double) Exit()       {}
func (d *double) Wake(i int) error {
	if i < 0 || i >= len(d.wake) {
		return fmt.Errorf("thread %d", i)
	}
	d.mem.Add32(d.doorbell[i], 1)
	select {
	case d.wake[i] <- struct{}{}:
	default:
	}
	return nil
}
func (d *double) Stopping() <-chan struct{} { return d.stopping }

// control
func (d *double) ControlBytes(export string, in []byte) (int32, error) {
	if export != "flatsql_p4_register_type" {
		return 0, fmt.Errorf("unknown export %s", export)
	}
	spec, err := decodeSpec(in)
	if err != nil {
		return format4.StatusArg, nil
	}
	return statusOfErr(d.fake.RegisterType(spec)), nil
}

func (d *double) ControlCall(export string, args ...interface{}) (int32, error) {
	switch export {
	case "flatsql_p4_set_quota":
		return statusOfErr(d.fake.SetQuota(int64(args[0].(float64)))), nil
	case "flatsql_p4_activate":
		return statusOfErr(d.fake.Activate(context.Background())), nil
	}
	return 0, fmt.Errorf("unknown export %s", export)
}

func (d *double) ControlOut(export string, capacity int) ([]byte, int32, error) {
	if export != "flatsql_p4_stats" {
		return nil, 0, fmt.Errorf("unknown export %s", export)
	}
	st, err := d.fake.Stats()
	if err != nil {
		return nil, statusOfErr(err), nil
	}
	var b []byte
	for _, v := range st {
		b = binary.LittleEndian.AppendUint64(b, v)
	}
	return b, int32(len(b)), nil
}

func (d *double) StopWithin(time.Duration) (int32, error) {
	d.stop()
	return 0, nil
}

func statusOfErr(err error) int32 {
	if err == nil {
		return 0
	}
	var se *format4.StatusError
	if errors.As(err, &se) {
		return se.Status
	}
	return format4.StatusInternal
}

// ---- service threads -------------------------------------------------------------

func (d *double) dequeue(q int) (uint32, bool) {
	pos := d.mem.Load64(d.deq[q])
	for {
		cell := d.cells[q] + uint32(pos&uint64(d.mask[q]))*16
		seq := d.mem.Load64(cell)
		switch dif := int64(seq) - int64(pos+1); {
		case dif == 0:
			if d.mem.CAS64(d.deq[q], pos, pos+1) {
				v := d.mem.Load32(cell + 8)
				d.mem.Store64(cell, pos+uint64(d.mask[q])+1)
				return v, true
			}
			pos = d.mem.Load64(d.deq[q])
		case dif < 0:
			return 0, false
		default:
			pos = d.mem.Load64(d.deq[q])
		}
	}
}

func (d *double) serve(thread int, class uint32) {
	defer d.wg.Done()
	state := d.doorbell[thread] + 4
	for d.mem.Load32(d.stopWord) == 0 {
		slot, ok := d.dequeue(int(class) - 1)
		if !ok {
			d.mem.Store32(state, 0)
			select {
			case <-d.wake[thread]:
			case <-d.stopping:
			case <-time.After(time.Millisecond):
			}
			continue
		}
		d.mem.Store32(state, 1)
		d.run(thread, slot)
	}
	d.mem.Store32(state, 2)
}

func (d *double) hdr(slot uint32) (uint32, int) {
	if slot < d.cfg.slots[0] {
		return d.slotBase[0] + slot*d.stride[0], 0
	}
	return d.slotBase[1] + (slot-d.cfg.slots[0])*d.stride[1], 1
}

// response is one request's answer.
type response struct {
	names  []string
	rows   [][]format2.Cell
	status int32
	err    string
}

func (d *double) run(thread int, slot uint32) {
	h, pool := d.hdr(slot)
	if !d.mem.CAS32(h, 2, 3) { // QUEUED -> RUNNING
		d.t.Errorf("slot %d dequeued in state %d", slot, d.mem.Load32(h))
		return
	}
	d.mem.Store32(h+400, uint32(thread))
	d.mem.Store64(h+128, uint64(time.Now().UnixNano()))
	op, class := d.mem.Load32(h+16), d.mem.Load32(h+20)
	req := make([]byte, d.mem.Load32(h+28))
	d.mem.ReadInto(h+448, req)
	d.ops.Add(1)
	if d.onRequest != nil {
		d.onRequest(op, req)
	}
	var res response
	switch {
	case d.mem.Load32(h+4) != 0:
		res = response{names: nil, status: format4.StatusCancelled}
	case (class == 1) != (pool == 0):
		res = response{status: format4.StatusArg, err: "class and pool disagree"}
	case class == 1 && d.busyWrites.Add(-1) >= 0:
		res = response{status: format4.StatusBusy}
	default:
		if class != 1 {
			if ns := d.slowReads.Load(); ns > 0 {
				time.Sleep(time.Duration(ns))
			}
		}
		res = d.exec(op, req)
	}
	ring := h + 448 + d.cfg.reqBytes[pool]
	status := res.status
	if !d.emit(h, ring, uint64(d.cfg.ring[pool]), encodeRB1(res)) && status == 0 {
		status = format4.StatusCancelled
	}
	d.mem.Store32(h+88, uint32(status))
	if b := []byte(res.err); len(b) > 0 {
		if len(b) > 256 {
			b = b[:256]
		}
		d.mem.WriteBytes(h+144, b)
		d.mem.Store32(h+92, uint32(len(b)))
	}
	d.mem.Store64(h+96, uint64(len(res.rows)))
	d.mem.Store64(h+136, uint64(time.Now().UnixNano()))
	d.mem.Store32(h, 5) // DONE
	d.mem.Add32(h+8, 1)
}

// emit streams b through the slot's ring, waiting for space; false when the
// request was cancelled or the double stopped mid-stream.
func (d *double) emit(h, ring uint32, capacity uint64, b []byte) bool {
	for len(b) > 0 {
		head, tail := d.mem.Load64(h+72), d.mem.Load64(h+80)
		free := capacity - (tail - head)
		if free == 0 {
			if d.mem.Load32(h+4) != 0 || d.mem.Load32(d.stopWord) != 0 {
				return false
			}
			select {
			case <-d.stopping:
				return false
			case <-time.After(50 * time.Microsecond):
			}
			continue
		}
		n := min(free, uint64(len(b)))
		at := tail & (capacity - 1)
		first := min(capacity-at, n)
		d.mem.WriteBytes(ring+uint32(at), b[:first])
		if n > first {
			d.mem.WriteBytes(ring, b[first:n])
		}
		d.mem.Store64(h+80, tail+n)
		d.mem.Add32(h+8, 1)
		b = b[n:]
	}
	return true
}

// ---- RB1 ----------------------------------------------------------------------------

func encodeRB1(r response) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 0x48314252)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(r.names)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	for _, n := range r.names {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(n)))
		b = append(b, n...)
	}
	if len(r.rows) > 0 {
		var body []byte
		for _, row := range r.rows {
			for _, c := range row {
				body = append(body, format2.EncodeParams([]format2.Cell{c})[4:]...)
			}
		}
		b = binary.LittleEndian.AppendUint32(b, 0x42314252)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(r.rows)))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(body)))
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = append(b, body...)
	}
	b = binary.LittleEndian.AppendUint32(b, 0x45314252)
	b = binary.LittleEndian.AppendUint32(b, uint32(r.status))
	b = binary.LittleEndian.AppendUint64(b, uint64(len(r.rows)))
	b = binary.LittleEndian.AppendUint64(b, 0)
	return binary.LittleEndian.AppendUint64(b, 0)
}

// ---- requests --------------------------------------------------------------------------

type tlvs map[uint16][][]byte

func parseTLV(b []byte) (tlvs, error) {
	out := tlvs{}
	for len(b) > 0 {
		if len(b) < 6 {
			return nil, errors.New("truncated TLV")
		}
		tag, n := binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])
		if uint64(len(b)-6) < uint64(n) {
			return nil, errors.New("TLV overruns")
		}
		out[tag] = append(out[tag], b[6:6+n])
		b = b[6+n:]
	}
	return out, nil
}

func (t tlvs) text(tag uint16) string {
	if v := t[tag]; len(v) > 0 {
		return string(v[0])
	}
	return ""
}

func (t tlvs) u8(tag uint16) uint8 {
	if v := t[tag]; len(v) > 0 && len(v[0]) == 1 {
		return v[0][0]
	}
	return 0
}

func (t tlvs) u32(tag uint16) uint32 {
	if v := t[tag]; len(v) > 0 && len(v[0]) == 4 {
		return binary.LittleEndian.Uint32(v[0])
	}
	return 0
}

func (t tlvs) i64(tag uint16) int64 {
	if v := t[tag]; len(v) > 0 && len(v[0]) == 8 {
		return int64(binary.LittleEndian.Uint64(v[0]))
	}
	return 0
}

func cidText(b []byte) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	out := []byte{'b'}
	var acc uint64
	bits := 0
	for _, c := range b {
		acc = acc<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, alphabet[(acc>>uint(bits))&31])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(acc<<uint(5-bits))&31])
	}
	return string(out)
}

func (t tlvs) cids() ([]string, error) {
	v := t[40]
	if len(v) == 0 {
		return nil, errors.New("no CIDs")
	}
	b := v[0]
	if len(b) < 4 || len(b) != 4+36*int(binary.LittleEndian.Uint32(b)) {
		return nil, errors.New("malformed CID list")
	}
	var out []string
	for i := 4; i < len(b); i += 36 {
		out = append(out, cidText(b[i:i+36]))
	}
	return out, nil
}

func decodeCells(b []byte, n int) ([]format2.Cell, []byte, error) {
	var out []format2.Cell
	for i := 0; i < n; i++ {
		if len(b) < 1 {
			return nil, nil, errors.New("truncated cell")
		}
		c := format2.Cell{Type: format2.CellType(b[0])}
		switch c.Type {
		case format2.CellNull:
			b = b[1:]
		case format2.CellInt, format2.CellReal:
			if len(b) < 9 {
				return nil, nil, errors.New("truncated number")
			}
			v := binary.LittleEndian.Uint64(b[1:])
			if c.Type == format2.CellInt {
				c.I = int64(v)
			} else {
				c.F = math.Float64frombits(v)
			}
			b = b[9:]
		case format2.CellText, format2.CellBlob:
			if len(b) < 5 || len(b)-5 < int(binary.LittleEndian.Uint32(b[1:])) {
				return nil, nil, errors.New("truncated bytes")
			}
			m := int(binary.LittleEndian.Uint32(b[1:]))
			c.B = append([]byte(nil), b[5:5+m]...)
			b = b[5+m:]
		default:
			return nil, nil, fmt.Errorf("cell type %d", c.Type)
		}
		out = append(out, c)
	}
	return out, b, nil
}

func (t tlvs) query() (format4.Query, error) {
	q := format4.Query{Type: t.text(1), Hydrate: t.u8(2) != 0, Limit: t.i64(3), Offset: t.i64(4),
		Order: format4.Order(t.u8(5)), SeqAfter: t.i64(6), SeqThrough: t.i64(7), Peer: t.text(9), Producer: t.text(10),
		Lane: format4.LaneFilter{Provider: t.text(11), Source: t.text(12), Batch: t.text(13), ContentKeyID: t.text(14),
			ProducerPeer: t.text(15), ProducerPubkey: t.text(16)},
		Search: t.text(18), ByteCap: t.i64(19)}
	if v := t[8]; len(v) > 0 {
		q.CID = cidText(v[0])
	}
	for _, p := range t[17] {
		if len(p) < 4 {
			return q, errors.New("short predicate")
		}
		vals, rest, err := decodeCells(p[4:], int(binary.LittleEndian.Uint16(p[2:])))
		if err != nil || len(rest) != 0 {
			return q, fmt.Errorf("predicate cells: %v", err)
		}
		q.Preds = append(q.Preds, format4.Pred{Field: format4.Field(p[0]), Op: format4.Op(p[1]), Values: vals})
	}
	return q, nil
}

func decodeSpec(b []byte) (format4.TypeSpec, error) {
	t, err := parseTLV(b)
	if err != nil {
		return format4.TypeSpec{}, err
	}
	var s format4.TypeSpec
	s.SchemaName, s.BFBS, s.Rules = t.text(1), t[3][0], t.text(4)
	copy(s.FID[:], t[2][0])
	s.MaxFrame, s.RingCap, s.Flags = uint64(t.i64(5)), uint64(t.i64(6)), t.u32(7)
	s.PageSize, s.Identity, s.A18Bound = t.u32(8), t.u8(9) != 0, t.u32(10)
	s.EpochProfile, s.FullText = t.u8(11), t.u8(12) != 0
	return s, nil
}

func decodePut(t tlvs) (format4.Batch, error) {
	b := format4.Batch{Type: t.text(1), Peer: t.text(50), Mode: format4.Mode(t.u8(52)), At: t.i64(54)}
	for _, raw := range t[51] {
		tt, err := parseTLV(raw)
		if err != nil {
			return b, err
		}
		b.Tags = append(b.Tags, format4.Tag{Provider: tt.text(1), Source: tt.text(2), SourceURL: tt.text(3), Batch: tt.text(4),
			ContentKeyID: tt.text(5), ProducerPeer: tt.text(6), ProducerPubkey: tt.text(7)})
	}
	if len(t[53]) == 0 {
		return b, errors.New("no records")
	}
	r := t[53][0]
	n := int(binary.LittleEndian.Uint32(r))
	r = r[4:]
	take := func(k int) ([]byte, error) {
		if len(r) < k {
			return nil, errors.New("truncated record")
		}
		v := r[:k]
		r = r[k:]
		return v, nil
	}
	for i := 0; i < n; i++ {
		var in format4.In
		h, err := take(4 + 36 + 8 + 4)
		if err != nil {
			return b, err
		}
		flags := binary.LittleEndian.Uint16(h)
		if flags&^uint16(31) != 0 {
			return b, errors.New("unknown record flags")
		}
		in.CID = cidText(h[4:40])
		in.TS = int64(binary.LittleEndian.Uint64(h[40:]))
		frame, err := take(int(binary.LittleEndian.Uint32(h[48:])))
		if err != nil || len(frame) < 4 || int(binary.LittleEndian.Uint32(frame)) != len(frame)-4 {
			return b, errors.New("bad frame")
		}
		in.Plain = append([]byte(nil), frame[4:]...)
		if flags&1 != 0 {
			l, err := take(4)
			if err != nil {
				return b, err
			}
			s, err := take(int(binary.LittleEndian.Uint32(l)))
			if err != nil {
				return b, err
			}
			in.Sealed = append([]byte{}, s...)
		}
		l, err := take(2)
		if err != nil {
			return b, err
		}
		sig, err := take(int(binary.LittleEndian.Uint16(l)))
		if err != nil {
			return b, err
		}
		if len(sig) > 0 {
			in.Sig = append([]byte(nil), sig...)
		}
		if flags&2 != 0 {
			id, err := take(32)
			if err != nil {
				return b, err
			}
			in.Ident = new([32]byte)
			copy(in.Ident[:], id)
		}
		if flags&4 != 0 {
			s, err := take(8)
			if err != nil {
				return b, err
			}
			in.Seq = int64(binary.LittleEndian.Uint64(s))
		}
		if flags&8 != 0 {
			l, err := take(2)
			if err != nil {
				return b, err
			}
			p, err := take(int(binary.LittleEndian.Uint16(l)))
			if err != nil {
				return b, err
			}
			in.Peer = string(p)
		}
		if flags&16 != 0 {
			l, err := take(2)
			if err != nil {
				return b, err
			}
			for k := 0; k < int(binary.LittleEndian.Uint16(l)); k++ {
				ta, err := take(10)
				if err != nil {
					return b, err
				}
				in.Tags = append(in.Tags, format4.TagAt{Tag: int(binary.LittleEndian.Uint16(ta)), At: int64(binary.LittleEndian.Uint64(ta[2:]))})
			}
		}
		b.Records = append(b.Records, in)
	}
	if len(r) != 0 {
		return b, errors.New("bytes after the records")
	}
	return b, nil
}

// ---- execution ----------------------------------------------------------------------------

var (
	recCols = []string{"seq", "cid", "producer", "peer", "ts", "epoch", "key", "sig", "data", "len", "provider", "source",
		"source_url", "batch", "content_key_id", "producer_peer", "producer_pubkey", "at"}
)

func i64(v int64) format2.Cell { return format2.Int(v) }

func optI64(p *int64) format2.Cell {
	if p == nil {
		return format2.Null()
	}
	return format2.Int(*p)
}

func blob(b []byte) format2.Cell {
	if b == nil {
		return format2.Null()
	}
	return format2.Blob(b)
}

func recRow(r format4.Rec) []format2.Cell {
	row := []format2.Cell{i64(r.Seq), format2.Text(r.CID), format2.Text(r.Producer), format2.Text(r.Peer), i64(r.TS),
		format2.Null(), format2.Null(), blob(r.Sig), blob(r.Data), i64(r.Len)}
	if r.HasEpoch {
		row[5] = i64(r.Epoch)
	}
	if r.Key != "" {
		row[6] = format2.Text(r.Key)
	}
	if r.Tag == nil {
		for range 8 {
			row = append(row, format2.Null())
		}
		return row
	}
	tg := r.Tag
	for _, s := range []string{tg.Provider, tg.Source, tg.SourceURL, tg.Batch, tg.ContentKeyID, tg.ProducerPeer, tg.ProducerPubkey} {
		row = append(row, format2.Text(s))
	}
	return append(row, i64(tg.At))
}

func recRows(recs []format4.Rec) [][]format2.Cell {
	out := make([][]format2.Cell, 0, len(recs))
	for _, r := range recs {
		out = append(out, recRow(r))
	}
	return out
}

func fail(names []string, err error) response {
	r := response{names: names, status: statusOfErr(err)}
	if err != nil {
		r.err = err.Error()
	}
	return r
}

func (d *double) exec(op uint32, req []byte) response {
	ctx := context.Background()
	t, err := parseTLV(req)
	if err != nil {
		return response{status: format4.StatusArg, err: err.Error()}
	}
	f := d.fake
	switch op {
	case 1:
		names := []string{"i", "action", "seq", "reject"}
		b, err := decodePut(t)
		if err != nil {
			return response{names: names, status: format4.StatusArg, err: err.Error()}
		}
		d.puts.Add(1)
		out, err := f.Put(ctx, b)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for i, o := range out {
			r.rows = append(r.rows, []format2.Cell{i64(int64(i)), i64(int64(o.Action)), i64(o.Seq), i64(int64(o.Reject))})
		}
		return r
	case 2:
		names := []string{"tags_deleted", "records_deleted", "files_deleted"}
		res, err := f.Supersede(ctx, t.text(1), t.text(11), t.text(12), t.text(60), t.u8(61) != 0)
		if err != nil {
			return fail(names, err)
		}
		return response{names: names, rows: [][]format2.Cell{{i64(res.TagsDeleted), i64(res.RecordsDeleted), i64(res.FilesDeleted)}}}
	case 3:
		names := []string{"deleted"}
		cids, err := t.cids()
		if err != nil {
			return response{names: names, status: format4.StatusArg, err: err.Error()}
		}
		n, err := f.Delete(ctx, t.text(1), cids)
		if err != nil {
			return fail(names, err)
		}
		return response{names: names, rows: [][]format2.Cell{{i64(n)}}}
	case 4:
		names := []string{"files_dropped", "records_dropped", "bytes_freed"}
		res, err := f.QuotaGC(ctx, t.i64(62))
		if err != nil {
			return fail(names, err)
		}
		return response{names: names, rows: [][]format2.Cell{{i64(res.FilesDropped), i64(res.RecordsDropped), i64(res.BytesFreed)}}}
	case 5:
		names := []string{"type", "entries", "mismatches"}
		rows, err := f.Rebuild(ctx, t.text(1), format4.RebuildWhat(t.u32(63)))
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), i64(x.Entries), i64(x.Mismatches)})
		}
		return r
	case 10:
		cids, err := t.cids()
		if err != nil {
			return response{names: recCols, status: format4.StatusArg, err: err.Error()}
		}
		recs, err := f.Get(ctx, t.text(1), cids, t.u8(41) != 0, t.u8(2) != 0)
		if err != nil {
			return fail(recCols, err)
		}
		return response{names: recCols, rows: recRows(recs)}
	case 11:
		names := []string{"cid", "seq", "producer", "provider", "source", "source_url", "batch", "content_key_id",
			"producer_peer", "producer_pubkey", "at"}
		cids, err := t.cids()
		if err != nil {
			return response{names: names, status: format4.StatusArg, err: err.Error()}
		}
		rows, err := f.Tags(ctx, t.text(1), cids)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.CID), i64(x.Seq), format2.Text(x.Producer), format2.Text(x.Provider),
				format2.Text(x.Source), format2.Text(x.SourceURL), format2.Text(x.Batch), format2.Text(x.ContentKeyID),
				format2.Text(x.ProducerPeer), format2.Text(x.ProducerPubkey), i64(x.At)})
		}
		return r
	case 12, 13, 14, 15:
		q, err := t.query()
		if err != nil {
			return response{status: format4.StatusArg, err: err.Error()}
		}
		switch op {
		case 12:
			recs, err := f.Scan(ctx, q)
			if err != nil {
				return fail(recCols, err)
			}
			return response{names: recCols, rows: recRows(recs)}
		case 13:
			names := []string{"n", "bytes", "max_seq", "max_ts", "max_at", "through", "more"}
			h, err := f.Head(ctx, q)
			if err != nil {
				return fail(names, err)
			}
			more := int64(0)
			if h.More {
				more = 1
			}
			return response{names: names, rows: [][]format2.Cell{{i64(h.N), i64(h.Bytes), i64(h.MaxSeq), i64(h.MaxTS), i64(h.MaxAt), i64(h.Through), i64(more)}}}
		case 14:
			recs, err := f.Window(ctx, q)
			if err != nil {
				return fail(recCols, err)
			}
			return response{names: recCols, rows: recRows(recs)}
		default:
			names := []string{"c0", "epoch", "cid"}
			rows, err := f.IndexPage(ctx, q)
			if err != nil {
				return fail(names, err)
			}
			r := response{names: names}
			for _, x := range rows {
				r.rows = append(r.rows, []format2.Cell{optI64(x.Col0), optI64(x.Epoch), format2.Text(x.CID)})
			}
			return r
		}
	case 16:
		q, err := t.query()
		if err != nil {
			return response{status: format4.StatusArg, err: err.Error()}
		}
		eq := format4.EpochQuery{Query: q, Profile: format4.EpochProfile(t.u8(30)), At: t.i64(31), MaxDelta: t.i64(32)}
		switch {
		case eq.Profile == format4.EpochCoverage:
			names := []string{"day", "n", "min_epoch", "max_epoch"}
			cov, err := f.Coverage(ctx, eq)
			if err != nil {
				return fail(names, err)
			}
			r := response{names: names}
			for _, c := range cov {
				r.rows = append(r.rows, []format2.Cell{format2.Text(c.Day), i64(c.N), i64(c.MinEpoch), i64(c.MaxEpoch)})
			}
			return r
		case t.u8(33) != 0:
			n, err := f.EpochCount(ctx, eq)
			if err != nil {
				return fail([]string{"n"}, err)
			}
			return response{names: []string{"n"}, rows: [][]format2.Cell{{i64(n)}}}
		default:
			recs, err := f.Epoch(ctx, eq)
			if err != nil {
				return fail(recCols, err)
			}
			return response{names: recCols, rows: recRows(recs)}
		}
	case 17:
		return d.summary(ctx, t)
	case 30, 31:
		return response{status: format4.StatusUnsupported, err: "the double runs no SQL"}
	}
	return response{status: format4.StatusArg, err: fmt.Sprintf("op %d", op)}
}

func (d *double) summary(ctx context.Context, t tlvs) response {
	f := d.fake
	switch t.u8(45) {
	case 1:
		names := []string{"type", "records", "copies", "bytes", "copy_bytes", "min_epoch", "max_epoch", "min_ts", "max_ts", "max_seq", "through"}
		rows, err := f.Types(ctx)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), i64(x.Records), i64(x.Copies), i64(x.Bytes), i64(x.CopyBytes),
				optI64(x.MinEpoch), optI64(x.MaxEpoch), i64(x.MinTS), i64(x.MaxTS), i64(x.MaxSeq), i64(x.Through)})
		}
		return r
	case 2:
		names := []string{"type", "producer", "peer", "records", "bytes", "min_ts", "max_ts", "max_seq", "files"}
		rows, err := f.Partitions(ctx)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), format2.Text(x.Producer), format2.Text(x.Peer), i64(x.Records),
				i64(x.Bytes), i64(x.MinTS), i64(x.MaxTS), i64(x.MaxSeq), i64(x.Files)})
		}
		return r
	case 3:
		names := []string{"type", "producer", "provider", "source", "batch", "content_key_id", "producer_peer", "producer_pubkey",
			"source_url", "records", "bytes", "max_seq", "first", "updated", "min_w", "max_w"}
		rows, err := f.Lanes(ctx, t.text(1))
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), format2.Text(x.Producer), format2.Text(x.Provider),
				format2.Text(x.Source), format2.Text(x.Batch), format2.Text(x.ContentKeyID), format2.Text(x.ProducerPeer),
				format2.Text(x.ProducerPubkey), format2.Text(x.SourceURL), i64(x.Records), i64(x.Bytes), i64(x.MaxSeq),
				i64(x.First), i64(x.Updated), i64(x.MinW), i64(x.MaxW)})
		}
		return r
	case 4:
		names := []string{"type", "files", "db_bytes", "wal_bytes", "journal_bytes", "index_bytes", "fts_bytes", "free_bytes"}
		rows, err := f.Disk(ctx)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), i64(x.Files), i64(x.DBBytes), i64(x.WALBytes),
				i64(x.JournalBytes), i64(x.IndexBytes), i64(x.FTSBytes), i64(x.FreeBytes)})
		}
		return r
	case 5:
		names := []string{"type", "state", "through"}
		rows, err := f.FTS(ctx)
		if err != nil {
			return fail(names, err)
		}
		r := response{names: names}
		for _, x := range rows {
			r.rows = append(r.rows, []format2.Cell{format2.Text(x.Type), format2.Text(x.State), i64(x.Through)})
		}
		return r
	}
	return response{status: format4.StatusArg, err: "summary kind"}
}
