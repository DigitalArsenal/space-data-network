// Package sdnapi is the SDN read-only HTTP API + operator-console plugin: the
// third SDN addition to upstream kubo (alongside sdnflag and sdnruntime). It
// exposes a live SDN node's state — identity, peers, the stored (source, type)
// catalog, bounded record listings, channels and installed apps — as small
// JSON under /sdn/v1/*, and (Phase 8) serves the self-contained static SDN
// operator console (kubo/sdn/sdnui) at "/" on the SAME loopback listener, so
// the node serves its own UI with no external assets and no extra port.
//
// # Zero core patch — the node serves it directly, kubo-style
//
// The plugin adds NO kubo core patch and mounts NO corehttp ServeOption: on
// daemon start it stands up its OWN dedicated http.Server serving the pure
// handler in kubo/sdn/sdnapi. That server is a plugin-owned goroutine bound to
// a configured address; it does not touch kubo's API (5001) or gateway (8080)
// mux. This is the same "plugins only" rule that held for Phases 1 and 6.
//
// # Loopback by default
//
// The listener binds 127.0.0.1:5020 by default. Public exposure is an explicit
// operator choice: set Plugins.sdnapi.Config.Addr to a non-loopback host and
// the plugin serves it, but logs a warning that the SDN state surface is now
// reachable off-host. It never binds 0.0.0.0 on its own.
//
// # Reaching the node's SDN state without duplicating it
//
// The handler resolves everything per request through accessors: the live
// services via sdnruntime.Services(), the SDN peer set and membership namespace
// via the sdnflag package accessors, and the swarm peers + identity + pubsub
// gate off the *core.IpfsNode. Because those are resolved lazily, the listener
// may start before the runtime services exist and still report current state.
//
// Enabled by default; set Plugins.sdnapi.Config.Enabled=false to opt out.
package sdnapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"
	ic "github.com/libp2p/go-libp2p/core/crypto"

	pluginsdnflag "github.com/ipfs/kubo/plugin/plugins/sdnflag"
	pluginsdnruntime "github.com/ipfs/kubo/plugin/plugins/sdnruntime"
	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/nodeepm"
	"github.com/ipfs/kubo/sdn/plugins"
	sdnapihttp "github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnodresults"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sdnui"
)

var log = logging.Logger("plugin/sdnapi")

// DefaultAddr is the loopback address the SDN HTTP API binds when the operator
// does not override Plugins.sdnapi.Config.Addr.
const DefaultAddr = "127.0.0.1:5020"

type sdnAPIPlugin struct {
	enabled bool
	addr    string

	srv     *http.Server
	flowMgr *flowrt.FlowManager
}

var _ plugin.PluginDaemonInternal = (*sdnAPIPlugin)(nil)

// Plugins is the exported list of plugins that will be loaded.
var Plugins = []plugin.Plugin{
	&sdnAPIPlugin{},
}

func (*sdnAPIPlugin) Name() string    { return "sdnapi" }
func (*sdnAPIPlugin) Version() string { return "0.1.0" }

// Init reads optional config. Enabled by default. Set
// Plugins.sdnapi.Config.Enabled=false to opt out, or .Addr to override the
// (loopback-by-default) listen address.
func (p *sdnAPIPlugin) Init(env *plugin.Environment) error {
	p.enabled = true
	p.addr = DefaultAddr
	if env != nil {
		if cfg, ok := env.Config.(map[string]interface{}); ok {
			if v, ok := cfg["Enabled"].(bool); ok {
				p.enabled = v
			}
			if v, ok := cfg["Addr"].(string); ok && strings.TrimSpace(v) != "" {
				p.addr = strings.TrimSpace(v)
			}
		}
	}
	return nil
}

func (p *sdnAPIPlugin) Start(node *core.IpfsNode) error {
	if !p.enabled {
		return nil
	}
	if err := logging.SetLogLevel("plugin/sdnapi", "info"); err != nil {
		return fmt.Errorf("failed to set log level: %w", err)
	}

	deps := sdnapihttp.Deps{
		Node: func() sdnapihttp.NodeInfo {
			return sdnapihttp.NodeInfo{
				PeerID:        node.Identity.String(),
				FlagNamespace: pluginsdnflag.Namespace(),
				PubSubEnabled: node.PubSub != nil,
			}
		},
		Store: func() *sdnstore.Store {
			if s := pluginsdnruntime.Services(); s != nil {
				return s.Store
			}
			return nil
		},
		Channels: func() *channels.Channels {
			if s := pluginsdnruntime.Services(); s != nil {
				return s.Channels
			}
			return nil
		},
		IPFSPeers: func() []string {
			if node.PeerHost == nil {
				return nil
			}
			ids := node.PeerHost.Network().Peers()
			out := make([]string, 0, len(ids))
			for _, id := range ids {
				out = append(out, id.String())
			}
			return out
		},
		SDNPeers: func() []string {
			ids := pluginsdnflag.SDNPeers()
			out := make([]string, 0, len(ids))
			for _, id := range ids {
				out = append(out, id.String())
			}
			return out
		},
		// Blockstore backs GET /sdn/v1/module?hash=: the node's own
		// content-addressed store is where modules resolved by an $APP's
		// APPModuleRef.CONTENT_HASH live, so the page harness fetches the exact
		// bytes the node would load and runs them under the same ABI.
		Blockstore: func() appmanifest.ModuleBlockstore {
			if node.Blockstore == nil {
				return nil
			}
			return node.Blockstore
		},
		// Modules backs GET /sdn/v1/modules and the module config endpoints: the
		// live cron scheduler is the module cron/config control surface. Resolved
		// lazily so the listener can start before the runtime services exist.
		// Wrapped in the compat shim (omm_compat.go) so the supplemental-OMM
		// board's hardcoded "supplemental-omm" config-panel id resolves to the
		// mounted OD ServiceFlow (org.sdn.flows.od-supplemental-omm) instead of
		// 404ing — every other module id is unaffected, passed straight through.
		Modules: func() sdnapihttp.ModuleAdmin {
			if s := pluginsdnruntime.Services(); s != nil && s.Scheduler != nil {
				return ommCompatModuleAdmin{real: s.Scheduler}
			}
			return nil
		},
		// Installer backs POST /sdn/v1/admin/modules/install (loopback + fail
		// closed): the real WASM-module install + register pipeline. Resolved
		// lazily; nil until the runtime is up. The adapter maps the pipeline's
		// types + sentinel errors onto the sdnapi surface.
		Installer: func() sdnapihttp.ModuleInstaller {
			if in := pluginsdnruntime.Installer(); in != nil {
				return installerAdapter{in}
			}
			return nil
		},
	}

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("sdnapi: listen on %s: %w", p.addr, err)
	}

	// Credential-entry admin routes (GET/PUT/DELETE /sdn/v1/admin/credentials):
	// the operator surface for the third-party data-source credentials ephemeris
	// modules fetch through the capability-gated "secrets" hostcall. Mounted on
	// this SAME loopback listener but guarded independently: fail closed and
	// loopback-only (requestFromLoopback), so credential management stays
	// same-host even if the operator points Addr at a public interface. A nil
	// keystore (runtime disabled/not started/could not open) makes every route
	// report 503; no route ever returns plaintext (write-only store).
	credsHandler := sdnapihttp.NewCredentialsHandler(sdnapihttp.CredentialsDeps{
		Store: func() sdnapihttp.CredentialStore {
			// Return a true nil interface (not a typed-nil) when unavailable so
			// the handler's nil check fires and reports 503.
			if s := pluginsdnruntime.CredentialStore(); s != nil {
				return s
			}
			return nil
		},
		Authorized: requestFromLoopback,
	})

	// Supplemental-OMM RUN API (GET /sdn/v1/runs...): the read-only board
	// surface over the node's REAL OD-fit results, derived from the mounted
	// OD ServiceFlow's fire history + its linked FlatSQL store
	// (sdn/sdnodresults) — NOT the disconnected, inert sdnruns.Store (see
	// plugin/plugins/sdnruntime/sdnruns.go: that run engine is fully inert
	// per the SDN_OD_FLOW_LOOP.md STOP block, so it never gains new rows;
	// derived runs are the run log going forward). A SEPARATE handler (like
	// credentials), mounted on this same loopback listener. The ODFlow
	// resolved lazily so the routes report an honest empty result before the
	// runtime is up or on a node with no OD flow mounted (the fallback for
	// nodes without the new store).
	odResultsReader := sdnodresults.NewReader(func() sdnodresults.ODFlow {
		fi := pluginsdnruntime.FlowInstaller()
		if fi == nil {
			return nil
		}
		sf := fi.Flow(ommFlowProgramID)
		if sf == nil {
			return nil
		}
		return sf
	})
	runsHandler := sdnapihttp.NewRunsHandler(sdnapihttp.RunsDeps{
		Reader: func() *sdnodresults.Reader { return odResultsReader },
	})

	// Node EPM / vCard / QR export (GET /sdn/v1/node/{epm,vcard,qr}): the node's
	// self-signed $EPM record, built ONCE from its libp2p identity and memoized
	// so the exported record (and its signature timestamp) is stable across
	// requests. A SEPARATE handler mounted on this same loopback listener.
	var (
		epmOnce   sync.Once
		epmCached []byte
		epmErr    error
	)
	nodeEPMHandler := sdnapihttp.NewNodeEPMHandler(sdnapihttp.NodeEPMDeps{
		EPM: func() ([]byte, error) {
			epmOnce.Do(func() { epmCached, epmErr = buildNodeEPM(node) })
			return epmCached, epmErr
		},
	})

	// Flow platform API (POST /api/v1/flows/bake + GET /api/v1/flows/palette and
	// the rest of the flow-management surface): the compose-and-deploy backend the
	// Flow Editor $APP (served at /sdn/v1/apps/flow-editor on THIS same listener)
	// drives. Mounting it here makes the editor page same-origin with the bake
	// endpoint. The baker is attached only when the node has the flowcc toolchain
	// staged; without it the bake route returns its own clean "toolchain not
	// staged" 501, and the palette still lists the host capability nodes.
	flowsHandler, flowsNote := p.buildFlowsHandler(node)

	p.srv = &http.Server{
		Handler:           newRootHandler(sdnapihttp.NewHandler(deps), sdnui.Handler(), credsHandler, runsHandler, nodeEPMHandler, flowsHandler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Infof("SDN flow platform API: %s", flowsNote)

	if isLoopbackAddr(p.addr) {
		log.Infof("SDN console + HTTP API listening on http://%s (loopback; read-only; UI at /, API under /sdn/v1/)", ln.Addr())
	} else {
		log.Warnf("SDN console + HTTP API listening on http://%s — this is NOT loopback: the node's SDN console and state surface are reachable off-host (operator-configured Addr)", ln.Addr())
	}

	go func() {
		if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Errorf("SDN HTTP API server stopped: %v", err)
		}
	}()

	// Shut the listener down cleanly when the node shuts down.
	go func() {
		<-node.Context().Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(ctx)
	}()

	return nil
}

func (p *sdnAPIPlugin) Close() error {
	if p.flowMgr != nil {
		p.flowMgr.CloseAll()
	}
	if p.srv != nil {
		return p.srv.Close()
	}
	return nil
}

// buildFlowsHandler constructs the flow-platform HTTP surface mounted under
// /api/v1/flows/ on the loopback listener. It builds a FlowManager backed by the
// node-data flow store (a sibling of the flowcc toolchain home) and attaches a
// Baker only when that toolchain is staged, so a node with no toolchain still
// serves the palette (host capabilities) and returns the bake path's clean 501
// on Deploy. It never fails the plugin: if the manager cannot be built it logs
// and returns a nil handler (the routes are simply not mounted).
func (p *sdnAPIPlugin) buildFlowsHandler(node *core.IpfsNode) (http.Handler, string) {
	home := flowcc.ResolveHome()
	cfg := flowconfig.FlowsConfig{
		Enabled:        true,
		StoragePath:    filepath.Join(filepath.Dir(home.Root()), "flows"),
		MaxMemoryPages: 2048,
	}
	fm, err := flowrt.NewFlowManager(cfg, plugins.New(), flowrt.HandlerMap{})
	if err != nil {
		return nil, fmt.Sprintf("NOT mounted: flow manager init failed (%v)", err)
	}
	p.flowMgr = fm
	note := "mounted at /api/v1/flows/ — bake path DISABLED (flowcc toolchain not staged; Deploy returns 501)"
	if home.Staged() {
		if baker, berr := flowrt.NewBaker(home, cfg.MaxMemoryPages); berr == nil {
			fm.SetBaker(baker)
			note = "mounted at /api/v1/flows/ — bake path ENABLED (flowcc toolchain staged at " + home.Root() + ")"
			// Phase-4 Task 3: wire the network-module path to the node's REAL
			// blockstore + a trust root + the node's publisher signer, so
			// fetch-to-bake and publish-as-a-module work on a live node (not just
			// the test harness). Best-effort: a node missing a blockstore or a
			// non-Ed25519 identity keeps the bake path but no network publish.
			if netNote := wireFlowNetModules(baker, node); netNote != "" {
				note += "; " + netNote
			}
			// Prewarm the flow-agnostic runtime object in the background so the
			// first editor Deploy hits the ~3s link-only path instead of paying
			// the one-time ~35s runtime compile inline on the first bake.
			go func() {
				if cached, perr := baker.PrewarmRuntime(context.Background()); perr != nil {
					log.Warnf("SDN flow bake prewarm failed: %v", perr)
				} else {
					log.Infof("SDN flow bake prewarm ready (runtime cached=%v)", cached)
				}
			}()
		} else {
			note = fmt.Sprintf("mounted at /api/v1/flows/ — toolchain present but baker init failed (%v); Deploy returns 501", berr)
		}
	}
	mux := http.NewServeMux()
	flowrt.RegisterAPI(mux, fm)
	return mux, note
}

// wireFlowNetModules attaches the node's REAL content-addressed blockstore and a
// live trust root to the flow baker's network-module path (Phase-4 Task 3):
//
//   - The fetcher's blockstore is node.Blockstore — the SAME store an $APP's
//     content-hash module refs resolve from — so a published flow-module is
//     persisted where a fetch-to-bake reads it.
//   - The trust root is a modulert.ModuleSignaturePolicy whose TrustedSigners
//     includes the node's own Ed25519 publisher key, so the node trusts (and can
//     fetch-to-bake) flow-modules it publishes. Additional external publisher keys
//     are added by extending this policy (the same signer-key model the module
//     load gate uses). Signed-only stays fail-closed: an untrusted signer is
//     refused.
//   - The publisher signer signs a bundle's content-hash digest with the node's
//     Ed25519 identity — the SAME Ed25519-over-content-hash primitive modulert's
//     module publication signature uses, so a flow published as a module verifies
//     through the identical gate as any other module.
//
// Returns a short status note (empty when nothing could be wired). Never fails
// the plugin: a node without a blockstore or with a non-Ed25519 identity simply
// keeps bake without network publish.
func wireFlowNetModules(baker *flowrt.Baker, node *core.IpfsNode) string {
	if node == nil || node.Blockstore == nil {
		return "network publish DISABLED (no blockstore)"
	}
	priv := node.PrivateKey
	if priv == nil || priv.GetPublic().Type() != ic.Ed25519 {
		return "network publish DISABLED (node identity is not Ed25519)"
	}
	pubRaw, err := priv.GetPublic().Raw()
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return "network publish DISABLED (could not export node publisher key)"
	}

	// Trust root: the node's own publisher key (self-trust), expressed as the
	// modulert publication-signature policy so external publisher keys can be
	// added the same way the module load gate trusts them.
	policy := &modulert.ModuleSignaturePolicy{
		TrustedSigners: []ed25519.PublicKey{ed25519.PublicKey(append([]byte(nil), pubRaw...))},
	}
	baker.SetNetModules(flowrt.NewNetModuleFetcher(node.Blockstore, policy.TrustedSigners))

	pubHex := hex.EncodeToString(pubRaw)
	baker.SetPublisher(func(digest []byte) (string, string, error) {
		sig, serr := priv.Sign(digest) // Ed25519 over the content-hash digest
		if serr != nil {
			return "", "", serr
		}
		return base64.StdEncoding.EncodeToString(sig), pubHex, nil
	})
	return "network publish ENABLED (blockstore + self-trusted Ed25519 publisher " + pubHex[:12] + "…)"
}

// newRootHandler composes the single loopback listener's routing: the
// read-only JSON API owns the /sdn/v1/ subtree, and the embedded static
// operator console (kubo/sdn/sdnui) owns everything else — "/" serves the app
// shell, "/styles.css" and "/app.js" its assets, and any other path is 404.
// Because the API is mounted on a subtree pattern, ServeMux forwards the full
// request path unchanged, so the API's own GET /sdn/v1/{node,peers,...} routes
// still match. The console and the API it drives are therefore same-origin,
// which is what lets the page fetch /sdn/v1/* with no cross-origin request.
func newRootHandler(api, ui, creds, runs, nodeEPM, flows http.Handler) http.Handler {
	mux := http.NewServeMux()
	// The flow-platform API owns the /api/v1/flows/ subtree (bake + palette +
	// flow management). It is more specific than the "/" console catch-all, so
	// ServeMux routes it to the flow handler; the editor $APP served under
	// /sdn/v1/apps/ reaches it same-origin. Absent (nil) when the manager could
	// not be built — the routes are simply unmounted.
	if flows != nil {
		mux.Handle("/api/v1/flows", flows)
		mux.Handle("/api/v1/flows/", flows)
	}
	// The credential admin + runs routes claim their exact prefixes; because they
	// are more specific than the "/sdn/v1/" subtree, ServeMux routes those requests
	// to their dedicated handlers and everything else under /sdn/v1/ to the
	// read-only API. The full request path is forwarded unchanged, so each
	// handler's own method+path routes still match.
	mux.Handle("/sdn/v1/admin/credentials", creds)
	mux.Handle("/sdn/v1/admin/credentials/", creds)
	mux.Handle("/sdn/v1/runs", runs)
	mux.Handle("/sdn/v1/runs/", runs)
	// The node EPM export routes are registered as EXACT patterns (not a
	// /sdn/v1/node/ subtree) so they never shadow or redirect the read-only API's
	// exact GET /sdn/v1/node route: /sdn/v1/node stays on the API, while
	// /sdn/v1/node/{epm,vcard,qr} route to the export handler.
	mux.Handle("/sdn/v1/node/epm", nodeEPM)
	mux.Handle("/sdn/v1/node/vcard", nodeEPM)
	mux.Handle("/sdn/v1/node/qr", nodeEPM)
	mux.Handle("/sdn/v1/", api)
	mux.Handle("/", ui)
	return mux
}

// buildNodeEPM builds the node's self-signed $EPM record from its libp2p
// identity: the peer ID, the Ed25519 public key as the signing key, the node's
// listen multiaddrs, and a self-signature produced by the node private key. The
// node identity must be Ed25519 (the kubo default); any other key type is
// refused rather than silently producing an unverifiable record.
func buildNodeEPM(node *core.IpfsNode) ([]byte, error) {
	priv := node.PrivateKey
	if priv == nil {
		return nil, fmt.Errorf("node has no private key")
	}
	pub := priv.GetPublic()
	if pub.Type() != ic.Ed25519 {
		return nil, fmt.Errorf("node identity is not Ed25519 (type %v); EPM signing key unavailable", pub.Type())
	}
	pubRaw, err := pub.Raw()
	if err != nil {
		return nil, fmt.Errorf("export node public key: %w", err)
	}

	peerID := node.Identity.String()
	var multiaddrs []string
	if node.PeerHost != nil {
		for _, a := range node.PeerHost.Addrs() {
			multiaddrs = append(multiaddrs, a.String()+"/p2p/"+peerID)
		}
	}

	return nodeepm.BuildNodeEPM(nodeepm.Identity{
		PeerID:     peerID,
		SigningPub: pubRaw,
		Multiaddrs: multiaddrs,
		Sign:       func(payload []byte) ([]byte, error) { return priv.Sign(payload) },
	})
}

// requestFromLoopback reports whether an HTTP request originates from the local
// host. It is the authorization gate for the credential-entry admin routes: even
// if the operator (mis)configures the listener onto a public Addr, credential
// management stays restricted to same-host clients. Fail closed — an
// unparseable or non-loopback RemoteAddr is refused.
func requestFromLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

// installerAdapter maps the sdnmodules.Installer pipeline onto the read-only
// API package's ModuleInstaller interface, translating grant/view types and the
// pipeline's sentinel errors (ErrInstallDenied/ErrModuleNotFound) onto the
// sdnapi sentinels the route maps to 403/404. This keeps the pure sdnapi
// package free of a dependency on the pipeline package.
type installerAdapter struct{ in *sdnmodules.Installer }

func (a installerAdapter) AdminInstall(ctx context.Context, contentHash string, grants []sdnapihttp.CapabilityGrant) (sdnapihttp.InstalledModuleView, error) {
	mg := make([]sdnmodules.CapabilityGrant, 0, len(grants))
	for _, g := range grants {
		mg = append(mg, sdnmodules.CapabilityGrant{Capability: g.Capability, ApprovedBy: g.ApprovedBy, Note: g.Note})
	}
	m, err := a.in.AdminInstall(ctx, contentHash, mg)
	if err != nil {
		switch {
		case errors.Is(err, sdnmodules.ErrInstallDenied):
			return sdnapihttp.InstalledModuleView{}, fmt.Errorf("%w: %v", sdnapihttp.ErrInstallDenied, err)
		case errors.Is(err, sdnmodules.ErrModuleNotFound):
			return sdnapihttp.InstalledModuleView{}, fmt.Errorf("%w: %v", sdnapihttp.ErrModuleNotFound, err)
		default:
			return sdnapihttp.InstalledModuleView{}, err
		}
	}
	return sdnapihttp.InstalledModuleView{
		ID:          m.ID,
		ContentHash: m.ContentHash,
		Name:        m.Name,
		Version:     m.Version,
		Enabled:     m.Enabled,
		Source:      m.Source,
		Timers:      m.Timers,
	}, nil
}

// isLoopbackAddr reports whether host:port binds a loopback interface. A host
// that does not parse as an IP (e.g. "localhost") is treated as loopback; an
// empty host or "0.0.0.0"/"::" is not.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A non-IP, non-localhost host is resolved by the OS; be conservative.
		return false
	}
	return ip.IsLoopback()
}
