# Hydra Marketplace Docker Demo

This demo exercises the Space Data Network marketplace shape required for Hydra-style tactical data sharing: two providers, Customer A, Customer B, and one unauthorized observer run as separate SDN nodes. The scenario uses SDN/libp2p/IPFS discovery and the normal SDN node image, with no Tailscale, tailnet, or side-channel discovery path.

## Commands

Dry-run the scenario contract without Docker:

```sh
npm run test:hydra-marketplace-demo
```

Run the Docker-backed demo:

```sh
npm run demo:hydra-marketplace
```

The full Docker command starts `deployment/hydra-marketplace-demo.compose.yaml`, waits at least five minutes for SDN/libp2p/IPFS discovery registration, verifies the field-level marketplace policy, writes `deployment/generated/hydra-marketplace-demo-report.json`, and then shuts the topology down. Use `-- --keep-running` to leave the containers up after verification.

## Topology

The compose file starts these roles:

| Service | Hydra role | Purpose |
| --- | --- | --- |
| `hydra-provider-maneuver` | `provider:maneuver-ephemeris` | Publishes protected maneuver ephemeris data. |
| `hydra-provider-catalog` | `provider:catalog-support` | Publishes support catalog data. |
| `hydra-customer-alpha` | `customer:alpha` | Receives Customer A grants. |
| `hydra-customer-beta` | `customer:beta` | Receives Customer B grants. |
| `hydra-observer` | `observer:unauthorized` | Can discover public metadata but cannot decrypt protected data. |

## Field Policy

The primary stream is `maneuver-ephemeris-live` using the `MPE` standard. Public fields remain visible for discovery and routing. Protected fields are encrypted per stream and per customer so maneuver ephemeris can be screened without broadcasting maneuver details to competitors.

| Field | Customer A | Customer B | unauthorized observer |
| --- | --- | --- | --- |
| `object_id` | public | public | public |
| `timestamp` | public | public | public |
| `position` | decrypted | decrypted | encrypted |
| `covariance_detail` | encrypted | decrypted | encrypted |
| `maneuver_plan` | encrypted | encrypted | encrypted |

The protected module is `hydra-maneuver-screening`. It is treated as an encrypted WASM module and only runs when the customer view contains the required `object_id`, `timestamp`, and `position` fields. The unauthorized observer is refused because it cannot decrypt `position`.

## Hydra Mapping

This demo maps SDN marketplace behavior to Hydra evaluation areas:

| Hydra need | SDN demo behavior |
| --- | --- |
| federated data mesh | Providers keep source data resident while publishing discoverable marketplace metadata and field-scoped grants. |
| real-time tactical sharing | Live stream roles model protected maneuver ephemeris and support catalog data from different providers. |
| zero-trust access | Each customer receives only the fields granted by policy; missing grants fail closed. |
| multi-node resilience | Providers, customers, and observer run as separate SDN nodes over SDN/libp2p/IPFS. |
| observability | The runner emits a JSON report with the access matrix, module gate result, revocation/rotation result, and Docker state. |

## Verification

The runner verifies:

- Customer A decrypts only its authorized protected field set.
- Customer B decrypts a different authorized field set.
- The unauthorized observer cannot decrypt protected fields.
- The protected module runs for authorized customer views.
- The protected module refuses the unauthorized observer.
- A revoked customer with a stale key epoch is rejected after rotation.
- The topology contains two providers, two customers, and one observer.
- Discovery remains SDN/libp2p/IPFS based.

The dry-run command is intended for fast source and CI checks. The full Docker command is the integration path that exercises container startup, the five-minute discovery gate, verification, report generation, and clean shutdown.
