package modulert

import "fmt"

// scheduledInputFrames resolves the single scheduled payload against its method.
// Multiple ports or alternative concrete types require explicit frame dispatch.
func scheduledInputFrames(manifest *Manifest, methodID string, payload []byte) ([]InvokeInputFrame, error) {
	if manifest == nil {
		return nil, fmt.Errorf("scheduled method %q has no manifest", methodID)
	}
	for _, method := range manifest.Methods {
		if method.MethodID != methodID {
			continue
		}
		if len(method.InputPorts) == 0 {
			if len(payload) != 0 {
				return nil, fmt.Errorf("scheduled method %q declares no input", methodID)
			}
			return nil, nil
		}
		if len(method.InputPorts) != 1 {
			return nil, fmt.Errorf("scheduled method %q requires explicit multi-port input", methodID)
		}
		port := method.InputPorts[0]
		var selected *ManifestFlatBufferTypeRef
		for _, set := range port.AcceptedTypeSets {
			for _, typ := range set.AllowedTypes {
				if typ.SchemaName == "" || typ.FileIdentifier == "" || typ.RootType == "" {
					return nil, fmt.Errorf("scheduled method %q input has an incomplete type", methodID)
				}
				if selected != nil && *selected != typ {
					return nil, fmt.Errorf("scheduled method %q input has ambiguous types", methodID)
				}
				value := typ
				selected = &value
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("scheduled method %q input has no concrete type", methodID)
		}
		return []InvokeInputFrame{{PortID: port.PortID, Payload: payload,
			SchemaName: selected.SchemaName, FileIdentifier: selected.FileIdentifier,
			RootTypeName: selected.RootType, WireFormat: payloadWireFormatFlatbuffer}}, nil
	}
	return nil, fmt.Errorf("scheduled method %q is not declared", methodID)
}
