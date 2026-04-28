# sdn.spaceaware.io Deployment

This directory contains the production target config for the first SDN
module-delivery and OrbPro licensing provider node.

The deployment config uses `host: sdn.spaceaware.io` on purpose. Keep the real
address and key material in `~/.ssh/config`; do not commit machine-specific SSH
paths or IP addresses here.

From the `space-data-network` repository root:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  -b \
  deploy full
```

The server must have these production secrets configured for OrbPro module
publishing:

```bash
SDN_LICENSE_ADMIN_TOKEN=<shared admin token>
SDN_MODULE_PUBLISH_TOKEN=<CI publish token>
SDN_PLUGIN_ROOT=/var/lib/spacedatanetwork/data/license/plugins
```

`SDN_MODULE_PUBLISH_TOKEN` is the token the OrbPro GitHub Pages workflow uses to
sign libp2p module-publish requests. If it is not set, the provider falls back
to `SDN_LICENSE_ADMIN_TOKEN`.
