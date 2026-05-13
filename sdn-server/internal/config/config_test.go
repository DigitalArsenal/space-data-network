package config

import "testing"

func TestDefaultListenAddressesIncludeBrowserPeerTransports(t *testing.T) {
	cfg := Default()

	requireListenAddress(t, cfg.Network.Listen, "/ip4/0.0.0.0/tcp/8080/ws")
	requireListenAddress(t, cfg.Network.Listen, "/ip4/0.0.0.0/udp/4003/webrtc-direct")
}

func requireListenAddress(t *testing.T, addrs []string, want string) {
	t.Helper()
	for _, addr := range addrs {
		if addr == want {
			return
		}
	}
	t.Fatalf("default network listen addresses missing %s: %#v", want, addrs)
}
