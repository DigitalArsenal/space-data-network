// @ts-check
const { app } = require('electron')
const os = require('node:os')
const path = require('node:path')
const fs = require('fs-extra')
const untildify = require('untildify')

const logger = require('./common/logger')
const store = require('./common/store')
const { IS_MAC, IS_WIN, PRODUCT_NAME } = require('./common/consts')
const { AUTO_LAUNCH: CONFIG_KEY } = require('./common/config-keys')

function isSupported () {
  const platform = os.platform()
  return platform === 'linux' || platform === 'win32' || platform === 'darwin'
}

function getDesktopFile () {
  return path.join(untildify('~/.config/autostart/'), 'space-data-network.desktop')
}

async function enable () {
  if (IS_MAC || IS_WIN) {
    app.setLoginItemSettings({ openAtLogin: true })
    return
  }
  await fs.outputFile(getDesktopFile(), `[Desktop Entry]
Type=Application
Version=1.0
Name=${PRODUCT_NAME}
Comment=Run the Space Data Network node at login
Exec="${process.execPath}"
Icon=space-data-network
StartupNotify=false
Terminal=false`)
}

async function disable () {
  if (IS_MAC || IS_WIN) {
    app.setLoginItemSettings({ openAtLogin: false })
    return
  }
  await fs.remove(getDesktopFile())
}

/** @returns {boolean} whether the app is set to launch at login */
function isEnabled () {
  return store.get(CONFIG_KEY, false) === true
}

/**
 * @param {boolean} enabled
 * @returns {Promise<boolean>} the value that is now in force
 */
async function setEnabled (enabled) {
  if (!isSupported() || process.env.NODE_ENV === 'development') {
    logger.info('[auto-launch] not available here')
    return isEnabled()
  }
  try {
    if (enabled) await enable()
    else await disable()
    store.set(CONFIG_KEY, enabled)
    logger.info(`[auto-launch] ${enabled ? 'enabled' : 'disabled'}`)
    return enabled
  } catch (err) {
    logger.error(/** @type {Error} */(err))
    return isEnabled()
  }
}

/** Put the stored preference back in force at start-up. */
async function applyStoredPreference () {
  if (!isSupported() || process.env.NODE_ENV === 'development') return
  try {
    if (isEnabled()) await enable()
  } catch (err) {
    logger.error(/** @type {Error} */(err))
  }
}

module.exports = { applyStoredPreference, isEnabled, isSupported, setEnabled }
