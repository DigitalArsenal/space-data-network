package channels

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ChannelGrant struct {
	GrantID   string
	ChannelID string
	Subject   string
	Scopes    []AccessBoundary
	IssuedAt  time.Time
	ExpiresAt time.Time
	Revoked   bool
}

type ChannelGrantIssueRequest struct {
	Channel   ChannelID
	Subject   string
	Scopes    []AccessBoundary
	ExpiresAt time.Time
}

type ChannelGrantRegistry struct {
	mu     sync.RWMutex
	grants map[string]ChannelGrant
}

func NewChannelGrantRegistry() *ChannelGrantRegistry {
	return &ChannelGrantRegistry{grants: make(map[string]ChannelGrant)}
}

func (r *ChannelGrantRegistry) Issue(req ChannelGrantIssueRequest) (ChannelGrant, error) {
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		return ChannelGrant{}, fmt.Errorf("grant subject is required")
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = DefaultPrivateGrantScopes()
	}
	now := time.Now().UTC()
	expiresAt := req.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(24 * time.Hour)
	}
	if !expiresAt.After(now) {
		return ChannelGrant{}, fmt.Errorf("grant expiration must be in the future")
	}
	grant := ChannelGrant{
		GrantID:   newGrantID(),
		ChannelID: req.Channel.ChannelID,
		Subject:   subject,
		Scopes:    dedupeScopes(scopes),
		IssuedAt:  now,
		ExpiresAt: expiresAt,
	}
	if r == nil {
		return grant, nil
	}
	r.mu.Lock()
	r.grants[grant.GrantID] = grant
	r.mu.Unlock()
	return grant, nil
}

func (r *ChannelGrantRegistry) HasChannelGrant(req AccessRequest) bool {
	if r == nil {
		return false
	}
	grantID := strings.TrimSpace(req.GrantID)
	if grantID == "" {
		return false
	}
	r.mu.RLock()
	grant, ok := r.grants[grantID]
	r.mu.RUnlock()
	if !ok || grant.Revoked || grant.ChannelID != req.Channel.ChannelID {
		return false
	}
	if strings.TrimSpace(req.Subject) != "" && grant.Subject != strings.TrimSpace(req.Subject) {
		return false
	}
	if !grant.ExpiresAt.IsZero() && !time.Now().UTC().Before(grant.ExpiresAt) {
		return false
	}
	for _, scope := range grant.Scopes {
		if scope == req.Boundary {
			return true
		}
	}
	return false
}

func DefaultPrivateGrantScopes() []AccessBoundary {
	return []AccessBoundary{
		BoundarySubscribe,
		BoundaryUnsubscribe,
		BoundaryPublish,
		BoundaryStreamOpen,
		BoundaryByteRangeRead,
		BoundaryKeyUnwrap,
		BoundaryShardImport,
		BoundaryModuleFeedDelivery,
		BoundaryLocalCacheRead,
	}
}

func dedupeScopes(scopes []AccessBoundary) []AccessBoundary {
	seen := make(map[AccessBoundary]bool, len(scopes))
	result := make([]AccessBoundary, 0, len(scopes))
	for _, scope := range scopes {
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		result = append(result, scope)
	}
	return result
}

func newGrantID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "grant-" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "grant-" + hex.EncodeToString(random[:])
}
