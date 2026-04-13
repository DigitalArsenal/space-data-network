package caps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// NewKeyslotCapFactory returns a CapFactory for the manifest-level "wallet_sign"
// capability. The current plugin manifest schema has no dedicated keyslot
// capability, so raw keyslot host operations are gated behind wallet_sign until
// the manifest schema is standardized upstream.
func NewKeyslotCapFactory() modulert.CapFactory {
	return func(mod *modulert.Module) modulert.CapHandler {
		if mod == nil {
			return newKeyslotCapHandler(nil)
		}
		return newKeyslotCapHandler(mod.NodeContext())
	}
}

func newKeyslotCapHandler(nodeCtx *modulert.NodeContext) modulert.CapHandler {
	return func(operation string, payload []byte) ([]byte, error) {
		if operation != "keyslot.get" {
			return errCapJSON(fmt.Sprintf("unknown keyslot operation: %s", operation)), nil
		}
		if nodeCtx == nil {
			return errCapJSON("keyslot context is not available"), nil
		}

		var request struct {
			SlotID string `json:"slotId"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return errCapJSON("invalid keyslot request payload: " + err.Error()), nil
		}
		slotID := strings.TrimSpace(request.SlotID)
		if slotID == "" {
			return errCapJSON("missing slotId"), nil
		}

		keySlots := nodeCtx.KeySlots
		if len(keySlots) == 0 {
			return errCapJSON("keyslot map is not configured"), nil
		}
		raw, ok := keySlots[slotID]
		if !ok || len(raw) == 0 {
			return errCapJSON("keyslot not found"), nil
		}
		cloned := append([]byte(nil), raw...)
		return okCapRaw(cloned), nil
	}
}
