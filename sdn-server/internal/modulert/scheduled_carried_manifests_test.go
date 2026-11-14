package modulert

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestScheduledCarriedFlowManifests(t *testing.T) {
	for _, path := range []string{
		"../../cmd/cron-robustness-e2e/testdata/retrieval-flow.wasm",
		"../../cmd/manual-run-backoff-e2e/testdata/retention-flow.wasm",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wantID := "com.digitalarsenal.flows.celestrak-gp-ingest"
			if bytes.Contains([]byte(path), []byte("retention-flow")) {
				wantID = "com.digitalarsenal.flows.record-retention"
			}
			var manifest *Manifest
			// SDK artifacts carry their PLG FlatBuffer verbatim in a data segment.
			for at := 4; at+8 < len(raw); at++ {
				if !bytes.Equal(raw[at:at+4], []byte("$PLG")) {
					continue
				}
				candidate := raw[at-4:]
				root := int(binary.LittleEndian.Uint32(candidate))
				if root < 8 || root > 256 || root+4 > len(candidate) {
					continue
				}
				back := int(int32(binary.LittleEndian.Uint32(candidate[root:])))
				if back <= 0 || back > root {
					continue
				}
				m, err := parseManifestFlatBuffer(candidate)
				if err == nil && m != nil && m.PluginID == wantID && len(m.Methods) > 0 {
					manifest = m
					break
				}
			}
			if manifest == nil {
				t.Fatal("embedded PLG manifest not found")
			}
			for _, method := range manifest.Methods {
				frames, err := scheduledInputFrames(manifest, method.MethodID, nil)
				if err != nil || len(frames) != 1 {
					t.Fatalf("%s:%s %v %v", manifest.PluginID, method.MethodID, frames, err)
				}
				if frames[0].SchemaName != "TimerTick.fbs" || frames[0].FileIdentifier != "TICK" || frames[0].RootTypeName != "TimerTick" {
					t.Fatalf("%s:%s timer contract: %+v", manifest.PluginID, method.MethodID, frames)
				}
				t.Logf("%s:%s typed", manifest.PluginID, method.MethodID)
			}
		})
	}
}
