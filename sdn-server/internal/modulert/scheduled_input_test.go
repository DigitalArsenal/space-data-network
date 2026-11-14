package modulert

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func scheduledTestManifest() *Manifest {
	return &Manifest{Methods: []ManifestMethod{{MethodID: "run", InputPorts: []ManifestPort{{PortID: "tick", AcceptedTypeSets: []ManifestAcceptedTypeSet{{AllowedTypes: []ManifestFlatBufferTypeRef{{SchemaName: "TimerTick.fbs", FileIdentifier: "TICK", RootType: "TimerTick"}}}}}}}}}
}
func TestScheduledInputConcreteContract(t *testing.T) {
	payload := []byte{1, 2, 3}
	frames, err := scheduledInputFrames(scheduledTestManifest(), "run", payload)
	if err != nil || len(frames) != 1 {
		t.Fatalf("%v %v", frames, err)
	}
	want := []InvokeInputFrame{{PortID: "tick", Payload: payload, SchemaName: "TimerTick.fbs", FileIdentifier: "TICK", RootTypeName: "TimerTick", WireFormat: payloadWireFormatFlatbuffer}}
	gotBytes, err := encodePluginInvokeRequestFrames("run", frames)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := encodePluginInvokeRequestFrames("run", want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatal("typed invocation differs")
	}
}
func TestScheduledInputLegacyCompatibility(t *testing.T) {
	cases := map[string]func(*Manifest){
		"no-ports": func(m *Manifest) { m.Methods[0].InputPorts = nil },
		"multiple-ports": func(m *Manifest) {
			m.Methods[0].InputPorts = append(m.Methods[0].InputPorts, m.Methods[0].InputPorts[0])
		},
		"multiple-types": func(m *Manifest) {
			s := &m.Methods[0].InputPorts[0].AcceptedTypeSets[0]
			s.AllowedTypes = append(s.AllowedTypes, ManifestFlatBufferTypeRef{SchemaName: "Other.fbs", FileIdentifier: "OTHR", RootType: "Other"})
		},
		"duplicate-types": func(m *Manifest) {
			s := &m.Methods[0].InputPorts[0].AcceptedTypeSets[0]
			s.AllowedTypes = append(s.AllowedTypes, s.AllowedTypes[0])
		},
		"no-types":              func(m *Manifest) { m.Methods[0].InputPorts[0].AcceptedTypeSets = nil },
		"incomplete-schema":     func(m *Manifest) { m.Methods[0].InputPorts[0].AcceptedTypeSets[0].AllowedTypes[0].SchemaName = "" },
		"incomplete-identifier": func(m *Manifest) { m.Methods[0].InputPorts[0].AcceptedTypeSets[0].AllowedTypes[0].FileIdentifier = "" },
		"incomplete-root":       func(m *Manifest) { m.Methods[0].InputPorts[0].AcceptedTypeSets[0].AllowedTypes[0].RootType = "" },
		"empty-port":            func(m *Manifest) { m.Methods[0].InputPorts[0].PortID = "" },
		"missing-method":        func(m *Manifest) { m.Methods = nil },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			for _, payload := range [][]byte{nil, {}, {1, 2, 3}} {
				m := scheduledTestManifest()
				alter(m)
				frames, err := scheduledInputFrames(m, "run", payload)
				if err != nil {
					t.Fatal(err)
				}
				if len(frames) != 1 || !bytes.Equal(frames[0].Payload, payload) {
					t.Fatalf("lost frame or payload: %+v", frames)
				}
				got, err := encodePluginInvokeRequestFrames("run", frames)
				if err != nil {
					t.Fatal(err)
				}
				want, err := encodePluginInvokeRequestFrames("run", []InvokeInputFrame{{PortID: "request", Payload: payload}})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatal("legacy invocation bytes changed")
				}
			}
		})
	}
	frames, err := scheduledInputFrames(nil, "run", nil)
	if err != nil || len(frames) != 1 || frames[0].PortID != "request" {
		t.Fatal("nil manifest lost legacy frame")
	}
}

// Contracts are extracted from the module collection's scheduled manifests.
// Fixture names identify every source manifest and timer, including flow bundles.
func TestScheduledManifestInventory(t *testing.T) {
	raw, err := os.ReadFile("testdata/scheduled-input-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, MethodID string
		Manifest       Manifest
		Typed          bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty manifest inventory")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			payload := []byte{1, 2, 3}
			frames, err := scheduledInputFrames(&tc.Manifest, tc.MethodID, payload)
			if err != nil || len(frames) != 1 {
				t.Fatalf("%+v %v", frames, err)
			}
			if tc.Typed {
				port := tc.Manifest.Methods[0].InputPorts[0]
				typ := port.AcceptedTypeSets[0].AllowedTypes[0]
				if frames[0].PortID != port.PortID || frames[0].SchemaName != typ.SchemaName || frames[0].RootTypeName != typ.RootType || frames[0].FileIdentifier != typ.FileIdentifier {
					t.Fatalf("wrong typed frame: %+v", frames)
				}
				t.Log("typed")
			} else {
				got, err := encodePluginInvokeRequestFrames(tc.MethodID, frames)
				if err != nil {
					t.Fatal(err)
				}
				want, err := encodePluginInvokeRequestFrames(tc.MethodID, []InvokeInputFrame{{PortID: "request", Payload: payload}})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatal("legacy bytes changed")
				}
				t.Log("legacy")
			}
		})
	}
}
