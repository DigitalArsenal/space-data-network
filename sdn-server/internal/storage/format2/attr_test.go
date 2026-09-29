package format2

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// The router's RecordAttr bytes equal the engine's buildRecordAttr for the
// same inputs (flatsql cpp/test/ps/vectors/record_attr.hex, 3.1.0).
func TestRecordAttrMatchesTheEngineGoldenBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/record_attr.hex")
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, h, _ := strings.Cut(line, " ")
		golden[name] = h
	}
	cases := map[string]RecordAttr{
		// The C++ case passes "src:source-b\x00key-f" through a const char*,
		// so the key it stores stops at the NUL.
		"full": {PeerID: []byte("12D3KooWPeer"), SupersedeKey: "src:source-b", SourceTimestamp: 1780000000123,
			LicenceKey: "licence-g", Tag: SourceTag{ProviderID: "provider-a", SourceName: "source-b", BatchID: "batch-c",
				ContentKeyID: "content-d", ProducerPeerID: "12D3KooWProducer", ProducerPublicKey: "producer-key-e"}},
		"tag_partial": {PeerID: []byte("peer"), Tag: SourceTag{ProviderID: "prov", BatchID: "b1"}},
		"no_tag":      {PeerID: []byte("peer-only")},
	}
	for name, a := range cases {
		want, ok := golden[name]
		if !ok {
			t.Fatalf("golden vector %q missing", name)
		}
		if got := hex.EncodeToString(BuildRecordAttr(a)); got != want {
			t.Errorf("%s:\n got  %s\n want %s", name, got, want)
		}
	}
}
