package channels

import (
	"strings"
	"sync"
	"time"
)

const (
	BoundaryListPrivate        AccessBoundary = "list_private"
	BoundarySubscribe          AccessBoundary = "subscribe"
	BoundaryUnsubscribe        AccessBoundary = "unsubscribe"
	BoundaryPublish            AccessBoundary = "publish"
	BoundaryGrantIssue         AccessBoundary = "grant_issue"
	BoundaryStreamOpen         AccessBoundary = "stream_open"
	BoundaryByteRangeRead      AccessBoundary = "byte_range_read"
	BoundaryKeyUnwrap          AccessBoundary = "key_unwrap"
	BoundaryShardImport        AccessBoundary = "shard_import"
	BoundaryModuleFeedDelivery AccessBoundary = "module_feed_delivery"
	BoundaryLocalCacheRead     AccessBoundary = "local_cache_read"
)

type AccessBoundary string

type AccessRequest struct {
	Channel  ChannelID
	Boundary AccessBoundary
	Subject  string
	GrantID  string
}

type AccessDecision struct {
	Allowed    bool
	GrantState string
	Reason     string
}

type AccessAuditEntry struct {
	Timestamp            time.Time
	ChannelID            string
	Boundary             AccessBoundary
	Subject              string
	GrantID              string
	Allowed              bool
	GrantState           string
	Reason               string
	PlaintextBytesLogged bool
	UnwrappedKeyLogged   bool
}

type GrantChecker interface {
	HasChannelGrant(AccessRequest) bool
}

type AccessGate struct {
	mu      sync.Mutex
	checker GrantChecker
	audit   []AccessAuditEntry
}

func NewAccessGate(checker GrantChecker) *AccessGate {
	return &AccessGate{checker: checker}
}

func (g *AccessGate) Authorize(req AccessRequest) AccessDecision {
	if req.Boundary == "" {
		return g.auditDecision(req, AccessDecision{
			Allowed:    false,
			GrantState: "required",
			Reason:     "access boundary is required",
		})
	}
	if g != nil && g.checker != nil && g.checker.HasChannelGrant(req) {
		return g.auditDecision(req, AccessDecision{
			Allowed:    true,
			GrantState: "verified",
			Reason:     "grant verified",
		})
	}
	return g.auditDecision(req, AccessDecision{
		Allowed:    false,
		GrantState: "required",
		Reason:     "verified channel grant required",
	})
}

func (g *AccessGate) AuditEntries() []AccessAuditEntry {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entries := make([]AccessAuditEntry, len(g.audit))
	copy(entries, g.audit)
	return entries
}

func (g *AccessGate) auditDecision(req AccessRequest, decision AccessDecision) AccessDecision {
	if g == nil {
		return decision
	}
	g.mu.Lock()
	g.audit = append(g.audit, AccessAuditEntry{
		Timestamp:  time.Now().UTC(),
		ChannelID:  req.Channel.ChannelID,
		Boundary:   req.Boundary,
		Subject:    strings.TrimSpace(req.Subject),
		GrantID:    strings.TrimSpace(req.GrantID),
		Allowed:    decision.Allowed,
		GrantState: decision.GrantState,
		Reason:     decision.Reason,
	})
	g.mu.Unlock()
	return decision
}
