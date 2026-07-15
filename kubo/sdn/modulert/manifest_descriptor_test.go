package modulert

import "testing"

func TestRuntimeManifestDescriptorIncludesMethodPortMetadata(t *testing.T) {
	descriptor := runtimeManifestDescriptor(&Manifest{
		PluginID: "licensing",
		Methods: []ManifestMethod{
			{
				MethodID:    "server_configure_runtime",
				DisplayName: "Configure runtime",
				Description: "Configure module runtime state",
				InputPorts: []ManifestPort{
					{
						PortID:      "request",
						DisplayName: "Request",
						MinStreams:  1,
						MaxStreams:  1,
						Required:    true,
						AcceptedTypeSets: []ManifestAcceptedTypeSet{
							{
								SetID: "licensing-config",
								AllowedTypes: []ManifestFlatBufferTypeRef{
									{
										SchemaName:     "MODULE.fbs",
										FileIdentifier: "MODL",
										SchemaVersion:  "1.0.0",
										RootType:       "ConfigureRuntimeRequest",
									},
								},
								AllowedWireFormats: []string{"FLATBUFFER"},
								Description:        "Configuration request",
							},
						},
					},
				},
				OutputPorts: []ManifestPort{
					{
						PortID:      "response",
						DisplayName: "Response",
						Required:    true,
					},
				},
				MaxBatch:    4,
				DrainPolicy: "DRAIN_UNTIL_YIELD",
			},
		},
	})

	if descriptor == nil || len(descriptor.Methods) != 1 {
		t.Fatalf("descriptor = %#v, want one method", descriptor)
	}
	method := descriptor.Methods[0]
	if got, want := method.MaxBatch, uint32(4); got != want {
		t.Fatalf("max batch = %d, want %d", got, want)
	}
	if got, want := method.DrainPolicy, "DRAIN_UNTIL_YIELD"; got != want {
		t.Fatalf("drain policy = %q, want %q", got, want)
	}
	if len(method.InputPorts) != 1 || len(method.OutputPorts) != 1 {
		t.Fatalf("ports = input %#v output %#v, want input and output port", method.InputPorts, method.OutputPorts)
	}
	port := method.InputPorts[0]
	if got, want := port.PortID, "request"; got != want {
		t.Fatalf("port id = %q, want %q", got, want)
	}
	if !port.Required || port.MinStreams != 1 || port.MaxStreams != 1 {
		t.Fatalf("port cardinality = %#v, want required single stream", port)
	}
	if len(port.AcceptedTypeSets) != 1 || len(port.AcceptedTypeSets[0].AllowedTypes) != 1 {
		t.Fatalf("accepted type sets = %#v, want one allowed type", port.AcceptedTypeSets)
	}
	allowedType := port.AcceptedTypeSets[0].AllowedTypes[0]
	if got, want := allowedType.RootType, "ConfigureRuntimeRequest"; got != want {
		t.Fatalf("root type = %q, want %q", got, want)
	}
	if got, want := port.AcceptedTypeSets[0].AllowedWireFormats[0], "FLATBUFFER"; got != want {
		t.Fatalf("wire format = %q, want %q", got, want)
	}
}
