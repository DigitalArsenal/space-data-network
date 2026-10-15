package sds

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	standardsCLM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CLM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// walkAccessors calls every read accessor flatc generated for v (a pointer to
// a generated table or struct), following sub-tables, structs and vectors.
// It is the property the structural verifier exists to guarantee: on a buffer
// VerifyStructure passed, none of these may panic.
func walkAccessors(v reflect.Value, depth int) {
	if depth > 16 || v.IsNil() {
		return
	}
	typ := v.Type()
	lengths := map[string]int{}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if strings.HasSuffix(m.Name, "Length") && m.Type.NumIn() == 1 && m.Type.NumOut() == 1 && m.Type.Out(0).Kind() == reflect.Int {
			n := int(v.Method(i).Call(nil)[0].Int())
			if n > 64 {
				n = 64
			}
			lengths[strings.TrimSuffix(m.Name, "Length")] = n
		}
	}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		name := m.Name
		if name == "Init" || name == "Table" || strings.HasPrefix(name, "Mutate") || strings.HasSuffix(name, "Length") {
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
		case ins == 1 && m.Type.In(1).Kind() == reflect.Ptr && m.Type.In(1) != reflect.TypeOf(&flatbuffers.Table{}):
			obj := reflect.New(m.Type.In(1).Elem())
			out := fn.Call([]reflect.Value{obj})
			if len(out) == 1 && out[0].Kind() == reflect.Ptr && !out[0].IsNil() {
				walkAccessors(out[0], depth+1)
			}
		case ins == 2 && m.Type.In(1).Kind() == reflect.Ptr && m.Type.In(2).Kind() == reflect.Int:
			for j := 0; j < lengths[name]; j++ {
				obj := reflect.New(m.Type.In(1).Elem())
				if fn.Call([]reflect.Value{obj, reflect.ValueOf(j)})[0].Bool() {
					walkAccessors(obj, depth+1)
				}
			}
		}
	}
}

func accessorsPanic(root func([]byte) reflect.Value, buf []byte) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	walkAccessors(root(buf), 0)
	return ""
}

type fuzzTarget struct {
	schema string
	record []byte
	// root reads buf the way consumers do.
	root func([]byte) reflect.Value
	// verify is the check that consumer relies on.
	verify func(string, []byte) error
}

func converterRecord(t *testing.T, v *Validator, schema, json string, opts wasm.FlatcOption) []byte {
	t.Helper()
	out, err := v.JSONToFlatBuffer(context.Background(), schema, []byte(json), opts)
	if err != nil {
		t.Fatalf("%s from JSON: %v", schema, err)
	}
	return out
}

const fullEPMJSON = `{
 "DN":"CN=node","LEGAL_NAME":"Org","FAMILY_NAME":"F","GIVEN_NAME":"G","ADDITIONAL_NAME":"A",
 "HONORIFIC_PREFIX":"Dr","HONORIFIC_SUFFIX":"III","JOB_TITLE":"Op","OCCUPATION":"Eng",
 "ADDRESS":{"COUNTRY":"US","REGION":"CO","LOCALITY":"Den","POSTAL_CODE":"80202","STREET":"1 Main","POST_OFFICE_BOX_NUMBER":"9"},
 "ALTERNATE_NAMES":["a","bb"],"EMAIL":"e@x","TELEPHONE":"1",
 "KEYS":[{"PUBLIC_KEY":"02ab","KEY_TYPE":"Signing","KEY_PATH":"m/0","ALGORITHM":"secp256k1"},{"PUBLIC_KEY":"03cd","KEY_TYPE":"Encryption"}],
 "MULTIFORMAT_ADDRESS":["/ip4/1.2.3.4/tcp/1"],"SIGNATURE":"sig","SIGNATURE_TIMESTAMP":7,
 "CHAIN_PROOFS":[{"CHAIN":"eth","ADDRESS":"0x1","PUBLIC_KEY":"02","SIGNATURE":"s","SIGNED_PAYLOAD":"p"}],
 "ENTITY_TYPE":"Node","SIGNATURE_ALGORITHM":"ecdsa",
 "DOMAIN_PROOFS":[{"DOMAIN":"x.example","PUBLIC_KEY":"02","SIGNATURE":"s"}],"PHOTO":"ph"}`

// TestStructureVerifiedBuffersNeverPanicAccessors is the regression for
// NET-01/NET-01b at the library level: thousands of deterministic mutations
// of real PNM, EPM and OMM records (single bytes and whole uoffsets). Any
// mutation VerifyStructure accepts must be readable by every generated
// accessor without a panic; and the mutations must actually be dangerous
// (some of them panic the accessors when nothing verifies them first),
// or the test proves nothing.
func TestStructureVerifiedBuffersNeverPanicAccessors(t *testing.T) {
	v := newConverterValidator(t)
	targets := []fuzzTarget{
		{
			schema: "PNM.fbs",
			record: converterRecord(t, v, "PNM.fbs", `{"MULTIFORMAT_ADDRESS":"/ip4/1.2.3.4","PUBLISH_TIMESTAMP":"2026","CID":"bafy","FILE_NAME":"f","FILE_ID":"id","SIGNATURE":"s","TIMESTAMP_SIGNATURE":"t","SIGNATURE_TYPE":"ed","TIMESTAMP_SIGNATURE_TYPE":"ed"}`, wasm.FlatcSizePrefixed),
			root:   func(b []byte) reflect.Value { return reflect.ValueOf(PNM.GetSizePrefixedRootAsPNM(b, 0)) },
			verify: VerifySizePrefixedStructure,
		},
		{
			schema: "EPM.fbs",
			record: converterRecord(t, v, "EPM.fbs", fullEPMJSON, wasm.FlatcSizePrefixed),
			root:   func(b []byte) reflect.Value { return reflect.ValueOf(EPM.GetSizePrefixedRootAsEPM(b, 0)) },
			verify: VerifySizePrefixedStructure,
		},
		{
			schema: "OMM.fbs",
			record: converterRecord(t, v, "OMM.fbs", `{"OBJECT_NAME":"ISS","OBJECT_ID":"1998-067A","NORAD_CAT_ID":25544,"EPOCH":"2026-10-15T00:00:00Z","MEAN_MOTION":15.5,"ECCENTRICITY":0.0001}`, StoredRecordOptions),
			root:   func(b []byte) reflect.Value { return reflect.ValueOf(OMM.GetRootAsOMM(b, 0)) },
			verify: VerifyBareStructure,
		},
	}
	rng := rand.New(rand.NewSource(20261015))
	for _, tg := range targets {
		if err := tg.verify(tg.schema, tg.record); err != nil {
			t.Fatalf("%s: the well-formed record fails verification: %v", tg.schema, err)
		}
		if msg := accessorsPanic(tg.root, tg.record); msg != "" {
			t.Fatalf("%s: the well-formed record panics accessors: %s", tg.schema, msg)
		}
		var mutated, accepted, dangerous int
		try := func(buf []byte) {
			mutated++
			unverifiedPanic := accessorsPanic(tg.root, buf) != ""
			if unverifiedPanic {
				dangerous++
			}
			if tg.verify(tg.schema, buf) != nil {
				return
			}
			accepted++
			if msg := accessorsPanic(tg.root, buf); msg != "" {
				t.Fatalf("%s: a buffer VerifyStructure accepted panics accessors: %s\nbuffer: %x", tg.schema, msg, buf)
			}
		}
		for i := range tg.record {
			for _, val := range []byte{0x00, 0x01, 0x7f, 0x80, 0xff, byte(rng.Intn(256))} {
				buf := append([]byte(nil), tg.record...)
				buf[i] = val
				try(buf)
			}
		}
		for i := 0; i+4 <= len(tg.record); i++ {
			for _, val := range []uint32{0xffffffff, 0x7ffffff0, 0x80000000, uint32(len(tg.record)), uint32(len(tg.record) - i), uint32(rng.Int31())} {
				buf := append([]byte(nil), tg.record...)
				binary.LittleEndian.PutUint32(buf[i:], val)
				try(buf)
			}
		}
		t.Logf("%s: %d mutations, %d accepted (all safe), %d panic accessors when unverified", tg.schema, mutated, accepted, dangerous)
		if dangerous == 0 {
			t.Fatalf("%s: no mutation was dangerous; the fuzz does not exercise the accessors", tg.schema)
		}
	}
}

// The NET-01 PNM itself: the exact 32-byte shape the re-audit published.
func craftNET01PNM() []byte {
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

func TestValidateRefusesTheNET01PNM(t *testing.T) {
	bad := craftNET01PNM()
	v, err := NewValidator(nil) // the envelope-only validator a default node runs
	if err != nil {
		t.Fatal(err)
	}
	if err := v.VerifyEnvelope("PNM.fbs", bad); err != nil {
		t.Fatalf("fixture must pass the envelope check (that is the bug): %v", err)
	}
	if err := v.Validate(context.Background(), "PNM.fbs", bad); !errors.Is(err, fbverify.ErrInvalid) {
		t.Fatalf("Validate(NET-01 PNM) = %v, want the structural verifier's refusal", err)
	}
	if err := VerifySizePrefixedStructure("PNM.fbs", bad); !errors.Is(err, fbverify.ErrInvalid) {
		t.Fatalf("VerifySizePrefixedStructure = %v", err)
	}
	if err := VerifyStructure("PNM.fbs", bad); !errors.Is(err, fbverify.ErrInvalid) {
		t.Fatalf("VerifyStructure = %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture no longer panics the accessor; the regression would prove nothing")
			}
		}()
		_ = PNM.GetSizePrefixedRootAsPNM(bad, 0).CID()
	}()
}

// A consumer committed to one envelope form must verify that form: a valid
// BARE $PNM read as size-prefixed takes its root from the identifier bytes.
func TestFormSpecificVerificationRefusesTheOtherForm(t *testing.T) {
	sizePrefixed := NewPNMBuilder().Build()
	if err := VerifySizePrefixedStructure("PNM.fbs", sizePrefixed); err != nil {
		t.Fatalf("size-prefixed PNM: %v", err)
	}
	bare := sizePrefixed[4:]
	if err := VerifyBareStructure("PNM.fbs", bare); err != nil {
		t.Fatalf("bare PNM: %v", err)
	}
	if err := VerifySizePrefixedStructure("PNM.fbs", bare); err == nil {
		t.Fatal("a bare PNM passed as size-prefixed")
	}
	if err := VerifyBareStructure("PNM.fbs", sizePrefixed); err == nil {
		t.Fatal("a size-prefixed PNM passed as bare")
	}
	if err := VerifyBareStructure("UNKNOWN.fbs", bare); err == nil {
		t.Fatal("an unknown schema verified")
	}
}

// Every standard the validator holds — the embedded IDL and the published
// binding-only ones — must compile in the structural verifier, or Validate
// refuses every record of it.
func TestEveryValidatorSchemaHasAStructuralVerifier(t *testing.T) {
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	names := append(v.Schemas(), "CLM.fbs")
	for _, name := range names {
		if _, err := v.structure.Schema(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := structureRegistry().Schema(name); err != nil {
			t.Errorf("default registry %s: %v", name, err)
		}
	}
}

// The binding-only CLM IDL must match the published binding slot for slot:
// a record built with the binding verifies, and corrupting the offsets the
// binding wrote is caught.
func TestBindingOnlyCLMIDLMatchesItsBinding(t *testing.T) {
	b := flatbuffers.NewBuilder(256)
	peerID := b.CreateString("12D3KooWPeer")
	profile := b.CreateString("bafyprofile")
	sig := b.CreateByteVector([]byte{1, 2, 3})
	standardsCLM.CLMCountersignatureStart(b)
	standardsCLM.CLMCountersignatureAddPEER_ID(b, peerID)
	standardsCLM.CLMCountersignatureAddPROFILE_CID(b, profile)
	standardsCLM.CLMCountersignatureAddSIGNED_AT(b, 9)
	standardsCLM.CLMCountersignatureAddSIGNATURE(b, sig)
	cs := standardsCLM.CLMCountersignatureEnd(b)
	standardsCLM.CLMStartCOUNTERSIGNATURESVector(b, 1)
	b.PrependUOffsetT(cs)
	css := b.EndVector(1)
	strs := map[string]flatbuffers.UOffsetT{}
	for _, s := range []string{"claim", "claimant", "cid", "obj", "name", "stmt", "url"} {
		strs[s] = b.CreateString(s)
	}
	csig := b.CreateByteVector([]byte{4, 5})
	standardsCLM.CLMStart(b)
	standardsCLM.CLMAddCLAIM_ID(b, strs["claim"])
	standardsCLM.CLMAddCLAIMANT_PEER_ID(b, strs["claimant"])
	standardsCLM.CLMAddCLAIMANT_PROFILE_CID(b, strs["cid"])
	standardsCLM.CLMAddOBJECT_KIND(b, 1 /* Satellite */)
	standardsCLM.CLMAddOBJECT_ID(b, strs["obj"])
	standardsCLM.CLMAddOBJECT_NAME(b, strs["name"])
	standardsCLM.CLMAddROLE(b, 1 /* Owner */)
	standardsCLM.CLMAddSTATEMENT(b, strs["stmt"])
	standardsCLM.CLMAddEVIDENCE_URL(b, strs["url"])
	standardsCLM.CLMAddCREATED_AT(b, 1)
	standardsCLM.CLMAddUPDATED_AT(b, 2)
	standardsCLM.CLMAddDELETED(b, true)
	standardsCLM.CLMAddSIGNATURE(b, csig)
	standardsCLM.CLMAddCOUNTERSIGNATURES(b, css)
	root := standardsCLM.CLMEnd(b)
	standardsCLM.FinishCLMBuffer(b, root)
	buf := b.FinishedBytes()
	if err := VerifyStructure("CLM.fbs", buf); err != nil {
		t.Fatalf("binding-built CLM: %v", err)
	}
	if msg := accessorsPanic(func(b []byte) reflect.Value { return reflect.ValueOf(standardsCLM.GetRootAsCLM(b, 0)) }, buf); msg != "" {
		t.Fatalf("binding-built CLM panics accessors: %s", msg)
	}
	// Point COUNTERSIGNATURES (slot 13) outside: the IDL must know that slot
	// is a table vector, or this would pass.
	bad := append([]byte(nil), buf...)
	rootPos := int(binary.LittleEndian.Uint32(bad))
	vt := rootPos - int(int32(binary.LittleEndian.Uint32(bad[rootPos:])))
	fo := int(binary.LittleEndian.Uint16(bad[vt+4+2*13:]))
	vec := rootPos + fo + int(binary.LittleEndian.Uint32(bad[rootPos+fo:]))
	binary.LittleEndian.PutUint32(bad[vec+4:], 0x7ffff000)
	if err := VerifyStructure("CLM.fbs", bad); !errors.Is(err, fbverify.ErrInvalid) {
		t.Fatalf("corrupt countersignature offset: %v", err)
	}
}
