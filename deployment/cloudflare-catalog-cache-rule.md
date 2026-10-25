# Cloudflare Cache Rule: the current catalog

Owner-run (Cloudflare and DNS are the owner's). One rule on the `spacedatanetwork.org` zone makes the node's
`GET /api/v1/data/catalog/current` cacheable at the edge. Cloudflare does not cache a path with no file extension
unless a Cache Rule makes it eligible.

## Rule

Rules > Cache Rules > Create rule

- Name: `catalog current`
- When incoming requests match, Custom filter expression:

  ```
  (http.host wildcard "*.spacedatanetwork.org" and http.request.uri.path eq "/api/v1/data/catalog/current")
  ```

- Cache eligibility: Eligible for cache
- Edge TTL: Use cache-control header if present, bypass cache if not
- Browser TTL: Respect origin TTL
- Cache key: default (the query string stays in the key, so `?format=json` and `?theory=SDNAP` are separate entries)
- Respect strong ETags: on
- Serve stale content while revalidating: on

The node's answer carries `ETag` (strong), `Last-Modified`, `Cache-Control: public, max-age=60, s-maxage=<seconds to
the next publication>, stale-while-revalidate=3600` and no `Set-Cookie`. After the rule exists, the second request
for the same URL returns `cf-cache-status: HIT`; a request with `If-None-Match` returns 304.

## Reach

The catalog node is host-02 (`celestrak.eth`, peer `16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U`), at
`https://kzwfwjn5ji4purkaknrwvpronfvibl14m4uj4zvolmjz02gajewxm0fo7nsyagx.spacedatanetwork.org`. The proxied wildcard
`*` reaches host-01, whose site gateway forwards the request to that peer over libp2p.

## Check

```
curl -sI 'https://<label>.spacedatanetwork.org/api/v1/data/catalog/current' | grep -i -E 'cf-cache-status|etag|cache-control'
```
