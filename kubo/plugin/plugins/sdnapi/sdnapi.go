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
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"

	pluginsdnflag "github.com/ipfs/kubo/plugin/plugins/sdnflag"
	pluginsdnruntime "github.com/ipfs/kubo/plugin/plugins/sdnruntime"
	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/channels"
	sdnapihttp "github.com/ipfs/kubo/sdn/sdnapi"
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

	srv *http.Server
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
		Modules: func() sdnapihttp.ModuleAdmin {
			if s := pluginsdnruntime.Services(); s != nil && s.Scheduler != nil {
				return s.Scheduler
			}
			return nil
		},
	}

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("sdnapi: listen on %s: %w", p.addr, err)
	}

	p.srv = &http.Server{
		Handler:           newRootHandler(sdnapihttp.NewHandler(deps), sdnui.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}

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
	if p.srv != nil {
		return p.srv.Close()
	}
	return nil
}

// newRootHandler composes the single loopback listener's routing: the
// read-only JSON API owns the /sdn/v1/ subtree, and the embedded static
// operator console (kubo/sdn/sdnui) owns everything else — "/" serves the app
// shell, "/styles.css" and "/app.js" its assets, and any other path is 404.
// Because the API is mounted on a subtree pattern, ServeMux forwards the full
// request path unchanged, so the API's own GET /sdn/v1/{node,peers,...} routes
// still match. The console and the API it drives are therefore same-origin,
// which is what lets the page fetch /sdn/v1/* with no cross-origin request.
func newRootHandler(api, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/sdn/v1/", api)
	mux.Handle("/", ui)
	return mux
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
