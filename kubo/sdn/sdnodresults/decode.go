package sdnodresults

// decode.go — turns the raw, size-prefixed $OMM / $OBD FlatBuffer bytes the
// store's wrapper `data` column carries into the read-model in types.go. Pure
// decode: no mutation, no re-encoding, no orchestration. NORAD_CAT_ID (OMM) /
// SAT_NO (OBD) is the join key between the two records for one fitted object.

import (
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OBD"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
)

// ommFacts is the subset of one decoded $OMM this package renders.
type ommFacts struct {
	Norad        uint32
	ObjectName   string
	ObjectID     string
	Epoch        string
	MeanMotion   float64
	Eccentricity float64
	Inclination  float64
}

// decodeOMM parses a size-prefixed $OMM buffer (the store wrapper's `data`
// column, verbatim). Returns ok=false for anything that fails to parse as an
// OMM (never a panic — FlatBuffers accessors are safe over arbitrary bytes,
// but a corrupt/foreign buffer could still yield garbage zero fields).
func decodeOMM(data []byte) (ommFacts, bool) {
	if len(data) < 12 {
		return ommFacts{}, false
	}
	defer func() { recover() }() //nolint:errcheck // defensive: never let a foreign/short buffer panic the read surface
	o := OMM.GetSizePrefixedRootAsOMM(data, 0)
	if o == nil {
		return ommFacts{}, false
	}
	return ommFacts{
		Norad:        o.NORAD_CAT_ID(),
		ObjectName:   string(o.OBJECT_NAME()),
		ObjectID:     string(o.OBJECT_ID()),
		Epoch:        string(o.EPOCH()),
		MeanMotion:   o.MEAN_MOTION(),
		Eccentricity: o.ECCENTRICITY(),
		Inclination:  o.INCLINATION(),
	}, true
}

// obdFacts is the subset of one decoded $OBD this package renders.
type obdFacts struct {
	SatNo        uint32
	WRMS         float64
	BestPassWRMS float64
	Iterations   int
	FitSpanDays  float64
}

// decodeOBD parses a size-prefixed $OBD buffer the same way decodeOMM does.
func decodeOBD(data []byte) (obdFacts, bool) {
	if len(data) < 12 {
		return obdFacts{}, false
	}
	defer func() { recover() }() //nolint:errcheck // defensive, see decodeOMM
	o := OBD.GetSizePrefixedRootAsOBD(data, 0)
	if o == nil {
		return obdFacts{}, false
	}
	return obdFacts{
		SatNo:        o.SAT_NO(),
		WRMS:         o.WRMS(),
		BestPassWRMS: o.BEST_PASS_WRMS(),
		Iterations:   int(o.NUM_ITERATIONS()),
		FitSpanDays:  o.FIT_SPAN(),
	}, true
}

// effectiveWRMS is the ruled fit-quality value for averaging: WRMS, falling
// back to BEST_PASS_WRMS when WRMS itself is unset (0) — the module-side
// ruling is "no wrms store column, read-side BLOB decode only," and this is
// that decode's one fallback rule. Returns (0, false) when neither is usable.
func (f obdFacts) effectiveWRMS() (float64, bool) {
	if f.WRMS > 0 {
		return f.WRMS, true
	}
	if f.BestPassWRMS > 0 {
		return f.BestPassWRMS, true
	}
	return 0, false
}
