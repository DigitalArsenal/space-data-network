package modulert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	APPfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"os"
	"testing"
)

func TestModuleApplicationUnavailable(t *testing.T) {
	m := &Module{}
	if _, err := m.ApplicationRecord(); err == nil {
		t.Fatal("unloaded module exposed an app")
	}
	m.mod = &wasmrt.Module{}
	m.manifest = &Manifest{PluginID: "test"}
	m.wasmBytes = wasmHeader
	if _, err := m.ApplicationRecord(); !errors.Is(err, ErrNoModuleApplication) {
		t.Fatalf("plain module: %v", err)
	}
	m.paused = true
	if _, err := m.ApplicationArtifact(); err == nil {
		t.Fatal("paused module exposed artifact")
	}
}

// SDN_MODULE_APP_WASM points at the SDK-built bundle. This exercises the same
// native loader, bundle verifier and APP decoder used by a running node.
func TestModuleApplicationSDKBundle(t *testing.T) {
	path := os.Getenv("SDN_MODULE_APP_WASM")
	if path == "" {
		t.Skip("set SDN_MODULE_APP_WASM to a compiled SDK APP bundle")
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(artifact, nil, &NodeContext{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	appBytes, err := m.ApplicationRecord()
	if err != nil {
		t.Fatal(err)
	}
	app := APPfb.GetSizePrefixedRootAsAPP(appBytes, 0)
	portable, err := m.ApplicationArtifact()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(portable)
	var ref APPfb.APPModuleRef
	if !app.MODULES(&ref, 0) || string(ref.CONTENT_HASH()) != hex.EncodeToString(hash[:]) {
		t.Fatal("native/browser artifact mismatch")
	}
	if m.UIDescriptor().URL == "" {
		t.Fatal("APP module was not made launchable")
	}
	// The native host must execute the portable payload that the browser gets.
	if m.manifest.PluginID == "com.digitalarsenal.analysis.catalog-composer" {
		b := flatbuffers.NewBuilder(128)
		name := b.CreateString("Native parity object")
		b.StartObject(3)
		b.PrependUOffsetTSlot(0, name, 0)
		b.PrependUint32Slot(2, 25544, 0)
		root := b.EndObject()
		b.FinishSizePrefixedWithFileIdentifier(root, []byte("$CAT"))
		cat := append([]byte(nil), b.FinishedBytes()...)
		recipe := []byte(`{"version":1,"layers":[{"id":"a","node":"peer-a","provider":"celestrak","source":"satcat","head":"test-snapshot"}],"stateSources":[],"asOf":1000,"maxAgeSeconds":100,"overrides":{}}`)
		output, invokeErr := m.InvokeMethodFrames(context.Background(), "compose", []InvokeInputFrame{
			{PortID: "recipe", Payload: recipe, WireFormat: payloadWireFormatAlignedBinary, ByteLength: uint32(len(recipe)), RequiredAlignment: 1},
			{PortID: "catalogs", Payload: cat, SchemaName: "CAT.fbs", FileIdentifier: "$CAT", RootTypeName: "CAT"},
		})
		var result struct {
			ObjectCount int `json:"objectCount"`
			Objects     []struct {
				Key           string `json:"key"`
				Name          string `json:"name"`
				SelectedLayer string `json:"selectedLayer"`
			} `json:"objects"`
		}
		if invokeErr != nil || json.Unmarshal(output, &result) != nil || result.ObjectCount != 1 || len(result.Objects) != 1 || result.Objects[0].Key != "norad:25544" || result.Objects[0].Name != "Native parity object" || result.Objects[0].SelectedLayer != "a" {
			t.Fatalf("native CAT composition: error=%v result=%s", invokeErr, output)
		}
	}
	html, err := m.ApplicationPage()
	if err != nil || !bytes.Contains(html, []byte("<html")) {
		t.Fatalf("inline page: %v", err)
	}
	// Returned bytes cannot mutate the installed artifact.
	appBytes[0] ^= 255
	if _, err = m.ApplicationRecord(); err != nil {
		t.Fatal("caller mutated installed APP")
	}
	savedID := m.manifest.PluginID
	m.manifest.PluginID = "other.module"
	if _, err = m.ApplicationRecord(); err == nil {
		t.Fatal("APP escaped its owning module binding")
	}
	m.manifest.PluginID = savedID
	// Tampering with the embedded page must fail the enclosing SDK bundle hash.
	var page APPfb.APPUIPage
	if !app.UI(&page, 0) {
		t.Fatal("missing SDK page")
	}
	at := bytes.Index(m.wasmBytes, page.CONTENT()[:32])
	if at < 0 {
		t.Fatal("SDK page missing from artifact")
	}
	m.wasmBytes[at] ^= 1
	if _, err = m.ApplicationRecord(); err == nil {
		t.Fatal("tampered APP was accepted")
	}
	m.wasmBytes[at] ^= 1
}
