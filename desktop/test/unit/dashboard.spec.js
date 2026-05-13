const fs = require('fs')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')

test.describe('SDN dashboard window', () => {
  test('loads the shared intro page before the dashboard app', () => {
    const source = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')

    expect(source).toContain('sdn-intro.html')
    expect(source).toContain('loadIntroPage')
  })

  test('uses IPFS-style square, regular-weight intro buttons with the canonical SDN domain', () => {
    const intro = fs.readFileSync(path.join(__dirname, '../../assets/pages/sdn-intro.html'), 'utf8')
    const linkRule = intro.match(/\n {4}a \{[\s\S]*?\n {4}\}/)?.[0] || ''

    expect(intro).toContain('https://spacedatanetwork.org')
    expect(intro).toContain('>spacedatanetwork.org<')
    expect(linkRule).toContain('border-radius: 2px;')
    expect(linkRule).toContain('font-weight: 500;')
    expect(intro).not.toContain('spacedatanet.org')
    expect(linkRule).not.toContain('border-radius: 999px;')
    expect(linkRule).not.toContain('font-weight: 800;')
  })

  test('uses Space Data Network labels for desktop shell surfaces', () => {
    const packageJson = JSON.parse(fs.readFileSync(path.join(__dirname, '../../package.json'), 'utf8'))
    const indexSource = fs.readFileSync(path.join(__dirname, '../../src/index.js'), 'utf8')
    const webuiSource = fs.readFileSync(path.join(__dirname, '../../src/webui/index.js'), 'utf8')
    const traySource = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')
    const autoLaunchSource = fs.readFileSync(path.join(__dirname, '../../src/auto-launch.js'), 'utf8')

    expect(packageJson.name).toBe('space-data-network-desktop')
    expect(packageJson.productName).toBe('Space Data Network')
    expect(indexSource).toContain("app.setAppUserModelId('org.spacedatanetwork.desktop')")
    expect(webuiSource).toContain("title: 'Space Data Network'")
    expect(traySource).toContain("tray.setToolTip('Space Data Network')")
    expect(autoLaunchSource).toContain('Name=Space Data Network')
    expect(autoLaunchSource).toContain('Comment=Space Data Network Startup Script')
  })

  test('does not run auto-update checks in unsigned local app builds', () => {
    const autoUpdaterSource = fs.readFileSync(path.join(__dirname, '../../src/auto-updater/index.js'), 'utf8')

    expect(autoUpdaterSource).toContain('function hasPackagedUpdateConfig')
    expect(autoUpdaterSource).toContain("path.join(process.resourcesPath, 'app-update.yml')")
    expect(autoUpdaterSource).toContain('!hasPackagedUpdateConfig()')
  })

  test('keeps electron auto-update checks disabled until the SDN update server exists', () => {
    const autoUpdaterSource = fs.readFileSync(path.join(__dirname, '../../src/auto-updater/index.js'), 'utf8')
    const runtimeFeedsSource = fs.readFileSync(path.join(__dirname, '../../src/sdn-updater/runtime-feeds.js'), 'utf8')

    expect(runtimeFeedsSource).toContain('const SDN_DESKTOP_AUTO_UPDATES_ENABLED = false')
    expect(autoUpdaterSource).toContain('SDN_DESKTOP_AUTO_UPDATES_ENABLED')
    expect(autoUpdaterSource).toContain('SDN desktop auto updates disabled until the SDN patch/update server is available')
    expect(autoUpdaterSource.indexOf('!SDN_DESKTOP_AUTO_UPDATES_ENABLED')).toBeLessThan(autoUpdaterSource.indexOf('!hasPackagedUpdateConfig()'))
  })

  test('does not block the desktop UI with dialogs for background updater errors', () => {
    const autoUpdaterSource = fs.readFileSync(path.join(__dirname, '../../src/auto-updater/index.js'), 'utf8')
    const errorHandler = autoUpdaterSource.match(/autoUpdater\.on\('error'[\s\S]*?\n  \}\)/)?.[0] || ''

    expect(errorHandler).toContain('if (!feedback)')
    expect(errorHandler.indexOf('if (!feedback)')).toBeLessThan(errorHandler.indexOf('showDialog({'))
    expect(errorHandler).toContain('feedback = false')
    expect(errorHandler).toContain('updater errors must not block the main process')
  })

  test('points desktop application update fallbacks at SDN releases', () => {
    const autoUpdaterSource = fs.readFileSync(path.join(__dirname, '../../src/auto-updater/index.js'), 'utf8')
    const runtimeFeedsSource = fs.readFileSync(path.join(__dirname, '../../src/sdn-updater/runtime-feeds.js'), 'utf8')
    const electronBuilderSource = fs.readFileSync(path.join(__dirname, '../../electron-builder.yml'), 'utf8')

    expect(autoUpdaterSource).toContain('SDN_DESKTOP_RELEASES_URL')
    expect(autoUpdaterSource).toContain('sdnDesktopReleaseVersionUrl')
    expect(runtimeFeedsSource).toContain("const SDN_DESKTOP_RELEASES_URL = 'https://github.com/DigitalArsenal/space-data-network/releases/latest'")
    expect(runtimeFeedsSource).toContain('https://github.com/DigitalArsenal/space-data-network/releases/tag/desktop-v')
    expect(electronBuilderSource).toContain('owner: DigitalArsenal')
    expect(electronBuilderSource).toContain('repo: space-data-network')
    expect(electronBuilderSource).not.toContain('owner: ipfs')
    expect(electronBuilderSource).not.toContain('repo: ipfs-desktop')
    expect(autoUpdaterSource).toContain('shell.openExternal(SDN_DESKTOP_RELEASES_URL)')
    expect(autoUpdaterSource).toContain('shell.openExternal(sdnDesktopReleaseVersionUrl(version))')
    expect(autoUpdaterSource).not.toContain('github.com/ipfs/ipfs-desktop')
    expect(autoUpdaterSource).not.toContain('github.com/ipfs-shipyard/ipfs-desktop')
  })

  test('registers SDN and Web UI schemes as fetch-capable privileged schemes once', () => {
    const indexSource = fs.readFileSync(path.join(__dirname, '../../src/index.js'), 'utf8')
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')
    const webuiSource = fs.readFileSync(path.join(__dirname, '../../src/webui/index.js'), 'utf8')
    const packageJson = JSON.parse(fs.readFileSync(path.join(__dirname, '../../package.json'), 'utf8'))

    expect(indexSource).toContain('protocol.registerSchemesAsPrivileged')
    expect(indexSource).toContain("scheme: 'sdn'")
    expect(indexSource).toContain("scheme: 'webui'")
    expect(indexSource).toContain('supportFetchAPI: true')
    expect(indexSource).toContain('corsEnabled: true')
    expect(indexSource.match(/registerSchemesAsPrivileged/g)).toHaveLength(1)
    expect(dashboardSource).toContain("registerStaticScheme({ scheme: 'sdn'")
    expect(webuiSource).toContain("registerStaticScheme({ scheme: 'webui'")
    expect(dashboardSource).toContain("directory: 'assets/sdn-ui'")
    expect(webuiSource).toContain("directory: 'assets/webui'")
    expect(dashboardSource).not.toContain("require('electron-serve')")
    expect(webuiSource).not.toContain("require('electron-serve')")
    expect(packageJson.scripts['build:sdn-ui:build']).toBe('npm --prefix ../sdn-js run build:ui')
    expect(packageJson.scripts['build:sdn-ui:copy']).toContain('../sdn-js/ui/dist')
    expect(packageJson.scripts['build:webui:build']).toBe('npm --prefix ../webui run build')
    expect(packageJson.scripts['build:webui:copy']).toBe('shx rm -rf assets/webui && shx cp -r ../webui/build assets/webui')
    expect(packageJson.scripts['build:webui:download']).toBeUndefined()
  })

  test('redirects extensionless desktop static app routes so relative assets resolve', () => {
    const staticServerSource = fs.readFileSync(path.join(__dirname, '../../src/static-http-server.js'), 'utf8')

    expect(staticServerSource).toContain('redirectBareAppRoute')
    expect(staticServerSource).toContain("parsed.pathname !== `/${routeName}`")
    expect(staticServerSource).toContain("res.writeHead(301, staticAssetHeaders('text/plain; charset=utf-8', { Location: `/${routeName}/${parsed.search}${parsed.hash}` }))")
  })

  test('serves configured SDN node identities with libp2p websocket addresses only', () => {
    const {
      configuredSdnNodesFromSshConfig,
      isSdnSSHHostAlias
    } = require('../../src/static-http-server')
    const configPath = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-ssh-config-')), 'config')

    fs.mkdirSync(path.dirname(configPath), { recursive: true })
    fs.writeFileSync(configPath, [
      'Host space-data-network-01 sdn.spaceaware.io',
      '    HostName 159.203.150.8',
      '    User root',
      '',
      'Host space-data-network-02 celestrak.eth',
      '    HostName 167.172.219.213',
      '    User root',
      '',
      'Host github.com',
      '    HostName github.com',
      '',
      'Host *.example.invalid',
      '    HostName ignored.example.invalid'
    ].join('\n'))

    expect(isSdnSSHHostAlias('space-data-network-01')).toBe(true)
    expect(isSdnSSHHostAlias('sdn.spaceaware.io')).toBe(true)
    expect(isSdnSSHHostAlias('celestrak.eth')).toBe(true)
    expect(isSdnSSHHostAlias('github.com')).toBe(false)
    expect(isSdnSSHHostAlias('*.example.invalid')).toBe(false)
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.id)).toEqual([
      'space-data-network-01',
      'space-data-network-02'
    ])
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.addrs)).toEqual([
      ['/ip4/159.203.150.8/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45'],
      ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4']
    ])
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.name)).toEqual([
      'SpaceAware.io',
      'CelesTrak Provider'
    ])
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.metadata.peer_id)).toEqual([
      '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
      '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'
    ])
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.metadata.public_key)).toEqual([
      '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2',
      '90aa23ea4ff2d68cf8cb8155135fe5a25b580ec805e835aabb0e8905ffb2c3b2'
    ])
    expect(configuredSdnNodesFromSshConfig(configPath).map(node => node.metadata.ipfs_artifact_addrs)).toEqual([
      ['/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWMtfuRiHtDuzMMRYB2oX8UKVqP43hZQakGBLhWsMnCd7K'],
      ['/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j']
    ])
    expect(JSON.stringify(configuredSdnNodesFromSshConfig(configPath))).not.toContain('/p2p/space-data-network-')
    expect(JSON.stringify(configuredSdnNodesFromSshConfig(configPath))).not.toContain('/p2p/sdn.spaceaware.io')
    expect(JSON.stringify(configuredSdnNodesFromSshConfig(configPath))).not.toContain('/p2p/celestrak.eth')
    expect(JSON.stringify(configuredSdnNodesFromSshConfig(configPath))).not.toContain('http')
  })

  test('serves observed local Kubo SDN peers with real peer IDs for desktop SDN pages', () => {
    const { kuboSwarmPeersToDesktopSdnPeers } = require('../../src/static-http-server')

    const peers = kuboSwarmPeersToDesktopSdnPeers({
      Peers: [
        {
          Peer: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
          Addr: '/ip4/159.203.150.8/tcp/4001',
          Identify: {
            ID: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
            AgentVersion: 'spacedatanetwork/1.0.3',
            Protocols: ['/space-data-network/module-delivery/1.0.0']
          }
        },
        {
          Peer: '12D3KooWGeneric',
          Addr: '/ip4/203.0.113.1/tcp/4001',
          Identify: {
            ID: '12D3KooWGeneric',
            AgentVersion: 'kubo/0.39.0',
            Protocols: ['/ipfs/kad/1.0.0']
          }
        }
      ]
    })

    expect(peers).toHaveLength(1)
    expect(peers[0].id).toBe('16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45')
    expect(peers[0].addrs[0]).toBe('/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45')
    expect(JSON.stringify(peers)).not.toContain('/p2p/space-data-network-')
  })

  test('connects real SDN seed peer multiaddrs before desktop peer API reports empty', async () => {
    const { connectDesktopSdnSeedPeers, DESKTOP_SDN_SEED_PEERS } = require('../../src/static-http-server')
    const requestedPaths = []

    const results = await connectDesktopSdnSeedPeers(async (apiPath) => {
      requestedPaths.push(apiPath)
      if (apiPath.includes('dns4')) throw new Error('dns seed unavailable in test')
      return { Strings: ['connect success'] }
    })

    expect(results).toHaveLength(DESKTOP_SDN_SEED_PEERS.length)
    expect(results.some(result => result.ok)).toBe(true)
    expect(results.some(result => !result.ok)).toBe(true)
    expect(decodeURIComponent(requestedPaths.join('\n'))).toContain('/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45')
    expect(decodeURIComponent(requestedPaths.join('\n'))).toContain('/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4')
    expect(decodeURIComponent(requestedPaths.join('\n'))).not.toContain('/p2p/space-data-network-')
    expect(decodeURIComponent(requestedPaths.join('\n'))).not.toContain('/p2p/sdn.spaceaware.io')
    expect(decodeURIComponent(requestedPaths.join('\n'))).not.toContain('/p2p/celestrak.eth')
  })

  test('serves Svelte local data endpoints with degraded desktop-local placeholders', () => {
    const staticServerSource = fs.readFileSync(path.join(__dirname, '../../src/static-http-server.js'), 'utf8')

    expect(staticServerSource).toContain("parsed.pathname === '/api/v1/data/health'")
    expect(staticServerSource).toContain("parsed.pathname === '/api/v1/data/objects'")
    expect(staticServerSource).toContain("parsed.pathname === '/api/v1/data/query'")
    expect(staticServerSource).toContain('object_index')
    expect(staticServerSource).toContain('objects: []')
    expect(staticServerSource).toContain('local SQL index is not wired in desktop-local yet')
    expect(staticServerSource).toContain('handled || serveDesktopLocalDataAPI(req, res)')
  })

  test('keeps local Kubo bootstrapped to upstream defaults and SDN seed nodes', () => {
    const daemonConfigSource = fs.readFileSync(path.join(__dirname, '../../src/daemon/config.js'), 'utf8')

    expect(daemonConfigSource).toContain("const DESKTOP_BOOTSTRAP_PEERS = Object.freeze([")
    expect(daemonConfigSource).toContain("'auto'")
    expect(daemonConfigSource).toContain('/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45')
    expect(daemonConfigSource).toContain('/dns4/sdn.spaceaware.io/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45')
    expect(daemonConfigSource).toContain('/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4')
    expect(daemonConfigSource).toContain('/dns4/celestrak.eth/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4')
    expect(daemonConfigSource).toContain('ensureDesktopBootstrapPeers')
    expect(daemonConfigSource).toContain('config.Bootstrap = nextBootstrap')
  })

  test('advertises the packaged Kubo daemon as SDN Desktop', () => {
    const storeSource = fs.readFileSync(path.join(__dirname, '../../src/common/store.js'), 'utf8')

    expect(storeSource).toContain('--agent-version-suffix=sdn-desktop')
    expect(storeSource).toContain('set /sdn-desktop')
    expect(storeSource).not.toContain('--agent-version-suffix=desktop')
  })

  test('syncs the live Kubo RPC and gateway addresses before the SDN dashboard app first loads', () => {
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')

    expect(dashboardSource).toContain('async function syncIpfsAddresses')
    expect(dashboardSource).toContain("url.searchParams.set('api', apiAddress.toString())")
    expect(dashboardSource).toContain("url.searchParams.set('gateway', gatewayUrl)")
    expect(dashboardSource).toContain('ipcMain.on(ipcMainEvents.IPFSD, () => {')
    expect(dashboardSource).toContain('if (dashboardAppLoaded) void syncIpfsAddresses()')
    expect(dashboardSource).toContain('const addressesSynced = await syncIpfsAddresses()')
    expect(dashboardSource).toContain('if (!addressesSynced) window.webContents.loadURL(url.toString())')
  })

  test('does not load the hidden SDN dashboard app from Kubo address updates at desktop startup', () => {
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')

    expect(dashboardSource).toContain('let dashboardAppLoaded = false')
    expect(dashboardSource).toContain('if (dashboardAppLoaded) void syncIpfsAddresses()')
    expect(dashboardSource).toContain('dashboardAppLoaded = true')
  })

  test('syncs the live Kubo RPC and gateway addresses before the desktop Web UI first loads', () => {
    const webuiSource = fs.readFileSync(path.join(__dirname, '../../src/webui/index.js'), 'utf8')

    expect(webuiSource).toContain('async function syncIpfsAddresses')
    expect(webuiSource).toContain("url.searchParams.set('api', apiAddress.toString())")
    expect(webuiSource).toContain("url.searchParams.set('gateway', gatewayUrl)")
    expect(webuiSource).toContain('ipcMain.on(ipcMainEvents.IPFSD, () => {')
    expect(webuiSource).toContain('if (webUiLoaded) void syncIpfsAddresses()')
    expect(webuiSource).toContain('const addressesSynced = await syncIpfsAddresses()')
    expect(webuiSource).toContain('if (!addressesSynced) window.loadURL(url.toString())')
  })

  test('does not load the hidden IPFS Web UI renderer at desktop startup', () => {
    const webuiSource = fs.readFileSync(path.join(__dirname, '../../src/webui/index.js'), 'utf8')

    expect(webuiSource).toContain('let webUiLoaded = false')
    expect(webuiSource).toContain('async function loadWebUIApp')
    expect(webuiSource).toContain("await loadWebUIApp(path || '/')")
    expect(webuiSource).not.toContain("return /** @type {Promise<void>} */(new Promise(resolve =>")
  })

  test('routes tray menu entries to SDN dashboard pages instead of IPFS Web UI pages', () => {
    const traySource = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    expect(traySource).toContain("id: 'sdnUiHome'")
    expect(traySource).toContain("label: 'SDN UI'")
    expect(traySource).toContain("click: () => { launchDashboard('/') }")
    expect(traySource).toContain("id: 'sdnStatus'")
    expect(traySource).toContain("id: 'sdnFiles'")
    expect(traySource).toContain("id: 'sdnPeers'")
    expect(traySource).toContain("id: 'sdnNodeSettings'")
    expect(traySource).toContain("click: () => { launchDashboard('/status') }")
    expect(traySource).toContain("click: () => { launchDashboard('/files') }")
    expect(traySource).toContain("click: () => { launchDashboard('/peers') }")
    expect(traySource).toContain("click: () => { launchDashboard('/settings') }")
    expect(traySource).not.toContain("id: 'webuiStatus'")
    expect(traySource).not.toContain("id: 'webuiFiles'")
    expect(traySource).not.toContain("id: 'webuiPeers'")
    expect(traySource).not.toContain("id: 'webuiNodeSettings'")
    expect(traySource).not.toContain("click: () => { launchWebUI('/status') }")
    expect(traySource).not.toContain("click: () => { launchWebUI('/files') }")
    expect(traySource).not.toContain("click: () => { launchWebUI('/peers') }")
    expect(traySource).not.toContain("click: () => { launchWebUI('/settings') }")
  })

  test('uses SDN UI route and shell overrides instead of upstream WebUI routes', () => {
    const appSource = fs.readFileSync(path.join(__dirname, '../../../sdn-js/ui/src/upstream-webui/overrides/App.js'), 'utf8')
    const bundleSource = fs.readFileSync(path.join(__dirname, '../../../sdn-js/ui/src/upstream-webui/bundles/index.js'), 'utf8')
    const routeSource = fs.readFileSync(path.join(__dirname, '../../../sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js'), 'utf8')
    const navSource = fs.readFileSync(path.join(__dirname, '../../../sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js'), 'utf8')
    const entrySource = fs.readFileSync(path.join(__dirname, '../../../sdn-js/ui/src/upstream-webui/index.js'), 'utf8')

    expect(appSource).toContain("import NavBar from './navigation/NavBar.js'")
    expect(appSource).not.toContain("webui/src/navigation/NavBar.js")
    expect(bundleSource).toContain("import routesBundle from '../overrides/bundles/routes.js'")
    expect(bundleSource).not.toContain("webui/src/bundles/routes.js")
    expect(entrySource).toContain('syncKuboGatewaySettingFromUrl')
    expect(routeSource).toContain("import SettingsPage from '../settings/SettingsPage.js'")
    expect(routeSource).toContain("import DirectoryPage from '../directory/DirectoryPage.js'")
    expect(routeSource).toContain("import ModulesPage from '../modules/ModulesPage.js'")
    expect(routeSource).toContain("import MarketplacePage from '../marketplace/MarketplacePage.js'")
    expect(navSource).toContain("import sdnLogoMark from './sdn-logo-mark.svg'")
    expect(navSource).toContain("<NavLink to='/modules'")
    expect(navSource).toContain("<ExternalNavLink href='/webui'")
  })

  test('uses SDN status labels in the tray menu instead of upstream IPFS labels', () => {
    const traySource = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    expect(traySource).toContain("['ipfsIsRunning', 'SDN is Running', 'green']")
    expect(traySource).toContain("['ipfsIsStarting', 'SDN is Starting', 'yellow']")
    expect(traySource).toContain("['ipfsIsStopping', 'SDN is Stopping', 'yellow']")
    expect(traySource).toContain("['ipfsIsNotRunning', 'SDN is Not Running', 'gray']")
    expect(traySource).not.toContain('label: i18n.t(status)')
    expect(traySource).not.toContain("'IPFS is Running'")
  })

  test('uses the simplified solid triangle tray mark with a cut-out dot', () => {
    const traySvg = fs.readFileSync(path.join(__dirname, '../../assets/icons/tray/sdn-tray.svg'), 'utf8')

    expect(traySvg).toContain('data-sdn-mark="toolbar-solid-triangle-cutout"')
    expect(traySvg).toContain('fill-rule="evenodd"')
    expect(traySvg).toContain('M64 116 L19 38 L109 38 Z')
    expect(traySvg).toContain('M64 56a8 8 0 1 0 0 16a8 8 0 1 0 0-16')
    expect(traySvg).not.toContain('<ellipse')
    expect(traySvg).not.toContain('<text')
  })
})
