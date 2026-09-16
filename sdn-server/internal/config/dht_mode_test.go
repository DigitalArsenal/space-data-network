package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Owner rule: a node must be able to discover AND be discovered through the
// DHT. Only the second half needs serving — a client is in nobody's routing
// table and answers nothing, so FindPeer against it fails.
func TestDHTServerDefaultsToAuto(t *testing.T) {
	if got := Default().Network.DHTServer; got != DHTServerModeAuto {
		t.Fatalf("dht_server defaults to %v, want auto: a reachable node must SERVE the DHT or it "+
			"cannot be found through it", got)
	}
}

// host-01 set dht_server: false after 2026-08-08, when serving the public DHT
// pinned it at 98.5%% CPU. That decision must survive this change verbatim.
func TestDHTServerBooleansKeepTheirMeaning(t *testing.T) {
	for input, want := range map[string]DHTServerMode{
		"dht_server: false":  DHTServerModeNever,
		"dht_server: true":   DHTServerModeAlways,
		"dht_server: never":  DHTServerModeNever,
		"dht_server: always": DHTServerModeAlways,
		"dht_server: auto":   DHTServerModeAuto,
	} {
		var probe struct {
			DHTServer DHTServerMode `yaml:"dht_server"`
		}
		if err := yaml.Unmarshal([]byte(input), &probe); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if probe.DHTServer != want {
			t.Errorf("%q = %v, want %v", input, probe.DHTServer, want)
		}
	}
}

func TestDHTServerRejectsNonsense(t *testing.T) {
	var probe struct {
		DHTServer DHTServerMode `yaml:"dht_server"`
	}
	if err := yaml.Unmarshal([]byte("dht_server: occasionally"), &probe); err == nil {
		t.Fatal("an unknown mode must be refused, not silently defaulted")
	}
}

// Both reachability-gated settings must agree on their vocabulary, or an
// operator has to remember which one takes which words.
func TestRelayAndDHTShareTheSameVocabulary(t *testing.T) {
	for _, word := range []string{"auto", "always", "never", "true", "false"} {
		var relay struct {
			V RelayMode `yaml:"v"`
		}
		var dhtMode struct {
			V DHTServerMode `yaml:"v"`
		}
		relayErr := yaml.Unmarshal([]byte("v: "+word), &relay)
		dhtErr := yaml.Unmarshal([]byte("v: "+word), &dhtMode)
		if (relayErr == nil) != (dhtErr == nil) {
			t.Errorf("%q: relay err=%v but dht err=%v", word, relayErr, dhtErr)
		}
		if relayErr == nil && relay.V.String() != dhtMode.V.String() {
			t.Errorf("%q: relay=%s dht=%s", word, relay.V, dhtMode.V)
		}
	}
}
