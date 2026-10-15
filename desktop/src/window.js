// @ts-check
//
// One window. It shows the start-up status page until the node answers, then
// loads the node's own dashboard -- which is compiled into the node binary and
// served by the node, so this shell renders it and nothing more.
const { BrowserWindow, shell } = require('electron')
const { join } = require('node:path')

const logger = require('./common/logger')
const { PHASES } = require('./node/ready')
const { consoleCookie, isConsoleNavigation, isExternalBrowserUrl } = require('./node/console-token')
const { PRODUCT_NAME } = require('./common/consts')

const STATUS_PAGE = join(__dirname, '..', 'assets', 'pages', 'status.html')

class MainWindow {
  constructor () {
    /** @type {Electron.BrowserWindow|null} */
    this.window = null
    this.showingDashboard = false
    /** @type {string|null} the dashboard this window holds the console cookie for */
    this.dashboardUrl = null
    /** @type {object|null} */
    this.lastState = null
    /** @type {() => string} the running daemon's console secret */
    this.consoleToken = () => ''
  }

  /** @param {() => string} provider */
  setConsoleTokenProvider (provider) {
    this.consoleToken = provider
  }

  /** @returns {Electron.BrowserWindow} */
  create () {
    const window = new BrowserWindow({
      title: PRODUCT_NAME,
      width: 1280,
      height: 860,
      minWidth: 420,
      minHeight: 420,
      show: false,
      backgroundColor: '#0b0f14',
      autoHideMenuBar: true,
      webPreferences: {
        preload: join(__dirname, 'preload.js'),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true
      }
    })
    this.window = window
    window.once('ready-to-show', () => window.show())
    window.on('closed', () => { this.window = null })
    // A link to somewhere else belongs in the user's browser, never in the
    // shell that runs their node.
    window.webContents.setWindowOpenHandler(({ url }) => {
      if (isExternalBrowserUrl(url)) shell.openExternal(url).catch((err) => logger.error(err))
      return { action: 'deny' }
    })
    // The window carries the console cookie, which is not port-scoped: it
    // never leaves the dashboard's origin, by link or by redirect.
    window.webContents.on('will-navigate', (event, url) => {
      if (isConsoleNavigation(this.dashboardUrl, url)) return
      event.preventDefault()
      if (isExternalBrowserUrl(url)) shell.openExternal(url).catch((err) => logger.error(err))
    })
    window.webContents.on('will-redirect', (event, url) => {
      if (!this.showingDashboard || isConsoleNavigation(this.dashboardUrl, url)) return
      logger.error(`[window] refused a redirect away from the dashboard origin: ${url}`)
      event.preventDefault()
    })
    this.showStatus()
    return window
  }

  show () {
    if (!this.window || this.window.isDestroyed()) {
      this.create()
      return
    }
    if (this.window.isMinimized()) this.window.restore()
    this.window.show()
    this.window.focus()
  }

  showStatus () {
    if (!this.window || this.window.isDestroyed()) return
    this.showingDashboard = false
    this.dashboardUrl = null
    this.window.loadFile(STATUS_PAGE).catch((err) => logger.error(err))
  }

  /** @param {string} url */
  showDashboard (url) {
    if (!this.window || this.window.isDestroyed()) return
    if (this.showingDashboard) return
    this.showingDashboard = true
    this.dashboardUrl = url
    logger.info(`[window] loading the node dashboard at ${url}`)
    const window = this.window
    // The console secret rides in an HttpOnly, SameSite=Strict cookie on the
    // node's own origin, set in this window's session before the dashboard
    // loads: it is what admits this window as the node's console.
    Promise.resolve()
      .then(() => window.webContents.session.cookies.set(consoleCookie(url, this.consoleToken())))
      .then(() => window.loadURL(url))
      .catch((err) => {
        logger.error(err)
        this.showingDashboard = false
        this.showStatus()
      })
  }

  /**
   * Route one node state to the window: the dashboard when the node is ready,
   * the status page otherwise.
   *
   * @param {{ phase: string, url: string }} state
   */
  applyState (state) {
    this.lastState = state
    if (!this.window || this.window.isDestroyed()) return
    if (state.phase === PHASES.READY && state.url) {
      this.showDashboard(state.url)
      return
    }
    if (this.showingDashboard) this.showStatus()
    this.send(state)
  }

  /** @param {object} state */
  send (state) {
    if (!this.window || this.window.isDestroyed() || this.showingDashboard) return
    const send = () => this.window?.webContents.send('node-state', state)
    if (this.window.webContents.isLoading()) {
      this.window.webContents.once('did-finish-load', send)
    } else {
      send()
    }
  }
}

module.exports = { MainWindow, STATUS_PAGE }
