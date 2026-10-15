package sds

import (
	"context"
	"reflect"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// fuzzTargets are the network-facing standards in the forms their consumers
// read them: an announcement ($PNM) and a profile ($EPM) size-prefixed, a
// dataset manifest ($DPM) bare. The first fuzz byte picks the target.
var fuzzTargets = []struct {
	schema string
	root   func([]byte) reflect.Value
	verify func(string, []byte) error
}{
	{"PNM.fbs", func(b []byte) reflect.Value { return reflect.ValueOf(PNM.GetSizePrefixedRootAsPNM(b, 0)) }, VerifySizePrefixedStructure},
	{"EPM.fbs", func(b []byte) reflect.Value { return reflect.ValueOf(EPM.GetSizePrefixedRootAsEPM(b, 0)) }, VerifySizePrefixedStructure},
	{"DPM.fbs", func(b []byte) reflect.Value { return reflect.ValueOf(DPM.GetRootAsDPM(b, 0)) }, VerifyBareStructure},
}

const fullDPMJSON = `{
 "VERSION":"1","DATASET_ID":"ds","UPDATE_ID":"u1","FILE_ID":"f","PROVIDER_PEER_ID":"12D3KooWP","PROVIDER_EPM_CID":"bafy",
 "PUBLISH_TIMESTAMP":"2026-10-15T00:00:00Z",
 "ASSETS":[{"CID":"bafyA","FILE_NAME":"a","BYTE_LENGTH":9,"SCHEMA_NAME":"OMM.fbs"}],
 "SOURCES":[{"SOURCE_NAME":"s","WARNINGS":["w1","w2"],"RECORD_COUNT":3}],
 "QUERY":{"CANONICAL_QUERY":"q","QUERY_SHA256":"qh","RESULT_SHA256":"rh","SCHEMA_NAMES":["OMM.fbs"],"PROVIDER_IDS":["p"],"SOURCE_NAMES":["s"],"BATCH_IDS":["b"]},
 "INDEXES":[{"INDEX_NAME":"i","CANONICAL_ORDER":"o","INDEX_ROOT":"r"}],
 "ENCRYPTION":{"ENCRYPTED":true,"ALGORITHM":"aes"},
 "PROVIDER_SIGNATURE":[1,2,3],"SIGNATURE_TYPE":"Ed25519"}`

// FuzzStructureVerifiedBuffersNeverPanicAccessors is the coverage-guided
// form of TestStructureVerifiedBuffersNeverPanicAccessors (security
// re-audit NET-01/NET-01b): whatever bytes the structural verifier accepts,
// every generated accessor must read without a panic. Without -fuzz it runs
// its seeds; `go test -run '^$' -fuzz FuzzStructureVerified ./internal/sds`
// searches for a counterexample.
func FuzzStructureVerifiedBuffersNeverPanicAccessors(f *testing.F) {
	fm, err := wasm.NewEmbeddedFlatcModule(context.Background())
	if err != nil {
		f.Fatalf("NewEmbeddedFlatcModule: %v", err)
	}
	v, err := NewValidator(fm)
	if err != nil {
		f.Fatalf("NewValidator: %v", err)
	}
	seed := func(target byte, schema, json string, opts wasm.FlatcOption) {
		out, err := v.JSONToFlatBuffer(context.Background(), schema, []byte(json), opts)
		if err != nil {
			f.Fatalf("%s seed: %v", schema, err)
		}
		f.Add(append([]byte{target}, out...))
	}
	seed(0, "PNM.fbs", `{"MULTIFORMAT_ADDRESS":"/ip4/1.2.3.4","PUBLISH_TIMESTAMP":"2026","CID":"bafy","FILE_NAME":"f","FILE_ID":"id","SIGNATURE":"s","TIMESTAMP_SIGNATURE":"t","SIGNATURE_TYPE":"ed","TIMESTAMP_SIGNATURE_TYPE":"ed"}`, wasm.FlatcSizePrefixed)
	seed(1, "EPM.fbs", fullEPMJSON, wasm.FlatcSizePrefixed)
	seed(2, "DPM.fbs", fullDPMJSON, StoredRecordOptions)
	fm.Close(context.Background())
	f.Add(append([]byte{0}, craftNET01PNM()...))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		tg := fuzzTargets[int(data[0])%len(fuzzTargets)]
		buf := data[1:]
		if tg.verify(tg.schema, buf) != nil {
			return
		}
		if msg := accessorsPanic(tg.root, buf); msg != "" {
			t.Fatalf("%s: a buffer the structural verifier accepted panics accessors: %s\nbuffer: %x", tg.schema, msg, buf)
		}
	})
}
