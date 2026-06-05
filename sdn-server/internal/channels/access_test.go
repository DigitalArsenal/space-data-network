package channels

import "testing"

func TestDefaultAccessGateFailsClosedForPrivateBoundaries(t *testing.T) {
	gate := NewAccessGate(nil)
	channel, err := ParseChannelID("spaceaware-OMM")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}

	for _, boundary := range []AccessBoundary{
		BoundaryListPrivate,
		BoundarySubscribe,
		BoundaryStreamOpen,
		BoundaryByteRangeRead,
		BoundaryKeyUnwrap,
		BoundaryShardImport,
		BoundaryModuleFeedDelivery,
		BoundaryLocalCacheRead,
	} {
		decision := gate.Authorize(AccessRequest{
			Channel:  channel,
			Boundary: boundary,
		})
		if decision.Allowed {
			t.Fatalf("%s should fail closed without a grant", boundary)
		}
		if decision.GrantState != "required" {
			t.Fatalf("%s grant state = %q, want required", boundary, decision.GrantState)
		}
	}
}

func TestAccessGateAllowsBoundaryWhenGrantCheckerApproves(t *testing.T) {
	channel, err := ParseChannelID("spaceaware-OMM")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}
	gate := NewAccessGate(staticGrantChecker{allowed: true})
	decision := gate.Authorize(AccessRequest{
		Channel:  channel,
		Boundary: BoundaryStreamOpen,
	})
	if !decision.Allowed {
		t.Fatalf("expected approved grant to allow stream open: %+v", decision)
	}
	if decision.GrantState != "verified" {
		t.Fatalf("grant state = %q, want verified", decision.GrantState)
	}
}

type staticGrantChecker struct {
	allowed bool
}

func (c staticGrantChecker) HasChannelGrant(AccessRequest) bool {
	return c.allowed
}
