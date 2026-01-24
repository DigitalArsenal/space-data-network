package pubsub

import (
	"testing"
	"time"
)

func TestNewTipQueueConfig(t *testing.T) {
	config := NewTipQueueConfig()

	if config.DefaultAutoFetch != DefaultAutoFetch {
		t.Errorf("DefaultAutoFetch mismatch: got %v, want %v", config.DefaultAutoFetch, DefaultAutoFetch)
	}
	if config.DefaultAutoPin != DefaultAutoPin {
		t.Errorf("DefaultAutoPin mismatch: got %v, want %v", config.DefaultAutoPin, DefaultAutoPin)
	}
	if config.DefaultTTL != DefaultTTL {
		t.Errorf("DefaultTTL mismatch: got %v, want %v", config.DefaultTTL, DefaultTTL)
	}
	if config.MaxQueueSize != DefaultMaxQueueSize {
		t.Errorf("MaxQueueSize mismatch: got %v, want %v", config.MaxQueueSize, DefaultMaxQueueSize)
	}
	if config.FetchTimeout != DefaultFetchTimeout {
		t.Errorf("FetchTimeout mismatch: got %v, want %v", config.FetchTimeout, DefaultFetchTimeout)
	}
}

func TestResolveConfigSystemDefaults(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true
	config.DefaultTTL = 48 * time.Hour

	resolved := config.ResolveConfig("peer1", "OMM")

	if !resolved.AutoFetch {
		t.Error("Expected AutoFetch to be true from system default")
	}
	if !resolved.AutoPin {
		t.Error("Expected AutoPin to be true from system default")
	}
	if resolved.TTL != 48*time.Hour {
		t.Errorf("Expected TTL 48h, got %v", resolved.TTL)
	}
	if resolved.Trusted {
		t.Error("Expected Trusted to be false by default")
	}
}

func TestResolveConfigSchemaDefaults(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = false
	config.DefaultAutoPin = false

	// Set schema default for OMM
	config.SetSchemaDefault("OMM", &SchemaConfig{
		AutoFetch: true,
		AutoPin:   true,
		TTL:       12 * time.Hour,
		Priority:  5,
	})

	// OMM should use schema defaults
	ommResolved := config.ResolveConfig("peer1", "OMM")
	if !ommResolved.AutoFetch {
		t.Error("OMM should have AutoFetch=true from schema default")
	}
	if !ommResolved.AutoPin {
		t.Error("OMM should have AutoPin=true from schema default")
	}
	if ommResolved.TTL != 12*time.Hour {
		t.Errorf("OMM should have TTL=12h, got %v", ommResolved.TTL)
	}
	if ommResolved.Priority != 5 {
		t.Errorf("OMM should have Priority=5, got %d", ommResolved.Priority)
	}

	// CDM should use system defaults
	cdmResolved := config.ResolveConfig("peer1", "CDM")
	if cdmResolved.AutoFetch {
		t.Error("CDM should have AutoFetch=false from system default")
	}
	if cdmResolved.AutoPin {
		t.Error("CDM should have AutoPin=false from system default")
	}
}

func TestResolveConfigSourceOverrides(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = false
	config.DefaultAutoPin = false

	// Set source override
	config.SetSourceOverride("trusted-peer", &SourceConfig{
		Trusted:   true,
		AutoFetch: BoolPtr(true),
		AutoPin:   BoolPtr(true),
		TTL:       DurationPtr(72 * time.Hour),
	})

	// Trusted peer should use source overrides
	trustedResolved := config.ResolveConfig("trusted-peer", "OMM")
	if !trustedResolved.AutoFetch {
		t.Error("Trusted peer should have AutoFetch=true")
	}
	if !trustedResolved.AutoPin {
		t.Error("Trusted peer should have AutoPin=true")
	}
	if trustedResolved.TTL != 72*time.Hour {
		t.Errorf("Trusted peer should have TTL=72h, got %v", trustedResolved.TTL)
	}
	if !trustedResolved.Trusted {
		t.Error("Trusted peer should have Trusted=true")
	}

	// Unknown peer should use system defaults
	unknownResolved := config.ResolveConfig("unknown-peer", "OMM")
	if unknownResolved.AutoFetch {
		t.Error("Unknown peer should have AutoFetch=false")
	}
	if unknownResolved.Trusted {
		t.Error("Unknown peer should have Trusted=false")
	}
}

func TestResolveConfigSourceSchemaOverrides(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = false
	config.DefaultAutoPin = false
	config.DefaultTTL = 24 * time.Hour

	// Set schema default
	config.SetSchemaDefault("OMM", &SchemaConfig{
		AutoFetch: true,
		AutoPin:   false,
		TTL:       12 * time.Hour,
	})

	// Set source default
	config.SetSourceOverride("special-peer", &SourceConfig{
		Trusted:   true,
		AutoFetch: BoolPtr(false), // Override schema default
	})

	// Set source+schema specific override
	config.SetSourceSchemaOverride("special-peer", "OMM", &SchemaConfig{
		AutoFetch: true,
		AutoPin:   true,
		TTL:       1 * time.Hour,
		Priority:  10,
	})

	// special-peer + OMM should use source+schema override (highest priority)
	resolved := config.ResolveConfig("special-peer", "OMM")
	if !resolved.AutoFetch {
		t.Error("Expected AutoFetch=true from source+schema override")
	}
	if !resolved.AutoPin {
		t.Error("Expected AutoPin=true from source+schema override")
	}
	if resolved.TTL != 1*time.Hour {
		t.Errorf("Expected TTL=1h from source+schema override, got %v", resolved.TTL)
	}
	if resolved.Priority != 10 {
		t.Errorf("Expected Priority=10, got %d", resolved.Priority)
	}

	// special-peer + CDM should use source override (no schema override)
	cdmResolved := config.ResolveConfig("special-peer", "CDM")
	if cdmResolved.AutoFetch {
		t.Error("Expected AutoFetch=false from source override")
	}
}

func TestResolveConfigPriorityOrder(t *testing.T) {
	config := NewTipQueueConfig()

	// Priority 4: System default
	config.DefaultAutoFetch = false
	config.DefaultAutoPin = false
	config.DefaultTTL = 24 * time.Hour

	// Priority 3: Schema default
	config.SetSchemaDefault("OMM", &SchemaConfig{
		AutoFetch: true, // Override system
		AutoPin:   false,
		TTL:       12 * time.Hour, // Override system
	})

	// Priority 2: Source default
	config.SetSourceOverride("peer1", &SourceConfig{
		AutoPin: BoolPtr(true), // Override schema default
		TTL:     DurationPtr(6 * time.Hour),
	})

	// Priority 1: Source+Schema override
	config.SetSourceSchemaOverride("peer1", "OMM", &SchemaConfig{
		TTL: 1 * time.Hour, // Override everything
	})

	resolved := config.ResolveConfig("peer1", "OMM")

	// AutoFetch: from source+schema (highest priority that sets it)
	// Note: source+schema SchemaConfig has AutoFetch=false (default value)
	if resolved.AutoFetch {
		t.Error("AutoFetch should be false from source+schema override")
	}

	// AutoPin: from source+schema (false by default in the override)
	if resolved.AutoPin {
		t.Error("AutoPin should be false from source+schema override")
	}

	// TTL: from source+schema (1h)
	if resolved.TTL != 1*time.Hour {
		t.Errorf("TTL should be 1h from source+schema, got %v", resolved.TTL)
	}
}

func TestTrustSource(t *testing.T) {
	config := NewTipQueueConfig()

	// Initially not trusted
	if config.IsTrusted("peer1") {
		t.Error("Peer should not be trusted initially")
	}

	// Trust the peer
	config.TrustSource("peer1")
	if !config.IsTrusted("peer1") {
		t.Error("Peer should be trusted after TrustSource")
	}

	// Untrust the peer
	config.UntrustSource("peer1")
	if config.IsTrusted("peer1") {
		t.Error("Peer should not be trusted after UntrustSource")
	}
}

func TestListTrustedSources(t *testing.T) {
	config := NewTipQueueConfig()

	config.TrustSource("peer1")
	config.TrustSource("peer2")
	config.SetSourceOverride("peer3", &SourceConfig{Trusted: false})

	trusted := config.ListTrustedSources()
	if len(trusted) != 2 {
		t.Errorf("Expected 2 trusted sources, got %d", len(trusted))
	}

	hasPeer1 := false
	hasPeer2 := false
	for _, p := range trusted {
		if p == "peer1" {
			hasPeer1 = true
		}
		if p == "peer2" {
			hasPeer2 = true
		}
	}
	if !hasPeer1 || !hasPeer2 {
		t.Error("Expected peer1 and peer2 in trusted list")
	}
}

func TestGetSourceConfig(t *testing.T) {
	config := NewTipQueueConfig()

	// No config initially
	_, ok := config.GetSourceConfig("peer1")
	if ok {
		t.Error("Expected no source config initially")
	}

	// Set config
	config.SetSourceOverride("peer1", &SourceConfig{
		Trusted:   true,
		AutoFetch: BoolPtr(true),
	})

	srcConfig, ok := config.GetSourceConfig("peer1")
	if !ok {
		t.Error("Expected source config to exist")
	}
	if !srcConfig.Trusted {
		t.Error("Expected Trusted=true")
	}
	if srcConfig.AutoFetch == nil || !*srcConfig.AutoFetch {
		t.Error("Expected AutoFetch=true")
	}
}

func TestGetSchemaDefault(t *testing.T) {
	config := NewTipQueueConfig()

	// No config initially
	_, ok := config.GetSchemaDefault("OMM")
	if ok {
		t.Error("Expected no schema default initially")
	}

	// Set config
	config.SetSchemaDefault("OMM", &SchemaConfig{
		AutoFetch: true,
		Priority:  5,
	})

	schemaConfig, ok := config.GetSchemaDefault("OMM")
	if !ok {
		t.Error("Expected schema default to exist")
	}
	if !schemaConfig.AutoFetch {
		t.Error("Expected AutoFetch=true")
	}
	if schemaConfig.Priority != 5 {
		t.Errorf("Expected Priority=5, got %d", schemaConfig.Priority)
	}
}

func TestConfigConcurrency(t *testing.T) {
	config := NewTipQueueConfig()

	// Concurrent reads and writes
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			peerID := "peer" + string(rune('0'+id))
			for j := 0; j < 100; j++ {
				config.TrustSource(peerID)
				config.IsTrusted(peerID)
				config.ResolveConfig(peerID, "OMM")
				config.SetSchemaDefault("OMM", &SchemaConfig{Priority: j})
				config.UntrustSource(peerID)
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestBoolPtr(t *testing.T) {
	truePtr := BoolPtr(true)
	if truePtr == nil || !*truePtr {
		t.Error("BoolPtr(true) should return pointer to true")
	}

	falsePtr := BoolPtr(false)
	if falsePtr == nil || *falsePtr {
		t.Error("BoolPtr(false) should return pointer to false")
	}
}

func TestDurationPtr(t *testing.T) {
	d := DurationPtr(5 * time.Minute)
	if d == nil || *d != 5*time.Minute {
		t.Error("DurationPtr should return correct pointer")
	}
}

func TestNilSourceConfigValues(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true
	config.DefaultTTL = 24 * time.Hour

	// Source override with nil values should fall through to defaults
	config.SetSourceOverride("peer1", &SourceConfig{
		Trusted:   true,
		AutoFetch: nil, // Should use default
		AutoPin:   nil, // Should use default
		TTL:       nil, // Should use default
	})

	resolved := config.ResolveConfig("peer1", "OMM")
	if !resolved.AutoFetch {
		t.Error("AutoFetch should fall through to default (true)")
	}
	if !resolved.AutoPin {
		t.Error("AutoPin should fall through to default (true)")
	}
	if resolved.TTL != 24*time.Hour {
		t.Errorf("TTL should fall through to default (24h), got %v", resolved.TTL)
	}
}
