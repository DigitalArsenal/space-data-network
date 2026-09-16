package config

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

func (m RelayMode) String() string { return autoModeString(int(m)) }

func (m *RelayMode) UnmarshalYAML(unmarshal func(interface{}) error) error {
	parsed, err := parseAutoMode("network.enable_relay", unmarshal)
	if err != nil {
		return err
	}
	*m = RelayMode(parsed)
	return nil
}

func (m RelayMode) MarshalYAML() (interface{}, error) { return m.String(), nil }
