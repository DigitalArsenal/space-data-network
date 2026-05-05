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

Before deploying from a development workstation, make sure the local desktop
tool is running so the bundled desktop/Kubo integration is healthy in the same
checkout:

```bash
test -d desktop/node_modules || npm run install:desktop
npm --prefix desktop start
```

Keep that process open while you smoke-test the deployed node. In a second
terminal, verify the production service after deploy:

```bash
./deployment/scripts/deploy.sh \
  -c deployment/spaceaware/servers.yaml \
  -u root \
  -b \
  status
curl -fsS https://sdn.spaceaware.io/ >/dev/null
curl -fsS https://sdn.spaceaware.io/webui/ >/dev/null
```

The provider authorizes OrbPro module publishing with SDN wallet admin state,
not a shared publish token. The deployment wallet that signs `plugins
publish-orbpro` must be present as an admin in the live auth store
(`/opt/data/sdn/auth.db`) or configured as an admin user before the provider is
started.

The server must have the encrypted plugin catalog root configured:

```bash
SDN_PLUGIN_ROOT=/var/lib/spacedatanetwork/data/license/plugins
```

The OrbPro GitHub Pages workflow signs libp2p module-publish requests with an
HD-wallet signing key. The provider verifies the Ed25519 signature against the
signer xpub and accepts the update only when that xpub is an admin.
