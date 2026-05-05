// @ts-check
const { screen, BrowserWindow, app, ipcMain } = require('electron')
const { join } = require('path')
const { URL } = require('url')
const logger = require('../common/logger')
const store = require('../common/store')
const { OPEN_WEBUI_LAUNCH: CONFIG_KEY } = require('../common/config-keys')
const dock = require('../utils/dock')
const getCtx = require('../context')
const registerStaticScheme = require('../static-scheme')
const ipcMainEvents = require('../common/ipc-main-events')
const { getDesktopStaticUrl } = require('../static-http-server')

registerStaticScheme({ scheme: 'sdn', directory: 'assets/sdn-ui' })
const introPath = join(__dirname, '../../assets/pages/sdn-intro.html')

function isIntroRoute (path) {
  return !path || path === '/'
}

function isIntroAdminNavigation (targetUrl) {
  try {
    const parsed = new URL(targetUrl)
    return parsed.protocol === 'file:' && (parsed.pathname === '/admin' || parsed.pathname === '/admin/')
  } catch {
    return false
  }
}

const createWindow = () => {
  logger.info('[dashboard] creating window')
  const dimensions = screen.getPrimaryDisplay()

  const window = new BrowserWindow({
    title: 'Space Data Network',
    show: false,
    autoHideMenuBar: true,
    titleBarStyle: 'hiddenInset',
    width: store.get('window.width', dimensions.width < 1440 ? dimensions.width : 1440),
    height: store.get('window.height', dimensions.height < 900 ? dimensions.height : 900),
    webPreferences: {
      preload: join(__dirname, 'preload.js'),
      webSecurity: false,
      allowRunningInsecureContent: false,
      enableRemoteModule: process.env.NODE_ENV === 'test',
      nodeIntegration: process.env.NODE_ENV === 'test'
    }
  })

  window.on('resize', () => {
    const dim = window.getSize()
    store.safeSet('window.width', dim[0])
    store.safeSet('window.height', dim[1])
  })

  window.on('close', (event) => {
    event.preventDefault()
    window.hide()
    dock.hide()
    logger.info('[dashboard] window hidden')
  })

  app.on('before-quit', () => {
    window.removeAllListeners('close')
  })

  return window
}

module.exports = async function () {
  logger.info('[dashboard] init...')
  const ctx = getCtx()
  const window = createWindow()
  ctx.setProp('dashboard', window)

  const url = await getDesktopStaticUrl('sdn')
  let apiAddress = null
  const loadIntroPage = () => window.loadFile(introPath)
  const getIpfsd = ctx.getFn('getIpfsd')

  async function syncIpfsApiAddress () {
    const ipfsd = await getIpfsd(true)

    if (ipfsd && ipfsd.apiAddr !== apiAddress) {
      apiAddress = ipfsd.apiAddr
      url.searchParams.set('api', apiAddress.toString())
      window.webContents.loadURL(url.toString())
      return true
    }

    return false
  }

  ipcMain.on(ipcMainEvents.IPFSD, syncIpfsApiAddress)

  const loadDashboardApp = async (path) => {
    url.hash = path || '/'
    const apiAddressSynced = await syncIpfsApiAddress()
    if (!apiAddressSynced) window.webContents.loadURL(url.toString())
  }

  window.webContents.on('will-navigate', (event, targetUrl) => {
    if (!isIntroAdminNavigation(targetUrl)) {
      return
    }

    event.preventDefault()
    loadDashboardApp('/')
  })

  ctx.setProp('launchDashboard', async (path, { focus = true, forceRefresh = false } = {}) => {
    if (window.isDestroyed()) {
      logger.error(`[dashboard] window is destroyed, not launching dashboard with ${path}`)
      return
    }

    if (forceRefresh) {
      window.webContents.reload()
    }

    if (isIntroRoute(path)) {
      loadIntroPage()
    } else {
      await loadDashboardApp(path)
    }

    if (focus) {
      window.show()
      window.focus()
      dock.show()
    }
  })

  const launchDashboard = ctx.getFn('launchDashboard')
  const splashScreen = await ctx.getProp('splashScreen')
  if (store.get(CONFIG_KEY)) {
    splashScreen.show()
  } else {
    splashScreen.destroy()
  }

  return /** @type {Promise<void>} */(new Promise(resolve => {
    window.once('ready-to-show', async () => {
      logger.info('[dashboard] window ready')
      if (store.get(CONFIG_KEY)) {
        await launchDashboard('/')
        try {
          splashScreen.destroy()
        } catch (err) {
          logger.error('[dashboard] failed to hide splash screen')
          logger.error(err)
        }
      }
      resolve()
    })

    loadIntroPage()
  }))
}
