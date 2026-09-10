// Security-bond attestation orchestration (owner 2026-08-03: "use a free
// service to get the balance of SOL, BTC, and ETH ... in a wasm module
// (using the external API accesses wired up)").
//
// The HOST here is connectors-only glue, per the WASM-not-Go-host-boundary
// law (owner 2026-07-16): every chain query and every price lookup happens
// inside the embedded bond-attestation WASM module through the generic
// `http` capability. This file only (1) schedules the module with the node's
// own EPM-derived chain addresses as the invoke payload, (2) caches the
// module's JSON attestation, and (3) serves it anonymously at
// GET /api/v1/trust/bond — the bond is public by design
// (publisher-key-adversarial-security: trust is priced by a bond peers can
// verify).
//
// The module artifact is go:embed'ed like the dashboard: the lean update
// lane ships only bin/ + manifest (the fleet bundles carry no runtime/ dir),
// so an in-binary module is the one delivery path every box already has.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

//go:embed embedded/bond-attestation.wasm
var bondAttestationWasm []byte

// bondRefreshInterval is how often the attestation is refreshed. Free public
// APIs are rate-limited, so requests reuse results for fifteen minutes.
const bondRefreshInterval = 15 * time.Minute
const bondRetryInterval = 10 * time.Second

// bondInitialDelay lets the node finish its boot replay before the first
// outbound attestation runs.
const bondInitialDelay = 5 * time.Second

type bondAttestor struct {
	mu         sync.RWMutex
	latest     json.RawMessage
	attestedAt time.Time
	peerID     string
	// onRefresh, when set, runs after every successful attestation (the
	// trust rules engine takes it as an early-evaluation trigger).
	onRefresh  func()
	peerMu     sync.Mutex
	peerCache  map[string]bondPeerResult
	lookupPeer func(string) (bondAddresses, error)
	invoke     func(context.Context, bondAddresses) ([]byte, error)
}

type bondPeerResult struct {
	body json.RawMessage
	at   time.Time
}

// bondAddresses is the module's invoke payload: this node's own EPM-derived
// chain addresses. Empty fields are simply absent balances.
type bondAddresses struct {
	Btc string `json:"btc,omitempty"`
	Eth string `json:"eth,omitempty"`
	Sol string `json:"sol,omitempty"`
}

// readBondAddresses pulls the chain addresses out of the node's own signed
// $EPM — the same facts /api/node/epm/json serves anonymously.
func readBondAddresses(n *node.Node) bondAddresses {
	var out bondAddresses
	epmSvc := n.EPMService()
	if epmSvc == nil {
		return out
	}
	m := epmSvc.GetNodeEPMJSON()
	if m == nil {
		return out
	}
	if v, ok := m["bitcoin_address"].(string); ok {
		out.Btc = v
	}
	if v, ok := m["ethereum_address"].(string); ok {
		out.Eth = v
	}
	if v, ok := m["solana_address"].(string); ok {
		out.Sol = v
	}
	return out
}

// bondModuleNodeContext returns a NodeContext whose capability policy
// approves EXACTLY the embedded artifact's hash for `http`. This is not a
// bypass of the operator allowlist: the module ships INSIDE the audited
// binary (go:embed), so it is the host's own trust domain — the approval is
// pinned to the embedded bytes' content hash, and any other module (any
// other hash) still fails closed.
func bondModuleNodeContext() *modulert.NodeContext {
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		return nil
	}
	if _, err := policy.Approve(modulert.CapabilityApproval{
		ModuleHash: modulert.ContentHashHex(bondAttestationWasm),
		Capability: "http",
		PluginID:   "org.spacedatanetwork.bond-attestation",
		ApprovedBy: "host-embedded",
		Note:       "ships in the daemon binary (bond_attestation.go); chain RPC in WASM per owner law 2026-07-16",
	}); err != nil {
		return nil
	}
	return &modulert.NodeContext{CapabilityPolicy: policy}
}

// refresh invokes the embedded module once and caches a valid attestation.
// A failed run leaves the previous attestation standing — a stale bond
// beats a vanished one, and `attested_at` says how stale.
func (b *bondAttestor) refresh(ctx context.Context, n *node.Node) {
	addrs := readBondAddresses(n)
	if addrs.Btc == "" && addrs.Eth == "" && addrs.Sol == "" {
		log.Debugf("bond attestation: node EPM carries no chain addresses yet")
		return
	}
	out, err := invokeBondAttestation(ctx, addrs)
	if err != nil {
		log.Warnf("bond attestation: %v", err)
		return
	}

	// The module's answer must be JSON with an `attested` verdict; anything
	// else is refused rather than cached.
	var probe struct {
		Attested *bool `json:"attested"`
	}
	if err := json.Unmarshal(out, &probe); err != nil || probe.Attested == nil {
		log.Warnf("bond attestation: module returned a non-attestation answer (%d bytes)", len(out))
		return
	}

	b.mu.Lock()
	b.latest = append(json.RawMessage(nil), out...)
	b.attestedAt = time.Now().UTC()
	b.peerID = n.PeerID().String()
	b.mu.Unlock()
	log.Infof("bond attestation: refreshed (%d bytes, attested=%v)", len(out), *probe.Attested)
	if b.onRefresh != nil {
		b.onRefresh()
	}
}

// invokeBondAttestation runs the embedded module once for the given
// addresses and returns its verbatim JSON answer. The trust rules engine
// reuses it for every subject it evaluates (trust_engine.go).
func invokeBondAttestation(ctx context.Context, addrs bondAddresses) ([]byte, error) {
	payload, err := json.Marshal(addrs)
	if err != nil {
		return nil, err
	}
	// A fresh module instance per run releases guest memory between refreshes.
	capReg := modulert.NewCapabilityRegistry()
	capReg.Register("http", caps.NewHTTPCapFactory())
	mod, err := modulert.NewModule(bondAttestationWasm, capReg, bondModuleNodeContext())
	if err != nil {
		return nil, fmt.Errorf("module load failed: %w", err)
	}
	defer mod.Close()

	invokeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := mod.InvokeCron(invokeCtx, "attest", payload)
	if err != nil {
		return nil, fmt.Errorf("attest invoke failed: %w", err)
	}
	return out, nil
}

func bondComplete(raw []byte) bool {
	var answer struct {
		Attested bool `json:"attested"`
	}
	return json.Unmarshal(raw, &answer) == nil && answer.Attested
}

// start refreshes successful balances periodically and retries incomplete reads.
func (b *bondAttestor) start(ctx context.Context, n *node.Node) {
	b.peerID = n.PeerID().String()
	b.lookupPeer = func(id string) (bondAddresses, error) {
		addresses, err := directoryChainAddresses(n.Store())(id)
		if err != nil {
			return bondAddresses{}, err
		}
		var out bondAddresses
		for _, a := range addresses {
			switch strings.ToLower(a.ChainID) {
			case "btc", "bitcoin":
				out.Btc = a.Address
			case "eth", "ethereum", "eip155:1":
				out.Eth = a.Address
			case "sol", "solana":
				out.Sol = a.Address
			}
		}
		if out == (bondAddresses{}) {
			return out, fmt.Errorf("no linked addresses")
		}
		return out, nil
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(bondInitialDelay):
		}
		for {
			b.refresh(ctx, n)
			b.mu.RLock()
			complete := bondComplete(b.latest)
			b.mu.RUnlock()
			delay := bondRefreshInterval
			if !complete {
				delay = bondRetryInterval
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func writeBondPending(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "3")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"pending":true}`))
}

// A warming node reports pending while its automatic lookup runs.
func (b *bondAttestor) handleBond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if id := r.URL.Query().Get("peer"); id != "" {
		b.mu.RLock()
		self := b.peerID
		b.mu.RUnlock()
		if id != self {
			b.handlePeerBond(w, r, id)
			return
		}
	}
	b.mu.RLock()
	latest := b.latest
	at := b.attestedAt
	peerID := b.peerID
	b.mu.RUnlock()
	if len(latest) == 0 {
		writeBondPending(w)
		return
	}
	// Wrap the module's verbatim answer with the node identity + timestamp.
	var body map[string]interface{}
	if err := json.Unmarshal(latest, &body); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	body["node"] = peerID
	body["attested_at"] = at.Format(time.RFC3339)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// Only directory-verified peers may initiate a lookup. Cache results and bound
// concurrent upstream work; this is not an arbitrary address/URL proxy.
func (b *bondAttestor) handlePeerBond(w http.ResponseWriter, r *http.Request, id string) {
	if b.lookupPeer == nil || len(id) > 128 {
		http.NotFound(w, r)
		return
	}
	b.mu.RLock()
	cached, ok := b.peerCache[id]
	b.mu.RUnlock()
	ttl := bondRetryInterval
	if bondComplete(cached.body) {
		ttl = bondRefreshInterval
	}
	if ok && time.Since(cached.at) < ttl {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(cached.body)
		return
	}
	if !b.peerMu.TryLock() {
		writeBondPending(w)
		return
	}
	defer b.peerMu.Unlock()
	addresses, err := b.lookupPeer(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	invoke := b.invoke
	if invoke == nil {
		invoke = invokeBondAttestation
	}
	raw, err := invoke(r.Context(), addresses)
	if err != nil {
		writeBondPending(w)
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		http.Error(w, "invalid balance response", 502)
		return
	}
	stamp := time.Now().UTC()
	body["node"] = id
	body["attested_at"] = stamp.Format(time.RFC3339)
	out, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "invalid balance response", 502)
		return
	}
	b.mu.Lock()
	if b.peerCache == nil {
		b.peerCache = make(map[string]bondPeerResult)
	}
	if len(b.peerCache) >= 128 {
		for key := range b.peerCache {
			delete(b.peerCache, key)
			break
		}
	}
	b.peerCache[id] = bondPeerResult{out, stamp}
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}
