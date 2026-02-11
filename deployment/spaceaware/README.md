# SpaceAware Single-Host Launch Stack

This folder is a launch-oriented deployment scaffold for a 24-hour rollout on one DigitalOcean host.

## Included services

- `sdn-node`: full Space Data Network node + public data API + license stream protocol.
- `sdn-ingest`: continuous CelesTrak/Space-Track ingestion worker.
- `orbpro-license`: OrbPro license service container (placeholder image).

## Quick start

1. Copy `.env.example` to `.env` and fill values.
2. Update `config/sdn/config.yaml` bootstrap/listen fields and trusted peers.
3. Ensure host paths exist:
   - `/opt/data/sdn`
   - `/opt/data/raw`
   - `/opt/data/keys`
   - `/opt/data/license`
4. Launch:

```bash
docker compose --env-file .env -f deployment/spaceaware/docker-compose.yml up -d
```

## Required environment variables

- `STRIPE_SECRET_KEY`
- `STRIPE_WEBHOOK_SECRET`
- `SDN_LICENSE_ADMIN_TOKEN` (used by `POST/PUT /api/v1/license/entitlements`)
- `SDN_PLUGIN_ROOT` (encrypted plugin catalog root; usually `/opt/data/license/plugins`)
- `PUBLIC_BOOTSTRAP_ADDR` (full multiaddr including `/p2p/<PEER_ID>`)

## Notes

- Public/free APIs are cacheable via Cloudflare.
- Token-protected APIs should bypass cache.
- The libp2p license protocol is `/orbpro/license/1.0.0`.
- Plugin endpoints:
  - `GET /api/v1/plugins/manifest`
  - `GET /api/v1/plugins/{id}/bundle` (cacheable encrypted payload)
  - `POST /api/v1/plugins/{id}/key-envelope` (auth + scope required)
