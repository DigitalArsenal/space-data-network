# Source Map

This package is a design handoff. Production implementation lives in the SDN
repo.

## Prototype To Production

| Prototype | Production |
| --- | --- |
| `prototype/index.html` | `sdn-js/ui/src/App.svelte` |
| App shell and nav | `sdn-js/ui/src/components/AppShell.svelte`, `sdn-js/ui/src/components/SideNav.svelte`, `sdn-js/ui/src/components/TopStatusBar.svelte` |
| Route normalization | `sdn-js/ui/src/lib/routes.ts` |
| Global style and tokens | `sdn-js/ui/src/styles/app.css`, `sdn-js/ui/src/styles/tokens.css` |
| Node screen | `sdn-js/ui/src/screens/NodeScreen.svelte` |
| Peers screen | `sdn-js/ui/src/screens/PeersScreen.svelte` |
| Data screen | `sdn-js/ui/src/screens/LocalDataScreen.svelte` |
| Channels screen | `sdn-js/ui/src/screens/ChannelsScreen.svelte` |
| Conjunction screen | `sdn-js/ui/src/screens/ConjunctionScreen.svelte` |
| Desktop route serving | `desktop/src/static-http-server.js`, `desktop/src/dashboard/index.js` |
| Packaged Desktop asset target | `desktop/assets/sdn-ui` |

## Boundary

The `webui/` directory is the upstream IPFS WebUI mirror. Do not treat it as
the main SDN redesign surface unless the redesign explicitly includes upstream
mirror overrides.
