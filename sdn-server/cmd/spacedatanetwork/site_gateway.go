package main

// THE NODE SITE GATEWAY (owner 2026-10-05, "Both").
//
// One proxied wildcard record, *.spacedatanetwork.org, points at a gateway
// node, host-01. A node with its own public HTTPS also gets a direct record;
// that is the cloudflare-registrar module's work. Two halves, one protocol:
//
//   - Every node serves its public surface over libp2p HTTP (siteProtocol).
//     It is the same handler chain as its HTTP listener, but it answers reads
//     of public routes only, as to an anonymous remote caller, and "/" is
//     always the homepage, never the dashboard.
//   - A node that receives a request under ANOTHER node's name answers "/"
//     and /home/ with its own embedded homepage app, and forwards the app's
//     data reads (the document, the EPM card, the public API) over that
//     protocol to the node the name encodes. The target must be a detected
//     SDN node. Only reads are forwarded, without the caller's cookies or
//     credentials, and every forwarded response is inert: sandboxed by its
//     policy, no sniffing, no cookies. So whatever a node returns, the code
//     that runs under spacedatanetwork.org is ours. Each node enforces its
//     own access gate.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	ma "github.com/multiformats/go-multiaddr"
)

// siteProtocol carries a node's public surface over libp2p HTTP.
const siteProtocol = protocol.ID("/sdn/site/1.0.0")

// siteForwardTimeout bounds one forwarded request: dial, stream and response.
const siteForwardTimeout = 30 * time.Second

// sitePeer is the node a request's name encodes.
func sitePeer(r *http.Request) (peer.ID, bool) {
	label, ok := siteLabel(r)
	if !ok {
		return "", false
	}
	c, err := cid.Decode(label)
	if err != nil {
		return "", false
	}
	id, err := peer.FromCid(c)
	if err != nil {
		return "", false
	}
	return id, true
}

func isRead(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// serveSiteOverLibp2p mounts the node's public surface on its libp2p host and
// returns the libp2p HTTP host, which the gateway half also dials with.
// public is the node's anonymous-route policy; selfHost is the node's own
// name, so "/" is always its homepage.
func serveSiteOverLibp2p(h host.Host, next http.Handler, public func(method, path string) bool, selfHost string) *libp2phttp.Host {
	site := &libp2phttp.Host{StreamHost: h}
	site.SetHTTPHandlerAtPath(siteProtocol, "/sdn/site/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isRead(r) {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		if p != "/" && p != "/index.html" && !isHomepagePath(p) && !public(r.Method, p) {
			http.NotFound(w, r)
			return
		}
		out := r.Clone(r.Context())
		out.Host = selfHost
		out.Header.Del("Cookie")
		out.Header.Del("Authorization")
		// A proxy hop is in evidence: no loopback trust applies.
		out.Header.Set("Forwarded", `for="_libp2p"`)
		next.ServeHTTP(w, out)
	}))
	go func() {
		if err := site.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warnf("node site over libp2p stopped: %v", err)
		}
	}()
	return site
}

// siteGateway forwards a request under another detected node's name to that
// node.
type siteGateway struct {
	self   peer.ID
	site   *libp2phttp.Host
	flags  func() map[string][]string // nodes found under the SDN advertisement
	addrs  func() map[string][]string // their advertised addresses
	agents func(peer.ID) string       // a connected peer's identify agent
}

// detected reports a node the gateway serves: found under the SDN
// advertisement, or connected and identifying as an SDN node.
func (g *siteGateway) detected(id peer.ID) bool {
	if _, ok := g.flags()[id.String()]; ok {
		return true
	}
	return strings.Contains(g.agents(id), "spacedatanetwork")
}

// wrap routes requests under a name in the zone: this node's own name goes to
// next, another node's to the gateway, and a name that is not a node's (the
// wildcard catches every name) gets 404. Requests under any other host go to
// next.
func (g *siteGateway) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g == nil || !isNodeSiteRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		switch id, ok := sitePeer(r); {
		case !ok:
			http.Error(w, "No Space Data Network node by this name.", http.StatusNotFound)
		case id == g.self:
			next.ServeHTTP(w, r)
		default:
			g.forward(w, r, id)
		}
	})
}

func (g *siteGateway) forward(w http.ResponseWriter, r *http.Request, id peer.ID) {
	if !isRead(r) || r.Header.Get("Upgrade") != "" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !g.detected(id) {
		http.Error(w, "No Space Data Network node by this name.", http.StatusNotFound)
		return
	}
	if p := r.URL.Path; p == "/" || p == "/index.html" || isHomepagePath(p) {
		serveHomepage(w, r)
		return
	}
	info := peer.AddrInfo{ID: id}
	for _, a := range g.addrs()[id.String()] {
		if m, err := ma.NewMultiaddr(a); err == nil {
			info.Addrs = append(info.Addrs, m)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), siteForwardTimeout)
	defer cancel()
	unreachable := func(w http.ResponseWriter, err error) {
		log.Debugf("node site %s unreachable: %v", id, err)
		http.Error(w, "This Space Data Network node is not reachable right now.", http.StatusBadGateway)
	}
	rt, err := g.site.NewConstrainedRoundTripper(info)
	if err == nil {
		rt, err = g.site.NamespaceRoundTripper(rt, siteProtocol, id)
	}
	if err != nil {
		unreachable(w, err)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = id.String()
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.SetXForwarded()
		},
		Transport: rt,
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			resp.Header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) { unreachable(w, err) },
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
