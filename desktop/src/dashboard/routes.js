// @ts-check

const DASHBOARD_ROUTES = Object.freeze({
  '/': '/node',
  '/status': '/node',
  '/files': '/node/data',
  '/peers': '/nodes',
  '/settings': '/node/settings'
})

/**
 * Translate the desktop shell's stable menu destinations to the daemon
 * dashboard's hash router. Unknown hash-style paths are passed through so new
 * daemon screens do not require a desktop release.
 *
 * @param {string} path
 * @returns {string}
 */
function daemonDashboardRoute (path) {
  const normalized = String(path || '/').trim() || '/'
  return DASHBOARD_ROUTES[normalized] || (normalized.startsWith('/') ? normalized : `/${normalized}`)
}

module.exports = { daemonDashboardRoute }
