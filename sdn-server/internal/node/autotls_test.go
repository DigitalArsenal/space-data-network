package node

import (
	"crypto/tls"
	"reflect"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// A node that enables AutoTLS must gain a browser-dialable listen address for
// every plain TCP listener it already had — WITHOUT the operator hand-writing
// the wildcard SNI multiaddr, and without a second port appearing.
func TestWithAutoTLSListenAddrsDerivesWSSPerTCPListener(t *testing.T) {
	got := withAutoTLSListenAddrs([]string{
		"/ip4/0.0.0.0/tcp/4001",
		"/ip6/::/tcp/4001",
	}, "libp2p.direct")

	want := []string{
		"/ip4/0.0.0.0/tcp/4001",
		"/ip4/0.0.0.0/tcp/4001/tls/sni/*.libp2p.direct/ws",
		"/ip6/::/tcp/4001",
		"/ip6/::/tcp/4001/tls/sni/*.libp2p.direct/ws",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived listen addrs\n got: %v\nwant: %v", got, want)
	}
}

// Decorating an address that is already a websocket, a QUIC or a WebRTC
// listener would produce a multiaddr no transport can bind, taking the whole
// node down at boot. Those must pass through untouched.
func TestWithAutoTLSListenAddrsLeavesNonPlainTCPAlone(t *testing.T) {
	in := []string{
		"/ip4/0.0.0.0/tcp/8080/ws",
		"/ip4/0.0.0.0/udp/4001/quic-v1",
		"/ip4/0.0.0.0/udp/4003/webrtc-direct",
		"/ip4/0.0.0.0/tcp/4002/tls/ws",
	}
	got := withAutoTLSListenAddrs(in, "libp2p.direct")
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("non-plain-TCP addrs were rewritten\n got: %v\nwant: %v", got, in)
	}
}

// An operator who already wrote the SNI address explicitly must not get a
// duplicate listener on the same port.
func TestWithAutoTLSListenAddrsIsIdempotent(t *testing.T) {
	in := []string{
		"/ip4/0.0.0.0/tcp/4001",
		"/ip4/0.0.0.0/tcp/4001/tls/sni/*.libp2p.direct/ws",
	}
	got := withAutoTLSListenAddrs(in, "libp2p.direct")
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("idempotency broken\n got: %v\nwant: %v", got, in)
	}
	if second := withAutoTLSListenAddrs(got, "libp2p.direct"); !reflect.DeepEqual(second, in) {
		t.Fatalf("second application changed the set: %v", second)
	}
}

// Fail-closed: the connector is off unless explicitly enabled.
func TestNewAutoTLSCertManagerDisabledByDefault(t *testing.T) {
	mgr, err := newAutoTLSCertManager(config.AutoTLSConfig{}, t.TempDir())
	if err != nil {
		t.Fatalf("disabled autotls returned error: %v", err)
	}
	if mgr != nil {
		t.Fatal("autotls certificate manager was built while disabled")
	}
}

func TestNewAutoTLSCertManagerEnabledBuildsManager(t *testing.T) {
	mgr, err := newAutoTLSCertManager(config.AutoTLSConfig{Enabled: true}, t.TempDir())
	if err != nil {
		t.Fatalf("enabled autotls: %v", err)
	}
	if mgr == nil {
		t.Fatal("enabled autotls produced no certificate manager")
	}
	if tlsCfg := mgr.TLSConfig(); tlsCfg == nil || tlsCfg.GetCertificate == nil {
		t.Fatal("certificate manager produced no usable TLS config")
	}
}

// A relative storage path silently resolves against the daemon's working
// directory, which on a systemd host is "/" — the certificate would land
// somewhere nobody backs up and be re-issued on every boot. Reject it at boot.
func TestNewAutoTLSCertManagerRejectsRelativeStoragePath(t *testing.T) {
	_, err := newAutoTLSCertManager(config.AutoTLSConfig{Enabled: true, StoragePath: "certs"}, t.TempDir())
	if err == nil {
		t.Fatal("relative storage_path was accepted")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

func TestNewAutoTLSCertManagerRejectsBadRegistrationDelay(t *testing.T) {
	if _, err := newAutoTLSCertManager(config.AutoTLSConfig{Enabled: true, RegistrationDelay: "soon"}, t.TempDir()); err == nil {
		t.Fatal("invalid registration_delay was accepted")
	}
	if _, err := newAutoTLSCertManager(config.AutoTLSConfig{Enabled: true, RegistrationDelay: "-5s"}, t.TempDir()); err == nil {
		t.Fatal("negative registration_delay was accepted")
	}
}

func TestAutoTLSDomainDefaultsToLibp2pDirect(t *testing.T) {
	if got := autoTLSDomain(config.AutoTLSConfig{}); got != "libp2p.direct" {
		t.Fatalf("default forge domain = %q, want libp2p.direct", got)
	}
	if got := autoTLSDomain(config.AutoTLSConfig{DomainSuffix: "forge.example"}); got != "forge.example" {
		t.Fatalf("configured forge domain = %q", got)
	}
}

// The whole point: with a TLS config the websocket transport BINDS the
// wildcard-SNI address (and shares the TCP port), and without one it must not
// — a browser-dialable socket may never come up unauthenticated.
func TestHostTransportsBindAutoTLSAddrOnlyWithTLSConfig(t *testing.T) {
	mgr, err := newAutoTLSCertManager(config.AutoTLSConfig{Enabled: true}, t.TempDir())
	if err != nil {
		t.Fatalf("cert manager: %v", err)
	}

	listen := []string{
		"/ip4/127.0.0.1/tcp/0",
		"/ip4/127.0.0.1/tcp/0/tls/sni/*.libp2p.direct/ws",
	}
	host, err := libp2p.New(append([]libp2p.Option{
		libp2p.ListenAddrStrings(listen...),
	}, hostTransportOptions(mgr.TLSConfig())...)...)
	if err != nil {
		t.Fatalf("libp2p.New with autotls transports: %v", err)
	}
	defer host.Close()

	var hasSNIWS bool
	for _, addr := range host.Addrs() {
		if strings.Contains(addr.String(), "/tls/sni/") && strings.HasSuffix(addr.String(), "/ws") {
			hasSNIWS = true
		}
	}
	if !hasSNIWS {
		t.Fatalf("autotls websocket address not bound: %v", host.Addrs())
	}

	// Same address, no TLS config: the listener must refuse rather than serve
	// plaintext on a name browsers will treat as TLS.
	plain, err := libp2p.New(append([]libp2p.Option{
		libp2p.ListenAddrStrings(listen...),
	}, hostTransportOptions(nil)...)...)
	if err == nil {
		defer plain.Close()
		for _, addr := range plain.Addrs() {
			if strings.Contains(addr.String(), "/tls/sni/") {
				t.Fatalf("wildcard-SNI websocket bound with no TLS config: %v", plain.Addrs())
			}
		}
	}
}

// Guard against a future refactor handing the transport an empty tls.Config
// (which would serve a certificate-less handshake) instead of the manager's.
func TestHostTransportOptionsWithoutTLSConfigOmitsSharedListener(t *testing.T) {
	if got := len(hostTransportOptions(nil)); got != 5 {
		t.Fatalf("transport option count without autotls = %d, want 5 (no ShareTCPListener)", got)
	}
	if got := len(hostTransportOptions(&tls.Config{})); got != 6 {
		t.Fatalf("transport option count with autotls = %d, want 6 (ShareTCPListener added)", got)
	}
}

// The advertised-address SHAPE is the difference between an address a browser
// can dial and one @libp2p/websockets discards before opening a socket, so the
// default must be the short /dns4 form and it must survive an operator who
// never wrote the key.
func TestAutoTLSShortAddrsDefaultsTrue(t *testing.T) {
	if !autoTLSShortAddrs(config.AutoTLSConfig{}) {
		t.Fatal("short_addrs unset must default to true (long /tls/sni form is not browser-dialable)")
	}
	no := false
	if autoTLSShortAddrs(config.AutoTLSConfig{ShortAddrs: &no}) {
		t.Fatal("explicit short_addrs:false was ignored")
	}
	yes := true
	if !autoTLSShortAddrs(config.AutoTLSConfig{ShortAddrs: &yes}) {
		t.Fatal("explicit short_addrs:true was ignored")
	}
}
