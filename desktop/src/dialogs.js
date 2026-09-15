// @ts-check
const { dialog } = require('electron')
const { PRODUCT_NAME } = require('./common/consts')

/**
 * @param {object} opts
 * @param {string} opts.title
 * @param {string} [opts.message]
 * @param {'none'|'info'|'error'|'question'|'warning'} [opts.type]
 * @param {string[]} [opts.buttons]
 * @param {Electron.BrowserWindow} [opts.parent]
 * @returns {number} the index of the button the user chose
 */
function showDialog ({ title, message = '', type = 'info', buttons = ['Close'], parent }) {
  const options = {
    type,
    buttons,
    noLink: true,
    defaultId: 0,
    cancelId: 0,
    title: PRODUCT_NAME,
    message: title,
    detail: message
  }
  return parent && !parent.isDestroyed()
    ? dialog.showMessageBoxSync(parent, options)
    : dialog.showMessageBoxSync(options)
}

/**
 * @param {Error|unknown} err
 * @param {string} [title]
 */
function errorDialog (err, title = 'Something went wrong') {
  const message = err instanceof Error ? err.message : String(err)
  return showDialog({ title, message, type: 'error' })
}

module.exports = { showDialog, errorDialog }
