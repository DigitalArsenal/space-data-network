# Public records through a CDN or Tor

This directory renders a dedicated Nginx origin for anonymous record reads.
It forwards selected bulk routes to the node's installed data-retrieval WASM
flow and keeps frequently requested response bytes in a shared hot cache.
FlatSQL remains the record store and query engine; the proxy neither decodes
FlatBuffers nor changes their framing, content, provenance or signatures.

The default is OMM on `127.0.0.1:8088`, forwarded to the local node at
`http://127.0.0.1:7173`. Select additional standards explicitly from the node's
existing public-schema policy. Unknown and private schemas are refused by the
renderer. The node must still independently authorize anonymous reads.

## Start locally

With Nginx installed and the node's HTTP listener running, from the SDN root:

```sh
node deployment/public-origin/render-nginx.mjs \
  --schemas omm,cat,rfb \
  --upstream http://127.0.0.1:7173 \
  --listen 127.0.0.1:8088 \
  --cache-mib 128 --ttl-seconds 15 > /tmp/sdn-public-origin.conf
nginx -t -c /tmp/sdn-public-origin.conf
nginx -c /tmp/sdn-public-origin.conf
curl --fail http://127.0.0.1:8088/api/v1/ready
curl --fail -D /tmp/records.headers \
  'http://127.0.0.1:8088/api/v1/data/omm/bulk?profile=nearest&limit=10' \
  -o /tmp/records.bin
```

Repeat the record request: `X-SDN-Cache: HIT` indicates the origin query was
avoided. Inspect `Age`, `ETag` and `Cache-Control` alongside this diagnostic.
Stop this standalone instance with
`nginx -c /tmp/sdn-public-origin.conf -s quit`.
Use a separate process namespace or adjust the generated PID/temp paths when
another standalone Nginx uses `/tmp/nginx.pid`.

The renderer prints configuration only. It does not install a service, reload
an existing edge or restart the SDN node. An HTTPS-only local node needs a
verified local TLS termination/proxy arrangement before using this HTTP hop;
the renderer provides no certificate-verification bypass.

## CDN origin

Forward the selected record routes and `/api/v1/ready` from the existing HTTPS
edge to `http://127.0.0.1:8088`. Keep the public origin's listener private; an
external CDN connects to the verified HTTPS edge. For container use,
`--listen 0.0.0.0:8088` is explicit and should be paired with a loopback-only
published port. The upstream must be loopback inside the same network
namespace as Nginx.

Configure the CDN to respect origin cache headers and include the **entire
query string and Accept header** in its cache key. A different epoch, limit,
profile or format must not share a response. Do not enable a global cache-all
rule, strip query parameters, override private/no-store responses or cache
operator endpoints. Credentials, cookies, writes, byte ranges and request
bodies are rejected by this origin. CORS supports credential-free reads and
conditional-request preflight. Readiness and errors always carry `no-store`.

The origin cache uses the full request URI and Accept value. It caches only
successful FlatBuffer stream responses with an ETag, coalesces concurrent
fills, revalidates expired entries and honors upstream `Cache-Control`,
`Vary`, `Set-Cookie` and `X-Accel-Expires`. A caller's Cache-Control or Pragma
request bypasses both cache reads and insertion. Cached replies preserve the
origin Date so a downstream CDN accounts for time already spent in this
cache. Non-stream formats can pass
through, but are not cached by this configuration.

When a stream response has no cache policy, the fallback is 15 seconds in
shared caches and immediate revalidation in browsers. `--ttl-seconds` changes
this fallback (1–60 seconds), not a policy explicitly supplied by the serving
module. `max_size` is an eviction target, not a filesystem quota: put the
cache on a dedicated quota-limited volume or bounded tmpfs to impose a hard
storage ceiling. The smoke test uses a 4 MiB eviction target inside a 32 MiB
tmpfs and a 96 MiB container memory limit. Cache loss only causes refetching;
it must never delete or invalidate FlatSQL storage.

No stale-on-error policy is enabled. Expired data requires a successful
origin revalidation or fetch; origin failure is reported to the client.
An ETag proves response identity for HTTP caching, not publisher authenticity.
Consumers must still verify SDS publication signatures and content hashes.
Immutable CID-addressed module artifacts and mutable signed update feeds have
different cache policies and use the existing release/gateway routes; this
record origin does not expose either route family.

## Onion service

Point the node's onion service at this public origin, explicitly overriding
the current default target (the admin listener):

```yaml
tor:
  enabled: true
  hidden_service_enabled: true
  hidden_service_port: 80
  hidden_service_target: 127.0.0.1:8088
  socks_address: 127.0.0.1:9050
  bypass_local_addresses: true
```

This is an incoming public record service. It does not assert that every
libp2p/Kubo connection is Tor-only. Outbound HTTP callers requiring that policy
must select the Go `OutboundTorOnly` transport; `tor-preferred` permits direct
fallback. Keep the local proxy reachable for the node's local dependencies.

The current runtime startup confirms the SOCKS listener and onion hostname,
not completed Tor bootstrap or onion reachability. Wait for bootstrap, then
verify from a Tor client using the public hostname reported by the node:

```sh
curl --fail --max-time 120 --socks5-hostname 127.0.0.1:9050 \
  'http://YOUR-NODE.onion/api/v1/data/omm/bulk?profile=nearest&limit=10' \
  -o /tmp/onion-records.bin
```

For byte comparison, use the same explicit epoch, profile and limit for
direct, CDN and onion requests; a moving "latest" query can legitimately
change between requests. Keep onion key files private and outside cache
volumes. The service reuses the same origin cache for direct and onion reads.

## Verification

```sh
node --test deployment/public-origin/render-nginx.test.mjs
docker pull nginx@sha256:dc5069ad14f19660b141b21236140b91656bf89bbc3e2417c70ae650cd66104c
SDN_REQUIRE_DOCKER_TESTS=1 node --test deployment/public-origin/nginx-smoke.test.mjs
```

The smoke suite starts and removes its own loopback-published container. It
tests byte preservation, one origin fill for twelve concurrent readers, query
and format separation, HEAD, weak/list ETags, CORS, cache restrictions,
request-header isolation, revalidation, changed bytes, expired-origin failure
and negative requests. Its opaque transport fixtures are not numerical or
FlatBuffer conformance vectors. Run the installed SDK module's compliance and
same-artifact browser/WasmEdge tests separately, then repeat the direct/CDN/Tor
comparison against the actual node. These functional checks are not a latency
or throughput benchmark.

Protocol references: [Nginx proxy cache](https://nginx.org/en/docs/http/ngx_http_proxy_module.html),
[HTTP caching](https://www.rfc-editor.org/rfc/rfc9111.html),
[Tor onion service setup](https://community.torproject.org/onion-services/setup/).
