// @ts-check
const { registerAppStartTime, getSecondsSinceAppStart } = require('./metrics/appStart')
registerAppStartTime()
require('v8-compile-cache')

const { app, dialog, protocol } = require('electron')

protocol.registerSchemesAsPrivileged([
  {
    scheme: 'sdn',
    privileges: {
      standard: true,
      secure: true,
      allowServiceWorkers: true,
      supportFetchAPI: true,
      corsEnabled: true
    }
  },
  {
    scheme: 'webui',
    privileges: {
      standard: true,
      secure: true,
      allowServiceWorkers: true,
      supportFetchAPI: true,
      corsEnabled: true
    }
  }
])

if (process.env.NODE_ENV === 'test') {
  const path = require('path')
  if (process.env.HOME) {
    app.setPath('home', process.env.HOME)
    app.setPath('userData', path.join(process.env.HOME, 'data'))
  }
}

const getCtx = require('./context')
const fixPath = require('fix-path')
const logger = require('./common/logger')
const setupProtocolHandlers = require('./protocol-handlers')
const setupI18n = require('./i18n')
const setupDaemon = require('./daemon')
const setupDashboard = require('./dashboard')
const setupWebUI = require('./webui')
const setupAutoLaunch = require('./auto-launch')
const setupAutoGc = require('./automatic-gc')
const setupPubsub = require('./enable-pubsub')
const setupNamesysPubsub = require('./enable-namesys-pubsub')
const setupTakeScreenshot = require('./take-screenshot')
const setupAppMenu = require('./app-menu')
const setupArgvFilesHandler = require('./argv-files-handler')
const setupAutoUpdater = require('./auto-updater')
const setupTray = require('./tray')
const setupAnalytics = require('./analytics')
const setupSecondInstance = require('./second-instance')
const { analyticsKeys } = require('./analytics/keys')
const handleError = require('./handleError')
const createSplashScreen = require('./splash/create-splash-screen')

configureWebAuthnPlatformAuthenticator()

// Hide Dock
if (app.dock) app.dock.hide()

// Sets User Model Id so notifications work on Windows 10
app.setAppUserModelId('org.spacedatanetwork.desktop')

// Fixes $PATH on macOS
fixPath()

// Only one instance can run at a time
if (!app.requestSingleInstanceLock()) {
  process.exit(0)
}

app.on('will-finish-launching', () => {
  setupProtocolHandlers()
})

process.on('uncaughtException', handleError)
process.on('unhandledRejection', handleError)

function configureWebAuthnPlatformAuthenticator () {
  if (typeof app.configureWebAuthn !== 'function') return
  const keychainAccessGroup = process.env.SDN_WEBAUTHN_KEYCHAIN_ACCESS_GROUP || process.env.SDN_WEB_AUTHN_KEYCHAIN_ACCESS_GROUP || ''
  if (!keychainAccessGroup) {
    logger.debug('Touch ID WebAuthn platform authenticator is available but no SDN_WEBAUTHN_KEYCHAIN_ACCESS_GROUP is configured')
    return
  }
  try {
    app.configureWebAuthn({
      touchID: { keychainAccessGroup }
    })
    logger.info('Touch ID WebAuthn platform authenticator enabled')
  } catch (err) {
    logger.warn(`failed to enable Touch ID WebAuthn platform authenticator: ${err.message || err}`)
  }
}

async function run () {
  try {
    await app.whenReady()
  } catch (e) {
    dialog.showErrorBox('Electron could not start', e.stack)
    app.exit(1)
  }

  try {
    await Promise.all([
      createSplashScreen(),
      setupDaemon(), // ctx.getIpfsd, startIpfs, stopIpfs, restartIpfs
      setupAnalytics(), // ctx.countlyDeviceId
      setupI18n(),
      setupAppMenu(),

      setupDashboard(), // ctx.dashboard, launchDashboard
      setupWebUI(), // ctx.webui, launchWebUI
      setupAutoUpdater(), // ctx.manualCheckForUpdates
      setupTray(), // ctx.tray
      setupArgvFilesHandler(),
      setupAutoLaunch(),
      setupAutoGc(),
      setupPubsub(),
      setupNamesysPubsub(),
      setupSecondInstance(),
      // Setup global shortcuts
      setupTakeScreenshot()
    ])
    const submitAppReady = () => {
      logger.addAnalyticsEvent({ withAnalytics: analyticsKeys.APP_READY, dur: getSecondsSinceAppStart() })
    }
    const dashboard = await getCtx().getProp('dashboard')
    if (dashboard.webContents.isLoading()) {
      dashboard.webContents.once('dom-ready', submitAppReady)
    } else {
      submitAppReady()
    }
  } catch (e) {
    const splash = await getCtx().getProp('splashScreen')
    if (splash && !splash.isDestroyed()) splash.hide()
    handleError(e)
  }
}

run()
