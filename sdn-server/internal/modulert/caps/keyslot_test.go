package caps

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

func TestKeyslotCapHandlerReturnsConfiguredSlotBytes(t *testing.T) {
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-signing": []byte("signing-seed-32-bytes-placeholder"),
		},
	}

	handler := newKeyslotCapHandler(nodeCtx)
	responseEnvelope, err := handler("keyslot.get", []byte(`{"slotId":"provider-signing"}`))
	if err != nil {
		t.Fatalf("keyslot.get returned error: %v", err)
	}

	var response struct {
		Ok     bool `json:"ok"`
		Result struct {
			Type   string `json:"__type"`
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !response.Ok {
		t.Fatalf("expected ok response, got: %s", string(responseEnvelope))
	}

	decoded, err := base64.StdEncoding.DecodeString(response.Result.Base64)
	if err != nil {
		t.Fatalf("decode slot base64: %v", err)
	}
	if string(decoded) != "signing-seed-32-bytes-placeholder" {
		t.Fatalf("unexpected slot bytes: %q", string(decoded))
	}
}
