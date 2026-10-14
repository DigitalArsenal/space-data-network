// Security-bond attestation orchestration (owner 2026-08-03: "use a free
// service to get the balance of SOL, BTC, and ETH ... in a wasm module
// (using the external API accesses wired up)").
//
// The HOST here is connectors-only glue, per the WASM-not-Go-host-boundary
// law (owner 2026-07-16): every chain query and every price lookup happens
// inside the embedded bond-attestation WASM module through the generic
// `http` capability. This file only (1) schedules the module with a $WHA
// request naming the node's own EPM-derived chain addresses, (2) caches the
// module's $WHA attestation, and (3) serves it anonymously at
// GET /api/v1/trust/bond, rendered as JSON for the dashboard (the external
// client edge; nothing between host and module is JSON) — the bond is public
// by design (publisher-key-adversarial-security: trust is priced by a bond
// peers can verify).
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

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/wha"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
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
	mu sync.RWMutex
	// latest is the module's last $WHA attestation, verbatim.
	latest     []byte
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
	attestation []byte // the module's $WHA, verbatim
	at          time.Time
}

// bondAddresses are the chain addresses a $WHA request names (this node's
// own EPM-derived ones, or a directory peer's). Empty fields are simply
// absent balances.
type bondAddresses struct {
	Btc, Eth, Sol string
}

// The module's chains, by CAIP-2 identifier ($WHA CHAIN_ID).
const (
	bondChainBTC = "bip122:000000000019d6689c085ae165831e93"
	bondChainETH = "eip155:1"
	bondChainSOL = "solana:5eykt4UsFv8P8NJdTREpY1vzQKqZKvdp"
)

// bondRequest is the $WHA request: ACCOUNTS only, one per address.
func bondRequest(addrs bondAddresses) []byte {
	b := flatbuffers.NewBuilder(256)
	var accounts []flatbuffers.UOffsetT
	for _, a := range []struct{ chain, name, address string }{
		{bondChainBTC, "BTC", addrs.Btc}, {bondChainETH, "ETH", addrs.Eth}, {bondChainSOL, "SOL", addrs.Sol},
	} {
		if a.address == "" {
			continue
		}
		chain, name, address := b.CreateString(a.chain), b.CreateString(a.name), b.CreateString(a.address)
		wha.WHAAccountStart(b)
		wha.WHAAccountAddCHAIN_ID(b, chain)
		wha.WHAAccountAddCHAIN_NAME(b, name)
		wha.WHAAccountAddADDRESS(b, address)
		accounts = append(accounts, wha.WHAAccountEnd(b))
	}
	wha.WHAStartACCOUNTSVector(b, len(accounts))
	for i := len(accounts) - 1; i >= 0; i-- {
		b.PrependUOffsetT(accounts[i])
	}
	vector := b.EndVector(len(accounts))
	wha.WHAStart(b)
	wha.WHAAddACCOUNTS(b, vector)
	wha.FinishWHABuffer(b, wha.WHAEnd(b))
	return b.FinishedBytes()
}

// bondAttestation reads a $WHA answer; nil when the bytes are not one.
func bondAttestation(raw []byte) *wha.WHA {
	if len(raw) < 8 || !wha.WHABufferHasIdentifier(raw) {
		return nil
	}
	return wha.GetRootAsWHA(raw, 0)
}

// bondJSON renders a $WHA attestation for the dashboard's GET
// /api/v1/trust/bond: attested, bond_usd and total_usd (null unless attested),
// holdings (usd null when unvalued), chains (usd null when incomplete) and
// errors, plus the node it attests and when it was read.
func bondJSON(raw []byte, nodeID string, at time.Time) ([]byte, error) {
	w := bondAttestation(raw)
	if w == nil {
		return nil, fmt.Errorf("not a $WHA attestation")
	}
	orNull := func(ok bool, v float64) any {
		if ok {
			return v
		}
		return nil
	}
	holdings := make([]map[string]any, 0, w.HOLDINGSLength())
	var h wha.WHAHolding
	for i := 0; i < w.HOLDINGSLength(); i++ {
		if !w.HOLDINGS(&h, i) {
			continue
		}
		row := map[string]any{"symbol": string(h.SYMBOL()), "amount": h.AMOUNT(), "usd": orNull(h.VALUED(), h.VALUE()),
			"chain": string(h.CHAIN_NAME()), "address": string(h.ADDRESS())}
		if contract := string(h.CONTRACT()); contract != "" {
			row["contract"] = contract
		}
		holdings = append(holdings, row)
	}
	chains := make([]map[string]any, 0, w.CHAINSLength())
	var c wha.WHAChain
	for i := 0; i < w.CHAINSLength(); i++ {
		if w.CHAINS(&c, i) {
			chains = append(chains, map[string]any{"chain": string(c.CHAIN_NAME()), "address": string(c.ADDRESS()),
				"token_count": c.TOKEN_COUNT(), "usd": orNull(c.COMPLETE(), c.VALUE()), "complete": c.COMPLETE()})
		}
	}
	errs := make([]string, 0, w.ERRORSLength())
	for i := 0; i < w.ERRORSLength(); i++ {
		errs = append(errs, string(w.ERRORS(i)))
	}
	total := orNull(w.ATTESTED(), w.TOTAL_VALUE())
	return json.Marshal(map[string]any{"attested": w.ATTESTED(), "bond_usd": total, "total_usd": total,
		"holdings": holdings, "chains": chains, "errors": errs, "node": nodeID, "attested_at": at.Format(time.RFC3339)})
}

// bondAttestedAt is the attestation's as-of time (AS_OF_MS, the module's
// clock read), else the time the host received it.
func bondAttestedAt(w *wha.WHA, received time.Time) time.Time {
	if ms := w.AS_OF_MS(); ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	return received
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

	// The module's answer must be a $WHA attestation; anything else is
	// refused rather than cached.
	w := bondAttestation(out)
	if w == nil {
		log.Warnf("bond attestation: module returned a non-attestation answer (%d bytes)", len(out))
		return
	}

	b.mu.Lock()
	b.latest = append([]byte(nil), out...)
	b.attestedAt = bondAttestedAt(w, time.Now().UTC())
	b.peerID = n.PeerID().String()
	b.mu.Unlock()
	log.Infof("bond attestation: refreshed (%d bytes, attested=%v)", len(out), w.ATTESTED())
	if b.onRefresh != nil {
		b.onRefresh()
	}
}

// invokeBondAttestation runs the embedded module once for the given
// addresses and returns its verbatim $WHA answer. The trust rules engine
// reuses it for every subject it evaluates (trust_engine.go).
func invokeBondAttestation(ctx context.Context, addrs bondAddresses) ([]byte, error) {
	// A fresh module instance per run releases guest memory between refreshes.
	capReg := modulert.NewCapabilityRegistry()
	capReg.Register("http", caps.NewHTTPCapFactory())
	mod, err := modulert.NewModule(bondAttestationWasm, capReg, bondModuleNodeContext())
	if err != nil {
		return nil, fmt.Errorf("module load failed: %w", err)
	}
	defer mod.Close()

	return attestBond(ctx, mod, addrs)
}

// attestBond invokes `attest` with the $WHA request on "request" and returns
// the $WHA on "attestation". The chain and price lookups are many outbound
// requests, so the call gets two minutes rather than the interactive budget.
func attestBond(ctx context.Context, mod *modulert.Module, addrs bondAddresses) ([]byte, error) {
	invokeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	invokeCtx = wasmrt.WithExecBudget(invokeCtx, wasmrt.ExecBudget{Timeout: 2 * time.Minute})
	outputs, err := mod.InvokeMethodOutputs(invokeCtx, "attest", []modulert.InvokeInputFrame{{
		PortID: "request", SchemaName: "WHA.fbs", FileIdentifier: wha.WHAIdentifier, RootTypeName: "WHA", Payload: bondRequest(addrs),
	}})
	if err != nil {
		return nil, fmt.Errorf("attest invoke failed: %w", err)
	}
	for _, o := range outputs {
		if o.PortID == "attestation" && bondAttestation(o.Payload) != nil {
			return o.Payload, nil
		}
	}
	return nil, fmt.Errorf("attest returned no $WHA attestation")
}

func bondComplete(raw []byte) bool {
	w := bondAttestation(raw)
	return w != nil && w.ATTESTED()
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
	// The module's attestation, with the node identity + timestamp.
	body, err := bondJSON(latest, peerID, at)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeBondJSON(w, body)
}

// Only directory-verified peers may initiate a lookup, one at a time, and
// it runs off the request with its own deadline: a remote admin's request
// arrives through the sealed transport, which ends it long before a slow
// chain API answers, so a lookup bound to the request never finished and the
// dashboard showed "Balance refresh failed" (owner 2026-10-07: "bond should
// work"). A caller gets the cached answer, stale while a refresh runs, or
// pending. This is not an arbitrary address/URL proxy.
func (b *bondAttestor) handlePeerBond(w http.ResponseWriter, r *http.Request, id string) {
	if b.lookupPeer == nil || len(id) > 128 {
		http.NotFound(w, r)
		return
	}
	b.mu.RLock()
	cached, ok := b.peerCache[id]
	b.mu.RUnlock()
	ttl := bondRetryInterval
	if bondComplete(cached.attestation) {
		ttl = bondRefreshInterval
	}
	if ok && time.Since(cached.at) < ttl {
		writeBondPeerJSON(w, id, cached)
		return
	}
	if b.peerMu.TryLock() {
		addresses, err := b.lookupPeer(id)
		if err != nil {
			b.peerMu.Unlock()
			http.NotFound(w, r)
			return
		}
		go func() {
			defer b.peerMu.Unlock()
			b.refreshPeer(id, addresses)
		}()
	}
	if ok {
		writeBondPeerJSON(w, id, cached)
		return
	}
	writeBondPending(w)
}

func writeBondPeerJSON(w http.ResponseWriter, id string, cached bondPeerResult) {
	body, err := bondJSON(cached.attestation, id, cached.at)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeBondJSON(w, body)
}

// refreshPeer runs one peer lookup and caches what the module answers.
func (b *bondAttestor) refreshPeer(id string, addresses bondAddresses) {
	invoke := b.invoke
	if invoke == nil {
		invoke = invokeBondAttestation
	}
	raw, err := invoke(context.Background(), addresses)
	if err != nil {
		return
	}
	attestation := bondAttestation(raw)
	if attestation == nil {
		return
	}
	stamp := bondAttestedAt(attestation, time.Now().UTC())
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.peerCache == nil {
		b.peerCache = make(map[string]bondPeerResult)
	}
	if _, held := b.peerCache[id]; !held && len(b.peerCache) >= 128 {
		for key := range b.peerCache {
			delete(b.peerCache, key)
			break
		}
	}
	b.peerCache[id] = bondPeerResult{append([]byte(nil), raw...), stamp}
}

func writeBondJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
