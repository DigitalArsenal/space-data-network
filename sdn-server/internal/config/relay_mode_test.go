package config

import "testing"

import "gopkg.in/yaml.v3"

// The owner rule is that a public node relays by default. The compatibility
// requirement is that `enable_relay: false` still means never — host-01 set it
// after the 2026-08-08 incident and that decision must survive this change.
func TestRelayModeParsesBooleansAndNames(t *testing.T) {
	for input, want := range map[string]RelayMode{
		"enable_relay: false":  RelayModeNever,
		"enable_relay: true":   RelayModeAlways,
		"enable_relay: never":  RelayModeNever,
		"enable_relay: always": RelayModeAlways,
		"enable_relay: auto":   RelayModeAuto,
		"enable_relay: AUTO":   RelayModeAuto,
		"enable_relay: \"\"":   RelayModeAuto,
	} {
		var probe struct {
			EnableRelay RelayMode `yaml:"enable_relay"`
		}
		if err := yaml.Unmarshal([]byte(input), &probe); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if probe.EnableRelay != want {
			t.Errorf("%q = %v, want %v", input, probe.EnableRelay, want)
		}
	}
}

// An unset field must be auto, because that is the default the rule depends on.
func TestRelayModeZeroValueIsAuto(t *testing.T) {
	var zero RelayMode
	if zero != RelayModeAuto {
		t.Fatalf("zero value = %v, want auto; a node with no setting must relay when public", zero)
	}
	if zero.String() != "auto" {
		t.Fatalf("String() = %q, want auto", zero.String())
	}
}

func TestRelayModeRejectsNonsense(t *testing.T) {
	var probe struct {
		EnableRelay RelayMode `yaml:"enable_relay"`
	}
	if err := yaml.Unmarshal([]byte("enable_relay: sometimes"), &probe); err == nil {
		t.Fatal("an unknown mode must be refused, not silently treated as a default")
	}
}

// The shipped default must be auto, not the old false.
func TestDefaultConfigRelaysWhenPublic(t *testing.T) {
	cfg := Default()
	if cfg.Network.EnableRelay != RelayModeAuto {
		t.Fatalf("default enable_relay = %v, want auto", cfg.Network.EnableRelay)
	}
}
