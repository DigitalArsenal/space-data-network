package flowrt

// Security audit (raw-load track): FlowManager.Deploy used to stop the
// running flow before the publication-signature check, so a refused upgrade
// still took the running version down. Deploy now verifies first, then swaps.

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

func TestFlowManagerRefusedUpgradeKeepsRunningFlow(t *testing.T) {
	mgr := newTestFlowManager(t)
	pub, _ := mustGenerateFlowSignerKey(t)
	mgr.SetModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}})

	const programID = "test.flow.deploy.refused-upgrade"
	running := NewFlowPlugin(FlowProgram{ProgramID: programID, Name: "running", Version: "1.0.0"}, nil, HandlerMap{}, "")
	mgr.mu.Lock()
	mgr.running[programID] = running
	mgr.mu.Unlock()

	unsigned := []byte("an unsigned upgrade the policy must refuse")
	if _, err := mgr.Deploy(context.Background(), unsigned, minimalFlowJSON(programID), nil); err == nil || !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("Deploy(unsigned upgrade) = %v, want a publication-signature refusal", err)
	}

	mgr.mu.Lock()
	still := mgr.running[programID]
	mgr.mu.Unlock()
	if still != running {
		t.Fatal("a refused upgrade removed the running flow")
	}
	running.mu.Lock()
	stopped := running.stopped
	running.mu.Unlock()
	if stopped {
		t.Fatal("a refused upgrade stopped the running flow")
	}
}

// An upgrade replaces a running flow, so at the max-flows limit it is not
// refused as if it were a new flow.
func TestFlowManagerUpgradeAtMaxFlowsIsNotCountedTwice(t *testing.T) {
	mgr := newTestFlowManager(t)
	mgr.cfg.MaxFlows = 1
	const programID = "test.flow.deploy.upgrade-at-limit"
	mgr.mu.Lock()
	mgr.running[programID] = NewFlowPlugin(FlowProgram{ProgramID: programID}, nil, HandlerMap{}, "")
	mgr.mu.Unlock()

	if _, err := mgr.Deploy(context.Background(), []byte("upgrade bytes, nil policy"), minimalFlowJSON(programID), nil); err != nil && strings.Contains(err.Error(), "max flows") {
		t.Fatalf("upgrade at the limit refused as a new flow: %v", err)
	}
	// A new flow at the limit is still refused.
	mgr.mu.Lock()
	mgr.running[programID] = NewFlowPlugin(FlowProgram{ProgramID: programID}, nil, HandlerMap{}, "")
	mgr.mu.Unlock()
	if _, err := mgr.Deploy(context.Background(), []byte("another flow"), minimalFlowJSON("test.flow.other"), nil); err == nil || !strings.Contains(err.Error(), "max flows") {
		t.Fatalf("a new flow at the limit: err = %v, want the max flows refusal", err)
	}
}
