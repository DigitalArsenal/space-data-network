package config

import (
	"fmt"
	"strings"
)

// RelayMode decides whether this node runs a public circuit-relay v2 HOP
// service — donating CPU and bandwidth to carry traffic between other peers.
//
// OWNER RULE: every public node that is not behind NAT should be a relay, and
// that is the DEFAULT. A network whose only relays are the handful someone
// remembered to configure cannot carry nodes behind corporate firewalls or
// CGNAT; those nodes connect, stay undialable, and can consume but never serve.
// Nodes that ARE reachable are exactly the ones able to fix that, so they do,
// unless an operator says otherwise.
//
// It is conditional, not unconditional, and the difference matters. On
// 2026-08-08 host-01 ran the HOP service unconditionally — the field existed
// and was never read — and sat at 98.5% CPU of 2 vCPUs with 780 inbound
// connections from ~700 distinct IPs while its actual job lost every race for
// the CPU. "Auto" only relays while AutoNAT reports this node publicly
// reachable, and the service runs under explicit resource limits.
type RelayMode int

const (
	// RelayModeAuto relays while AutoNAT says this node is publicly
	// reachable, and stops when it says otherwise. The default.
	RelayModeAuto RelayMode = iota
	// RelayModeAlways runs the HOP service regardless of reachability.
	RelayModeAlways
	// RelayModeNever never runs it. `enable_relay: false` means this, so an
	// operator who turned it off stays off.
	RelayModeNever
)

func (m RelayMode) String() string {
	switch m {
	case RelayModeAlways:
		return "always"
	case RelayModeNever:
		return "never"
	default:
		return "auto"
	}
}

// UnmarshalYAML accepts the historical booleans as well as the named modes, so
// an existing `enable_relay: false` keeps meaning exactly what its author meant.
func (m *RelayMode) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var asBool bool
	if err := unmarshal(&asBool); err == nil {
		if asBool {
			*m = RelayModeAlways
		} else {
			*m = RelayModeNever
		}
		return nil
	}

	var asString string
	if err := unmarshal(&asString); err != nil {
		return fmt.Errorf("network.enable_relay: expected true, false, or one of auto/always/never: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(asString)) {
	case "", "auto":
		*m = RelayModeAuto
	case "always", "true", "yes", "on":
		*m = RelayModeAlways
	case "never", "false", "no", "off":
		*m = RelayModeNever
	default:
		return fmt.Errorf("network.enable_relay: unknown mode %q (want auto, always, or never)", asString)
	}
	return nil
}

func (m RelayMode) MarshalYAML() (interface{}, error) { return m.String(), nil }
