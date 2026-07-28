package modulert

import (
	"encoding/json"
	"strings"
	"testing"
)

// HTML entity needles, written as escapes so nothing in an editor or pipeline
// can decode them inside this source file.
const (
	htmlAmpEntity = "&amp;"
	htmlLtEntity  = "&lt;"
	htmlGtEntity  = "&gt;"
)

// A hostcall response is a length-prefixed byte payload read by a wasm guest,
// not an HTML document. encoding/json's default HTML escaping turned every "&"
// in a configured value into the HTML ampersand entity, which silently rewrote
// an operator's source URL: "gp.php?GROUP=stations&FORMAT=csv" reached the
// guest as "gp.php?GROUP=stationsu0026FORMAT=csv" (the guest's minimal JSON
// reader does not decode \u escapes), and the node then burned its entire
// timeout dialling a host that does not exist.
func TestGetConfigDoesNotHTMLEscapeConfiguredValues(t *testing.T) {
	const url = "https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=csv"

	raw := okJSON(map[string]interface{}{
		"celestrak_gp_url": url,
		"angle_brackets":   "<a> & </a>",
	})

	for _, entity := range []string{htmlAmpEntity, htmlLtEntity, htmlGtEntity} {
		if strings.Contains(string(raw), entity) {
			t.Fatalf("hostcall response HTML-escaped %q: %s", entity, raw)
		}
	}
	if !strings.Contains(string(raw), url) {
		t.Fatalf("configured URL not delivered verbatim: %s", raw)
	}

	// Still valid JSON, still the ok/result envelope, and with no trailing
	// newline (the guest reads exactly the advertised length).
	if raw[len(raw)-1] == '\n' {
		t.Fatalf("hostcall response ends with a stray newline: %q", raw)
	}
	var envelope struct {
		OK     bool              `json:"ok"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, raw)
	}
	if !envelope.OK || envelope.Result["celestrak_gp_url"] != url {
		t.Fatalf("decoded envelope = %+v", envelope)
	}
}

// The same encoder serves plugin.getConfig, which is how an operator retargets
// a data source. Exercise it through the real dispatch path.
func TestPluginGetConfigDeliversURLsVerbatim(t *testing.T) {
	const url = "https://celestrak.org/satcat/records.php?GROUP=stations&FORMAT=csv"
	bridge := &HostBridge{nodeCtx: &NodeContext{Config: map[string]interface{}{
		"celestrak_satcat_url": url,
	}}}

	raw := bridge.Dispatch("plugin.getConfig", []byte("{}"))
	if strings.Contains(string(raw), htmlAmpEntity) {
		t.Fatalf("plugin.getConfig HTML-escaped the configured URL: %s", raw)
	}
	if !strings.Contains(string(raw), url) {
		t.Fatalf("plugin.getConfig mangled the configured URL: %s", raw)
	}
}
