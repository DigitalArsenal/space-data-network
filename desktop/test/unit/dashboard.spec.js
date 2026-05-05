const fs = require('fs')
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
    expect(packageJson.scripts['build:webui:build']).toBe('npm --prefix ../webui run build')
    expect(packageJson.scripts['build:webui:copy']).toBe('shx rm -rf assets/webui && shx cp -r ../webui/build assets/webui')
    expect(packageJson.scripts['build:webui:download']).toBeUndefined()
  })

  test('syncs the live Kubo RPC address before the SDN dashboard app first loads', () => {
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../../src/dashboard/index.js'), 'utf8')

    expect(dashboardSource).toContain('async function syncIpfsApiAddress')
    expect(dashboardSource).toContain('ipcMain.on(ipcMainEvents.IPFSD, syncIpfsApiAddress)')
    expect(dashboardSource).toContain('const apiAddressSynced = await syncIpfsApiAddress()')
    expect(dashboardSource).toContain('if (!apiAddressSynced) window.webContents.loadURL(url.toString())')
  })

  test('syncs the live Kubo RPC address before the desktop Web UI first loads', () => {
    const webuiSource = fs.readFileSync(path.join(__dirname, '../../src/webui/index.js'), 'utf8')

    expect(webuiSource).toContain('async function syncIpfsApiAddress')
    expect(webuiSource).toContain('ipcMain.on(ipcMainEvents.IPFSD, syncIpfsApiAddress)')
    expect(webuiSource).toContain('const apiAddressSynced = await syncIpfsApiAddress()')
    expect(webuiSource).toContain('if (!apiAddressSynced) window.loadURL(url.toString())')
  })

  test('routes tray menu home to SDN UI and IPFS links to Web UI pages', () => {
    const traySource = fs.readFileSync(path.join(__dirname, '../../src/tray.js'), 'utf8')

    expect(traySource).toContain("id: 'sdnUiHome'")
    expect(traySource).toContain("label: 'SDN UI'")
    expect(traySource).toContain("click: () => { launchDashboard('/') }")
    expect(traySource).toContain("id: 'webuiStatus'")
    expect(traySource).toContain("click: () => { launchDashboard('/status') }")
    expect(traySource).not.toContain("click: () => { launchWebUI('/') }")
    expect(traySource).not.toContain("id: 'webuiStatus',\n      label: i18n.t('status'),\n      click: () => { launchDashboard('/') }")
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
