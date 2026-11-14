package modulert

import (
	"bytes"
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
