package channels

import (
	"testing"
	"time"
)

func TestChannelGrantRegistryVerifiesSubjectScopeAndExpiry(t *testing.T) {
	t.Parallel()

	channel, err := ParseChannelID("spaceaware-OMM")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}
	registry := NewChannelGrantRegistry()
	grant, err := registry.Issue(ChannelGrantIssueRequest{
		Channel:   channel,
		Subject:   "peer-alpha",
		Scopes:    []AccessBoundary{BoundarySubscribe},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	if !registry.HasChannelGrant(AccessRequest{
		Channel:  channel,
		Boundary: BoundarySubscribe,
		Subject:  "peer-alpha",
		GrantID:  grant.GrantID,
	}) {
		t.Fatal("expected matching grant to authorize subscribe")
	}
	if registry.HasChannelGrant(AccessRequest{
		Channel:  channel,
		Boundary: BoundaryStreamOpen,
		Subject:  "peer-alpha",
		GrantID:  grant.GrantID,
	}) {
		t.Fatal("grant authorized a boundary outside its scope")
	}
	if registry.HasChannelGrant(AccessRequest{
		Channel:  channel,
		Boundary: BoundarySubscribe,
		Subject:  "peer-beta",
		GrantID:  grant.GrantID,
	}) {
		t.Fatal("grant authorized a different subject")
	}
}

func TestChannelGrantRegistryRejectsExpiredGrant(t *testing.T) {
	t.Parallel()

	channel, err := ParseChannelID("spaceaware-OMM")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}
	registry := NewChannelGrantRegistry()
	_, err = registry.Issue(ChannelGrantIssueRequest{
		Channel:   channel,
		Subject:   "peer-alpha",
		Scopes:    []AccessBoundary{BoundarySubscribe},
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	})
	if err == nil {
		t.Fatal("expected expired grant issue to fail")
	}
}
