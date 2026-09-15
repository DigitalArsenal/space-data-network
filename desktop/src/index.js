// @ts-check
//
// Space Data Network desktop: a shell that runs the bundled node and shows the
// node's own dashboard.
//
// It owns four things and nothing else: the node child process, one window,
// the tray, and the logs directory. The dashboard, the API, the wallet sign-in
// assets and the Kubo the node needs are all inside the node bundle and are
// served by the node itself.
require('v8-compile-cache')

const { app, ipcMain, shell } = require('electron')
const { join } = require('node:path')

if (process.env.NODE_ENV === 'test' && process.env.HOME) {
  app.setPath('home', process.env.HOME)
  app.setPath('userData', join(process.env.HOME, 'data'))
}

const logger = require('./common/logger')
const setupAppMenu = require('./app-menu')
const autoLaunch = require('./auto-launch')
const { errorDialog, showDialog } = require('./dialogs')
const { MainWindow } = require('./window')
const { NodeTray } = require('./tray')
const { NodeSupervisor } = require('./node/supervisor')
const { showRecoveryPhrase } = require('./node/identity')
const { PHASES } = require('./node/ready')
const { IS_MAC } = require('./common/consts')

app.setAppUserModelId('org.spacedatanetwork.desktop')

// One node per machine, so one instance of the app that runs it.
if (!app.requestSingleInstanceLock()) {
  app.exit(0)
}

const window = new MainWindow()
/** @type {NodeSupervisor|null} */
let supervisor = null
/** @type {NodeTray|null} */
let tray = null
let quitting = false

async function quit () {
  if (quitting) return
  quitting = true
  logger.info('[app] quitting')
  try {
    await supervisor?.stop()
  } catch (err) {
    logger.error(/** @type {Error} */(err))
  }
  tray?.destroy()
  app.exit(0)
}

function openDashboard () {
  window.show()
  if (supervisor?.state.phase === PHASES.READY && supervisor.dashboardUrl) {
    window.showDashboard(supervisor.dashboardUrl)
  }
}

function showLogs () {
  shell.openPath(logger.logsPath).then((err) => { if (err) logger.warn(`[app] could not open logs: ${err}`) })
}

function restartNode () {
  window.show()
  window.showStatus()
  supervisor?.restart().catch((err) => {
    logger.error(/** @type {Error} */(err))
    errorDialog(err, 'Could not restart the node')
  })
}

function recoveryPhrase () {
  showRecoveryPhrase({
    bundle: supervisor?.bundle ?? null,
    configPath: supervisor?.configPath ?? '',
    parent: window.window ?? undefined
  }).catch((err) => logger.error(/** @type {Error} */(err)))
}

const actions = { openDashboard, restartNode, showLogs, showRecoveryPhrase: recoveryPhrase, quit }

async function run () {
  await app.whenReady()

  supervisor = new NodeSupervisor({
    userDataPath: app.getPath('userData'),
    appPath: app.getAppPath(),
    resourcesPath: app.isPackaged ? process.resourcesPath : undefined,
    logsPath: logger.logsPath
  })

  setupAppMenu(actions)
  window.create()
  tray = new NodeTray(actions)
  tray.start()
  await autoLaunch.applyStoredPreference()

  supervisor.on('state', (state) => {
    window.applyState(state)
    tray?.update(state)
  })

  ipcMain.on('node-state-request', () => window.send(supervisor?.snapshot() ?? {}))
  ipcMain.on('node-restart', () => restartNode())
  ipcMain.on('node-show-logs', () => showLogs())

  app.on('second-instance', () => openDashboard())
  app.on('activate', () => window.show())
  // Closing the window leaves the node running in the tray; Quit stops it.
  app.on('window-all-closed', () => { if (!IS_MAC && quitting) app.exit(0) })
  app.on('before-quit', (event) => {
    if (quitting) return
    event.preventDefault()
    quit()
  })

  await supervisor.start()
}

process.on('uncaughtException', onFatal)
process.on('unhandledRejection', onFatal)

/** @param {unknown} err */
function onFatal (err) {
  if (err == null) return
  logger.error(/** @type {Error} */(err))
  if (app.isReady()) errorDialog(err, 'Space Data Network hit an error')
}

run().catch((err) => {
  logger.error(err)
  showDialog({ title: 'Space Data Network could not start', message: String(err?.message ?? err), type: 'error' })
  app.exit(1)
})
