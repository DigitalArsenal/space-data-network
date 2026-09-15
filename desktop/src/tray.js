// @ts-check
//
// The menubar / tray item: what the node is doing, and the five things an
// operator does to it.
const { Menu, Tray, nativeImage, nativeTheme } = require('electron')
const path = require('node:path')

const logger = require('./common/logger')
const { IS_MAC, PRODUCT_NAME, VERSION } = require('./common/consts')
const { PHASES } = require('./node/ready')
const autoLaunch = require('./auto-launch')

const STATUS_COLOUR = {
  [PHASES.STARTING]: 'yellow',
  [PHASES.HYDRATING]: 'yellow',
  [PHASES.READY]: 'green',
  [PHASES.FAILED]: 'red',
  [PHASES.STOPPED]: 'gray'
}

const STATUS_LABEL = {
  [PHASES.STARTING]: 'Node is starting',
  [PHASES.HYDRATING]: 'Node is opening its store',
  [PHASES.READY]: 'Node is running',
  [PHASES.FAILED]: 'Node has stopped unexpectedly',
  [PHASES.STOPPED]: 'Node is stopped'
}

/** @param {'on'|'off'} state */
function trayIcon (state) {
  const dir = path.resolve(path.join(__dirname, '..', 'assets', 'icons', 'tray'))
  if (IS_MAC) return path.join(dir, 'macos', `${state}-22Template.png`)
  const theme = nativeTheme.shouldUseDarkColors ? 'dark' : 'light'
  return path.join(dir, 'others', `${state}-32-${theme}.png`)
}

/**
 * The status dots ship only as @Nx variants, so the image is resized here
 * rather than relying on a base-name file that does not exist.
 *
 * @param {string} phase
 */
function statusIcon (phase) {
  const colour = STATUS_COLOUR[phase] || 'gray'
  const file = path.resolve(path.join(__dirname, '..', 'assets', 'icons', 'status', `${colour}@2x.png`))
  return nativeImage.createFromPath(file).resize({ width: 10, height: 10 })
}

class NodeTray {
  /**
   * @param {object} actions
   * @param {() => void} actions.openDashboard
   * @param {() => void} actions.restartNode
   * @param {() => void} actions.showLogs
   * @param {() => void} actions.showRecoveryPhrase
   * @param {() => void} actions.quit
   */
  constructor (actions) {
    this.actions = actions
    /** @type {Electron.Tray|null} */
    this.tray = null
    this.state = { phase: PHASES.STARTING, detail: '', url: '' }
  }

  start () {
    // The Tray has to stay referenced or it is garbage collected away.
    this.tray = new Tray(trayIcon('off'))
    this.tray.setToolTip(PRODUCT_NAME)
    this.tray.on('double-click', () => this.actions.openDashboard())
    if (!IS_MAC) this.tray.on('click', () => this.tray?.popUpContextMenu())
    nativeTheme.on('updated', () => this.render())
    this.render()
    logger.info('[tray] ready')
  }

  /** @param {{ phase: string, detail: string, url: string }} state */
  update (state) {
    this.state = state
    this.render()
  }

  buildMenu () {
    const { phase } = this.state
    const running = phase === PHASES.READY
    return Menu.buildFromTemplate([
      { label: STATUS_LABEL[phase] || 'Node status unknown', enabled: false, icon: statusIcon(phase) },
      { type: 'separator' },
      { label: 'Open dashboard', enabled: running, click: () => this.actions.openDashboard() },
      { label: 'Restart node', click: () => this.actions.restartNode() },
      { label: 'Show logs', click: () => this.actions.showLogs() },
      { type: 'separator' },
      { label: 'Show recovery phrase', click: () => this.actions.showRecoveryPhrase() },
      {
        label: 'Launch at login',
        type: 'checkbox',
        enabled: autoLaunch.isSupported(),
        checked: autoLaunch.isEnabled(),
        click: (item) => {
          autoLaunch.setEnabled(item.checked).then((value) => {
            item.checked = value
            this.render()
          })
        }
      },
      { type: 'separator' },
      { label: `Version ${VERSION}`, enabled: false },
      { label: 'Quit', click: () => this.actions.quit() }
    ])
  }

  render () {
    if (!this.tray || this.tray.isDestroyed()) return
    this.tray.setImage(trayIcon(this.state.phase === PHASES.READY ? 'on' : 'off'))
    this.tray.setToolTip(`${PRODUCT_NAME} — ${STATUS_LABEL[this.state.phase] || 'unknown'}`)
    this.tray.setContextMenu(this.buildMenu())
  }

  destroy () {
    this.tray?.destroy()
    this.tray = null
  }
}

module.exports = { NodeTray, STATUS_LABEL, trayIcon, statusIcon }
