package node

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	p2pforge "github.com/ipshipyard/p2p-forge/client"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// AutoTLS is a CONNECTOR, not a feature: it provisions the TLS material the
// libp2p websocket transport needs so that a BROWSER on an https:// origin can
// dial this node directly.
//
// Why it exists (ops-host02-browser-relay-promotion, owner ruling 2026-08-06):
// a browser cannot open a plain ws:// socket from an https:// page (mixed
// content) and will not accept a self-signed certificate hash outside
// webrtc-direct/webtransport. Before this, the ONLY CA-authenticated
// browser-dialable address in the fleet was host-01's, which terminates TLS on
// its ADMIN http server and tunnels the upgrade to a loopback libp2p ws
// listener — a mechanism that forces the node's admin surface onto the public
// internet and cannot be applied to a box whose admin listener is loopback by
// design.
//
// p2p-forge (libp2p.direct) needs no DNS record, no owner console action and no
// admin exposure: the certificate is issued for a name DERIVED FROM THIS NODE'S
// OWN PEER ID (`*.<peerid-base36>.libp2p.direct`), the DNS-01 challenge is
// brokered by the forge registration endpoint after it verifies the node is
// reachable at the multiaddrs it claims, and the forge DNS server answers the
// A record from the IP encoded in the name. Verified live on host-02
// 2026-08-06 with a SECP256K1 identity — the key type every SDN node uses:
// Let's Encrypt issued, `Verification: OK` off-box, TLSv1.3.
//
// Fail-closed: disabled unless explicitly enabled, and a manager that cannot be
// built is an error at boot, never a silently plaintext listener.

// autoTLSSNIComponent is the multiaddr fragment that tells the websocket
// transport to serve TLS with the forge wildcard certificate, selecting it by
// SNI. It is the same shape kubo uses (core/node/libp2p/addrs.go).
func autoTLSSNIComponent(domain string) string {
	return fmt.Sprintf("/tls/sni/*.%s/ws", domain)
}

// autoTLSDomain resolves the configured forge domain, defaulting to the public
// libp2p.direct broker.
func autoTLSDomain(cfg config.AutoTLSConfig) string {
	if d := strings.TrimSpace(cfg.DomainSuffix); d != "" {
		return d
	}
	return p2pforge.DefaultForgeDomain
}

// autoTLSShortAddrs resolves the advertised-address shape, defaulting to the
// short /dns4 form because that is the only one the browser stack can dial
// (see config.AutoTLSConfig.ShortAddrs for the measurement).
func autoTLSShortAddrs(cfg config.AutoTLSConfig) bool {
	if cfg.ShortAddrs == nil {
		return true
	}
	return *cfg.ShortAddrs
}

// withAutoTLSListenAddrs returns the listen addresses augmented with the
// AutoTLS websocket address for every PLAIN /tcp/<port> listener.
//
// Deriving it, rather than making the operator hand-write
// `/ip4/0.0.0.0/tcp/4001/tls/sni/*.libp2p.direct/ws`, keeps the deployed config
// diff to a single `autotls.enabled: true` and removes the class of failure
// where the flag is on but the transport was never given an address to serve —
// which presents as a node that quietly advertises nothing browser-dialable.
// The derived address shares the TCP port with the plain TCP transport
// (libp2p.ShareTCPListener), so no new port is opened and no firewall changes.
//
// Idempotent: an operator who DID write the address explicitly gets no
// duplicate, and non-TCP (quic/webrtc) or already-decorated (/ws, /tls/ws)
// addresses are left alone.
func withAutoTLSListenAddrs(listen []string, domain string) []string {
	sni := autoTLSSNIComponent(domain)
	out := make([]string, 0, len(listen)*2)
	existing := make(map[string]bool, len(listen))
	for _, addr := range listen {
		existing[strings.TrimSpace(addr)] = true
	}
	for _, addr := range listen {
		trimmed := strings.TrimSpace(addr)
		out = append(out, addr)
		if !isPlainTCPListenAddr(trimmed) {
			continue
		}
		derived := trimmed + sni
		if existing[derived] {
			continue
		}
		existing[derived] = true
		out = append(out, derived)
	}
	return out
}

// isPlainTCPListenAddr reports whether the multiaddr is exactly
// /ip{4,6}/<addr>/tcp/<port> with nothing layered on top. Anything already
// carrying /ws, /tls, /quic-v1 or /webrtc-direct is either already a websocket
// listener or a different transport entirely, and decorating it would produce
// an address no transport can listen on.
func isPlainTCPListenAddr(addr string) bool {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return false
	}
	protos := ma.Protocols()
	if len(protos) != 2 {
		return false
	}
	switch protos[0].Code {
	case multiaddr.P_IP4, multiaddr.P_IP6:
	default:
		return false
	}
	return protos[1].Code == multiaddr.P_TCP
}

// newAutoTLSCertManager builds the p2p-forge certificate manager. Returns
// (nil, nil) when AutoTLS is disabled, which is the default.
func newAutoTLSCertManager(cfg config.AutoTLSConfig, dataPath string) (*p2pforge.P2PForgeCertMgr, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	storagePath := strings.TrimSpace(cfg.StoragePath)
	if storagePath == "" {
		storagePath = filepath.Join(dataPath, "p2p-forge-certs")
	}
	if !filepath.IsAbs(storagePath) {
		return nil, fmt.Errorf("network.autotls.storage_path must be absolute, got %q", storagePath)
	}

	registrationDelay := time.Duration(0)
	if raw := strings.TrimSpace(cfg.RegistrationDelay); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("network.autotls.registration_delay %q: %w", raw, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("network.autotls.registration_delay must not be negative, got %q", raw)
		}
		registrationDelay = parsed
	}

	// certmagic logs through its own package-level defaults when an issuer
	// falls back to them; name them so those lines are attributable instead of
	// arriving as anonymous stderr noise.
	rawLogger := zap.L().Named("autotls")
	certmagic.Default.Logger = rawLogger.Named("certmagic-default")
	certmagic.DefaultACME.Logger = rawLogger.Named("certmagic-acme")

	opts := []p2pforge.P2PForgeCertMgrOptions{
		p2pforge.WithLogger(rawLogger.Sugar()),
		p2pforge.WithForgeDomain(autoTLSDomain(cfg)),
		p2pforge.WithRegistrationDelay(registrationDelay),
		p2pforge.WithUserAgent(versioninfo.AgentVersion),
		p2pforge.WithCertificateStorage(&certmagic.FileStorage{Path: storagePath}),
		p2pforge.WithShortForgeAddrs(autoTLSShortAddrs(cfg)),
	}
	if endpoint := strings.TrimSpace(cfg.RegistrationEndpoint); endpoint != "" {
		opts = append(opts, p2pforge.WithForgeRegistrationEndpoint(endpoint))
	}
	if ca := strings.TrimSpace(cfg.CAEndpoint); ca != "" {
		opts = append(opts, p2pforge.WithCAEndpoint(ca))
	}
	if token := strings.TrimSpace(cfg.RegistrationToken); token != "" {
		opts = append(opts, p2pforge.WithForgeAuth(token))
	}
	if cfg.AllowPrivateAddrs {
		// TEST ONLY: skips the "wait until libp2p reports ReachabilityPublic"
		// gate and lets private multiaddrs be submitted to the broker. On a
		// public host this only removes the safety that keeps failed
		// registrations out of the log.
		opts = append(opts, p2pforge.WithAllowPrivateForgeAddrs())
	}

	certMgr, err := p2pforge.NewP2PForgeCertMgr(opts...)
	if err != nil {
		return nil, fmt.Errorf("configure autotls certificate manager: %w", err)
	}
	return certMgr, nil
}
