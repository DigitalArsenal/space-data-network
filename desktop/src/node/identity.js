// @ts-check
//
// "Show recovery phrase" -- the node's own `show-identity --show-mnemonic`,
// rendered in a modal the operator has to dismiss.
//
// The phrase never touches the logger, the preference store, a renderer
// process or a file: it is read from the child's stderr, put in one native
// dialog, and dropped when that dialog closes.
const { execFile } = require('node:child_process')
const { nodeEnv } = require('./paths')
const { showDialog, errorDialog } = require('../dialogs')
const logger = require('../common/logger')

const IDENTITY_TIMEOUT_MS = 120_000

/**
 * Run `show-identity --show-mnemonic` and return its report.
 *
 * @param {{ binary: string, root: string }} bundle
 * @param {string} configPath
 * @returns {Promise<string>} everything the command printed on stderr
 */
function readIdentityReport (bundle, configPath) {
  return new Promise((resolve, reject) => {
    execFile(bundle.binary, ['show-identity', '--show-mnemonic', '--config', configPath], {
      env: nodeEnv(bundle.root),
      timeout: IDENTITY_TIMEOUT_MS,
      maxBuffer: 1 << 20
    }, (err, _stdout, stderr) => {
      if (err) {
        // The failure text can only be the command's own error line: the
        // phrase is printed last and never reached on a failure. Take the
        // final line so nothing earlier rides along.
        const lines = String(stderr).trim().split('\n').filter(Boolean)
        reject(new Error(lines[lines.length - 1] || err.message))
        return
      }
      resolve(String(stderr))
    })
  })
}

/**
 * Split the report into the identity lines and the phrase itself.
 *
 * @param {string} report
 * @returns {{ summary: string, phrase: string }}
 */
function parseIdentityReport (report) {
  const marker = /\*{3} MNEMONIC \(SENSITIVE[^\n]*\n/
  const match = marker.exec(report)
  if (!match) return { summary: cleanSummary(report), phrase: '' }
  const summary = cleanSummary(report.slice(0, match.index))
  const phrase = report.slice(match.index + match[0].length).trim().split('\n')[0].trim()
  return { summary, phrase }
}

/** @param {string} text */
function cleanSummary (text) {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('---') && !line.startsWith('config:'))
    .join('\n')
}

/**
 * @param {object} opts
 * @param {{ binary: string, root: string }|null} opts.bundle
 * @param {string} opts.configPath
 * @param {Electron.BrowserWindow} [opts.parent]
 */
async function showRecoveryPhrase ({ bundle, configPath, parent }) {
  if (!bundle || !configPath) {
    showDialog({
      title: 'The node is not set up yet',
      message: 'Start the node once so it can create its identity, then try again.',
      type: 'info',
      parent
    })
    return
  }
  let report
  try {
    // Deliberately unlogged: the only record of this call is the dialog.
    report = await readIdentityReport(bundle, configPath)
  } catch (err) {
    logger.warn('[identity] could not read the node identity')
    errorDialog(err, 'Could not read the recovery phrase')
    return
  }
  const { summary, phrase } = parseIdentityReport(report)
  if (!phrase) {
    errorDialog(new Error('The node did not return a recovery phrase.'), 'Could not read the recovery phrase')
    return
  }
  showDialog({
    title: 'Recovery phrase',
    type: 'warning',
    buttons: ['Close'],
    parent,
    message: [
      'Anyone with these words controls this node. Write them down and keep them offline.',
      '',
      phrase,
      '',
      summary
    ].join('\n')
  })
}

module.exports = { parseIdentityReport, readIdentityReport, showRecoveryPhrase }
