package modulert

import (
	"bytes"
	"strings"
	"testing"
)

func TestInvokeMethodOutputsExtractsEveryPortInOrder(t *testing.T) {
	arena := []byte("states---report")
	response := &pluginInvokeResponse{
		OutputFrames: []pluginInvokeFrame{
			{PortID: "states", Offset: 0, Size: 6},
			{PortID: "report", Offset: 9, Size: 6},
		},
		PayloadArena: arena,
	}
	outputs, err := extractPluginInvokeOutputs(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(outputs))
	}
	if outputs[0].PortID != "states" || !bytes.Equal(outputs[0].Payload, []byte("states")) {
		t.Fatalf("states output = %+v", outputs[0])
	}
	if outputs[1].PortID != "report" || !bytes.Equal(outputs[1].Payload, []byte("report")) {
		t.Fatalf("report output = %+v", outputs[1])
	}

	// Returned frames own their payload bytes rather than aliasing guest arena
	// storage that is freed as soon as InvokeMethodOutputs returns.
	response.PayloadArena[0] = 'X'
	if string(outputs[0].Payload) != "states" {
		t.Fatalf("output payload aliases response arena: %q", outputs[0].Payload)
	}
}

func TestInvokeMethodOutputsRejectsInvalidFrameBounds(t *testing.T) {
	_, err := extractPluginInvokeOutputs(&pluginInvokeResponse{
		OutputFrames: []pluginInvokeFrame{{PortID: "states", Offset: 2, Size: 4}},
		PayloadArena: []byte{1, 2, 3},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds payload arena") {
		t.Fatalf("error = %v, want output bounds failure", err)
	}
}
