// Trust rules engine wiring (sdn-trust-rules-engine, owner 2026-09-01).
//
// The node evaluates every active `$TRP` policy against every subject it
// knows on a configurable cadence (0.1 Hz by default) and early on events —
// edge mutations, policy changes, a refreshed bond — and publishes signed
// `$TRV` verdicts on the trust topics. The host stays connectors only: chain
// balances come from the embedded bond-attestation WASM module over the
// generic http capability (bond_attestation.go); subjects' addresses come
// from their verified `$EPM` in the directory.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// trustSettingsFile persists the operator's runtime evaluation interval.
const trustSettingsFile = "trust-settings.json"

type trustRuntimeSettings struct {
	EvaluationIntervalMs uint32 `json:"EVALUATION_INTERVAL_MS"`
}

func loadTrustSettings(dir string) trustRuntimeSettings {
	var s trustRuntimeSettings
	if strings.TrimSpace(dir) == "" {
		return s
	}
	data, err := os.ReadFile(filepath.Join(dir, trustSettingsFile))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

func saveTrustSettings(dir string, ms uint32) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("no storage path to persist trust settings")
	}
	data, err := json.Marshal(trustRuntimeSettings{EvaluationIntervalMs: ms})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, trustSettingsFile), data, 0o600)
}

// bondChainForSymbol maps the module's asset symbols to the chain ids the
// `$EPM` ChainProofs use, with the asset's native decimals.
func bondChainForSymbol(symbol string) (chain string, decimals uint32) {
	switch strings.ToUpper(strings.TrimSpace(symbol)) {
	case "BTC":
		return "bitcoin", 8
	case "ETH":
		return "ethereum", 18
	case "SOL":
		return "solana", 9
	}
	return "", 0
}

// bondBalances answers a subject's holdings by running the embedded
// bond-attestation module against the subject's attested addresses.
func bondBalances(ctx context.Context, addrs []trust.ChainAddress) ([]trust.Holding, error) {
	var req bondAddresses
	byChain := map[string]string{}
	for _, a := range addrs {
		addr := strings.TrimSpace(a.Address)
		if addr == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(a.ChainID)) {
		case "bitcoin", "btc":
			req.Btc, byChain["bitcoin"] = addr, addr
		case "ethereum", "eth":
			req.Eth, byChain["ethereum"] = addr, addr
		case "solana", "sol":
			req.Sol, byChain["solana"] = addr, addr
		}
	}
	if len(byChain) == 0 {
		return nil, errors.New("the subject has no attested address on a supported chain (bitcoin, ethereum, solana)")
	}
	out, err := invokeBondAttestation(ctx, req)
	if err != nil {
		return nil, err
	}
	return parseBondHoldings(out, byChain, time.Now().UnixMilli())
}

// parseBondHoldings turns the module's verbatim answer into engine holdings.
func parseBondHoldings(out []byte, byChain map[string]string, nowMs int64) ([]trust.Holding, error) {
	var ans struct {
		Attested *bool `json:"attested"`
		Holdings []struct {
			Symbol string   `json:"symbol"`
			Amount float64  `json:"amount"`
			USD    *float64 `json:"usd"`
		} `json:"holdings"`
	}
	if err := json.Unmarshal(out, &ans); err != nil || ans.Attested == nil {
		return nil, errors.New("the bond-attestation module returned a non-attestation answer")
	}
	holdings := make([]trust.Holding, 0, len(ans.Holdings))
	for _, h := range ans.Holdings {
		chain, decimals := bondChainForSymbol(h.Symbol)
		if chain == "" {
			continue
		}
		holding := trust.Holding{
			ChainID: chain, Address: byChain[chain], Symbol: strings.ToUpper(h.Symbol),
			Amount: h.Amount, Decimals: decimals, ObservedAtMs: nowMs,
			SourceQuery: "org.spacedatanetwork.bond-attestation/attest",
		}
		if h.USD != nil {
			holding.USD, holding.PricedInUSD = *h.USD, true
		}
		holdings = append(holdings, holding)
	}
	return holdings, nil
}

// directoryChainAddresses resolves a peer's attested addresses from its
// verified `$EPM` directory record (ChainProofs, else the flattened
// <chain>_address fields the directory projection carries).
func directoryChainAddresses(flat *storage.FlatSQLStore) trust.AddressResolver {
	return func(peerID string) ([]trust.ChainAddress, error) {
		if flat == nil {
			return nil, errors.New("directory store is unavailable")
		}
		records, err := flat.QueryDirectory(storage.DirectoryQuery{Kind: directory.KindNode, PeerID: peerID, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return nil, errors.New("no verified profile in the directory for this peer")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(records[0].EPMJSON), &payload); err != nil {
			return nil, fmt.Errorf("parse directory profile: %w", err)
		}
		var out []trust.ChainAddress
		seen := map[string]bool{}
		if proofs, ok := payload["chain_proofs"].([]any); ok {
			for _, raw := range proofs {
				entry, _ := raw.(map[string]any)
				chain, _ := entry["chain"].(string)
				address, _ := entry["address"].(string)
				if chain == "" || address == "" || seen[chain] {
					continue
				}
				seen[chain] = true
				out = append(out, trust.ChainAddress{ChainID: chain, Address: address})
			}
		}
		for _, chain := range []string{"bitcoin", "ethereum", "solana"} {
			if address, _ := payload[chain+"_address"].(string); address != "" && !seen[chain] {
				seen[chain] = true
				out = append(out, trust.ChainAddress{ChainID: chain, Address: address})
			}
		}
		return out, nil
	}
}

// directoryNodeSubjects lists every node with a verified profile in the
// directory (except this node) as a subject for the rules engine.
func directoryNodeSubjects(flat *storage.FlatSQLStore, selfPeerID string) func() []string {
	return func() []string {
		if flat == nil {
			return nil
		}
		records, err := flat.QueryDirectory(storage.DirectoryQuery{Kind: directory.KindNode, ExcludePeerID: selfPeerID, Limit: 2000})
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(records))
		for _, rec := range records {
			if id := strings.TrimSpace(rec.PeerID); id != "" && id != selfPeerID {
				out = append(out, id)
			}
		}
		return out
	}
}

// startTrustRulesEngine loads the trust graph, starts the engine and mounts
// the trust API (edges/scores + policies/verdicts/settings) on adminMux.
func startTrustRulesEngine(ctx context.Context, n *node.Node, adminMux *http.ServeMux, storagePath string, requireAuth bool, resolveAuth func() *auth.Handler, bondAtt *bondAttestor) error {
	flat := n.Store()
	if flat == nil {
		return errors.New("store is unavailable")
	}
	store, err := trust.NewStoreWithFlatSQL(flat)
	if err != nil {
		return fmt.Errorf("open trust store: %w", err)
	}
	graph, err := store.LoadGraph()
	if err != nil {
		return fmt.Errorf("load trust graph: %w", err)
	}
	svc := trust.NewService(graph, nil)
	peerID := n.PeerID().String()
	svc.TrackEvaluator(peerID)

	key, err := storefrontSigningKeyFromRaw(n.SigningKey())
	if err != nil {
		return fmt.Errorf("evaluator signing key: %w", err)
	}
	publish := func(topic string, data []byte) error { return n.PublishToTopic(ctx, topic, data) }

	policies, err := trust.NewPolicyStore(flat, peerID, key)
	if err != nil {
		return err
	}
	verdicts, err := trust.NewVerdictStore(flat, peerID)
	if err != nil {
		return err
	}
	engine, err := trust.NewEngine(trust.EngineConfig{
		Policies: policies, Verdicts: verdicts, Service: svc,
		Balances: trust.BalanceSourceFunc(bondBalances),
		Resolve:  directoryChainAddresses(flat),
		Subjects: directoryNodeSubjects(flat, peerID),
		Publish:  publish, Key: key, PeerID: peerID,
	})
	if err != nil {
		return err
	}
	if s := loadTrustSettings(storagePath); s.EvaluationIntervalMs > 0 {
		engine.SetIntervalOverride(s.EvaluationIntervalMs)
	}
	if bondAtt != nil {
		bondAtt.onRefresh = func() { engine.Trigger("bond-refreshed") }
	}
	go engine.Run(ctx)

	h := api.NewTrustHandler(svc)
	h.Store = store
	h.Events = &trust.EventPublisher{SenderPriv: key, Publish: publish}
	h.Policies = policies
	h.Verdicts = verdicts
	h.Engine = engine
	h.SaveInterval = func(ms uint32) error { return saveTrustSettings(storagePath, ms) }
	h.Protect = func(inner http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if requireAuth {
				var handler *auth.Handler
				if resolveAuth != nil {
					handler = resolveAuth()
				}
				if handler == nil {
					http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
					return
				}
				if !handler.RequireTrust(w, r, peers.Admin) {
					return
				}
			}
			inner(w, r)
		}
	}
	h.RegisterRoutes(adminMux)
	log.Infof("Trust rules engine started (evaluator %s, interval override %d ms)", peerID, engine.IntervalOverride())
	return nil
}
