package sds

// DIFFERENTIAL FUZZING OF THE STRUCTURAL VERIFIER (evidence, not a unit test).
//
// internal/fbverify is held to flatbuffers' own C++ verifier: the reflection
// verifier plus the DeepVerifier that flatc's binary_to_json runs, embedded
// in the node as flatc-wasi.wasm. For every input this harness asks both:
//
//   - ours accepts, the reference refuses: a conformance gap. Always fatal.
//   - ours refuses, the reference accepts: fatal unless the refusal carries
//     one of the strict classes documented in fbverify/verify.go.
//   - ours accepts: every generated Go accessor of the root type is driven,
//     and a panic is fatal.
//   - sds.VerifyStructure accepts: every envelope form a generated reader
//     could select (by identifier) is accepted by the reference and reads
//     without a panic.
//
// The reference sees each input as the node's own verification path hands
// it over (Validator.placeForVerifier): a size-prefixed buffer as is, a bare
// buffer at its recordalign origin.
//
//	go test -run '^FuzzStructuralVerifierMatchesReference$' -v ./internal/sds   # seeds
//	go test -run '^$' -fuzz '^FuzzStructuralVerifierMatchesReference$' -fuzztime 10m ./internal/sds
//
// SDN_FBVERIFY_REAL_CORPUS=<dir> adds every <dir>/<SCHEMA>/*.bin as seeds for
// that schema in both forms (records captured from real nodes, kept out of
// the repository), and logs per-schema accept/refuse counts.

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/KML"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/storage/recordalign"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

type diffTarget struct {
	schema       string
	sizePrefixed bool
	// root reads buf in this target's form; nil for schemas whose binding
	// this harness does not import (differential only).
	root func([]byte) reflect.Value
}

// diffTargets are fixed so committed seeds keep their meaning: the first
// input byte selects one, modulo the count. Real-corpus targets are appended.
var diffTargets = []diffTarget{
	{"PNM.fbs", true, func(b []byte) reflect.Value { return reflect.ValueOf(PNM.GetSizePrefixedRootAsPNM(b, 0)) }},
	{"PNM.fbs", false, func(b []byte) reflect.Value { return reflect.ValueOf(PNM.GetRootAsPNM(b, 0)) }},
	{"EPM.fbs", true, func(b []byte) reflect.Value { return reflect.ValueOf(EPM.GetSizePrefixedRootAsEPM(b, 0)) }},
	{"DPM.fbs", false, func(b []byte) reflect.Value { return reflect.ValueOf(DPM.GetRootAsDPM(b, 0)) }},
	{"KML.fbs", false, func(b []byte) reflect.Value { return reflect.ValueOf(KML.GetRootAsKML(b, 0)) }},
	{"OMM.fbs", false, func(b []byte) reflect.Value { return reflect.ValueOf(OMM.GetRootAsOMM(b, 0)) }},
}

// Synthetic records with dummy values, built by the node's own converter, for
// schemas whose real records are not committed (an EPM carries a node's
// public keys and wallet addresses).
const syntheticEPMJSON = `{
 "DN":"CN=node","LEGAL_NAME":"Org","FAMILY_NAME":"F","GIVEN_NAME":"G","ADDITIONAL_NAME":"A",
 "HONORIFIC_PREFIX":"Dr","HONORIFIC_SUFFIX":"III","JOB_TITLE":"Op","OCCUPATION":"Eng",
 "ADDRESS":{"COUNTRY":"US","REGION":"CO","LOCALITY":"Den","POSTAL_CODE":"80202","STREET":"1 Main","POST_OFFICE_BOX_NUMBER":"9"},
 "ALTERNATE_NAMES":["a","bb"],"EMAIL":"e@x","TELEPHONE":"1",
 "KEYS":[{"PUBLIC_KEY":"02ab","KEY_TYPE":"Signing","KEY_PATH":"m/0","ALGORITHM":"secp256k1"},{"PUBLIC_KEY":"03cd","KEY_TYPE":"Encryption"}],
 "MULTIFORMAT_ADDRESS":["/ip4/192.0.2.1/tcp/1"],"SIGNATURE":"sig","SIGNATURE_TIMESTAMP":7,
 "CHAIN_PROOFS":[{"CHAIN":"eth","ADDRESS":"0x1","PUBLIC_KEY":"02","SIGNATURE":"s","SIGNED_PAYLOAD":"p"}],
 "ENTITY_TYPE":"Node","SIGNATURE_ALGORITHM":"ecdsa",
 "DOMAIN_PROOFS":[{"DNS_DOMAIN":"x.example","PUBLIC_KEY":"02","SIGNATURE":"s"}],"PHOTO":"ph"}`

const syntheticOMMJSON = `{"OBJECT_NAME":"TEST SAT","OBJECT_ID":"2000-000A","NORAD_CAT_ID":99999,"EPOCH":"2026-10-15T00:00:00Z","MEAN_MOTION":15.5,"ECCENTRICITY":0.0001,"INCLINATION":51.6,"RA_OF_ASC_NODE":10,"ARG_OF_PERICENTER":20,"MEAN_ANOMALY":30,"BSTAR":0.0001}`

type verdict struct {
	accept bool
	class  fbverify.Class
	reason string
}

type differential struct {
	v       *Validator
	targets []diffTarget
}

func newDifferential(tb testing.TB, extraSchemas []string) *differential {
	fm, err := wasm.NewEmbeddedFlatcModule(context.Background())
	if err != nil {
		tb.Fatalf("NewEmbeddedFlatcModule: %v", err)
	}
	tb.Cleanup(func() { _ = fm.Close(context.Background()) })
	v, err := NewValidator(fm)
	if err != nil {
		tb.Fatalf("NewValidator: %v", err)
	}
	d := &differential{v: v, targets: append([]diffTarget(nil), diffTargets...)}
	for _, schema := range extraSchemas {
		d.targets = append(d.targets, diffTarget{schema: schema, sizePrefixed: true}, diffTarget{schema: schema, sizePrefixed: false})
	}
	for _, tg := range d.targets {
		if _, err := v.converterID(context.Background(), tg.schema); err != nil {
			tb.Fatalf("reference cannot load %s: %v", tg.schema, err)
		}
		if _, err := structureRegistry().Schema(tg.schema); err != nil {
			tb.Fatalf("fbverify cannot compile %s: %v", tg.schema, err)
		}
	}
	return d
}

// ours is fbverify on exactly one form, structure only (the reference's
// reflection verifier checks no file identifier either).
func (d *differential) ours(tg diffTarget, buf []byte) verdict {
	s, _ := structureRegistry().Schema(tg.schema)
	var err error
	if tg.sizePrefixed {
		err = s.VerifySizePrefixedBuffer(buf)
	} else {
		err = verifyBare(s, buf)
	}
	if err == nil {
		return verdict{accept: true}
	}
	class, ok := fbverify.ClassOf(err)
	if !ok {
		class = fbverify.ClassConformant
	}
	return verdict{class: class, reason: err.Error()}
}

// reference is flatc-wasi's binary_to_json verification of the same form.
func (d *differential) reference(tb testing.TB, tg diffTarget, buf []byte) verdict {
	ctx := context.Background()
	id, _ := d.v.converterID(ctx, tg.schema)
	view, opts := buf, wasm.FlatcOption(0)
	if tg.sizePrefixed {
		opts = wasm.FlatcSizePrefixed
	} else if r := recordalign.Record(buf); r.SizePrefixed {
		view, opts = r.Buf, wasm.FlatcSizePrefixed
	}
	err := d.v.flatc.VerifyBinary(ctx, id, view, opts)
	if err == nil {
		return verdict{accept: true}
	}
	status, ok := wasm.FlatcStatusOf(err)
	switch {
	case ok && status == wasm.FlatcInvalidBinary:
		return verdict{reason: err.Error()}
	case ok && status == wasm.FlatcJSONGeneration:
		// Verified; only the JSON printer declined (e.g. non-UTF-8 text).
		return verdict{accept: true}
	}
	tb.Fatalf("%s: reference returned no verdict: %v", tg.schema, err)
	return verdict{}
}

// compare applies the harness rules to one input in one form and returns the
// two verdicts.
func (d *differential) compare(tb testing.TB, tg diffTarget, buf []byte) (verdict, verdict) {
	ours, ref := d.ours(tg, buf), d.reference(tb, tg, buf)
	form := map[bool]string{true: "size-prefixed", false: "bare"}[tg.sizePrefixed]
	switch {
	case ours.accept && !ref.accept:
		tb.Fatalf("%s %s: CONFORMANCE GAP: fbverify accepts, the C++ verifier refuses (%s)\nbuffer: %x", tg.schema, form, ref.reason, buf)
	case !ours.accept && ref.accept && ours.class == fbverify.ClassConformant:
		tb.Fatalf("%s %s: fbverify refuses as conformant, the C++ verifier accepts: %s\nbuffer: %x", tg.schema, form, ours.reason, buf)
	}
	if ours.accept && tg.root != nil {
		if msg := accessorsPanic(tg.root, buf); msg != "" {
			tb.Fatalf("%s %s: a buffer fbverify accepted panics a Go accessor: %s\nbuffer: %x", tg.schema, form, msg, buf)
		}
	}
	return ours, ref
}

// checkForms is the envelope-form property behind VerifyStructure (review
// finding S1): when it accepts, every form a generated reader could select
// by identifier verifies in the reference and reads without a panic.
func (d *differential) checkForms(tb testing.TB, schema string, buf []byte) {
	if VerifyStructure(schema, buf) != nil {
		return
	}
	s, _ := structureRegistry().Schema(schema)
	ident := s.FileIdentifier()
	if ident == "" {
		return
	}
	for _, tg := range d.targets {
		if tg.schema != schema {
			continue
		}
		base := 0
		if tg.sizePrefixed {
			base = sizePrefixLength
		}
		if len(buf) < base+minFlatBufferLength || string(buf[base+4:base+8]) != ident {
			continue
		}
		if ref := d.reference(tb, tg, buf); !ref.accept {
			tb.Fatalf("%s: VerifyStructure accepted, but a reader selecting the %v form gets bytes the C++ verifier refuses (%s)\nbuffer: %x", schema, map[bool]string{true: "size-prefixed", false: "bare"}[tg.sizePrefixed], ref.reason, buf)
		}
		if tg.root != nil {
			if msg := accessorsPanic(tg.root, buf); msg != "" {
				tb.Fatalf("%s: VerifyStructure accepted, but the %v form panics a Go accessor: %s\nbuffer: %x", schema, tg.sizePrefixed, msg, buf)
			}
		}
	}
}

func FuzzStructuralVerifierMatchesReference(f *testing.F) {
	corpus := realCorpus(f)
	extra := make([]string, 0, len(corpus))
	for schema := range corpus {
		extra = append(extra, schema)
	}
	sort.Strings(extra)
	d := newDifferential(f, extra)

	targetIndex := func(schema string, sizePrefixed bool) byte {
		for i, tg := range d.targets {
			if tg.schema == schema && tg.sizePrefixed == sizePrefixed {
				return byte(i)
			}
		}
		f.Fatalf("no target %s/%v", schema, sizePrefixed)
		return 0
	}
	add := func(schema string, sizePrefixed bool, buf []byte) {
		f.Add(append([]byte{targetIndex(schema, sizePrefixed)}, buf...))
	}

	// Real records from the environment: both forms, with counts.
	for _, schema := range extra {
		counts := map[string]int{}
		for _, rec := range corpus[schema] {
			sized, bare := bothForms(rec)
			for _, form := range []struct {
				prefixed bool
				buf      []byte
			}{{true, sized}, {false, bare}} {
				tg := d.targets[targetIndex(schema, form.prefixed)]
				ours, ref := d.compare(f, tg, form.buf)
				key := fmt.Sprintf("%s ours=%v ref=%v", map[bool]string{true: "size-prefixed", false: "bare"}[form.prefixed], ours.accept, ref.accept)
				if !ours.accept && ref.accept {
					key += " (" + ours.class.String() + ")"
				}
				counts[key]++
				add(schema, form.prefixed, form.buf)
			}
		}
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f.Logf("real %s: %s: %d", schema, k, counts[k])
		}
	}

	// Constructed boundary seeds (the committed real-record seeds live in
	// testdata/fuzz/FuzzStructuralVerifierMatchesReference).
	for _, depth := range []int{2, 63, 64, 70} {
		buf := nestedKML(depth)
		add("KML.fbs", false, buf)
		ours, ref := d.compare(f, d.targets[targetIndex("KML.fbs", false)], buf)
		want := depth+1 <= fbverify.MaxDepth
		if ours.accept != want || ref.accept != want {
			f.Fatalf("KML with %d nested folders (%d tables deep): ours=%v reference=%v, want %v", depth, depth+1, ours.accept, ref.accept, want)
		}
	}
	add("PNM.fbs", true, outOfRangeFieldPNM())
	synthetic := map[string][]byte{}
	for _, s := range []struct {
		schema, json string
		opts         wasm.FlatcOption
		prefixed     bool
	}{{"EPM.fbs", syntheticEPMJSON, wasm.FlatcSizePrefixed, true}, {"OMM.fbs", syntheticOMMJSON, StoredRecordOptions, false}} {
		buf, err := d.v.JSONToFlatBuffer(context.Background(), s.schema, []byte(s.json), s.opts)
		if err != nil {
			f.Fatalf("synthetic %s: %v", s.schema, err)
		}
		synthetic[s.schema] = buf
		add(s.schema, s.prefixed, buf)
		if ours, ref := d.compare(f, d.targets[targetIndex(s.schema, s.prefixed)], buf); !ours.accept || !ref.accept {
			f.Fatalf("synthetic %s: ours=%v reference=%v (%s %s)", s.schema, ours.accept, ref.accept, ours.reason, ref.reason)
		}
	}
	if os.Getenv("SDN_FBVERIFY_SWEEP") != "" {
		inputs := []sweepInput{
			{d.targets[targetIndex("EPM.fbs", true)], synthetic["EPM.fbs"]},
			{d.targets[targetIndex("OMM.fbs", false)], synthetic["OMM.fbs"]},
			{d.targets[targetIndex("KML.fbs", false)], nestedKML(3)},
			{d.targets[targetIndex("PNM.fbs", true)], outOfRangeFieldPNM()},
		}
		for _, schema := range extra {
			for _, rec := range corpus[schema] {
				sized, bare := bothForms(rec)
				inputs = append(inputs, sweepInput{d.targets[targetIndex(schema, true)], sized}, sweepInput{d.targets[targetIndex(schema, false)], bare})
			}
		}
		d.sweep(f, inputs)
	}
	for _, tg := range d.targets[:4] {
		for _, n := range []int{0, 3, 4, 7, 8, 11, 12} {
			add(tg.schema, tg.sizePrefixed, make([]byte, n))
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		tg := d.targets[int(data[0])%len(d.targets)]
		buf := data[1:]
		d.compare(t, tg, buf)
		d.checkForms(t, tg.schema, buf)
	})
}

type sweepInput struct {
	tg  diffTarget
	buf []byte
}

// sweep runs a deterministic mutation sweep through the differential: every
// byte set to 0x00, 0x80 and 0xff, and every u32 set to 0, 1, 0x7fffffff,
// 0x80000000 and the buffer length, at every position. It logs the verdict
// pairs and the strict classes; compare fails on any disallowed pair.
func (d *differential) sweep(tb testing.TB, inputs []sweepInput) {
	counts := map[string]int{}
	total := 0
	for _, in := range inputs {
		try := func(buf []byte) {
			total++
			ours, ref := d.compare(tb, in.tg, buf)
			d.checkForms(tb, in.tg.schema, buf)
			key := fmt.Sprintf("ours=%v ref=%v", ours.accept, ref.accept)
			if !ours.accept && ref.accept {
				key += " " + ours.class.String()
			}
			counts[key]++
		}
		for i := range in.buf {
			for _, v := range []byte{0x00, 0x80, 0xff} {
				if in.buf[i] == v {
					continue
				}
				buf := append([]byte(nil), in.buf...)
				buf[i] = v
				try(buf)
			}
		}
		for i := 0; i+4 <= len(in.buf); i++ {
			for _, v := range []uint32{0, 1, 0x7fffffff, 0x80000000, uint32(len(in.buf))} {
				buf := append([]byte(nil), in.buf...)
				binary.LittleEndian.PutUint32(buf[i:], v)
				try(buf)
			}
		}
		for _, n := range []int{len(in.buf) / 2, len(in.buf) - 1} {
			if n >= 0 {
				try(append([]byte(nil), in.buf[:n]...))
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tb.Logf("sweep: %d mutated inputs from %d records", total, len(inputs))
	for _, k := range keys {
		tb.Logf("sweep: %s: %d", k, counts[k])
	}
}

// bothForms returns a record as a size-prefixed buffer and as the bare
// buffer its readers store (prefix stripped), whichever form it came in.
func bothForms(rec []byte) (sized, bare []byte) {
	if len(rec) >= 12 && int(binary.LittleEndian.Uint32(rec)) == len(rec)-4 {
		return rec, rec[4:]
	}
	sized = binary.LittleEndian.AppendUint32(make([]byte, 0, len(rec)+4), uint32(len(rec)))
	return append(sized, rec...), rec
}

// realCorpus reads SDN_FBVERIFY_REAL_CORPUS/<SCHEMA>/*.bin for schemas both
// verifiers know.
func realCorpus(tb testing.TB) map[string][][]byte {
	dir := os.Getenv("SDN_FBVERIFY_REAL_CORPUS")
	out := map[string][][]byte{}
	if dir == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("SDN_FBVERIFY_REAL_CORPUS: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		schema := NormalizeSchemaFileName(e.Name())
		if _, err := structureRegistry().Schema(schema); err != nil {
			tb.Logf("real corpus: skipping %s: %v", e.Name(), err)
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, e.Name(), "*.bin"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err == nil {
				out[schema] = append(out[schema], b)
			}
		}
	}
	return out
}

// nestedKML builds, with the generated Go builder, a KML whose root holds a
// chain of depth nested KMLFolders: depth+1 tables deep.
func nestedKML(depth int) []byte {
	b := flatbuffers.NewBuilder(256)
	var inner flatbuffers.UOffsetT
	for i := 0; i < depth; i++ {
		var folders flatbuffers.UOffsetT
		if i > 0 {
			KML.KMLFolderStartFOLDERSVector(b, 1)
			b.PrependUOffsetT(inner)
			folders = b.EndVector(1)
		}
		name := b.CreateString(fmt.Sprintf("f%d", i))
		KML.KMLFolderStart(b)
		KML.KMLFolderAddNAME(b, name)
		if i > 0 {
			KML.KMLFolderAddFOLDERS(b, folders)
		}
		inner = KML.KMLFolderEnd(b)
	}
	KML.KMLStartFOLDERSVector(b, 1)
	b.PrependUOffsetT(inner)
	folders := b.EndVector(1)
	KML.KMLStart(b)
	KML.KMLAddFOLDERS(b, folders)
	KML.FinishKMLBuffer(b, KML.KMLEnd(b))
	return b.FinishedBytes()
}

// outOfRangeFieldPNM is the re-audit's NET-01 shape: a size-prefixed PNM
// whose CID field uoffset points far past the end of the buffer.
func outOfRangeFieldPNM() []byte {
	inner := make([]byte, 28)
	binary.LittleEndian.PutUint32(inner[0:], 20)
	copy(inner[4:8], "$PNM")
	binary.LittleEndian.PutUint16(inner[8:], 10)
	binary.LittleEndian.PutUint16(inner[10:], 8)
	binary.LittleEndian.PutUint16(inner[16:], 4) // CID (vtable slot 8) at table+4
	binary.LittleEndian.PutUint32(inner[20:], 12)
	binary.LittleEndian.PutUint32(inner[24:], 0x7fffff00)
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

// accessorsPanic drives every generated accessor of the root reflectively
// and reports a panic.
func accessorsPanic(root func([]byte) reflect.Value, buf []byte) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	walkAccessors(root(buf), 0, map[walked]bool{})
	return ""
}

// walked identifies one table read: the bindings expose alias accessors
// (FOLDERS and Folders) for the same field, so without this a nested chain
// is walked 2^depth times.
type walked struct {
	typ reflect.Type
	pos flatbuffers.UOffsetT
}

var tableType = reflect.TypeOf(&flatbuffers.Table{})

func walkAccessors(v reflect.Value, depth int, seen map[walked]bool) {
	if depth > 70 || v.IsNil() {
		return
	}
	typ := v.Type()
	if m := v.MethodByName("Table"); m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() == 1 {
		if tab, ok := m.Call(nil)[0].Interface().(flatbuffers.Table); ok {
			key := walked{typ, tab.Pos}
			if seen[key] {
				return
			}
			seen[key] = true
		}
	}
	lengths := map[string]int{}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if strings.HasSuffix(m.Name, "Length") && m.Type.NumIn() == 1 && m.Type.NumOut() == 1 && m.Type.Out(0).Kind() == reflect.Int {
			n := int(v.Method(i).Call(nil)[0].Int())
			if n > 64 {
				// Spot-check long vectors at both ends.
				n = 64
			}
			lengths[strings.TrimSuffix(m.Name, "Length")] = n
		}
	}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		name := m.Name
		if name == "Init" || name == "Table" || strings.HasPrefix(name, "Mutate") || strings.HasSuffix(name, "Length") || strings.HasSuffix(name, "ByKey") {
			continue
		}
		ins := m.Type.NumIn() - 1 // minus the receiver
		fn := v.Method(i)
		switch {
		case ins == 0:
			fn.Call(nil)
		case ins == 1 && m.Type.In(1).Kind() == reflect.Int:
			for j := 0; j < lengths[name]; j++ {
				fn.Call([]reflect.Value{reflect.ValueOf(j)})
			}
		case ins == 1 && m.Type.In(1) == tableType:
			// A union value: read its offset into a bare table.
			fn.Call([]reflect.Value{reflect.ValueOf(&flatbuffers.Table{})})
		case ins == 1 && m.Type.In(1).Kind() == reflect.Ptr:
			obj := reflect.New(m.Type.In(1).Elem())
			out := fn.Call([]reflect.Value{obj})
			if len(out) == 1 && out[0].Kind() == reflect.Ptr && !out[0].IsNil() {
				walkAccessors(out[0], depth+1, seen)
			}
		case ins == 2 && m.Type.In(1).Kind() == reflect.Ptr && m.Type.In(2).Kind() == reflect.Int:
			for j := 0; j < lengths[name]; j++ {
				obj := reflect.New(m.Type.In(1).Elem())
				if fn.Call([]reflect.Value{obj, reflect.ValueOf(j)})[0].Bool() {
					walkAccessors(obj, depth+1, seen)
				}
			}
		}
	}
}
