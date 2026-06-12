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
		GrantID:  "grant-1",
	})
	if !decision.Allowed {
		t.Fatalf("expected approved grant to allow stream open: %+v", decision)
	}
	if decision.GrantState != "verified" {
		t.Fatalf("grant state = %q, want verified", decision.GrantState)
	}
}

func TestAccessGateAuditsPrivateGrantDecisions(t *testing.T) {
	channel, err := ParseChannelID("spaceaware-OMM")
	if err != nil {
		t.Fatalf("ParseChannelID failed: %v", err)
	}
	gate := NewAccessGate(staticGrantChecker{allowed: true})

	denied := gate.Authorize(AccessRequest{
		Channel:  channel,
		Boundary: BoundaryStreamOpen,
		Subject:  "peer-alpha",
		GrantID:  "missing-grant",
	})
	allowed := gate.Authorize(AccessRequest{
		Channel:  channel,
		Boundary: BoundaryModuleFeedDelivery,
		Subject:  "peer-alpha",
		GrantID:  "grant-1",
	})

	if denied.Allowed || !allowed.Allowed {
		t.Fatalf("unexpected decisions denied=%+v allowed=%+v", denied, allowed)
	}
	entries := gate.AuditEntries()
	if len(entries) != 2 {
		t.Fatalf("audit entry count = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].ChannelID != "spaceaware-OMM" ||
		entries[0].Boundary != BoundaryStreamOpen ||
		entries[0].Subject != "peer-alpha" ||
		entries[0].GrantID != "missing-grant" ||
		entries[0].Allowed ||
		entries[0].GrantState != "required" {
		t.Fatalf("unexpected denied audit entry: %#v", entries[0])
	}
	if entries[1].ChannelID != "spaceaware-OMM" ||
		entries[1].Boundary != BoundaryModuleFeedDelivery ||
		!entries[1].Allowed ||
		entries[1].GrantState != "verified" {
		t.Fatalf("unexpected allowed audit entry: %#v", entries[1])
	}
	for _, entry := range entries {
		if entry.PlaintextBytesLogged || entry.UnwrappedKeyLogged {
			t.Fatalf("audit entry leaked private material: %#v", entry)
		}
	}
}

type staticGrantChecker struct {
	allowed bool
}

func (c staticGrantChecker) HasChannelGrant(req AccessRequest) bool {
	return c.allowed && req.GrantID == "grant-1"
}
