package flowrt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// FlowManager owns the flow store, running FlowPlugin instances, and
// capability handlers. It registers flow plugins with the SDN plugin manager.
type FlowManager struct {
	store        *FlowStore
	cfg          config.FlowsConfig
	pluginMgr    *plugins.Manager
	capabilities HandlerMap

	// sigPolicy is the operator-controlled publication-trailer signature
	// trust policy consulted at install (Deploy) and load (loadAndRegister)
	// time (loop I2 — mirrors modulert's I1 module load gate). nil (the
	// zero value, set via NewFlowManager) means signature enforcement is
	// not configured for this node: Deploy/loadAndRegister behave exactly
	// as before this field existed. Set via SetModuleSignaturePolicy —
	// activation is owner/release-gated, so production wiring is left to
	// the caller (see SetModuleSignaturePolicy's doc). Guarded by its own
	// mutex (not mu): Deploy calls loadAndRegister while already holding
	// mu, and sync.Mutex is not reentrant.
	sigPolicyMu sync.RWMutex
	sigPolicy   *modulert.ModuleSignaturePolicy

	mu      sync.Mutex
	running map[string]*FlowPlugin // programID → running plugin
}

// SetModuleSignaturePolicy installs the publication-trailer signature trust
// policy Deploy/loadAndRegister enforce against (loop I2). Passing nil (also
// the default after NewFlowManager) disables enforcement — flow bundles
// install/load exactly as they did before this gate existed. This is a
// separate setter rather than a NewFlowManager parameter so existing call
// sites are unaffected; a production node attaches a real policy here only
// once that activation is explicitly released (see the coordinator note in
// the loop I2 task for the exact node.go wiring).
func (m *FlowManager) SetModuleSignaturePolicy(policy *modulert.ModuleSignaturePolicy) {
	m.sigPolicyMu.Lock()
	defer m.sigPolicyMu.Unlock()
	m.sigPolicy = policy
}

// signaturePolicy reads the currently installed policy (nil = inert).
func (m *FlowManager) signaturePolicy() *modulert.ModuleSignaturePolicy {
	m.sigPolicyMu.RLock()
	defer m.sigPolicyMu.RUnlock()
	return m.sigPolicy
}

// NewFlowManager creates a flow manager with the given config and capability handlers.
func NewFlowManager(cfg config.FlowsConfig, pluginMgr *plugins.Manager, capabilities HandlerMap) (*FlowManager, error) {
	store, err := NewFlowStore(cfg.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("create flow store: %w", err)
	}

	return &FlowManager{
		store:        store,
		cfg:          cfg,
		pluginMgr:    pluginMgr,
		capabilities: capabilities,
		running:      make(map[string]*FlowPlugin),
	}, nil
}

// LoadAll scans the flow store and registers each installed flow as a plugin.
func (m *FlowManager) LoadAll(ctx context.Context) error {
	if !m.cfg.Enabled {
		log.Info("Flows disabled in config")
		return nil
	}

	flows, err := m.store.List()
	if err != nil {
		return fmt.Errorf("list flows: %w", err)
	}

	log.Infof("Loading %d installed flows", len(flows))

	for _, flow := range flows {
		if err := m.loadAndRegister(ctx, flow); err != nil {
			log.Warnf("Failed to load flow %q: %v", flow.ProgramID, err)
			continue
		}
	}
	return nil
}

// Deploy installs and starts a new flow from a deployment payload.
func (m *FlowManager) Deploy(ctx context.Context, wasmBytes []byte, flowJSON []byte, artifactMeta []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Parse the flow program
	var program FlowProgram
	if err := json.Unmarshal(flowJSON, &program); err != nil {
		return "", fmt.Errorf("parse flow JSON: %w", err)
	}
	if program.ProgramID == "" {
		return "", fmt.Errorf("flow JSON missing programId")
	}

	// Check max flows limit
	if m.cfg.MaxFlows > 0 && len(m.running) >= m.cfg.MaxFlows {
		return "", fmt.Errorf("max flows limit reached (%d)", m.cfg.MaxFlows)
	}

	// Stop existing flow if upgrading
	if existing, ok := m.running[program.ProgramID]; ok {
		existing.Close()
		delete(m.running, program.ProgramID)
	}

	// Publication-trailer signature gate (loop I2 — defensive hardening,
	// FAIL CLOSED once configured): admitted here, BEFORE the artifact is
	// written to disk, reusing the exact gate modulert's module load path
	// applies (instantiateWASM, loop I1) via the exported wrapper. The
	// portable (trailer-stripped) return value is discarded on purpose —
	// Install below persists wasmBytes VERBATIM, trailer intact, so an
	// installed flow stays signed at rest; loadAndRegister strips the
	// trailer again at actual wasm-compile time. nil sigPolicy (today's
	// default) makes this a no-op that never rejects.
	if _, _, sigErr := modulert.EnforceModuleSignaturePolicy(m.signaturePolicy(), wasmBytes); sigErr != nil {
		return "", fmt.Errorf("flow %q: %w", program.ProgramID, sigErr)
	}

	// Install to disk
	if err := m.store.Install(program.ProgramID, wasmBytes, flowJSON, artifactMeta); err != nil {
		return "", fmt.Errorf("install flow: %w", err)
	}

	// Load and register. Start failure is NOT an install failure: flows that
	// import the module-SDK hostcall bridge (HTTP-mounted flows, loop C.4)
	// cannot run as standalone trigger plugins; lazy HTTP mounts serve them
	// from the config mount table on demand.
	flow, err := m.store.Get(program.ProgramID)
	if err != nil {
		return "", err
	}
	if err := m.loadAndRegister(ctx, flow); err != nil {
		log.Warnf("Flow %q installed; standalone start skipped (mount-served flows load lazily via flows.mounts): %v", program.ProgramID, err)
	}

	return program.ProgramID, nil
}

// Stop stops a running flow.
func (m *FlowManager) Stop(programID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fp, ok := m.running[programID]
	if !ok {
		return fmt.Errorf("flow %q not running", programID)
	}
	fp.Close()
	delete(m.running, programID)
	return nil
}

// Remove stops and deletes a flow.
func (m *FlowManager) Remove(programID string) error {
	m.Stop(programID)
	return m.store.Remove(programID)
}

// List returns all installed flows and their running status.
func (m *FlowManager) List() ([]*FlowStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	flows, err := m.store.List()
	if err != nil {
		return nil, err
	}

	var statuses []*FlowStatus
	for _, f := range flows {
		_, running := m.running[f.ProgramID]
		statuses = append(statuses, &FlowStatus{
			ProgramID: f.ProgramID,
			Name:      f.Name,
			Version:   f.Version,
			Running:   running,
		})
	}
	return statuses, nil
}

// FlowStatus describes the state of an installed flow.
type FlowStatus struct {
	ProgramID string `json:"programId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Running   bool   `json:"running"`
}

// Store returns the underlying FlowStore.
func (m *FlowManager) Store() *FlowStore { return m.store }

// CloseAll stops all running flows.
func (m *FlowManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, fp := range m.running {
		fp.Close()
		delete(m.running, id)
	}
}

// loadAndRegister loads a flow WASM, creates a FlowPlugin, and registers it.
func (m *FlowManager) loadAndRegister(ctx context.Context, flow *InstalledFlow) error {
	wasmPath := m.store.WASMPath(flow.ProgramID)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("read wasm: %w", err)
	}

	// Publication-trailer signature gate (loop I2 — see the identical
	// comment in Deploy above): re-checked here so a cold daemon restart
	// (LoadAll, which never goes through Deploy) is covered too, not just
	// the HTTP deploy path. Idempotent/cheap when Deploy already verified
	// the same bytes moments ago. nil sigPolicy is a no-op.
	if _, _, sigErr := modulert.EnforceModuleSignaturePolicy(m.signaturePolicy(), wasmBytes); sigErr != nil {
		return fmt.Errorf("flow %q: %w", flow.ProgramID, sigErr)
	}

	// Parse flow program for triggers
	var program FlowProgram
	program.ProgramID = flow.ProgramID
	program.Name = flow.Name
	program.Version = flow.Version

	flowJSONPath := m.store.FlowJSONPath(flow.ProgramID)
	if data, err := os.ReadFile(flowJSONPath); err == nil {
		json.Unmarshal(data, &program)
	}

	// Create flow runtime
	rt, err := NewFlowRuntime(wasmBytes, m.cfg.MaxMemoryPages)
	if err != nil {
		return fmt.Errorf("create flow runtime: %w", err)
	}

	// Create plugin
	fp := NewFlowPlugin(program, rt, m.capabilities, wasmPath)

	// Register with plugin manager
	if err := m.pluginMgr.Register(fp); err != nil {
		rt.Release()
		return fmt.Errorf("register flow plugin: %w", err)
	}

	m.running[flow.ProgramID] = fp
	log.Infof("Flow %q loaded and registered", flow.ProgramID)
	return nil
}
