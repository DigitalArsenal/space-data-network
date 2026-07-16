package sdnruns

// ephemeris_source.go is the PRODUCTION ephemeris source for the supplemental-OMM
// run engine: a store-backed EphemerisSource that reads EVERY per-object operator
// ephemeris record a provider's data-source WASM module has ingested into the
// node record store and yields one Ephemeris per object. This is the core of the
// "fit every object each enabled provider ingested" behaviour — a run over the
// Starlink lane fits thousands of objects, not one.
//
// # The store seam
//
// A data-source module (e.g. spacex-starlink-source) fetches operator ephemeris
// over its http capability, builds one CCSDS OEM record PER satellite as a
// schema-exact JSON document (build_oem_record), and stores each under
// (source_name, "OEM") via the storage.ingest_with_source host capability. The
// bytes that land in the store are that JSON document verbatim (the store
// content-addresses whatever the module writes; ReadBySourceType returns it
// unchanged). So ReadBySourceType(ctx, <lane>, "OEM") returns raw JSON-OEM
// records, one per object.
//
// # Format reconciliation (JSON-OEM -> CCSDS OEM KVN)
//
// The analysis/od module's fit consumes ONLY "meme" (SpaceX MEME text) or "oem"
// (CCSDS OEM *KVN* text) — its oem parser is a line-oriented KVN parser, not a
// JSON reader. The store holds OEM as JSON. So this source converts each stored
// JSON-OEM record into the CCSDS OEM KVN text the fitter parses, and feeds it on
// the "oem" input path. The identity fields (OBJECT_NAME / OBJECT_ID /
// NORAD_CAT_ID) and the provider's CelesTrak-comparable DATA_SOURCE token are
// carried on the Ephemeris so the OD fit stamps them onto the produced OMM.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// providerLanes maps a RunConfig provider token to the record-store source lane
// the provider's data-source WASM module writes its per-object OEM records under
// (the module's source_name). The map is explicit but tolerant: an unmapped
// token is used as-is, so a config that already names the store lane works too.
var providerLanes = map[string]string{
	"starlink":        "spacex-starlink",
	"spacex-starlink": "spacex-starlink",
	"iss":             "iss",
	"oneweb":          "oneweb",
	"glonass":         "glonass",
	"intelsat":        "intelsat",
	"cpf":             "cpf",
	"gps":             "gps",
}

// providerDataSource maps a provider token to the CelesTrak-comparable
// DATA_SOURCE token its data-source module tags records with (the module's
// data_source constant). The stored JSON-OEM record does not itself carry
// DATA_SOURCE (it lives in the module's provenance sidecar), so the run stamps it
// from this map. A record that DOES carry a DATA_SOURCE field overrides it.
var providerDataSource = map[string]string{
	"starlink":        "SpaceX-E",
	"spacex-starlink": "SpaceX-E",
	"iss":             "ISS-E",
	"oneweb":          "OneWeb-E",
	"glonass":         "GLONASS-RE",
	"intelsat":        "Intelsat-11P",
	"cpf":             "CPF",
	"gps":             "GPS-A",
}

// laneFor resolves a provider token to its record-store source lane.
func laneFor(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if lane, ok := providerLanes[p]; ok {
		return lane
	}
	return p
}

// StoreEphemerisSource is the production EphemerisSource: it reads every stored
// per-object OEM record for a provider's store lane and yields one Ephemeris per
// object (JSON-OEM converted to CCSDS OEM KVN for the OD fit).
type StoreEphemerisSource struct {
	records     RecordStore
	issFallback []byte // embedded ISS OEM KVN; used ONLY when provider "iss" has zero stored OEM
	log         Logger
}

// NewStoreEphemerisSource builds a store-backed ephemeris source over records.
// issFallback (may be nil) is a checked-in real ISS OEM KVN fixture returned as
// the single "iss" object ONLY when the store holds no ingested ISS OEM — so a
// firewalled local smoke with no ingested data still produces its one ISS fit,
// while a node that HAS ingested ephemeris fits every stored object.
func NewStoreEphemerisSource(records RecordStore, issFallback []byte, log Logger) *StoreEphemerisSource {
	return &StoreEphemerisSource{records: records, issFallback: issFallback, log: log}
}

func (s *StoreEphemerisSource) logf(format string, args ...interface{}) {
	if s.log != nil {
		s.log(format, args...)
	}
}

// Pull returns one Ephemeris per stored OEM record for the provider's lane. When
// the lane holds nothing and the provider is "iss", it falls back to the embedded
// ISS fixture (single object); for every other provider an empty lane yields no
// objects (nothing to fit).
func (s *StoreEphemerisSource) Pull(ctx context.Context, provider string) ([]Ephemeris, error) {
	lane := laneFor(provider)
	var recs [][]byte
	if s.records != nil {
		r, err := s.records.ReadBySourceType(ctx, lane, "OEM")
		if err != nil {
			s.logf("sdnruns: read OEM lane %q for provider %q failed: %v", lane, provider, err)
		} else {
			recs = r
		}
	}

	out := make([]Ephemeris, 0, len(recs))
	dataSource := providerDataSource[strings.ToLower(strings.TrimSpace(provider))]
	for i, rec := range recs {
		eph, ok := ephemerisFromOEMJSON(provider, dataSource, rec)
		if !ok {
			s.logf("sdnruns: provider %q lane %q: skipped unparseable OEM record %d/%d", provider, lane, i+1, len(recs))
			continue
		}
		out = append(out, eph)
	}

	if len(out) == 0 && strings.EqualFold(strings.TrimSpace(provider), "iss") && len(s.issFallback) > 0 {
		s.logf("sdnruns: provider \"iss\" store lane empty; using embedded ISS OEM fixture (1 object)")
		out = append(out, Ephemeris{
			Provider:   "iss",
			Format:     "oem",
			ObjectName: "ISS",
			ObjectID:   "1998-067-A",
			NoradCatID: 25544,
			DataSource: "ISS-E",
			Bytes:      append([]byte(nil), s.issFallback...),
		})
	}
	return out, nil
}

// --- JSON-OEM record -> Ephemeris (CCSDS OEM KVN) ---------------------------

// oemJSONRecord mirrors the data-source modules' build_oem_record output (the
// schema-exact CCSDS OEM JSON document each stores per satellite).
type oemJSONRecord struct {
	Vers         json.Number    `json:"CCSDS_OEM_VERS"`
	CreationDate string         `json:"CREATION_DATE"`
	Originator   string         `json:"ORIGINATOR"`
	Blocks       []oemJSONBlock `json:"EPHEMERIS_DATA_BLOCK"`
}

type oemJSONBlock struct {
	ObjectName      string  `json:"OBJECT_NAME"`
	ObjectID        string  `json:"OBJECT_ID"`
	NoradCatID      uint32  `json:"NORAD_CAT_ID"`
	DataSource      string  `json:"DATA_SOURCE"`
	CenterName      string  `json:"CENTER_NAME"`
	ReferenceFrame  string  `json:"REFERENCE_FRAME"`
	TimeSystem      string  `json:"TIME_SYSTEM"`
	StartTime       string  `json:"START_TIME"`
	StopTime        string  `json:"STOP_TIME"`
	StepSize        float64 `json:"STEP_SIZE"`
	StateVectorSize int     `json:"STATE_VECTOR_SIZE"`
	// Flat, row-major state vectors (starlink / oneweb / glonass / intelsat):
	// STATE_VECTOR_SIZE values per state, epochs implied by START_TIME+i*STEP_SIZE.
	EphemerisData []float64 `json:"EPHEMERIS_DATA"`
	// Explicit per-line points carrying their own EPOCH (CPF position-only).
	EphemerisDataLines []oemJSONLine `json:"EPHEMERIS_DATA_LINES"`
}

type oemJSONLine struct {
	Epoch string  `json:"EPOCH"`
	X     float64 `json:"X"`
	Y     float64 `json:"Y"`
	Z     float64 `json:"Z"`
}

// ephemerisFromOEMJSON parses one stored JSON-OEM record and builds the Ephemeris
// the OD fit consumes: CCSDS OEM KVN bytes on the "oem" path plus the identity +
// DATA_SOURCE the fit stamps onto the produced OMM. ok is false for a record that
// carries no usable segment or state vectors.
func ephemerisFromOEMJSON(provider, providerDataSource string, rec []byte) (Ephemeris, bool) {
	var doc oemJSONRecord
	dec := json.NewDecoder(strings.NewReader(string(rec)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil || len(doc.Blocks) == 0 {
		return Ephemeris{}, false
	}
	blk := doc.Blocks[0]

	kvn, ok := oemBlockToKVN(doc, blk)
	if !ok {
		return Ephemeris{}, false
	}

	dataSource := strings.TrimSpace(blk.DataSource)
	if dataSource == "" {
		dataSource = providerDataSource
	}
	return Ephemeris{
		Provider:   provider,
		Format:     "oem",
		ObjectName: blk.ObjectName,
		ObjectID:   blk.ObjectID,
		NoradCatID: blk.NoradCatID,
		DataSource: dataSource,
		Bytes:      kvn,
	}, true
}

// oemKVNTimeLayout is the CCSDS OEM KVN epoch form the analysis/od parser reads
// (matches the NASA ISS OEM fixture: no zone suffix, millisecond precision).
const oemKVNTimeLayout = "2006-01-02T15:04:05.000"

// oemBlockToKVN renders one JSON-OEM segment as CCSDS OEM KVN text. It emits the
// header + one META block (REF_FRAME/CENTER_NAME/TIME_SYSTEM the OD parser
// validates) + one data line per state vector. Flat EPHEMERIS_DATA reconstructs
// each line's epoch as START_TIME + i*STEP_SIZE; explicit EPHEMERIS_DATA_LINES
// carry their own EPOCH.
func oemBlockToKVN(doc oemJSONRecord, blk oemJSONBlock) ([]byte, bool) {
	var b strings.Builder

	creation := strings.TrimSpace(doc.CreationDate)
	if creation == "" {
		creation = time.Now().UTC().Format(oemKVNTimeLayout)
	}
	originator := strings.TrimSpace(doc.Originator)
	if originator == "" {
		originator = "SDN"
	}
	center := strings.TrimSpace(blk.CenterName)
	if center == "" {
		center = "EARTH"
	}
	frame := strings.TrimSpace(blk.ReferenceFrame)
	if frame == "" {
		frame = "TEME"
	}
	timeSys := strings.TrimSpace(blk.TimeSystem)
	if timeSys == "" {
		timeSys = "UTC"
	}

	b.WriteString("CCSDS_OEM_VERS = 2.0\n")
	b.WriteString("CREATION_DATE = " + creation + "\n")
	b.WriteString("ORIGINATOR = " + originator + "\n\n")
	b.WriteString("META_START\n")
	b.WriteString("OBJECT_NAME = " + oneLine(blk.ObjectName) + "\n")
	if id := strings.TrimSpace(blk.ObjectID); id != "" {
		b.WriteString("OBJECT_ID = " + oneLine(id) + "\n")
	}
	b.WriteString("CENTER_NAME = " + oneLine(center) + "\n")
	b.WriteString("REF_FRAME = " + oneLine(frame) + "\n")
	b.WriteString("TIME_SYSTEM = " + oneLine(timeSys) + "\n")
	if st := strings.TrimSpace(blk.StartTime); st != "" {
		b.WriteString("START_TIME = " + oneLine(st) + "\n")
	}
	if sp := strings.TrimSpace(blk.StopTime); sp != "" {
		b.WriteString("STOP_TIME = " + oneLine(sp) + "\n")
	}
	b.WriteString("META_STOP\n\n")

	lines := 0

	// Explicit per-line points (CPF position-only): each carries its own EPOCH.
	for _, ln := range blk.EphemerisDataLines {
		ep := strings.TrimSpace(ln.Epoch)
		if ep == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("%s %s %s %s\n", ep,
			ftoa(ln.X), ftoa(ln.Y), ftoa(ln.Z)))
		lines++
	}

	// Flat row-major state vectors: epochs reconstructed from START_TIME+i*STEP.
	if len(blk.EphemerisData) > 0 {
		size := blk.StateVectorSize
		if size != 3 && size != 6 {
			size = 6
		}
		start, ok := parseOEMTime(blk.StartTime)
		step := blk.StepSize
		if ok && step > 0 {
			n := len(blk.EphemerisData) / size
			for i := 0; i < n; i++ {
				row := blk.EphemerisData[i*size : (i+1)*size]
				epoch := start.Add(time.Duration(float64(i)*step*float64(time.Second))).UTC().Format(oemKVNTimeLayout)
				switch size {
				case 6:
					b.WriteString(fmt.Sprintf("%s %s %s %s %s %s %s\n", epoch,
						ftoa(row[0]), ftoa(row[1]), ftoa(row[2]),
						ftoa(row[3]), ftoa(row[4]), ftoa(row[5])))
				case 3:
					b.WriteString(fmt.Sprintf("%s %s %s %s\n", epoch,
						ftoa(row[0]), ftoa(row[1]), ftoa(row[2])))
				}
				lines++
			}
		}
	}

	if lines == 0 {
		return nil, false
	}
	return []byte(b.String()), true
}

// parseOEMTime parses the ISO-8601 epoch forms the data-source modules emit
// (with/without a zone, with/without fractional seconds). Returns UTC.
func parseOEMTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// ftoa formats a float the way the CCSDS OEM state lines carry it: fixed enough
// precision for km / km-s state vectors, trailing zeros trimmed.
func ftoa(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// oneLine collapses any embedded newlines in a KVN value so a single record
// field cannot inject extra KVN lines.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
