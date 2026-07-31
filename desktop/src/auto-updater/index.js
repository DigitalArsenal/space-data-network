const { shell, app, Notification, net, ipcMain } = require('electron')
const i18n = require('i18next')
const fs = require('fs')
const path = require('path')
const logger = require('../common/logger')
const { showDialog } = require('../dialogs')
const { IS_MAC, IS_WIN, IS_APPIMAGE } = require('../common/consts')
const ipcMainEvents = require('../common/ipc-main-events')
const getCtx = require('../context')
const store = require('../common/store')
const CONFIG_KEYS = require('../common/config-keys')
const {
  SDN_DESKTOP_AUTO_UPDATES_ENABLED,
  SDN_DESKTOP_RELEASES_URL,
  sdnDesktopReleaseVersionUrl
} = require('../sdn-updater/runtime-feeds')
const { createSdnFeedUpdater } = require('../sdn-updater/feed-updater')
const { SDN_UPDATE_FEED_BASE_URL } = require('../sdn-updater/release-feed')
const { loadTrustedUpdateRoots } = require('../sdn-updater/trusted-roots')

function isAutoUpdateSupported () {
  if (!SDN_DESKTOP_AUTO_UPDATES_ENABLED) {
    logger.info('[updater] SDN desktop auto updates disabled until the SDN patch/update server is available')
    return false
  }
  if (store.get(CONFIG_KEYS.DISABLE_AUTO_UPDATE, false)) {
    logger.info('[updater] auto update explicitly disabled, not checking for updates automatically')
    return false
  }
  if (!hasPackagedUpdateConfig()) {
    logger.info('[updater] app-update.yml missing, not checking for updates automatically')
    return false
  }
  // atm only macOS, windows and AppImage builds support autoupdate mechanism,
  // everything else needs to be updated manually or via a third-party package manager
  return IS_MAC || IS_WIN || IS_APPIMAGE
}

function hasPackagedUpdateConfig () {
  return fs.existsSync(path.join(process.resourcesPath, 'app-update.yml'))
}

let updateNotification = null // must be a global to avoid gc
let feedback = false

async function fetchResponse (url) {
  const response = await net.fetch(url, { cache: 'no-store' })
  if (!response.ok) {
    throw new Error(`SDN update feed request failed with status ${response.status} for ${url}`)
  }
  return response
}

async function fetchJson (url) {
  const response = await fetchResponse(url)
  return response.json()
}

async function fetchBytes (url) {
  const response = await fetchResponse(url)
  return Buffer.from(await response.arrayBuffer())
}

// The SDN feed updater downloads update payloads exclusively from the
// SDN-owned signed update feed (https://sdn.spaceaware.io/updates), never
// from inherited IPFS Desktop GitHub release feeds. A fresh instance is
// created per check so the persisted sequence is always re-read.
function createFeedUpdater () {
  const ctx = getCtx()

  return createSdnFeedUpdater({
    rootDir: path.join(app.getPath('userData'), 'sdn-updates'),
    baseUrl: process.env.SDN_UPDATE_FEED_BASE_URL || SDN_UPDATE_FEED_BASE_URL,
    channel: 'stable',
    platform: process.platform,
    arch: process.arch,
    currentSequence: store.get(CONFIG_KEYS.SDN_UPDATE_SEQUENCE, 0),
    trustedRoots: loadTrustedUpdateRoots(),
    fetchJson,
    fetchBytes,
    lifecycle: {
      getIpfsd: ctx.getFn('getIpfsd'),
      stopIpfs: ctx.getFn('stopIpfs'),
      startIpfs: ctx.getFn('startIpfs')
    },
    log: message => logger.info(`[updater] ${message}`)
  })
}

function showUpdateErrorDialog () {
  const opt = showDialog({
    title: i18n.t('autoUpdateError.title'),
    message: i18n.t('autoUpdateError.message'),
    type: 'error',
    buttons: [
      i18n.t('autoUpdateError.later'),
      i18n.t('autoUpdateError.downloadNow')
    ]
  })

  if (opt === 1) {
    shell.openExternal(SDN_DESKTOP_RELEASES_URL)
  }
}

function onUpdateError (err) {
  logger.error(`[updater] error: ${err.message}`)
  if (err.stack) {
    logger.error(`[updater] stack: ${err.stack}`)
  }

  if (!feedback) {
    return
  }

  feedback = false

  // Show dialogs only for explicit user-requested update checks. Background
  // updater errors must not block the main process that serves desktop UI.
  showUpdateErrorDialog()
}

function onUpdateNotAvailable () {
  logger.info('[updater] update not available')

  if (!feedback) {
    return
  }

  feedback = false
  showDialog({
    title: i18n.t('updateNotAvailableDialog.title'),
    message: i18n.t('updateNotAvailableDialog.message', { version: app.getVersion() }),
    type: 'info',
    buttons: [
      i18n.t('close')
    ]
  })
}

async function commitStagedUpdate (updater, staged) {
  const result = await updater.commit(staged.updateId, async currentPath => {
    for (const file of ['manifest.json', 'update.wasm', 'bundle.tar.zst']) {
      if (!fs.existsSync(path.join(currentPath, file))) {
        throw new Error(`committed update is missing ${file}`)
      }
    }
  })

  await store.safeSet(CONFIG_KEYS.SDN_UPDATE_SEQUENCE, staged.sequence)
  logger.info(`[updater] update ${staged.updateId} committed, sequence is now ${staged.sequence}`)
  return result
}

function onUpdateStaged (updater, staged) {
  const version = staged.version
  logger.info(`[updater] update ${staged.updateId} (${version}) verified and staged`)

  const feedbackDialog = () => {
    const opt = showDialog({
      title: i18n.t('updateDownloadedDialog.title'),
      message: i18n.t('updateDownloadedDialog.message', { version }),
      type: 'info',
      buttons: [
        i18n.t('updateDownloadedDialog.later'),
        i18n.t('updateDownloadedDialog.now')
      ]
    })
    if (opt === 1) { // now
      setImmediate(async () => {
        try {
          await commitStagedUpdate(updater, staged)
        } catch (err) {
          logger.error(`[updater] commit failed: ${err.message}`)
          showUpdateErrorDialog()
        }
      })
    }
  }

  if (feedback) {
    feedback = false
    // when in instant feedback mode, surface the update immediately
    const opt = showDialog({
      title: i18n.t('updateAvailableDialog.title'),
      message: i18n.t('updateAvailableDialog.message', { version }),
      type: 'info',
      buttons: [
        i18n.t('close'),
        i18n.t('readReleaseNotes')
      ]
    })

    if (opt === 1) {
      shell.openExternal(sdnDesktopReleaseVersionUrl(version))
    }

    feedbackDialog()
  } else {
    // show unobtrusive notification + dialog on click
    updateNotification = new Notification({
      title: i18n.t('updateDownloadedNotification.title'),
      body: i18n.t('updateDownloadedNotification.message', { version })
    })
    updateNotification.on('click', feedbackDialog)
    updateNotification.show()
  }
}

async function checkForUpdates () {
  logger.info('[updater] checking for updates')
  ipcMain.emit(ipcMainEvents.UPDATING)
  try {
    const updater = createFeedUpdater()
    const staged = await updater.checkAndStage()
    if (staged) {
      onUpdateStaged(updater, staged)
    } else {
      onUpdateNotAvailable()
    }
  } catch (err) {
    onUpdateError(err)
  }
  ipcMain.emit(ipcMainEvents.UPDATING_ENDED)
}

module.exports = async function () {
  if (['test', 'development'].includes(process.env.NODE_ENV ?? '')) {
    getCtx().setProp('manualCheckForUpdates', () => {
      showDialog({
        title: 'Not available in development',
        message: 'Yes, you called this function successfully.',
        buttons: [i18n.t('close')]
      })
    })
    return
  }
  if (!isAutoUpdateSupported()) {
    getCtx().setProp('manualCheckForUpdates', () => {
      shell.openExternal(SDN_DESKTOP_RELEASES_URL)
    })
    return
  }

  checkForUpdates()

  setInterval(checkForUpdates, 43200000)

  // enable on-demand check via About submenu
  getCtx().setProp('manualCheckForUpdates', () => {
    feedback = true
    checkForUpdates()
  })
}
