package flowrt

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// RegisterAPI registers flow management REST endpoints on the given mux.
// All routes are under /api/v1/flows/.
func RegisterAPI(mux *http.ServeMux, mgr *FlowManager) {
	mux.HandleFunc("/api/v1/flows", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListFlows(w, r, mgr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/flows/deploy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleDeploy(w, r, mgr)
	})

	mux.HandleFunc("/api/v1/flows/bake", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleBake(w, r, mgr)
	})

	mux.HandleFunc("/api/v1/flows/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleCapabilities(w, r)
	})

	mux.HandleFunc("/api/v1/flows/palette", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handlePalette(w, r, mgr)
	})

	// Routes with flow ID in the path: /api/v1/flows/{id}/*
	mux.HandleFunc("/api/v1/flows/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/flows/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "missing flow ID", http.StatusBadRequest)
			return
		}

		flowID := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}

		switch {
		case action == "" && r.Method == http.MethodGet:
			handleGetFlow(w, r, mgr, flowID)
		case action == "" && r.Method == http.MethodDelete:
			handleDeleteFlow(w, r, mgr, flowID)
		case action == "start" && r.Method == http.MethodPost:
			handleStartFlow(w, r, mgr, flowID)
		case action == "stop" && r.Method == http.MethodPost:
			handleStopFlow(w, r, mgr, flowID)
		case action == "status" && r.Method == http.MethodGet:
			handleFlowStatus(w, r, mgr, flowID)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

func handleListFlows(w http.ResponseWriter, r *http.Request, mgr *FlowManager) {
	flows, err := mgr.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(flows)
}

// DeployPayload is the JSON body for POST /api/v1/flows/deploy.
type DeployPayload struct {
	// WASMBase64 is the compiled flow WASM binary, base64-encoded.
	WASMBase64 string `json:"wasmBase64"`

	// FlowJSON is the original flow graph definition.
	FlowJSON json.RawMessage `json:"flowJson"`

	// ArtifactMeta is optional compiled artifact metadata.
	ArtifactMeta json.RawMessage `json:"artifactMeta,omitempty"`
}

func handleDeploy(w http.ResponseWriter, r *http.Request, mgr *FlowManager) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024)) // 64MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var payload DeployPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if payload.WASMBase64 == "" {
		http.Error(w, "missing wasmBase64", http.StatusBadRequest)
		return
	}
	if len(payload.FlowJSON) == 0 {
		http.Error(w, "missing flowJson", http.StatusBadRequest)
		return
	}

	wasmBytes, err := base64.StdEncoding.DecodeString(payload.WASMBase64)
	if err != nil {
		http.Error(w, "invalid base64 wasm: "+err.Error(), http.StatusBadRequest)
		return
	}

	programID, err := mgr.Deploy(r.Context(), wasmBytes, payload.FlowJSON, payload.ArtifactMeta)
	if err != nil {
		http.Error(w, "deploy failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "deployed",
		"programId": programID,
	})
}

// handleBake serves the node-side BAKE deploy path: POST a flow graph + module
// refs, the node bakes the composed runtime.wasm with its own flowcc toolchain
// and installs+starts it. Falls back with 501 when no toolchain is staged.
func handleBake(w http.ResponseWriter, r *http.Request, mgr *FlowManager) {
	if mgr.Baker() == nil {
		http.Error(w, "bake path not enabled: node has no staged flowcc toolchain", http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024)) // 16MB: a graph, not a wasm
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req BakeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.FlowJSON) == 0 {
		http.Error(w, "missing flowJson", http.StatusBadRequest)
		return
	}

	res, programID, err := mgr.BakeAndDeploy(r.Context(), req)
	if err != nil {
		http.Error(w, "bake failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "baked",
		"programId":      programID,
		"modules":        res.Modules,
		"wasmBytes":      len(res.Wasm),
		"flowRuntimeObj": res.FlowRuntimeO,
		"cacheHit":       res.CacheHit,
		"bakeMillis":     res.Elapsed.Milliseconds(),
	})
}

func handleGetFlow(w http.ResponseWriter, r *http.Request, mgr *FlowManager, flowID string) {
	flow, err := mgr.store.Get(flowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(flow)
}

func handleDeleteFlow(w http.ResponseWriter, r *http.Request, mgr *FlowManager, flowID string) {
	if err := mgr.Remove(flowID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "programId": flowID})
}

func handleStartFlow(w http.ResponseWriter, r *http.Request, mgr *FlowManager, flowID string) {
	flow, err := mgr.store.Get(flowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := mgr.loadAndRegister(r.Context(), flow); err != nil {
		http.Error(w, "start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started", "programId": flowID})
}

func handleStopFlow(w http.ResponseWriter, r *http.Request, mgr *FlowManager, flowID string) {
	if err := mgr.Stop(flowID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped", "programId": flowID})
}

func handleFlowStatus(w http.ResponseWriter, r *http.Request, mgr *FlowManager, flowID string) {
	mgr.mu.Lock()
	fp, running := mgr.running[flowID]
	mgr.mu.Unlock()

	status := map[string]interface{}{
		"programId": flowID,
		"running":   running,
	}

	if running && fp != nil && fp.runtime != nil {
		status["nodeCount"] = fp.runtime.NodeCount
		status["edgeCount"] = fp.runtime.EdgeCount
		status["triggerCount"] = fp.runtime.TriggerCount
		status["dependencyCount"] = fp.runtime.DepCount
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleCapabilities returns the list of available host capabilities
// that flows can use (IPFS, FlatSQL, etc.).
func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	caps := AvailableCapabilities()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(caps)
}

// AvailableCapabilities returns JSON descriptors of all host-provided
// capability nodes that the flow editor can use.
func AvailableCapabilities() []json.RawMessage {
	return []json.RawMessage{
		ipfsCapabilityDescriptor(),
		storageCapabilityDescriptor(),
	}
}

func ipfsCapabilityDescriptor() json.RawMessage {
	desc := map[string]interface{}{
		"pluginId":     "io.spacedatanetwork.ipfs",
		"name":         "IPFS Node Operations",
		"pluginFamily": "INFRASTRUCTURE",
		"capabilities": []string{"ipfs", "pubsub", "storage_adapter"},
		"methods": []map[string]interface{}{
			{"methodId": "pubsub_publish", "description": "Publish a message to an IPFS pubsub topic", "inputPorts": []string{"topic", "data"}, "outputPorts": []string{"output"}},
			{"methodId": "pubsub_subscribe", "description": "Subscribe to IPFS pubsub topic (trigger)", "triggerKind": "pubsub-subscription"},
			{"methodId": "ls", "description": "List directory contents for a CID", "inputPorts": []string{"cid"}, "outputPorts": []string{"output"}},
			{"methodId": "cat", "description": "Read file content by CID", "inputPorts": []string{"cid"}, "outputPorts": []string{"output"}},
			{"methodId": "add", "description": "Add bytes to IPFS, returns CID", "inputPorts": []string{"input"}, "outputPorts": []string{"output"}},
			{"methodId": "pin_add", "description": "Pin a CID to local storage", "inputPorts": []string{"cid"}, "outputPorts": []string{"output"}},
			{"methodId": "pin_rm", "description": "Unpin a CID from local storage", "inputPorts": []string{"cid"}, "outputPorts": []string{"output"}},
			{"methodId": "pin_ls", "description": "List pinned CIDs", "inputPorts": []string{"type"}, "outputPorts": []string{"output"}},
			{"methodId": "dag_get", "description": "Get a DAG node by CID", "inputPorts": []string{"cid"}, "outputPorts": []string{"output"}},
			{"methodId": "name_resolve", "description": "Resolve an IPNS name to a CID", "inputPorts": []string{"name"}, "outputPorts": []string{"output"}},
			{"methodId": "name_publish", "description": "Publish a CID under IPNS", "inputPorts": []string{"cid", "key"}, "outputPorts": []string{"output"}},
			{"methodId": "id", "description": "Get IPFS node peer identity", "inputPorts": []string{}, "outputPorts": []string{"output"}},
			{"methodId": "swarm_peers", "description": "List connected swarm peers", "inputPorts": []string{}, "outputPorts": []string{"output"}},
		},
	}
	b, _ := json.Marshal(desc)
	return b
}

func storageCapabilityDescriptor() json.RawMessage {
	desc := map[string]interface{}{
		"pluginId":     "io.spacedatanetwork.flatsql",
		"name":         "FlatSQL Storage",
		"pluginFamily": "DATA_SOURCE",
		"capabilities": []string{"storage_query", "storage_write"},
		"methods": []map[string]interface{}{
			{"methodId": "query", "description": "Query records by schema, day, entity, norad_cat_id", "inputPorts": []string{"schema", "day", "entity_id", "norad_cat_id"}, "outputPorts": []string{"output"}},
			{"methodId": "store", "description": "Store a FlatBuffer record", "inputPorts": []string{"schema", "data"}, "outputPorts": []string{"output"}},
			{"methodId": "delete", "description": "Delete a record by CID", "inputPorts": []string{"cid", "schema"}, "outputPorts": []string{"output"}},
		},
	}
	b, _ := json.Marshal(desc)
	return b
}
