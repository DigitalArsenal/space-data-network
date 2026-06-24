const { app, shell } = require('electron')
const path = require('path')
const os = require('os')
const i18n = require('i18next')
const dialog = require('./dialog')

const SDN_DOCS_URL = 'https://spacedatanetwork.org/docs/'
const SDN_ISSUES_URL = 'https://github.com/DigitalArsenal/space-data-network/issues/new'
const SDN_ISSUE_LABELS = 'kind%2Fbug%2C+need%2Ftriage'

const issueTitle = (e) => {
  const es = e.stack ? e.stack.toString() : 'unknown error, no stacktrace'
  const firstLine = es.substr(0, Math.min(es.indexOf('\n'), 72))
  return `[gui error report] ${firstLine}`
}

const issueTemplate = (e) => `<!-- 👉️ Please describe HERE what you were doing when this error happened. -->

- **Desktop**: ${app.getVersion()}
- **OS**: ${os.platform()} ${os.release()} ${os.arch()}
- **Electron**: ${process.versions.electron}
- **Chrome**: ${process.versions.chrome}

\`\`\`
${e.stack}
\`\`\`
`

let hasErrored = false

function generateErrorIssueUrl (e) {
  const body = `${issueTemplate(e)}\n\nSDN docs: ${SDN_DOCS_URL}`
  return `${SDN_ISSUES_URL}?labels=${SDN_ISSUE_LABELS}&template=bug_report.md&title=${encodeURI(issueTitle(e))}&body=${encodeURI(body)}`.substring(0, 1999)
}

/**
 * This will fail and throw another application error if electron hasn't booted up properly.
 * @param {Error} e
 * @returns
 */
function criticalErrorDialog (e) {
  if (hasErrored) return
  hasErrored = true

  const option = dialog({
    title: i18n.t('ipfsDesktopHasShutdownDialog.title'),
    message: i18n.t('ipfsDesktopHasShutdownDialog.message'),
    type: 'error',
    buttons: [
      i18n.t('restartIpfsDesktop'),
      i18n.t('close'),
      i18n.t('reportTheError')
    ]
  })

  if (option === 0) {
    app.relaunch()
  } else if (option === 2) {
    shell.openExternal(generateErrorIssueUrl(e))
  }

  app.exit(1)
}

// Shows a recoverable error dialog with the default title and message.
// Passing an options object alongside the error can be used to override
// the title and message.
function recoverableErrorDialog (e, options) {
  const cfg = {
    title: i18n.t('recoverableErrorDialog.title'),
    message: i18n.t('recoverableErrorDialog.message'),
    type: 'error',
    buttons: [
      i18n.t('close'),
      i18n.t('reportTheError'),
      i18n.t('openLogs')
    ]
  }

  if (options) {
    if (options.title) {
      cfg.title = options.title
    }

    if (options.message) {
      cfg.message = options.message
    }
  }

  const option = dialog(cfg)

  if (option === 1) {
    shell.openExternal(generateErrorIssueUrl(e))
  } else if (option === 2) {
    shell.openPath(path.join(app.getPath('userData'), 'combined.log'))
  }
}

module.exports = Object.freeze({
  criticalErrorDialog,
  recoverableErrorDialog,
  generateErrorIssueUrl
})
