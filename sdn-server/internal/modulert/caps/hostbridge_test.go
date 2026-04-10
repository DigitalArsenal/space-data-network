package caps

import (
	"encoding/json"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

func TestHostBridgeListOperationsIncludesProtocolRequestOnlyWhenRegistered(t *testing.T) {
	bridge := modulert.NewHostBridge(nil, []string{"protocol_dial"})

	decodeOperations := func(raw []byte) []string {
		t.Helper()

		var envelope struct {
			Ok     bool     `json:"ok"`
			Result []string `json:"result"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode operations response: %v", err)
		}
		if !envelope.Ok {
			t.Fatalf("expected ok response, got: %s", string(raw))
		}
		return envelope.Result
	}

	contains := func(values []string, target string) bool {
		for _, value := range values {
			if value == target {
				return true
			}
		}
		return false
	}

	initial := decodeOperations(bridge.Dispatch("host.listOperations", nil))
	if contains(initial, "protocol.request") {
		t.Fatalf("protocol.request should not be listed before a protocol handler is registered")
	}

	bridge.RegisterCapHandler("protocol", func(operation string, payload []byte) ([]byte, error) {
		return []byte(`{"ok":true,"result":true}`), nil
	})

	registered := decodeOperations(bridge.Dispatch("host.listOperations", nil))
	if !contains(registered, "protocol.request") {
		t.Fatalf("protocol.request should be listed after a protocol handler is registered")
	}
}
