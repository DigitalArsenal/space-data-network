package sdnservices

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
)

// NewHTTPCapFactory returns a BridgeCapFactory serving the "http" capability
// against the Go net/http client — the kubo port of the sdn-server http cap
// (sdn-server/internal/modulert/caps/http.go) so a flow's hostcap/http-request
// node can perform an outbound HTTP GET/fetch.
//
// Supported operation:
//
//	http.request — {
//	    "method": "GET|POST|...",
//	    "url": "https://...",
//	    "headers": {"Content-Type": "application/json"},
//	    "body": "utf8 string or base64 bytes",
//	    "body_encoding": "utf8|base64",  // default utf8
//	    "timeout_ms": 30000,
//	    "max_bytes": 16777216,           // optional response-size clamp
//	}
//	→ {"status":200,"headers":{...},"body":"...","body_encoding":"utf8|base64"}
//
// Fail closed: "http" is an operator-gated SENSITIVE capability (modulert
// sensitiveCapabilities), so a bridge only receives the grant after the
// capability-policy gate approves the module's content hash. Every call ALSO
// re-checks bridge.HasCapability("http") (defense in depth): a module not
// granted http cannot fetch, even if the handler were somehow reachable.
//
// CelesTrak / Space-Track fetch policy (HARD OWNER RULE — firewall-recovery
// courtesy, NOT rate-limit evasion): requests whose host is celestrak.org or
// space-track.org (or a subdomain) are gated by a process-shared policy that
// enforces >= 2.5s SERIAL spacing between requests and a 3-hour URL-keyed
// PERSISTENT ledger (a URL already fetched within the last 3h is refused, not
// re-fetched). Any other host (a local test stub, a reachable public mirror)
// is unaffected. See celestrakPolicy.
func NewHTTPCapFactory(cfg HTTPCapConfig) modulert.BridgeCapFactory {
	policy := newCelestrakPolicy(cfg)
	return func(_ *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		h := &httpCapAdapter{bridge: bridge, policy: policy}
		return h.handle
	}
}

// HTTPCapConfig configures the http cap and its CelesTrak/Space-Track fetch
// policy. The zero value is valid: MinSpacing/LedgerTTL fall back to the
// owner-mandated 2.5s / 3h, and an empty LedgerDir puts the ledger in
// no-persistence mode (spacing + within-process TTL still enforced, but the
// TTL does not survive a restart).
type HTTPCapConfig struct {
	// LedgerDir is the directory the persistent CelesTrak fetch ledger lives
	// under (owner rule: <Repo.Path>/sdn/). Empty => in-memory only.
	LedgerDir string
	// MinSpacing is the minimum serial gap between two gated-host requests.
	// 0 => celestrakMinSpacing (2.5s). Never weaken below 2.5s in production.
	MinSpacing time.Duration
	// LedgerTTL is the per-URL no-refetch window. 0 => celestrakLedgerTTL (3h).
	LedgerTTL time.Duration
	// now/sleep are injectable for deterministic tests. Nil => wall clock /
	// time.Sleep.
	now   func() time.Time
	sleep func(time.Duration)
}

// httpCapMaxResponseBytes is the host-policy ceiling for http.request response
// bodies (parity with the sdn-server cap's 100 MB read limit). A response over
// the effective bound is an ERROR, never a silent truncation.
const httpCapMaxResponseBytes = 100 * 1024 * 1024

type httpCapAdapter struct {
	bridge *modulert.HostBridge
	policy *celestrakPolicy
}

func (h *httpCapAdapter) has(cap string) bool {
	return h.bridge != nil && h.bridge.HasCapability(cap)
}

func (h *httpCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	if operation != "http.request" {
		return errCapJSON(fmt.Sprintf("unknown http operation: %s", operation)), nil
	}
	// POLICY (fail closed, defense in depth): the http grant is required even
	// to reach the network. A bridge without it never fetches.
	if !h.has("http") {
		return errCapJSON("http.request requires the http capability grant"), nil
	}

	var req struct {
		Method       string            `json:"method"`
		URL          string            `json:"url"`
		Headers      map[string]string `json:"headers"`
		Body         string            `json:"body"`
		BodyEncoding string            `json:"body_encoding"`
		TimeoutMs    int               `json:"timeout_ms"`
		MaxBytes     int64             `json:"max_bytes"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return errCapJSON("invalid request payload: " + err.Error()), nil
	}
	if req.URL == "" {
		return errCapJSON("missing url"), nil
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = 30000
	}

	// CelesTrak/Space-Track firewall-recovery courtesy: refuse a within-3h
	// re-fetch and honor >= 2.5s serial spacing BEFORE the request leaves the
	// host. Non-gated hosts (test stubs, mirrors) pass straight through.
	if skip, reason := h.policy.reserve(req.URL); skip {
		return errCapJSON("http.request refused by CelesTrak fetch policy: " + reason), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancel()

	var bodyReader io.Reader
	if req.Body != "" {
		if req.BodyEncoding == "base64" {
			bodyReader = bytes.NewReader(decodeBase64Cap(req.Body))
		} else {
			bodyReader = strings.NewReader(req.Body)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, strings.ToUpper(req.Method), req.URL, bodyReader)
	if err != nil {
		return errCapJSON("invalid request: " + err.Error()), nil
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: time.Duration(req.TimeoutMs) * time.Millisecond}
	resp, err := client.Do(httpReq)
	if err != nil {
		return errCapJSON("request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	limit := int64(httpCapMaxResponseBytes)
	if req.MaxBytes > 0 && req.MaxBytes < limit {
		limit = req.MaxBytes
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return errCapJSON("failed to read response body: " + err.Error()), nil
	}
	if int64(len(respBody)) > limit {
		return errCapJSON(fmt.Sprintf("response body exceeds the %d-byte limit — refusing to deliver a truncated payload", limit)), nil
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	// Binary content is base64-encoded; text/json/xml stays utf8.
	contentType := resp.Header.Get("Content-Type")
	isBinary := !strings.HasPrefix(contentType, "text/") &&
		!strings.Contains(contentType, "json") &&
		!strings.Contains(contentType, "xml")
	bodyEncoding := "utf8"
	bodyOut := string(respBody)
	if isBinary {
		bodyEncoding = "base64"
		bodyOut = encodeBase64Cap(respBody)
	}

	return okCapJSON(map[string]interface{}{
		"status":        resp.StatusCode,
		"headers":       respHeaders,
		"body":          bodyOut,
		"body_encoding": bodyEncoding,
	}), nil
}

// ---------------------------------------------------------------------------
// CelesTrak / Space-Track fetch policy (owner rule).
// ---------------------------------------------------------------------------

const (
	// celestrakMinSpacing is the minimum SERIAL gap between two requests to a
	// gated host. Owner rule (firewall-recovery courtesy): >= 2.5s. Never
	// weaken.
	celestrakMinSpacing = 2500 * time.Millisecond
	// celestrakLedgerTTL is the per-URL no-refetch window. Owner rule: 3h.
	celestrakLedgerTTL = 3 * time.Hour
	// celestrakLedgerFile is the persistent ledger file name under LedgerDir.
	celestrakLedgerFile = "celestrak_fetch_ledger.json"
)

// celestrakPolicy enforces the owner's CelesTrak/Space-Track fetch policy for
// gated hosts: >= minSpacing SERIAL spacing between requests, and a per-URL
// TTL ledger (never re-fetch a URL within ttl) persisted under ledgerDir so
// the window survives a daemon restart. It is process-shared (one instance
// per BuildServices) so every module and flow observes the SAME spacing clock
// and ledger — the courtesy is node-wide, not per-caller.
type celestrakPolicy struct {
	minSpacing time.Duration
	ttl        time.Duration
	ledgerPath string
	now        func() time.Time
	sleep      func(time.Duration)

	mu        sync.Mutex
	ledger    map[string]int64 // url -> last-fetch unix seconds
	lastFetch time.Time        // last gated request time (for spacing)
	loaded    bool
}

func newCelestrakPolicy(cfg HTTPCapConfig) *celestrakPolicy {
	p := &celestrakPolicy{
		minSpacing: cfg.MinSpacing,
		ttl:        cfg.LedgerTTL,
		now:        cfg.now,
		sleep:      cfg.sleep,
		ledger:     make(map[string]int64),
	}
	if p.minSpacing <= 0 {
		p.minSpacing = celestrakMinSpacing
	}
	if p.ttl <= 0 {
		p.ttl = celestrakLedgerTTL
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.sleep == nil {
		p.sleep = time.Sleep
	}
	if dir := strings.TrimSpace(cfg.LedgerDir); dir != "" {
		p.ledgerPath = filepath.Join(dir, celestrakLedgerFile)
	}
	return p
}

// isGatedHost reports whether host (as a URL) is celestrak.org or
// space-track.org (or a subdomain). Only these hosts are subject to the
// spacing + ledger courtesy.
func isGatedHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, base := range []string{"celestrak.org", "celestrak.com", "space-track.org"} {
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

// reserve decides whether a fetch of rawURL may proceed, applying the owner's
// policy and RECORDING the (intended) fetch atomically. For a gated host it
// (a) refuses when the URL was fetched within ttl (skip=true), else (b) sleeps
// out any remaining serial-spacing gap before returning skip=false. A
// non-gated host always returns skip=false immediately with no side effects.
//
// The spacing sleep happens while p.mu is held: gated requests are SERIAL by
// design, so there is never a second gated caller to block, and holding the
// lock guarantees two racing gated fetches cannot both observe the same
// lastFetch and skip the gap.
func (p *celestrakPolicy) reserve(rawURL string) (skip bool, reason string) {
	if !isGatedHost(rawURL) {
		return false, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLoadedLocked()

	now := p.now()
	if last, ok := p.ledger[rawURL]; ok {
		age := now.Sub(time.Unix(last, 0))
		if age >= 0 && age < p.ttl {
			return true, fmt.Sprintf("%s was fetched %s ago (< %s no-refetch window; firewall-recovery courtesy ledger)",
				rawURL, age.Round(time.Second), p.ttl)
		}
	}

	// Serial spacing: wait out the remainder of minSpacing since the last
	// gated request.
	if !p.lastFetch.IsZero() {
		if elapsed := now.Sub(p.lastFetch); elapsed < p.minSpacing {
			p.sleep(p.minSpacing - elapsed)
			now = p.now()
		}
	}
	p.lastFetch = now
	p.ledger[rawURL] = now.Unix()
	p.saveLocked()
	return false, ""
}

// ensureLoadedLocked lazily loads the persistent ledger on first use.
func (p *celestrakPolicy) ensureLoadedLocked() {
	if p.loaded {
		return
	}
	p.loaded = true
	if p.ledgerPath == "" {
		return
	}
	data, err := os.ReadFile(p.ledgerPath)
	if err != nil {
		return // missing/unreadable ledger => empty (fail open on read only)
	}
	var m map[string]int64
	if json.Unmarshal(data, &m) == nil && m != nil {
		p.ledger = m
	}
}

// saveLocked persists the ledger atomically (temp + rename). Best-effort: a
// persistence failure never blocks a fetch (the in-memory ledger still
// enforces the TTL for this process lifetime).
func (p *celestrakPolicy) saveLocked() {
	if p.ledgerPath == "" {
		return
	}
	dir := filepath.Dir(p.ledgerPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(p.ledger, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "."+celestrakLedgerFile+".*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Chmod(tmpPath, 0o600)
	_ = os.Rename(tmpPath, p.ledgerPath)
}
