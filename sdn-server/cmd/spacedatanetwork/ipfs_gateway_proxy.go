package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// onlyIfCached is the IPFS gateway request directive (Cache-Control) that makes
// Kubo answer only from content it already holds and refuse — 412 Precondition
// Failed, immediately — anything it would have to fetch from the network.
const onlyIfCached = "only-if-cached"

// newIPFSGatewayProxy builds the same-origin /ipfs/* proxy in front of the
// node's Kubo HTTP gateway. The node exposes the gateway so its own dashboard
// can read the shards, manifests and archives this node holds; it must not be
// a public fetch gateway that pulls arbitrary CIDs from the network on behalf
// of anonymous callers (SEC-04). Every proxied request therefore carries
// only-if-cached, and a 412 from Kubo is reported as 404: this node does not
// hold that content.
//
// The gateway owns the CORS headers on /ipfs/. /ipfs/ is on the public access
// list, so the admin middleware has already written its own
// Access-Control-Allow-Origin before this handler runs; the reverse proxy then
// adds the gateway's, and a response carrying two values is refused by every
// browser (SpaceAware terrain tiles and water mask went dark that way). The
// earlier header set is dropped so exactly one survives.
func newIPFSGatewayProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Header.Del("Origin")
		req.Header.Del("Referer")
		req.Header.Del("User-Agent")
		req.Header.Set("Cache-Control", onlyIfCached)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		normalizeIPFSGatewayCORSHeaders(resp.Header)
		if resp.StatusCode == http.StatusPreconditionFailed {
			resp.StatusCode = http.StatusNotFound
			resp.Status = "404 Not Found"
			resp.Header.Set("X-SDN-IPFS", "not-held")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "upstream IPFS gateway unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range ipfsGatewayCORSHeaders {
			w.Header().Del(name)
		}
		proxy.ServeHTTP(w, r)
	})
}

var ipfsGatewayCORSHeaders = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers",
	"Access-Control-Allow-Credentials",
	"Access-Control-Max-Age",
}
