package peers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// Test peer IDs (using valid base58 encoded Ed25519 peer IDs)
var (
	testPeerID1, _ = peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	testPeerID2, _ = peer.Decode("12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p")
	testPeerID3, _ = peer.Decode("12D3KooWP5MYTnN8DcQDw7aDUFZY2vQAhvMwZZZ1XN3U9Wh3mJUW")
)

// allScaleValues enumerates every distinct point on the PGP-aligned
// TrustLevel scale (Phase C1), from the hard veto (Never) up to the
// numeric maximum (Ultimate).
var allScaleValues = []TrustLevel{Never, Untrusted, Limited, Standard, Trusted, Admin, Ultimate}

func TestTrustLevel_String(t *testing.T) {
	tests := []struct {
		level    TrustLevel
		expected string
	}{
		{Never, "never"},
		{Untrusted, "unknown"},
		{Unknown, "unknown"},
		{Limited, "marginal"},
		{Marginal, "marginal"},
		{Standard, "standard"},
		{Trusted, "full"},
		{Full, "full"},
		{Admin, "admin"},
		{Ultimate, "ultimate"},
		{TrustLevel(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("TrustLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

func TestParseTrustLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected TrustLevel
		wantErr  bool
	}{
		// Canonical PGP ownertrust names.
		{"never", Never, false},
		{"unknown", Unknown, false},
		{"marginal", Marginal, false},
		{"standard", Standard, false},
		{"full", Full, false},
		{"admin", Admin, false},
		{"ultimate", Ultimate, false},
		// Legacy names must still parse (existing API clients/config/
		// scripts submit these) even though String() no longer emits them.
		{"untrusted", Untrusted, false},
		{"limited", Limited, false},
		{"trusted", Trusted, false},
		{"invalid", Untrusted, true},
		{"", Untrusted, true},
	}

	for _, tt := range tests {
		got, err := ParseTrustLevel(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseTrustLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("ParseTrustLevel(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// TestTrustLevel_RoundTrip round-trips every scale value through
// String -> ParseTrustLevel -> JSON Marshal/Unmarshal -> FlatSQL peer
// registry persistence, per the C1 test requirement.
func TestTrustLevel_RoundTrip(t *testing.T) {
	for _, level := range allScaleValues {
		level := level
		t.Run(level.String(), func(t *testing.T) {
			// parse(format(x)) == x
			parsed, err := ParseTrustLevel(level.String())
			if err != nil {
				t.Fatalf("ParseTrustLevel(%q) error: %v", level.String(), err)
			}
			if parsed != level {
				t.Errorf("parse(format(%d)) = %d, want %d", level, parsed, level)
			}

			// JSON round trip.
			data, err := json.Marshal(level)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			var decoded TrustLevel
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}
			if decoded != level {
				t.Errorf("JSON round trip: got %d, want %d", decoded, level)
			}

			// FlatSQL peer-registry persistence round trip (int8 encoding).
			record := peerRegistryRecordFromTrustedPeer(&TrustedPeer{
				ID:         testPeerID1,
				TrustLevel: level,
			}, time.Now().UnixMilli())
			encoded, err := encodePeerRegistryRecord(record)
			if err != nil {
				t.Fatalf("encodePeerRegistryRecord failed: %v", err)
			}
			decodedRecord, err := decodePeerRegistryRecord(encoded)
			if err != nil {
				t.Fatalf("decodePeerRegistryRecord failed: %v", err)
			}
			if decodedRecord.TrustLevel != level {
				t.Errorf("FlatSQL persist round trip: got %d, want %d", decodedRecord.TrustLevel, level)
			}
		})
	}
}

// TestTrustLevel_UltimateIsMax asserts level 5 (Ultimate) exists and is
// the numeric maximum of the scale.
func TestTrustLevel_UltimateIsMax(t *testing.T) {
	if Ultimate != 5 {
		t.Fatalf("Ultimate = %d, want 5", int(Ultimate))
	}
	for _, level := range allScaleValues {
		if level > Ultimate {
			t.Errorf("%s (%d) exceeds Ultimate (5)", level, level)
		}
	}
}

// TestTrustLevel_LegacyMigration asserts each legacy stored value (the
// only five TrustLevel values any pre-C1 build could ever have persisted:
// Untrusted..Admin, 0-4) migrates deterministically to its documented PGP
// target, per the mapping table in trust.go's TrustLevel doc comment.
func TestTrustLevel_LegacyMigration(t *testing.T) {
	tests := []struct {
		name         string
		legacyRaw    int8 // the byte a pre-C1 build would have written
		wantMigrated TrustLevel
		wantString   string
	}{
		{"Untrusted(0)->Unknown", 0, Unknown, "unknown"},
		{"Limited(1)->Marginal", 1, Marginal, "marginal"},
		{"Standard(2)->Standard(no PGP alias, classifies >=Marginal)", 2, Standard, "standard"},
		{"Trusted(3)->Full", 3, Full, "full"},
		{"Admin(4)->Admin(no PGP alias, classifies >=Full)", 4, Admin, "admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate decoding a record a pre-C1 build wrote: the raw
			// int8 byte, run through the same normalize/decode path used
			// by the live persistence loader.
			migrated := normalizeTrustLevel(TrustLevel(tt.legacyRaw))
			if migrated != tt.wantMigrated {
				t.Fatalf("legacy raw %d migrated to %d, want %d", tt.legacyRaw, migrated, tt.wantMigrated)
			}
			if got := migrated.String(); got != tt.wantString {
				t.Errorf("migrated value String() = %q, want %q", got, tt.wantString)
			}
		})
	}

	// Out-of-range/corrupted bytes fail closed to Unknown, not the old
	// Standard fallback.
	if got := normalizeTrustLevel(TrustLevel(-42)); got != Unknown {
		t.Errorf("corrupted low value normalized to %v, want Unknown", got)
	}
	if got := normalizeTrustLevel(TrustLevel(42)); got != Unknown {
		t.Errorf("corrupted high value normalized to %v, want Unknown", got)
	}
}

func TestTrustLevel_Ownertrust(t *testing.T) {
	tests := []struct {
		level TrustLevel
		want  Ownertrust
	}{
		{Never, OwnertrustNever},
		{Untrusted, OwnertrustUnknown},
		{Unknown, OwnertrustUnknown},
		{Limited, OwnertrustMarginal},
		{Marginal, OwnertrustMarginal},
		{Standard, OwnertrustMarginal},
		{Trusted, OwnertrustFull},
		{Full, OwnertrustFull},
		{Admin, OwnertrustFull},
		{Ultimate, OwnertrustUltimate},
	}
	for _, tt := range tests {
		if got := tt.level.Ownertrust(); got != tt.want {
			t.Errorf("%s.Ownertrust() = %s, want %s", tt.level, got, tt.want)
		}
	}
}

// TestComputeValidity_WebOfTrustRule exercises the PGP web-of-trust
// validity rule directly against internal/trust.Graph: >=3 marginal
// trusters, or >=1 full/ultimate truster, computes VALID.
func TestComputeValidity_WebOfTrustRule(t *testing.T) {
	t.Run("nil graph is fail-safe", func(t *testing.T) {
		valid, marginal, full := ComputeValidity(nil, "subject")
		if valid || marginal != 0 || full != 0 {
			t.Fatalf("nil graph: valid=%v marginal=%d full=%d, want false/0/0", valid, marginal, full)
		}
	})

	t.Run("2 marginal signers is not valid", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, "signer1", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "signer2", "subject", Marginal.EdgeWeight())
		valid, marginal, full := ComputeValidity(g, "subject")
		if valid || marginal != 2 || full != 0 {
			t.Fatalf("2 marginal: valid=%v marginal=%d full=%d, want false/2/0", valid, marginal, full)
		}
	})

	t.Run("3 marginal signers is valid", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, "signer1", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "signer2", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "signer3", "subject", Marginal.EdgeWeight())
		valid, marginal, full := ComputeValidity(g, "subject")
		if !valid || marginal != 3 || full != 0 {
			t.Fatalf("3 marginal: valid=%v marginal=%d full=%d, want true/3/0", valid, marginal, full)
		}
	})

	t.Run("1 full signer is valid", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, "signer1", "subject", Full.EdgeWeight())
		valid, marginal, full := ComputeValidity(g, "subject")
		if !valid || marginal != 0 || full != 1 {
			t.Fatalf("1 full: valid=%v marginal=%d full=%d, want true/0/1", valid, marginal, full)
		}
	})

	t.Run("unknown/never trusters contribute no bonus", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, "signer1", "subject", Unknown.EdgeWeight())
		mustSetEdge(t, g, "signer2", "subject", Unknown.EdgeWeight())
		mustSetEdge(t, g, "signer3", "subject", Unknown.EdgeWeight())
		valid, marginal, full := ComputeValidity(g, "subject")
		if valid || marginal != 0 || full != 0 {
			t.Fatalf("unknown trusters: valid=%v marginal=%d full=%d, want false/0/0", valid, marginal, full)
		}
	})
}

func mustSetEdge(t *testing.T, g *trust.Graph, truster, trustee string, weight float64) {
	t.Helper()
	if err := g.SetEdge(trust.Edge{Truster: truster, Trustee: trustee, Weight: weight}); err != nil {
		t.Fatalf("SetEdge(%s->%s) failed: %v", truster, trustee, err)
	}
}

// TestComputeValidityRooted_WebOfTrustRule exercises the ROOTED PGP
// web-of-trust validity rule (Phase C6) directly against
// internal/trust.Graph: a truster's vote only counts if it is itself
// trust-anchored w.r.t. root (root itself, or a peer root directly trusts
// at >=Marginal — a depth-1 "trusted introducer"). This is the regression
// coverage for the C6 finding: 3 self-minted, unrooted identities must NOT
// be able to manufacture computed validity for each other.
func TestComputeValidityRooted_WebOfTrustRule(t *testing.T) {
	const root = "root"

	t.Run("nil graph is fail-safe", func(t *testing.T) {
		valid, marginal, full := ComputeValidityRooted(nil, root, "subject")
		if valid || marginal != 0 || full != 0 {
			t.Fatalf("nil graph: valid=%v marginal=%d full=%d, want false/0/0", valid, marginal, full)
		}
	})

	t.Run("empty root is fail-safe even with a well-formed graph", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, "signer1", "subject", Full.EdgeWeight())
		valid, marginal, full := ComputeValidityRooted(g, "", "subject")
		if valid || marginal != 0 || full != 0 {
			t.Fatalf("empty root: valid=%v marginal=%d full=%d, want false/0/0", valid, marginal, full)
		}
	})

	t.Run("3 unrooted self-minted marginal trusters do NOT make a subject valid", func(t *testing.T) {
		g := trust.NewGraph()
		// signer1..3 are self-minted identities asserting marginal trust
		// for subject; root never trust-anchored any of them (no
		// root->signer edge exists at all).
		mustSetEdge(t, g, "signer1", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "signer2", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "signer3", "subject", Marginal.EdgeWeight())

		valid, marginal, full := ComputeValidityRooted(g, root, "subject")
		if valid || marginal != 0 || full != 0 {
			t.Fatalf("3 unrooted self-minted marginal trusters must not validate: valid=%v marginal=%d full=%d, want false/0/0", valid, marginal, full)
		}
	})

	t.Run("3 marginals from trusters that the root directly trusts DO make a subject valid", func(t *testing.T) {
		g := trust.NewGraph()
		for _, signer := range []string{"signer1", "signer2", "signer3"} {
			mustSetEdge(t, g, root, signer, Marginal.EdgeWeight()) // root trust-anchors the introducer
			mustSetEdge(t, g, signer, "subject", Marginal.EdgeWeight())
		}

		valid, marginal, full := ComputeValidityRooted(g, root, "subject")
		if !valid || marginal != 3 || full != 0 {
			t.Fatalf("3 marginals from root-trusted introducers should validate: valid=%v marginal=%d full=%d, want true/3/0", valid, marginal, full)
		}
	})

	t.Run("1 root-trusted full introducer DOES make a subject valid", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, root, "introducer", Marginal.EdgeWeight()) // root anchors the introducer
		mustSetEdge(t, g, "introducer", "subject", Full.EdgeWeight())

		valid, marginal, full := ComputeValidityRooted(g, root, "subject")
		if !valid || marginal != 0 || full != 1 {
			t.Fatalf("1 root-trusted full introducer should validate: valid=%v marginal=%d full=%d, want true/0/1", valid, marginal, full)
		}
	})

	t.Run("root's own direct vote counts without a separate anchoring edge", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, root, "subject", Full.EdgeWeight())

		valid, marginal, full := ComputeValidityRooted(g, root, "subject")
		if !valid || marginal != 0 || full != 1 {
			t.Fatalf("root's own full vote should validate directly: valid=%v marginal=%d full=%d, want true/0/1", valid, marginal, full)
		}
	})

	t.Run("an unrooted 3rd vote cannot round out an otherwise-insufficient rooted count", func(t *testing.T) {
		g := trust.NewGraph()
		mustSetEdge(t, g, root, "introducer1", Marginal.EdgeWeight())
		mustSetEdge(t, g, root, "introducer2", Marginal.EdgeWeight())
		mustSetEdge(t, g, "introducer1", "subject", Marginal.EdgeWeight())
		mustSetEdge(t, g, "introducer2", "subject", Marginal.EdgeWeight())
		// A 3rd, unrooted self-minted signer tries to make up the missing
		// vote to reach MinMarginalTrusters — it must be ignored.
		mustSetEdge(t, g, "outsider", "subject", Marginal.EdgeWeight())

		valid, marginal, full := ComputeValidityRooted(g, root, "subject")
		if valid || marginal != 2 || full != 0 {
			t.Fatalf("unrooted 3rd vote must not count toward validity: valid=%v marginal=%d full=%d, want false/2/0", valid, marginal, full)
		}
	})
}

// TestRegistry_EffectiveTrustLevel_LivePath exercises the LIVE accessors
// (IsAllowed/IsTrusted/EffectiveTrustLevel) the daemon actually calls, not
// ComputeValidity directly, per the C2 test requirement.
func TestRegistry_EffectiveTrustLevel_LivePath(t *testing.T) {
	t.Run("empty/absent graph degrades to direct assignment", func(t *testing.T) {
		registry := NewRegistry(false, nil)
		registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Untrusted})
		if registry.IsAllowed(testPeerID1) {
			t.Fatal("no graph wired: Untrusted peer should remain disallowed")
		}
		if registry.EffectiveTrustLevel(testPeerID1) != Untrusted {
			t.Fatalf("no graph wired: EffectiveTrustLevel = %v, want Untrusted", registry.EffectiveTrustLevel(testPeerID1))
		}

		// Wiring an EMPTY graph must be equally inert.
		registry.SetTrustGraph(trust.NewGraph())
		if registry.IsAllowed(testPeerID1) {
			t.Fatal("empty graph: Untrusted peer should remain disallowed")
		}
	})

	t.Run("3 marginal trusters from root-trusted introducers elevates an unassigned peer to allowed", func(t *testing.T) {
		// Phase C6: the live path is ROOTED, so trusters must themselves be
		// trust-anchored to the registry's own root identity before their
		// votes count.
		registry := NewRegistry(true, nil) // strict mode: unknown peers start Untrusted
		registry.SetRootIdentity(testPeerID3)
		g := trust.NewGraph()
		subject := testPeerID1.String()
		root := testPeerID3.String()
		for _, signer := range []string{"s1", "s2", "s3"} {
			mustSetEdge(t, g, root, signer, Marginal.EdgeWeight()) // root trust-anchors each introducer
			mustSetEdge(t, g, signer, subject, Marginal.EdgeWeight())
		}
		registry.SetTrustGraph(g)

		if !registry.IsAllowed(testPeerID1) {
			t.Fatal("3 marginal trusters from root-trusted introducers should elevate an otherwise-untrusted peer to allowed")
		}
		if got := registry.EffectiveTrustLevel(testPeerID1); got != Marginal {
			t.Fatalf("EffectiveTrustLevel = %v, want Marginal", got)
		}
		if !registry.IsValid(testPeerID1) {
			t.Fatal("IsValid should be true")
		}
	})

	t.Run("1 root-trusted full introducer elevates an unassigned peer to allowed", func(t *testing.T) {
		registry := NewRegistry(true, nil)
		registry.SetRootIdentity(testPeerID3)
		g := trust.NewGraph()
		subject := testPeerID1.String()
		root := testPeerID3.String()
		mustSetEdge(t, g, root, "s1", Marginal.EdgeWeight()) // root trust-anchors the introducer
		mustSetEdge(t, g, "s1", subject, Full.EdgeWeight())
		registry.SetTrustGraph(g)

		if !registry.IsAllowed(testPeerID1) {
			t.Fatal("1 full truster from a root-trusted introducer should elevate an otherwise-untrusted peer to allowed")
		}
	})

	t.Run("2 marginal trusters (even root-trusted) is not enough", func(t *testing.T) {
		registry := NewRegistry(true, nil)
		registry.SetRootIdentity(testPeerID3)
		g := trust.NewGraph()
		subject := testPeerID1.String()
		root := testPeerID3.String()
		mustSetEdge(t, g, root, "s1", Marginal.EdgeWeight())
		mustSetEdge(t, g, root, "s2", Marginal.EdgeWeight())
		mustSetEdge(t, g, "s1", subject, Marginal.EdgeWeight())
		mustSetEdge(t, g, "s2", subject, Marginal.EdgeWeight())
		registry.SetTrustGraph(g)

		if registry.IsAllowed(testPeerID1) {
			t.Fatal("2 marginal trusters should not clear validity")
		}
	})

	t.Run("3 unrooted self-minted marginal trusters do NOT elevate (C6 regression)", func(t *testing.T) {
		// The root IS wired here, but s1/s2/s3 are self-minted identities
		// the root never trust-anchored (no root->signer edges at all):
		// this is exactly the C6 attack the rooted live path must close.
		registry := NewRegistry(true, nil)
		registry.SetRootIdentity(testPeerID3)
		g := trust.NewGraph()
		subject := testPeerID1.String()
		mustSetEdge(t, g, "s1", subject, Marginal.EdgeWeight())
		mustSetEdge(t, g, "s2", subject, Marginal.EdgeWeight())
		mustSetEdge(t, g, "s3", subject, Marginal.EdgeWeight())
		registry.SetTrustGraph(g)

		if registry.IsAllowed(testPeerID1) {
			t.Fatal("3 self-minted unrooted marginal trusters must not elevate validity (C6)")
		}
		if got := registry.EffectiveTrustLevel(testPeerID1); got != Untrusted {
			t.Fatalf("EffectiveTrustLevel = %v, want Untrusted (no unrooted bonus)", got)
		}
	})

	t.Run("no root identity wired: even a well-formed full truster grants no bonus", func(t *testing.T) {
		registry := NewRegistry(true, nil) // root intentionally NOT set
		g := trust.NewGraph()
		subject := testPeerID1.String()
		mustSetEdge(t, g, "s1", subject, Full.EdgeWeight())
		registry.SetTrustGraph(g)

		if registry.IsAllowed(testPeerID1) {
			t.Fatal("no root identity wired: computed validity must grant no bonus (fail-safe)")
		}
	})

	t.Run("Never is a hard veto computed validity cannot override", func(t *testing.T) {
		registry := NewRegistry(false, nil)
		registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Never})
		g := trust.NewGraph()
		subject := testPeerID1.String()
		mustSetEdge(t, g, "s1", subject, Full.EdgeWeight())
		registry.SetTrustGraph(g)

		if registry.IsAllowed(testPeerID1) {
			t.Fatal("Never must veto computed validity")
		}
		if registry.EffectiveTrustLevel(testPeerID1) != Never {
			t.Fatalf("EffectiveTrustLevel = %v, want Never", registry.EffectiveTrustLevel(testPeerID1))
		}
	})

	t.Run("direct assignment wins where higher than computed floor", func(t *testing.T) {
		registry := NewRegistry(false, nil)
		registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Admin})
		registry.SetRootIdentity(testPeerID3)
		g := trust.NewGraph()
		subject := testPeerID1.String()
		root := testPeerID3.String()
		// Well-formed, root-anchored marginal votes really would compute
		// VALID (floor Marginal) on their own — the point of this test is
		// that Admin still wins since it is already higher.
		for _, signer := range []string{"s1", "s2", "s3"} {
			mustSetEdge(t, g, root, signer, Marginal.EdgeWeight())
			mustSetEdge(t, g, signer, subject, Marginal.EdgeWeight())
		}
		registry.SetTrustGraph(g)

		if got := registry.EffectiveTrustLevel(testPeerID1); got != Admin {
			t.Fatalf("EffectiveTrustLevel = %v, want Admin (direct assignment must win)", got)
		}
	})

	t.Run("IsFullyTrusted exposes the Phase D auto-pin hook", func(t *testing.T) {
		registry := NewRegistry(false, nil)
		registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Trusted})
		registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Standard})
		if !registry.IsFullyTrusted(testPeerID1) {
			t.Error("Trusted/Full peer should be IsFullyTrusted")
		}
		if registry.IsFullyTrusted(testPeerID2) {
			t.Error("Standard peer should not be IsFullyTrusted")
		}
	})
}

func TestNewRegistryTreatsTypedNilPersistenceAsInMemory(t *testing.T) {
	var persistence *FlatSQLPersistence

	registry := NewRegistry(false, persistence)

	if registry == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if registry.persistence != nil {
		t.Fatal("typed nil persistence should be treated as in-memory persistence")
	}
	if len(registry.peers) != 0 {
		t.Fatalf("new in-memory registry has %d peers, want 0", len(registry.peers))
	}
}

func TestTrustLevel_JSON(t *testing.T) {
	tests := []TrustLevel{Untrusted, Limited, Standard, Trusted, Admin}

	for _, level := range tests {
		data, err := json.Marshal(level)
		if err != nil {
			t.Errorf("Marshal TrustLevel(%d) failed: %v", level, err)
			continue
		}

		var decoded TrustLevel
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Errorf("Unmarshal TrustLevel(%d) failed: %v", level, err)
			continue
		}

		if decoded != level {
			t.Errorf("JSON round-trip: got %v, want %v", decoded, level)
		}
	}
}

func TestRegistry_AddPeer(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
		Name:       "Test Peer",
	}

	// Add peer
	if err := registry.AddPeer(tp); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	// Verify peer was added
	got, err := registry.GetPeer(testPeerID1)
	if err != nil {
		t.Fatalf("GetPeer failed: %v", err)
	}

	if got.Name != "Test Peer" {
		t.Errorf("Got name %q, want %q", got.Name, "Test Peer")
	}

	if got.AddedAt.IsZero() {
		t.Error("AddedAt should be set automatically")
	}

	// Try to add duplicate
	if err := registry.AddPeer(tp); err != ErrPeerAlreadyExists {
		t.Errorf("Expected ErrPeerAlreadyExists, got %v", err)
	}
}

func TestRegistry_RemovePeer(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Remove peer
	if err := registry.RemovePeer(testPeerID1); err != nil {
		t.Fatalf("RemovePeer failed: %v", err)
	}

	// Verify peer was removed
	if _, err := registry.GetPeer(testPeerID1); err != ErrPeerNotFound {
		t.Errorf("Expected ErrPeerNotFound, got %v", err)
	}

	// Remove non-existent peer
	if err := registry.RemovePeer(testPeerID1); err != ErrPeerNotFound {
		t.Errorf("Expected ErrPeerNotFound, got %v", err)
	}
}

func TestRegistry_SetTrustLevel(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Update trust level
	if err := registry.SetTrustLevel(testPeerID1, Trusted); err != nil {
		t.Fatalf("SetTrustLevel failed: %v", err)
	}

	// Verify
	got := registry.GetTrustLevel(testPeerID1)
	if got != Trusted {
		t.Errorf("Got trust level %v, want %v", got, Trusted)
	}
}

func TestRegistry_GetTrustLevel_StrictMode(t *testing.T) {
	// Non-strict mode: unknown peers get Standard
	registry := NewRegistry(false, nil)
	if got := registry.GetTrustLevel(testPeerID1); got != Standard {
		t.Errorf("Non-strict mode: got %v, want %v", got, Standard)
	}

	// Strict mode: unknown peers get Untrusted
	strictRegistry := NewRegistry(true, nil)
	if got := strictRegistry.GetTrustLevel(testPeerID1); got != Untrusted {
		t.Errorf("Strict mode: got %v, want %v", got, Untrusted)
	}
}

func TestRegistry_IsAllowed(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peer with different trust levels
	registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Untrusted})
	registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Limited})
	registry.AddPeer(&TrustedPeer{ID: testPeerID3, TrustLevel: Trusted})

	tests := []struct {
		peerID  peer.ID
		allowed bool
	}{
		{testPeerID1, false}, // Untrusted
		{testPeerID2, true},  // Limited
		{testPeerID3, true},  // Trusted
	}

	for _, tt := range tests {
		if got := registry.IsAllowed(tt.peerID); got != tt.allowed {
			t.Errorf("IsAllowed(%s) = %v, want %v", tt.peerID.ShortString(), got, tt.allowed)
		}
	}
}

func TestRegistry_Groups(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add a peer
	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Create a group
	group := &PeerGroup{
		Name:              "test-group",
		Description:       "Test Group",
		DefaultTrustLevel: Trusted,
	}

	if err := registry.AddGroup(group); err != nil {
		t.Fatalf("AddGroup failed: %v", err)
	}

	// Add peer to group
	if err := registry.AddPeerToGroup(testPeerID1, "test-group"); err != nil {
		t.Fatalf("AddPeerToGroup failed: %v", err)
	}

	// Verify peer is in group
	peers := registry.ListPeersByGroup("test-group")
	if len(peers) != 1 || peers[0].ID != testPeerID1 {
		t.Error("Peer should be in group")
	}

	// Verify peer has group in their list
	updatedPeer, _ := registry.GetPeer(testPeerID1)
	found := false
	for _, g := range updatedPeer.Groups {
		if g == "test-group" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Peer should have group in their list")
	}

	// Remove peer from group
	if err := registry.RemovePeerFromGroup(testPeerID1, "test-group"); err != nil {
		t.Fatalf("RemovePeerFromGroup failed: %v", err)
	}

	peers = registry.ListPeersByGroup("test-group")
	if len(peers) != 0 {
		t.Error("Peer should not be in group after removal")
	}
}

func TestRegistry_ListPeersByTrustLevel(t *testing.T) {
	registry := NewRegistry(false, nil)

	registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Standard})
	registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Trusted})
	registry.AddPeer(&TrustedPeer{ID: testPeerID3, TrustLevel: Standard})

	standardPeers := registry.ListPeersByTrustLevel(Standard)
	if len(standardPeers) != 2 {
		t.Errorf("Expected 2 standard peers, got %d", len(standardPeers))
	}

	trustedPeers := registry.ListPeersByTrustLevel(Trusted)
	if len(trustedPeers) != 1 {
		t.Errorf("Expected 1 trusted peer, got %d", len(trustedPeers))
	}
}

func TestRegistry_RecordConnection(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Record connection
	registry.RecordConnection(testPeerID1)

	got, _ := registry.GetPeer(testPeerID1)
	if got.ConnectionCount != 1 {
		t.Errorf("ConnectionCount = %d, want 1", got.ConnectionCount)
	}
	if got.LastConnected.IsZero() {
		t.Error("LastConnected should be set")
	}
}

func TestRegistry_RecordMessage(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Record sent message
	registry.RecordMessage(testPeerID1, true, 1000)

	got, _ := registry.GetPeer(testPeerID1)
	if got.MessagesSent != 1 {
		t.Errorf("MessagesSent = %d, want 1", got.MessagesSent)
	}
	if got.BytesSent != 1000 {
		t.Errorf("BytesSent = %d, want 1000", got.BytesSent)
	}

	// Record received message
	registry.RecordMessage(testPeerID1, false, 2000)

	got, _ = registry.GetPeer(testPeerID1)
	if got.MessagesReceived != 1 {
		t.Errorf("MessagesReceived = %d, want 1", got.MessagesReceived)
	}
	if got.BytesReceived != 2000 {
		t.Errorf("BytesReceived = %d, want 2000", got.BytesReceived)
	}
}

func TestRegistry_ExportImport(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peers and groups
	registry.AddPeer(&TrustedPeer{
		ID:           testPeerID1,
		TrustLevel:   Trusted,
		Name:         "Peer 1",
		Organization: "Org 1",
	})
	registry.AddPeer(&TrustedPeer{
		ID:           testPeerID2,
		TrustLevel:   Standard,
		Name:         "Peer 2",
		Organization: "Org 2",
	})
	registry.AddGroup(&PeerGroup{
		Name:              "group-1",
		Description:       "Test Group",
		DefaultTrustLevel: Trusted,
	})

	// Export
	data, err := registry.Export()
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Create new registry and import
	newRegistry := NewRegistry(false, nil)
	if err := newRegistry.Import(data, false); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify
	if newRegistry.PeerCount() != 2 {
		t.Errorf("PeerCount = %d, want 2", newRegistry.PeerCount())
	}
	if newRegistry.GroupCount() != 1 {
		t.Errorf("GroupCount = %d, want 1", newRegistry.GroupCount())
	}

	peer1, _ := newRegistry.GetPeer(testPeerID1)
	if peer1.Name != "Peer 1" || peer1.TrustLevel != Trusted {
		t.Error("Imported peer 1 data mismatch")
	}
}

func TestTrustedPeer_JSON(t *testing.T) {
	tp := &TrustedPeer{
		ID:           testPeerID1,
		TrustLevel:   Trusted,
		Name:         "Test Peer",
		Organization: "Test Org",
		Groups:       []string{"group1", "group2"},
		AddedAt:      time.Now(),
		Metadata:     map[string]string{"key": "value"},
	}

	data, err := json.Marshal(tp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded TrustedPeer
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != tp.ID {
		t.Errorf("ID mismatch: got %s, want %s", decoded.ID, tp.ID)
	}
	if decoded.TrustLevel != tp.TrustLevel {
		t.Errorf("TrustLevel mismatch: got %v, want %v", decoded.TrustLevel, tp.TrustLevel)
	}
	if decoded.Name != tp.Name {
		t.Errorf("Name mismatch: got %s, want %s", decoded.Name, tp.Name)
	}
	if len(decoded.Groups) != 2 {
		t.Errorf("Groups length mismatch: got %d, want 2", len(decoded.Groups))
	}
}

func TestRegistry_GetTrustedAddrInfos(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peers with different trust levels
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	})
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID2,
		TrustLevel: Trusted,
	})
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID3,
		TrustLevel: Admin,
	})

	// Note: No addresses, so all should have empty Addrs
	// GetTrustedAddrInfos only returns peers with Trusted+ AND addresses
	infos := registry.GetTrustedAddrInfos()

	// Without addresses, no peers should be returned
	if len(infos) != 0 {
		t.Errorf("Expected 0 AddrInfos (no addresses), got %d", len(infos))
	}
}
