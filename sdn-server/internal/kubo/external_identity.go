package kubo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A node that does NOT manage its own Kubo still has to make that Kubo
// identify as an SDN node.
//
// Peers decide SDN membership from the libp2p identify agent-version
// (internal/epm/sdnpeers.go, isSDNAgentVersion), and a deployed box runs TWO
// libp2p hosts: sdn-server's own, and a Kubo. When this node supervises that
// Kubo, Supervisor.applyConfig sets Version.AgentSuffix and the job is done.
// But planManagedKubo declines to manage anything whenever admin.ipfs_api_url
// names an operator-run daemon — which the one production config in this repo
// does — and that Kubo then announces a bare "kubo/<version>". On Kademlia the
// IPFS half of such a node is indistinguishable from any other IPFS node, which
// is exactly the condition the 2026-07-28 owner ruling was about.
//
// So the same guarantee is pursued over the RPC API that node already uses.
// This is deliberately NOT silent-and-best-effort: an unidentified peer is a
// network-visible defect, and the operator has to be told in terms they can act
// on.

// ExternalIdentityStatus is what an operator-run Kubo reports about itself.
type ExternalIdentityStatus struct {
	AgentVersion  string // as the daemon presents it on identify, right now
	IsSDN         bool   // does the accounts board accept it
	SuffixSet     bool   // is Version.AgentSuffix already the value we want
	RestartNeeded bool   // config now correct, but the running daemon predates it
}

// agentVersionIsSDN mirrors internal/epm's isSDNAgentVersion, which is what
// actually decides membership. Kept as a copy rather than exporting that one:
// internal/epm depends on much more than this package should, and the rule is
// three Contains checks that a test pins on both sides.
func agentVersionIsSDN(agentVersion string) bool {
	value := strings.ToLower(strings.TrimSpace(agentVersion))
	return strings.Contains(value, "spacedatanetwork") ||
		strings.Contains(value, "space-data-network") ||
		strings.Contains(value, "sdn-desktop")
}

// EnsureExternalAgentSuffix asks an operator-run Kubo how it identifies, and
// sets Version.AgentSuffix when it does not identify as SDN.
//
// The suffix takes effect when the daemon next starts — Kubo reads it once, at
// daemon start (cmd/ipfs/kubo/daemon.go) — so a node whose config this call had
// to change is reported with RestartNeeded, and the caller says so out loud
// rather than leaving the operator believing it took.
func EnsureExternalAgentSuffix(ctx context.Context, apiURL, wantSuffix string) (ExternalIdentityStatus, error) {
	var status ExternalIdentityStatus
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if base == "" {
		return status, fmt.Errorf("kubo: no API URL")
	}
	if strings.TrimSpace(wantSuffix) == "" {
		return status, fmt.Errorf("kubo: refusing to set an empty agent suffix")
	}

	agent, err := externalAgentVersion(ctx, base)
	if err != nil {
		return status, err
	}
	status.AgentVersion = agent
	status.IsSDN = agentVersionIsSDN(agent)
	if status.IsSDN {
		return status, nil
	}

	current, err := externalConfigValue(ctx, base, "Version.AgentSuffix")
	if err == nil && current == wantSuffix {
		// The config is already right; the daemon is simply older than it.
		status.SuffixSet = true
		status.RestartNeeded = true
		return status, nil
	}

	if err := setExternalConfigValue(ctx, base, "Version.AgentSuffix", wantSuffix); err != nil {
		return status, err
	}
	status.SuffixSet = true
	status.RestartNeeded = true
	return status, nil
}

func externalAgentVersion(ctx context.Context, base string) (string, error) {
	body, err := kuboPost(ctx, base+"/api/v0/id")
	if err != nil {
		return "", err
	}
	var payload struct {
		AgentVersion string `json:"AgentVersion"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("kubo: decode /api/v0/id: %w", err)
	}
	return payload.AgentVersion, nil
}

func externalConfigValue(ctx context.Context, base, key string) (string, error) {
	body, err := kuboPost(ctx, base+"/api/v0/config?arg="+url.QueryEscape(key))
	if err != nil {
		return "", err
	}
	var payload struct {
		Value any `json:"Value"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("kubo: decode config read: %w", err)
	}
	if s, ok := payload.Value.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("kubo: %s is not a string", key)
}

func setExternalConfigValue(ctx context.Context, base, key, value string) error {
	endpoint := base + "/api/v0/config?arg=" + url.QueryEscape(key) + "&arg=" + url.QueryEscape(value)
	if _, err := kuboPost(ctx, endpoint); err != nil {
		return fmt.Errorf("kubo: set %s: %w", key, err)
	}
	return nil
}

func kuboPost(ctx context.Context, endpoint string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bounded like the health probe above it: this is a local daemon, but a
	// wrong URL can point at anything.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubo: %s answered %s", endpoint, resp.Status)
	}
	return body, nil
}
