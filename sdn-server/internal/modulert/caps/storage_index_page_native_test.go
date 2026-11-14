package caps

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

func TestStorageIndexPageNativeConnector(t *testing.T) {
	path := os.Getenv("SDN_INDEX_PAGE_MODULE")
	if path == "" {
		t.Skip("SDN_INDEX_PAGE_MODULE supplies the query connector artifact")
	}
	t.Setenv("SDN_STORE_FORMAT", "4")
	store := newCapTestStore(t)
	want := indexSynthetic("OCM.fbs", 42)
	if _, e := store.StoreWithSourceTags("OCM.fbs", want, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic", SourceName: "page"}); e != nil {
		t.Fatal(e)
	}
	wasm, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	policy, e := modulert.NewCapabilityPolicyStore(filepath.Join(t.TempDir(), "policy.json"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = policy.Approve(modulert.CapabilityApproval{ModuleHash: modulert.ContentHashHex(wasm), Capability: "storage_query"}); e != nil {
		t.Fatal(e)
	}
	registry := modulert.NewCapabilityRegistry()
	registry.RegisterBridgeAware("storage_query", NewStorageCapFactory(store))
	module, e := modulert.NewModule(wasm, registry, &modulert.NodeContext{CapabilityPolicy: policy})
	if e != nil {
		t.Fatal(e)
	}
	defer module.Close()
	request := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "OCM.fbs", ProviderID: "synthetic", SourceName: "page", Limit: 1})[4:]
	b := flatbuffers.NewBuilder(512)
	method := b.CreateString("index_page")
	port := b.CreateString("query")
	schema := b.CreateString("QRP.fbs")
	fid := b.CreateString("$QRP")
	root := b.CreateString("QRP")
	arena := b.CreateByteVector(request)
	PIV.FlatBufferTypeRefStart(b)
	PIV.FlatBufferTypeRefAddSchemaName(b, schema)
	PIV.FlatBufferTypeRefAddFileIdentifier(b, fid)
	PIV.FlatBufferTypeRefAddRootType(b, root)
	ref := PIV.FlatBufferTypeRefEnd(b)
	PIV.TABStart(b)
	PIV.TABAddPortId(b, port)
	PIV.TABAddTypeRef(b, ref)
	PIV.TABAddSize(b, uint32(len(request)))
	tab := PIV.TABEnd(b)
	PIV.PIVRequestStartInputsVector(b, 1)
	b.PrependUOffsetT(tab)
	inputs := b.EndVector(1)
	PIV.PIVRequestStart(b)
	PIV.PIVRequestAddMethodId(b, method)
	PIV.PIVRequestAddInputs(b, inputs)
	PIV.PIVRequestAddPayloadArena(b, arena)
	req := PIV.PIVRequestEnd(b)
	PIV.PIVStart(b)
	PIV.PIVAddRequest(b, req)
	PIV.FinishPIVBuffer(b, PIV.PIVEnd(b))
	wire := b.FinishedBytes()
	mod := module.Mod()
	ptr, e := mod.Allocate(wire)
	if e != nil {
		t.Fatal(e)
	}
	defer mod.SecureDeallocate(ptr, uint32(len(wire)))
	lengthPtr, e := mod.Allocate([]byte{0, 0, 0, 0})
	if e != nil {
		t.Fatal(e)
	}
	defer mod.SecureDeallocate(lengthPtr, 4)
	results, e := mod.ExecuteContext(context.Background(), "plugin_invoke_stream", int32(ptr), int32(len(wire)), int32(lengthPtr))
	if e != nil {
		t.Fatal(e)
	}
	length, e := mod.ReadMemory(lengthPtr, 4)
	if e != nil {
		t.Fatal(e)
	}
	n := binary.LittleEndian.Uint32(length)
	outPtr := uint32(wasmrt.ToInt32(results[0]))
	defer mod.SecureDeallocate(outPtr, n)
	out, e := mod.ReadMemory(outPtr, n)
	if e != nil {
		t.Fatal(e)
	}
	resp := PIV.GetRootAsPIV(out, 0).Response(nil)
	if resp == nil || resp.StatusCode() != 0 {
		t.Fatal("native invoke failed")
	}
	found := false
	for i := 0; i < resp.OutputsLength(); i++ {
		var tab PIV.TAB
		resp.Outputs(&tab, i)
		if string(tab.PortId()) != "page" {
			continue
		}
		found = true
		stream := resp.PayloadArenaBytes()[tab.Offset() : tab.Offset()+tab.Size()]
		h, e := syncwire.ParseQRP(stream)
		if e != nil {
			t.Fatal(e)
		}
		if h.RECORD_COUNT() != 1 || h.TOTAL_COUNT() != 1 || len(h.NEXT_CURSOR()) != 0 {
			t.Fatal("invalid native page")
		}
		at := 4 + int(binary.LittleEndian.Uint32(stream))
		for binary.LittleEndian.Uint32(stream[at:]) == 0 {
			at += 4
		}
		size := int(binary.LittleEndian.Uint32(stream[at:]))
		if !bytes.Equal(stream[at+4:at+4+size], want) {
			t.Fatal("native record bytes differ")
		}
	}
	if !found {
		t.Fatal("native connector emitted no page")
	}
	t.Log("native connector -> typed storage capability -> durable OCM bytes: equal")
}
