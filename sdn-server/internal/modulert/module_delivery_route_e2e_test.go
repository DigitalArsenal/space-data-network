package modulert_test

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/deliveryclient"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// The module-delivery stream carries two message types: $LCH requests and
// $LPF proofs. licensing/core takes them on two ports ("request" and
// "proof"), and the protocol declaration names one, so the host routes each
// frame to the port whose accepted type names its file identifier. This runs
// the real licensing artifact behind the node's own libp2p stream handler and
// reads the outcome from the delivery audit: each frame reaches the key server
// (an unconfigured key server refuses both, "server-handle-failed"), where a
// mis-routed frame would be refused at the port ("invalid-request-input").
func TestModuleDeliveryRoutesFramesToTheirLicensingPorts(t *testing.T) {
	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"ipfs", "protocol_dial", "protocol_handle", "wallet_sign", "crypto_sign", "crypto_verify", "secrets", "logging"} {
		if _, err := policy.Approve(modulert.CapabilityApproval{ModuleHash: modulert.ContentHashHex(wasm), Capability: capability, PluginID: "licensing", ApprovedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	mod, err := modulert.NewModule(wasm, modulert.NewCapabilityRegistry(), &modulert.NodeContext{CapabilityPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	defer mod.Close()
	if !hasInputPort(mod.Manifest(), "server_handle_message", "proof") {
		t.Skipf("%s predates the typed licensing contract (no \"proof\" port)", wasmPath)
	}

	provider, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	requester, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer requester.Close()
	requester.Peerstore().AddAddrs(provider.ID(), provider.Addrs(), peerstore.PermanentAddrTTL)
	if err := mod.Start(context.Background(), plugins.RuntimeContext{Host: provider}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var audit []string
	license.SetGrantAuditSink(func(line string) {
		mu.Lock()
		audit = append(audit, line)
		mu.Unlock()
	}, nil)
	defer license.SetGrantAuditSink(nil, nil)

	send := func(frame []byte) string {
		t.Helper()
		mu.Lock()
		audit = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s, err := requester.NewStream(ctx, provider.ID(), protocol.ID(modulert.ModuleDeliveryWireID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(frame); err != nil {
			t.Fatal(err)
		}
		_ = s.CloseWrite()
		answer, _ := io.ReadAll(s)
		_ = s.Close()
		if len(answer) != 0 {
			t.Fatalf("an unconfigured key server answered %d bytes", len(answer))
		}
		mu.Lock()
		defer mu.Unlock()
		for _, line := range audit {
			if strings.Contains(line, "outcome=aborted") {
				return line
			}
		}
		t.Fatalf("no aborted delivery in the audit: %q", audit)
		return ""
	}

	key := make([]byte, 32)
	challenge, err := deliveryclient.EncodeChallengeRequest(deliveryclient.ChallengeRequest{
		RequestID: "req-1", ModuleID: "com.example.module", ModuleVersion: "1.0.0", RequestedDomain: "localhost",
		RequesterSigningPublicKey: key, RequesterEphemeralPublicKey: key, ProviderPeerID: provider.ID().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := deliveryclient.EncodeGrantProof(deliveryclient.GrantProof{
		RequestID: "req-1", ModuleID: "com.example.module", ModuleVersion: "1.0.0", RequestedDomain: "localhost",
		RequesterEphemeralPublicKey: key, ChallengeNonce: key, ProviderPeerID: provider.ID().String(),
		Signature: make([]byte, 64), RequesterSigningPublicKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, frame := range map[string][]byte{"$LCH request": challenge, "$LPF proof": proof} {
		if line := send(frame); !strings.Contains(line, "Failed to handle licensing delivery message") {
			t.Fatalf("%s did not reach the key server: %s", name, line)
		}
	}
	// A frame no port names goes to the declared input port, which refuses it.
	if line := send(append([]byte{8, 0, 0, 0}, "$XYZ\x00\x00\x00\x00"...)); !strings.Contains(line, "not accepted by port: request") {
		t.Fatalf("an unnamed frame: %s", line)
	}
}

func hasInputPort(m *modulert.Manifest, methodID, portID string) bool {
	if m == nil {
		return false
	}
	for _, method := range m.Methods {
		if method.MethodID != methodID {
			continue
		}
		for _, port := range method.InputPorts {
			if port.PortID == portID {
				return true
			}
		}
	}
	return false
}
