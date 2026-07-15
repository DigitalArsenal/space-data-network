package sdnservices

import (
	"encoding/base64"
	"encoding/json"
)

// The capability hostcall response envelope. These mirror the shape the
// sdn-server caps used ({"ok":true,"result":...} / {"ok":false,"error":{...}})
// so a guest module's host-side response decoder behaves identically here.
// They are re-implemented locally (rather than imported from sdn-server) so the
// kubo module tree carries no dependency on sdn-server internals.

func okCapJSON(result interface{}) []byte {
	r, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return r
}

func errCapJSON(msg string) []byte {
	r, _ := json.Marshal(map[string]interface{}{
		"ok":    false,
		"error": map[string]string{"message": msg},
	})
	return r
}

// decodeBase64Cap decodes a base64 field, tolerating both standard and URL
// alphabets and missing padding (guest SDKs vary). Returns nil on failure.
func decodeBase64Cap(s string) []byte {
	if s == "" {
		return nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b
		}
	}
	return nil
}

// encodeBase64Cap standard-base64-encodes raw bytes for a JSON response field.
func encodeBase64Cap(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
