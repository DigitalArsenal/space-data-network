// @ts-check
const { screen, BrowserWindow, app, ipcMain, shell } = require('electron')
const { join } = require('path')
const logger = require('../common/logger')
const store = require('../common/store')
const { OPEN_WEBUI_LAUNCH: CONFIG_KEY } = require('../common/config-keys')
const dock = require('../utils/dock')
const getCtx = require('../context')
const ipcMainEvents = require('../common/ipc-main-events')
const { STATUS } = require('../daemon/consts')
const { daemonDashboardRoute } = require('./routes')

const createWindow = () => {
  logger.info('[dashboard] creating window')
  const dimensions = screen.getPrimaryDisplay()

  const window = new BrowserWindow({
    title: 'Space Data Network',
    show: false,
    autoHideMenuBar: true,
    frame: false,
    titleBarStyle: 'hiddenInset',
    width: store.get('window.width', dimensions.width < 1440 ? dimensions.width : 1440),
    height: store.get('window.height', dimensions.height < 900 ? dimensions.height : 900),
    webPreferences: {
      preload: join(__dirname, 'preload.js'),
      webSecurity: true,
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

  const getSdnDaemon = ctx.getFn('getSdnDaemon')
  let dashboardAppLoaded = false
  let currentRoute = '/'
  let currentOrigin = ''

  async function loadDashboardApp (path = '/') {
    const daemon = await getSdnDaemon(true)
    if (!daemon?.adminUrl) {
      logger.error('[dashboard] SDN daemon is not running')
      return false
    }

    const url = new URL(daemon.adminUrl)
    url.hash = daemonDashboardRoute(path)
    currentRoute = path
    currentOrigin = url.origin
    await window.webContents.loadURL(url.toString())
    dashboardAppLoaded = true
    return true
  }

  window.webContents.setWindowOpenHandler(({ url }) => {
    try {
      if (new URL(url).origin === currentOrigin) return { action: 'allow' }
    } catch (_) {}
    shell.openExternal(url).catch(err => logger.error('[dashboard] failed to open external URL', err))
    return { action: 'deny' }
  })

  window.webContents.on('will-navigate', (event, targetUrl) => {
    try {
      if (new URL(targetUrl).origin === currentOrigin) return
    } catch (_) {}
    event.preventDefault()
    shell.openExternal(targetUrl).catch(err => logger.error('[dashboard] failed to open external URL', err))
  })

  ipcMain.on(ipcMainEvents.IPFSD, status => {
    if (status === STATUS.STARTING_FINISHED || dashboardAppLoaded) {
      loadDashboardApp(currentRoute).catch(err => logger.error('[dashboard] failed to reload daemon dashboard', err))
    }
  })

  ctx.setProp('launchDashboard', async (path, { focus = true, forceRefresh = false } = {}) => {
    if (window.isDestroyed()) {
      logger.error(`[dashboard] window is destroyed, not launching dashboard with ${path}`)
      return
    }

    if (!dashboardAppLoaded || path) {
      await loadDashboardApp(path || '/')
    } else if (forceRefresh) {
      window.webContents.reload()
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

    loadDashboardApp('/').catch(err => logger.error('[dashboard] failed to load daemon dashboard', err))
  }))
}
