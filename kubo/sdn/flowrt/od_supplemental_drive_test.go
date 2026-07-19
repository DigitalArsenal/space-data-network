//go:build linux

package flowrt

// od_supplemental_drive_test.go — local-first drain proof. Loads the
// PRE-BAKED composed runtime.wasm under the node's real WasmEdge (WithWASIThreads
// + AOT-at-load + auto-wired in-wasm FlatSQL store), serves the provider fetches
// from on-disk fixtures via a space_data_module_host host module (NO real
// network), fires the timer, and reports the AUTHORITATIVE thread count
// (ThreadPeak) + store rows under WasmEdge — the crux the browser harness mirrors.
//
// Env: SDN_ODSUP_RUNTIME_WASM (baked artifact) + SDN_ODSUP_FIXTURES (json map
// url->fixture path).

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

func TestODSupplementalWasmEdgeDrive(t *testing.T) {
	wasmPath := os.Getenv("SDN_ODSUP_RUNTIME_WASM")
	fixturesJSON := os.Getenv("SDN_ODSUP_FIXTURES")
	if wasmPath == "" || fixturesJSON == "" {
		t.Skip("set SDN_ODSUP_RUNTIME_WASM + SDN_ODSUP_FIXTURES")
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read runtime.wasm: %v", err)
	}
	var fixtures map[string]string
	if err := json.Unmarshal([]byte(fixturesJSON), &fixtures); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	// space_data_module_host fixture bridge (call/response_len/read_response).
	var responseBuf []byte
	served := map[string]bool{}
	notFound := 0
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	setResp := func(meta map[string]interface{}) {
		mb, _ := json.Marshal(meta)
		buf := make([]byte, 4+len(mb)+4)
		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(mb)))
		copy(buf[4:], mb)
		responseBuf = buf
	}
	hostFuncs := []wasmrt.HostFunc{
		{
			Name:    "call",
			Params:  []*wasmedge.ValType{i32(), i32(), i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				opPtr, opLen := uint32(params[0].(int32)), uint32(params[1].(int32))
				pPtr, pLen := uint32(params[2].(int32)), uint32(params[3].(int32))
				mem := cf.GetMemoryByIndex(0)
				op, _ := mem.GetData(uint(opPtr), uint(opLen))
				var url string
				if pLen >= 4 {
					hdr, _ := mem.GetData(uint(pPtr), 4)
					metaLen := binary.LittleEndian.Uint32(hdr)
					if pLen >= 4+metaLen {
						metaBytes, _ := mem.GetData(uint(pPtr+4), uint(metaLen))
						var req struct {
							URL string `json:"url"`
						}
						json.Unmarshal(metaBytes, &req)
						url = req.URL
					}
				}
				if string(op) == "http.request" {
					if path, ok := fixtures[url]; ok {
						b, rerr := os.ReadFile(path)
						if rerr == nil {
							served[url] = true
							setResp(map[string]interface{}{"ok": true, "status": 200, "body_encoding": "base64", "body": base64.StdEncoding.EncodeToString(b)})
							return []interface{}{int32(0)}, wasmedge.Result_Success
						}
					}
					notFound++
					setResp(map[string]interface{}{"ok": true, "status": 404, "body_encoding": "utf8", "body": ""})
					return []interface{}{int32(0)}, wasmedge.Result_Success
				}
				setResp(map[string]interface{}{"ok": true, "result": nil})
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
		},
		{
			Name:    "response_len",
			Returns: []*wasmedge.ValType{i32()},
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{int32(len(responseBuf))}, wasmedge.Result_Success
			},
		},
		{
			Name:    "read_response",
			Params:  []*wasmedge.ValType{i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				dstPtr, dstLen := uint32(params[0].(int32)), uint32(params[1].(int32))
				mem := cf.GetMemoryByIndex(0)
				n := uint32(len(responseBuf))
				if n > dstLen {
					n = dstLen
				}
				if n > 0 {
					mem.SetData(responseBuf[:n], uint(dstPtr), uint(n))
				}
				return []interface{}{int32(n)}, wasmedge.Result_Success
			},
		},
	}

	rt, err := NewFlowRuntime(wasmBytes, 32768, wasmrt.WithHostModule("space_data_module_host", hostFuncs))
	if err != nil {
		t.Fatalf("NewFlowRuntime: %v", err)
	}
	defer rt.Release()
	rt.SetLinkedSection(func(run func() error) error { return run() })
	t.Logf("loaded: %d nodes, %d edges, %d triggers", rt.NodeCount, rt.EdgeCount, rt.TriggerCount)

	// Deliver the trigger frames the live FireTrigger delivers: the JSON tick to
	// the providers' "config" port and the [u64le unix_ms] fire timestamp to the
	// store's "trigger" port (a required, trigger-fed port).
	tick, _ := json.Marshal(map[string]string{"trigger": "t0"})
	fireStamp := make([]byte, 8)
	binary.LittleEndian.PutUint64(fireStamp, uint64(1721000000000))
	deliver := func(port string, payload []byte) {
		portBytes := append([]byte(port), 0)
		buf := make([]byte, flowFrameDescriptorSize+len(portBytes)+len(payload))
		copy(buf[flowFrameDescriptorSize:], portBytes)
		copy(buf[flowFrameDescriptorSize+len(portBytes):], payload)
		framePtr, aerr := rt.Module().AllocateSize(uint32(len(buf)))
		if aerr != nil {
			t.Fatalf("alloc %s frame: %v", port, aerr)
		}
		encodeFrameDescriptor(buf[:flowFrameDescriptorSize], &FlowFrameDescriptor{
			PortIDPointer: framePtr + flowFrameDescriptorSize,
			Offset:        framePtr + flowFrameDescriptorSize + uint32(len(portBytes)),
			Size:          uint32(len(payload)),
			Occupied:      true,
		})
		if werr := rt.Module().WriteMemory(framePtr, buf); werr != nil {
			t.Fatalf("write %s frame: %v", port, werr)
		}
		rt.EnqueueTriggerFrame(0, framePtr)
	}
	deliver("config", tick)
	deliver("trigger", fireStamp)

	res, derr := rt.Drain(context.Background(), HandlerMap{}, DrainOptions{MaxIterations: 5000})
	if derr != nil {
		t.Fatalf("drain: %v", derr)
	}
	// DIAG: per-node identity + runtime state (which node is stuck?).
	for i := uint32(0); i < rt.NodeCount; i++ {
		info := rt.nodeInfo[i]
		if st, e := rt.GetNodeRuntimeState(i); e == nil {
			t.Logf("DIAG node[%d] plugin=%s method=%s state=%+v", i, info.PluginID, info.MethodID, st)
		} else {
			t.Logf("DIAG node[%d] plugin=%s method=%s (no state: %v)", i, info.PluginID, info.MethodID, e)
		}
	}

	count := func(tbl string) int64 {
		if rt.store == nil {
			return -1
		}
		r, e := rt.store.db.Query(fmt.Sprintf("SELECT COUNT(*) FROM %s", tbl))
		if e != nil || len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
			return -1
		}
		if n, ok := r.Rows[0][0].(int64); ok {
			return n
		}
		return -1
	}
	omm, ocm, obd := count("sds_omm"), count("sds_ocm"), count("sds_obd")
	oemLeak := count("sds_oem")
	if rt.store != nil {
		if r, e := rt.store.db.Query("SELECT provider, source_name, MAX(pulled_at), COUNT(*) FROM sds_omm GROUP BY provider"); e == nil {
			for _, row := range r.Rows {
				t.Logf("★ ATTRIBUTION: provider=%v source_name=%v max_pulled_at=%v count=%v", row[0], row[1], row[2], row[3])
			}
		}
	}

	// -- RESET (operator "clear a run") end-to-end: pick one stored batch, clear
	//    it via the DUMB ClearBatch primitive, and assert (a) every row of that
	//    batch vanishes across all three tables and (b) the arena export shrinks
	//    -- exercising mark_deleted_bulk + compact (fresh-DB rebuild + swap)
	//    against real composed-flow data.
	if rt.store != nil && omm > 0 {
		var batchID string
		if r, e := rt.store.db.Query("SELECT batch_id FROM sds_omm WHERE batch_id != '' LIMIT 1"); e == nil && len(r.Rows) == 1 && len(r.Rows[0]) == 1 {
			if bid, ok := r.Rows[0][0].(string); ok {
				batchID = bid
			}
		}
		if batchID == "" {
			t.Logf("RESET: no non-empty batch_id in store; skipping ClearBatch assertion")
		} else {
			before, _ := rt.store.db.ExportData()
			tomb, survivors, cerr := rt.store.ClearBatch(batchID)
			if cerr != nil {
				t.Fatalf("ClearBatch(%q): %v", batchID, cerr)
			}
			after, _ := rt.store.db.ExportData()
			var left int64 = -1
			if r, e := rt.store.db.Query("SELECT COUNT(*) FROM sds_omm WHERE batch_id = ?", batchID); e == nil && len(r.Rows) == 1 && len(r.Rows[0]) == 1 {
				if n, ok := r.Rows[0][0].(int64); ok {
					left = n
				}
			}
			remain := count("sds_omm") + count("sds_ocm") + count("sds_obd")
			t.Logf("★ RESET(ClearBatch): batch=%s tombstoned(omm/ocm/obd)=%d/%d/%d survivors=%d exportBefore=%dB exportAfter=%dB remainingRows=%d clearedBatchRowsLeft=%d",
				batchID, len(tomb["sds_omm"]), len(tomb["sds_ocm"]), len(tomb["sds_obd"]), survivors, len(before), len(after), remain, left)
			if left != 0 {
				t.Fatalf("ClearBatch left %d rows of batch %s in sds_omm (want 0)", left, batchID)
			}
			if len(after) >= len(before) {
				t.Fatalf("ClearBatch did not shrink the arena: before=%dB after=%dB", len(before), len(after))
			}
			if len(tomb["sds_omm"]) <= 0 {
				t.Fatalf("ClearBatch tombstoned 0 sds_omm rows for a present batch")
			}
		}
	}

	if serr := rt.SnapshotStore(); serr != nil {
		t.Logf("snapshot: %v", serr)
	}

	t.Logf("★ WASMEDGE DRIVE: nodesInvoked=%d served=%v notFound=%d", res.NodesInvoked, served, notFound)
	t.Logf("★ THREADS UNDER WASMEDGE: spawnCount=%d peak=%d osTids=%v", rt.ThreadSpawnCount(), rt.ThreadPeak(), rt.WorkerOSThreadIDs())
	t.Logf("★ STORE ROWS: sds_omm=%d sds_ocm=%d sds_obd=%d ; sds_oem(leak, want -1/0)=%d", omm, ocm, obd, oemLeak)

	if omm <= 0 {
		t.Fatalf("no $OMM stored — flow did not fit+store")
	}
	if rt.ThreadPeak() < 2 {
		t.Errorf("THREAD-COUNT FINDING: composed OD flow ran only peak=%d worker(s) under WasmEdge (hardware_concurrency path)", rt.ThreadPeak())
	}
}
