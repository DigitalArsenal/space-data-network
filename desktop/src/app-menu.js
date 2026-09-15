// @ts-check
const { app, Menu, shell } = require('electron')
const logger = require('./common/logger')
const { IS_MAC } = require('./common/consts')

/**
 * @param {object} actions
 * @param {() => void} actions.openDashboard
 * @param {() => void} actions.restartNode
 * @param {() => void} actions.showLogs
 * @param {() => void} actions.showRecoveryPhrase
 */
module.exports = function setupAppMenu (actions) {
  logger.info('[appMenu] init')

  /** @type {Electron.MenuItemConstructorOptions[]} */
  const template = [
    {
      label: 'Node',
      submenu: [
        { label: 'Open dashboard', click: actions.openDashboard },
        { label: 'Restart node', click: actions.restartNode },
        { label: 'Show logs', click: actions.showLogs },
        { type: 'separator' },
        { label: 'Show recovery phrase', click: actions.showRecoveryPhrase },
        { type: 'separator' },
        IS_MAC ? { role: 'close' } : { role: 'quit' }
      ]
    },
    {
      label: 'Edit',
      submenu: [
        { role: 'undo' }, { role: 'redo' }, { type: 'separator' },
        { role: 'cut' }, { role: 'copy' }, { role: 'paste' }, { role: 'selectAll' }
      ]
    },
    {
      label: 'View',
      submenu: [
        { role: 'reload' }, { role: 'forceReload' }, { role: 'toggleDevTools' }, { type: 'separator' },
        { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' },
        { role: 'togglefullscreen' }
      ]
    },
    {
      role: 'window',
      submenu: IS_MAC
        ? [{ role: 'close' }, { role: 'minimize' }, { role: 'zoom' }, { type: 'separator' }, { role: 'front' }]
        : [{ role: 'minimize' }, { role: 'close' }]
    },
    {
      role: 'help',
      submenu: [
        {
          label: 'Space Data Network documentation',
          click: () => { shell.openExternal('https://spacedatanetwork.org/docs/').catch((err) => logger.error(err)) }
        }
      ]
    }
  ]

  if (IS_MAC) {
    template.unshift({
      label: app.name,
      submenu: [
        { role: 'about' }, { type: 'separator' },
        { role: 'services' }, { type: 'separator' },
        { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' },
        { role: 'quit' }
      ]
    })
  }

  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
  logger.info('[appMenu] ready')
}
