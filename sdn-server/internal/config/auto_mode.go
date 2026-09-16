package config

import (
	"fmt"
	"strings"
)

// Two settings now share the same shape — network.enable_relay and
// network.dht_server — because they answer the same question: should this node
// do work for other peers? The answer depends on whether it can be reached, and
// a node behind NAT cannot usefully do either.
//
// Both accept the booleans they used to be, so a config written before this
// still means exactly what its author meant. That matters: host-01 set both to
// false after 2026-08-08, when it sat at 98.5% CPU of 2 vCPUs serving the
// public DHT and relaying strangers' traffic while its real work starved.
const (
	autoModeAuto   = 0
	autoModeAlways = 1
	autoModeNever  = 2
)

// parseAutoMode reads true/false or auto/always/never out of YAML.
func parseAutoMode(field string, unmarshal func(interface{}) error) (int, error) {
	var asBool bool
	if err := unmarshal(&asBool); err == nil {
		if asBool {
			return autoModeAlways, nil
		}
		return autoModeNever, nil
	}

	var asString string
	if err := unmarshal(&asString); err != nil {
		return autoModeAuto, fmt.Errorf("%s: expected true, false, or one of auto/always/never: %w", field, err)
	}
	switch strings.ToLower(strings.TrimSpace(asString)) {
	case "", "auto":
		return autoModeAuto, nil
	case "always", "true", "yes", "on":
		return autoModeAlways, nil
	case "never", "false", "no", "off":
		return autoModeNever, nil
	default:
		return autoModeAuto, fmt.Errorf("%s: unknown mode %q (want auto, always, or never)", field, asString)
	}
}

func autoModeString(m int) string {
	switch m {
	case autoModeAlways:
		return "always"
	case autoModeNever:
		return "never"
	default:
		return "auto"
	}
}

// DHTServerMode decides whether this node SERVES the Kademlia DHT — answers
// other peers' lookups — as opposed to only querying it.
//
// OWNER RULE: a node must be able to discover AND be discovered through the
// DHT. Those are not the same capability. A client-mode node can query all it
// likes, but it is never added to another peer's routing table and answers
// nothing, so a FindPeer against it fails: it can find everyone and nobody can
// find it. Being findable requires serving.
//
// DEFAULT IS "auto": serve while AutoNAT reports this node publicly reachable,
// and fall back to client when it does not. A node behind NAT should NOT be in
// anyone's routing table — it cannot answer, and an undialable entry wastes
// every query that walks through it.
//
// Conditional, not unconditional, for the reason in the comment above. The
// August incident was ModeAutoServer hardcoded, on a box with no admission
// control. There is a peer admission controller now (node.go, protected sets +
// value-ordered trimming), and serving is tied to reachability, so the load has
// both a ceiling and a precondition.
type DHTServerMode int

const (
	DHTServerModeAuto DHTServerMode = iota
	DHTServerModeAlways
	DHTServerModeNever
)

func (m DHTServerMode) String() string { return autoModeString(int(m)) }

func (m *DHTServerMode) UnmarshalYAML(unmarshal func(interface{}) error) error {
	parsed, err := parseAutoMode("network.dht_server", unmarshal)
	if err != nil {
		return err
	}
	*m = DHTServerMode(parsed)
	return nil
}

func (m DHTServerMode) MarshalYAML() (interface{}, error) { return m.String(), nil }
