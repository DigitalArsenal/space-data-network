package format4proof

import (
	"math/rand"
	"regexp"
	"time"

	CATfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	MPEfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"
)

// Record clones: new CIDs from real fixture records, the earlier baselines'
// mutation (zzbaseline cloneOf; sds-tb-gen's corpus mutation), so every arm
// ingests byte-identical inputs.

var isoRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)

// shiftISO moves the "YYYY-MM-DD[T ]HH:MM:SS" at p by sec seconds, in place.
func shiftISO(p []byte, sec int64) {
	if len(p) < 19 {
		return
	}
	sep := p[10]
	b := make([]byte, 19)
	copy(b, p[:19])
	b[10] = 'T'
	tm, err := time.Parse("2006-01-02T15:04:05", string(b))
	if err != nil {
		return
	}
	copy(p, tm.Add(time.Duration(sec)*time.Second).Format("2006-01-02T15:04:05"))
	p[10] = sep
}

// CloneOf returns clone c (c >= 1) of seed, a record of type typ: the same
// object later (OMM and IQC: every ISO time + c×61 s; MPE: EPOCH + c×61 s);
// CAT: 85% a new object (NORAD + 100003×c), 15% the same object with PERIOD
// nudged (a supersede). rng decides the CAT split.
func CloneOf(typ string, seed []byte, c int64, rng *rand.Rand) []byte {
	out := append([]byte(nil), seed...)
	switch typ {
	case "OMM", "IQC":
		for _, loc := range isoRe.FindAllIndex(out, -1) {
			shiftISO(out[loc[0]:loc[1]], c*61)
		}
	case "MPE":
		m := MPEfb.GetRootAsMPE(out, 0)
		e := m.EPOCH()
		d := float64(c * 61)
		if e < 1e8 { // a day-based epoch
			d /= 86400
		}
		if !m.MutateEPOCH(e + d) {
			m.MutateMEAN_ANOMALY(m.MEAN_ANOMALY() + 1e-6*float64(c))
		}
	case "CAT":
		if rng != nil && rng.Float64() < 0.15 {
			return CloneKeepIdentity(seed, c)
		}
		ct := CATfb.GetRootAsCAT(out, 0)
		ct.MutateNORAD_CAT_ID(ct.NORAD_CAT_ID() + uint32(100003*c))
	}
	return out
}

// CloneKeepIdentity returns a CAT record with the same object identity and a
// new CID (PERIOD, or INCLINATION when PERIOD is absent, nudged): the
// supersede-on-ingest input (W02).
func CloneKeepIdentity(seed []byte, c int64) []byte {
	out := append([]byte(nil), seed...)
	ct := CATfb.GetRootAsCAT(out, 0)
	p := ct.PERIOD()
	np := p * (1 + 1e-9*float64(c))
	if np == p {
		np = p + 1e-9*float64(c) // a zero period
	}
	if !ct.MutatePERIOD(np) {
		ct.MutateINCLINATION(ct.INCLINATION() + 1e-9*float64(c))
	}
	return out
}

// iqcVolatileSlots are IQC's ingest-volatile string fields (vtable slots of
// RETRIEVED_AT, CREATED_AT and UPDATED_AT; storage/record_ingest_identity.go).
var iqcVolatileSlots = []flatbuffers.VOffsetT{16, 92, 94}

// RestampIQC changes the last digit of every volatile string of an IQC
// record in place (same length): a new CID with the same ingest identity,
// "the same capture fetched again" (W05). It reports whether any field
// changed.
func RestampIQC(seed []byte, c int64) ([]byte, bool) {
	out := append([]byte(nil), seed...)
	if len(out) < 8 {
		return out, false
	}
	t := flatbuffers.Table{Bytes: out, Pos: flatbuffers.GetUOffsetT(out)}
	changed := false
	for _, slot := range iqcVolatileSlots {
		o := flatbuffers.UOffsetT(t.Offset(slot))
		if o == 0 {
			continue
		}
		start := o + t.Pos
		str := t.ByteVector(start)
		for i := len(str) - 1; i >= 0; i-- {
			if str[i] >= '0' && str[i] <= '9' {
				str[i] = '0' + byte((int64(str[i]-'0')+1+c%9)%10)
				changed = true
				break
			}
		}
	}
	return out, changed
}
